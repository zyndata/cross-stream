package netcfg

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NetworkManager names and defaults of the hub's shared ports.
const (
	APConnection  = "crossstream-ap"
	EthConnection = "crossstream-eth"
	DefaultNMDir  = "/etc/NetworkManager"
	APPriority    = 100 // autoconnect-priority, above client profiles (default 0)
	dnsmasqConf   = "dnsmasq-shared.d/crossstream.conf"
)

// Default hub addresses of the hotspot and the Ethernet share port.
var (
	DefaultAPAddr  = netip.MustParsePrefix("10.42.0.1/24")
	DefaultEthAddr = netip.MustParsePrefix("10.42.1.1/24")
)

// ErrHotspotRestarted is returned by Stations when the OS had stopped the
// hotspot on its own and it was started again (Windows Mobile Hotspot).
var ErrHotspotRestarted = errors.New("the hotspot was stopped by the system and has been restarted")

// HotspotConfig is the wanted state of the hub's shared ports.
type HotspotConfig struct {
	Iface   string // Wi-Fi interface for the AP
	SSID    string
	PSK     string
	Band    string // "a" (5 GHz, falls back to "bg") or "bg"
	Channel int    // 0 = automatic: least loaded after a neighbour scan
	Country string // regulatory domain, e.g. "PL"
	MAC     string // cloned MAC = BSSID (stable, so Android can skip its approval dialog)
	Addr    netip.Prefix
	// Optional Ethernet share port ("" = none).
	EthIface string
	EthAddr  netip.Prefix
	// ClientWifi: what to do when Iface is connected to Wi-Fi as a client
	// (ClientWifiAsk, ClientWifiShare, ClientWifiDisconnect).
	ClientWifi string
}

// HotspotState is what Up actually started.
type HotspotState struct {
	Iface   string `json:"iface"`
	Band    string `json:"band"`
	Channel int    `json:"channel"`
	// ChannelWhy says how the channel was chosen; Scan summarises the
	// neighbour scan behind an automatic choice (phase 14).
	ChannelWhy string `json:"channel_why,omitempty"`
	// Width is the channel width the AP runs at, MHz (from iw; 0 = unknown).
	Width    int      `json:"width,omitempty"`
	Scan     string   `json:"scan,omitempty"`
	BSSID    string   `json:"bssid"`
	EthIface string   `json:"eth_iface,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// Windows: the hotspot's own address (Mobile Hotspot picks the subnet)
	// and the connection profile it shares.
	Addr   netip.Prefix `json:"addr,omitzero"`
	Shared string       `json:"shared,omitempty"`
}

// Hotspot runs the AP (and the optional Ethernet share port) through
// NetworkManager.
type Hotspot struct {
	NMDir   string // default DefaultNMDir
	SysRoot string // default "/" (rfkill and phy lookup)
	Run     Runner // nil = exec
	// WaitAddr waits until iface has addr; nil = poll for 20 s.
	WaitAddr func(ctx context.Context, iface string, addr netip.Addr) error

	// DeviceWait limits the wait for NetworkManager to take a new AP
	// interface (0 = 5 s).
	DeviceWait time.Duration

	vif string // AP interface a ClientWifiShare added ("" = none)
	// restore is the Wi-Fi connection a ClientWifiDisconnect took the card
	// from; Down connects it again ("" = none).
	restore string
}

// ValidateHotspot checks the user-settable fields.
func ValidateHotspot(c HotspotConfig) error {
	if err := keyfileString("ssid", c.SSID, 1, 32); err != nil {
		return err
	}
	if err := keyfileString("psk", c.PSK, 8, 63); err != nil {
		return err
	}
	if c.Band != "a" && c.Band != "bg" {
		return fmt.Errorf("hotspot: band %q: want a (5 GHz) or bg (2.4 GHz)", c.Band)
	}
	if c.Channel < 0 || c.Channel > 196 {
		return fmt.Errorf("hotspot: channel %d out of range", c.Channel)
	}
	if len(c.Country) != 2 || strings.ToUpper(c.Country) != c.Country || strings.ContainsAny(c.Country, "0123456789") {
		return fmt.Errorf("hotspot: country %q: want two upper-case letters, e.g. PL", c.Country)
	}
	hw, err := net.ParseMAC(c.MAC)
	if err != nil || len(hw) != 6 || hw[0]&1 != 0 {
		return fmt.Errorf("hotspot: mac %q: want a unicast MAC aa:bb:cc:dd:ee:ff", c.MAC)
	}
	return nil
}

// keyfileString accepts printable ASCII without '\', with no leading or
// trailing space (NM keyfiles are GKeyFile: '\' escapes, ends are trimmed).
func keyfileString(name, s string, lo, hi int) error {
	if len(s) < lo || len(s) > hi {
		return fmt.Errorf("hotspot: %s must be %d–%d characters", name, lo, hi)
	}
	for _, r := range s {
		if r < 0x20 || r > 0x7e || r == '\\' {
			return fmt.Errorf("hotspot: %s: only printable ASCII without '\\'", name)
		}
	}
	if strings.TrimSpace(s) != s {
		return fmt.Errorf("hotspot: %s must not start or end with a space", name)
	}
	return nil
}

func (h *Hotspot) nmDir() string {
	if h.NMDir == "" {
		return DefaultNMDir
	}
	return h.NMDir
}

func (h *Hotspot) keyfile(id string) string {
	return filepath.Join(h.nmDir(), "system-connections", id+".nmconnection")
}

// Up checks the radio, writes the profiles and the dnsmasq drop-in, starts
// the AP (retrying on 2.4 GHz if 5 GHz fails) and waits for the hub address.
// It is idempotent: calling it again applies changed settings. A Wi-Fi
// client on c.Iface is handled by c.ClientWifi; a failed share comes back
// as a *ClientWifiError with Err set.
func (h *Hotspot) Up(ctx context.Context, c HotspotConfig) (HotspotState, error) {
	st, shared, err := h.up(ctx, c)
	if err != nil && shared != nil {
		if derr := h.dropVif(ctx); derr != nil {
			st.Warnings = append(st.Warnings, derr.Error())
		}
		shared.Tried, shared.Err = ClientWifiShare, err
		return st, shared
	}
	return st, err
}

// up is Up; shared is the client the AP runs next to (nil = none).
func (h *Hotspot) up(ctx context.Context, c HotspotConfig) (st HotspotState, shared *ClientWifiError, err error) {
	if err := ValidateHotspot(c); err != nil {
		return HotspotState{}, nil, err
	}
	if !ValidClientWifi(c.ClientWifi) {
		return HotspotState{}, nil, fmt.Errorf("hotspot: client_wifi %q: want share, disconnect or empty (ask)", c.ClientWifi)
	}
	st = HotspotState{Iface: c.Iface, BSSID: strings.ToLower(c.MAC), EthIface: c.EthIface}
	root := h.SysRoot
	if root == "" {
		root = "/"
	}
	if err := rfkillBlocked(root); err != nil {
		return st, shared, err
	}
	phy, err := phyName(root, c.Iface)
	if err != nil {
		return st, shared, err
	}
	if _, err := runWith(ctx, h.Run, "", "iw", "reg", "set", c.Country); err != nil {
		st.Warnings = append(st.Warnings, "could not set the regulatory country "+c.Country+": "+err.Error())
	}
	out, err := runWith(ctx, h.Run, "", "iw", "phy", phy, "info")
	if err != nil {
		st.Warnings = append(st.Warnings, "iw phy info failed, channel left to NetworkManager: "+err.Error())
	}
	info := ParsePhy(out)
	if err == nil && !info.AP {
		return st, shared, fmt.Errorf("hotspot: %s (%s) has no AP mode (iw phy info): use a Wi-Fi card that supports AP mode", c.Iface, phy)
	}
	ds, err := Devices(ctx, h.Run)
	if err != nil {
		return st, shared, err
	}
	choice := ChannelChoice{}
	if cw := clientConflict(ds, c.Iface, info); cw != nil {
		sc, ok, err := h.resolveClient(ctx, c, cw, &st)
		if err != nil {
			return st, nil, err
		}
		if ok {
			c, shared = sc, cw
			choice = ChannelChoice{Band: c.Band, Channel: c.Channel, Why: st.ChannelWhy}
		}
	} else if err := h.dropVif(ctx); err != nil {
		st.Warnings = append(st.Warnings, err.Error())
	}
	var scan []BSS
	if shared == nil {
		scan = h.scanForChannel(ctx, c, info, &st)
		if choice, err = ChooseChannel(info, c.Band, c.Channel, c.Country, scan); err != nil {
			return st, nil, err
		}
	}
	if choice.Warning != "" {
		st.Warnings = append(st.Warnings, choice.Warning)
	}
	band, ch := choice.Band, choice.Channel
	st.ChannelWhy = choice.Why
	if err := h.writeFile(filepath.Join(h.nmDir(), dnsmasqConf), dnsmasqDropIn, 0o644); err != nil {
		return st, shared, err
	}
	if c.EthIface != "" {
		if err := h.loadProfile(ctx, EthConnection, ethKeyfile(c)); err != nil {
			return st, shared, err
		}
		if _, err := runWith(ctx, h.Run, "", "nmcli", "--wait", "5", "connection", "up", "id", EthConnection); err != nil {
			st.Warnings = append(st.Warnings, "Ethernet share port "+c.EthIface+
				" is not active yet (no cable?): NetworkManager starts it when a cable is plugged in")
		}
	}
	// 5 GHz: ask for 40 MHz on an HT40 card (NetworkManager's automatic width
	// gave 20 MHz on the Dell's Intel AX201; 80 MHz made wpa_supplicant's
	// HT scan fail there after ~25 s, 40 MHz starts at once, phase 18);
	// without it if the AP does not start so.
	width := 0
	if band == "a" && info.HT40 && shared == nil { // next to a client: its width rules
		width = 40
	}
	err = h.startProfile(ctx, APConnection, apKeyfile(c, band, ch, width))
	if err != nil && width > 0 {
		wideErr := err
		width = 0
		if err = h.startProfile(ctx, APConnection, apKeyfile(c, band, ch, width)); err == nil {
			st.Warnings = append(st.Warnings, "40 MHz access point failed to start, using the automatic width: "+wideErr.Error())
		}
	}
	if err != nil && band == "a" && shared == nil {
		c24, perr := ChooseChannel(info, "bg", 0, c.Country, scan)
		if perr != nil {
			return st, shared, errors.Join(err, perr)
		}
		st.Warnings = append(st.Warnings, "5 GHz access point failed to start, using 2.4 GHz: "+err.Error())
		band, ch, st.ChannelWhy = "bg", c24.Channel, c24.Why
		err = h.startProfile(ctx, APConnection, apKeyfile(c, band, ch, 0))
	}
	if err != nil {
		return st, shared, err
	}
	st.Band, st.Channel = band, ch
	wait := h.WaitAddr
	if wait == nil {
		wait = pollAddr
	}
	if err := wait(ctx, c.Iface, c.Addr.Addr()); err != nil {
		return st, shared, err
	}
	if out, err := runWith(ctx, h.Run, "", "iw", "dev", c.Iface, "info"); err == nil {
		st.Width = parseWidth(out)
	}
	return st, shared, nil
}

// scanForChannel scans the neighbours when the channel is automatic. Our AP
// is stopped first (no scan while it is up; Up restarts it right after).
// A failed scan is a warning: the first usable channel is taken then.
func (h *Hotspot) scanForChannel(ctx context.Context, c HotspotConfig, info PhyInfo, st *HotspotState) []BSS {
	if c.Channel != 0 || len(info.Channels) == 0 {
		return nil
	}
	_, _ = runWith(ctx, h.Run, "", "nmcli", "connection", "down", "id", APConnection)
	scan, err := ScanNeighbours(ctx, h.Run, c.Iface)
	if err != nil {
		st.Warnings = append(st.Warnings, "neighbour scan failed, using the first usable channel: "+err.Error())
		return nil
	}
	st.Scan = ScanSummary(info, scan)
	return scan
}

// loadProfile writes a keyfile and makes NetworkManager (re)read it.
func (h *Hotspot) loadProfile(ctx context.Context, id, content string) error {
	path := h.keyfile(id)
	if err := h.writeFile(path, content, 0o600); err != nil {
		return err
	}
	if _, err := runWith(ctx, h.Run, "", "nmcli", "connection", "load", path); err != nil {
		return fmt.Errorf("hotspot: %w", err)
	}
	return nil
}

// startProfile loads a keyfile and activates it.
func (h *Hotspot) startProfile(ctx context.Context, id, content string) error {
	if err := h.loadProfile(ctx, id, content); err != nil {
		return err
	}
	if _, err := runWith(ctx, h.Run, "", "nmcli", "--wait", "30", "connection", "up", "id", id); err != nil {
		return fmt.Errorf("hotspot: %w", err)
	}
	return nil
}

func (h *Hotspot) writeFile(path, content string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("hotspot: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return fmt.Errorf("hotspot: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("hotspot: %w", err)
	}
	return nil
}

// Down deletes the hub's NM profiles (which stops them) and the dnsmasq
// drop-in. Errors are joined; missing pieces are fine.
func (h *Hotspot) Down(ctx context.Context) error {
	var errs []error
	for _, id := range []string{APConnection, EthConnection} {
		path := h.keyfile(id)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if _, err := runWith(ctx, h.Run, "", "nmcli", "connection", "delete", "id", id); err != nil {
			errs = append(errs, err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := os.Remove(filepath.Join(h.nmDir(), dnsmasqConf)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	if err := h.dropVif(ctx); err != nil {
		errs = append(errs, err)
	}
	// The laptop goes back to the network the hotspot took it from (owner, 2026-11-26).
	if conn := h.restore; conn != "" {
		h.restore = ""
		if _, err := runWith(ctx, h.Run, "", "nmcli", "--wait", "15", "connection", "up", "id", conn); err != nil {
			errs = append(errs, fmt.Errorf("reconnect Wi-Fi %q: %w", conn, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("hotspot down: %w", err)
	}
	return nil
}

// Stations counts the clients associated with the AP.
func (h *Hotspot) Stations(ctx context.Context, iface string) (int, error) {
	out, err := runWith(ctx, h.Run, "", "iw", "dev", iface, "station", "dump")
	if err != nil {
		return 0, fmt.Errorf("hotspot stations: %w", err)
	}
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "Station ") {
			n++
		}
	}
	return n, nil
}

func pollAddr(ctx context.Context, iface string, addr netip.Addr) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		if p, err := InterfaceIPv4(iface); err == nil && p.Addr() == addr {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("hotspot: %s did not get %s within 20 s", iface, addr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
