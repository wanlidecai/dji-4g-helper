package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestResumePreservesUserDisabledInternet(t *testing.T) {
	instance := &app{networkPolicyLoaded: true, force4GOff: true, port: "existing USB connection"}
	instance.rememberUSBNetMode("3")
	if err := instance.recoverCellularAfterResume(); err != nil {
		t.Fatal(err)
	}
	if instance.port != "existing USB connection" || !instance.force4GOff {
		t.Fatal("wake changed the user's disabled-internet policy or USB connection")
	}
}

func TestResumePreservesSMSOnlyMode(t *testing.T) {
	instance := &app{networkPolicyLoaded: true, port: "existing USB connection"}
	instance.rememberUSBNetMode("0")
	if err := instance.recoverCellularAfterResume(); err != nil {
		t.Fatal(err)
	}
	mode, known := instance.knownUSBNetMode()
	if !known || mode != 0 || instance.port != "existing USB connection" {
		t.Fatal("wake changed SMS-only mode or attempted a USB reconnection")
	}
}

func TestResumeLeavesHealthyInternetConnectionUntouched(t *testing.T) {
	instance := &app{networkPolicyLoaded: true, port: "existing USB connection"}
	instance.rememberUSBNetMode(strconv.Itoa(nativeInternetUSBMode()))
	checks := 0
	if err := instance.recoverCellularAfterResumeWithProbe(func() connectivityState {
		checks++
		return connectivityOnline
	}); err != nil {
		t.Fatal(err)
	}
	mode, known := instance.knownUSBNetMode()
	if checks != 1 || instance.port != "existing USB connection" || !known || mode != nativeInternetUSBMode() || !instance.usbATBackoffUntil.IsZero() {
		t.Fatal("wake changed a healthy connection or failed to stop on the first successful probe")
	}
}

func TestResumePreservesConnectionWhenProbeIsInconclusive(t *testing.T) {
	instance := &app{networkPolicyLoaded: true, port: "existing USB connection"}
	instance.rememberUSBNetMode(strconv.Itoa(nativeInternetUSBMode()))
	if err := instance.recoverCellularAfterResumeWithProbe(func() connectivityState { return connectivityInconclusive }); err != nil {
		t.Fatal(err)
	}
	if instance.port != "existing USB connection" || !instance.usbATBackoffUntil.IsZero() {
		t.Fatal("an inconclusive wake probe attempted to reset the connection")
	}
}

func TestResumeStopsWhenLaterProbeSucceeds(t *testing.T) {
	instance := &app{networkPolicyLoaded: true, port: "existing USB connection"}
	instance.rememberUSBNetMode(strconv.Itoa(nativeInternetUSBMode()))
	checks := 0
	if err := instance.recoverCellularAfterResumeWithProbe(func() connectivityState {
		checks++
		if checks == 1 {
			return connectivityOffline
		}
		return connectivityOnline
	}); err != nil {
		t.Fatal(err)
	}
	if checks != 2 || instance.port != "existing USB connection" || !instance.usbATBackoffUntil.IsZero() {
		t.Fatal("a successful later probe did not preserve the existing connection")
	}
}

func TestProbeErrorsDoNotAuthorizeNetworkReset(t *testing.T) {
	for _, err := range []error{
		&net.DNSError{Err: "DNS lookup failed", Name: "example.invalid"},
		&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "DNS lookup failed"}},
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")},
		errors.New("TLS certificate verification failed"),
		errors.New("PowerShell inventory unavailable"),
	} {
		if definitelyOfflineNetworkError(err) {
			t.Fatalf("inconclusive error was treated as offline: %v", err)
		}
	}
}

type testTCPConnectTimeout struct{}

func (testTCPConnectTimeout) Error() string   { return "TCP connection timed out" }
func (testTCPConnectTimeout) Timeout() bool   { return true }
func (testTCPConnectTimeout) Temporary() bool { return true }

func TestBoundTCPConnectTimeoutCanEstablishOffline(t *testing.T) {
	if !definitelyOfflineNetworkError(&net.OpError{Op: "dial", Net: "tcp", Err: testTCPConnectTimeout{}}) {
		t.Fatal("a typed TCP-connect timeout did not establish offline state")
	}
	if definitelyOfflineNetworkError(&net.OpError{Op: "read", Net: "tcp", Err: testTCPConnectTimeout{}}) {
		t.Fatal("a later read/TLS timeout authorized a module reset")
	}
	if definitelyOfflineNetworkError(&net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "DNS timeout", IsTimeout: true}}) {
		t.Fatal("a DNS timeout authorized a module reset")
	}
}

func TestWakeMarkerLimitsRestoreToOneModuleReset(t *testing.T) {
	now := time.Now()
	marker := &wakeRecoveryMarker{Version: 1, CreatedAt: now, OriginalMode: 3, Action: wakeRestore, RestartCount: 1}
	if !validWakeRecoveryMarker(marker, now) {
		t.Fatal("first serial rebuild marker rejected")
	}
	marker.Action, marker.RestartCount = wakeReconnectOnly, 2
	if !validWakeRecoveryMarker(marker, now) {
		t.Fatal("post-CFUN reconnect-only marker rejected")
	}
	marker.RestartCount = 3
	if validWakeRecoveryMarker(marker, now) {
		t.Fatal("unbounded repeated rebuild marker accepted")
	}
	marker.RestartCount, marker.CreatedAt = 2, now.Add(-6*time.Minute)
	if validWakeRecoveryMarker(marker, now) {
		t.Fatal("stale wake marker accepted")
	}
	marker.CreatedAt, marker.OriginalMode = now, 0
	if validWakeRecoveryMarker(marker, now) {
		t.Fatal("SMS-only mode accepted as an internet restoration marker")
	}
	marker.ManualRebuild = true
	if !validWakeRecoveryMarker(marker, now) {
		t.Fatal("manual SMS COM rebuild marker rejected")
	}
	marker.Action = wakeRestore
	if validWakeRecoveryMarker(marker, now) {
		t.Fatal("manual SMS COM rebuild marker authorized an internet reset")
	}
}

func TestATCommandsDuringReconnectFailCleanly(t *testing.T) {
	instance := &app{}
	if _, err := instance.usbATCommand("AT", time.Second); err == nil {
		t.Fatal("an AT command should fail cleanly while the USB device is detached")
	}
	if _, err := instance.usbATCommandWithPrompt("AT+CMGS=1", []byte("00"), time.Second); err == nil {
		t.Fatal("an SMS command should fail cleanly while the USB device is detached")
	}
}

func TestRejectedATModeChangeDoesNotChangeWakePolicy(t *testing.T) {
	instance := &app{}
	instance.observeUSBNetCommand(`AT+QCFG="usbnet",0`, "OK")
	instance.observeUSBNetCommand(`AT+QCFG="usbnet",3`, "ERROR")
	instance.observeUSBNetCommand(`AT+QCFG="usbnet"`, "ERROR")
	instance.observeUSBNetCommand(`AT+QCFG="usbnet"`, `+QCFG: "usbnet",3`)
	instance.observeUSBNetCommand(`AT+QCFG="usbnet"`, "+QCFG: \"usbnet\",3\r\nERROR")
	mode, known := instance.knownUSBNetMode()
	if !known || mode != 0 {
		t.Fatal("a rejected AT command changed the remembered operating mode")
	}
}

func TestDuplicateResumeNotificationsAreCoalesced(t *testing.T) {
	for _, instance := range []*app{
		{resumeInProgress: true},
		{lastResume: time.Now()},
	} {
		response := httptest.NewRecorder()
		instance.routes().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/system/resume", nil))
		if response.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want accepted", response.Code)
		}
		var body struct {
			Coalesced bool `json:"coalesced"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.Coalesced {
			t.Fatalf("duplicate wake was not coalesced: %s", response.Body.String())
		}
	}
}
