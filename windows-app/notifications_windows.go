//go:build windows

package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const notificationMessage = 0x8002
const notificationTimer = 0x601
const notificationReplaceTimer = 0x602

var notificationsEnabled atomic.Bool
var notificationGeneration atomic.Uint64
var notificationBaselineNeeded atomic.Bool

type notificationBatch struct {
	events     []notificationEvent
	generation uint64
}

var notificationBatches = make(chan notificationBatch, 8)
var pendingNotifications []notificationEvent
var activeNotification *notificationEvent
var replacingNotification bool
var procGetExtendedTCPTable = syscall.NewLazyDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

func ownedListenerReady() bool {
	childMu.Lock()
	if child == nil || stopping.Load() {
		childMu.Unlock()
		return false
	}
	pid := child.pid
	childMu.Unlock()
	var size uint32
	result, _, _ := procGetExtendedTCPTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, 2, 3, 0)
	if result != 122 || size < 4 || size > 1<<20 {
		return false
	}
	for attempts := 0; attempts < 3; attempts++ {
		buffer := make([]byte, size)
		result, _, _ = procGetExtendedTCPTable.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 0, 2, 3, 0)
		if result == 122 && size <= 1<<20 {
			continue
		}
		if result != 0 || size < 4 || int(size) > len(buffer) {
			return false
		}
		if tcpListenerTableHasOwner(buffer[:size], pid, 17575) {
			childMu.Lock()
			stillOwned := child != nil && child.pid == pid && !stopping.Load()
			childMu.Unlock()
			return stillOwned
		}
		return false
	}
	return false
}

type preferences struct {
	NotificationsEnabled *bool `json:"notifications_enabled,omitempty"`
}

func loadPreferences() {
	notificationsEnabled.Store(true)
	data, err := os.ReadFile(filepath.Join(dataDir, "settings.json"))
	if err != nil {
		return
	}
	var settings preferences
	if err := json.Unmarshal(data, &settings); err == nil && settings.NotificationsEnabled != nil {
		notificationsEnabled.Store(*settings.NotificationsEnabled)
	}
}

func toggleNotifications() {
	enabled := !notificationsEnabled.Load()
	data, _ := json.MarshalIndent(preferences{NotificationsEnabled: &enabled}, "", "  ")
	name := filepath.Join(dataDir, "settings.json")
	if err := os.WriteFile(name+".tmp", data, 0600); err != nil {
		message("无法保存通知设置，详情见日志。")
		log.Printf("save notification preference: %v", err)
		return
	}
	if err := os.Rename(name+".tmp", name); err != nil {
		message("无法保存通知设置，详情见日志。")
		log.Printf("replace notification preference: %v", err)
		return
	}
	notificationsEnabled.Store(enabled)
	notificationGeneration.Add(1)
	if enabled {
		// The backend may have been unavailable while notifications were off.
		// A fresh baseline skips that unseen history when it becomes available.
		notificationBaselineNeeded.Store(true)
	}
	if !enabled {
		pendingNotifications = nil
		activeNotification = nil
		hideNativeNotification()
	}
}

// Poll the owned local backend only. Network errors preserve the last cursor;
// the backend's instance ID prevents old SMS from replaying after a restart.
func monitorNotifications() {
	tracker := &eventTracker{}
	lastErrorLog := time.Time{}
	for !stopping.Load() {
		if !ownedListenerReady() {
			if !waitUnlessStopping(2 * time.Second) {
				return
			}
			continue
		}
		generation := notificationGeneration.Load()
		if notificationBaselineNeeded.Swap(false) {
			tracker = &eventTracker{}
		}
		response, err := fetchEvents(client, localURL, tracker)
		if err == nil && ownedListenerReady() {
			batch := tracker.consume(response, time.Now())
			if len(batch) > 0 && notificationsEnabled.Load() && generation == notificationGeneration.Load() {
				select {
				case notificationBatches <- notificationBatch{events: batch, generation: generation}:
					procPostMessage.Call(hwnd, notificationMessage, 0, 0)
				default:
					log.Println("native notification queue is busy")
				}
			}
		} else if err != nil && time.Since(lastErrorLog) > time.Minute {
			// Do not record SMS bodies or phone numbers in the host log.
			log.Printf("event poll unavailable: %v", err)
			lastErrorLog = time.Now()
		}
		if !waitUnlessStopping(2 * time.Second) {
			return
		}
	}
}

func hideNativeNotification() {
	n := notifyData{Size: uint32(unsafe.Sizeof(notifyData{})), Hwnd: hwnd, ID: 1, Flags: 0x10}
	call(shell32, "Shell_NotifyIconW", 1, uintptr(unsafe.Pointer(&n)))
	call(user32, "KillTimer", hwnd, notificationTimer)
}

func waitForNotificationReplacement() {
	activeNotification = nil
	replacingNotification = true
	hideNativeNotification()
	// Empty-info hides the old banner asynchronously. Let its callback drain
	// before opening a fresh call banner so a stale hide cannot dismiss it.
	call(user32, "SetTimer", hwnd, notificationReplaceTimer, 500, 0)
}

func receiveNotifications() {
	for {
		select {
		case batch := <-notificationBatches:
			if !notificationsEnabled.Load() || batch.generation != notificationGeneration.Load() {
				continue
			}
			for _, event := range batch.events {
				if event.Type == "call_ended" {
					filtered := pendingNotifications[:0]
					for _, pending := range pendingNotifications {
						if pending.Type != "incoming_call" || pending.CallID != event.CallID {
							filtered = append(filtered, pending)
						}
					}
					pendingNotifications = filtered
					if activeNotification != nil && activeNotification.Type == "incoming_call" && activeNotification.CallID == event.CallID {
						waitForNotificationReplacement()
					}
					continue
				}
				if event.Type == "incoming_call" {
					pendingNotifications = append([]notificationEvent{event}, pendingNotifications...)
					if activeNotification != nil {
						waitForNotificationReplacement()
					}
				} else if len(pendingNotifications) < 32 {
					pendingNotifications = append(pendingNotifications, event)
				}
			}
		default:
			showNextNotification()
			return
		}
	}
}

func showNextNotification() {
	if activeNotification != nil || replacingNotification || !notificationsEnabled.Load() || len(pendingNotifications) == 0 {
		return
	}
	event := pendingNotifications[0]
	pendingNotifications = pendingNotifications[1:]
	activeNotification = &event
	flags := uint32(0x10) // NIF_INFO
	if event.Type == "incoming_call" {
		flags |= 0x40 // NIF_REALTIME: never queue a ringing banner in the shell.
	}
	n := notifyData{Size: uint32(unsafe.Sizeof(notifyData{})), Hwnd: hwnd, ID: 1, Flags: flags, InfoFlags: 1 | 0x80}
	copy(n.InfoTitle[:], notificationUTF16(productName+" · "+event.Title, len(n.InfoTitle)))
	copy(n.Info[:], notificationUTF16(event.Body, len(n.Info)))
	if call(shell32, "Shell_NotifyIconW", 1, uintptr(unsafe.Pointer(&n))) == 0 {
		log.Println("Shell_NotifyIcon native notification failed")
	}
	// Also drain a banner if Explorer never supplies its terminal callback.
	call(user32, "SetTimer", hwnd, notificationTimer, 60000, 0)
}

func notificationFinished(clicked bool) {
	if replacingNotification {
		return
	}
	if clicked && activeNotification != nil {
		shellOpen(localURL + "/?view=" + activeNotification.Target)
	}
	activeNotification = nil
	call(user32, "KillTimer", hwnd, notificationTimer)
	showNextNotification()
}

func handleNotificationTimer(timer uintptr) bool {
	switch timer {
	case notificationReplaceTimer:
		call(user32, "KillTimer", hwnd, notificationReplaceTimer)
		replacingNotification = false
		showNextNotification()
		return true
	case notificationTimer:
		waitForNotificationReplacement()
		return true
	}
	return false
}
