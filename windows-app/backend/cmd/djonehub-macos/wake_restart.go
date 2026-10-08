package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	wakeRestore       = "restore"
	wakeReconnectOnly = "reconnect-only"
	wakeRestartExit   = 75
)

type wakeRecoveryMarker struct {
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	OriginalMode  int       `json:"original_mode"`
	Action        string    `json:"action"`
	Port          string    `json:"port"`
	RestartCount  int       `json:"restart_count"`
	ManualRebuild bool      `json:"manual_rebuild,omitempty"`
}

func wakeRecoveryPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "DJOneHub", "wake-recovery.json"), nil
}

func validWakeRecoveryMarker(marker *wakeRecoveryMarker, now time.Time) bool {
	return marker != nil && marker.Version == 1 &&
		(marker.OriginalMode == 3 || (marker.OriginalMode == 0 && marker.ManualRebuild && marker.Action == wakeReconnectOnly)) &&
		(marker.Action == wakeRestore || marker.Action == wakeReconnectOnly) &&
		marker.RestartCount >= 1 && marker.RestartCount <= 2 &&
		!marker.CreatedAt.After(now.Add(10*time.Second)) && now.Sub(marker.CreatedAt) < 5*time.Minute
}

func readWakeRecoveryMarker(consume bool) (*wakeRecoveryMarker, error) {
	if runtime.GOOS != "windows" {
		return nil, nil
	}
	path, err := wakeRecoveryPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var marker wakeRecoveryMarker
	valid := json.Unmarshal(data, &marker) == nil && validWakeRecoveryMarker(&marker, time.Now())
	if consume || !valid {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if !valid {
		return nil, nil
	}
	return &marker, nil
}

func consumeWakeRecoveryMarker() (*wakeRecoveryMarker, error) {
	return readWakeRecoveryMarker(true)
}

func (a *app) requestWakeBackendRestart(action string, previous *wakeRecoveryMarker) error {
	mode, known := a.knownUSBNetMode()
	policy, err := a.cellularPolicyStatus()
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return nil
	}
	if action == wakeReconnectOnly {
		// CFUN was already submitted in mode 3. Rebuild its invalid COM handle
		// even if the user disables internet now; the fresh process rechecks
		// both ForceOff and the actual mode before considering lease renewal.
		mode, known = 3, true
	} else if !known || mode != 3 || policy.ForceOff {
		return nil
	}
	count := 1
	if previous != nil {
		count = previous.RestartCount + 1
	}
	if count > 2 {
		return errors.New("达到本次唤醒串口重建次数上限，保持现状")
	}
	marker := &wakeRecoveryMarker{Version: 1, CreatedAt: time.Now(), OriginalMode: mode, Action: action, Port: a.port, RestartCount: count}
	if !validWakeRecoveryMarker(marker, time.Now()) {
		return errors.New("无效的唤醒重建请求")
	}
	return writeWakeRestartMarker(marker)
}

func (a *app) requestManualBackendRestart(mode int) error {
	if runtime.GOOS != "windows" || (mode != 0 && mode != 3) {
		return errors.New("无法确认手动重启前的模块模式")
	}
	marker := &wakeRecoveryMarker{Version: 1, CreatedAt: time.Now(), OriginalMode: mode, Action: wakeReconnectOnly, Port: a.port, RestartCount: 1, ManualRebuild: true}
	return writeWakeRestartMarker(marker)
}

func writeWakeRestartMarker(marker *wakeRecoveryMarker) error {
	if !validWakeRecoveryMarker(marker, time.Now()) {
		return errors.New("无效的串口重建marker")
	}
	path, err := wakeRecoveryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	log.Printf("wake recovery: requesting owned backend rebuild, action=%s restart=%d exit=%d", marker.Action, marker.RestartCount, wakeRestartExit)
	// The launcher owns this process and its Job Object. Exit closes the stale
	// COM handle and lets a fresh Manager recreate its permanently closed queues.
	os.Exit(wakeRestartExit)
	return nil
}

func discoverStartupATPort() (string, error) {
	marker, err := readWakeRecoveryMarker(false)
	if err != nil || marker == nil {
		return discoverATPort()
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		port, err := discoverATPort()
		if err == nil {
			return port, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("唤醒重建后仍未发现 DJI AT 串口: %w", err)
		}
		time.Sleep(2 * time.Second)
	}
}
