package main

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNativeListenerTableRequiresExactOwnedLoopback(t *testing.T) {
	// Byte layout from the documented MIB_TCPTABLE_OWNER_PID structure.
	fixture := make([]byte, 28)
	binary.LittleEndian.PutUint32(fixture[:4], 1)
	binary.LittleEndian.PutUint32(fixture[4:8], 2)
	copy(fixture[8:12], []byte{127, 0, 0, 1})
	binary.BigEndian.PutUint16(fixture[12:14], 17575)
	binary.LittleEndian.PutUint32(fixture[24:28], 9843)
	if !tcpListenerTableHasOwner(fixture, 9843, 17575) {
		t.Fatal("documented native listener field layout did not match")
	}
	if tcpListenerTableHasOwner(fixture, 9844, 17575) || tcpListenerTableHasOwner(fixture, 9843, 7575) || tcpListenerTableHasOwner(fixture[:27], 9843, 17575) {
		t.Fatal("foreign PID, different port, or a truncated table was trusted")
	}
	fixture[8] = 0
	fixture[11] = 0
	if tcpListenerTableHasOwner(fixture, 9843, 17575) {
		t.Fatal("an all-interfaces listener was accepted as the owned loopback server")
	}
}

func TestNotificationBaselineAndBackendRestart(t *testing.T) {
	now := time.Now()
	tracker := &eventTracker{}
	oldSMS := notificationEvent{Seq: 1, Type: "sms", Body: "old SMS"}
	ring := notificationEvent{Seq: 2, Type: "incoming_call", CallID: "a", Baseline: true, Timestamp: now.Format(time.RFC3339Nano)}
	got := tracker.consume(eventResponse{InstanceID: "one", Cursor: 2, Events: []notificationEvent{oldSMS, ring}}, now)
	if len(got) != 1 || got[0].Type != "incoming_call" || tracker.cursor != 2 {
		t.Fatalf("baseline replayed historical SMS or lost an active call: %+v", got)
	}
	got = tracker.consume(eventResponse{InstanceID: "one", Cursor: 2, Events: []notificationEvent{ring}, Reset: true}, now)
	if len(got) != 0 {
		t.Fatal("same active call was notified twice on a reset baseline")
	}
	got = tracker.consume(eventResponse{InstanceID: "two", Cursor: 1, Events: []notificationEvent{oldSMS}, Reset: true}, now)
	if len(got) != 0 || tracker.cursor != 1 || tracker.instanceID != "two" {
		t.Fatal("backend restart replayed historical SMS or retained the old cursor")
	}
	got = tracker.consume(eventResponse{InstanceID: "two", Cursor: 2, Events: []notificationEvent{{Seq: 2, Type: "sms"}}}, now)
	if len(got) != 1 || got[0].Target != "sms" {
		t.Fatal("a genuinely new SMS after restart was missed")
	}
}

func TestNotificationCursorAndDisabledConsumption(t *testing.T) {
	tracker := &eventTracker{instanceID: "one", cursor: 5}
	now := time.Now()
	batch := []notificationEvent{{Seq: 4, Type: "sms"}, {Seq: 6, Type: "sms", Target: "https://other-site.invalid"}, {Seq: 8, Type: "sms"}}
	got := tracker.consume(eventResponse{InstanceID: "one", Cursor: 7, Events: batch}, now)
	if len(got) != 1 || got[0].Seq != 6 || got[0].Target != "sms" || tracker.cursor != 7 {
		t.Fatal("cursor boundaries or fixed internal routes were ignored")
	}
	// The host discards this result while disabled, but consume must still advance.
	_ = tracker.consume(eventResponse{InstanceID: "one", Cursor: 8, Events: []notificationEvent{{Seq: 8, Type: "sms"}}}, now)
	got = tracker.consume(eventResponse{InstanceID: "one", Cursor: 8, Events: []notificationEvent{{Seq: 8, Type: "sms"}}}, now)
	if len(got) != 0 {
		t.Fatal("reenabling notifications replayed an event consumed while disabled")
	}
	// If the backend was unavailable while disabled, re-enable starts a fresh
	// baseline rather than requesting that unseen backlog with the old cursor.
	tracker = &eventTracker{}
	got = tracker.consume(eventResponse{InstanceID: "one", Cursor: 11, Events: []notificationEvent{{Seq: 10, Type: "sms"}, {Seq: 11, Type: "sms"}}}, now)
	if len(got) != 0 || tracker.cursor != 11 {
		t.Fatal("fresh re-enable baseline replayed offline-period SMS")
	}
}

func TestEndedAndStaleCallsDoNotDisplayRinging(t *testing.T) {
	now := time.Now()
	tracker := &eventTracker{instanceID: "one"}
	got := tracker.consume(eventResponse{InstanceID: "one", Cursor: 4, Events: []notificationEvent{
		{Seq: 1, Type: "incoming_call", CallID: "ended", Timestamp: now.Format(time.RFC3339Nano)},
		{Seq: 2, Type: "call_ended", CallID: "ended"},
		{Seq: 3, Type: "incoming_call", CallID: "stale", Timestamp: now.Add(-time.Minute).Format(time.RFC3339Nano)},
		{Seq: 4, Type: "missed_call", CallID: "ended"},
	}}, now)
	if len(got) != 2 || got[0].Type != "call_ended" || got[1].Type != "missed_call" {
		t.Fatalf("stale/ended ringing alert survived: %+v", got)
	}
	got = tracker.consume(eventResponse{InstanceID: "one", Cursor: 5, Events: []notificationEvent{{Seq: 5, Type: "missed_call", CallID: "ended"}}}, now)
	if len(got) != 0 {
		t.Fatal("duplicate missed-call event was not suppressed")
	}
}

func TestFetchNotificationsUsesReadOnlyCursorRequests(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/events" {
			t.Fatal("notification poll attempted a mutation or wrong endpoint")
		}
		if requests == 1 && r.URL.RawQuery != "" {
			t.Fatal("first poll requested historical events instead of a baseline")
		}
		if requests == 2 && (r.URL.Query().Get("after") != "9" || r.URL.Query().Get("instance_id") != "owned instance") {
			t.Fatal("subsequent poll lost the cursor/instance boundary")
		}
		_ = json.NewEncoder(w).Encode(eventResponse{InstanceID: "owned instance", Cursor: 9})
	}))
	defer server.Close()
	tracker := &eventTracker{}
	payload, err := fetchEvents(server.Client(), server.URL, tracker)
	if err != nil {
		t.Fatal(err)
	}
	tracker.consume(payload, time.Now())
	if _, err := fetchEvents(server.Client(), server.URL, tracker); err != nil {
		t.Fatal(err)
	}
}

func TestUTF16NotificationLimitsPreserveEmojiAndNUL(t *testing.T) {
	got := notificationUTF16("ab😀xyz", 4)
	if len(got) != 3 || got[0] != 'a' || got[1] != 'b' || got[2] != 0 {
		t.Fatalf("truncated a UTF-16 surrogate pair: %x", got)
	}
	got = notificationUTF16("a\x00b\tc", 64)
	if len(got) != 6 || got[1] != ' ' || got[3] != ' ' || got[5] != 0 {
		t.Fatal("embedded NUL/control characters escaped into the native API")
	}
}
