package iphlp

import (
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modIphlpapi           = windows.NewLazySystemDLL("iphlpapi.dll")
	procInitUnicastAddr   = modIphlpapi.NewProc("InitializeUnicastIpAddressEntry")
	procCreateUnicastAddr = modIphlpapi.NewProc("CreateUnicastIpAddressEntry")
	procDeleteUnicastAddr = modIphlpapi.NewProc("DeleteUnicastIpAddressEntry")
	procSetIPInterface    = modIphlpapi.NewProc("SetIpInterfaceEntry")
	procInitForward       = modIphlpapi.NewProc("InitializeIpForwardEntry")
	procCreateForward     = modIphlpapi.NewProc("CreateIpForwardEntry2")
	procDeleteForward     = modIphlpapi.NewProc("DeleteIpForwardEntry2")
	procSetDNS            = modIphlpapi.NewProc("SetInterfaceDnsSettings")
	procLuidToGUID        = modIphlpapi.NewProc("ConvertInterfaceLuidToGuid")
	procIndexToLuid       = modIphlpapi.NewProc("ConvertInterfaceIndexToLuid")
)

// Iface names an interface by LUID (preferred, known at once for an adapter
// the hub created) or, when LUID is 0, by index.
type Iface struct {
	LUID  uint64
	Index uint32
}

func (i Iface) String() string {
	if i.LUID != 0 {
		return fmt.Sprintf("luid %#x", i.LUID)
	}
	return fmt.Sprintf("index %d", i.Index)
}

func (i Iface) matches(luid uint64, index uint32) bool {
	if i.LUID != 0 {
		return luid == i.LUID
	}
	return index == i.Index
}

// call runs an IP Helper function that returns a Win32 error code (NO_ERROR = 0).
func call(p *windows.LazyProc, args ...uintptr) error {
	r, _, _ := p.Call(args...)
	if r != 0 {
		return windows.Errno(r)
	}
	return nil
}

// AddAddress adds an IPv4 address to the interface (active store only; it
// goes with the adapter). An address that exists already is success.
func AddAddress(ifc Iface, p netip.Prefix) error {
	f, err := addressFields(p)
	if err != nil {
		return err
	}
	var row windows.MibUnicastIpAddressRow
	_, _, _ = procInitUnicastAddr.Call(uintptr(unsafe.Pointer(&row)))
	row.InterfaceLuid, row.InterfaceIndex = ifc.LUID, ifc.Index
	*(*sockaddrInet)(unsafe.Pointer(&row.Address)) = f.addr
	row.OnLinkPrefixLength = f.onLink
	row.DadState = 4 // IpDadStatePreferred: no duplicate address detection delay
	err = call(procCreateUnicastAddr, uintptr(unsafe.Pointer(&row)))
	if err != nil && !errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
		return fmt.Errorf("iphlp: add address %v on %v: %w", p, ifc, err)
	}
	return nil
}

// SetAddress makes p the only IPv4 address of the interface (like netsh
// "set address source=static").
func SetAddress(ifc Iface, p netip.Prefix) error {
	have, err := Addresses(ifc)
	if err != nil {
		return err
	}
	for _, a := range have {
		if a != p {
			if err := DeleteAddress(ifc, a); err != nil {
				return err
			}
		}
	}
	return AddAddress(ifc, p)
}

// DeleteAddress removes an IPv4 address; a missing one is success.
func DeleteAddress(ifc Iface, p netip.Prefix) error {
	f, err := addressFields(p)
	if err != nil {
		return err
	}
	var row windows.MibUnicastIpAddressRow
	_, _, _ = procInitUnicastAddr.Call(uintptr(unsafe.Pointer(&row)))
	row.InterfaceLuid, row.InterfaceIndex = ifc.LUID, ifc.Index
	*(*sockaddrInet)(unsafe.Pointer(&row.Address)) = f.addr
	row.OnLinkPrefixLength = f.onLink
	err = call(procDeleteUnicastAddr, uintptr(unsafe.Pointer(&row)))
	if err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
		return fmt.Errorf("iphlp: delete address %v on %v: %w", p, ifc, err)
	}
	return nil
}

// Addresses lists the IPv4 addresses of the interface.
func Addresses(ifc Iface) ([]netip.Prefix, error) {
	var tab *windows.MibUnicastIpAddressTable
	if err := windows.GetUnicastIpAddressTable(windows.AF_INET, &tab); err != nil {
		return nil, fmt.Errorf("iphlp: address table: %w", err)
	}
	defer windows.FreeMibTable(unsafe.Pointer(tab))
	var out []netip.Prefix
	for _, r := range unsafe.Slice(&tab.Table[0], tab.NumEntries) {
		if !ifc.matches(r.InterfaceLuid, r.InterfaceIndex) {
			continue
		}
		if a, ok := addr4(*(*sockaddrInet)(unsafe.Pointer(&r.Address))); ok {
			out = append(out, netip.PrefixFrom(a, int(r.OnLinkPrefixLength)))
		}
	}
	return out, nil
}

// AddRoute adds an IPv4 route through the interface (active store only).
// A route that exists already is success.
func AddRoute(ifc Iface, r Route) error {
	row, err := routeRow(ifc, r)
	if err != nil {
		return err
	}
	err = call(procCreateForward, uintptr(unsafe.Pointer(&row)))
	if err != nil && !errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
		return fmt.Errorf("iphlp: add route %v on %v: %w", r.Dst, ifc, err)
	}
	return nil
}

// DeleteRoute removes an IPv4 route; a missing one is success.
func DeleteRoute(ifc Iface, r Route) error {
	row, err := routeRow(ifc, r)
	if err != nil {
		return err
	}
	err = call(procDeleteForward, uintptr(unsafe.Pointer(&row)))
	if err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
		return fmt.Errorf("iphlp: delete route %v on %v: %w", r.Dst, ifc, err)
	}
	return nil
}

func routeRow(ifc Iface, r Route) (windows.MibIpForwardRow2, error) {
	var row windows.MibIpForwardRow2
	f, err := r.fields()
	if err != nil {
		return row, err
	}
	_, _, _ = procInitForward.Call(uintptr(unsafe.Pointer(&row)))
	row.InterfaceLuid, row.InterfaceIndex = ifc.LUID, ifc.Index
	*(*sockaddrInet)(unsafe.Pointer(&row.DestinationPrefix.Prefix)) = f.dst
	row.DestinationPrefix.PrefixLength = f.dstLen
	*(*sockaddrInet)(unsafe.Pointer(&row.NextHop)) = f.nextHop
	row.Metric = f.metric
	row.Protocol = windows.MIB_IPPROTO_NETMGMT
	return row, nil
}

// Routes lists the IPv4 routes through the interface.
func Routes(ifc Iface) ([]Route, error) {
	var t *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &t); err != nil {
		return nil, fmt.Errorf("iphlp: route table: %w", err)
	}
	defer windows.FreeMibTable(unsafe.Pointer(t))
	var out []Route
	for _, r := range t.Rows() {
		if !ifc.matches(r.InterfaceLuid, r.InterfaceIndex) {
			continue
		}
		dst, ok := addr4(*(*sockaddrInet)(unsafe.Pointer(&r.DestinationPrefix.Prefix)))
		if !ok {
			continue
		}
		hop, _ := addr4(*(*sockaddrInet)(unsafe.Pointer(&r.NextHop)))
		out = append(out, Route{Dst: netip.PrefixFrom(dst, int(r.DestinationPrefix.PrefixLength)),
			NextHop: hop, Metric: r.Metric})
	}
	return out, nil
}

// Interface reads the IPv4 settings of the interface.
func Interface(ifc Iface) (Settings, error) {
	row, err := ipInterface(ifc)
	if err != nil {
		return Settings{}, err
	}
	return Settings{MTU: row.NlMtu, Metric: row.Metric, Forwarding: row.ForwardingEnabled != 0}, nil
}

// Configure applies s to the interface's IPv4 settings and reports whether
// anything had to change.
func Configure(ifc Iface, s Settings) (bool, error) {
	row, err := ipInterface(ifc)
	if err != nil {
		return false, err
	}
	f := ifaceFields{nlMtu: row.NlMtu, useAutomaticMetric: row.UseAutomaticMetric,
		metric: row.Metric, forwardingEnabled: row.ForwardingEnabled}
	if !s.apply(&f) {
		return false, nil
	}
	row.NlMtu, row.UseAutomaticMetric, row.Metric, row.ForwardingEnabled =
		f.nlMtu, f.useAutomaticMetric, f.metric, f.forwardingEnabled
	row.SitePrefixLength = 0 // must be 0 for IPv4 on Set
	if err := call(procSetIPInterface, uintptr(unsafe.Pointer(row))); err != nil {
		return false, fmt.Errorf("iphlp: set interface %v: %w", ifc, err)
	}
	return true, nil
}

func ipInterface(ifc Iface) (*windows.MibIpInterfaceRow, error) {
	row := &windows.MibIpInterfaceRow{Family: windows.AF_INET, InterfaceLuid: ifc.LUID, InterfaceIndex: ifc.Index}
	if err := windows.GetIpInterfaceEntry(row); err != nil {
		return nil, fmt.Errorf("iphlp: interface %v: %w", ifc, err)
	}
	return row, nil
}

// dnsInterfaceSettings is DNS_INTERFACE_SETTINGS (netioapi.h, version 1).
type dnsInterfaceSettings struct {
	Version             uint32
	Flags               uint64
	Domain              *uint16
	NameServer          *uint16
	SearchList          *uint16
	RegistrationEnabled uint32
	RegisterAdapterName uint32
	EnableLLMNR         uint32
	QueryAdapterName    uint32
	ProfileNameServer   *uint16
}

const (
	dnsSettingsVersion1  = 1
	dnsSettingNameServer = 0x0002
)

// SetDNS sets the interface's IPv4 DNS servers (Windows 10 2004 and later).
// Mobile Hotspot hands its clients a DNS server only when the shared
// connection has one.
func SetDNS(ifc Iface, servers []netip.Addr) error {
	ns, err := nameServers(servers)
	if err != nil {
		return err
	}
	if err := procSetDNS.Find(); err != nil {
		return fmt.Errorf("iphlp: SetInterfaceDnsSettings: %w", err)
	}
	luid := ifc.LUID
	if luid == 0 {
		if err := call(procIndexToLuid, uintptr(ifc.Index), uintptr(unsafe.Pointer(&luid))); err != nil {
			return fmt.Errorf("iphlp: luid of %v: %w", ifc, err)
		}
	}
	var guid windows.GUID
	if err := call(procLuidToGUID, uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&guid))); err != nil {
		return fmt.Errorf("iphlp: guid of %v: %w", ifc, err)
	}
	p, err := windows.UTF16PtrFromString(ns)
	if err != nil {
		return err
	}
	set := dnsInterfaceSettings{Version: dnsSettingsVersion1, Flags: dnsSettingNameServer, NameServer: p}
	// The GUID is passed by value: amd64 passes a 16-byte struct by
	// reference, arm64 in two registers.
	var args []uintptr
	switch runtime.GOARCH {
	case "amd64":
		args = []uintptr{uintptr(unsafe.Pointer(&guid))}
	case "arm64":
		g := (*[2]uint64)(unsafe.Pointer(&guid))
		args = []uintptr{uintptr(g[0]), uintptr(g[1])}
	default:
		return fmt.Errorf("iphlp: SetDNS not supported on %s", runtime.GOARCH)
	}
	args = append(args, uintptr(unsafe.Pointer(&set)))
	if err := call(procSetDNS, args...); err != nil {
		return fmt.Errorf("iphlp: set DNS %q on %v: %w", ns, ifc, err)
	}
	return nil
}

// LUID returns the interface's LUID (ifc.LUID, else converted from its index).
func LUID(ifc Iface) (uint64, error) {
	luid := ifc.LUID
	if luid == 0 {
		if err := call(procIndexToLuid, uintptr(ifc.Index), uintptr(unsafe.Pointer(&luid))); err != nil {
			return 0, fmt.Errorf("iphlp: luid of %v: %w", ifc, err)
		}
	}
	return luid, nil
}

// DNS reads the interface's IPv4 DNS servers (test and diagnostics).
func DNS(ifc Iface) ([]netip.Addr, error) {
	var size uint32 = 15000
	for range 3 {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_INET, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST, 0, aa, &size)
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("iphlp: adapters: %w", err)
		}
		for a := aa; a != nil; a = a.Next {
			if !ifc.matches(a.Luid, a.IfIndex) {
				continue
			}
			var out []netip.Addr
			for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
				if ip, ok := netip.AddrFromSlice(d.Address.IP()); ok {
					out = append(out, ip.Unmap())
				}
			}
			return out, nil
		}
		return nil, fmt.Errorf("iphlp: no adapter with %v", ifc)
	}
	return nil, errors.New("iphlp: adapter list keeps growing")
}
