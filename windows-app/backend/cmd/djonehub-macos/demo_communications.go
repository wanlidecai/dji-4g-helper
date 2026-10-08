package main

import (
	"net/http"
	"time"
)

// Only demo mode exposes synthetic events. The production device service never
// accepts injected calls or SMS, and these endpoints issue no AT commands.
func (a *app) demoCommunicationEvent(w http.ResponseWriter, r *http.Request) {
	if !a.demo {
		http.NotFound(w, r)
		return
	}
	if !localCommunicationMutation(w, r) {
		return
	}
	var body struct {
		Type    string `json:"type"`
		Number  string `json:"number"`
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	now := time.Now()
	if body.Number == "" {
		body.Number = "10086"
	}
	switch body.Type {
	case "sms":
		a.completeSMSBaseline()
		if body.Content == "" {
			body.Content = "这是碗里的菜的演示短信通知。"
		}
		a.recordSMS(body.Number, body.Content, now)
	case "incoming_call":
		a.applyCallPoll([]parsedCall{{Index: 1, Direction: "incoming", State: "incoming", Number: body.Number}}, now)
	case "call_ended", "missed_call":
		a.applyCallPoll(nil, now)
	default:
		writeError(w, http.StatusBadRequest, "demo type must be sms, incoming_call, or call_ended")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "demo": true})
}
