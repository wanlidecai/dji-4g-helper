package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	utf16encoding "unicode/utf16"
)

type notificationEvent struct {
	ID        string `json:"id"`
	Seq       uint64 `json:"seq"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Number    string `json:"number"`
	Target    string `json:"target"`
	Timestamp string `json:"timestamp"`
	CallID    string `json:"call_id"`
	CallState string `json:"call_state"`
	Baseline  bool   `json:"baseline"`
}

type eventResponse struct {
	InstanceID string              `json:"instance_id"`
	Cursor     uint64              `json:"cursor"`
	Events     []notificationEvent `json:"events"`
	Reset      bool                `json:"reset"`
}

type eventTracker struct {
	instanceID string
	cursor     uint64
	seenCalls  map[string]bool
}

func fetchEvents(httpClient *http.Client, base string, tracker *eventTracker) (eventResponse, error) {
	endpoint := base + "/api/events"
	if tracker.instanceID != "" {
		query := url.Values{"after": {strconv.FormatUint(tracker.cursor, 10)}, "instance_id": {tracker.instanceID}}
		endpoint += "?" + query.Encode()
	}
	response, err := httpClient.Get(endpoint)
	if err != nil {
		return eventResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return eventResponse{}, fmt.Errorf("event endpoint HTTP %d", response.StatusCode)
	}
	var payload eventResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return eventResponse{}, err
	}
	if payload.InstanceID == "" || len(payload.Events) > 256 {
		return eventResponse{}, fmt.Errorf("invalid event stream metadata")
	}
	return payload, nil
}

// First launch and backend restart only consume a current ringing snapshot.
// Disabled notifications still advance the cursor, preventing old alerts on re-enable.
func (tracker *eventTracker) consume(response eventResponse, now time.Time) []notificationEvent {
	fresh := tracker.instanceID == "" || tracker.instanceID != response.InstanceID || response.Reset
	if tracker.seenCalls == nil {
		tracker.seenCalls = make(map[string]bool)
	}
	if tracker.instanceID != response.InstanceID {
		tracker.seenCalls = make(map[string]bool)
	}
	oldCursor := tracker.cursor
	tracker.instanceID = response.InstanceID
	if fresh || response.Cursor > tracker.cursor {
		tracker.cursor = response.Cursor
	}
	ended := make(map[string]bool)
	for _, event := range response.Events {
		if event.Type == "call_ended" && event.CallID != "" {
			ended[event.CallID] = true
		}
	}
	var accepted []notificationEvent
	for _, event := range response.Events {
		if !fresh && (event.Seq <= oldCursor || event.Seq > response.Cursor) {
			continue
		}
		if fresh && event.Type != "incoming_call" {
			continue
		}
		switch event.Type {
		case "sms":
			event.Target = "sms"
		case "incoming_call":
			if ended[event.CallID] {
				continue
			}
			if !fresh && !event.Baseline {
				created, err := time.Parse(time.RFC3339Nano, event.Timestamp)
				if err != nil || now.Sub(created) > 30*time.Second {
					continue
				}
			}
			event.Target = "calls"
		case "missed_call", "call_ended":
			event.Target = "calls"
		default:
			continue
		}
		if event.CallID != "" && event.Type != "call_ended" {
			key := event.Type + ":" + event.CallID
			if tracker.seenCalls[key] {
				continue
			}
			tracker.seenCalls[key] = true
			if len(tracker.seenCalls) > 512 {
				tracker.seenCalls = map[string]bool{key: true}
			}
		}
		accepted = append(accepted, event)
	}
	return accepted
}

// Windows strings are UTF-16 with a terminating NUL. Never split a surrogate
// pair at the shell's 63/255-code-unit limits or let embedded NUL hide text.
func notificationUTF16(text string, capacity int) []uint16 {
	text = strings.Map(func(r rune) rune {
		if r == 0 || (r < 32 && r != '\n') {
			return ' '
		}
		return r
	}, text)
	units := utf16encoding.Encode([]rune(text))
	if len(units) >= capacity {
		units = units[:capacity-1]
		if len(units) > 0 && units[len(units)-1] >= 0xd800 && units[len(units)-1] <= 0xdbff {
			units = units[:len(units)-1]
		}
	}
	return append(units, 0)
}
