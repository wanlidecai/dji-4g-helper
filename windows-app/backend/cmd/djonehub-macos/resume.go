package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func (a *app) rememberUSBNetMode(mode string) {
	value, err := strconv.Atoi(mode)
	if err != nil || value < 0 || value > 3 {
		return
	}
	a.usbNetModeMu.Lock()
	a.lastUSBNetMode, a.usbNetModeKnown = value, true
	a.usbNetModeMu.Unlock()
}

func (a *app) knownUSBNetMode() (int, bool) {
	a.usbNetModeMu.Lock()
	defer a.usbNetModeMu.Unlock()
	return a.lastUSBNetMode, a.usbNetModeKnown
}

func (a *app) observeUSBNetCommand(command, response string) {
	command = strings.ToUpper(strings.TrimSpace(command))
	if command == `AT+QCFG="USBNET"` {
		if atCommandSucceeded(response) {
			a.rememberUSBNetMode(parseUSBNetMode(response))
		}
		return
	}
	if strings.HasPrefix(command, `AT+QCFG="USBNET",`) && atCommandSucceeded(response) {
		a.rememberUSBNetMode(strings.TrimSpace(strings.TrimPrefix(command, `AT+QCFG="USBNET",`)))
	}
}

// The native tray application reports power-resume notifications. Windows
// sometimes emits more than one notification for one physical wake-up.
func (a *app) resumeSystem(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "demo": true})
		return
	}
	a.resumeMu.Lock()
	if a.resumeInProgress || (!a.lastResume.IsZero() && time.Since(a.lastResume) < 45*time.Second) {
		a.resumeMu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "coalesced": true})
		return
	}
	a.resumeInProgress, a.lastResume = true, time.Now()
	a.resumeMu.Unlock()
	go func() {
		defer func() {
			a.resumeMu.Lock()
			a.resumeInProgress = false
			a.resumeMu.Unlock()
		}()
		if err := a.recoverCellularAfterResume(); err != nil {
			log.Printf("wake network recovery: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

func nativeInternetUSBMode() int {
	if runtime.GOOS == "windows" {
		return 3 // RNDIS. Windows does not provide the macOS ECM driver.
	}
	return 1
}

func isExpectedUSBRebootDisconnect(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToUpper(err.Error())
	return strings.Contains(text, "NO_DEVICE") || strings.Contains(text, "NOT_FOUND") ||
		strings.Contains(text, "TIMED OUT") || strings.Contains(text, "EOF")
}

func (a *app) resumeInternetAllowed() (bool, error) {
	policy, err := a.cellularPolicyStatus()
	if err != nil {
		return false, err
	}
	if policy.ForceOff {
		return false, nil
	}
	mode, known := a.knownUSBNetMode()
	if !known {
		response, err := a.runATCommand(`AT+QCFG="usbnet"`, 3*time.Second)
		if err != nil {
			return false, err
		}
		a.rememberUSBNetMode(parseUSBNetMode(response))
		mode, known = a.knownUSBNetMode()
	}
	return known && mode == nativeInternetUSBMode(), nil
}

type connectivityState uint8

const (
	connectivityInconclusive connectivityState = iota
	connectivityOffline
	connectivityOnline
)

// Only a verified missing link/route or explicit socket network-unreachable
// error or repeated typed TCP-connect timeout establishes offline state. DNS,
// TLS, later HTTP timeouts, and inventory failures remain inconclusive.
func probeCellularInternet() connectivityState {
	var name string
	var source net.IP
	if runtime.GOOS == "windows" {
		var state connectivityState
		name, source, state = windowsCellularProbeInterface()
		if state != connectivityOnline {
			return state
		}
	} else {
		interfaces := discoverMacNetworkInterfaces()
		name = selectUSBTrafficInterface(interfaces, macDefaultRoute{})
		for _, item := range interfaces {
			if item.Name == name {
				source = net.ParseIP(item.IPv4)
				break
			}
		}
		if source == nil {
			return connectivityInconclusive
		}
	}
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: source}, Timeout: 3 * time.Second}
	if runtime.GOOS == "windows" {
		if err := bindWindowsCellularProbe(dialer, name); err != nil {
			return connectivityInconclusive
		}
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", address)
		},
		TLSHandshakeTimeout: 3 * time.Second,
		DisableKeepAlives:   true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	allOffline := true
	for _, target := range []string{"https://www.baidu.com/", "https://www.qq.com/"} {
		req, err := http.NewRequest(http.MethodHead, target, nil)
		if err != nil {
			allOffline = false
			continue
		}
		response, err := client.Do(req)
		if err == nil {
			response.Body.Close()
			// Even an HTTP 4xx/5xx proves this bound TLS connection works.
			return connectivityOnline
		}
		if !definitelyOfflineNetworkError(err) {
			allOffline = false
		}
	}
	if allOffline {
		return connectivityOffline
	}
	return connectivityInconclusive
}

func (a *app) recoverCellularAfterResume() error {
	return a.recoverCellularAfterResumeWithProbe(probeCellularInternet)
}

func (a *app) recoverCellularAfterResumeWithProbe(probe func() connectivityState) error {
	marker, err := consumeWakeRecoveryMarker()
	if err != nil {
		return err
	}
	if marker != nil {
		if marker.OriginalMode != nativeInternetUSBMode() {
			return nil
		}
		if _, known := a.knownUSBNetMode(); !known {
			a.rememberUSBNetMode(strconv.Itoa(marker.OriginalMode))
		}
	}
	allowed, err := a.resumeInternetAllowed()
	if err != nil || !allowed {
		return err
	}
	// A single successful probe ends recovery immediately. Every sample must
	// establish offline state before a disruptive action can be considered.
	for attempt := 0; attempt < 3; attempt++ {
		switch probe() {
		case connectivityOnline:
			log.Printf("wake network check: 4G remains available; no changes needed")
			return nil
		case connectivityInconclusive:
			log.Printf("wake network check: result inconclusive; preserving connection")
			return nil
		}
		if attempt < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	allowed, err = a.resumeInternetAllowed()
	if err != nil || !allowed {
		return err
	}
	// A consumed reconnect-only marker records that CFUN was already sent.
	// This stage may reattach the serial backend and renew an existing lease,
	// but it must never set usbnet or reset the module for a second time.
	if marker != nil && marker.Action == wakeReconnectOnly {
		mode, err := a.readHardwareUSBNetMode()
		if err != nil {
			return err
		}
		if mode != nativeInternetUSBMode() {
			return nil
		}
		return a.restoreCellularLeaseAfterResume(probe)
	}

	mode, modeErr := a.readHardwareUSBNetMode()
	if modeErr != nil && a.modem != nil && runtime.GOOS == "windows" {
		// Manager.Stop permanently closes its queues. Rebuild the owned backend
		// process instead of trying to reuse the stale serial Manager.
		if marker != nil {
			return modeErr
		}
		if probe() != connectivityOffline {
			return nil
		}
		allowed, err := a.resumeInternetAllowed()
		if err != nil || !allowed {
			return err
		}
		return a.requestWakeBackendRestart(wakeRestore, marker)
	}
	if a.modem == nil {
		if modeErr != nil {
			a.markUSBATDetached("system resumed with confirmed unavailable cellular internet")
			time.Sleep(2 * time.Second)
			if err := a.ensureUSBAT(); err != nil {
				return err
			}
			mode, modeErr = a.readHardwareUSBNetMode()
		}
	}
	if modeErr != nil {
		return modeErr
	}
	if mode != nativeInternetUSBMode() {
		return nil
	}
	// Recheck both connectivity and user intent immediately before writing AT.
	if probe() != connectivityOffline {
		return nil
	}
	allowed, err = a.resumeInternetAllowed()
	if err != nil || !allowed {
		return err
	}
	log.Printf("wake network check: 4G confirmed offline; restoring USB mode %d once", nativeInternetUSBMode())
	response, err := a.runATCommand(fmt.Sprintf(`AT+QCFG="usbnet",%d`, nativeInternetUSBMode()), 8*time.Second)
	if err != nil {
		return err
	}
	if !atCommandSucceeded(response) {
		return fmt.Errorf("恢复 USB 上网模式失败: %s", response)
	}
	response, err = a.runATCommand("AT+CFUN=1,1", 3*time.Second)
	if err != nil {
		if !isExpectedUSBRebootDisconnect(err) {
			return err
		}
	} else if !atCommandSucceeded(response) {
		return fmt.Errorf("模块重启未确认: %s", response)
	}
	if a.modem != nil && runtime.GOOS == "windows" {
		return a.requestWakeBackendRestart(wakeReconnectOnly, marker)
	}
	if a.modem == nil {
		a.markUSBATDetached("USB network recovery reboot")
		a.setUSBATBackoff(12 * time.Second)
	}
	time.Sleep(12 * time.Second)
	return a.restoreCellularLeaseAfterResume(probe)
}

func (a *app) readHardwareUSBNetMode() (int, error) {
	response, err := a.runATCommand(`AT+QCFG="usbnet"`, 3*time.Second)
	if err != nil {
		return -1, err
	}
	mode, err := strconv.Atoi(parseUSBNetMode(response))
	if err != nil || !atCommandSucceeded(response) {
		return -1, errors.New("无法确认硬件当前USB模式，保持现状")
	}
	return mode, nil
}

func (a *app) restoreCellularLeaseAfterResume(probe func() connectivityState) error {
	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	renewed := make(map[string]bool)
	for time.Now().Before(deadline) {
		allowed, err := a.resumeInternetAllowed()
		if err != nil || !allowed {
			return err
		}
		state := probe()
		if state == connectivityOnline {
			log.Printf("wake network recovery: cellular internet restored")
			return nil
		}
		if state == connectivityInconclusive {
			return nil
		}
		if a.modem == nil {
			if err := a.ensureUSBAT(); err != nil {
				lastErr = err
			}
		}
		services, err := discoverMacNetworkServices()
		if err != nil {
			return err
		}
		for _, service := range services {
			if !isDJICellularService(service) || service.Disabled || renewed[service.Name] {
				continue
			}
			a.networkPolicyMu.Lock()
			if a.force4GOff {
				a.networkPolicyMu.Unlock()
				return nil
			}
			var didRenew bool
			if runtime.GOOS == "windows" {
				didRenew, err = renewWindowsNetworkServiceAfterResume(service.Name)
			}
			a.networkPolicyMu.Unlock()
			if err != nil {
				lastErr = err
			} else {
				renewed[service.Name] = true
			}
			if didRenew && probe() == connectivityOnline {
				return nil
			}
		}
		time.Sleep(3 * time.Second)
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("唤醒后 DJI 网卡仍未取得可用网络，请检查 RNDIS 驱动和 SIM 状态")
}
