//go:build windows

package main

// The libusb-1.0 Windows DLL is loaded from this executable's directory only.
// This provides actual USB bulk AT transport without a C compiler dependency.
import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	djiUSBVendorID  = 0x2ca3
	djiUSBProductID = 0x4006
)

var usbDLL *syscall.DLL
var usbDLLOnce sync.Once
var usbDLLError error
var usbFunctions map[string]*syscall.Proc

func loadUSB() error {
	usbDLLOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			usbDLLError = err
			return
		}
		usbDLL, usbDLLError = syscall.LoadDLL(filepath.Join(filepath.Dir(exe), "libusb-1.0.dll"))
		if usbDLLError != nil {
			return
		}
		usbFunctions = make(map[string]*syscall.Proc)
		for _, n := range []string{"libusb_init", "libusb_exit", "libusb_open_device_with_vid_pid", "libusb_close", "libusb_claim_interface", "libusb_release_interface", "libusb_get_device", "libusb_get_active_config_descriptor", "libusb_free_config_descriptor", "libusb_bulk_transfer", "libusb_error_name"} {
			proc, err := usbDLL.FindProc(n)
			if err != nil {
				usbDLLError = err
				return
			}
			usbFunctions[n] = proc
		}
	})
	return usbDLLError
}
func usbCall(name string, args ...uintptr) uintptr {
	r, _, _ := usbFunctions[name].Call(args...)
	return r
}
func usbRC(value uintptr) int32 { return int32(value) }

type usbAT struct {
	ctx         uintptr
	handle      uintptr
	iface       int
	endpointIn  byte
	endpointOut byte
	mu          sync.Mutex
}
type usbATCandidate struct {
	iface       int
	endpointIn  byte
	endpointOut byte
}

// Structures match the 64-bit C ABI declared in bundled libusb.h.
type usbConfigDescriptor struct {
	Length, Type                                   byte
	Total                                          uint16
	NumInterfaces, Value, Index, Attributes, Power byte
	Interface                                      uintptr
	Extra                                          uintptr
	ExtraLength                                    int32
}
type usbInterface struct {
	Altsetting    uintptr
	NumAltsetting int32
}
type usbInterfaceDescriptor struct {
	Length, Type, Number, Altsetting, NumEndpoints, Class, Subclass, Protocol, Index byte
	Endpoint                                                                         uintptr
	Extra                                                                            uintptr
	ExtraLength                                                                      int32
}
type usbEndpointDescriptor struct {
	Length, Type, Address, Attributes byte
	MaxPacket                         uint16
	Interval, Refresh, Synch          byte
	Extra                             uintptr
	ExtraLength                       int32
}

func openDJIUSBAT() (*usbAT, error) {
	if err := loadUSB(); err != nil {
		return nil, fmt.Errorf("load bundled libusb-1.0.dll: %w", err)
	}
	var ctx uintptr
	if rc := usbRC(usbCall("libusb_init", uintptr(unsafe.Pointer(&ctx)))); rc != 0 {
		return nil, fmt.Errorf("libusb init: %s", usbErrorName(rc))
	}
	handle := usbCall("libusb_open_device_with_vid_pid", ctx, djiUSBVendorID, djiUSBProductID)
	if handle == 0 {
		usbCall("libusb_exit", ctx)
		return nil, errors.New("DJI USB AT device 2ca3:4006 not found or no WinUSB driver on AT interface; see Windows setup guide")
	}
	candidates, err := usbATCandidates(handle)
	if err != nil {
		usbCall("libusb_close", handle)
		usbCall("libusb_exit", ctx)
		return nil, err
	}
	var lastErr error
	for _, c := range candidates {
		if rc := usbRC(usbCall("libusb_claim_interface", handle, uintptr(c.iface))); rc != 0 {
			lastErr = fmt.Errorf("claim USB AT interface %d: %s (WinUSB driver required for AT interface only)", c.iface, usbErrorName(rc))
			continue
		}
		dev := &usbAT{ctx: ctx, handle: handle, iface: c.iface, endpointIn: c.endpointIn, endpointOut: c.endpointOut}
		if response, err := dev.Command("AT", 900*time.Millisecond); err == nil && atProbeSucceeded(response) {
			return dev, nil
		} else {
			lastErr = fmt.Errorf("probe USB AT interface %d: %v", c.iface, err)
		}
		usbCall("libusb_release_interface", handle, uintptr(c.iface))
	}
	usbCall("libusb_close", handle)
	usbCall("libusb_exit", ctx)
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no USB bulk interfaces found")
}
func usbATCandidates(handle uintptr) ([]usbATCandidate, error) {
	dev := usbCall("libusb_get_device", handle)
	if dev == 0 {
		return nil, errors.New("USB device handle empty")
	}
	var pointer uintptr
	if rc := usbRC(usbCall("libusb_get_active_config_descriptor", dev, uintptr(unsafe.Pointer(&pointer)))); rc != 0 {
		return nil, fmt.Errorf("USB config: %s", usbErrorName(rc))
	}
	defer usbCall("libusb_free_config_descriptor", pointer)
	if pointer == 0 {
		return nil, errors.New("USB config empty")
	}
	config := (*usbConfigDescriptor)(unsafe.Pointer(pointer))
	if config.NumInterfaces > 64 || (config.NumInterfaces > 0 && config.Interface == 0) {
		return nil, errors.New("USB interface count exceeds limit")
	}
	var candidates []usbATCandidate
	for _, intf := range unsafe.Slice((*usbInterface)(unsafe.Pointer(config.Interface)), int(config.NumInterfaces)) {
		if intf.NumAltsetting < 0 || intf.NumAltsetting > 64 || (intf.NumAltsetting > 0 && intf.Altsetting == 0) {
			continue
		}
		for _, alt := range unsafe.Slice((*usbInterfaceDescriptor)(unsafe.Pointer(intf.Altsetting)), int(intf.NumAltsetting)) {
			if !qdc507ATInterface(int(alt.Number)) {
				continue
			}
			if alt.NumEndpoints > 64 || (alt.NumEndpoints > 0 && alt.Endpoint == 0) {
				continue
			}
			var in, out byte
			for _, ep := range unsafe.Slice((*usbEndpointDescriptor)(unsafe.Pointer(alt.Endpoint)), int(alt.NumEndpoints)) {
				if ep.Attributes&3 != 2 {
					continue
				}
				if ep.Address&0x80 != 0 {
					in = ep.Address
				} else {
					out = ep.Address
				}
			}
			if in != 0 && out != 0 {
				candidates = append(candidates, usbATCandidate{int(alt.Number), in, out})
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].iface < candidates[j].iface })
	return candidates, nil
}
func (u *usbAT) Close() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == 0 {
		return
	}
	usbCall("libusb_release_interface", u.handle, uintptr(u.iface))
	usbCall("libusb_close", u.handle)
	usbCall("libusb_exit", u.ctx)
	u.handle = 0
	u.ctx = 0
}
func (u *usbAT) Command(cmd string, timeout time.Duration) (string, error) {
	if u == nil {
		return "", errors.New("USB AT device is not open")
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", errors.New("AT command is empty")
	}
	if !strings.HasPrefix(strings.ToUpper(cmd), "AT") {
		return "", errors.New("command must start with AT")
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == 0 {
		return "", errors.New("USB AT device is not open")
	}

	u.drainLocked()
	payload := []byte(cmd + "\r")
	if err := u.bulkWriteLocked(u.endpointOut, payload, timeout); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	var chunks []string
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 900*time.Millisecond {
			remaining = 900 * time.Millisecond
		}
		data, err := u.bulkReadLocked(u.endpointIn, remaining)
		if err != nil {
			if errors.Is(err, errUSBTimeout) {
				continue
			}
			return strings.Join(chunks, ""), err
		}
		if len(data) == 0 {
			continue
		}
		chunks = append(chunks, string(data))
		joined := strings.Join(chunks, "")
		if atResponseComplete(joined) {
			return normalizeATResponse(joined), nil
		}
	}
	if len(chunks) == 0 {
		return "", errors.New("USB AT command timed out without response")
	}
	return normalizeATResponse(strings.Join(chunks, "")), nil
}

// CommandWithPrompt executes an AT command that enters an interactive input
// state, then submits followUp after the modem returns its ">" prompt.
func (u *usbAT) CommandWithPrompt(cmd string, followUp []byte, timeout time.Duration) (string, error) {
	if u == nil {
		return "", errors.New("USB AT device is not open")
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", errors.New("AT command is empty")
	}
	if !strings.HasPrefix(strings.ToUpper(cmd), "AT") {
		return "", errors.New("command must start with AT")
	}
	if len(followUp) == 0 {
		return "", errors.New("interactive AT follow-up is empty")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.handle == 0 {
		return "", errors.New("USB AT device is not open")
	}

	u.drainLocked()
	if err := u.bulkWriteLocked(u.endpointOut, []byte(cmd+"\r"), timeout); err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	var response strings.Builder
	promptReceived := false
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining > 900*time.Millisecond {
			remaining = 900 * time.Millisecond
		}
		data, err := u.bulkReadLocked(u.endpointIn, remaining)
		if err != nil {
			if errors.Is(err, errUSBTimeout) {
				continue
			}
			return normalizeATResponse(response.String()), err
		}
		if len(data) == 0 {
			continue
		}
		response.Write(data)
		joined := response.String()

		if !promptReceived {
			if atResponseIsError(joined) {
				return normalizeATResponse(joined), nil
			}
			if !atResponseHasPrompt(joined) {
				continue
			}
			if err := u.bulkWriteLocked(u.endpointOut, followUp, time.Until(deadline)); err != nil {
				return normalizeATResponse(joined), err
			}
			promptReceived = true
			continue
		}

		if atResponseComplete(joined) {
			return normalizeATResponse(joined), nil
		}
	}

	if promptReceived {
		// ESC cancels a pending message editor on modems that still accept input.
		_ = u.bulkWriteLocked(u.endpointOut, []byte{0x1b}, 300*time.Millisecond)
	}
	if response.Len() == 0 {
		return "", errors.New("USB interactive AT command timed out without response")
	}
	return normalizeATResponse(response.String()), errors.New("USB interactive AT command timed out before completion")
}

var errUSBTimeout = errors.New("usb timeout")

func (u *usbAT) drainLocked() {
	for {
		if _, err := u.bulkReadLocked(u.endpointIn, 80*time.Millisecond); err != nil {
			return
		}
	}
}

func (u *usbAT) Description() string {
	if u == nil {
		return "USB AT"
	}
	return fmt.Sprintf("USB AT · 2ca3:4006 interface %d out 0x%02x in 0x%02x",
		u.iface, u.endpointOut, u.endpointIn)
}

func (u *usbAT) bulkWriteLocked(endpoint byte, payload []byte, timeout time.Duration) error {
	if len(payload) == 0 {
		return nil
	}
	var transferred int32
	rc := usbRC(usbCall("libusb_bulk_transfer", u.handle, uintptr(endpoint), uintptr(unsafe.Pointer(&payload[0])), uintptr(len(payload)), uintptr(unsafe.Pointer(&transferred)), uintptr(max(1, timeout.Milliseconds()))))
	runtime.KeepAlive(payload)
	if rc != 0 {
		return fmt.Errorf("USB bulk write: %s", usbErrorName(rc))
	}
	if int(transferred) != len(payload) {
		return fmt.Errorf("USB short write: %d/%d", transferred, len(payload))
	}
	return nil
}
func (u *usbAT) bulkReadLocked(endpoint byte, timeout time.Duration) ([]byte, error) {
	buf := make([]byte, 512)
	var transferred int32
	rc := usbRC(usbCall("libusb_bulk_transfer", u.handle, uintptr(endpoint), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&transferred)), uintptr(max(1, timeout.Milliseconds()))))
	runtime.KeepAlive(buf)
	if rc == -7 {
		return nil, errUSBTimeout
	}
	if rc != 0 {
		return nil, fmt.Errorf("USB bulk read: %s", usbErrorName(rc))
	}
	if transferred < 0 || transferred > int32(len(buf)) {
		return nil, errors.New("invalid USB transfer length")
	}
	return buf[:transferred], nil
}
func usbErrorName(rc int32) string {
	ptr := usbCall("libusb_error_name", uintptr(rc))
	if ptr == 0 {
		return fmt.Sprintf("code %d", rc)
	}
	bytes := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), 64)
	for i, v := range bytes {
		if v == 0 {
			return string(bytes[:i])
		}
	}
	return fmt.Sprintf("code %d", rc)
}
func atResponseComplete(resp string) bool {
	normalized := strings.ReplaceAll(resp, "\r\n", "\n")
	return strings.Contains(normalized, "\nOK\n") ||
		strings.HasSuffix(normalized, "\nOK") ||
		atResponseIsError(normalized)
}

func atResponseIsError(resp string) bool {
	normalized := strings.ToUpper(strings.ReplaceAll(resp, "\r\n", "\n"))
	return strings.Contains(normalized, "\nERROR\n") ||
		strings.HasSuffix(normalized, "\nERROR") ||
		strings.Contains(normalized, "+CME ERROR:") ||
		strings.Contains(normalized, "+CMS ERROR:")
}

func atResponseHasPrompt(resp string) bool {
	trimmed := strings.TrimRight(resp, " \t\r\n")
	return strings.HasSuffix(trimmed, ">")
}

// A probe must receive OK. ERROR merely proves that a bulk interface accepted
// bytes; it is not the modem's AT channel (the QMI interface can do that).
func atProbeSucceeded(resp string) bool {
	normalized := strings.ReplaceAll(strings.TrimSpace(resp), "\r\n", "\n")
	return normalized == "OK" || strings.HasSuffix(normalized, "\nOK")
}

func normalizeATResponse(resp string) string {
	resp = strings.ReplaceAll(resp, "\r\r\n", "\r\n")
	resp = strings.TrimSpace(resp)
	lines := strings.Split(resp, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		filtered = append(filtered, line)
	}
	return strings.Join(filtered, "\r\n")
}

// Fail compilation if the x64 libusb ABI layout drifts.
var _ [40 - unsafe.Sizeof(usbConfigDescriptor{})]byte
var _ [unsafe.Sizeof(usbConfigDescriptor{}) - 40]byte
var _ [16 - unsafe.Offsetof(usbConfigDescriptor{}.Interface)]byte
var _ [unsafe.Offsetof(usbConfigDescriptor{}.Interface) - 16]byte
var _ [16 - unsafe.Sizeof(usbInterface{})]byte
var _ [unsafe.Sizeof(usbInterface{}) - 16]byte
var _ [40 - unsafe.Sizeof(usbInterfaceDescriptor{})]byte
var _ [unsafe.Sizeof(usbInterfaceDescriptor{}) - 40]byte
var _ [32 - unsafe.Sizeof(usbEndpointDescriptor{})]byte
var _ [unsafe.Sizeof(usbEndpointDescriptor{}) - 32]byte
