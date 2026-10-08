import Foundation
import IOKit
import IOKit.network
import IOKit.usb
import SystemConfiguration

private struct USBIdentity: Equatable {
    let vendorID: Int
    let productID: Int
}

private func usbIdentity(forBSDName name: String) -> USBIdentity? {
    guard let matching = IOBSDNameMatching(kIOMainPortDefault, 0, name) else {
        return nil
    }
    let interface = IOServiceGetMatchingService(kIOMainPortDefault, matching)
    guard interface != IO_OBJECT_NULL else { return nil }
    defer { IOObjectRelease(interface) }

    let options = IOOptionBits(kIORegistryIterateParents | kIORegistryIterateRecursively)
    func integer(_ key: String) -> Int? {
        (IORegistryEntrySearchCFProperty(
            interface,
            kIOServicePlane,
            key as CFString,
            kCFAllocatorDefault,
            options
        ) as? NSNumber)?.intValue
    }

    guard let vendorID = integer(kUSBVendorID),
          let productID = integer(kUSBProductID) else {
        return nil
    }
    return USBIdentity(vendorID: vendorID, productID: productID)
}

private let supported = [
    USBIdentity(vendorID: 0x2CA3, productID: 0x4006),
    USBIdentity(vendorID: 0x2C7C, productID: 0x0125),
]
let interfaces = (SCNetworkInterfaceCopyAll() as? [SCNetworkInterface]) ?? []
if CommandLine.arguments.contains("--probe") {
    for interface in interfaces {
        let name = (SCNetworkInterfaceGetBSDName(interface) as String?) ?? "-"
        let type = (SCNetworkInterfaceGetInterfaceType(interface) as String?) ?? "-"
        let identity = usbIdentity(forBSDName: name)
        let usb = identity.map {
            String(format: "%04x:%04x", $0.vendorID, $0.productID)
        } ?? "-"
        print("\(name)\t\(type)\t\(usb)")
    }
    exit(0)
}

guard geteuid() == 0 else {
    fputs("must run as root\n", stderr)
    exit(2)
}

if let rebindIndex = CommandLine.arguments.firstIndex(of: "--rebind"),
   CommandLine.arguments.indices.contains(rebindIndex + 1) {
    let targetBSDName = CommandLine.arguments[rebindIndex + 1]
    guard targetBSDName.range(of: #"^en[0-9]+$"#, options: .regularExpression) != nil else {
        fputs("invalid target BSD name\n", stderr)
        exit(10)
    }
    guard let preferences = SCPreferencesCreate(
        nil,
        "DJOneHub QDC507 Network Rebind" as NSString,
        nil
    ), SCPreferencesLock(preferences, true) else {
        fputs("cannot lock network preferences\n", stderr)
        exit(11)
    }
    defer { SCPreferencesUnlock(preferences) }
    guard let networkSet = SCNetworkSetCopyCurrent(preferences),
          let services = SCNetworkSetCopyServices(networkSet) as? [SCNetworkService],
          let service = services.first(where: {
              (SCNetworkServiceGetName($0) as String?) == "Baiwang"
          }),
          let serviceID = SCNetworkServiceGetServiceID(service) as String? else {
        fputs("existing Baiwang network service not found\n", stderr)
        exit(12)
    }

    let path = "/NetworkServices/\(serviceID)/Interface" as CFString
    guard let raw = SCPreferencesPathGetValue(preferences, path) as? [String: Any] else {
        fputs("cannot read Baiwang interface configuration\n", stderr)
        exit(13)
    }
    var updated = raw
    let oldBSDName = raw[kSCPropNetInterfaceDeviceName as String] as? String ?? "-"
    updated[kSCPropNetInterfaceDeviceName as String] = targetBSDName
    guard SCPreferencesPathSetValue(preferences, path, updated as CFDictionary),
          SCPreferencesCommitChanges(preferences),
          SCPreferencesApplyChanges(preferences) else {
        fputs("cannot rebind Baiwang network service\n", stderr)
        exit(14)
    }
    print("\(oldBSDName) -> \(targetBSDName)")
    exit(0)
}

guard let interface = interfaces.first(where: {
    guard (SCNetworkInterfaceGetInterfaceType($0) as String?) ==
            (kSCNetworkInterfaceTypeEthernet as String),
          let name = SCNetworkInterfaceGetBSDName($0) as String?,
          let identity = usbIdentity(forBSDName: name) else {
        return false
    }
    return supported.contains(identity)
}) else {
    fputs("live QDC507 ECM interface not found\n", stderr)
    exit(3)
}

guard let bsdName = SCNetworkInterfaceGetBSDName(interface) as String? else {
    fputs("QDC507 interface has no BSD name\n", stderr)
    exit(4)
}
guard let preferences = SCPreferencesCreate(
    nil,
    "DJOneHub QDC507 Network Service" as NSString,
    nil
) else {
    fputs("cannot create network preferences\n", stderr)
    exit(5)
}
guard SCPreferencesLock(preferences, true) else {
    fputs("cannot lock network preferences\n", stderr)
    exit(6)
}
defer { SCPreferencesUnlock(preferences) }
guard let networkSet = SCNetworkSetCopyCurrent(preferences) else {
    fputs("cannot find current network set\n", stderr)
    exit(7)
}

let services = (SCNetworkSetCopyServices(networkSet) as? [SCNetworkService]) ?? []
let existing = services.first(where: {
    guard let serviceInterface = SCNetworkServiceGetInterface($0),
          let name = SCNetworkInterfaceGetBSDName(serviceInterface) as String? else {
        return false
    }
    return name == bsdName
})

let service: SCNetworkService
if let existing {
    service = existing
} else {
    guard let created = SCNetworkServiceCreate(preferences, interface),
          SCNetworkServiceEstablishDefaultConfiguration(created),
          SCNetworkServiceSetName(created, "Baiwang Concurrent" as NSString),
          SCNetworkSetAddService(networkSet, created) else {
        fputs("cannot create QDC507 network service\n", stderr)
        exit(8)
    }
    service = created
}

guard SCNetworkServiceSetEnabled(service, true),
      SCPreferencesCommitChanges(preferences),
      SCPreferencesApplyChanges(preferences) else {
    fputs("cannot save QDC507 network service\n", stderr)
    exit(9)
}

_ = SCNetworkInterfaceForceConfigurationRefresh(interface)
print(bsdName)
