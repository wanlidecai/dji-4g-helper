package main

import (
	"testing"
	"time"
)

func TestParseCLCCIncomingCall(t *testing.T) {
	got := parseCLCC("AT+CLCC\r\n+CLCC: 1,1,4,0,0,\"13800138000\",129\r\nOK")
	if len(got) != 1 {
		t.Fatalf("parseCLCC() len=%d, want 1", len(got))
	}
	if got[0].Index != 1 || got[0].Direction != "incoming" ||
		got[0].State != "incoming" || got[0].Number != "13800138000" {
		t.Fatalf("parseCLCC()=%+v", got[0])
	}
}

func TestParseCLCCNoCall(t *testing.T) {
	if got := parseCLCC("AT+CLCC\r\nOK"); len(got) != 0 {
		t.Fatalf("parseCLCC()=%+v, want empty", got)
	}
}

func TestParseCLCCIgnoresDataSession(t *testing.T) {
	response := "AT+CLCC\r\n+CLCC: 2,1,0,1,0,\"\",128\r\nOK"
	if got := parseCLCC(response); len(got) != 0 {
		t.Fatalf("parseCLCC()=%+v, want data session ignored", got)
	}
}

func TestCallLifecycleMarksMissed(t *testing.T) {
	a := &app{callPollInterval: 3 * time.Second, callNotifier: func(callRecord) {}}
	started := time.Date(2026, 7, 26, 10, 0, 0, 0, time.Local)
	a.applyCallPoll([]parsedCall{{
		Index: 1, Direction: "incoming", State: "incoming", Number: "10086",
	}}, started)
	a.applyCallPoll(nil, started.Add(8*time.Second))

	if a.activeCall != nil {
		t.Fatal("active call was not cleared")
	}
	if len(a.callHistory) != 1 || !a.callHistory[0].Missed {
		t.Fatalf("history=%+v, want one missed call", a.callHistory)
	}
}

func TestAnsweredCallIsNotMissed(t *testing.T) {
	a := &app{callPollInterval: 3 * time.Second, callNotifier: func(callRecord) {}}
	started := time.Date(2026, 7, 26, 10, 0, 0, 0, time.Local)
	a.applyCallPoll([]parsedCall{{
		Index: 1, Direction: "incoming", State: "incoming", Number: "10086",
	}}, started)
	a.applyCallPoll([]parsedCall{{
		Index: 1, Direction: "incoming", State: "active", Number: "10086",
	}}, started.Add(3*time.Second))
	a.applyCallPoll(nil, started.Add(8*time.Second))

	if len(a.callHistory) != 1 || a.callHistory[0].Missed {
		t.Fatalf("history=%+v, want answered call", a.callHistory)
	}
}
