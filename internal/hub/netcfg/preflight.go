package netcfg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Channel is one frequency `iw phy <phy> info` lists.
type Channel struct {
	Num      int
	MHz      int
	NoIR     bool // no initiating radiation: no AP here
	Disabled bool
	Radar    bool // DFS: the AP would have to wait for a radar check
}

// usableAP reports whether an access point may start on the channel.
func (c Channel) usableAP() bool { return !c.NoIR && !c.Disabled && !c.Radar }

// PhyInfo is what the hotspot needs from `iw phy <phy> info`.
type PhyInfo struct {
	AP bool // "AP" in the supported interface modes
	// APWithManaged: one valid combination runs an AP next to a client and
	// also two clients (NetworkManager's wpa_supplicant brings a new AP
	// interface up as a client first, phase 18).
	APWithManaged bool
	HT40          bool // "HT20/HT40": 40 MHz channels
	Channels      []Channel
}

var (
	freqRe  = regexp.MustCompile(`^\* (\d+)(?:\.\d+)? MHz \[(\d+)\](.*)$`)
	groupRe = regexp.MustCompile(`#\{ ([^}]*) \} <= (\d+)`)
	totalRe = regexp.MustCompile(`total <= (\d+)`)
)

// ParsePhy parses `iw phy <phy> info` (or one phy of `iw list`).
func ParsePhy(out string) PhyInfo {
	var info PhyInfo
	section := ""
	var combo strings.Builder
	var combos []string
	for _, raw := range strings.Split(out, "\n") {
		l := strings.TrimSpace(raw)
		if l == "HT20/HT40" {
			info.HT40 = true
		}
		switch {
		case l == "Supported interface modes:":
			section = "modes"
			continue
		case l == "valid interface combinations:":
			section = "combos"
			continue
		case strings.HasSuffix(l, ":") && !strings.HasPrefix(l, "*"):
			section = ""
			if !strings.HasPrefix(l, "Frequencies") {
				continue
			}
		}
		if m := freqRe.FindStringSubmatch(l); m != nil {
			mhz, _ := strconv.Atoi(m[1])
			num, _ := strconv.Atoi(m[2])
			flags := m[3]
			info.Channels = append(info.Channels, Channel{Num: num, MHz: mhz,
				NoIR:     strings.Contains(flags, "no IR") || strings.Contains(flags, "passive scan"),
				Disabled: strings.Contains(flags, "disabled"),
				Radar:    strings.Contains(flags, "radar detection")})
			continue
		}
		switch section {
		case "modes":
			if l == "* AP" {
				info.AP = true
			} else if !strings.HasPrefix(l, "*") {
				section = ""
			}
		case "combos":
			switch {
			case strings.HasPrefix(l, "* #{"):
				if combo.Len() > 0 {
					combos = append(combos, combo.String())
				}
				combo.Reset()
				combo.WriteString(l)
			case strings.HasPrefix(l, "total") || strings.HasPrefix(l, "#{"):
				combo.WriteString(" " + l)
			default:
				section = ""
			}
		}
	}
	if combo.Len() > 0 {
		combos = append(combos, combo.String())
	}
	for _, c := range combos {
		if comboAPManaged(c) {
			info.APWithManaged = true
		}
	}
	return info
}

// comboAPManaged reports whether one interface combination allows an AP
// next to a managed (client) interface and, for the moment wpa_supplicant
// takes the new AP interface as a client, two managed interfaces. Intel
// AX201 (`#{ managed } <= 1, #{ AP, … } <= 1`) allows the first only: the
// second interface fails to come up with EBUSY.
func comboAPManaged(c string) bool {
	total := 0
	if m := totalRe.FindStringSubmatch(c); m != nil {
		total, _ = strconv.Atoi(m[1])
	}
	if total < 2 {
		return false
	}
	ap, managed, mgdCap := -1, -1, 0
	apWithM := false
	for i, g := range groupRe.FindAllStringSubmatch(c, -1) {
		types := strings.Split(g[1], ", ")
		n, _ := strconv.Atoi(g[2])
		hasAP, hasM := slices.Contains(types, "AP"), slices.Contains(types, "managed")
		if hasAP && hasM && n >= 2 {
			apWithM = true
		}
		if hasAP {
			ap = i
		}
		if hasM {
			managed = i
			mgdCap += n
		}
	}
	apWithM = apWithM || (ap >= 0 && managed >= 0 && ap != managed)
	return apWithM && mgdCap >= 2
}

// Channels tried in order when no channel is configured.
var (
	pref5  = []int{36, 40, 44, 48, 149, 153, 157, 161, 165}
	pref24 = []int{1, 6, 11}
)

// ChannelChoice is the band and channel the AP starts on, and why.
type ChannelChoice struct {
	Band    string
	Channel int    // 0 = NetworkManager picks
	Warning string // e.g. 5 GHz not allowed here
	Why     string // shown in status and GUI
}

// PickChannel is ChooseChannel without a scan (first usable channel).
func PickChannel(info PhyInfo, band string, want int, country string) (string, int, string, error) {
	c, err := ChooseChannel(info, band, want, country, nil)
	return c.Band, c.Channel, c.Warning, err
}

// ChooseChannel picks the AP band and channel in the order configured
// channel (if usable) → 5 GHz (band "a") → 2.4 GHz. Without a configured
// channel it takes the least loaded usable preferred channel of the band
// according to scan (nil scan: the first usable one). Band "a" falls back
// to "bg" with a warning when no 5 GHz channel allows an AP (no-IR). With no
// channel list (iw failed) it keeps the band and lets NM pick (channel 0).
func ChooseChannel(info PhyInfo, band string, want int, country string, scan []BSS) (ChannelChoice, error) {
	if len(info.Channels) == 0 {
		return ChannelChoice{Band: band, Why: "NetworkManager picks (no channel list)"}, nil
	}
	find := func(lo, hi int, prefs []int) (int, string) {
		for _, c := range info.Channels {
			if want > 0 && c.Num == want && c.MHz >= lo && c.MHz < hi && c.usableAP() {
				return want, "configured"
			}
		}
		if scan != nil {
			return leastLoaded(info, lo, hi, prefs, scan)
		}
		for _, n := range prefs {
			for _, c := range info.Channels {
				if c.Num == n && c.MHz >= lo && c.MHz < hi && c.usableAP() {
					return n, "first usable channel (no scan)"
				}
			}
		}
		return 0, ""
	}
	warn := ""
	if band == "a" {
		if ch, why := find(5000, 5900, pref5); ch > 0 {
			return ChannelChoice{Band: "a", Channel: ch, Why: why}, nil
		}
		warn = fmt.Sprintf("no 5 GHz channel allows an access point here (no-IR, country %s): using 2.4 GHz", country)
		want = 0
	}
	if ch, why := find(2400, 2500, pref24); ch > 0 {
		return ChannelChoice{Band: "bg", Channel: ch, Warning: warn, Why: why}, nil
	}
	return ChannelChoice{Warning: warn}, errors.New("hotspot: no 2.4 GHz channel 1, 6 or 11 allows an access point")
}

// rfkillBlocked returns an error when a Wi-Fi rfkill switch is on.
func rfkillBlocked(sysRoot string) error {
	dirs, _ := filepath.Glob(filepath.Join(sysRoot, "sys/class/rfkill/rfkill*"))
	for _, d := range dirs {
		if readTrim(filepath.Join(d, "type")) != "wlan" {
			continue
		}
		if readTrim(filepath.Join(d, "hard")) == "1" {
			return errors.New("hotspot: Wi-Fi is blocked by a hardware switch (rfkill hard block): turn the radio on")
		}
		if readTrim(filepath.Join(d, "soft")) == "1" {
			return errors.New("hotspot: Wi-Fi is soft-blocked (rfkill): run `rfkill unblock wifi`")
		}
	}
	return nil
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// phyName returns the wiphy of a Wi-Fi interface, e.g. phy0.
func phyName(sysRoot, iface string) (string, error) {
	name := readTrim(filepath.Join(sysRoot, "sys/class/net", iface, "phy80211/name"))
	if name == "" {
		return "", fmt.Errorf("hotspot: %s is not a Wi-Fi interface (no phy80211)", iface)
	}
	return name, nil
}

// Device is one row of `nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device`.
type Device struct {
	Name, Type, State, Connection string
}

// Devices lists NetworkManager devices.
func Devices(ctx context.Context, run Runner) ([]Device, error) {
	out, err := runWith(ctx, run, "", "nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device")
	if err != nil {
		return nil, fmt.Errorf("hotspot: NetworkManager is required (apt install network-manager): %w", err)
	}
	var ds []Device
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := splitTerse(l)
		if len(f) == 4 {
			ds = append(ds, Device{f[0], f[1], f[2], f[3]})
		}
	}
	return ds, nil
}

// WifiDevice returns the first Wi-Fi interface NetworkManager manages.
func WifiDevice(ctx context.Context, run Runner) (string, error) {
	ds, err := Devices(ctx, run)
	if err != nil {
		return "", err
	}
	for _, d := range ds {
		if d.Type == "wifi" && d.State != "unmanaged" && d.Name != APVirtualIface {
			return d.Name, nil
		}
	}
	return "", errors.New("hotspot: no Wi-Fi interface managed by NetworkManager")
}

// splitTerse splits one `nmcli -t` line: fields are ':'-separated and
// literal ':' and '\' are escaped with '\'.
func splitTerse(l string) []string {
	var fs []string
	var cur strings.Builder
	for i := 0; i < len(l); i++ {
		switch {
		case l[i] == '\\' && i+1 < len(l):
			i++
			cur.WriteByte(l[i])
		case l[i] == ':':
			fs = append(fs, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(l[i])
		}
	}
	return append(fs, cur.String())
}
