package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDialRejectsATInjectionBeforeAnyCommand(t *testing.T) {
	for _, number := range []string{"123;AT+CFUN=1,1", "123\r\nATA", "*99#", "+", strings.Repeat("1", 21), "++123", "123,456"} {
		a := &app{callATOverride: func(string, time.Duration) (string, error) {
			t.Fatalf("AT invoked for invalid number %q", number)
			return "", nil
		}}
		r := httptest.NewRecorder()
		a.dialCall(r, httptest.NewRequest("POST", "/api/calls/dial", strings.NewReader(`{"number":`+quoteJSON(number)+`}`)))
		if r.Code != http.StatusBadRequest {
			t.Fatalf("invalid %q status %d", number, r.Code)
		}
	}
}

func TestDialBuildsOnlyVoiceCommandAndDoesNotInventActiveOnOK(t *testing.T) {
	var commands []string
	a := &app{callATOverride: func(command string, _ time.Duration) (string, error) {
		commands = append(commands, command)
		return "OK", nil
	}}
	r := httptest.NewRecorder()
	a.dialCall(r, httptest.NewRequest("POST", "/api/calls/dial", strings.NewReader(`{"number":"+86 (138) 0013-8000"}`)))
	if r.Code != http.StatusOK {
		t.Fatalf("dial status%d: %s", r.Code, r.Body.String())
	}
	if strings.Join(commands, "|") != "AT+CLCC|ATD+8613800138000;|AT+CLCC" {
		t.Fatalf("commands %+v", commands)
	}
	if a.activeCall != nil || !a.callControlVerified {
		t.Fatalf("inventedcall %+v", a.activeCall)
	}
}

func TestAnswerAndHangupCannotTargetLTEDataSession(t *testing.T) {
	for _, action := range []string{"answer", "hangup", "reject"} {
		var commands []string
		a := &app{callATOverride: func(command string, _ time.Duration) (string, error) {
			commands = append(commands, command)
			return "+CLCC: 2,1,0,1,0,\"\",128\r\nOK", nil
		}}
		r := httptest.NewRecorder()
		a.performCallAction(r, httptest.NewRequest("POST", "/api/calls/"+action, strings.NewReader(`{}`)), action)
		if r.Code != http.StatusConflict || strings.Join(commands, "|") != "AT+CLCC" {
			t.Fatalf("%s status%d commands%+v", action, r.Code, commands)
		}
	}
}

func TestHangupDoesNotFallbackToATHAndErrorKeepsCall(t *testing.T) {
	var commands []string
	a := &app{callATOverride: func(command string, _ time.Duration) (string, error) {
		commands = append(commands, command)
		if command == "AT+CLCC" {
			return "+CLCC: 1,1,4,0,0,\"10086\",129\r\nOK", nil
		}
		return "ERROR", nil
	}}
	r := httptest.NewRecorder()
	a.performCallAction(r, httptest.NewRequest("POST", "/api/calls/reject", strings.NewReader(`{}`)), "reject")
	if r.Code != http.StatusBadGateway || strings.Join(commands, "|") != "AT+CLCC|AT+CHUP" {
		t.Fatalf("status%d commands%+v", r.Code, commands)
	}
	if a.activeCall == nil || a.activeCall.Rejected || a.callControlVerified {
		t.Fatalf("failedactionchangedstate %+v", a.activeCall)
	}
}

func TestRejectedRingingCallIsNotMissed(t *testing.T) {
	a := newDemoApp()
	a.applyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "incoming", Number: "10086"}}, time.Now())
	r := httptest.NewRecorder()
	a.performCallAction(r, httptest.NewRequest("POST", "/api/calls/reject", strings.NewReader(`{}`)), "reject")
	if r.Code != http.StatusOK || len(a.callHistory) != 1 || !a.callHistory[0].Rejected || a.callHistory[0].Missed {
		t.Fatalf("status%d history %+v", r.Code, a.callHistory)
	}
	for _, event := range a.events {
		if event.Type == "missed_call" {
			t.Fatal("rejectedcall producedmissednotification")
		}
	}
}

func TestAnswerMarksAnsweredBeforePollingSoNoFalseMissedAfterFastEnd(t *testing.T) {
	poll := 0
	a := &app{callATOverride: func(command string, _ time.Duration) (string, error) {
		if command == "AT+CLCC" {
			poll++
			if poll == 1 {
				return "+CLCC: 1,1,4,0,0,\"10086\",129\r\nOK", nil
			}
		}
		return "OK", nil
	}}
	r := httptest.NewRecorder()
	a.performCallAction(r, httptest.NewRequest("POST", "/api/calls/answer", strings.NewReader(`{}`)), "answer")
	if r.Code != http.StatusOK || len(a.callHistory) != 1 || !a.callHistory[0].Answered || a.callHistory[0].Missed {
		t.Fatalf("status%d history %+v", r.Code, a.callHistory)
	}
}

func TestCallActionRejectsChangedCallIDBeforeMutation(t *testing.T) {
	var commands []string
	a := &app{callATOverride: func(command string, _ time.Duration) (string, error) {
		commands = append(commands, command)
		return "+CLCC: 1,1,4,0,0,\"10086\",129\r\nOK", nil
	}}
	r := httptest.NewRecorder()
	a.performCallAction(r, httptest.NewRequest("POST", "/api/calls/answer", strings.NewReader(`{"call_id":"stale-call"}`)), "answer")
	if r.Code != http.StatusConflict || len(commands) != 1 {
		t.Fatalf("status%d commands %+v", r.Code, commands)
	}
}

func TestVoiceActionsRejectBusyAndErrorEvenIfResponseIncludesOK(t *testing.T) {
	for _, response := range []string{"ERROR", "NO CARRIER\r\nOK", "BUSY\r\nOK", "NO ANSWER\r\nOK", "+CME ERROR: 30\r\nOK"} {
		if voiceActionSucceeded(response) {
			t.Fatalf("false success %q", response)
		}
	}
	if !voiceActionSucceeded("ATD10086;\r\nOK") {
		t.Fatal("validcommandrejected")
	}
}

func quoteJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestExternalOriginCannotPlaceCallsOrSendSMS(t *testing.T) {
	a := newDemoApp()
	for _, route := range []string{"/api/calls/dial", "/api/calls/answer", "/api/calls/hangup", "/api/sms/send", "/api/sms/read"} {
		r := httptest.NewRecorder()
		req := httptest.NewRequest("POST", route, strings.NewReader(`{"number":"10086","phone":"10086","message":"hello","ids":[]}`))
		req.Host = "127.0.0.1:7575"
		req.Header.Set("Origin", "https://example.com")
		a.routes().ServeHTTP(r, req)
		if r.Code != http.StatusForbidden {
			t.Fatalf("external origin %s status%d", route, r.Code)
		}
	}
	if a.activeCall != nil || len(a.sms) != 2 {
		t.Fatal("external website mutated communications")
	}
}

func TestLocalOriginAllowsNativeWebViewDial(t *testing.T) {
	a := newDemoApp()
	r := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/calls/dial", strings.NewReader(`{"number":"10086"}`))
	req.Host = "127.0.0.1:7575"
	req.Header.Set("Origin", "http://127.0.0.1:7575")
	a.routes().ServeHTTP(r, req)
	if r.Code != http.StatusOK || a.activeCall == nil {
		t.Fatalf("local webview status%d %s", r.Code, r.Body.String())
	}
}

func TestMultipleVoiceCallsRejectEveryControlWithoutMutatingAT(t *testing.T) {
	for _, action := range []string{"answer", "reject", "hangup"} {
		var commands []string
		a := &app{callATOverride: func(command string, _ time.Duration) (string, error) {
			commands = append(commands, command)
			return "+CLCC: 1,0,0,0,0,\"10086\",129\r\n+CLCC: 2,1,5,0,0,\"10010\",129\r\nOK", nil
		}}
		r := httptest.NewRecorder()
		a.performCallAction(r, httptest.NewRequest("POST", "/api/calls/"+action, strings.NewReader(`{}`)), action)
		if r.Code != http.StatusConflict || strings.Join(commands, "|") != "AT+CLCC" {
			t.Fatalf("%s multiplecalls status%d commands%+v", action, r.Code, commands)
		}
		if !strings.Contains(r.Body.String(), "暂不支持多通话控制") || len(a.activeCalls) != 2 || len(a.callHistory) != 0 {
			t.Fatalf("%s changedmultiplecallstate %s", action, r.Body.String())
		}
	}
}

func TestMultipleVoiceCallsBlockAdditionalDial(t *testing.T) {
	var commands []string
	a := &app{callATOverride: func(command string, _ time.Duration) (string, error) {
		commands = append(commands, command)
		return "+CLCC: 1,0,0,0,0,\"10086\",129\r\n+CLCC: 2,1,5,0,0,\"10010\",129\r\nOK", nil
	}}
	r := httptest.NewRecorder()
	a.dialCall(r, httptest.NewRequest("POST", "/api/calls/dial", strings.NewReader(`{"number":"10000"}`)))
	if r.Code != http.StatusConflict || strings.Join(commands, "|") != "AT+CLCC" {
		t.Fatalf("multiplecalldial status%d commands%+v", r.Code, commands)
	}
}
