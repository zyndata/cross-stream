package netcfg

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// dnsmasqDropIn turns off DNS in NM's shared-mode dnsmasq (xs-hubd serves
// DNS on the port address) and hands out the port's own address as DNS.
const dnsmasqDropIn = `# CrossStream (xs-hubd): DHCP only; xs-hubd answers DNS on the hub address.
port=0
dhcp-option=option:dns-server,0.0.0.0
`

// profileUUID is stable per profile and interface, so a reload replaces the
// profile instead of adding a second one.
func profileUUID(id, iface string) string {
	s := sha256.Sum256([]byte(id + "/" + iface))
	s[6] = s[6]&0x0f | 0x40
	s[8] = s[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", s[0:4], s[4:6], s[6:8], s[8:10], s[10:16])
}

// apKeyfile is the AP profile; width > 0 sets the channel width in MHz
// (NetworkManager channel-width, ignored by versions without it).
func apKeyfile(c HotspotConfig, band string, ch, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[connection]\nid=%s\nuuid=%s\ntype=wifi\ninterface-name=%s\nautoconnect=true\nautoconnect-priority=%d\n\n",
		APConnection, profileUUID(APConnection, c.Iface), c.Iface, APPriority)
	fmt.Fprintf(&b, "[wifi]\nmode=ap\nssid=%s\nband=%s\n", c.SSID, band)
	if ch > 0 {
		fmt.Fprintf(&b, "channel=%d\n", ch)
	}
	if width > 0 {
		fmt.Fprintf(&b, "channel-width=%d\n", width)
	}
	fmt.Fprintf(&b, "cloned-mac-address=%s\n\n", strings.ToUpper(c.MAC))
	fmt.Fprintf(&b, "[wifi-security]\nkey-mgmt=wpa-psk\nproto=rsn\npairwise=ccmp\ngroup=ccmp\npsk=%s\n\n", c.PSK)
	fmt.Fprintf(&b, "[ipv4]\nmethod=shared\naddress1=%s\n\n[ipv6]\nmethod=disabled\n", c.Addr)
	return b.String()
}

func ethKeyfile(c HotspotConfig) string {
	return fmt.Sprintf("[connection]\nid=%s\nuuid=%s\ntype=ethernet\ninterface-name=%s\nautoconnect=true\nautoconnect-priority=%d\n\n"+
		"[ethernet]\n\n[ipv4]\nmethod=shared\naddress1=%s\n\n[ipv6]\nmethod=disabled\n",
		EthConnection, profileUUID(EthConnection, c.EthIface), c.EthIface, APPriority, c.EthAddr)
}
