package netcfg

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// What Up does when the hotspot's Wi-Fi card is connected to a network as a
// client (HotspotConfig.ClientWifi, phase 18).
const (
	ClientWifiAsk        = ""           // return *ClientWifiError: the owner chooses
	ClientWifiShare      = "share"      // run the AP next to the client on APVirtualIface
	ClientWifiDisconnect = "disconnect" // disconnect the client, AP on the card itself
	// APVirtualIface is the AP interface added next to a Wi-Fi client.
	APVirtualIface = "xsap0"
)

// ValidClientWifi reports whether s is a ClientWifi choice.
func ValidClientWifi(s string) bool {
	return s == ClientWifiAsk || s == ClientWifiShare || s == ClientWifiDisconnect
}

// ClientWifiError says the hotspot's card is connected to Wi-Fi Conn as a
// client. CanShare: the card allows an AP next to the client. Err is the
// failure of a chosen ClientWifiShare/ClientWifiDisconnect (nil = not
// chosen yet).
type ClientWifiError struct {
	Iface    string
	Conn     string
	CanShare bool
	Tried    string
	Err      error
}

func (e *ClientWifiError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("hotspot: %s is connected to Wi-Fi %q as a client and %q failed: %v", e.Iface, e.Conn, e.Tried, e.Err)
	}
	hint := "disconnect it (`nmcli device disconnect " + e.Iface + "`) or use a second (USB) Wi-Fi card"
	if e.CanShare {
		hint = "the card can run an AP next to the client on the same channel: add a virtual interface " +
			"(`iw dev " + e.Iface + " interface add " + APVirtualIface + " type __ap`) and start xs-hubd with -wifi-iface " +
			APVirtualIface + ", or " + hint
	}
	return fmt.Sprintf("hotspot: %s is connected to Wi-Fi %q as a client: %s", e.Iface, e.Conn, hint)
}

func (e *ClientWifiError) Unwrap() error { return e.Err }

// clientConflict returns a *ClientWifiError when iface is connected as a
// Wi-Fi client (to anything but our AP): one interface runs one NM connection.
func clientConflict(ds []Device, iface string, info PhyInfo) *ClientWifiError {
	for _, d := range ds {
		if d.Name != iface || d.Connection == "" || d.Connection == APConnection || !strings.HasPrefix(d.State, "connected") {
			continue
		}
		return &ClientWifiError{Iface: iface, Conn: d.Connection, CanShare: info.APWithManaged}
	}
	return nil
}

// ClientConn returns the Wi-Fi network iface is connected to as a client
// ("" = none or our AP).
func (h *Hotspot) ClientConn(ctx context.Context, iface string) (string, error) {
	ds, err := Devices(ctx, h.Run)
	if err != nil {
		return "", err
	}
	if cw := clientConflict(ds, iface, PhyInfo{}); cw != nil {
		return cw.Conn, nil
	}
	return "", nil
}

// resolveClient applies c.ClientWifi to a client conflict. For a share it
// returns the config moved to APVirtualIface on the client's band and
// channel (shared = true); a disconnect keeps c. A conflict without a
// usable choice comes back as the *ClientWifiError.
func (h *Hotspot) resolveClient(ctx context.Context, c HotspotConfig, cw *ClientWifiError, st *HotspotState) (HotspotConfig, bool, error) {
	switch {
	case c.ClientWifi == ClientWifiDisconnect:
		if _, err := runWith(ctx, h.Run, "", "nmcli", "device", "disconnect", c.Iface); err != nil {
			cw.Tried, cw.Err = ClientWifiDisconnect, err
			return c, false, cw
		}
		st.Warnings = append(st.Warnings, fmt.Sprintf("Wi-Fi %q disconnected from %s for the hotspot", cw.Conn, c.Iface))
		h.restore = cw.Conn // connected again when the hotspot goes down
		if err := h.dropVif(ctx); err != nil {
			st.Warnings = append(st.Warnings, err.Error())
		}
		return c, false, nil
	case c.ClientWifi == ClientWifiShare && cw.CanShare:
		sc, err := h.shareClient(ctx, c)
		if err != nil {
			cw.Tried, cw.Err = ClientWifiShare, err
			if derr := h.dropVif(ctx); derr != nil {
				cw.Err = errors.Join(err, derr)
			}
			return c, false, cw
		}
		st.Iface = sc.Iface
		st.ChannelWhy = fmt.Sprintf("next to Wi-Fi %q (its channel)", cw.Conn)
		return sc, true, nil
	}
	return c, false, cw
}

// shareClient adds APVirtualIface on the client card (kept if it exists)
// and returns c moved there, on the client's channel: the card has one
// radio, so both run on one channel.
func (h *Hotspot) shareClient(ctx context.Context, c HotspotConfig) (HotspotConfig, error) {
	out, err := runWith(ctx, h.Run, "", "iw", "dev", c.Iface, "info")
	if err != nil {
		return c, err
	}
	band, ch, err := clientChannel(out)
	if err != nil {
		return c, err
	}
	// Own MAC (the hotspot's BSSID): with the card's MAC the kernel refuses
	// to bring the second interface up (EBUSY in wpa_supplicant, Intel AX201).
	if _, err := runWith(ctx, h.Run, "", "iw", "dev", APVirtualIface, "info"); err != nil {
		if _, err := runWith(ctx, h.Run, "", "iw", "dev", c.Iface, "interface", "add", APVirtualIface, "type", "__ap",
			"addr", strings.ToLower(c.MAC)); err != nil {
			return c, fmt.Errorf("add %s: %w", APVirtualIface, err)
		}
	}
	h.vif = APVirtualIface
	if err := h.waitDevice(ctx, APVirtualIface); err != nil {
		return c, err
	}
	c.Iface, c.Band, c.Channel = APVirtualIface, band, ch
	return c, nil
}

// waitDevice waits up to 5 s until NetworkManager can use iface
// ("unavailable": wpa_supplicant could not take it).
func (h *Hotspot) waitDevice(ctx context.Context, iface string) error {
	state := "not listed"
	deadline := time.Now().Add(cmp.Or(h.DeviceWait, 5*time.Second))
	for {
		ds, err := Devices(ctx, h.Run)
		if err != nil {
			return err
		}
		for _, d := range ds {
			if d.Name != iface {
				continue
			}
			if state = d.State; state != "unmanaged" && state != "unavailable" {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("NetworkManager cannot use %s (state %s; wpa_supplicant log says why)", iface, state)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// dropVif deletes the AP interface a share added (none: no-op).
func (h *Hotspot) dropVif(ctx context.Context) error {
	if h.vif == "" {
		return nil
	}
	if _, err := runWith(ctx, h.Run, "", "iw", "dev", h.vif, "del"); err != nil {
		return fmt.Errorf("delete %s: %w", h.vif, err)
	}
	h.vif = ""
	return nil
}

// clientChannel reads "channel 36 (5180 MHz)" from `iw dev <iface> info`.
func clientChannel(out string) (band string, ch int, err error) {
	for _, l := range strings.Split(out, "\n") {
		var mhz int
		if _, e := fmt.Sscanf(strings.TrimSpace(l), "channel %d (%d MHz)", &ch, &mhz); e != nil {
			continue
		}
		switch {
		case mhz >= 2400 && mhz < 2500:
			return "bg", ch, nil
		case mhz >= 5000 && mhz < 5900:
			return "a", ch, nil
		}
		return "", 0, fmt.Errorf("the Wi-Fi network is on %d MHz, where the hotspot cannot run", mhz)
	}
	return "", 0, errors.New("no channel in `iw dev info` (not connected?)")
}

// parseWidth reads "width: 80 MHz" from `iw dev <iface> info` (0 = not found).
func parseWidth(out string) int {
	for _, l := range strings.Split(out, "\n") {
		if i := strings.Index(l, "width: "); i >= 0 {
			var w int
			if _, err := fmt.Sscanf(l[i:], "width: %d MHz", &w); err == nil {
				return w
			}
		}
	}
	return 0
}
