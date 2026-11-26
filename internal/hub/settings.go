package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
)

// SettingsFile holds the hub-wide settings changed over IPC (GUI).
const SettingsFile = "settings.json"

// Settings are the hub-wide bonding settings (settings.get / settings.set).
type Settings struct {
	// OverflowMbps: secondary relays stay unused while normal ones send at
	// least this much (0 = off).
	OverflowMbps uint16 `json:"overflow_mbps"`
	// Redundancy: off, auto (default) or on (architecture.md "Redundancy").
	Redundancy string `json:"redundancy"`
	// Mode: max_throughput (default) or min_latency.
	Mode string `json:"mode"`
	// Aggregation batches small packets (default on).
	Aggregation bool `json:"aggregation"`
	// PEP: off or on — new client TCP flows become split-TCP streams
	// (architecture.md "Performance Enhancing Proxy — PEP").
	PEP string `json:"pep"`
	// PEPExclude: ports ("22"), port ranges ("5000-5100") and IPv4
	// addresses or networks whose TCP flows stay in packet mode.
	PEPExclude []string `json:"pep_exclude"`
	// OwnTraffic: the hub laptop's own traffic goes through CrossStream too
	// (phase 21; default off on Linux, always on on Windows).
	OwnTraffic bool `json:"own_traffic"`
}

// DefaultSettings are the settings before anything was saved.
func DefaultSettings() Settings {
	return Settings{Redundancy: bond.RedundancyAuto.String(), Mode: bond.MaxThroughput.String(), Aggregation: true,
		PEP: DefaultPEP, PEPExclude: []string{}}
}

// SettingsParams are the params of settings.set; nil fields keep their value.
type SettingsParams struct {
	OverflowMbps *uint16 `json:"overflow_mbps,omitempty"`
	Redundancy   *string `json:"redundancy,omitempty"`
	Mode         *string `json:"mode,omitempty"`
	Aggregation  *bool   `json:"aggregation,omitempty"`
	PEP          *string `json:"pep,omitempty"`
	// PEPExclude replaces the list when not nil.
	PEPExclude *[]string `json:"pep_exclude,omitempty"`
	OwnTraffic *bool     `json:"own_traffic,omitempty"`
}

// ErrOwnTrafficFixed is returned when own traffic would be turned off on a Windows hub.
var ErrOwnTrafficFixed = errors.New("own_traffic off: not available on a Windows hub")

// BondOpts are the session-wide bonding options of a Client.
type BondOpts struct {
	Redundancy  bond.Redundancy
	Mode        bond.Mode
	NoAggregate bool
}

// ParseRedundancy parses off / auto / on.
func ParseRedundancy(s string) (bond.Redundancy, error) {
	for _, r := range []bond.Redundancy{bond.RedundancyOff, bond.RedundancyAuto, bond.RedundancyOn} {
		if r.String() == s {
			return r, nil
		}
	}
	return 0, fmt.Errorf("redundancy %q (want off, auto or on)", s)
}

// ParseBondMode parses max_throughput / min_latency.
func ParseBondMode(s string) (bond.Mode, error) {
	for _, m := range []bond.Mode{bond.MaxThroughput, bond.MinLatency} {
		if m.String() == s {
			return m, nil
		}
	}
	return 0, fmt.Errorf("mode %q (want max_throughput or min_latency)", s)
}

// bondOpts converts and checks the settings.
func (s Settings) bondOpts() (BondOpts, error) {
	r, err := ParseRedundancy(s.Redundancy)
	if err != nil {
		return BondOpts{}, err
	}
	m, err := ParseBondMode(s.Mode)
	if err != nil {
		return BondOpts{}, err
	}
	if s.PEP != PEPOff && s.PEP != PEPOn {
		return BondOpts{}, fmt.Errorf("pep %q (want off or on)", s.PEP)
	}
	return BondOpts{Redundancy: r, Mode: m, NoAggregate: !s.Aggregation}, nil
}

// HotspotPowerParams are the params of hotspot.power.
type HotspotPowerParams struct {
	On bool `json:"on"`
}

// loadSettings reads settings.json; ok is false when it does not exist yet
// (the xs-hubd flags apply then).
func loadSettings(dir string) (s Settings, ok bool, err error) {
	s = DefaultSettings()
	b, err := os.ReadFile(filepath.Join(dir, SettingsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return s, false, nil
	}
	if err != nil {
		return Settings{}, false, fmt.Errorf("hub settings: %w", err)
	}
	if err := json.Unmarshal(b, &s); err != nil { // missing keys keep their defaults
		return Settings{}, false, fmt.Errorf("hub settings: %w", err)
	}
	if s.PEPExclude == nil {
		s.PEPExclude = []string{}
	}
	if _, err := s.bondOpts(); err != nil {
		return Settings{}, false, fmt.Errorf("hub settings: %w", err)
	}
	return s, true, nil
}

func saveSettings(dir string, s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("hub settings: %w", err)
	}
	path := filepath.Join(dir, SettingsFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("hub settings: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("hub settings: %w", err)
	}
	return nil
}

// Settings returns the hub-wide settings.
func (d *Daemon) Settings() Settings {
	s := *d.settings.Load()
	s.OverflowMbps = uint16(d.overflow.Load())
	s.OwnTraffic = s.OwnTraffic || netcfg.OwnTrafficAlways
	return s
}

// SetSettings changes and saves the hub-wide settings and applies them to
// the running session (both directions).
func (d *Daemon) SetSettings(p SettingsParams) (Settings, error) {
	d.setMu.Lock()
	defer d.setMu.Unlock()
	s := d.Settings()
	if p.OverflowMbps != nil {
		s.OverflowMbps = *p.OverflowMbps
	}
	if p.Redundancy != nil {
		s.Redundancy = *p.Redundancy
	}
	if p.Mode != nil {
		s.Mode = *p.Mode
	}
	if p.Aggregation != nil {
		s.Aggregation = *p.Aggregation
	}
	if p.PEP != nil {
		s.PEP = *p.PEP
	}
	if p.PEPExclude != nil {
		s.PEPExclude = append([]string{}, *p.PEPExclude...)
	}
	if p.OwnTraffic != nil {
		if netcfg.OwnTrafficAlways && !*p.OwnTraffic {
			return Settings{}, fmt.Errorf("settings.set: %w", ErrOwnTrafficFixed)
		}
		s.OwnTraffic = *p.OwnTraffic
	}
	o, err := s.bondOpts()
	if err != nil {
		return Settings{}, fmt.Errorf("settings.set: %w", err)
	}
	rules, err := parsePEPExclude(s.PEPExclude)
	if err != nil {
		return Settings{}, fmt.Errorf("settings.set: %w", err)
	}
	if err := saveSettings(d.cfg.Dir, s); err != nil {
		return Settings{}, err
	}
	d.overflow.Store(uint32(s.OverflowMbps))
	d.pepRules.Store(&rules)
	d.settings.Store(&s)
	if c := d.client.Load(); c != nil {
		c.SetOverflow(s.OverflowMbps)
		c.SetBondOpts(o)
	}
	d.setOwnTraffic(s.OwnTraffic)
	d.log.Info("hub settings changed", "overflow_mbps", s.OverflowMbps, "redundancy", s.Redundancy,
		"mode", s.Mode, "aggregation", s.Aggregation, "pep", s.PEP, "pep_exclude", len(s.PEPExclude),
		"own_traffic", s.OwnTraffic)
	return s, nil
}

// HotspotPower stops or starts the AP. Relays on the hotspot disconnect
// while it is off. The state is not saved: xs-hubd starts the AP on start.
func (d *Daemon) HotspotPower(ctx context.Context, on bool) (HotspotInfo, error) {
	if d.cfg.Hotspot == nil {
		return HotspotInfo{}, errors.New("hotspot.power: xs-hubd runs headless (no hotspot)")
	}
	d.hsMu.Lock()
	defer d.hsMu.Unlock()
	d.mu.Lock()
	off, s := d.hsOff, d.hs
	d.mu.Unlock()
	if off == !on {
		return d.HotspotGet(), nil
	}
	if !on {
		if err := d.cfg.Hotspot.Down(ctx); err != nil {
			return HotspotInfo{}, fmt.Errorf("hotspot.power: %w", err)
		}
		d.mu.Lock()
		d.hsOff, d.hsErr = true, ""
		d.mu.Unlock()
		d.log.Info("hotspot stopped over IPC")
		return d.HotspotGet(), nil
	}
	st, err := d.cfg.Hotspot.Up(ctx, hotspotConfig(s, d.apIface(), d.cfg.EthIface))
	d.mu.Lock()
	if err != nil {
		d.hsErr = err.Error()
	} else {
		d.hsOff, d.hsState, d.hsErr = false, st, ""
	}
	d.mu.Unlock()
	if err != nil {
		return HotspotInfo{}, fmt.Errorf("hotspot.power: %w", err)
	}
	d.log.Info("hotspot started over IPC", "ssid", s.SSID, "band", st.Band, "channel", st.Channel,
		"why", st.ChannelWhy, "scan", st.Scan)
	d.restartIfMoved(st)
	return d.HotspotGet(), nil
}

// relayGone records why a relay left, for the GUI.
func (d *Daemon) relayGone(id, reason string) {
	d.mu.Lock()
	d.gone[id] = goneInfo{Reason: reason, At: time.Now()}
	d.mu.Unlock()
}

type goneInfo struct {
	Reason string
	At     time.Time
}
