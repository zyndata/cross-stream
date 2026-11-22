package netcfg

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// WinHotspotSubnet is the client network Mobile Hotspot always uses.
var WinHotspotSubnet = netip.MustParsePrefix("192.168.137.0/24")

// WinHotspot runs the AP through Windows Mobile Hotspot (WinRT tethering API
// in PowerShell). Mobile Hotspot shares one connection profile, chosen when
// it starts, and does DHCP and NAT for it (192.168.137.0/24); the hub wants
// that profile to be the tunnel adapter's. Before the tunnel exists it
// starts on any profile, and Reshare restarts it on the tunnel once. Windows
// picks the channel and the BSSID (the hotspot adapter's MAC); SSID,
// passphrase and, on Windows 11, the band are ours.
type WinHotspot struct {
	Share string // tunnel adapter alias to share once it exists (e.g. "xs0")
	Run   Runner // nil = exec

	op      sync.Mutex // one start/stop at a time
	mu      sync.Mutex
	shared  string         // profile the running hotspot shares
	want    *HotspotConfig // last config started; nil after Down
	addr    netip.Prefix   // hotspot address while it runs
	revived time.Time      // last start (Stations waits reviveEvery after it)
}

// Up starts (or reconfigures) Mobile Hotspot.
func (h *WinHotspot) Up(ctx context.Context, c HotspotConfig) (HotspotState, error) {
	return h.up(ctx, c, false)
}

// Reshare restarts the hotspot on the tunnel profile if it shares another
// one; changed reports whether it restarted (relays reconnect by themselves).
func (h *WinHotspot) Reshare(ctx context.Context, c HotspotConfig) (st HotspotState, changed bool, err error) {
	h.mu.Lock()
	done := h.shared == h.Share
	h.mu.Unlock()
	if done || h.Share == "" {
		return HotspotState{}, false, nil
	}
	st, err = h.up(ctx, c, true)
	return st, err == nil, err
}

func (h *WinHotspot) up(ctx context.Context, c HotspotConfig, restart bool) (HotspotState, error) {
	if err := ValidateHotspot(c); err != nil {
		return HotspotState{}, err
	}
	h.op.Lock()
	defer h.op.Unlock()
	defer func() {
		h.mu.Lock()
		h.revived = time.Now()
		h.mu.Unlock()
	}()
	share := ""
	if InterfaceExists(h.Share) {
		share = h.Share
	}
	r := ""
	if restart {
		r = "1"
	}
	out, err := runPS(ctx, h.Run, "hotspot.ps1", map[string]string{"Op": "up", "Ssid": c.SSID, "Pass": c.PSK,
		"Band": c.Band, "Share": share, "Restart": r})
	if err != nil {
		return HotspotState{}, fmt.Errorf("hotspot up: %w", err)
	}
	st := parseWinHotspot(out)
	if st.Iface == "" || !st.Addr.IsValid() {
		return st, fmt.Errorf("hotspot up: no adapter or address in %q", strings.TrimSpace(out))
	}
	h.mu.Lock()
	h.shared, h.want, h.addr = st.Shared, &c, st.Addr
	h.mu.Unlock()
	return st, nil
}

// parseWinHotspot reads the key=value lines of hotspot.ps1 up.
func parseWinHotspot(out string) HotspotState {
	var st HotspotState
	for _, ln := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(ln), "=")
		if !ok {
			continue
		}
		switch k {
		case "iface":
			st.Iface = v
		case "mac":
			st.BSSID = strings.ToLower(strings.ReplaceAll(v, "-", ":"))
		case "addr":
			if p, err := netip.ParsePrefix(v); err == nil && p.Addr().Is4() && !p.Addr().IsLinkLocalUnicast() {
				st.Addr = p
			}
		case "shared":
			st.Shared = v
		case "band":
			st.Band = v
		case "warn":
			st.Warnings = append(st.Warnings, v)
		}
	}
	st.ChannelWhy = "chosen by Windows"
	return st
}

// Down stops Mobile Hotspot.
func (h *WinHotspot) Down(ctx context.Context) error {
	h.op.Lock()
	defer h.op.Unlock()
	if _, err := runPS(ctx, h.Run, "hotspot.ps1", map[string]string{"Op": "down"}); err != nil {
		return fmt.Errorf("hotspot down: %w", err)
	}
	h.mu.Lock()
	h.shared, h.want = "", nil
	h.mu.Unlock()
	return nil
}

// reviveEvery limits how often Stations restarts a hotspot Windows stopped.
const reviveEvery = 30 * time.Second

// Stations counts the hotspot clients in the neighbour table (arp.exe is
// cheap; the tethering API's ClientCount needs a PowerShell run). It is
// called every few seconds, so it also restarts a hotspot that Windows
// stopped on its own (idle timeout, adapter reset) unless Down stopped it.
func (h *WinHotspot) Stations(ctx context.Context, iface string) (int, error) {
	pfx, err := InterfaceIPv4(iface)
	h.mu.Lock()
	addr := h.addr
	h.mu.Unlock()
	if err == nil && addr.IsValid() && pfx != addr { // a stopped hotspot adapter keeps a 169.254 address
		err = fmt.Errorf("hotspot adapter %s has %s, not %s", iface, pfx, addr)
	}
	if err != nil {
		h.mu.Lock()
		want, due := h.want, time.Since(h.revived) > reviveEvery
		h.mu.Unlock()
		if want == nil || !due || !h.op.TryLock() { // a start or stop is running: not stopped by Windows
			return 0, err
		}
		h.op.Unlock()
		if _, uerr := h.up(ctx, *want, true); uerr != nil {
			return 0, fmt.Errorf("hotspot stopped by Windows, restart failed: %w", uerr)
		}
		return 0, ErrHotspotRestarted
	}
	run := h.Run
	if run == nil {
		run = func(ctx context.Context, _ string, name string, args ...string) (string, error) {
			b, err := exec.CommandContext(ctx, name, args...).Output()
			return string(b), err
		}
	}
	out, err := run(ctx, "", "arp", "-a", "-N", pfx.Addr().String())
	if err != nil {
		return 0, fmt.Errorf("hotspot stations: %w", err)
	}
	return countNeighbours(out, pfx), nil
}

// countNeighbours counts dynamic unicast entries of arp -a output inside
// pfx ("  10.42.0.23   aa-bb-cc-dd-ee-ff   dynamic"; the type word is
// localised, the MAC format is not).
func countNeighbours(out string, pfx netip.Prefix) int {
	n := 0
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 {
			continue
		}
		a, err := netip.ParseAddr(f[0])
		if err != nil || !pfx.Contains(a) || a == pfx.Addr() || a == lastAddr(pfx) || strings.HasPrefix(f[1], "ff-ff") ||
			strings.HasPrefix(f[1], "01-00-5e") {
			continue
		}
		n++
	}
	return n
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]) | host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
