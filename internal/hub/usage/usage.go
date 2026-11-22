// Package usage keeps the hub's usage history (architecture.md "Usage
// history"): bytes per relay per day and the failovers the hub survived,
// kept for KeepDays and written atomically.
package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// KeepDays is how long days are kept.
const KeepDays = 400

// DateLayout is the day key (local time).
const DateLayout = "2006-01-02"

// Bytes are the bytes of one relay on one day: Up = hub → server through the
// relay's cellular link, Down = the other way (relay counters).
type Bytes struct {
	Up   uint64 `json:"up"`
	Down uint64 `json:"down"`
}

type relayHist struct {
	Name string           `json:"name"`
	Days map[string]Bytes `json:"days"`
}

type fileJSON struct {
	V         int                   `json:"v"`
	Relays    map[string]*relayHist `json:"relays"`
	Failovers map[string]int        `json:"failovers"` // per day
}

// Store is the usage history. Methods are safe for concurrent use.
type Store struct {
	path string

	mu    sync.Mutex
	data  fileJSON
	dirty bool
}

// Open loads path (a missing file is an empty history).
func Open(path string) (*Store, error) {
	s := &Store{path: path, data: fileJSON{V: 1, Relays: map[string]*relayHist{}, Failovers: map[string]int{}}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("usage %s: %w", path, err)
	}
	if s.data.Relays == nil {
		s.data.Relays = map[string]*relayHist{}
	}
	if s.data.Failovers == nil {
		s.data.Failovers = map[string]int{}
	}
	for _, r := range s.data.Relays {
		if r.Days == nil {
			r.Days = map[string]Bytes{}
		}
	}
	return s, nil
}

func day(t time.Time) string { return t.Local().Format(DateLayout) }

// Add counts bytes of a relay at time at.
func (s *Store) Add(relayID, name string, up, down uint64, at time.Time) {
	if up == 0 && down == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.data.Relays[relayID]
	if r == nil {
		r = &relayHist{Days: map[string]Bytes{}}
		s.data.Relays[relayID] = r
	}
	if name != "" {
		r.Name = name
	}
	k := day(at)
	b := r.Days[k]
	b.Up += up
	b.Down += down
	r.Days[k] = b
	s.dirty = true
}

// AddFailover counts one failover the hub survived (a relay left while
// another kept the tunnel up).
func (s *Store) AddFailover(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Failovers[day(at)]++
	s.dirty = true
}

// Save prunes days older than KeepDays before now and writes the file
// (temp file + rename) when something changed.
func (s *Store) Save(now time.Time) error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	cut := day(now.AddDate(0, 0, -KeepDays))
	for id, r := range s.data.Relays {
		for k := range r.Days {
			if k < cut {
				delete(r.Days, k)
			}
		}
		if len(r.Days) == 0 {
			delete(s.data.Relays, id)
		}
	}
	for k := range s.data.Failovers {
		if k < cut {
			delete(s.data.Failovers, k)
		}
	}
	b, err := json.Marshal(s.data)
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("usage: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("usage: %w", err)
	}
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		err = os.Rename(tmp, s.path)
		if err == nil {
			return nil
		}
	}
	s.mu.Lock()
	s.dirty = true
	s.mu.Unlock()
	return fmt.Errorf("usage: write %s failed", s.path)
}

// DayBytes is one day of one relay in a Report.
type DayBytes struct {
	Date string `json:"date"`
	Bytes
}

// RelayReport is one relay in a Report: totals and days (oldest first).
type RelayReport struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	Up   uint64     `json:"up"`
	Down uint64     `json:"down"`
	Days []DayBytes `json:"days"`
}

// DayCount is a per-day counter.
type DayCount struct {
	Date string `json:"date"`
	N    int    `json:"n"`
}

// Report is the history between two days (inclusive), relays sorted by
// total bytes, largest first.
type Report struct {
	From         string        `json:"from"`
	To           string        `json:"to"`
	Relays       []RelayReport `json:"relays"`
	Failovers    int           `json:"failovers"`
	FailoverDays []DayCount    `json:"failover_days"`
}

// Get returns the history of relay ("" = all) from..to ("YYYY-MM-DD";
// "" = no bound).
func (s *Store) Get(relay, from, to string) Report {
	in := func(k string) bool { return (from == "" || k >= from) && (to == "" || k <= to) }
	rep := Report{From: from, To: to, Relays: []RelayReport{}, FailoverDays: []DayCount{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.data.Relays {
		if relay != "" && relay != id && relay != r.Name {
			continue
		}
		rr := RelayReport{ID: id, Name: r.Name, Days: []DayBytes{}}
		for k, b := range r.Days {
			if in(k) {
				rr.Days = append(rr.Days, DayBytes{Date: k, Bytes: b})
				rr.Up += b.Up
				rr.Down += b.Down
			}
		}
		if len(rr.Days) == 0 {
			continue
		}
		slices.SortFunc(rr.Days, func(a, b DayBytes) int { return cmpStr(a.Date, b.Date) })
		rep.Relays = append(rep.Relays, rr)
	}
	slices.SortFunc(rep.Relays, func(a, b RelayReport) int {
		ta, tb := a.Up+a.Down, b.Up+b.Down
		switch {
		case ta > tb:
			return -1
		case ta < tb:
			return 1
		}
		return cmpStr(a.ID, b.ID)
	})
	for k, n := range s.data.Failovers {
		if in(k) {
			rep.FailoverDays = append(rep.FailoverDays, DayCount{Date: k, N: n})
			rep.Failovers += n
		}
	}
	slices.SortFunc(rep.FailoverDays, func(a, b DayCount) int { return cmpStr(a.Date, b.Date) })
	return rep
}

func cmpStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
