//go:build windows

// Windows tray host. No command prompt or browser is opened during startup.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const localURL = "http://127.0.0.1:17575"
const homeURL = localURL + "/?view=network"
const productName = "碗里的菜"

var user32 = syscall.NewLazyDLL("user32.dll")
var shell32 = syscall.NewLazyDLL("shell32.dll")
var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var procDefWindowProc = user32.NewProc("DefWindowProcW")
var procPostMessage = user32.NewProc("PostMessageW")
var hwnd uintptr
var child *ownedBackend
var backendJob windows.Handle
var backendLogFile *os.File
var supervisorDone = make(chan struct{})
var childMu sync.Mutex
var stopping atomic.Bool
var resumeBusy atomic.Bool
var recoveryPending atomic.Bool
var appDir string
var dataDir string
var client = &http.Client{Timeout: 8 * time.Second}
var taskbarCreated uintptr
var appIcon uintptr

func utf16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }
func call(dll *syscall.LazyDLL, name string, args ...uintptr) uintptr {
	r, _, _ := dll.NewProc(name).Call(args...)
	return r
}
func message(body string) {
	call(user32, "MessageBoxW", 0, uintptr(unsafe.Pointer(utf16(body))), uintptr(unsafe.Pointer(utf16(productName))), 0x40)
}
func shellOpen(path string) {
	call(shell32, "ShellExecuteW", hwnd, uintptr(unsafe.Pointer(utf16("open"))), uintptr(unsafe.Pointer(utf16(path))), 0, 0, 1)
}

type point struct{ X, Y int32 }
type msg struct {
	HWND           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          point
	Private        uint32
}
type wndClass struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}
type guid struct {
	D1     uint32
	D2, D3 uint16
	D4     [8]byte
}
type notifyData struct {
	Size                uint32
	Hwnd                uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	Guid                guid
	BalloonIcon         uintptr
}

func notify(action uintptr) {
	n := notifyData{Size: uint32(unsafe.Sizeof(notifyData{})), Hwnd: hwnd, ID: 1, Flags: 7, Callback: 0x8001, Icon: appIcon}
	copy(n.Tip[:], syscall.StringToUTF16(productName+" · 后台运行"))
	call(shell32, "Shell_NotifyIconW", action, uintptr(unsafe.Pointer(&n)))
	if action == 0 {
		n.Version = 4
		call(shell32, "Shell_NotifyIconW", 4, uintptr(unsafe.Pointer(&n)))
	}
}
func menu() {
	m := call(user32, "CreatePopupMenu")
	defer call(user32, "DestroyMenu", m)
	for _, item := range []struct {
		ID    uintptr
		Title string
	}{{1, "打开碗里的菜"}, {6, "短信"}, {7, "通话"}, {8, "首页（网络情况）"}, {10, "短信与来电通知"}, {2, "检查并恢复网络"}, {3, "打开日志文件夹"}, {4, "设置登录时自动启动"}, {5, "取消登录时自动启动"}, {9, "退出"}} {
		flags := uintptr(0)
		if item.ID == 10 && notificationsEnabled.Load() {
			flags = 8 // MF_CHECKED
		}
		call(user32, "AppendMenuW", m, flags, item.ID, uintptr(unsafe.Pointer(utf16(item.Title))))
	}
	var p point
	call(user32, "GetCursorPos", uintptr(unsafe.Pointer(&p)))
	call(user32, "SetForegroundWindow", hwnd)
	selected := call(user32, "TrackPopupMenu", m, 0x100|0x2, uintptr(p.X), uintptr(p.Y), 0, hwnd, 0)
	switch selected {
	case 1:
		shellOpen(homeURL)
	case 6:
		shellOpen(localURL + "/?view=sms")
	case 7:
		shellOpen(localURL + "/?view=calls")
	case 8:
		shellOpen(homeURL)
	case 10:
		toggleNotifications()
	case 2:
		go resumeNetwork()
	case 3:
		shellOpen(dataDir)
	case 4:
		go setStartup(true)
	case 5:
		go setStartup(false)
	case 9:
		call(user32, "DestroyWindow", hwnd)
	}
}
func windowProc(window uintptr, m uint32, w, l uintptr) uintptr {
	switch m {
	case 0x8001:
		event := l & 0xffff // NOTIFYICON_VERSION_4 puts the event in LOWORD.
		if event == 0x205 || event == 0x202 || event == 0x7b || event == 0x400 || event == 0x401 {
			menu()
		} else if event == 0x403 || event == 0x404 {
			notificationFinished(false)
		} else if event == 0x405 {
			notificationFinished(true)
		}
		return 0
	case notificationMessage:
		receiveNotifications()
		return 0
	case 0x113: // WM_TIMER
		if handleNotificationTimer(w) {
			return 0
		}
	case 0x218:
		if w == 0x12 || w == 7 {
			go resumeNetwork()
		}
		return 1
	case 0x219:
		if w == 0x8000 {
			go resumeNetwork()
		}
		return 1
	case 2:
		stopping.Store(true)
		notify(2)
		stopOwnedBackend()
		call(user32, "PostQuitMessage", 0)
		return 0
	}
	if taskbarCreated != 0 && uintptr(m) == taskbarCreated {
		notify(0)
		return 0
	}
	return call(user32, "DefWindowProcW", window, uintptr(m), w, l)
}

func resumeNetwork() {
	if !resumeBusy.CompareAndSwap(false, true) {
		return
	}
	defer resumeBusy.Store(false)
	log.Println("resume/device reconnect: waiting for USB")
	time.Sleep(4 * time.Second)
	deadline := time.Now().Add(90 * time.Second)
	for attempt := 0; time.Now().Before(deadline) && !stopping.Load(); attempt++ {
		if !ownedListenerReady() {
			if !waitUnlessStopping(6 * time.Second) {
				return
			}
			continue
		}
		resp, err := client.Post(localURL+"/api/system/resume", "application/json", nil)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				log.Println("resume accepted by backend")
				recoveryPending.Store(false)
				return
			}
			log.Printf("resume status %d", resp.StatusCode)
		} else {
			log.Printf("resume retry: %v", err)
		}
		time.Sleep(6 * time.Second)
	}
}

// CreateProcess assigns the job atomically via JOB_LIST before user code runs.
// The job handle is not inherited. A tray crash therefore closes the final job
// handle and terminates the backend and its PowerShell descendants.
// https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute
const procThreadAttributeJobList = 0x0002000D

type ownedBackend struct {
	handle windows.Handle
	pid    uint32
}

func createBackendJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}
func startOwnedBackend() (*ownedBackend, error) {
	// Caller holds childMu and has already checked stopping. Holding this lock
	// through CreateProcess prevents an exit from racing a newly created child.
	if backendJob == 0 || stopping.Load() {
		return nil, fmt.Errorf("helper is stopping")
	}
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	jobs := []windows.Handle{backendJob}
	if err = attributes.Update(procThreadAttributeJobList, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs[0])); err != nil {
		return nil, err
	}
	process := windows.CurrentProcess()
	var output windows.Handle
	if err = windows.DuplicateHandle(process, windows.Handle(backendLogFile.Fd()), process, &output, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	defer windows.CloseHandle(output)
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	input, err := windows.CreateFile(utf16("NUL"), windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &security, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(input)
	handles := []windows.Handle{input, output}
	if err = attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return nil, err
	}
	startup := windows.StartupInfoEx{}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	startup.ShowWindow = windows.SW_HIDE
	startup.StdInput = input
	startup.StdOutput = output
	startup.StdErr = output
	startup.ProcThreadAttributeList = attributes.List()
	binary := filepath.Join(appDir, "runtime", "DJOneHub-backend.exe")
	commandLine := utf16("\"" + binary + "\" -listen 127.0.0.1:17575")
	var info windows.ProcessInformation
	err = windows.CreateProcess(utf16(binary), commandLine, nil, nil, true, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_NO_WINDOW, nil, utf16(filepath.Join(appDir, "runtime")), &startup.StartupInfo, &info)
	runtime.KeepAlive(jobs)
	runtime.KeepAlive(handles)
	if err != nil {
		return nil, err
	}
	windows.CloseHandle(info.Thread)
	return &ownedBackend{handle: info.Process, pid: info.ProcessId}, nil
}
func stopOwnedBackend() {
	stopping.Store(true)
	childMu.Lock()
	defer childMu.Unlock()
	if backendJob != 0 {
		windows.CloseHandle(backendJob)
		backendJob = 0
	}
}
func waitUnlessStopping(duration time.Duration) bool {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if stopping.Load() {
			return false
		}
		time.Sleep(min(100*time.Millisecond, time.Until(deadline)))
	}
	return !stopping.Load()
}
func supervise() {
	defer close(supervisorDone)
	backoff := time.Second
	for !stopping.Load() {
		childMu.Lock()
		if stopping.Load() || backendJob == 0 {
			childMu.Unlock()
			return
		}
		current, err := startOwnedBackend()
		if err == nil {
			child = current
		}
		childMu.Unlock()
		exitCode := uint32(0)
		if err != nil {
			log.Printf("start owned backend: %v", err)
		} else {
			log.Printf("owned backend PID %d", current.pid)
			if recoveryPending.Load() {
				go resumeNetwork()
			}
			_, waitErr := windows.WaitForSingleObject(current.handle, windows.INFINITE)
			if waitErr != nil {
				log.Printf("wait backend: %v", waitErr)
			}
			_ = windows.GetExitCodeProcess(current.handle, &exitCode)
			log.Printf("backend exit code %d", exitCode)
			windows.CloseHandle(current.handle)
		}
		childMu.Lock()
		if child == current {
			child = nil
		}
		if backendJob != 0 && !stopping.Load() {
			_ = windows.TerminateJobObject(backendJob, 0)
		}
		childMu.Unlock()
		if stopping.Load() {
			return
		}
		requestedRebuild := err == nil && exitCode == 75
		if requestedRebuild {
			recoveryPending.Store(true)
		}
		if requestedRebuild {
			log.Println("COM recovery restart requested; waiting 12 seconds for USB enumeration")
			if !waitUnlessStopping(12 * time.Second) {
				return
			}
		} else {
			if !waitUnlessStopping(backoff) {
				return
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
		}
	}
}
func monitorClock() {
	last := time.Now()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if stopping.Load() {
			return
		}
		now := time.Now()
		if now.Sub(last) > 45*time.Second {
			go resumeNetwork()
		}
		last = now
	}
}
func setStartup(enabled bool) {
	exe, _ := os.Executable()
	script := ""
	escaped := escapePS(exe)
	if enabled {
		script = "$a=New-ScheduledTaskAction -Execute '" + escaped + "';$t=New-ScheduledTaskTrigger -AtLogOn -User ([System.Security.Principal.WindowsIdentity]::GetCurrent().Name);$s=New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -MultipleInstances IgnoreNew;$p=New-ScheduledTaskPrincipal -UserId ([System.Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType Interactive -RunLevel Highest;Register-ScheduledTask -TaskName 'DJI4GHelper' -Action $a -Trigger $t -Principal $p -Settings $s -Force | Out-Null"
	} else {
		script = "Unregister-ScheduledTask -TaskName 'DJI4GHelper' -Confirm:$false -ErrorAction SilentlyContinue"
	}
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Printf("startup: %v %s", err, output)
		message("自动启动设置失败，详情见日志。")
	} else if enabled {
		message("已设置登录时自动启动。请保持解压文件夹的位置不变。")
	} else {
		message("已取消登录时自动启动。")
	}
}
func escapePS(s string) string {
	out := ""
	for _, r := range s {
		if r == '\'' {
			out += "''"
		} else {
			out += string(r)
		}
	}
	return out
}
func main() {
	runtime.LockOSThread()
	exe, err := os.Executable()
	if err != nil {
		return
	}
	appDir = filepath.Dir(exe)
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	dataDir = filepath.Join(base, "DJI4GHelper")
	_ = os.MkdirAll(dataDir, 0700)
	loadPreferences()
	f, err := os.OpenFile(filepath.Join(dataDir, "helper.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		message("无法打开日志文件夹，请检查用户目录的权限。")
		return
	}
	defer f.Close()
	backendLogFile = f
	log.SetOutput(f)
	if _, err := os.Stat(filepath.Join(appDir, "runtime", "DJOneHub-backend.exe")); err != nil {
		message("请先完整解压 ZIP，再运行碗里的菜.exe；runtime 文件夹必须保留在同一目录。")
		return
	}
	admin := call(shell32, "IsUserAnAdmin")
	if admin == 0 {
		result := call(shell32, "ShellExecuteW", 0, uintptr(unsafe.Pointer(utf16("runas"))), uintptr(unsafe.Pointer(utf16(exe))), 0, uintptr(unsafe.Pointer(utf16(appDir))), 1)
		if result <= 32 {
			message("需要管理员权限来恢复模块的 USB 网卡和 DHCP。请右键程序选择以管理员身份运行。")
		}
		return
	}
	mutexName := utf16("Local\\WanlidecaiDJI4GHelper")
	handle, _, mutexErr := kernel32.NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(mutexName)))
	if handle == 0 {
		return
	}
	defer kernel32.NewProc("CloseHandle").Call(handle)
	if mutexErr == syscall.Errno(183) {
		return
	}
	instance := call(kernel32, "GetModuleHandleW", 0)
	appIcon = call(user32, "LoadIconW", instance, 1)
	if appIcon == 0 {
		appIcon = call(user32, "LoadIconW", 0, 32512)
	}
	className := utf16("DJI4GHelperTrayClass")
	wc := wndClass{Size: uint32(unsafe.Sizeof(wndClass{})), WndProc: syscall.NewCallback(windowProc), Instance: instance, ClassName: className, Icon: appIcon, SmallIcon: appIcon}
	if call(user32, "RegisterClassExW", uintptr(unsafe.Pointer(&wc))) == 0 {
		log.Println("RegisterClassEx failed")
		return
	}
	hwnd = call(user32, "CreateWindowExW", 0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16(productName))), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if hwnd == 0 {
		log.Println("CreateWindowEx failed")
		return
	}
	backendJob, err = createBackendJob()
	if err != nil {
		message("无法创建后台进程管理对象：" + err.Error())
		return
	}
	defer stopOwnedBackend()
	taskbarCreated = call(user32, "RegisterWindowMessageW", uintptr(unsafe.Pointer(utf16("TaskbarCreated"))))
	// Explorer runs at medium integrity while this host is elevated for DHCP.
	// Permit only its tray callback and restart message for this hidden window.
	// https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-changewindowmessagefilterex
	for _, shellMessage := range []uintptr{0x8001, taskbarCreated} {
		if shellMessage != 0 && call(user32, "ChangeWindowMessageFilterEx", hwnd, shellMessage, 1, 0) == 0 {
			log.Printf("allow shell callback message 0x%x failed", shellMessage)
		}
	}
	notify(0)
	log.Println("Windows helper started", fmt.Sprintf("PID %d", os.Getpid()))
	go supervise()
	go monitorClock()
	go monitorNotifications()
	var message msg
	for {
		r := call(user32, "GetMessageW", uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if r == 0 || int32(r) == -1 {
			break
		}
		call(user32, "TranslateMessage", uintptr(unsafe.Pointer(&message)))
		call(user32, "DispatchMessageW", uintptr(unsafe.Pointer(&message)))
	}
	stopOwnedBackend()
	select {
	case <-supervisorDone:
	case <-time.After(3 * time.Second):
	}
}
