package hub

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/zyndata/cross-stream/internal/hub/netcfg"
)

// HotspotFile holds the hotspot settings in the state directory (0600: PSK).
const HotspotFile = "hotspot.json"

// HotspotSettings are the persisted, user-settable hotspot fields.
type HotspotSettings struct {
	SSID    string `json:"ssid"`
	PSK     string `json:"psk"`
	Band    string `json:"band"`    // a (5 GHz, falls back to 2.4) or bg
	Channel int    `json:"channel"` // 0 = automatic
	Country string `json:"country"`
	MAC     string `json:"mac"` // cloned MAC = BSSID, generated once
	// ClientWifi: what the Linux hub does when its Wi-Fi card is connected
	// to a network as a client (netcfg.ClientWifi*; "" = ask).
	ClientWifi string `json:"client_wifi,omitempty"`
}

// HotspotController runs the hotspot (netcfg.Hotspot; a fake in tests).
type HotspotController interface {
	Up(ctx context.Context, cfg netcfg.HotspotConfig) (netcfg.HotspotState, error)
	Down(ctx context.Context) error
	Stations(ctx context.Context, iface string) (int, error)
}

// HotspotInfo is the result of hotspot.get and hotspot.set.
type HotspotInfo struct {
	Enabled  bool                `json:"enabled"` // false in headless mode
	On       bool                `json:"on"`      // false while stopped over IPC
	Settings HotspotSettings     `json:"settings"`
	State    netcfg.HotspotState `json:"state"`
	Clients  int                 `json:"clients"`
	Error    string              `json:"error,omitempty"`
}

// HotspotParams are the params of hotspot.set; nil fields keep their value.
type HotspotParams struct {
	SSID    *string `json:"ssid,omitempty"`
	PSK     *string `json:"psk,omitempty"`
	Band    *string `json:"band,omitempty"`
	Channel *int    `json:"channel,omitempty"`
	Country *string `json:"country,omitempty"`
	// ClientWifi: share, disconnect, or "" to ask again.
	ClientWifi *string `json:"client_wifi,omitempty"`
}

func (p HotspotParams) apply(s HotspotSettings) HotspotSettings {
	if p.SSID != nil {
		s.SSID = *p.SSID
	}
	if p.PSK != nil {
		s.PSK = *p.PSK
	}
	if p.Band != nil {
		s.Band = *p.Band
	}
	if p.Channel != nil {
		s.Channel = *p.Channel
	}
	if p.Country != nil {
		s.Country = strings.ToUpper(*p.Country)
	}
	if p.ClientWifi != nil {
		s.ClientWifi = *p.ClientWifi
	}
	return s
}

// LoadHotspotSettings reads hotspot.json, creating it with defaults on the
// first start (SSID from the host name, random PSK and MAC).
func LoadHotspotSettings(dir, host string) (HotspotSettings, error) {
	path := filepath.Join(dir, HotspotFile)
	b, err := os.ReadFile(path)
	if err == nil {
		var s HotspotSettings
		if err := json.Unmarshal(b, &s); err != nil {
			return HotspotSettings{}, fmt.Errorf("hotspot settings %s: %w", path, err)
		}
		return s, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return HotspotSettings{}, fmt.Errorf("hotspot settings: %w", err)
	}
	s, err := defaultHotspot(host)
	if err != nil {
		return HotspotSettings{}, err
	}
	return s, SaveHotspotSettings(dir, s)
}

// SaveHotspotSettings writes hotspot.json atomically with mode 0600.
func SaveHotspotSettings(dir string, s HotspotSettings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("hotspot settings: %w", err)
	}
	path := filepath.Join(dir, HotspotFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("hotspot settings: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("hotspot settings: %w", err)
	}
	return nil
}

// pskAlphabet avoids look-alike characters (the PSK may be typed by hand).
const pskAlphabet = "abcdefghjkmnpqrstuvwxyzACDEFGHJKLMNPQRSTUVWXYZ23456789"

func defaultHotspot(host string) (HotspotSettings, error) {
	var r [18]byte
	if _, err := rand.Read(r[:]); err != nil {
		return HotspotSettings{}, fmt.Errorf("hotspot defaults: %w", err)
	}
	psk := make([]byte, 12)
	for i := range psk {
		psk[i] = pskAlphabet[int(r[i])%len(pskAlphabet)]
	}
	mac := net.HardwareAddr(r[12:18])
	mac[0] = mac[0]&0xFC | 0x02 // locally administered, unicast
	ssid := "CrossStream"
	if host = sanitizeSSID(host); host != "" {
		ssid += "-" + host
	}
	if len(ssid) > 32 {
		ssid = ssid[:32]
	}
	return HotspotSettings{SSID: strings.TrimSpace(ssid), PSK: string(psk), Band: "a", Country: "PL", MAC: mac.String()}, nil
}

func sanitizeSSID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// hotspotConfig builds the netcfg config from the settings and interfaces.
func hotspotConfig(s HotspotSettings, iface, eth string) netcfg.HotspotConfig {
	c := netcfg.HotspotConfig{Iface: iface, SSID: s.SSID, PSK: s.PSK, Band: s.Band, Channel: s.Channel,
		Country: s.Country, MAC: s.MAC, Addr: netcfg.DefaultAPAddr, ClientWifi: s.ClientWifi}
	if eth != "" {
		c.EthIface, c.EthAddr = eth, netcfg.DefaultEthAddr
	}
	return c
}
