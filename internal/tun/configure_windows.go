package tun

import (
	"crypto/sha256"
	"fmt"
	"net"

	"golang.org/x/sys/windows"
	wgtun "golang.zx2c4.com/wireguard/tun"

	"github.com/zyndata/cross-stream/internal/iphlp"
)

// openDevice creates (or reuses) the Wintun adapter and configures its
// address and MTU. The adapter GUID is derived from the name, so the same
// adapter comes back after a restart (ICS keeps pointing at it). The name
// returned is the adapter's real alias, which Windows may have suffixed.
func openDevice(cfg Config) (wgtun.Device, string, error) {
	guid := AdapterGUID(cfg.Name)
	dev, err := wgtun.CreateTUNWithRequestedGUID(cfg.Name, &guid, cfg.MTU)
	if err != nil {
		return nil, "", fmt.Errorf("create wintun %s: %w", cfg.Name, err)
	}
	nt, ok := dev.(*wgtun.NativeTun)
	if !ok {
		_ = dev.Close()
		return nil, "", fmt.Errorf("create wintun %s: unexpected device %T", cfg.Name, dev)
	}
	row := windows.MibIfRow2{InterfaceLuid: nt.LUID()}
	if err := windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row); err != nil {
		_ = dev.Close()
		return nil, "", fmt.Errorf("wintun %s: interface row: %w", cfg.Name, err)
	}
	name := windows.UTF16ToString(row.Alias[:])
	if err := configure(iphlp.Iface{LUID: nt.LUID(), Index: row.InterfaceIndex}, cfg); err != nil {
		_ = dev.Close()
		return nil, "", fmt.Errorf("configure wintun %s: %w", name, err)
	}
	return dev, name, nil
}

// AdapterGUID is the fixed Wintun adapter GUID for an interface name.
func AdapterGUID(name string) windows.GUID {
	h := sha256.Sum256([]byte("CrossStream wintun " + name))
	g := windows.GUID{
		Data1: uint32(h[0])<<24 | uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3]),
		Data2: uint16(h[4])<<8 | uint16(h[5]),
		Data3: (uint16(h[6])<<8|uint16(h[7]))&0x0fff | 0x4000, // version 4 layout
	}
	copy(g.Data4[:], h[8:16])
	g.Data4[0] = g.Data4[0]&0x3f | 0x80
	return g
}

// TunMetric is the interface metric of the hub's TUN (automatic metric off).
const TunMetric = 5

// configure sets the IPv4 address, netmask, MTU, metric and DNS servers of
// the adapter.
// Nothing is stored: the IP Helper calls change the active state only and
// the adapter is rebuilt each start.
func configure(ifc iphlp.Iface, cfg Config) error {
	if err := iphlp.SetAddress(ifc, cfg.Prefix); err != nil {
		return fmt.Errorf("set address: %w", err)
	}
	if _, err := iphlp.Configure(ifc, iphlp.Settings{MTU: uint32(cfg.MTU), Metric: TunMetric}); err != nil {
		return fmt.Errorf("set mtu %d: %w", cfg.MTU, err)
	}
	if len(cfg.DNS) > 0 {
		if err := iphlp.SetDNS(ifc, cfg.DNS); err != nil {
			return fmt.Errorf("set dns: %w", err)
		}
	}
	return nil
}

// SetMTU changes the IPv4 MTU of an existing interface (alias).
func SetMTU(name string, mtu int) error {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return fmt.Errorf("set mtu %d on %s: %w", mtu, name, err)
	}
	if _, err := iphlp.Configure(iphlp.Iface{Index: uint32(ifc.Index)}, iphlp.Settings{MTU: uint32(mtu)}); err != nil {
		return fmt.Errorf("set mtu %d on %s: %w", mtu, name, err)
	}
	return nil
}
