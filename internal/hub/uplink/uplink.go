// Package uplink finds the hub's own internet interfaces (USB tethering,
// Bluetooth PAN, modems, Ethernet, a Wi-Fi client card) and keeps the
// registry of known local links (architecture.md "Local uplinks on the hub").
package uplink

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Kind is the type of a local uplink.
type Kind string

// Uplink kinds.
const (
	KindUSB       Kind = "usb"       // phone USB tethering (Android RNDIS/NCM, iPhone ipheth)
	KindBluetooth Kind = "bluetooth" // Bluetooth PAN (slow: backup by default)
	KindModem     Kind = "modem"     // USB modem or built-in laptop SIM (WWAN)
	KindEthernet  Kind = "ethernet"
	KindWiFi      Kind = "wifi" // a Wi-Fi card as client
)

// Iface is one interface that qualifies as an uplink: up, with an IPv4
// address and its own default route.
type Iface struct {
	Name    string
	MAC     string // "" for raw-IP interfaces (many WWAN modems)
	Kind    Kind
	Driver  string
	Addr    netip.Prefix
	Gateway netip.Addr // invalid for point-to-point links
	Metric  int
}

// Key identifies a link across plug-ins: its MAC, or its interface name for
// interfaces without one.
func (i Iface) Key() string {
	if i.MAC != "" {
		return i.MAC
	}
	return "if:" + i.Name
}

// skipPrefixes are virtual interfaces that are never uplinks.
var skipPrefixes = []string{"lo", "xs", "docker", "br-", "veth", "virbr", "tun", "tap", "wg", "vnet", "zt", "tailscale", "p2p-"}

// Virtual reports whether an interface name belongs to a virtual device.
func Virtual(name string) bool {
	for _, p := range skipPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// usbDrivers are the kernel drivers of phone USB tethering.
var usbDrivers = []string{"ipheth", "rndis_host", "cdc_ncm", "cdc_ether"}

// modemDrivers are the kernel drivers of WWAN modems.
var modemDrivers = []string{"qmi_wwan", "cdc_mbim", "mhi_net", "huawei_cdc_ncm", "sierra_net", "option"}

// Classify returns the kind of an interface from its name, kernel driver
// and sysfs DEVTYPE (any may be empty).
func Classify(name, driver, devtype string) Kind {
	switch {
	case devtype == "wlan" || strings.HasPrefix(name, "wl"):
		return KindWiFi
	case devtype == "wwan" || strings.HasPrefix(name, "wwan") || strings.HasPrefix(name, "wwp") ||
		slices.Contains(modemDrivers, driver):
		return KindModem
	case devtype == "bluetooth" || strings.HasPrefix(name, "bnep"):
		return KindBluetooth
	case slices.Contains(usbDrivers, driver) || strings.HasPrefix(name, "usb") ||
		strings.HasPrefix(name, "enx") || strings.HasPrefix(name, "apple"):
		return KindUSB
	}
	return KindEthernet
}

// Route is an IPv4 default route of one interface.
type Route struct {
	Gateway netip.Addr
	Metric  int
}

// ParseDefaultRoutes reads /proc/net/route content and returns the lowest
// metric default route per interface.
func ParseDefaultRoutes(s string) map[string]Route {
	out := make(map[string]Route)
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 32)
		if err != nil || flags&1 == 0 { // RTF_UP
			continue
		}
		metric, err := strconv.Atoi(f[6])
		if err != nil {
			continue
		}
		var gw netip.Addr
		if b, err := hex.DecodeString(f[2]); err == nil && len(b) == 4 {
			var a [4]byte
			binary.BigEndian.PutUint32(a[:], binary.LittleEndian.Uint32(b))
			if a != [4]byte{} {
				gw = netip.AddrFrom4(a)
			}
		}
		if r, ok := out[f[0]]; !ok || metric < r.Metric {
			out[f[0]] = Route{Gateway: gw, Metric: metric}
		}
	}
	return out
}

// Label is the default display name of a new link.
func Label(k Kind, iface string) string {
	names := map[Kind]string{KindUSB: "USB", KindBluetooth: "Bluetooth", KindModem: "Modem",
		KindEthernet: "Ethernet", KindWiFi: "Wi-Fi"}
	return names[k] + " " + iface
}
