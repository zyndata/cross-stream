package pairing

import (
	"errors"
	"fmt"
	"slices"
)

// Path priorities and direction modes in PathSettings (names as in control.Limits).
const (
	DirOn         = "on"
	DirBackupOnly = "backup_only"
	DirOff        = "off"
)

// ErrSettings is wrapped by PathSettings validation errors.
var ErrSettings = errors.New("pairing: invalid path settings")

// PathSettings is the hub owner's policy for one relay's path (PATH_CONFIG).
// Empty strings and zeros are the defaults: normal, unlimited, on.
type PathSettings struct {
	Priority string `json:"priority,omitempty"` // normal, secondary, backup
	RateKbps uint32 `json:"rate_kbps,omitempty"`
	CapMB    uint32 `json:"cap_mb,omitempty"`
	Up       string `json:"up,omitempty"`   // on, backup_only, off
	Down     string `json:"down,omitempty"` // on, backup_only, off
}

// Validate checks the enum values.
func (p PathSettings) Validate() error {
	if !slices.Contains([]string{"", "normal", "secondary", "backup"}, p.Priority) {
		return fmt.Errorf("priority %q: %w", p.Priority, ErrSettings)
	}
	for _, d := range []string{p.Up, p.Down} {
		if !slices.Contains([]string{"", DirOn, DirBackupOnly, DirOff}, d) {
			return fmt.Errorf("direction %q: %w", d, ErrSettings)
		}
	}
	return nil
}

// SetPath stores the path settings of the relay with this id or name.
func (r *Registry) SetPath(idOrName string, p PathSettings) (Relay, error) {
	if err := p.Validate(); err != nil {
		return Relay{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	i, err := r.findLocked(idOrName)
	if err != nil {
		return Relay{}, fmt.Errorf("set path: %w", err)
	}
	d := r.d
	d.Relays = slices.Clone(d.Relays)
	d.Relays[i].Path = p
	if err := r.saveLocked(d); err != nil {
		return Relay{}, err
	}
	return d.Relays[i], nil
}
