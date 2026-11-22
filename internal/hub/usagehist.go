package hub

import (
	"path/filepath"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/hub/usage"
)

// Usage history (architecture.md "Usage history").
const (
	UsageFile      = "usage.json"
	UsageSaveEvery = 60 * time.Second
)

// UsageParams are the params of usage.get: relay id or name ("" = all),
// days "YYYY-MM-DD" ("" = no bound).
type UsageParams struct {
	Relay string `json:"relay,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// UsageInfo is the result of usage.get: the history plus the speed tests
// in the range.
type UsageInfo struct {
	usage.Report
	SpeedTests []SpeedResult `json:"speed_tests"`
}

func openUsage(dir string) (*usage.Store, error) {
	return usage.Open(filepath.Join(dir, UsageFile))
}

// countUsage adds the bytes since the relay's previous stats message (the
// first message of a connection is the baseline; a counter reset counts
// from zero).
func (d *Daemon) countUsage(rc *relayPath, m *control.Stats, now time.Time) {
	prev := rc.prevStats
	if prev == nil {
		return
	}
	up, down := m.BytesUp, m.BytesDown
	if up >= prev.BytesUp && down >= prev.BytesDown {
		up, down = up-prev.BytesUp, down-prev.BytesDown
	}
	d.usage.Add(rc.rec.ID, rc.rec.Name, up, down, now)
}

// noteFailover counts a relay that left while the tunnel stayed up on
// another relay.
func (d *Daemon) noteFailover(now time.Time) {
	if d.ctx == nil || d.ctx.Err() != nil {
		return
	}
	c := d.client.Load()
	d.mu.Lock()
	others := len(d.connected()) + d.localsUp()
	d.mu.Unlock()
	if c != nil && c.Up() && others > 0 {
		d.usage.AddFailover(now)
	}
}

func (d *Daemon) saveUsage(now time.Time) {
	d.saveUplinks()
	if err := d.usage.Save(now); err != nil {
		d.log.Warn("usage history", "err", err)
	}
}

// Usage returns the history and the speed tests in the range.
func (d *Daemon) Usage(p UsageParams) (UsageInfo, error) {
	info := UsageInfo{Report: d.usage.Get(p.Relay, p.From, p.To), SpeedTests: []SpeedResult{}}
	tests, err := d.SpeedTests()
	if err != nil {
		return UsageInfo{}, err
	}
	for _, t := range tests {
		k := t.At.Local().Format(usage.DateLayout)
		if (p.From == "" || k >= p.From) && (p.To == "" || k <= p.To) {
			info.SpeedTests = append(info.SpeedTests, t)
		}
	}
	return info, nil
}
