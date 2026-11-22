package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/speedtest"
	"github.com/zyndata/cross-stream/internal/wire"
)

// SpeedTestsFile keeps the latest speed test results (for the usage
// history and the field-test table, phases 11 and 14).
const SpeedTestsFile = "speedtests.json"

// speedTestsKept is how many results the file keeps.
const speedTestsKept = 50

// ErrTestRunning is returned while another speed test runs.
var ErrTestRunning = errors.New("a speed test is already running")

// SpeedTestParams are the params of speedtest.run.
type SpeedTestParams struct {
	Relay string `json:"relay,omitempty"`  // id or name; empty = all relays at once
	Dir   string `json:"dir,omitempty"`    // down, up or both (default)
	Secs  int    `json:"secs,omitempty"`   // per direction (default 10, max 30)
	MaxMB uint16 `json:"max_mb,omitempty"` // filler cap per path and direction (default 20)
}

// SpeedPath is one path's result in one direction.
type SpeedPath struct {
	Relay  string  `json:"relay"` // relay name
	PathID uint8   `json:"path_id"`
	Dir    string  `json:"dir"`
	Mbps   float64 `json:"mbps"`
	Bytes  uint64  `json:"bytes"` // received outer bytes
	Secs   float64 `json:"secs"`  // first to last packet
	RTTms  float64 `json:"rtt_ms"`
	Loss   float64 `json:"loss"` // path loss estimate after the test (0..1)
	// AtLeast: the path kept up with the filler until the budget or the time
	// ran out, so Mbps is a lower bound (a bigger max_mb measures more).
	AtLeast bool `json:"at_least,omitempty"`
	// MaxMB is the budget this result was measured with (bigger after a retry).
	MaxMB uint16 `json:"max_mb,omitempty"`
}

// RetryFactor is the budget multiplier for a path the first round could not
// saturate: it is measured once more alone with this many times the budget
// (at most speedtest.MaxMaxMB).
const RetryFactor = 3

// SpeedResult is the result of speedtest.run.
type SpeedResult struct {
	At       time.Time   `json:"at"`
	Relay    string      `json:"relay,omitempty"` // empty = all relays
	Secs     int         `json:"secs"`
	MaxMB    uint16      `json:"max_mb"`
	Paths    []SpeedPath `json:"paths"`
	DownMbps float64     `json:"down_mbps"` // sum over paths (all relays at once)
	UpMbps   float64     `json:"up_mbps"`
	SentMB   float64     `json:"sent_mb"` // filler this hub sent (upload)
}

// clientTests is a client's speed test state.
type clientTests struct {
	counter speedtest.Counter
	next    uint16
	mu      sync.Mutex
	waiters map[uint16]chan wire.TestResult
}

func (t *clientTests) result(r *wire.TestResult) {
	t.mu.Lock()
	ch := t.waiters[r.TestID]
	t.mu.Unlock()
	if ch != nil {
		select {
		case ch <- *r:
		default:
		}
	}
}

// RunTest runs one speed test direction on a path (wire.TestAllPaths = every
// alive path) and returns per-path results and the outer bytes this hub sent.
func (c *Client) RunTest(ctx context.Context, pathID, dir uint8, d time.Duration, maxMB uint16) ([]wire.TestResult, uint64, error) {
	c.init()
	s := c.sess.Load()
	if s == nil {
		return nil, 0, errors.New("speed test: tunnel is down")
	}
	d, maxMB = speedtest.Clamp(d, maxMB)
	paths := testPaths(s.Bond(), pathID)
	if len(paths) == 0 {
		return nil, 0, errors.New("speed test: no alive path")
	}
	until := time.Now().Add(d + speedtest.Grace + speedtest.HoldExtra)
	for _, p := range paths { // the filler fills the path's queue: PONGs come late
		s.Bond().HoldAlive(p, until)
	}
	c.tests.mu.Lock()
	c.tests.next++
	id := c.tests.next
	ch := make(chan wire.TestResult, 4*len(paths))
	if c.tests.waiters == nil {
		c.tests.waiters = make(map[uint16]chan wire.TestResult)
	}
	c.tests.waiters[id] = ch
	c.tests.mu.Unlock()
	defer func() {
		c.tests.mu.Lock()
		delete(c.tests.waiters, id)
		c.tests.mu.Unlock()
	}()
	if dir == wire.TestDown {
		c.tests.counter.Expect(id, time.Now())
	}
	req := &wire.TestReq{TestID: id, PathID: pathID, Dir: dir, DurationMS: uint16(d.Milliseconds()), MaxMB: maxMB}
	for range 2 { // control frame on the best path; the server ignores the copy
		if err := s.Send(req); err != nil {
			return nil, 0, fmt.Errorf("speed test: %w", err)
		}
	}
	if dir == wire.TestDown {
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-time.After(d + speedtest.Grace):
		}
		return withZeros(c.tests.counter.Take(id), id, paths), 0, nil
	}
	// The server reports at d + Grace after the request, whenever the filler
	// ended (the ramp may use up the budget in a few seconds): wait for that.
	resultsBy := time.Now().Add(d + speedtest.Grace + 3*time.Second)
	sent := speedtest.Send(ctx, id, paths, d, uint64(maxMB)*1_000_000, func(p uint8, f *wire.TestData) error {
		s.Bond().NoteSent(p, speedtest.DatagramBytes)
		return s.SendOn(p, f)
	})
	var total uint64
	for _, n := range sent {
		total += n
	}
	got := make(map[uint8]wire.TestResult)
	timeout := time.After(time.Until(resultsBy))
	for len(got) < len(paths) {
		select {
		case <-ctx.Done():
			return nil, total, ctx.Err()
		case r := <-ch:
			got[r.PathID] = r
		case <-timeout:
			res := make([]wire.TestResult, 0, len(got))
			for _, r := range got {
				res = append(res, r)
			}
			return withZeros(res, id, paths), total, nil
		}
	}
	res := make([]wire.TestResult, 0, len(got))
	for _, r := range got {
		res = append(res, r)
	}
	return res, total, nil
}

// testPaths are the paths a test runs on: one, or every ALIVE one.
func testPaths(b *bond.Engine, pathID uint8) []uint8 {
	st := b.Stats()
	var out []uint8
	for _, p := range st.Paths {
		if p.State != bond.Alive.String() {
			continue
		}
		if pathID == wire.TestAllPaths || p.ID == pathID {
			out = append(out, p.ID)
		}
	}
	return out
}

// withZeros adds an empty result for every path that got nothing.
func withZeros(res []wire.TestResult, id uint16, paths []uint8) []wire.TestResult {
	seen := make(map[uint8]bool, len(res))
	for _, r := range res {
		seen[r.PathID] = true
	}
	for _, p := range paths {
		if !seen[p] {
			res = append(res, wire.TestResult{TestID: id, PathID: p})
		}
	}
	return res
}

// SpeedTest runs speedtest.run: one relay or all relays at once, download
// and/or upload, and stores the result.
func (d *Daemon) SpeedTest(ctx context.Context, p SpeedTestParams) (SpeedResult, error) {
	if !d.testMu.TryLock() {
		return SpeedResult{}, ErrTestRunning
	}
	defer d.testMu.Unlock()
	c := d.client.Load()
	if c == nil {
		return SpeedResult{}, errors.New("speed test: not enrolled")
	}
	pathID := wire.TestAllPaths
	if p.Relay != "" {
		found := false
		for _, r := range d.reg.List() {
			if r.ID == p.Relay || r.Name == p.Relay {
				pathID, found = r.PathID, true
				break
			}
		}
		if !found {
			return SpeedResult{}, fmt.Errorf("speed test: no relay %q", p.Relay)
		}
	}
	var dirs []uint8
	switch p.Dir {
	case "", "both":
		dirs = []uint8{wire.TestDown, wire.TestUp}
	case "down":
		dirs = []uint8{wire.TestDown}
	case "up":
		dirs = []uint8{wire.TestUp}
	default:
		return SpeedResult{}, fmt.Errorf("speed test: dir %q (want down, up or both)", p.Dir)
	}
	if p.Secs == 0 {
		p.Secs = speedtest.DefaultSecs
	}
	dur, maxMB := speedtest.Clamp(time.Duration(p.Secs)*time.Second, p.MaxMB)
	res := SpeedResult{At: time.Now().UTC(), Relay: p.Relay, Secs: int(dur.Seconds()), MaxMB: maxMB, Paths: []SpeedPath{}}
	names := make(map[uint8]string)
	for _, r := range d.reg.List() {
		names[r.PathID] = r.Name
	}
	if d.upReg != nil {
		for _, l := range d.upReg.List() {
			names[l.PathID] = l.Name // a local link (USB tethering, modem, Ethernet)
		}
	}
	d.log.Info("speed test started", "relay", p.Relay, "dir", p.Dir, "secs", res.Secs, "max_mb", maxMB)
	for _, dir := range dirs {
		out, sent, err := c.RunTest(ctx, pathID, dir, dur, maxMB)
		if err != nil {
			return SpeedResult{}, err
		}
		res.SentMB += float64(sent) / 1e6
		got := d.speedPaths(c, out, dir, dur, maxMB, names)
		// Only when needed: a path that took everything is measured again alone
		// with a bigger budget (the GUI shows that maximum cost up front).
		if more := min(uint32(maxMB)*RetryFactor, speedtest.MaxMaxMB); uint32(maxMB) < more {
			for i, sp := range got {
				if !sp.AtLeast {
					continue
				}
				d.log.Info("speed test: path faster than the budget measures, again with more", "path", sp.PathID, "dir", sp.Dir, "max_mb", more)
				out2, sent2, err := c.RunTest(ctx, sp.PathID, dir, dur, uint16(more))
				if err != nil {
					return SpeedResult{}, err
				}
				res.SentMB += float64(sent2) / 1e6
				for _, again := range d.speedPaths(c, out2, dir, dur, uint16(more), names) {
					if again.PathID == sp.PathID && again.Mbps >= sp.Mbps {
						got[i] = again
					}
				}
			}
		}
		for _, sp := range got {
			if dir == wire.TestUp {
				res.UpMbps += sp.Mbps
			} else {
				res.DownMbps += sp.Mbps
			}
			res.Paths = append(res.Paths, sp)
		}
	}
	res.DownMbps, res.UpMbps, res.SentMB = round2(res.DownMbps), round2(res.UpMbps), round2(res.SentMB)
	sort.SliceStable(res.Paths, func(i, j int) bool {
		if res.Paths[i].Dir != res.Paths[j].Dir {
			return res.Paths[i].Dir == "down"
		}
		return res.Paths[i].PathID < res.Paths[j].PathID
	})
	if err := d.saveSpeedTest(res); err != nil {
		d.log.Warn("save speed test", "err", err)
	}
	d.log.Info("speed test done", "down_mbps", res.DownMbps, "up_mbps", res.UpMbps, "sent_mb", res.SentMB)
	d.emit(Event{Type: EvSpeedTestEnd, Name: p.Relay, Value: int(res.DownMbps + 0.5), Up: int(res.UpMbps + 0.5)})
	return res, nil
}

// speedPaths turns one round's TEST_RESULTs into per-path results.
func (d *Daemon) speedPaths(c *Client, out []wire.TestResult, dir uint8, dur time.Duration, maxMB uint16, names map[uint8]string) []SpeedPath {
	stats := make(map[uint8]bond.PathStats)
	for _, ps := range c.Report().Bond.Paths {
		stats[ps.ID] = ps
	}
	name := "down"
	if dir == wire.TestUp {
		name = "up"
	}
	got := make([]SpeedPath, 0, len(out))
	for _, r := range out {
		mbps := speedtest.Mbps(r)
		sp := SpeedPath{Relay: names[r.PathID], PathID: r.PathID, Dir: name, Mbps: round2(mbps),
			Bytes: r.Bytes, Secs: round2(float64(r.DurationUS) / 1e6), MaxMB: maxMB,
			AtLeast: speedtest.Capped(mbps, dur, uint64(maxMB)*1_000_000)}
		if ps, ok := stats[r.PathID]; ok {
			sp.RTTms, sp.Loss = round2(float64(ps.SRTT)/1e6), round2(ps.Loss)
		}
		got = append(got, sp)
	}
	return got
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// SpeedTests returns the stored results, newest first.
func (d *Daemon) SpeedTests() ([]SpeedResult, error) {
	b, err := os.ReadFile(filepath.Join(d.cfg.Dir, SpeedTestsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return []SpeedResult{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("speed tests: %w", err)
	}
	var out []SpeedResult
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("speed tests: %w", err)
	}
	return out, nil
}

func (d *Daemon) saveSpeedTest(r SpeedResult) error {
	all, err := d.SpeedTests()
	if err != nil {
		all = nil // a broken file is replaced
	}
	all = append([]SpeedResult{r}, all...)
	if len(all) > speedTestsKept {
		all = all[:speedTestsKept]
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(d.cfg.Dir, SpeedTestsFile)
	if err := os.WriteFile(path+".tmp", append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
