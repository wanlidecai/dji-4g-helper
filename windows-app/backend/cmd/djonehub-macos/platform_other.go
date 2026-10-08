//go:build !windows

package main

import (
	"errors"
	"net"
	"syscall"
)

const windowsDJIHardwarePort = "DJI USB (VID_2CA3)"

func discoverWindowsATPort() (string, error)     { return "", errors.New("Windows only") }
func discoverWindowsUSBDevice() *usbDeviceStatus { return nil }
func discoverWindowsNetworkServices() ([]macNetworkService, error) {
	return nil, errors.New("Windows only")
}
func setWindowsNetworkServiceEnabled(string, bool) error  { return errors.New("Windows only") }
func renewWindowsNetworkServiceDHCP(string) error         { return errors.New("Windows only") }
func (a *app) applyWindowsCellularPolicyLocked() error    { return errors.New("Windows only") }
func discoverWindowsNetworkInterfaces() []macNetInterface { return nil }
func discoverWindowsDefaultRoute() macDefaultRoute        { return macDefaultRoute{} }
func discoverWindowsInterfaceCounters() (map[string]networkByteCounters, error) {
	return nil, errors.New("Windows only")
}

func bindWindowsCellularProbe(*net.Dialer, string) error { return errors.New("Windows only") }

func renewWindowsNetworkServiceAfterResume(string) (bool, error) {
	return false, errors.New("Windows only")
}
func windowsCellularProbeInterface() (string, net.IP, connectivityState) {
	return "", nil, connectivityInconclusive
}

func definitelyOfflineNetworkError(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return false
	}
	var operation *net.OpError
	if !errors.As(err, &operation) || operation.Op != "dial" {
		return false
	}
	if operation.Timeout() {
		return true
	}
	return errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH)
}
