package server

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/speedtest"
	"github.com/zyndata/cross-stream/internal/wire"
)

// hubTests is a session's speed test state.
type hubTests struct {
	counter speedtest.Counter
	mu      sync.Mutex
	seen    map[uint16]time.Time // test ids already started (the hub sends TEST_REQ twice)
	down    atomic.Int32         // download tests sending now
}

// maxDownTests is how many download tests of one hub may send at once.
const maxDownTests = 2

// first reports whether test id is new (and remembers it).
func (t *hubTests) first(id uint16, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = make(map[uint16]time.Time)
	}
	for k, at := range t.seen {
		if now.Sub(at) > time.Minute {
			delete(t.seen, k)
		}
	}
	if _, ok := t.seen[id]; ok {
		return false
	}
	t.seen[id] = now
	return true
}

// pathList formats path ids for logs ([]uint8 would print as bytes).
func pathList(ps []uint8) []int {
	out := make([]int, len(ps))
	for i, p := range ps {
		out[i] = int(p)
	}
	return out
}

// testPaths are the paths a test runs on: one, or every ALIVE one.
func testPaths(b *bond.Engine, pathID uint8) []uint8 {
	if pathID != wire.TestAllPaths {
		return []uint8{pathID}
	}
	var out []uint8
	for _, p := range b.Stats().Paths {
		if p.State == bond.Alive.String() {
			out = append(out, p.ID)
		}
	}
	return out
}

// onTestReq starts a speed test the hub asked for: download = the server
// sends filler now; upload = the server counts what arrives and reports
// TEST_RESULT per path after the test.
func (s *Server) onTestReq(hs *hubSession, f *wire.TestReq) {
	now := s.o.Now()
	if !hs.tests.first(f.TestID, now) {
		return
	}
	d, maxMB := speedtest.Clamp(time.Duration(f.DurationMS)*time.Millisecond, f.MaxMB)
	paths := testPaths(hs.sess.Bond(), f.PathID)
	for _, p := range paths { // the filler fills the path's queue: PONGs come late
		hs.sess.Bond().HoldAlive(p, now.Add(d+speedtest.Grace+speedtest.HoldExtra))
	}
	s.log.Info("speed test", "hub", hs.hub.Name, "test", f.TestID, "dir", f.Dir, "paths", pathList(paths), "secs", d.Seconds(), "max_mb", maxMB)
	if f.Dir == wire.TestDown {
		if hs.tests.down.Add(1) > maxDownTests {
			hs.tests.down.Add(-1)
			s.log.Warn("speed test refused: too many at once", "hub", hs.hub.Name, "test", f.TestID)
			return
		}
		go func() {
			defer hs.tests.down.Add(-1)
			ctx, cancel := context.WithTimeout(context.Background(), d+time.Second)
			defer cancel()
			speedtest.Send(ctx, f.TestID, paths, d, uint64(maxMB)*1_000_000, func(p uint8, td *wire.TestData) error {
				hs.sess.Bond().NoteSent(p, speedtest.DatagramBytes)
				return hs.sess.SendOn(p, td)
			})
		}()
		return
	}
	hs.tests.counter.Expect(f.TestID, now)
	time.AfterFunc(d+speedtest.Grace, func() {
		res := hs.tests.counter.Take(f.TestID)
		got := make(map[uint8]bool, len(res))
		for _, r := range res {
			got[r.PathID] = true
		}
		for _, p := range paths { // nothing arrived: still answer, so the hub does not wait
			if !got[p] {
				res = append(res, wire.TestResult{TestID: f.TestID, PathID: p})
			}
		}
		for range 2 { // control frame: sent twice
			for i := range res {
				if err := hs.sess.Send(&res[i]); err != nil {
					s.log.Debug("speed test result", "hub", hs.hub.Name, "err", err)
				}
			}
		}
	})
}
