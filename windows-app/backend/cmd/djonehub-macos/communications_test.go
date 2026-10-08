package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type eventResponse struct {
	Instance string               `json:"instance_id"`
	Cursor   uint64               `json:"cursor"`
	Events   []communicationEvent `json:"events"`
	Reset    bool                 `json:"reset"`
}

func getEvents(t *testing.T, a *app, query string) eventResponse {
	t.Helper()
	r := httptest.NewRecorder()
	a.listCommunicationEvents(r, httptest.NewRequest("GET", "/api/events"+query, nil))
	if r.Code != http.StatusOK {
		t.Fatalf("events status %d: %s", r.Code, r.Body.String())
	}
	var response eventResponse
	if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestSMSInitialScanQuietThenDeduplicatedIncomingNotification(t *testing.T) {
	a := &app{}
	baseline := getEvents(t, a, "")
	now := time.Now()
	old := receivedSMS{Sender: "10086", Content: "old inbox", Timestamp: now.Add(-time.Hour)}
	a.mergeSMS([]receivedSMS{old})
	a.completeSMSBaseline()
	query := fmt.Sprintf("?after=%d&instance_id=%s", baseline.Cursor, baseline.Instance)
	if got := getEvents(t, a, query); len(got.Events) != 0 {
		t.Fatalf("old inbox notified: %+v", got.Events)
	}
	fresh := receivedSMS{Sender: "10010", Content: "new SMS 482913", Timestamp: now}
	a.mergeSMS([]receivedSMS{old, fresh})
	a.mergeSMS([]receivedSMS{fresh})
	got := getEvents(t, a, query)
	if len(got.Events) != 1 || got.Events[0].Type != "sms" || got.Events[0].Number != "10010" {
		t.Fatalf("events %+v", got.Events)
	}
	if next := getEvents(t, a, fmt.Sprintf("?after=%d&instance_id=%s", got.Cursor, got.Instance)); len(next.Events) != 0 {
		t.Fatalf("events replayed: %+v", next.Events)
	}
}

func TestEventsResetNeverReplaysSMSAndKeepsLiveRing(t *testing.T) {
	a := &app{}
	a.completeSMSBaseline()
	a.recordSMS("10086", "hello", time.Now())
	a.applyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "incoming", Number: "10010"}}, time.Now())
	got := getEvents(t, a, "?after=0&instance_id=old-process")
	if !got.Reset || len(got.Events) != 1 || got.Events[0].Type != "incoming_call" || !got.Events[0].Baseline {
		t.Fatalf("baseline %+v", got)
	}
	if got.Events[0].CallID == "" {
		t.Fatal("live ringing has no stable call ID")
	}
}

func TestCommunicationEventsBoundedRetentionAndExpiredCursorReset(t *testing.T) {
	a := &app{}
	base := getEvents(t, a, "")
	for i := 0; i < communicationEventLimit+10; i++ {
		a.publishCommunicationEvent(communicationEvent{Type: "sms", Body: fmt.Sprint(i)})
	}
	if len(a.events) != communicationEventLimit {
		t.Fatalf("events count %d", len(a.events))
	}
	got := getEvents(t, a, fmt.Sprintf("?after=0&instance_id=%s", base.Instance))
	if !got.Reset || len(got.Events) != 0 {
		t.Fatalf("expiredcursor %+v", got)
	}
	r := httptest.NewRecorder()
	a.listCommunicationEvents(r, httptest.NewRequest("GET", "/api/events?after=-1", nil))
	if r.Code != http.StatusBadRequest {
		t.Fatalf("invalidcursor status %d", r.Code)
	}
}

func TestIncomingAndMissedCallEventsExactlyOnce(t *testing.T) {
	a := &app{}
	now := time.Now()
	ring := []parsedCall{{Index: 1, Direction: "incoming", State: "incoming", Number: "10086"}}
	a.applyCallPoll(ring, now)
	a.applyCallPoll(ring, now.Add(time.Second))
	a.applyCallPoll(nil, now.Add(2*time.Second))
	a.applyCallPoll(nil, now.Add(3*time.Second))
	if len(a.events) != 3 || a.events[0].Type != "incoming_call" || a.events[1].Type != "call_ended" || a.events[2].Type != "missed_call" {
		t.Fatalf("events %+v", a.events)
	}
	if a.events[0].CallID != a.events[2].CallID {
		t.Fatal("call IDs changed across lifecycle")
	}
}

func TestMultipleVoiceCallsDoNotLoseActiveHistoryWhenWaitingTakesPriority(t *testing.T) {
	a := &app{}
	now := time.Now()
	active := parsedCall{Index: 1, Direction: "outgoing", State: "active", Number: "10086"}
	a.applyCallPoll([]parsedCall{active}, now)
	originalID := a.activeCall.ID
	waiting := parsedCall{Index: 2, Direction: "incoming", State: "waiting", Number: "10010"}
	a.applyCallPoll([]parsedCall{active, waiting}, now.Add(time.Second))
	if a.activeCall.Index != 2 || len(a.activeCalls) != 2 || len(a.callHistory) != 0 {
		t.Fatalf("call selection %+v", a.activeCall)
	}
	a.applyCallPoll([]parsedCall{active}, now.Add(2*time.Second))
	if a.activeCall.ID != originalID || len(a.callHistory) != 1 || !a.callHistory[0].Missed {
		t.Fatalf("active %+v history %+v", a.activeCall, a.callHistory)
	}
	a.applyCallPoll(nil, now.Add(3*time.Second))
	if len(a.callHistory) != 2 || a.callHistory[0].Missed {
		t.Fatalf("history %+v", a.callHistory)
	}
}

func TestSMSStableIDsReadAndDurableMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "communications.json")
	old := []receivedSMS{{Sender: "10086", Content: "existing SMS", Timestamp: time.Now()}}
	data, _ := json.Marshal(old)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	a := &app{communicationStorePath: path}
	a.initializeCommunicationStore()
	if len(a.sms) != 1 || a.sms[0].ID == "" || a.sms[0].Direction != "incoming" {
		t.Fatalf("migration %+v", a.sms)
	}
	if len(a.events) != 0 {
		t.Fatal("migration notified")
	}
	id := a.sms[0].ID
	r := httptest.NewRecorder()
	a.markSMSRead(r, httptest.NewRequest("POST", "/api/sms/read", strings.NewReader(`{"ids":["`+id+`"]}`)))
	if r.Code != http.StatusOK || !a.sms[0].Read {
		t.Fatalf("read status%d %s", r.Code, r.Body.String())
	}
	b := &app{communicationStorePath: path}
	b.initializeCommunicationStore()
	if len(b.sms) != 1 || b.sms[0].ID != id || !b.sms[0].Read {
		t.Fatalf("reloaded %+v", b.sms)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("cache perms %o", st.Mode().Perm())
	}
	b.completeSMSBaseline()
	b.mergeSMS(old)
	if len(b.events) != 0 || len(b.sms) != 1 {
		t.Fatal("migrated inbox duplicated")
	}
}

func TestBadCacheIsPreservedAndPreventsModuleCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "communications.json")
	bad := []byte("not valid JSON")
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	a := &app{communicationStorePath: path}
	a.initializeCommunicationStore()
	a.recordSMS("10086", "new message", time.Now())
	if a.communicationStoreHealthy() {
		t.Fatal("bad cache permitted cleanup")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(bad) {
		t.Fatal("bad original cache replaced")
	}
}

func TestDemoEventInjectionUnavailableInProduction(t *testing.T) {
	a := &app{}
	r := httptest.NewRecorder()
	a.demoCommunicationEvent(r, httptest.NewRequest("POST", "/api/demo/events", strings.NewReader(`{"type":"incoming_call"}`)))
	if r.Code != 404 || len(a.events) != 0 || len(a.activeCalls) != 0 {
		t.Fatalf("production demo injection status%d", r.Code)
	}
}

func TestAutomaticSMSCleanupIndicesAreExactAndUnique(t *testing.T) {
	if got := parseUSBATSMSIndex(`+CMGL: 7,0,,38`); got != 7 {
		t.Fatalf("index%d", got)
	}
	for _, header := range []string{`+CMGR: 7`, `+CMGL: 7;AT+CFUN=1,1`, `+CMGL: -1,0,,38`, `+CMGL: 999999999,0,,38`} {
		if got := parseUSBATSMSIndex(header); got != 0 {
			t.Fatalf("unsafeindex %q -> %d", header, got)
		}
	}
	got := uniqueSMSIndices([]int{7, 7, 2, -1, 100001, 3})
	if fmt.Sprint(got) != "[2 3 7]" {
		t.Fatalf("cleanupindices %+v", got)
	}
}
