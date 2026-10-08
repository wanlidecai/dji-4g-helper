//go:build windows

package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/iniwex5/vohive/internal/modem"
)

const windowsDJIHardwarePort = "DJI USB (VID_2CA3)"

// CIM's USB hardware identity is authoritative. An interface name alone may
// have been renamed or reused by a completely different network adapter.
const windowsDJIAdapterFilter = `$_.PNPDeviceID -match '^USB\\VID_2CA3(&|\\)'`

type windowsAdapter struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	PNPID       string `json:"pnp_id"`
	Index       int    `json:"index"`
	Enabled     bool   `json:"enabled"`
	Status      int    `json:"status"`
	IPv4        string `json:"ipv4"`
	DHCPEnabled bool   `json:"dhcp_enabled"`
	HasDefault  bool   `json:"has_default"`
}

func hiddenPowerShell(script string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	// -EncodedCommand preserves non-ASCII adapter aliases without shell quoting.
	units := utf16.Encode([]rune(`$ErrorActionPreference='Stop'; [Console]::OutputEncoding=[System.Text.UTF8Encoding]::new($false); ` + script))
	encoded := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(encoded[i*2:], unit)
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(message)
	}
	return out, nil
}

func psLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func windowsAdapterSelection(name string) string {
	return `$a = @(Get-CimInstance Win32_NetworkAdapter | Where-Object { ` + windowsDJIAdapterFilter + ` -and $_.NetConnectionID -eq ` + psLiteral(name) + ` }); if ($a.Count -ne 1) { throw '未找到唯一的 DJI USB 网卡，未更改网络设置' }; $idx = [uint32]$a[0].InterfaceIndex; `
}

func discoverWindowsATPort() (string, error) {
	out, err := hiddenPowerShell(`$ports = @(Get-CimInstance Win32_SerialPort | Where-Object { $_.PNPDeviceID -match '^USB\\VID_2CA3(&|\\)' } | ForEach-Object { [pscustomobject]@{ port=$_.DeviceID; description=$_.Name } }); ConvertTo-Json -InputObject $ports -Compress`)
	if err != nil {
		return "", fmt.Errorf("读取 DJI AT 串口失败: %w", err)
	}
	var ports []struct {
		Port        string `json:"port"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &ports); err != nil {
		return "", err
	}
	sort.SliceStable(ports, func(i, j int) bool {
		return strings.Contains(strings.ToUpper(ports[i].Description), " AT ") && !strings.Contains(strings.ToUpper(ports[j].Description), " AT ")
	})
	for _, port := range ports {
		if _, err := modem.ProbeIMEICached(port.Port, 3*time.Second); err == nil {
			return port.Port, nil
		}
	}
	return "", errors.New("未找到 DJI AT 串口；可安装 DJI/Quectel USB 驱动，或使用 USB AT 通道")
}

func discoverWindowsUSBDevice() *usbDeviceStatus {
	out, err := hiddenPowerShell(`$devices = @(Get-CimInstance Win32_PnPEntity | Where-Object { $_.PNPDeviceID -match '^USB\\VID_2CA3&PID_' -and $_.ConfigManagerErrorCode -ne 45 }); $device = $devices | Where-Object { $_.PNPDeviceID -match 'PID_4006' -or $_.Name -match '4G|QDC507|Quectel|Baiwang|Remote NDIS|RNDIS' } | Select-Object -First 1; if ($null -ne $device) { [pscustomobject]@{ product=$device.Name; vendor=$device.Manufacturer; pnp_id=$device.PNPDeviceID } | ConvertTo-Json -Compress }`)
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return nil
	}
	var device struct {
		Product string `json:"product"`
		Vendor  string `json:"vendor"`
		PNPID   string `json:"pnp_id"`
	}
	if json.Unmarshal(out, &device) != nil {
		return nil
	}
	pid := regexp.MustCompile(`(?i)PID_([0-9a-f]{4})`).FindStringSubmatch(device.PNPID)
	productID := ""
	if len(pid) == 2 {
		productID = strings.ToLower(pid[1])
	}
	return &usbDeviceStatus{
		Product: device.Product, Vendor: device.Vendor, VendorID: "2ca3",
		ProductID: productID, LocationID: device.PNPID, Mode: "Windows USB / RNDIS",
	}
}

func discoverWindowsAdapters() ([]windowsAdapter, error) {
	out, err := hiddenPowerShell(`$configs=@(Get-CimInstance Win32_NetworkAdapterConfiguration); $routes=@(Get-NetRoute -AddressFamily IPv4); $items = @(Get-CimInstance Win32_NetworkAdapter | Where-Object { $_.NetConnectionID } | ForEach-Object { $adapter=$_; $config=$configs | Where-Object { $_.InterfaceIndex -eq $adapter.InterfaceIndex } | Select-Object -First 1; $ip=@($config.IPAddress | Where-Object { $_ -match '^\d+\.\d+\.\d+\.\d+$' -and $_ -notlike '169.254.*' }) | Select-Object -First 1; $default=@($routes | Where-Object { $_.InterfaceIndex -eq $adapter.InterfaceIndex -and $_.DestinationPrefix -eq '0.0.0.0/0' }); [pscustomobject]@{ name=$adapter.NetConnectionID; description=$adapter.Name; pnp_id=$adapter.PNPDeviceID; index=[int]$adapter.InterfaceIndex; enabled=[bool]$adapter.NetEnabled; status=[int]$adapter.NetConnectionStatus; ipv4=[string]$ip; dhcp_enabled=[bool]$config.DHCPEnabled; has_default=($default.Count -gt 0) } }); ConvertTo-Json -InputObject $items -Compress`)
	if err != nil {
		return nil, err
	}
	var adapters []windowsAdapter
	if err := json.Unmarshal(out, &adapters); err != nil {
		return nil, err
	}
	return adapters, nil
}

func isWindowsDJIAdapter(adapter windowsAdapter) bool {
	return regexp.MustCompile(`(?i)^USB\\VID_2CA3(?:&|\\)`).MatchString(adapter.PNPID)
}

func discoverWindowsNetworkServices() ([]macNetworkService, error) {
	adapters, err := discoverWindowsAdapters()
	if err != nil {
		return nil, err
	}
	var services []macNetworkService
	for _, adapter := range adapters {
		if isWindowsDJIAdapter(adapter) {
			services = append(services, macNetworkService{Name: adapter.Name, HardwarePort: windowsDJIHardwarePort, Device: strconv.Itoa(adapter.Index), Disabled: !adapter.Enabled})
		}
	}
	return services, nil
}

func setWindowsNetworkServiceEnabled(name string, enabled bool) error {
	operation := "Disable-NetAdapter"
	if enabled {
		operation = "Enable-NetAdapter"
	}
	_, err := hiddenPowerShell(windowsAdapterSelection(name) + `Get-NetAdapter -InterfaceIndex $idx | ` + operation + ` -Confirm:$false`)
	return err
}

func renewWindowsNetworkServiceDHCP(name string) error {
	// WMI renews only this verified USB adapter's lease. ipconfig /renew without
	// an interface argument would also disturb Wi-Fi and other wired adapters.
	_, err := hiddenPowerShell(windowsAdapterSelection(name) + `
Get-NetAdapter -InterfaceIndex $idx | Enable-NetAdapter -Confirm:$false;
Set-NetIPInterface -InterfaceIndex $idx -AddressFamily IPv4 -IgnoreDefaultRoutes Disabled -Dhcp Enabled;
Set-NetIPInterface -InterfaceIndex $idx -AddressFamily IPv6 -IgnoreDefaultRoutes Disabled -ErrorAction SilentlyContinue;
Set-DnsClientServerAddress -InterfaceIndex $idx -ResetServerAddresses;
$config = Get-CimInstance Win32_NetworkAdapterConfiguration | Where-Object { $_.InterfaceIndex -eq $idx };
if ($null -ne $config) { $result = Invoke-CimMethod -InputObject $config -MethodName EnableDHCP; if ($result.ReturnValue -gt 1) { throw ('DHCP 启用失败: ' + $result.ReturnValue) }; $result = Invoke-CimMethod -InputObject $config -MethodName RenewDHCPLease; if ($result.ReturnValue -gt 1) { throw ('DHCP 续租失败: ' + $result.ReturnValue) } }`)
	return err
}

func renewWindowsNetworkServiceAfterResume(name string) (bool, error) {
	out, err := hiddenPowerShell(windowsAdapterSelection(name) + `
if (-not $a[0].NetEnabled) { [pscustomobject]@{ renewed=$false; reason='disabled' } | ConvertTo-Json -Compress; exit 0 };
$config = Get-CimInstance Win32_NetworkAdapterConfiguration | Where-Object { $_.InterfaceIndex -eq $idx } | Select-Object -First 1;
if ($null -eq $config -or -not $config.DHCPEnabled) { [pscustomobject]@{ renewed=$false; reason='static' } | ConvertTo-Json -Compress; exit 0 };
$result = Invoke-CimMethod -InputObject $config -MethodName RenewDHCPLease;
if ($result.ReturnValue -gt 1) { throw ('DJI DHCP 续租失败: ' + $result.ReturnValue) };
[pscustomobject]@{ renewed=$true } | ConvertTo-Json -Compress`)
	if err != nil {
		return false, err
	}
	var result struct {
		Renewed bool `json:"renewed"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return false, err
	}
	return result.Renewed, nil
}

// A successful inventory can establish offline state. An inventory error,
// a manually disabled adapter, or a static configuration cannot authorize reset.
func windowsCellularProbeInterface() (string, net.IP, connectivityState) {
	adapters, err := discoverWindowsAdapters()
	if err != nil {
		return "", nil, connectivityInconclusive
	}
	found, recoverable := false, false
	for _, adapter := range adapters {
		if !isWindowsDJIAdapter(adapter) {
			continue
		}
		found = true
		if !adapter.Enabled || !adapter.DHCPEnabled {
			continue
		}
		recoverable = true
		address := net.ParseIP(adapter.IPv4)
		if adapter.Status == 2 && adapter.HasDefault && address != nil && !address.IsUnspecified() && !address.IsLinkLocalUnicast() {
			return adapter.Name, address, connectivityOnline
		}
	}
	if found && !recoverable {
		return "", nil, connectivityInconclusive
	}
	return "", nil, connectivityOffline
}

func blockWindowsNetworkServiceRoute(name string) error {
	_, err := hiddenPowerShell(windowsAdapterSelection(name) + `
Set-NetIPInterface -InterfaceIndex $idx -AddressFamily IPv4 -IgnoreDefaultRoutes Enabled;
Set-NetIPInterface -InterfaceIndex $idx -AddressFamily IPv6 -IgnoreDefaultRoutes Enabled -ErrorAction SilentlyContinue;
Get-NetRoute -InterfaceIndex $idx -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false;
Get-NetRoute -InterfaceIndex $idx -DestinationPrefix '::/0' -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false`)
	return err
}

func (a *app) applyWindowsCellularPolicyLocked() error {
	services, err := discoverWindowsNetworkServices()
	if err != nil {
		return err
	}
	var failures []string
	if a.force4GOff {
		if len(services) == 0 {
			return errors.New("未找到 DJI RNDIS 网卡；Windows 上网模式需要 usbnet=3 和 RNDIS 驱动")
		}
		for _, service := range services {
			if err := blockWindowsNetworkServiceRoute(service.Name); err != nil {
				failures = append(failures, err.Error())
			} else {
				a.disabled4GServices = appendUnique(a.disabled4GServices, service.Name)
			}
		}
	} else {
		var pending []string
		for _, name := range a.disabled4GServices {
			present := false
			for _, service := range services {
				if service.Name == name {
					present = true
					break
				}
			}
			// A disconnected adapter is kept pending. A reused alias is never
			// passed to a mutation unless its USB VID is verified again.
			if !present {
				pending = append(pending, name)
				continue
			}
			if err := renewWindowsNetworkServiceDHCP(name); err != nil {
				pending = append(pending, name)
				failures = append(failures, err.Error())
			}
		}
		a.disabled4GServices = pending
	}
	if err := a.persistNetworkPolicyLocked(); err != nil {
		failures = append(failures, err.Error())
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func discoverWindowsNetworkInterfaces() []macNetInterface {
	adapters, err := discoverWindowsAdapters()
	if err != nil {
		return nil
	}
	var interfaces []macNetInterface
	for _, adapter := range adapters {
		status, kind := "inactive", "other"
		if adapter.Status == 2 && adapter.Enabled {
			status = "active"
		}
		if isWindowsDJIAdapter(adapter) {
			kind = "dji-usb"
		}
		interfaces = append(interfaces, macNetInterface{Name: adapter.Name, Status: status, IPv4: adapter.IPv4, Kind: kind})
	}
	return interfaces
}

func discoverWindowsDefaultRoute() macDefaultRoute {
	out, err := hiddenPowerShell(`$routes=@(Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue | ForEach-Object { $route=$_; $ip=Get-NetIPInterface -InterfaceIndex $route.InterfaceIndex -AddressFamily IPv4; if ($ip.ConnectionState -eq 'Connected') { [pscustomobject]@{ interface=$route.InterfaceAlias; gateway=$route.NextHop; metric=($route.RouteMetric + $ip.InterfaceMetric) } } } | Sort-Object metric); if ($routes.Count -gt 0) { $routes[0] | ConvertTo-Json -Compress }`)
	if err != nil {
		return macDefaultRoute{}
	}
	var route macDefaultRoute
	_ = json.Unmarshal(out, &route)
	return route
}

func discoverWindowsInterfaceCounters() (map[string]networkByteCounters, error) {
	out, err := hiddenPowerShell(`$items=@(Get-CimInstance Win32_NetworkAdapter | Where-Object { ` + windowsDJIAdapterFilter + ` -and $_.NetConnectionID } | ForEach-Object { $name=$_.NetConnectionID; $stats=Get-NetAdapter -InterfaceIndex $_.InterfaceIndex | Get-NetAdapterStatistics; [pscustomobject]@{ name=$name; rx=[uint64]$stats.ReceivedBytes; tx=[uint64]$stats.SentBytes } }); ConvertTo-Json -InputObject $items -Compress`)
	if err != nil {
		return nil, err
	}
	var items []struct {
		Name string `json:"name"`
		RX   uint64 `json:"rx"`
		TX   uint64 `json:"tx"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, err
	}
	counters := make(map[string]networkByteCounters)
	for _, item := range items {
		counters[item.Name] = networkByteCounters{RX: item.RX, TX: item.TX}
	}
	return counters, nil
}

// IP_UNICAST_IF selects the interface for this probe socket only. Microsoft
// specifies that the interface index must be supplied in network byte order.
// https://learn.microsoft.com/en-us/windows/win32/winsock/ipproto-ip-socket-options
func bindWindowsCellularProbe(dialer *net.Dialer, name string) error {
	intf, err := net.InterfaceByName(name)
	if err != nil {
		return err
	}
	index := int(bits.ReverseBytes32(uint32(intf.Index)))
	dialer.Control = func(network, address string, raw syscall.RawConn) error {
		var socketErr error
		if err := raw.Control(func(fd uintptr) { socketErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IP, 31, index) }); err != nil {
			return err
		}
		return socketErr
	}
	return nil
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
	// This classifier is used only after the probe socket has been explicitly
	// bound to the verified module interface. DNS is excluded above; a typed
	// TCP-connect timeout can establish the stale-adapter black-hole case.
	if operation.Timeout() {
		return true
	}
	// WSAENETDOWN, WSAENETUNREACH, WSAEHOSTDOWN, WSAEHOSTUNREACH.
	for _, code := range []syscall.Errno{10050, 10051, 10064, 10065} {
		if errors.Is(err, code) {
			return true
		}
	}
	return false
}
