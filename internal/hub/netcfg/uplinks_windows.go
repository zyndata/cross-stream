package netcfg

import (
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/zyndata/cross-stream/internal/hub/uplink"
)

// RPFilter is false on Windows: there is no reverse path filter to loosen.
const RPFilter = false

// Interface types of IP Helper (ipifcons.h) not named in x/sys.
const (
	ifTypeWWANPP  = 243
	ifTypeWWANPP2 = 244
)

// adapter is the part of an IP Helper adapter record the scan looks at.
type adapter struct {
	Name, Description string
	IfType, Metric    uint32
	Up                bool
	MAC               net.HardwareAddr
	Addr              netip.Prefix
	Gateway           netip.Addr
}

// ScanUplinks lists the adapters that qualify as local uplinks: up, not
// virtual, not in exclude (hotspot adapter, tunnel), with an IPv4 address
// and a default gateway. Names are the adapter aliases ("Ethernet 3", or
// what the user renamed it to).
func ScanUplinks(exclude []string) ([]uplink.Iface, error) {
	as, err := adapters()
	if err != nil {
		return nil, err
	}
	var out []uplink.Iface
	for _, a := range as {
		if i, ok := uplinkOf(a, exclude); ok {
			out = append(out, i)
		}
	}
	return out, nil
}

// uplinkOf decides whether adapter a is an uplink and of which kind.
func uplinkOf(a adapter, exclude []string) (uplink.Iface, bool) {
	d := strings.ToLower(a.Description)
	virtual := a.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK || a.IfType == windows.IF_TYPE_TUNNEL ||
		strings.Contains(d, "hyper-v") || strings.Contains(d, "wireguard") || strings.Contains(d, "wintun") ||
		strings.Contains(d, "wi-fi direct") || strings.Contains(d, "virtualbox") || strings.Contains(d, "vmware") ||
		strings.Contains(d, "tap-windows") || strings.Contains(d, "loopback")
	if !a.Up || virtual || slices.Contains(exclude, a.Name) || !a.Addr.IsValid() || !a.Gateway.IsValid() {
		return uplink.Iface{}, false
	}
	return uplink.Iface{Name: a.Name, MAC: a.MAC.String(), Kind: kindOf(a), Driver: a.Description,
		Addr: a.Addr, Gateway: a.Gateway, Metric: int(a.Metric)}, true
}

// kindOf classifies an adapter by interface type and driver description.
func kindOf(a adapter) uplink.Kind {
	d := strings.ToLower(a.Description)
	switch {
	case a.IfType == windows.IF_TYPE_IEEE80211:
		return uplink.KindWiFi
	case a.IfType == ifTypeWWANPP || a.IfType == ifTypeWWANPP2 || strings.Contains(d, "mobile broadband") ||
		strings.Contains(d, "wwan"):
		return uplink.KindModem
	case strings.Contains(d, "bluetooth"):
		return uplink.KindBluetooth
	case strings.Contains(d, "remote ndis") || strings.Contains(d, "rndis") || strings.Contains(d, "apple mobile device") ||
		strings.Contains(d, "ncm"):
		return uplink.KindUSB
	}
	return uplink.KindEthernet
}

// adapters reads the IPv4 adapter list with gateways.
func adapters() ([]adapter, error) {
	size := uint32(16 << 10)
	var buf []byte
	for range 4 {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_INET, windows.GAA_FLAG_INCLUDE_GATEWAYS|windows.GAA_FLAG_SKIP_ANYCAST|
			windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil, fmt.Errorf("uplink scan: %w", err)
		}
		buf = nil
	}
	if buf == nil {
		return nil, fmt.Errorf("uplink scan: adapter list keeps growing")
	}
	var out []adapter
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
		a := adapter{Name: windows.UTF16PtrToString(aa.FriendlyName), Description: windows.UTF16PtrToString(aa.Description),
			IfType: aa.IfType, Metric: aa.Ipv4Metric, Up: aa.OperStatus == windows.IfOperStatusUp,
			MAC: net.HardwareAddr(slices.Clone(aa.PhysicalAddress[:aa.PhysicalAddressLength]))}
		for u := aa.FirstUnicastAddress; u != nil; u = u.Next {
			if ip, ok := netip.AddrFromSlice(u.Address.IP()); ok && ip.Unmap().Is4() {
				a.Addr = netip.PrefixFrom(ip.Unmap(), int(u.OnLinkPrefixLength))
				break
			}
		}
		for g := aa.FirstGatewayAddress; g != nil; g = g.Next {
			if ip, ok := netip.AddrFromSlice(g.Address.IP()); ok && ip.Unmap().Is4() && !ip.Unmap().IsUnspecified() {
				a.Gateway = ip.Unmap()
				break
			}
		}
		out = append(out, a)
	}
	return out, nil
}
