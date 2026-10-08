import Foundation

@main
struct NetworkRecoveryTests {
    static func main() {
        let fixture = """
        An asterisk (*) denotes that a network service is disabled.
        (1) Wi-Fi
        (Hardware Port: Wi-Fi, Device: en0)
        (2) Renamed DJI service
        (Hardware Port: Baiwang, Device: en9)
        (*) Old DJI
        (Hardware Port: Baiwang, Device: en8)
        (4) Baiwang
        (Hardware Port: USB Ethernet, Device: en10)
        """
        let services = NetworkRecovery.parseServices(fixture)
        precondition(services.count == 4)
        precondition(NetworkRecovery.parseServices(fixture.replacingOccurrences(of: "\n", with: "\r\n")) == services)
        precondition(services.filter { $0.isDJI && !$0.disabled }.map { $0.device } == ["en9"])
        precondition(services[2].disabled)
        precondition(!services[3].isDJI)
        precondition(NetworkRecovery.parseServices("partial header").isEmpty)
        let valid = "DHCP Configuration\nIP address: 192.0.2.2\nSubnet mask: 255.255.255.0\nRouter: 192.0.2.1\n"
        precondition(NetworkRecovery.parseIPv4Info(valid).usesDHCP)
        precondition(NetworkRecovery.parseIPv4Info(valid).ready)
        precondition(!NetworkRecovery.parseIPv4Info(valid.replacingOccurrences(of: "DHCP Configuration", with: "Manual Configuration")).usesDHCP)
        precondition(!NetworkRecovery.parseIPv4Info("DHCP with manual address\n" + valid).usesDHCP)
        precondition(!NetworkRecovery.parseIPv4Info(valid.replacingOccurrences(of: "192.0.2.2", with: "169.254.2.2")).ready)
        precondition(!NetworkRecovery.parseIPv4Info(valid.replacingOccurrences(of: "192.0.2.1", with: "192.0.2.2")).ready)
        precondition(!NetworkRecovery.parseIPv4Info("DHCP Configuration\nIP address: 192.0.2.2\n").ready)
        precondition(!NetworkRecovery.parseIPv4Info(valid.replacingOccurrences(of: "192.0.2.2", with: "999.0.2.2")).ready)
        precondition(NetworkRecovery.parseCarrier("en9: flags=8863\n\tstatus: active\n"))
        precondition(!NetworkRecovery.parseCarrier("en9: flags=8863\n\tstatus: inactive\n"))
        precondition(NetworkRecovery.parseInterfaceReady("en9: flags=8863\n\tinet 192.0.2.2 netmask 0xffffff00\n\tstatus: active\n"))
        precondition(!NetworkRecovery.parseInterfaceReady("en9: flags=8863\n\tstatus: active\n"))
        precondition(!NetworkRecovery.parseInterfaceReady("en9: flags=8863\n\tinet 169.254.2.2 netmask 0xffff0000\n\tstatus: active\n"))
        precondition(NetworkRecovery.parsePolicy(nil) == .allowed)
        precondition(NetworkRecovery.parsePolicy(Data(#"{"force_off":true,"services":["Renamed DJI service"]}"#.utf8)) == .forceOff)
        precondition(NetworkRecovery.parsePolicy(Data(#"{"force_off":false}"#.utf8)) == .allowed)
        precondition(NetworkRecovery.parsePolicy(Data(#"{"force_off":1}"#.utf8)) == .invalid)
        precondition(NetworkRecovery.parsePolicy(Data("garbled".utf8)) == .invalid)
        precondition(NetworkRecovery.classifyProbe(status: 0, text: "<TITLE>Success</TITLE>", expected: "<TITLE>Success</TITLE>") == .connected)
        precondition(NetworkRecovery.classifyProbe(status: 0, text: "Sign in to this hotspot", expected: "Success") == .inconclusive)
        precondition(NetworkRecovery.classifyProbe(status: 6, text: "Could not resolve host", expected: "Success") == .inconclusive)
        precondition(NetworkRecovery.classifyProbe(status: 35, text: "SSL connect error", expected: "Success") == .inconclusive)
        precondition(NetworkRecovery.classifyProbe(status: 7, text: "Connection failed", expected: "Success") == .disconnected)
        precondition(NetworkRecovery.classifyProbe(status: 28, text: "Timed out", expected: "Success") == .disconnected)
        print("NetworkRecovery fixtures passed: dynamic hardware identity, disabled/manual/force-off preservation, DHCP and carrier readiness")
    }
}
