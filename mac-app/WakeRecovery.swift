import Foundation

protocol WakeServiceManager: AnyObject {
    var wakeRecoveryAllowed: Bool { get }
    var wakeMutationAllowed: Bool { get }
    func setSystemSleeping(_ sleeping: Bool)
    func reconnectForWake(ifActive: @escaping () -> Bool, completion: @escaping (Bool) -> Void)
    func log(_ message: String)
}

// A cancellation token is also read by the network worker before any DHCP mutation.
final class WakeCancellation {
    private let lock = NSLock()
    private var value = false
    var cancelled: Bool { lock.lock(); defer { lock.unlock() }; return value }
    func cancel() { lock.lock(); value = true; lock.unlock() }
}

final class WakeRecovery {
    var onChange: (() -> Void)?
    private(set) var status = "等待电脑唤醒"
    private(set) var recovering = false
    private var eligibleBeforeSleep: Bool?
    private var token = WakeCancellation()
    private var pending: DispatchWorkItem?
    private var deadline = Date.distantPast
    private var rebootRequested = false
    private var disconnectedChecks = 0
    private var backendReconnected = false
    private let service: WakeServiceManager
    private let baseURL: URL
    private let demo: Bool
    private let testInternetMode: Bool
    private let testConnectionSurvives: Bool

    init(service: WakeServiceManager, baseURL: URL, demo: Bool, testInternetMode: Bool = false,
         testConnectionSurvives: Bool = false) {
        self.service = service
        self.baseURL = baseURL
        self.demo = demo
        self.testInternetMode = demo && testInternetMode
        self.testConnectionSurvives = demo && testConnectionSurvives
    }

    func willSleep() {
        cancel()
        eligibleBeforeSleep = service.wakeRecoveryAllowed
        service.setSystemSleeping(true)
        status = "等待电脑唤醒"
        service.log("System sleep observed; wake recovery eligible: \(eligibleBeforeSleep == true)")
        onChange?()
    }

    func didWake() {
        service.setSystemSleeping(false)
        let eligible = eligibleBeforeSleep ?? service.wakeRecoveryAllowed
        eligibleBeforeSleep = nil
        guard eligible, service.wakeRecoveryAllowed else {
            status = "服务已手动停止，保持停止"
            service.log("Wake recovery skipped: service was stopped or managed externally")
            onChange?()
            return
        }
        // Duplicate wake notifications cannot start overlapping modem resets.
        guard !recovering else { return }
        token = WakeCancellation()
        recovering = true
        rebootRequested = false
        disconnectedChecks = 0
        backendReconnected = false
        deadline = Date().addingTimeInterval(100)
        setStatus("等待 USB 模块就绪…")
        schedule(after: 3) { [weak self] in self?.inspectBeforeRecovery() }
    }

    func cancel() {
        token.cancel()
        pending?.cancel()
        pending = nil
        if recovering { service.log("Wake recovery cancelled by sleep or user action") }
        recovering = false
    }

    private func active(_ expected: WakeCancellation) -> Bool {
        token === expected && !expected.cancelled && recovering
    }

    private func schedule(after delay: TimeInterval, _ action: @escaping () -> Void) {
        guard recovering, !token.cancelled else { return }
        let expected = token
        let work = DispatchWorkItem { [weak self] in
            guard let self, self.active(expected) else { return }
            self.pending = nil
            if Date() >= self.deadline { self.finish("恢复超时，请检查模块连接"); return }
            action()
        }
        pending?.cancel()
        pending = work
        DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: work)
    }

    private func reconnectThenReadMode() {
        backendReconnected = true
        let expected = token
        setStatus("正在重新连接模块…")
        service.reconnectForWake(ifActive: { [weak self] in self?.active(expected) == true }) { [weak self] started in
            guard let self, self.active(expected) else { return }
            guard started else { self.finish("服务已停止，恢复已取消"); return }
            self.schedule(after: 2) { [weak self] in self?.readMode() }
        }
    }

    private func inspectBeforeRecovery() {
        setStatus("正在检查模块网络…")
        if demo {
            if testConnectionSurvives { finish("网络连接正常，保持连接") }
            else { readMode() }
            return
        }
        let expected = token
        NetworkRecovery.inspect(cancelled: { expected.cancelled }) { [weak self] result in
            guard let self, self.active(expected) else { return }
            switch result {
            case .connected: self.finish("网络连接正常，保持连接")
            case .skipped(let reason): self.finish(reason)
            case .inconclusive:
                self.disconnectedChecks = 0
                self.setStatus("网络检测暂未确认，稍后再检查…")
                self.schedule(after: 8) { [weak self] in self?.inspectBeforeRecovery() }
            case .disconnected:
                self.disconnectedChecks += 1
                if self.disconnectedChecks < 2 {
                    self.setStatus("等待唤醒后的网络稳定…")
                    self.schedule(after: 5) { [weak self] in self?.inspectBeforeRecovery() }
                } else { self.readMode() }
            }
        }
    }

    private func readMode() {
        request("api/status") { [weak self] json, _ in
            guard let self else { return }
            let mode = self.testInternetMode && json?["usbnet_mode"] != nil ? 1 : (json?["usbnet_mode"] as? NSNumber)?.intValue
            guard let mode else {
                if !self.backendReconnected { self.reconnectThenReadMode(); return }
                self.setStatus("等待模块重新识别…")
                self.schedule(after: 4) { [weak self] in self?.readMode() }
                return
            }
            guard mode == 1 else { self.finish("保留原工作模式，无需恢复上网"); return }
            guard !self.forceOffPolicy() else { self.finish("4G 已关闭，保持原设置"); return }
            self.applyInternetMode()
        }
    }

    private func forceOffPolicy() -> Bool {
        let policy = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/DJOneHub/network-policy.json")
        guard FileManager.default.fileExists(atPath: policy.path) else { return false }
        guard let data = try? Data(contentsOf: policy),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let forceOff = json["force_off"] as? Bool else { return true }
        return forceOff
    }

    private func applyInternetMode() {
        guard service.wakeMutationAllowed else {
            if !service.wakeRecoveryAllowed { finish("服务归属已改变，保持当前连接") }
            else { schedule(after: 3) { [weak self] in self?.readMode() } }
            return
        }
        guard !forceOffPolicy() else { finish("4G 已关闭，保持原设置"); return }
        setStatus("正在恢复上网模式…")
        request("api/network/usbnet", method: "POST", body: ["mode": 1]) { [weak self] json, code in
            guard let self else { return }
            let response = (json?["response"] as? String ?? "").uppercased()
            guard code == 200, (json?["mode"] as? NSNumber)?.intValue == 1,
                  response.contains("OK"), !response.contains("ERROR") else {
                self.setStatus("等待模块接受上网模式…")
                self.schedule(after: 5) { [weak self] in self?.readMode() }
                return
            }
            self.service.log("Wake recovery applied usbnet=1")
            self.rebootModuleOnce()
        }
    }

    private func rebootModuleOnce() {
        guard !rebootRequested else { return }
        guard service.wakeMutationAllowed, !forceOffPolicy() else {
            finish("设置或服务归属已改变，恢复已取消")
            return
        }
        rebootRequested = true
        setStatus("等待模块重新启动…")
        service.log("Wake recovery requesting the same module reboot as the internet-mode button")
        request("api/network/reboot-module", method: "POST") { [weak self] _, code in
            guard let self else { return }
            // A successful reboot can disconnect USB before its HTTP response arrives.
            self.service.log("Wake reboot response status: \(code ?? 0); waiting for USB enumeration")
            self.schedule(after: self.demo ? 1 : 12) { [weak self] in
                guard let self else { return }
                let expected = self.token
                self.service.reconnectForWake(ifActive: { [weak self] in self?.active(expected) == true }) { [weak self] started in
                    guard let self, self.active(expected) else { return }
                    guard started else { self.finish("服务已停止，恢复已取消"); return }
                    self.schedule(after: 3) { [weak self] in self?.restoreNetwork() }
                }
            }
        }
    }

    private func restoreNetwork() {
        setStatus("正在恢复网卡连接…")
        if demo { finish("演示唤醒恢复验证通过"); return }
        let expected = token
        NetworkRecovery.recover(cancelled: { expected.cancelled }) { [weak self] result in
            guard let self, self.active(expected) else { return }
            switch result {
            case .ready: self.verifyRecoveredNetwork()
            case .skipped(let reason): self.finish(reason)
            case .failed(let reason): self.finish(reason)
            case .retry:
                self.setStatus("等待网卡重新获取地址…")
                self.schedule(after: 5) { [weak self] in self?.restoreNetwork() }
            }
        }
    }

    private func verifyRecoveredNetwork() {
        let expected = token
        NetworkRecovery.inspect(cancelled: { expected.cancelled }) { [weak self] result in
            guard let self, self.active(expected) else { return }
            switch result {
            case .connected: self.finish("上网模式及网络连接已恢复")
            case .skipped(let reason): self.finish(reason)
            case .disconnected, .inconclusive:
                self.setStatus("等待模块网络恢复…")
                self.schedule(after: 5) { [weak self] in self?.verifyRecoveredNetwork() }
            }
        }
    }

    private func request(_ path: String, method: String = "GET", body: [String: Any]? = nil,
                         completion: @escaping ([String: Any]?, Int?) -> Void) {
        let expected = token
        var request = URLRequest(url: baseURL.appendingPathComponent(path))
        request.httpMethod = method
        request.timeoutInterval = 10
        request.cachePolicy = .reloadIgnoringLocalCacheData
        if let body {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try? JSONSerialization.data(withJSONObject: body)
        }
        URLSession.shared.dataTask(with: request) { [weak self] data, response, _ in
            let json = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
            DispatchQueue.main.async {
                guard let self, self.active(expected) else { return }
                completion(json, (response as? HTTPURLResponse)?.statusCode)
            }
        }.resume()
    }

    private func setStatus(_ message: String) {
        if status != message { service.log("Wake recovery: \(message)") }
        status = message
        onChange?()
    }

    private func finish(_ message: String) {
        recovering = false
        pending?.cancel()
        pending = nil
        setStatus(message)
    }
}
