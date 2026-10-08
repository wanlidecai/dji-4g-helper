package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type callRecord struct {
	ID        string     `json:"id"`
	Index     int        `json:"index"`
	Direction string     `json:"direction"`
	State     string     `json:"state"`
	Number    string     `json:"number,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Missed    bool       `json:"missed"`
	Answered  bool       `json:"answered"`
	Rejected  bool       `json:"rejected"`
	Notified  bool       `json:"-"`
}

type parsedCall struct {
	Index     int
	Direction string
	State     string
	Number    string
}

func parseCLCC(response string) []parsedCall {
	out := make([]parsedCall, 0)
	seen := make(map[int]bool)
	for _, line := range strings.Split(strings.ReplaceAll(response, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "+CLCC:") {
			continue
		}
		reader := csv.NewReader(strings.NewReader(strings.TrimSpace(strings.TrimPrefix(line, "+CLCC:"))))
		reader.TrimLeadingSpace = true
		fields, err := reader.Read()
		if err != nil || len(fields) < 5 {
			continue
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		index, err := strconv.Atoi(fields[0])
		// Only mode 0 is voice. The sample also reports active LTE data
		// sessions in CLCC mode 1; hanging those up would disconnect internet.
		if err != nil || index <= 0 || seen[index] || fields[3] != "0" ||
			(fields[1] != "0" && fields[1] != "1") {
			continue
		}
		state := mapCallState(fields[2])
		if state == "unknown" {
			continue
		}
		call := parsedCall{Index: index, Direction: mapCallDirection(fields[1]), State: state}
		if len(fields) >= 6 {
			call.Number = notificationText(fields[5], 80)
		}
		out = append(out, call)
		seen[index] = true
	}
	return out
}

func mapCallDirection(raw string) string {
	if raw == "1" {
		return "incoming"
	}
	return "outgoing"
}

func mapCallState(raw string) string {
	switch raw {
	case "0":
		return "active"
	case "1":
		return "held"
	case "2":
		return "dialing"
	case "3":
		return "alerting"
	case "4":
		return "incoming"
	case "5":
		return "waiting"
	default:
		return "unknown"
	}
}

func callIsRinging(state string) bool { return state == "incoming" || state == "waiting" }

func (a *app) callCommand(command string, timeout time.Duration) (string, error) {
	if a.callATOverride != nil {
		return a.callATOverride(command, timeout)
	}
	return a.runATCommand(command, timeout)
}

func (a *app) startCallPoller(ctx context.Context) {
	interval := a.callPollInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := a.pollCallOnce(); err != nil {
				log.Printf("call poll unavailable: %v", err)
			}
			timer.Reset(interval)
		}
	}
}

func (a *app) pollCallOnce() error {
	a.callPollMu.Lock()
	defer a.callPollMu.Unlock()
	if a.demo {
		a.callMu.Lock()
		a.callProbeComplete, a.callControlSupported = true, true
		a.callMu.Unlock()
		a.setCallPollStatus(nil)
		return nil
	}
	if a.callATOverride == nil && a.modem == nil && a.currentUSBDevice() == nil {
		err := fmt.Errorf("未连接大疆 4G 模块")
		a.setCallPollStatus(err)
		return err
	}
	// CLCC is read-only and already contains the caller number. Do not
	// configure CLIP, audio, IMS or USB modes while monitoring notifications.
	response, err := a.callCommand("AT+CLCC", 3*time.Second)
	if err == nil && !callQuerySucceeded(response) {
		err = fmt.Errorf("模块未接受通话状态查询")
	}
	if err != nil {
		a.setCallPollStatus(err)
		return err
	}
	a.callMu.Lock()
	a.callProbeComplete, a.callControlSupported = true, true
	a.callMu.Unlock()
	a.applyCallPoll(parseCLCC(response), time.Now())
	a.setCallPollStatus(nil)
	return nil
}

func (a *app) applyCallPoll(calls []parsedCall, now time.Time) {
	a.callMu.Lock()
	if a.activeCalls == nil {
		a.activeCalls = make(map[int]*callRecord)
	}
	present := make(map[int]bool, len(calls))
	var notify []callRecord
	var events []communicationEvent
	for _, current := range calls {
		if present[current.Index] {
			continue
		}
		present[current.Index] = true
		record := a.activeCalls[current.Index]
		if record != nil && (record.Direction != current.Direction ||
			(record.Number != "" && current.Number != "" && record.Number != current.Number)) {
			events = append(events, a.archiveCallLocked(record, now)...)
			record = nil
		}
		if record == nil {
			record = &callRecord{ID: randomCommunicationID(), Index: current.Index,
				Direction: current.Direction, StartedAt: now}
			a.activeCalls[current.Index] = record
		}
		record.State, record.UpdatedAt = current.State, now
		if current.Number != "" {
			record.Number = current.Number
		}
		if current.State == "active" || current.State == "held" {
			record.Answered = true
		}
		if current.Direction == "incoming" && callIsRinging(current.State) && !record.Notified {
			record.Notified = true
			notify = append(notify, *record)
			events = append(events, callCommunicationEvent(*record, "incoming_call", now))
		}
	}
	for index, record := range a.activeCalls {
		if !present[index] {
			events = append(events, a.archiveCallLocked(record, now)...)
			delete(a.activeCalls, index)
		}
	}
	a.selectActiveCallLocked()
	a.callMu.Unlock()
	if len(events) > 0 {
		a.persistCommunicationStore()
	}
	for _, event := range events {
		a.publishCommunicationEvent(event)
	}
	if a.callNotifier != nil {
		for _, record := range notify {
			a.callNotifier(record)
		}
	}
}

func (a *app) archiveCallLocked(record *callRecord, now time.Time) []communicationEvent {
	ended := now
	record.EndedAt, record.UpdatedAt = &ended, now
	record.Missed = record.Direction == "incoming" && !record.Answered && !record.Rejected
	record.State = "ended"
	a.callHistory = append([]callRecord{*record}, a.callHistory...)
	if len(a.callHistory) > 100 {
		a.callHistory = a.callHistory[:100]
	}
	events := []communicationEvent{callCommunicationEvent(*record, "call_ended", now)}
	if record.Missed {
		events = append(events, callCommunicationEvent(*record, "missed_call", now))
	}
	return events
}

func (a *app) selectActiveCallLocked() {
	a.activeCall = nil
	for _, record := range a.activeCalls {
		if a.activeCall == nil || callStatePriority(record.State) > callStatePriority(a.activeCall.State) ||
			(callStatePriority(record.State) == callStatePriority(a.activeCall.State) && record.Index < a.activeCall.Index) {
			a.activeCall = record
		}
	}
}

func callStatePriority(state string) int {
	switch state {
	case "incoming", "waiting":
		return 5
	case "active":
		return 4
	case "alerting":
		return 3
	case "dialing":
		return 2
	case "held":
		return 1
	default:
		return 0
	}
}

func (a *app) setCallPollStatus(err error) {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	a.callLastPoll = time.Now()
	if err != nil {
		a.callLastPollError = err.Error()
		return
	}
	a.callLastPollError = ""
}

func (a *app) callStatus(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		_ = a.pollCallOnce()
	}
	a.callMu.RLock()
	defer a.callMu.RUnlock()
	var active *callRecord
	if a.activeCall != nil {
		copy := *a.activeCall
		active = &copy
	}
	history := append([]callRecord{}, a.callHistory...)
	activeCalls := make([]callRecord, 0, len(a.activeCalls))
	for _, record := range a.activeCalls {
		activeCalls = append(activeCalls, *record)
	}
	available := a.callControlSupported && a.callLastPollError == "" &&
		(a.demo || time.Since(a.callLastPoll) < 15*time.Second)
	writeJSON(w, http.StatusOK, map[string]any{
		"active": active, "active_calls": activeCalls, "history": history,
		"polling": !a.demo, "poll_interval_s": int(a.callPollInterval.Seconds()),
		"last_poll": a.callLastPoll, "last_poll_error": a.callLastPollError,
		"capabilities": map[string]any{
			"probe_complete":         a.callProbeComplete,
			"call_control_supported": a.callControlSupported,
			"call_control_verified":  a.callControlVerified,
			"dial_available":         available && len(activeCalls) == 0,
			"answer_available":       available && len(activeCalls) == 1 && active != nil && callIsRinging(active.State),
			"hangup_available":       available && len(activeCalls) == 1 && active != nil,
			"audio_available":        false,
			"audio_note":             "本版已提供通话控制；电脑麦克风和扬声器的通话音频尚未实现。模块音频接口和匹配的语音运行时需单独配置。",
		},
	})
}

func normalizeDialNumber(input string) (string, bool) {
	var out strings.Builder
	for _, r := range strings.TrimSpace(input) {
		switch {
		case r >= '0' && r <= '9':
			out.WriteRune(r)
		case r == '+' && out.Len() == 0:
			out.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return "", false
		}
	}
	number := out.String()
	digits := strings.TrimPrefix(number, "+")
	if len(digits) == 0 || len(digits) > 20 {
		return "", false
	}
	return number, true
}

func (a *app) dialCall(w http.ResponseWriter, r *http.Request) {
	if !localCommunicationMutation(w, r) {
		return
	}
	var body struct {
		Number string `json:"number"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	number, valid := normalizeDialNumber(body.Number)
	if !valid {
		writeError(w, http.StatusBadRequest, "请输入有效电话号码（仅数字及开头的 +）")
		return
	}
	a.callActionMu.Lock()
	defer a.callActionMu.Unlock()
	if err := a.pollCallOnce(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	a.callMu.RLock()
	busy := len(a.activeCalls) > 0
	a.callMu.RUnlock()
	if busy {
		writeError(w, http.StatusConflict, "已有语音通话，请结束后再拨号")
		return
	}
	if a.demo {
		a.applyCallPoll([]parsedCall{{Index: 1, Direction: "outgoing", State: "dialing", Number: number}}, time.Now())
	} else {
		response, err := a.callCommand("ATD"+number+";", 10*time.Second)
		if err == nil && !voiceActionSucceeded(response) {
			err = fmt.Errorf("拨号未成功，模块或运营商未接受请求")
		}
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		// Query actual state rather than inventing a connected call from OK.
		_ = a.pollCallOnce()
	}
	a.setCallControlVerified()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": "dial", "number": number, "audio_available": false})
}

func (a *app) answerCall(w http.ResponseWriter, r *http.Request) { a.performCallAction(w, r, "answer") }
func (a *app) hangupCall(w http.ResponseWriter, r *http.Request) { a.performCallAction(w, r, "hangup") }
func (a *app) rejectCall(w http.ResponseWriter, r *http.Request) { a.performCallAction(w, r, "reject") }

func (a *app) performCallAction(w http.ResponseWriter, r *http.Request, action string) {
	if !localCommunicationMutation(w, r) {
		return
	}
	var body struct {
		CallID string `json:"call_id"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if !decodeJSON(w, r, &body) {
			return
		}
	}
	a.callActionMu.Lock()
	defer a.callActionMu.Unlock()
	if err := a.pollCallOnce(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	a.callMu.RLock()
	multipleCalls := len(a.activeCalls) > 1
	var selected *callRecord
	if a.activeCall != nil {
		copy := *a.activeCall
		selected = &copy
	}
	a.callMu.RUnlock()
	if multipleCalls {
		writeError(w, http.StatusConflict, "暂不支持多通话控制；当前存在多个语音通话，请先使用其他设备结束多余通话")
		return
	}
	if selected == nil {
		writeError(w, http.StatusConflict, "当前没有语音通话")
		return
	}
	if body.CallID != "" && body.CallID != selected.ID {
		writeError(w, http.StatusConflict, "通话状态已变化，请重新操作")
		return
	}
	if (action == "answer" || action == "reject") && !callIsRinging(selected.State) {
		writeError(w, http.StatusConflict, "当前没有待接听的来电")
		return
	}
	command := "AT+CHUP"
	if action == "answer" {
		command = "ATA"
	}
	if !a.demo {
		response, err := a.callCommand(command, 5*time.Second)
		if err == nil && !voiceActionSucceeded(response) {
			err = fmt.Errorf("模块未接受通话操作")
		}
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	a.callMu.Lock()
	if record := a.activeCalls[selected.Index]; record != nil && record.ID == selected.ID {
		if action == "answer" {
			record.Answered = true
		}
		if action == "reject" || (action == "hangup" && callIsRinging(record.State)) {
			record.Rejected = true
		}
	}
	a.callMu.Unlock()
	if a.demo {
		if action == "answer" {
			a.applyCallPoll([]parsedCall{{Index: selected.Index, Direction: selected.Direction, State: "active", Number: selected.Number}}, time.Now())
		} else {
			a.applyCallPoll(nil, time.Now())
		}
	} else {
		_ = a.pollCallOnce()
	}
	a.setCallControlVerified()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": action, "rejected": action == "reject", "audio_available": false})
}

func callQuerySucceeded(response string) bool {
	if !atCommandSucceeded(response) {
		return false
	}
	for _, line := range strings.Split(strings.ReplaceAll(response, "\r", ""), "\n") {
		upper := strings.ToUpper(strings.TrimSpace(line))
		if upper == "ERROR" || strings.HasPrefix(upper, "+CME ERROR:") || strings.HasPrefix(upper, "+CMS ERROR:") {
			return false
		}
	}
	return true
}

func voiceActionSucceeded(response string) bool {
	if !callQuerySucceeded(response) {
		return false
	}
	for _, line := range strings.Split(strings.ReplaceAll(response, "\r", ""), "\n") {
		switch strings.ToUpper(strings.TrimSpace(line)) {
		case "BUSY", "NO CARRIER", "NO ANSWER", "NO DIALTONE", "NO DIAL TONE":
			return false
		}
	}
	return true
}

func (a *app) setCallControlVerified() {
	a.callMu.Lock()
	a.callControlVerified = true
	a.callMu.Unlock()
}
