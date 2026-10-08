import AppKit
import UserNotifications

final class MessageNotifications: NSObject, UNUserNotificationCenterDelegate {
    var onChange: (() -> Void)?
    var onOpen: ((String) -> Void)?
    var canPoll: (() -> Bool)?
    var onLog: ((String) -> Void)?
    private let center = UNUserNotificationCenter.current()
    private let baseURL: URL
    private let stateURL: URL
    private let demo: Bool
    private var timer: Timer?
    private var fetching = false
    private var stopped = false
    private var preferenceGeneration = 0
    private var instanceID: String?
    private var cursor: Int?
    private var seen = Set<String>()
    private var orderedSeen: [String] = []
    private var seenCalls = Set<String>()
    private var scheduledIdentifiers = Set<String>()
    private var orderedIdentifiers: [String] = []
    private(set) var enabled = true
    private(set) var authorization = "notDetermined"
    private(set) var unreadSMS = 0
    private(set) var missedCalls = 0
    private(set) var deliveredCount = 0
    private(set) var lastDeliveryError = ""
    var isAuthorized: Bool { authorization == "authorized" }
    var label: String {
        if !enabled { return "消息通知：已关闭" }
        if isAuthorized { return "消息通知：已开启" }
        return authorization == "denied" ? "消息通知：系统已关闭" : "消息通知：等待允许"
    }

    init(baseURL: URL, directory: URL, port: Int, demo: Bool) {
        self.baseURL = baseURL
        self.stateURL = directory.appendingPathComponent("notifications-\(port).json")
        self.demo = demo
        super.init()
        if let data = try? Data(contentsOf: stateURL),
           let saved = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
            enabled = saved["enabled"] as? Bool ?? true
            instanceID = saved["instance_id"] as? String
            cursor = saved["cursor"] as? Int
        }
        center.delegate = self
    }

    func begin() {
        refreshAuthorization()
        let t = Timer(timeInterval: 2, repeats: true) { [weak self] _ in self?.poll() }
        timer = t
        RunLoop.main.add(t, forMode: .common)
    }
    func end() {
        stopped = true; timer?.invalidate(); timer = nil; save()
        if demo {
            let identifiers = Array(scheduledIdentifiers)
            center.removeDeliveredNotifications(withIdentifiers: identifiers)
            center.removePendingNotificationRequests(withIdentifiers: identifiers)
        }
    }
    func markViewed(_ target: String) {
        if target == "sms" { unreadSMS = 0 }
        if target == "calls" { missedCalls = 0 }
        onChange?()
    }
    func toggle() {
        if isAuthorized {
            if enabled { enabled = false; preferenceGeneration += 1; save() }
            else { enableWithFreshBaseline() }
            onChange?()
        }
        else { permissionOrSettings() }
    }
    private func enableWithFreshBaseline() {
        enabled = true
        preferenceGeneration += 1
        instanceID = nil; cursor = nil
        save()
    }
    func permissionOrSettings() {
        center.getNotificationSettings { [weak self] settings in
            guard let self else { return }
            DispatchQueue.main.async {
                if settings.authorizationStatus == .notDetermined {
                    self.center.requestAuthorization(options: [.alert, .sound, .badge]) { [weak self] allowed, _ in
                        DispatchQueue.main.async {
                            guard let self else { return }
                            if allowed { self.enableWithFreshBaseline() }
                            self.refreshAuthorization()
                        }
                    }
                } else if settings.authorizationStatus == .denied {
                    if let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension") { NSWorkspace.shared.open(url) }
                    self.refreshAuthorization()
                } else {
                    if !self.enabled { self.enableWithFreshBaseline() }
                    self.refreshAuthorization()
                }
            }
        }
    }
    func refreshAuthorization() {
        center.getNotificationSettings { [weak self] settings in
            DispatchQueue.main.async {
                guard let self, !self.stopped else { return }
                switch settings.authorizationStatus {
                case .authorized, .provisional, .ephemeral: self.authorization = "authorized"
                case .denied: self.authorization = "denied"
                default: self.authorization = "notDetermined"
                }
                self.onChange?()
            }
        }
    }

    private func poll() {
        guard !stopped, !fetching, canPoll?() == true else { return }
        var components = URLComponents(url: baseURL.appendingPathComponent("api/events"), resolvingAgainstBaseURL: false)!
        if let cursor, let instanceID { components.queryItems = [URLQueryItem(name: "after", value: String(cursor)), URLQueryItem(name: "instance_id", value: instanceID)] }
        var request = URLRequest(url: components.url!)
        request.timeoutInterval = 4
        request.cachePolicy = .reloadIgnoringLocalCacheData
        fetching = true
        let generation = preferenceGeneration
        URLSession.shared.dataTask(with: request) { [weak self] data, response, _ in
            DispatchQueue.main.async {
                guard let self else { return }
                self.fetching = false
                guard !self.stopped, self.preferenceGeneration == generation,
                      self.canPoll?() == true, let http = response as? HTTPURLResponse, http.statusCode == 200,
                      let data, let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                      let newInstance = json["instance_id"] as? String, let newCursor = json["cursor"] as? Int else { return }
                let changed = self.instanceID != newInstance || self.cursor != newCursor
                if self.instanceID != newInstance { self.seen.removeAll(); self.orderedSeen.removeAll(); self.seenCalls.removeAll() }
                self.instanceID = newInstance
                let events = json["events"] as? [[String: Any]] ?? []
                let endedCalls = Set(events.filter { $0["type"] as? String == "call_ended" }.compactMap { $0["call_id"] as? String })
                for event in events {
                    if event["type"] as? String == "incoming_call",
                       let callID = event["call_id"] as? String, endedCalls.contains(callID) { continue }
                    self.consume(event)
                }
                self.cursor = newCursor
                if changed { self.save() }
            }
        }.resume()
    }

    private func consume(_ event: [String: Any]) {
        guard let eventID = event["id"] as? String, let kind = event["type"] as? String else { return }
        let key = "\(instanceID ?? "")-\(eventID)"
        guard seen.insert(key).inserted else { return }
        orderedSeen.append(key)
        if orderedSeen.count > 512 { seen.remove(orderedSeen.removeFirst()) }
        let callID = event["call_id"] as? String ?? eventID
        let incomingID = "incoming-\(instanceID ?? "")-\(callID)"
        if kind == "call_ended" {
            center.removeDeliveredNotifications(withIdentifiers: [incomingID])
            center.removePendingNotificationRequests(withIdentifiers: [incomingID])
            return
        }
        if kind == "incoming_call" {
            if event["baseline"] as? Bool != true, let raw = event["timestamp"] as? String {
                let parser = ISO8601DateFormatter()
                parser.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
                let parsed = parser.date(from: raw) ?? ISO8601DateFormatter().date(from: raw)
                if parsed == nil || Date().timeIntervalSince(parsed!) > 30 { return }
            }
            guard seenCalls.insert(callID).inserted else { return }
            if seenCalls.count > 256 { seenCalls = [callID] }
        }
        let target = event["target"] as? String == "calls" ? "calls" : "sms"
        if kind == "sms" { unreadSMS += 1 }
        if kind == "missed_call" { missedCalls += 1 }
        onChange?()
        guard enabled, isAuthorized, !demo || CommandLine.arguments.contains("--test-notifications") else { return }
        let content = UNMutableNotificationContent()
        let title = event["title"] as? String ?? (kind == "sms" ? "新短信" : "来电")
        content.title = demo ? "演示 · \(title)" : title
        content.body = String((event["body"] as? String ?? "打开碗里的菜查看").prefix(160))
        content.sound = .default
        content.threadIdentifier = target
        content.userInfo = ["target": target]
        let identifier = kind == "incoming_call" ? incomingID : "message-\(key)"
        scheduledIdentifiers.insert(identifier)
        orderedIdentifiers.append(identifier)
        if orderedIdentifiers.count > 512 { scheduledIdentifiers.remove(orderedIdentifiers.removeFirst()) }
        center.add(UNNotificationRequest(identifier: identifier, content: content, trigger: nil)) { [weak self] error in
            DispatchQueue.main.async {
                guard let self else { return }
                self.lastDeliveryError = error?.localizedDescription ?? ""
                self.onLog?("Native notification \(kind): \(error == nil ? "accepted" : "delivery failed")")
                DispatchQueue.main.asyncAfter(deadline: .now() + 1) {
                    self.center.getDeliveredNotifications { [weak self] notifications in
                        DispatchQueue.main.async {
                            guard let self else { return }
                            self.deliveredCount = notifications.filter {
                                self.scheduledIdentifiers.contains($0.request.identifier)
                            }.count
                            self.onChange?()
                        }
                    }
                }
            }
        }
    }
    private func save() {
        var state: [String: Any] = ["enabled": enabled]
        if let instanceID { state["instance_id"] = instanceID }
        if let cursor { state["cursor"] = cursor }
        guard let data = try? JSONSerialization.data(withJSONObject: state) else { return }
        try? data.write(to: stateURL, options: .atomic)
        try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: stateURL.path)
    }
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list, .sound])
    }
    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        let target = response.notification.request.content.userInfo["target"] as? String == "calls" ? "calls" : "sms"
        DispatchQueue.main.async { [weak self] in self?.onOpen?(target); completionHandler() }
    }
}
