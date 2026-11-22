package speedtest

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

func TestSendRampAndBudget(t *testing.T) {
	tests := []struct {
		name     string
		d        time.Duration
		maxBytes uint64
		want     uint64 // bytes per path
		early    bool   // stops before d (budget used up)
	}{
		// 4 Mb/s for the first 400 ms: 150 kB by 300 ms, the budget not reached.
		{"time runs out", 300 * time.Millisecond, 1 << 30, uint64(rampBytes(300*time.Millisecond)) / DatagramBytes * DatagramBytes, false},
		{"budget runs out", 5 * time.Second, 50 * DatagramBytes, 50 * DatagramBytes, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			got := map[uint8]int{}
			send := func(p uint8, f *wire.TestData) error {
				if f.TestID != 7 || len(f.Filler) != FillerSize {
					t.Errorf("frame %d/%d", f.TestID, len(f.Filler))
				}
				mu.Lock()
				got[p]++
				mu.Unlock()
				if p == 2 {
					return errors.New("queue full") // counted anyway: the schedule does not speed up
				}
				return nil
			}
			start := time.Now()
			sent := Send(context.Background(), 7, []uint8{1, 2}, tt.d, tt.maxBytes, send)
			el := time.Since(start)
			if tt.early != (el < tt.d) {
				t.Errorf("took %v of %v, want early %v", el, tt.d, tt.early)
			}
			for _, p := range []uint8{1, 2} {
				if diff := int64(sent[p]) - int64(tt.want); diff > 2*DatagramBytes || diff < -2*DatagramBytes || sent[p] > tt.maxBytes {
					t.Errorf("path %d: %d bytes (%d datagrams), want about %d", p, sent[p], got[p], tt.want)
				}
			}
		})
	}
}

func TestRampSchedule(t *testing.T) {
	tests := []struct {
		name     string
		d        time.Duration
		maxBytes uint64
		peak     float64 // Mb/s
	}{
		{"20 MB budget ends in the 256 Mb/s step", 10 * time.Second, 20_000_000, 256},
		{"tiny budget: first step", 10 * time.Second, 100_000, 4},
		{"time ends first", time.Second, 1 << 40, 16},
		{"huge budget: ceiling", 30 * time.Second, 1 << 40, 1000},
	}
	for _, tt := range tests {
		if got := PeakRate(tt.d, tt.maxBytes); math.Abs(got-tt.peak) > 1e-9 {
			t.Errorf("%s: peak %.1f Mb/s, want %.1f", tt.name, got, tt.peak)
		}
	}
	// 20 MB, 10 s: the sender stops at ~2.63 s inside the 256 Mb/s step.
	caps := []struct {
		mbps    float64
		atLeast bool
	}{
		{51, false},  // the phone's 5G upload: 64 Mb/s from 1.6 s on
		{118, false}, // its download: 128 Mb/s from 2.0 s, 0.63 s above it
		{200, true},  // only the last 0.23 s at 256 Mb/s
		{300, true},  // never sent that fast
	}
	for _, c := range caps {
		if got := Capped(c.mbps, 10*time.Second, 20_000_000); got != c.atLeast {
			t.Errorf("capped(%.0f Mb/s) = %v, want %v", c.mbps, got, c.atLeast)
		}
	}
	if s := stopAt(10*time.Second, 20_000_000); s < 2600*time.Millisecond || s > 2700*time.Millisecond {
		t.Errorf("stop %v, want ~2.63 s", s)
	}
	if stopAt(time.Second, 1<<40) != time.Second {
		t.Error("time limit")
	}
	if rampRate(100*time.Second) != rampCeil {
		t.Error("ramp above the ceiling")
	}
}

// TestCounterBestWindow: the ramp's slow start does not lower the result; the
// best second (path full) is the capacity.
func TestCounterBestWindow(t *testing.T) {
	var c Counter
	t0 := time.Unix(1_700_000_000, 0)
	c.Expect(5, t0)
	// 1 s at 10 datagrams/100 ms, then 2 s at 50/100 ms (the path's capacity).
	// The best half second: 250 datagrams.
	at := t0
	for i := range 30 {
		n := 10
		if i >= 10 {
			n = 50
		}
		for range n {
			c.Add(5, 1, DatagramBytes, at)
		}
		at = at.Add(100 * time.Millisecond)
	}
	r := c.Take(5)
	if len(r) != 1 {
		t.Fatalf("results %v", r)
	}
	want := 250 * DatagramBytes * 8 / 0.5e6
	if math.Abs(Mbps(r[0])-want) > 1e-9 {
		t.Errorf("Mbps %.3f, want the best half second %.3f", Mbps(r[0]), want)
	}
}

func TestSendStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sent := Send(ctx, 1, []uint8{1}, 10*time.Second, 1<<30, func(uint8, *wire.TestData) error { return nil })
	if sent[1] > 1<<20 {
		t.Fatalf("sent %d after cancel", sent[1])
	}
	if len(Send(context.Background(), 1, nil, time.Second, 1, nil)) != 0 {
		t.Fatal("no paths")
	}
}

func TestCounter(t *testing.T) {
	var c Counter
	t0 := time.Unix(1_700_000_000, 0)
	c.Add(7, 1, 10, t0) // never expected: ignored
	if c.Take(7) != nil {
		t.Fatal("unexpected test counted")
	}
	c.Expect(3, t0)
	c.Expect(9, t0)
	for i := range 11 {
		c.Add(3, 1, DatagramBytes, t0.Add(time.Duration(i)*100*time.Millisecond))
	}
	c.Add(3, 2, DatagramBytes, t0)
	c.Add(9, 1, 10, t0) // another test
	res := c.Take(3)
	if len(res) != 2 {
		t.Fatalf("results %v", res)
	}
	for _, r := range res {
		switch r.PathID {
		case 1:
			if r.Bytes != 6*DatagramBytes || r.DurationUS != 500_000 { // best half second: 5 datagrams (+1, see Mbps)
				t.Errorf("path 1: %+v", r)
			}
			want := 10 * DatagramBytes * 8 / 1e6
			if math.Abs(Mbps(r)-want) > 1e-9 {
				t.Errorf("Mbps %.4f, want %.4f", Mbps(r), want)
			}
		case 2:
			if Mbps(r) != 0 {
				t.Errorf("one packet: %.3f Mb/s, want 0", Mbps(r))
			}
		}
	}
	if c.Take(3) != nil {
		t.Fatal("taken twice")
	}
	c.Expect(10, t0.Add(10*time.Minute)) // expires test 9
	if c.Take(9) != nil {
		t.Fatal("abandoned test kept")
	}
}

func TestClamp(t *testing.T) {
	tests := []struct {
		d   time.Duration
		mb  uint16
		wd  time.Duration
		wmb uint16
	}{
		{0, 0, MinDuration, DefaultMaxMB},
		{time.Hour, 1000, MaxDuration, MaxMaxMB},
		{5 * time.Second, 30, 5 * time.Second, 30},
	}
	for _, tt := range tests {
		if d, mb := Clamp(tt.d, tt.mb); d != tt.wd || mb != tt.wmb {
			t.Errorf("Clamp(%v, %d) = %v, %d", tt.d, tt.mb, d, mb)
		}
	}
}
