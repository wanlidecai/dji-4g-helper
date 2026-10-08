import Foundation
import Darwin

enum NetworkRecoveryResult {
    case ready
    case skipped(String)
    case retry(String)
    case failed(String)
}

enum NetworkInspectionResult {
    case connected
    case disconnected(String)
    case skipped(String)
    case inconclusive(String)
}

/// Only renews an already enabled DHCP service on DJI's Baiwang hardware port.
/// It never enables a service, changes a manual address, or changes a modem mode.
enum NetworkRecovery {
    struct Service: Equatable {
        let name: String
        let hardwarePort: String
        let device: String
        let disabled: Bool
        var isDJI: Bool {
            hardwarePort.caseInsensitiveCompare("Baiwang") == .orderedSame
                && device.range(of: #"^en[0-9]+$"#, options: .regularExpression) != nil
        }
    }
    struct IPv4Info: Equatable {
        let usesDHCP: Bool
        let ready: Bool
    }
    enum Policy: Equatable { case allowed, forceOff, invalid }
    enum ProbeResult: Equatable { case connected, disconnected, inconclusive }
    private static let queue = DispatchQueue(label: "cn.wanlidecai.dji4g.network-recovery", qos: .utility)

    static func recover(cancelled: @escaping () -> Bool = { false },
                        completion: @escaping (NetworkRecoveryResult) -> Void) {
        queue.async {
            let result = perform(cancelled: cancelled)
            DispatchQueue.main.async { completion(result) }
        }
    }

    /// Connectivity checks bind their sockets to Baiwang's current interface,
    /// so an available Wi-Fi connection cannot hide a failed cellular link.
    static func inspect(cancelled: @escaping () -> Bool = { false },
                        completion: @escaping (NetworkInspectionResult) -> Void) {
        queue.async {
            let result = inspectNow(cancelled: cancelled)
            DispatchQueue.main.async { completion(result) }
        }
    }

    static func parseServices(_ output: String) -> [Service] {
        let header = try! NSRegularExpression(pattern: #"^\((\*|[0-9]+)\)\s+(.+)$"#)
        let detail = try! NSRegularExpression(pattern: #"^\(Hardware Port:\s*([^,]+),\s*Device:\s*([^)]+)\)$"#)
        let lines = output.replacingOccurrences(of: "\r\n", with: "\n")
            .components(separatedBy: "\n").map { $0.trimmingCharacters(in: .whitespaces) }
        guard lines.count > 1 else { return [] }
        var services: [Service] = []
        for index in 0..<(lines.count - 1) {
            guard let h = captures(header, lines[index]), let d = captures(detail, lines[index + 1]) else { continue }
            services.append(Service(name: h[1], hardwarePort: d[0], device: d[1], disabled: h[0] == "*"))
        }
        return services
    }

    static func parseIPv4Info(_ output: String) -> IPv4Info {
        let lines = output.components(separatedBy: .newlines).map { $0.trimmingCharacters(in: .whitespaces) }
        let dhcp = lines.first?.caseInsensitiveCompare("DHCP Configuration") == .orderedSame
        var fields: [String: String] = [:]
        for line in lines {
            guard let separator = line.firstIndex(of: ":") else { continue }
            fields[String(line[..<separator]).lowercased()] = String(line[line.index(after: separator)...])
                .trimmingCharacters(in: .whitespaces)
        }
        let address = fields["ip address"] ?? ""
        let router = fields["router"] ?? ""
        return IPv4Info(usesDHCP: dhcp, ready: validIPv4(address) && validIPv4(router) && address != router)
    }

    static func parseCarrier(_ output: String) -> Bool {
        output.components(separatedBy: .newlines).contains {
            $0.trimmingCharacters(in: .whitespaces).lowercased() == "status: active"
        }
    }

    static func parseInterfaceReady(_ output: String) -> Bool {
        parseCarrier(output) && output.components(separatedBy: .newlines).contains { line in
            let fields = line.split(whereSeparator: { $0.isWhitespace })
            return fields.count > 1 && fields[0] == "inet" && validIPv4(String(fields[1]))
        }
    }

    static func parsePolicy(_ data: Data?) -> Policy {
        guard let data else { return .allowed }
        guard let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let value = object["force_off"] as? NSNumber,
              CFGetTypeID(value) == CFBooleanGetTypeID() else { return .invalid }
        return value.boolValue ? .forceOff : .allowed
    }

    static func classifyProbe(status: Int32, text: String, expected: String) -> ProbeResult {
        if status == 0 && text.contains(expected) { return .connected }
        // DNS, TLS, captive portals and unexpected content do not prove a broken modem.
        return status == 7 || status == 28 ? .disconnected : .inconclusive
    }

    private static func captures(_ regex: NSRegularExpression, _ line: String) -> [String]? {
        guard let match = regex.firstMatch(in: line, range: NSRange(line.startIndex..., in: line)) else { return nil }
        return (1..<match.numberOfRanges).compactMap {
            Range(match.range(at: $0), in: line).map { String(line[$0]).trimmingCharacters(in: .whitespaces) }
        }
    }

    private static func validIPv4(_ value: String) -> Bool {
        let parts = value.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 4 else { return false }
        let octets = parts.compactMap { part -> Int? in
            guard !part.isEmpty, part.allSatisfy({ $0.isASCII && $0.isNumber }),
                  let number = Int(part), (0...255).contains(number) else { return nil }
            return number
        }
        guard octets.count == 4 else { return false }
        return octets[0] > 0 && octets[0] < 224 && octets[0] != 127
            && !(octets[0] == 169 && octets[1] == 254)
    }

    private static func currentPolicy() -> Policy {
        let file = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/DJOneHub/network-policy.json")
        guard FileManager.default.fileExists(atPath: file.path) else { return .allowed }
        guard let data = try? Data(contentsOf: file) else { return .invalid }
        return parsePolicy(data)
    }

    private static func inspectNow(cancelled: () -> Bool) -> NetworkInspectionResult {
        if cancelled() { return .skipped("检查已取消") }
        switch currentPolicy() {
        case .forceOff: return .skipped("4G 已由用户关闭")
        case .invalid: return .inconclusive("无法确认已有的 4G 网络策略")
        case .allowed: break
        }
        guard let list = command("/usr/sbin/networksetup", ["-listnetworkserviceorder"], cancelled: cancelled),
              list.status == 0 else { return .inconclusive("等待系统网络服务就绪") }
        let services = parseServices(list.text).filter { $0.isDJI }
        guard !services.isEmpty else { return .inconclusive("等待大疆网卡重新出现") }
        var eligible = 0, uncertain = false
        for service in services where !service.disabled {
            guard let info = command("/usr/sbin/networksetup", ["-getinfo", service.name], cancelled: cancelled),
                  info.status == 0 else { uncertain = true; continue }
            let parsed = parseIPv4Info(info.text)
            guard parsed.usesDHCP else { continue }
            eligible += 1
            guard let link = command("/sbin/ifconfig", [service.device], cancelled: cancelled),
                  link.status == 0 else { uncertain = true; continue }
            guard parsed.ready, parseInterfaceReady(link.text) else { continue }
            let endpoints = [
                ("https://captive.apple.com/hotspot-detect.html", "<TITLE>Success</TITLE>"),
                ("http://www.msftconnecttest.com/connecttest.txt", "Microsoft Connect Test")
            ]
            for (url, expected) in endpoints {
                guard let check = command("/usr/bin/curl", ["--interface", service.device, "--noproxy", "*",
                    "--connect-timeout", "3", "--max-time", "5", "--fail", "--silent", "--show-error", url],
                    timeout: 6, cancelled: cancelled) else { uncertain = true; continue }
                switch classifyProbe(status: check.status, text: check.text, expected: expected) {
                case .connected: return .connected
                case .inconclusive: uncertain = true
                case .disconnected: break
                }
            }
        }
        if cancelled() { return .skipped("检查已取消") }
        guard eligible > 0 else { return .skipped("保留已停用或手动设置的大疆网络服务") }
        return uncertain ? .inconclusive("公网检查暂时无法确认，保持当前连接") : .disconnected("大疆网卡连接尚未恢复")
    }

    private static func perform(cancelled: () -> Bool) -> NetworkRecoveryResult {
        if cancelled() { return .skipped("恢复已取消") }
        switch currentPolicy() {
        case .forceOff: return .skipped("4G 已由用户关闭")
        case .invalid: return .failed("无法确认已有的 4G 网络策略")
        case .allowed: break
        }
        guard let list = command("/usr/sbin/networksetup", ["-listnetworkserviceorder"], cancelled: cancelled),
              list.status == 0 else { return .retry("等待系统网络服务就绪") }
        let services = parseServices(list.text).filter { $0.isDJI }
        guard !services.isEmpty else { return .retry("等待大疆网卡重新出现") }
        var eligible = 0
        for service in services where !service.disabled {
            if cancelled() { return .skipped("恢复已取消") }
            guard let before = command("/usr/sbin/networksetup", ["-getinfo", service.name], cancelled: cancelled),
                  before.status == 0 else { return .retry("等待大疆网络配置就绪") }
            guard parseIPv4Info(before.text).usesDHCP else { continue }
            eligible += 1
            guard let carrier = command("/sbin/ifconfig", [service.device], cancelled: cancelled),
                  carrier.status == 0, parseCarrier(carrier.text) else { return .retry("等待大疆网卡链路恢复") }
            // Check again immediately before writing: a user may disable 4G during the wake delay.
            if cancelled() { return .skipped("恢复已取消") }
            guard currentPolicy() == .allowed else { return .skipped("4G 策略已改变") }
            guard let freshList = command("/usr/sbin/networksetup", ["-listnetworkserviceorder"], cancelled: cancelled),
                  freshList.status == 0,
                  parseServices(freshList.text).contains(service),
                  let freshInfo = command("/usr/sbin/networksetup", ["-getinfo", service.name], cancelled: cancelled),
                  freshInfo.status == 0, parseIPv4Info(freshInfo.text).usesDHCP else {
                return .skipped("网络设置已改变")
            }
            if cancelled() { return .skipped("恢复已取消") }
            guard let renew = command("/usr/sbin/networksetup", ["-setdhcp", service.name], cancelled: cancelled) else {
                return cancelled() ? .skipped("恢复已取消") : .retry("网络续租尚未完成")
            }
            guard renew.status == 0,
                  renew.text.range(of: #"(?i)error|you must be root|not authorized|not a recognized network service"#,
                                   options: .regularExpression) == nil else {
                return .failed("系统未允许大疆网卡续租，请检查网络设置权限")
            }
            guard let after = command("/usr/sbin/networksetup", ["-getinfo", service.name], cancelled: cancelled),
                  after.status == 0, parseIPv4Info(after.text).ready,
                  let link = command("/sbin/ifconfig", [service.device], cancelled: cancelled),
                  link.status == 0, parseInterfaceReady(link.text) else { return .retry("等待大疆网卡取得网络配置") }
        }
        return eligible > 0 ? .ready : .skipped("保留已停用或手动设置的大疆网络服务")
    }

    private struct Output { let status: Int32; let text: String }
    // Fixed system tools run off the main thread; command arguments never enter a shell.
    private static func command(_ path: String, _ args: [String], timeout: TimeInterval = 4,
                                cancelled: () -> Bool) -> Output? {
        guard !cancelled() else { return nil }
        let process = Process(), pipe = Pipe(), finished = DispatchSemaphore(value: 0)
        process.executableURL = URL(fileURLWithPath: path)
        process.arguments = args
        process.environment = ["PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL": "C"]
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = pipe
        process.standardError = pipe
        process.terminationHandler = { _ in finished.signal() }
        do { try process.run() } catch { return nil }
        let deadline = Date().addingTimeInterval(timeout)
        while finished.wait(timeout: .now() + 0.05) == .timedOut {
            if cancelled() || Date() >= deadline {
                if process.isRunning { process.terminate() }
                if finished.wait(timeout: .now() + 0.25) == .timedOut, process.isRunning {
                    _ = kill(process.processIdentifier, SIGKILL)
                    _ = finished.wait(timeout: .now() + 0.5)
                }
                try? pipe.fileHandleForReading.close()
                return nil
            }
        }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        try? pipe.fileHandleForReading.close()
        return Output(status: process.terminationStatus, text: String(data: data, encoding: .utf8) ?? "")
    }
}
