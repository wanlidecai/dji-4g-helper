import AppKit
import WebKit
import ServiceManagement
import Darwin

let appName = "大疆4g模块辅助工具"
let arguments = CommandLine.arguments
let demoMode = arguments.contains("--demo")
let portIndex = arguments.firstIndex(of: "--port")
let servicePort = portIndex.flatMap { $0 + 1 < arguments.count ? Int(arguments[$0 + 1]) : nil } ?? 7575
guard (1024...65535).contains(servicePort) else { exit(2) }
let baseURL = URL(string: "http://127.0.0.1:\(servicePort)")!
let dataDirectory = FileManager.default.homeDirectoryForCurrentUser
    .appendingPathComponent("Library/Application Support/DJI4GHelper", isDirectory: true)
let logDirectory = FileManager.default.homeDirectoryForCurrentUser
    .appendingPathComponent("Library/Logs/DJOneHub", isDirectory: true)
try FileManager.default.createDirectory(at: dataDirectory, withIntermediateDirectories: true)
try FileManager.default.createDirectory(at: logDirectory, withIntermediateDirectories: true)

// A shared lock also prevents two copies of the app from competing for the USB device.
let lockFD = open(dataDirectory.appendingPathComponent("app-\(servicePort).lock").path, O_CREAT | O_RDWR, S_IRUSR | S_IWUSR)
guard lockFD >= 0, flock(lockFD, LOCK_EX | LOCK_NB) == 0 else { exit(0) }
_ = fcntl(lockFD, F_SETFD, FD_CLOEXEC)

func executablePath(_ pid: pid_t) -> String? {
    var buffer = [CChar](repeating: 0, count: 4096)
    guard proc_pidpath(pid, &buffer, UInt32(buffer.count)) > 0 else { return nil }
    return URL(fileURLWithPath: String(cString: buffer)).resolvingSymlinksInPath().path
}

func writeJSON(_ value: [String: Any], to url: URL) {
    if let data = try? JSONSerialization.data(withJSONObject: value, options: [.prettyPrinted, .sortedKeys]) {
        try? data.write(to: url, options: .atomic)
    }
}

final class BackendController: WakeServiceManager {
    var onChange: (() -> Void)?
    private(set) var state = "正在启动…"
    private(set) var healthy = false
    private(set) var hardwareConnected = false
    private(set) var external = false
    private var process: Process?
    private var recoveredPID: pid_t?
    private var output: FileHandle?
    private var timer: Timer?
    private var retryWork: DispatchWorkItem?
    private var wantRunning = true
    private var systemSleeping = false
    private var testResleepPosted = false
    private var stopping = false
    private var checking = false
    private var launchRequested = false
    private var healthEpoch = 0
    private var stopCompletions: [() -> Void] = []
    private var failures = 0
    private var launchedAt = Date()
    private let pidFile = dataDirectory.appendingPathComponent("service-\(servicePort).json")
    private let runtime = Bundle.main.resourceURL!.appendingPathComponent("runtime", isDirectory: true)
    private var binary: URL { runtime.appendingPathComponent("bin/djonehub-macos").resolvingSymlinksInPath() }
    var pid: pid_t? { process?.processIdentifier ?? recoveredPID }
    var ownsService: Bool { pid != nil }
    var wakeRecoveryAllowed: Bool { wantRunning && !external }
    var wakeMutationAllowed: Bool {
        guard wakeRecoveryAllowed, healthy, let servicePID = pid,
              executablePath(servicePID) == binary.path else { return false }
        let check = Process(), pipe = Pipe()
        check.executableURL = URL(fileURLWithPath: "/usr/sbin/lsof")
        check.arguments = ["-t", "-nP", "-a", "-p", String(servicePID), "-iTCP:\(servicePort)", "-sTCP:LISTEN"]
        check.standardOutput = pipe
        check.standardError = FileHandle.nullDevice
        do {
            try check.run()
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            check.waitUntilExit()
            return String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) == String(servicePID)
        } catch { return false }
    }

    func setSystemSleeping(_ sleeping: Bool) {
        systemSleeping = sleeping
        healthEpoch += 1
        if sleeping { retryWork?.cancel(); retryWork = nil }
        else { poll() }
    }

    func reconnectForWake(ifActive: @escaping () -> Bool, completion: @escaping (Bool) -> Void) {
        guard ifActive(), !external else { completion(false); return }
        stop(preserveRunningIntent: true) { [weak self] in
            guard let self, ifActive() else { completion(false); return }
            self.start()
            completion(true)
        }
        if demoMode, arguments.contains("--test-sleep-during-reconnect"), !testResleepPosted {
            testResleepPosted = true
            let center = NSWorkspace.shared.notificationCenter
            center.post(name: NSWorkspace.willSleepNotification, object: NSWorkspace.shared)
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
                center.post(name: NSWorkspace.didWakeNotification, object: NSWorkspace.shared)
            }
        }
    }

    init() {
        // Recover an app-owned service after an unexpected app exit, without resetting the module.
        if let data = try? Data(contentsOf: pidFile),
           let record = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
           let number = record["pid"] as? Int, number > 1,
           record["executable"] as? String == binary.path,
           executablePath(pid_t(number)) == binary.path {
            recoveredPID = pid_t(number)
            log("Recovered existing app service PID \(number)")
        } else {
            try? FileManager.default.removeItem(at: pidFile)
        }
    }

    func begin() {
        let healthTimer = Timer(timeInterval: 5, repeats: true) { [weak self] _ in
            guard let self, !self.stopping, !self.systemSleeping else { return }
            if let pid = self.recoveredPID, executablePath(pid) != self.binary.path {
                self.recoveredPID = nil
                try? FileManager.default.removeItem(at: self.pidFile)
                self.healthy = false
                self.state = "后台服务已退出"
                self.scheduleRecovery()
            }
            self.poll()
        }
        timer = healthTimer
        RunLoop.main.add(healthTimer, forMode: .common)
        start()
    }

    func start(resetFailures: Bool = true) {
        guard !stopping, !systemSleeping else { return }
        if resetFailures { failures = 0 }
        wantRunning = true
        retryWork?.cancel()
        retryWork = nil
        if ownsService { poll(); return }
        state = "正在启动…"
        onChange?()
        poll(launchIfMissing: true)
    }

    private func spawn() {
        guard wantRunning, !stopping, !systemSleeping, !ownsService else { return }
        do {
            let logURL = logDirectory.appendingPathComponent("djonehub-app-\(servicePort).log")
            if let size = (try? FileManager.default.attributesOfItem(atPath: logURL.path))?[.size] as? NSNumber,
               size.intValue > 5 * 1024 * 1024 {
                let previous = logURL.appendingPathExtension("1")
                try? FileManager.default.removeItem(at: previous)
                try FileManager.default.moveItem(at: logURL, to: previous)
            }
            if !FileManager.default.fileExists(atPath: logURL.path) {
                FileManager.default.createFile(atPath: logURL.path, contents: nil, attributes: [.posixPermissions: 0o600])
            }
            let handle = try FileHandle(forWritingTo: logURL)
            try handle.seekToEnd()
            let child = Process()
            child.executableURL = binary
            child.currentDirectoryURL = dataDirectory
            var environment = ProcessInfo.processInfo.environment
            environment["PATH"] = "/usr/bin:/bin:/usr/sbin:/sbin:" + (environment["PATH"] ?? "")
            child.environment = environment
            child.arguments = ["-listen", "127.0.0.1:\(servicePort)"] + (demoMode ? ["-demo"] : [])
            child.standardInput = FileHandle.nullDevice
            child.standardOutput = handle
            child.standardError = handle
            child.terminationHandler = { [weak self] finished in
                DispatchQueue.main.async {
                    guard let self, self.process === finished else { return }
                    self.process = nil
                    try? self.output?.close()
                    self.output = nil
                    try? FileManager.default.removeItem(at: self.pidFile)
                    self.healthy = false
                    self.hardwareConnected = false
                    self.log("Service exited, status \(finished.terminationStatus)")
                    if !self.stopping { self.scheduleRecovery() }
                    self.onChange?()
                }
            }
            try child.run()
            output = handle
            process = child
            external = false
            healthy = false
            launchedAt = Date()
            state = "正在连接后台服务…"
            writeJSON(["pid": Int(child.processIdentifier), "executable": binary.path], to: pidFile)
            log("Service started, PID \(child.processIdentifier)")
            onChange?()
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.7) { [weak self] in self?.poll() }
        } catch {
            state = "启动失败：\(error.localizedDescription)"
            healthy = false
            log(state)
            onChange?()
        }
    }

    private func scheduleRecovery() {
        guard wantRunning, !stopping, !systemSleeping, retryWork == nil else {
            if !wantRunning { state = "已停止" }
            return
        }
        if Date().timeIntervalSince(launchedAt) > 60 { failures = 0 }
        failures += 1
        guard failures <= 3 else {
            wantRunning = false
            state = "服务异常，请查看日志或重新启动"
            onChange?()
            return
        }
        state = "服务已退出，正在重新启动（\(failures)/3）…"
        onChange?()
        let work = DispatchWorkItem { [weak self] in
            guard let self else { return }
            self.retryWork = nil
            if self.wantRunning, !self.stopping { self.start(resetFailures: false) }
        }
        retryWork = work
        DispatchQueue.main.asyncAfter(deadline: .now() + Double(failures * 2), execute: work)
    }

    private func portIsOccupied() -> Bool {
        let check = Process()
        let pipe = Pipe()
        check.executableURL = URL(fileURLWithPath: "/usr/sbin/lsof")
        check.arguments = ["-t", "-nP", "-iTCP:\(servicePort)", "-sTCP:LISTEN"]
        check.standardOutput = pipe
        check.standardError = FileHandle.nullDevice
        do {
            try check.run()
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            check.waitUntilExit()
            return !data.isEmpty
        } catch { return true }
    }

    func poll(launchIfMissing: Bool = false) {
        if launchIfMissing { launchRequested = true }
        guard !checking, !stopping, !systemSleeping else { return }
        checking = true
        let requestEpoch = healthEpoch
        var request = URLRequest(url: baseURL.appendingPathComponent("api/health"))
        request.timeoutInterval = 3
        request.cachePolicy = .reloadIgnoringLocalCacheData
        URLSession.shared.dataTask(with: request) { [weak self] data, response, error in
            let json = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
            let valid = (response as? HTTPURLResponse)?.statusCode == 200
                && json?["ok"] as? Bool == true && json?["demo"] is Bool
                && json?["esim_available"] is Bool && json?["port"] is String
                && json?["discovery_error"] is String
            DispatchQueue.main.async {
                guard let self else { return }
                self.checking = false
                guard !self.stopping else { return }
                guard requestEpoch == self.healthEpoch else {
                    if self.launchRequested { self.poll() }
                    return
                }
                let shouldLaunch = self.launchRequested
                self.launchRequested = false
                self.healthy = valid
                self.hardwareConnected = valid && ((json?["usb_device"] as? [String: Any]) != nil
                    || ((json?["discovery_error"] as? String) == "" && json?["port"] as? String != "未发现 AT 串口"))
                if valid {
                    self.external = !self.ownsService
                    self.state = self.external ? "已有服务（由其他程序管理）" : "运行中"
                } else if self.ownsService {
                    self.state = Date().timeIntervalSince(self.launchedAt) < 40 ? "正在连接后台服务…" : "服务暂时无响应，可尝试重启"
                } else if shouldLaunch, self.wantRunning {
                    if self.portIsOccupied() {
                        self.external = true
                        self.state = "端口 \(servicePort) 被占用，请先停止原服务"
                    } else {
                        self.external = false
                        self.spawn()
                        return
                    }
                } else if self.external {
                    self.external = false
                    self.state = "原服务已停止"
                    if self.wantRunning { self.start(); return }
                } else if !self.wantRunning {
                    self.state = "已停止"
                }
                self.onChange?()
            }
        }.resume()
    }

    func stop(preserveRunningIntent: Bool = false, completion: @escaping () -> Void = {}) {
        stopCompletions.append(completion)
        if !preserveRunningIntent { wantRunning = false }
        guard !stopping else { return }
        healthEpoch += 1
        launchRequested = false
        retryWork?.cancel()
        retryWork = nil
        guard let servicePID = pid, executablePath(servicePID) == binary.path else {
            state = external ? "已有服务（由其他程序管理）" : "已停止"
            onChange?()
            finishStops()
            return
        }
        stopping = true
        state = "正在停止…"
        onChange?()
        kill(servicePID, SIGTERM)
        let deadline = Date().addingTimeInterval(12)
        let waitTimer = Timer(timeInterval: 0.1, repeats: true) { [weak self] waitTimer in
            guard let self else { waitTimer.invalidate(); return }
            if executablePath(servicePID) != self.binary.path {
                waitTimer.invalidate()
                self.process = nil
                self.recoveredPID = nil
                try? self.output?.close()
                self.output = nil
                try? FileManager.default.removeItem(at: self.pidFile)
                self.stopping = false
                self.healthy = false
                self.hardwareConnected = false
                self.state = "已停止"
                self.onChange?()
                self.finishStops()
            } else if Date() > deadline {
                // Only force-stop the verified, app-owned executable after its HTTP shutdown grace period.
                kill(servicePID, SIGKILL)
            }
        }
        // AppKit waits for terminateLater in its modal run-loop mode.
        RunLoop.main.add(waitTimer, forMode: .common)
        RunLoop.main.add(waitTimer, forMode: .modalPanel)
    }

    private func finishStops() {
        let completions = stopCompletions
        stopCompletions.removeAll()
        for completion in completions { completion() }
    }

    func log(_ message: String) {
        let url = logDirectory.appendingPathComponent("helper-\(servicePort).log")
        if !FileManager.default.fileExists(atPath: url.path) {
            FileManager.default.createFile(atPath: url.path, contents: nil, attributes: [.posixPermissions: 0o600])
        }
        guard let handle = try? FileHandle(forWritingTo: url) else { return }
        defer { try? handle.close() }
        _ = try? handle.seekToEnd()
        try? handle.write(contentsOf: Data("\(ISO8601DateFormatter().string(from: Date())) \(message)\n".utf8))
    }
}

final class ManagerWebView: WKWebView {
    // Accessory apps need explicit edit shortcuts when they have no standard application menu.
    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        guard event.modifierFlags.intersection(.deviceIndependentFlagsMask) == .command else {
            return super.performKeyEquivalent(with: event)
        }
        if event.charactersIgnoringModifiers == "w" { window?.performClose(nil); return true }
        if event.charactersIgnoringModifiers == "q" { NSApp.terminate(nil); return true }
        if event.charactersIgnoringModifiers == "r" { reload(); return true }
        let selectors: [String: Selector] = ["c": NSSelectorFromString("copy:"), "v": NSSelectorFromString("paste:"),
            "x": NSSelectorFromString("cut:"), "a": NSSelectorFromString("selectAll:")]
        if let key = event.charactersIgnoringModifiers, let selector = selectors[key] {
            return NSApp.sendAction(selector, to: nil, from: self)
        }
        return super.performKeyEquivalent(with: event)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate, WKUIDelegate, WKNavigationDelegate, WKScriptMessageHandler {
    private let backend = BackendController()
    private var statusItem: NSStatusItem!
    private var stateItem: NSMenuItem!
    private var hardwareItem: NSMenuItem!
    private var startItem: NSMenuItem!
    private var stopItem: NSMenuItem!
    private var loginItem: NSMenuItem!
    private var managerWindow: NSWindow?
    private var webView: ManagerWebView?
    private var quitting = false
    private var pendingWindowLoad = false
    private var selectedView = "network"
    private var notificationsItem: NSMenuItem!
    private lazy var messages = MessageNotifications(baseURL: baseURL, directory: dataDirectory, port: servicePort, demo: demoMode)
    private var wakeItem: NSMenuItem!
    private var powerObservers: [NSObjectProtocol] = []
    private lazy var wakeRecovery = WakeRecovery(service: backend, baseURL: baseURL, demo: demoMode,
        testInternetMode: arguments.contains("--test-wake-once") && !arguments.contains("--test-sms-mode"),
        testConnectionSurvives: arguments.contains("--test-network-stays-online"))

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        statusItem.button?.image = NSImage(systemSymbolName: "leaf", accessibilityDescription: appName)
        statusItem.button?.image?.isTemplate = true
        statusItem.button?.title = "4G"
        statusItem.button?.toolTip = appName
        let menu = NSMenu()
        menu.autoenablesItems = false
        stateItem = menu.addItem(withTitle: "后台服务：正在启动…", action: nil, keyEquivalent: "")
        stateItem.isEnabled = false
        hardwareItem = menu.addItem(withTitle: "模块：检测中…", action: nil, keyEquivalent: "")
        hardwareItem.isEnabled = false
        wakeItem = menu.addItem(withTitle: "唤醒恢复：自动", action: nil, keyEquivalent: "")
        wakeItem.isEnabled = false
        menu.addItem(.separator())
        addItem(menu, "打开大疆4g模块辅助工具首页", #selector(showHome), "o")
        addItem(menu, "短信", #selector(showSMS), "1")
        addItem(menu, "通话", #selector(showCalls), "2")
        startItem = addItem(menu, "重新启动服务", #selector(startOrRestart))
        stopItem = addItem(menu, "停止后台服务", #selector(stopService))
        menu.addItem(.separator())
        loginItem = addItem(menu, "登录时自动启动", #selector(toggleLogin))
        notificationsItem = addItem(menu, "消息通知：等待允许", #selector(toggleNotifications))
        addItem(menu, "通知设置…", #selector(notificationSettings))
        addItem(menu, "查看日志", #selector(showLogs))
        menu.addItem(.separator())
        addItem(menu, "退出大疆4g模块辅助工具", #selector(quit), "q")
        statusItem.menu = menu
        backend.onChange = { [weak self] in self?.update() }
        wakeRecovery.onChange = { [weak self] in self?.update() }
        messages.canPoll = { [weak self] in self?.backend.healthy == true && self?.backend.ownsService == true && self?.backend.external == false && self?.quitting == false }
        messages.onChange = { [weak self] in self?.updateNotificationStatus() }
        messages.onLog = { [weak self] in self?.backend.log($0) }
        messages.onOpen = { [weak self] target in self?.openView(target) }
        messages.begin()
        let powerCenter = NSWorkspace.shared.notificationCenter
        powerObservers.append(powerCenter.addObserver(forName: NSWorkspace.willSleepNotification, object: nil, queue: .main) { [weak self] _ in
            self?.wakeRecovery.willSleep()
        })
        powerObservers.append(powerCenter.addObserver(forName: NSWorkspace.didWakeNotification, object: nil, queue: .main) { [weak self] _ in
            guard let self, !self.quitting else { return }
            self.pendingWindowLoad = self.managerWindow?.isVisible ?? false
            self.wakeRecovery.didWake()
        })
        backend.log("App launched, PID \(getpid()), demo \(demoMode)")
        backend.begin()
        update()
        if arguments.contains("--show-window") { showManager() }
        // Explicit verification mode exercises the actual observer path without sleeping the user's Mac.
        if arguments.contains("--test-wake-once") {
            DispatchQueue.main.asyncAfter(deadline: .now() + 3) {
                powerCenter.post(name: NSWorkspace.willSleepNotification, object: NSWorkspace.shared)
                if demoMode && arguments.contains("--test-stop-before-wake") { self.stopService() }
                DispatchQueue.main.asyncAfter(deadline: .now() + 1) {
                    powerCenter.post(name: NSWorkspace.didWakeNotification, object: NSWorkspace.shared)
                }
            }
        }
    }

    @discardableResult private func addItem(_ menu: NSMenu, _ title: String, _ action: Selector, _ key: String = "") -> NSMenuItem {
        let item = menu.addItem(withTitle: title, action: action, keyEquivalent: key)
        item.target = self
        return item
    }

    private func update() {
        stateItem.title = "后台服务：\(backend.state)"
        hardwareItem.title = demoMode ? "模块：演示模式" : "模块：\(backend.healthy ? (backend.hardwareConnected ? "已连接" : "未连接，请插入模块") : "等待服务")"
        wakeItem.title = "唤醒恢复：\(wakeRecovery.status)"
        startItem.title = backend.ownsService ? "重新启动服务" : "启动后台服务"
        startItem.isEnabled = !quitting && !backend.external
        stopItem.isEnabled = backend.ownsService && !quitting
        let loginStatus = SMAppService.mainApp.status
        loginItem.state = loginStatus == .enabled ? .on : .off
        loginItem.title = loginStatus == .requiresApproval ? "登录启动待允许（打开系统设置）" : "登录时自动启动"
        loginItem.isEnabled = !demoMode
        statusItem.button?.toolTip = "\(appName) · \(backend.state)"
        if pendingWindowLoad, backend.healthy {
            pendingWindowLoad = false
            webView?.load(URLRequest(url: pageURL))
        }
        writeJSON(["app_pid": Int(getpid()), "service_pid": backend.pid.map(Int.init) ?? 0,
            "owned": backend.ownsService, "healthy": backend.healthy, "state": backend.state,
            "hardware_connected": backend.hardwareConnected, "demo": demoMode,
            "wake_state": wakeRecovery.status, "wake_recovering": wakeRecovery.recovering,
            "notification_authorization": messages.authorization, "notifications_enabled": messages.enabled,
            "notifications_delivered": messages.deliveredCount, "notification_error": messages.lastDeliveryError,
            "window_visible": managerWindow?.isVisible ?? false, "port": servicePort],
            to: dataDirectory.appendingPathComponent("status-\(servicePort).json"))
    }

    @objc private func showManager() {
        if managerWindow == nil {
            let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1160, height: 780),
                styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
            window.title = appName
            window.backgroundColor = NSColor(calibratedRed: 0.973, green: 0.969, blue: 0.953, alpha: 1)
            window.minSize = NSSize(width: 760, height: 560)
            window.isReleasedWhenClosed = false
            window.delegate = self
            window.center()
            let configuration = WKWebViewConfiguration()
            configuration.userContentController.add(self, name: "app")
            let view = ManagerWebView(frame: window.contentView!.bounds, configuration: configuration)
            view.autoresizingMask = [.width, .height]
            view.uiDelegate = self
            view.navigationDelegate = self
            window.contentView = view
            managerWindow = window
            webView = view
        }
        if backend.healthy {
            if webView?.url?.host != "127.0.0.1" { webView?.load(URLRequest(url: pageURL)) }
        } else {
            pendingWindowLoad = true
            webView?.loadHTMLString("<meta charset='utf-8'><style>body{background:#f8f7f2;font:16px -apple-system;padding:80px;color:#28634e}h1{font-size:32px;letter-spacing:2px}p{color:#6b746e;line-height:1.8}</style><h1>大疆4g模块辅助工具</h1><p>正在准备网络、短信和通话…</p><p>连接完成后会自动打开。关闭窗口后，仍会在菜单栏后台运行。</p>", baseURL: nil)
        }
        managerWindow?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        update()
    }

    private var pageURL: URL {
        var url = URLComponents(url: baseURL, resolvingAgainstBaseURL: false)!
        url.queryItems = [URLQueryItem(name: "view", value: selectedView)]
        return url.url!
    }
    private func openView(_ view: String) {
        selectedView = view
        showManager()
        if backend.healthy { webView?.load(URLRequest(url: pageURL)) }
        messages.markViewed(view)
    }
    @objc private func showHome() { openView("network") }
    @objc private func showSMS() { openView("sms") }
    @objc private func showCalls() { openView("calls") }
    @objc private func toggleNotifications() { messages.toggle() }
    @objc private func notificationSettings() { messages.permissionOrSettings() }
    private func updateNotificationStatus() {
        notificationsItem?.title = messages.label
        notificationsItem?.state = messages.enabled && messages.isAuthorized ? .on : .off
        let count = messages.unreadSMS + messages.missedCalls
        statusItem?.button?.title = count > 0 ? "4G \(min(count, 99))" : "4G"
        let json: [String: Any] = ["authorization": messages.authorization, "enabled": messages.enabled]
        if let data = try? JSONSerialization.data(withJSONObject: json), let text = String(data: data, encoding: .utf8) {
            webView?.evaluateJavaScript("window.dispatchEvent(new CustomEvent('wanli:notification-status',{detail:\(text)}))")
        }
    }
    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.frameInfo.isMainFrame, let url = message.frameInfo.request.url,
              url.host == "127.0.0.1", url.port == servicePort,
              let body = message.body as? [String: Any], let action = body["action"] as? String else { return }
        switch action {
        case "notificationSettings": messages.permissionOrSettings()
        case "notificationStatus": messages.refreshAuthorization()
        case "viewChanged":
            if let view = body["view"] as? String, ["sms", "calls", "network", "settings"].contains(view) {
                selectedView = view; messages.markViewed(view)
            }
        default: break
        }
    }

    @objc private func startOrRestart() {
        wakeRecovery.cancel()
        if backend.ownsService {
            backend.stop(preserveRunningIntent: true) { [weak self] in
                guard let self, !self.quitting else { return }
                self.backend.start()
            }
        } else { backend.start() }
        pendingWindowLoad = managerWindow?.isVisible ?? false
    }

    @objc private func stopService() { wakeRecovery.cancel(); backend.stop() }
    @objc private func showLogs() { NSWorkspace.shared.open(logDirectory) }
    @objc private func quit() { NSApp.terminate(nil) }

    @objc private func toggleLogin() {
        do {
            switch SMAppService.mainApp.status {
            case .enabled: try SMAppService.mainApp.unregister()
            case .requiresApproval: SMAppService.openSystemSettingsLoginItems()
            default: try SMAppService.mainApp.register()
            }
            update()
        } catch { showError("无法更改登录启动", error.localizedDescription) }
    }

    private func showError(_ title: String, _ detail: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = detail
        alert.addButton(withTitle: "好")
        NSApp.activate(ignoringOtherApps: true)
        alert.runModal()
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        showHome()
        return true
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    func windowWillClose(_ notification: Notification) {
        DispatchQueue.main.async { [weak self] in self?.update() }
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard backend.ownsService else { return .terminateNow }
        if quitting { return .terminateLater }
        quitting = true
        messages.end()
        wakeRecovery.cancel()
        messages.end()
        webView?.configuration.userContentController.removeScriptMessageHandler(forName: "app")
        backend.stop { sender.reply(toApplicationShouldTerminate: true) }
        return .terminateLater
    }

    func applicationWillTerminate(_ notification: Notification) {
        wakeRecovery.cancel()
        for observer in powerObservers { NSWorkspace.shared.notificationCenter.removeObserver(observer) }
        backend.log("App stopped")
        try? FileManager.default.removeItem(at: dataDirectory.appendingPathComponent("status-\(servicePort).json"))
        flock(lockFD, LOCK_UN)
        close(lockFD)
    }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
        showError(appName, message)
        completionHandler()
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) {
        let alert = NSAlert()
        alert.messageText = "确认操作"
        alert.informativeText = message
        alert.addButton(withTitle: "确定")
        alert.addButton(withTitle: "取消")
        completionHandler(alert.runModal() == .alertFirstButtonReturn)
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let url = navigationAction.request.url else { decisionHandler(.cancel); return }
        if url.scheme == "about" || (url.scheme == "http" && url.host == "127.0.0.1" && url.port == servicePort) {
            decisionHandler(.allow)
        } else {
            // External links only open after an explicit click inside the manager.
            if navigationAction.navigationType == .linkActivated { NSWorkspace.shared.open(url) }
            decisionHandler(.cancel)
        }
    }
}

let application = NSApplication.shared
let delegate = AppDelegate()
application.delegate = delegate
application.run()
