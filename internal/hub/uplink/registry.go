package uplink

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/pairing"
)

// File is the registry file in the hub state directory.
const File = "uplinks.json"

// Path ids of local uplinks: relays use 1..pairing.MaxRelays.
const (
	FirstPathID = pairing.MaxRelays + 1
	LastPathID  = 254 // 255 = wire.TestAllPaths
)

// Errors.
var (
	ErrNotFound = errors.New("uplink: no such link")
	ErrFull     = errors.New("uplink: no free path id")
)

// Link is a known local uplink, remembered by MAC address (Key).
type Link struct {
	ID      string `json:"id"`  // u1, u2, …
	Key     string `json:"key"` // MAC, or "if:<name>" without one
	Name    string `json:"name"`
	Kind    Kind   `json:"kind"`
	Iface   string `json:"iface"` // last seen interface name
	PathID  uint8  `json:"path_id"`
	Enabled bool   `json:"enabled"`
	// TTL sets the TTL of packets leaving through the link to 65 (tethered
	// phones: carriers spot tethering by TTL 63).
	TTL bool `json:"ttl"`
	// Path: priority, rate limit and directions, as for relays (cap_mb is
	// not used: Limits.CapMB has a period).
	Path pairing.PathSettings `json:"path"`
	// Limits: data cap per session/day/month (reset day), stop or throttle,
	// as in the relay app. max_priority, used_mb and battery_min are unused.
	Limits control.Limits `json:"limits"`
	// Used is the byte count of the cap period starting at UsedSince.
	Used      uint64    `json:"used_bytes,omitempty"`
	UsedSince time.Time `json:"used_since,omitzero"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Params are the params of uplinks.update; nil fields keep their value.
type Params struct {
	Uplink   string          `json:"uplink"` // id or name
	Name     *string         `json:"name,omitempty"`
	Enabled  *bool           `json:"enabled,omitempty"`
	TTL      *bool           `json:"ttl,omitempty"`
	Priority *string         `json:"priority,omitempty"`
	RateKbps *uint32         `json:"rate_kbps,omitempty"`
	Up       *string         `json:"up,omitempty"`
	Down     *string         `json:"down,omitempty"`
	Limits   *control.Limits `json:"limits,omitempty"`
	// ResetUsage sets the counter of the current period to zero.
	ResetUsage bool `json:"reset_usage,omitempty"`
}

type fileData struct {
	NextID int    `json:"next_id"`
	Links  []Link `json:"links"`
}

// Registry is the persistent list of known local uplinks. Safe for
// concurrent use.
type Registry struct {
	path string
	mu   sync.Mutex
	d    fileData
}

// Open loads the registry from dir (empty when the file does not exist).
func Open(dir string) (*Registry, error) {
	r := &Registry{path: filepath.Join(dir, File), d: fileData{NextID: 1}}
	b, err := os.ReadFile(r.path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("uplink: read registry: %w", err)
	}
	if err := json.Unmarshal(b, &r.d); err != nil {
		return nil, fmt.Errorf("uplink: parse %s: %w", File, err)
	}
	return r, nil
}

// List returns all known links sorted by id.
func (r *Registry) List() []Link {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.d.Links)
	slices.SortFunc(out, func(a, b Link) int { return int(a.PathID) - int(b.PathID) })
	return out
}

// Get returns a link by id or name.
func (r *Registry) Get(idOrName string) (Link, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.findLocked(idOrName)
	if i < 0 {
		return Link{}, false
	}
	return r.d.Links[i], true
}

func (r *Registry) findLocked(idOrName string) int {
	return slices.IndexFunc(r.d.Links, func(l Link) bool { return l.ID == idOrName || l.Name == idOrName })
}

// Defaults returns the settings of a newly found link: disabled (metered
// data is never used without consent), TTL fix on for USB tethers,
// Bluetooth PAN as a backup path.
func Defaults(i Iface, now time.Time) Link {
	l := Link{Key: i.Key(), Name: Label(i.Kind, i.Name), Kind: i.Kind, Iface: i.Name, TTL: i.Kind == KindUSB,
		FirstSeen: now.UTC(), LastSeen: now.UTC()}
	if i.Kind == KindBluetooth {
		l.Path.Priority = control.PriorityBackup
	}
	return l
}

// Seen records that interface i is present: a new link is added with
// Defaults and saved; a known one gets its interface name and kind updated
// (saved only when they changed). isNew reports a first sighting.
func (r *Registry) Seen(i Iface, now time.Time) (l Link, isNew bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if k := slices.IndexFunc(r.d.Links, func(l Link) bool { return l.Key == i.Key() }); k >= 0 {
		l = r.d.Links[k]
		changed := l.Iface != i.Name || l.Kind != i.Kind
		l.Iface, l.Kind, l.LastSeen = i.Name, i.Kind, now.UTC()
		r.d.Links[k] = l
		if changed {
			err = r.saveLocked()
		}
		return l, false, err
	}
	pid, ok := r.freePathLocked()
	if !ok {
		return Link{}, false, ErrFull
	}
	l = Defaults(i, now)
	l.ID, l.PathID = "u"+strconv.Itoa(r.d.NextID), pid
	r.d.NextID++
	r.d.Links = append(r.d.Links, l)
	return l, true, r.saveLocked()
}

func (r *Registry) freePathLocked() (uint8, bool) {
	for id := FirstPathID; id <= LastPathID; id++ {
		if !slices.ContainsFunc(r.d.Links, func(l Link) bool { return int(l.PathID) == id }) {
			return uint8(id), true
		}
	}
	return 0, false
}

// Update applies p to a link and saves.
func (r *Registry) Update(p Params, now time.Time) (Link, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.findLocked(p.Uplink)
	if i < 0 {
		return Link{}, fmt.Errorf("%w: %q", ErrNotFound, p.Uplink)
	}
	l := r.d.Links[i]
	if err := p.apply(&l, now); err != nil {
		return Link{}, err
	}
	if p.Name != nil && slices.ContainsFunc(r.d.Links, func(o Link) bool { return o.ID != l.ID && o.Name == l.Name }) {
		return Link{}, fmt.Errorf("uplink: name %q is taken", l.Name)
	}
	r.d.Links[i] = l
	return l, r.saveLocked()
}

func (p Params) apply(l *Link, now time.Time) error {
	if p.Name != nil {
		if *p.Name == "" || len(*p.Name) > 64 {
			return errors.New("uplink: name must be 1-64 characters")
		}
		l.Name = *p.Name
	}
	if p.Enabled != nil {
		l.Enabled = *p.Enabled
	}
	if p.TTL != nil {
		l.TTL = *p.TTL
	}
	ps := l.Path
	if p.Priority != nil {
		ps.Priority = *p.Priority
	}
	if p.RateKbps != nil {
		ps.RateKbps = *p.RateKbps
	}
	if p.Up != nil {
		ps.Up = *p.Up
	}
	if p.Down != nil {
		ps.Down = *p.Down
	}
	if err := ps.Validate(); err != nil {
		return err
	}
	l.Path = ps
	if p.Limits != nil {
		lim := *p.Limits
		lim.MaxPriority, lim.UsedMB, lim.BatteryMin = "", 0, 0
		if err := lim.Validate(); err != nil {
			return err
		}
		l.Limits = lim
	}
	if p.ResetUsage {
		l.Used, l.UsedSince = 0, now.UTC()
	}
	return nil
}

// SetUsed stores a link's data count (saved with the next Save).
func (r *Registry) SetUsed(id string, used uint64, since time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i := r.findLocked(id); i >= 0 {
		r.d.Links[i].Used, r.d.Links[i].UsedSince = used, since.UTC()
	}
}

// Forget removes a link (it is found again as new when still present).
func (r *Registry) Forget(idOrName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.findLocked(idOrName)
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrNotFound, idOrName)
	}
	r.d.Links = slices.Delete(r.d.Links, i, i+1)
	return r.saveLocked()
}

// Save writes the registry (data counters included).
func (r *Registry) Save() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.saveLocked()
}

func (r *Registry) saveLocked() error {
	b, err := json.MarshalIndent(r.d, "", "  ")
	if err != nil {
		return fmt.Errorf("uplink: encode registry: %w", err)
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("uplink: write registry: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("uplink: save registry: %w", err)
	}
	return nil
}
