package main

import (
	"errors"
	"time"
)

func (a *app) currentUSBAT() *usbAT {
	a.usbATMu.Lock()
	defer a.usbATMu.Unlock()
	return a.usbAT
}

func (a *app) setUSBATBackoff(delay time.Duration) {
	a.usbATMu.Lock()
	a.usbATBackoffUntil = time.Now().Add(delay)
	a.usbATMu.Unlock()
}

func (a *app) usbATCommand(command string, timeout time.Duration) (string, error) {
	device := a.currentUSBAT()
	if device == nil {
		return "", errors.New("USB AT device is unavailable during reconnect")
	}
	return device.Command(command, timeout)
}

func (a *app) usbATCommandWithPrompt(command string, payload []byte, timeout time.Duration) (string, error) {
	device := a.currentUSBAT()
	if device == nil {
		return "", errors.New("USB AT device is unavailable during reconnect")
	}
	return device.CommandWithPrompt(command, payload, timeout)
}
