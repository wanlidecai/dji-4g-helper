package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const communicationEventLimit = 256

type communicationEvent struct {
	ID        string    `json:"id"`
	Seq       uint64    `json:"seq"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Number    string    `json:"number,omitempty"`
	Target    string    `json:"target"`
	Timestamp time.Time `json:"timestamp"`
	CallID    string    `json:"call_id,omitempty"`
	CallState string    `json:"call_state,omitempty"`
	Baseline  bool      `json:"baseline,omitempty"`
}

func randomCommunicationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

func normalizeSMS(item *receivedSMS) {
	if item.Direction == "" {
		item.Direction = "incoming"
	}
	if item.Code == "" {
		item.Code = extractSMSCode(item.Content)
	}
	if item.ID == "" {
		digest := sha256.Sum256([]byte(smsCacheKey(*item)))
		item.ID = hex.EncodeToString(digest[:16])
	}
}

// Event IDs and sequence numbers belong to one process instance. Clients must
// establish a new baseline on instance change rather than replay an old inbox.
func (a *app) publishCommunicationEvent(event communicationEvent) {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	if a.eventInstance == "" {
		a.eventInstance = randomCommunicationID()
	}
	a.eventCursor++
	event.Seq = a.eventCursor
	if event.ID == "" {
		event.ID = fmt.Sprintf("%s-%d", a.eventInstance, event.Seq)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	a.events = append(a.events, event)
	if len(a.events) > communicationEventLimit {
		a.events = append([]communicationEvent(nil), a.events[len(a.events)-communicationEventLimit:]...)
	}
}

func notificationText(s string, limit int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s))
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit]) + "…"
}

func (a *app) publishSMSEvent(item receivedSMS) {
	number := notificationText(item.Sender, 60)
	title := "新短信"
	if number != "" {
		title = "来自 " + number + " 的短信"
	}
	a.publishCommunicationEvent(communicationEvent{
		ID: "sms-" + item.ID, Type: "sms", Title: title,
		Body: notificationText(item.Content, 160), Number: number,
		Target: "sms", Timestamp: time.Now(),
	})
}

func callCommunicationEvent(record callRecord, kind string, now time.Time) communicationEvent {
	number := notificationText(record.Number, 60)
	body := number
	if body == "" {
		body = "未知号码"
	}
	title := "来电"
	if kind == "missed_call" {
		title = "未接来电"
	}
	if kind == "call_ended" {
		title = "通话已结束"
	}
	return communicationEvent{
		ID: record.ID + "-" + kind, Type: kind, Title: title, Body: body,
		Number: number, Target: "calls", Timestamp: now,
		CallID: record.ID, CallState: record.State,
	}
}

func (a *app) listCommunicationEvents(w http.ResponseWriter, r *http.Request) {
	afterText, hasAfter := r.URL.Query()["after"]
	var after uint64
	if hasAfter {
		var err error
		if len(afterText) != 1 || afterText[0] == "" {
			writeError(w, http.StatusBadRequest, "after must be a nonnegative cursor")
			return
		}
		after, err = strconv.ParseUint(afterText[0], 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "after must be a nonnegative cursor")
			return
		}
	}
	a.eventMu.Lock()
	if a.eventInstance == "" {
		a.eventInstance = randomCommunicationID()
	}
	instance, cursor := a.eventInstance, a.eventCursor
	reset := !hasAfter || r.URL.Query().Get("instance_id") != instance || after > cursor
	if !reset && len(a.events) > 0 && after < a.events[0].Seq-1 {
		reset = true
	}
	events := make([]communicationEvent, 0)
	if !reset {
		for _, event := range a.events {
			if event.Seq > after {
				events = append(events, event)
			}
		}
	}
	a.eventMu.Unlock()
	// A call already ringing when the tray host starts is actionable. Only
	// this live snapshot crosses the baseline; historical SMS never does.
	if reset {
		a.callMu.RLock()
		for _, record := range a.activeCalls {
			if record.Direction == "incoming" && callIsRinging(record.State) {
				event := callCommunicationEvent(*record, "incoming_call", time.Now())
				event.Seq, event.Baseline = cursor, true
				events = append(events, event)
			}
		}
		a.callMu.RUnlock()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"instance_id": instance, "cursor": cursor, "events": events, "reset": reset,
	})
}

func (a *app) markSMSRead(w http.ResponseWriter, r *http.Request) {
	if !localCommunicationMutation(w, r) {
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 || len(body.IDs) > 500 {
		writeError(w, http.StatusBadRequest, "ids must contain 1 to 500 message IDs")
		return
	}
	wanted := make(map[string]bool, len(body.IDs))
	for _, id := range body.IDs {
		if len(id) != 32 {
			writeError(w, http.StatusBadRequest, "invalid message ID")
			return
		}
		if _, err := hex.DecodeString(id); err != nil {
			writeError(w, http.StatusBadRequest, "invalid message ID")
			return
		}
		wanted[id] = true
	}
	a.smsMu.Lock()
	marked := 0
	for i := range a.sms {
		normalizeSMS(&a.sms[i])
		if wanted[a.sms[i].ID] && !a.sms[i].Read {
			a.sms[i].Read = true
			marked++
		}
	}
	a.smsMu.Unlock()
	if marked > 0 {
		a.persistCommunicationStore()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "marked": marked})
}

type communicationStore struct {
	Version int           `json:"version"`
	SMS     []receivedSMS `json:"sms"`
	Calls   []callRecord  `json:"calls"`
}

func (a *app) initializeCommunicationStore() {
	a.communicationStoreMu.Lock()
	defer a.communicationStoreMu.Unlock()
	if a.communicationStoreInitialized {
		return
	}
	a.communicationStoreInitialized = true
	if a.communicationStorePath == "" {
		if a.demo {
			return
		}
		dir, err := os.UserConfigDir()
		if err != nil {
			a.communicationStoreSaveError = err.Error()
			return
		}
		a.communicationStorePath = filepath.Join(dir, "DJOneHub", "communications.json")
	}
	file, err := os.Open(a.communicationStorePath)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		a.communicationStoreLoadFailed = true
		a.communicationStoreSaveError = err.Error()
		log.Printf("communication cache unavailable: %v", err)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		a.communicationStoreLoadFailed = true
		a.communicationStoreSaveError = "communication cache could not be read within size limit"
		log.Print(a.communicationStoreSaveError)
		return
	}
	var store communicationStore
	if err = json.Unmarshal(data, &store); err != nil {
		// The old running backend only exposed a plain SMS array. Accept its
		// local snapshot once so an app update does not lose cached messages.
		if err = json.Unmarshal(data, &store.SMS); err != nil {
			a.communicationStoreLoadFailed = true
			a.communicationStoreSaveError = "communication cache is not valid JSON"
			log.Print(a.communicationStoreSaveError)
			return
		}
		store.Version = 1
	}
	if store.Version != 1 {
		a.communicationStoreLoadFailed = true
		a.communicationStoreSaveError = "unsupported communication cache version"
		log.Print(a.communicationStoreSaveError)
		return
	}
	if len(store.SMS) > 500 {
		store.SMS = store.SMS[:500]
	}
	if len(store.Calls) > 100 {
		store.Calls = store.Calls[:100]
	}
	a.smsMu.Lock()
	a.smsKnown = make(map[string]bool, len(store.SMS))
	a.smsKnownOrder = nil
	for i := range store.SMS {
		normalizeSMS(&store.SMS[i])
		key := smsCacheKey(store.SMS[i])
		a.smsKnown[key] = true
		a.smsKnownOrder = append(a.smsKnownOrder, key)
	}
	a.sms = store.SMS
	a.smsMu.Unlock()
	a.callMu.Lock()
	a.callHistory = store.Calls
	a.callMu.Unlock()
}

func (a *app) persistCommunicationStore() {
	a.communicationStoreMu.Lock()
	defer a.communicationStoreMu.Unlock()
	if !a.communicationStoreInitialized || a.communicationStorePath == "" {
		return
	}
	// Preserve any existing cache that could not be loaded for repair.
	if a.communicationStoreLoadFailed {
		return
	}
	a.smsMu.RLock()
	sms := append([]receivedSMS{}, a.sms...)
	a.smsMu.RUnlock()
	a.callMu.RLock()
	calls := append([]callRecord{}, a.callHistory...)
	a.callMu.RUnlock()
	data, err := json.MarshalIndent(communicationStore{Version: 1, SMS: sms, Calls: calls}, "", "  ")
	if err == nil {
		err = atomicWritePrivate(a.communicationStorePath, data)
	}
	if err != nil {
		a.communicationStoreSaveError = err.Error()
		log.Printf("communication cache save failed: %v", err)
		return
	}
	a.communicationStoreSaveError = ""
}

func (a *app) communicationStoreHealthy() bool {
	a.communicationStoreMu.Lock()
	defer a.communicationStoreMu.Unlock()
	return a.communicationStoreInitialized && a.communicationStorePath != "" && a.communicationStoreSaveError == ""
}

func atomicWritePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".communications-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Browsers cannot use another website to place a call or send an SMS through
// this loopback service. Native loopback clients do not send an Origin header.
func localCommunicationMutation(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err == nil && u.Scheme == "http" && u.Host == r.Host &&
		(u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1") {
		return true
	}
	writeError(w, http.StatusForbidden, "只能从本机应用执行通话和短信操作")
	return false
}
