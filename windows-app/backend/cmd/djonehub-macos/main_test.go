package main

import "testing"

func TestPortScore(t *testing.T) {
	tests := []struct {
		name string
		port string
		want int
	}{
		{name: "named Quectel port", port: "/dev/cu.Quectel-AT", want: 100},
		{name: "usb modem", port: "/dev/cu.usbmodem2101", want: 80},
		{name: "usb serial", port: "/dev/cu.usbserial-1420", want: 60},
		{name: "bluetooth", port: "/dev/cu.Bluetooth-Incoming-Port", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := portScore(tt.port); got != tt.want {
				t.Fatalf("portScore(%q) = %d, want %d", tt.port, got, tt.want)
			}
		})
	}
}

func TestParseUSBNetMode(t *testing.T) {
	for _, tt := range []struct {
		response string
		want     string
	}{
		{response: "AT+QCFG=\"usbnet\"\r\n+QCFG: \"usbnet\",0\r\nOK", want: "0"},
		{response: "+QCFG: \"usbnet\",1\r\nOK", want: "1"},
		{response: "ERROR", want: ""},
	} {
		if got := parseUSBNetMode(tt.response); got != tt.want {
			t.Fatalf("parseUSBNetMode(%q) = %q, want %q", tt.response, got, tt.want)
		}
	}
}

func TestParseMacNetworkServices(t *testing.T) {
	input := `An asterisk (*) denotes that a network service is disabled.
(1) Wi-Fi
(Hardware Port: Wi-Fi, Device: en0)

(2) Baiwang 2
(Hardware Port: Baiwang, Device: en8)

(*) Baiwang
(Hardware Port: Baiwang, Device: en10)
`
	services := parseMacNetworkServices(input)
	if len(services) != 3 {
		t.Fatalf("service count = %d, want 3", len(services))
	}
	if services[1].Name != "Baiwang 2" || services[1].Device != "en8" ||
		services[1].Disabled || !isDJICellularService(services[1]) {
		t.Fatalf("active cellular service = %+v", services[1])
	}
	if !services[2].Disabled || !isDJICellularService(services[2]) {
		t.Fatalf("disabled cellular service = %+v", services[2])
	}
}

func TestParseMacIPv4ServiceInfo(t *testing.T) {
	info := parseMacIPv4ServiceInfo(`DHCP Configuration
IP address: 192.168.225.29
Subnet mask: 255.255.255.0
Router: 192.168.225.1
`)
	if info.Address != "192.168.225.29" || info.Subnet != "255.255.255.0" {
		t.Fatalf("IPv4 service info = %+v", info)
	}
}

func TestInitUSBATESIMManagerAfterDelayedUSBOpen(t *testing.T) {
	instance := &app{}

	manager, switchAllowed := instance.currentESIMManager()
	if manager != nil || switchAllowed {
		t.Fatalf("initial eSIM state = (%v, %v), want unavailable", manager, switchAllowed)
	}

	instance.initUSBATESIMManager()
	manager, switchAllowed = instance.currentESIMManager()
	if manager == nil {
		t.Fatal("USB AT recovery did not initialize the eSIM manager")
	}
	if !switchAllowed {
		t.Fatal("USB AT eSIM manager should allow profile switching")
	}

	instance.initUSBATESIMManager()
	managerAgain, _ := instance.currentESIMManager()
	if managerAgain != manager {
		t.Fatal("repeated USB AT recovery replaced the existing eSIM manager")
	}
}
