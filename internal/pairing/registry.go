package pairing

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
	"github.com/zyndata/cross-stream/internal/crypto"
)

// RegistryFile is the registry file name in the hub state directory.
const RegistryFile = "relays.json"

// MaxRelays is the number of path ids available to relays (1..199); hub
// local uplinks use 200..254 (internal/hub/uplink).
const MaxRelays = 199

// Registry errors.
var (
	ErrFull     = errors.New("pairing: hub full")
	ErrNotFound = errors.New("pairing: relay not found")
)

// Relay is one paired relay.
type Relay struct {
	ID       string         `json:"id"`  // stable, "r<n>"
	Key      string         `json:"key"` // relay static public key, base64
	Name     string         `json:"name"`
	Carrier  string         `json:"carrier,omitempty"`
	PathID   uint8          `json:"path_id"`
	Limits   control.Limits `json:"limits"`
	Path     PathSettings   `json:"path,omitzero"`
	PairedAt time.Time      `json:"paired_at"`
}

type registryData struct {
	NextID    int      `json:"next_id"`
	Relays    []Relay  `json:"relays"`
	Forgotten []string `json:"forgotten,omitempty"` // keys of forgotten relays
}

// Registry is the persistent list of paired relays plus the keys of
// forgotten ones (rejected with "forgotten" unless they pair again with a
// fresh token). Every change is written at once (atomic rename, mode 0600).
// Safe for concurrent use.
type Registry struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
	d    registryData
}

// OpenRegistry loads the registry from dir, or starts an empty one.
func OpenRegistry(dir string, now func() time.Time) (*Registry, error) {
	if now == nil {
		now = time.Now
	}
	r := &Registry{path: filepath.Join(dir, RegistryFile), now: now, d: registryData{NextID: 1}}
	b, err := os.ReadFile(r.path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open registry: %w", err)
	}
	if err := json.Unmarshal(b, &r.d); err != nil {
		return nil, fmt.Errorf("parse registry %s: %w", r.path, err)
	}
	if r.d.NextID < 1 {
		r.d.NextID = 1
	}
	return r, nil
}

// Lookup returns the relay with this static key.
func (r *Registry) Lookup(key [crypto.KeyLen]byte) (Relay, bool) {
	k := crypto.EncodeKey(key)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rl := range r.d.Relays {
		if rl.Key == k {
			return rl, true
		}
	}
	return Relay{}, false
}

// IsForgotten reports whether this key belongs to a forgotten relay.
func (r *Registry) IsForgotten(key [crypto.KeyLen]byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.d.Forgotten, crypto.EncodeKey(key))
}

// Add registers a newly paired relay (the caller has checked its pairing
// token). It gets a new id and the lowest free path id; a forgotten key is
// taken off the forgotten list. Adding a known key returns the existing relay.
func (r *Registry) Add(key [crypto.KeyLen]byte, h control.Hello) (Relay, error) {
	k := crypto.EncodeKey(key)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rl := range r.d.Relays {
		if rl.Key == k {
			return rl, nil
		}
	}
	pid, ok := r.freePathLocked()
	if !ok {
		return Relay{}, ErrFull
	}
	rl := Relay{ID: "r" + strconv.Itoa(r.d.NextID), Key: k, Name: h.RelayName, Carrier: h.Carrier,
		PathID: pid, Limits: h.Limits, PairedAt: r.now().UTC()}
	d := r.d
	d.NextID++
	d.Relays = append(slices.Clone(d.Relays), rl)
	d.Forgotten = slices.DeleteFunc(slices.Clone(d.Forgotten), func(s string) bool { return s == k })
	if err := r.saveLocked(d); err != nil {
		return Relay{}, err
	}
	return rl, nil
}

func (r *Registry) freePathLocked() (uint8, bool) {
	used := make(map[uint8]bool, len(r.d.Relays))
	for _, rl := range r.d.Relays {
		used[rl.PathID] = true
	}
	for id := 1; id <= MaxRelays; id++ {
		if !used[uint8(id)] {
			return uint8(id), true
		}
	}
	return 0, false
}

// Update stores the name, carrier and limits a known relay reported in hello
// or a limits message.
func (r *Registry) Update(id, name, carrier string, l control.Limits) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.indexLocked(id)
	if i < 0 {
		return fmt.Errorf("update %s: %w", id, ErrNotFound)
	}
	d := r.d
	d.Relays = slices.Clone(d.Relays)
	d.Relays[i].Name, d.Relays[i].Carrier, d.Relays[i].Limits = name, carrier, l
	return r.saveLocked(d)
}

// Forget removes the relay with this id or name (the id wins; a name must be
// unique) and remembers its key as forgotten.
func (r *Registry) Forget(idOrName string) (Relay, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, err := r.findLocked(idOrName)
	if err != nil {
		return Relay{}, fmt.Errorf("forget: %w", err)
	}
	rl := r.d.Relays[i]
	d := r.d
	d.Relays = slices.Delete(slices.Clone(d.Relays), i, i+1)
	d.Forgotten = append(slices.Clone(d.Forgotten), rl.Key)
	if err := r.saveLocked(d); err != nil {
		return Relay{}, err
	}
	return rl, nil
}

// List returns all paired relays sorted by path id.
func (r *Registry) List() []Relay {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.d.Relays)
	slices.SortFunc(out, func(a, b Relay) int { return int(a.PathID) - int(b.PathID) })
	return out
}

// findLocked returns the index of the relay with this id or name (the id
// wins; a name must be unique).
func (r *Registry) findLocked(idOrName string) (int, error) {
	i := r.indexLocked(idOrName)
	if i >= 0 {
		return i, nil
	}
	for j, rl := range r.d.Relays {
		if rl.Name != idOrName {
			continue
		}
		if i >= 0 {
			return -1, fmt.Errorf("%q: name is not unique, use the relay id", idOrName)
		}
		i = j
	}
	if i < 0 {
		return -1, fmt.Errorf("%q: %w", idOrName, ErrNotFound)
	}
	return i, nil
}

func (r *Registry) indexLocked(id string) int {
	return slices.IndexFunc(r.d.Relays, func(rl Relay) bool { return rl.ID == id })
}

// saveLocked writes d and makes it current only if the write succeeded.
func (r *Registry) saveLocked(d registryData) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("save registry: %w", err)
	}
	if err := writeFileAtomic(r.path, append(b, '\n')); err != nil {
		return fmt.Errorf("save registry: %w", err)
	}
	r.d = d
	return nil
}

// writeFileAtomic writes b to path via a temp file and rename (mode 0600).
func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}
