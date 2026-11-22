package datacap

import (
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
)

func TestPeriodStart(t *testing.T) {
	loc := time.UTC
	birth := time.Date(2026, 3, 1, 8, 0, 0, 0, loc)
	tests := []struct {
		name string
		l    control.Limits
		now  time.Time
		want time.Time
	}{
		{"session", control.Limits{}, time.Date(2026, 3, 9, 1, 0, 0, 0, loc), birth},
		{"day", control.Limits{CapPeriod: control.PeriodDay}, time.Date(2026, 3, 9, 13, 5, 0, 0, loc),
			time.Date(2026, 3, 9, 0, 0, 0, 0, loc)},
		{"month default day", control.Limits{CapPeriod: control.PeriodMonth}, time.Date(2026, 3, 9, 13, 0, 0, 0, loc),
			time.Date(2026, 3, 1, 0, 0, 0, 0, loc)},
		{"month after reset", control.Limits{CapPeriod: control.PeriodMonth, ResetDay: 5}, time.Date(2026, 3, 9, 0, 0, 0, 0, loc),
			time.Date(2026, 3, 5, 0, 0, 0, 0, loc)},
		{"month before reset", control.Limits{CapPeriod: control.PeriodMonth, ResetDay: 15}, time.Date(2026, 3, 9, 0, 0, 0, 0, loc),
			time.Date(2026, 2, 15, 0, 0, 0, 0, loc)},
		{"january before reset", control.Limits{CapPeriod: control.PeriodMonth, ResetDay: 15}, time.Date(2026, 1, 2, 0, 0, 0, 0, loc),
			time.Date(2025, 12, 15, 0, 0, 0, 0, loc)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PeriodStart(tt.l, tt.now, birth); !got.Equal(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMeter(t *testing.T) {
	now := time.Date(2026, 3, 9, 23, 59, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	// Stop mode: everything passes until the cap, then nothing.
	m := New(control.Limits{CapMB: 1, CapPeriod: control.PeriodDay}, clock)
	if !m.Allow(BytesPerMB - 1) {
		t.Fatal("under cap dropped")
	}
	if _, stop := m.State(); stop {
		t.Fatal("stop below cap")
	}
	if !m.Allow(1) || m.Allow(1) {
		t.Fatal("cap boundary wrong")
	}
	if used, stop := m.State(); !stop || used != BytesPerMB {
		t.Fatalf("used %d stop %v", used, stop)
	}
	// Next day: a new period.
	now = now.Add(2 * time.Minute)
	if used, stop := m.State(); stop || used != 0 || !m.Allow(100) {
		t.Fatalf("new period used %d stop %v", used, stop)
	}

	// Throttle mode: over the cap, only throttle_kbps passes.
	m = New(control.Limits{CapMB: 1, UsedMB: 2, OverCap: control.OverCapThrottle, ThrottleKbps: 800}, clock)
	if _, stop := m.State(); stop {
		t.Fatal("throttle mode must not stop")
	}
	if m.Allow(1000) {
		t.Fatal("no tokens yet")
	}
	now = now.Add(100 * time.Millisecond) // 800 kbit/s * 0.1 s = 10000 bytes
	if !m.Allow(9000) || m.Allow(2000) {
		t.Fatal("throttle rate wrong")
	}
	now = now.Add(time.Hour) // burst is bounded: rate/4 = 25000 bytes
	if m.Allow(30_000) || !m.Allow(25_000) {
		t.Fatal("burst not bounded")
	}

	// New limits replace the count with the owner's used_mb.
	m.SetLimits(control.Limits{CapMB: 5, UsedMB: 3})
	if used, _ := m.State(); used != 3*BytesPerMB {
		t.Fatalf("used %d", used)
	}
}

func TestMeterOwnerCount(t *testing.T) {
	now := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	m := New(control.Limits{CapMB: 1, CapPeriod: control.PeriodDay}, clock)
	m.Add(400_000)
	m.Update(control.Limits{CapMB: 2, CapPeriod: control.PeriodDay})
	if since, used := m.Snapshot(); used != 400_000 || !since.Equal(time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("update lost the count: %v %d", since, used)
	}
	m.Add(2 * BytesPerMB) // Add counts past the cap
	if used, stop := m.State(); !stop || used != 2_400_000 {
		t.Fatalf("used %d stop %v", used, stop)
	}
	m.Reset()
	if used, stop := m.State(); stop || used != 0 {
		t.Fatal("reset")
	}
	// Restore: same period kept, an older one ignored.
	m2 := New(control.Limits{CapMB: 1, CapPeriod: control.PeriodDay}, clock)
	m2.Restore(time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC), 700)
	if _, used := m2.Snapshot(); used != 700 {
		t.Fatalf("restore %d", used)
	}
	m3 := New(control.Limits{CapMB: 1, CapPeriod: control.PeriodDay}, clock)
	m3.Restore(time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), 700)
	if _, used := m3.Snapshot(); used != 0 {
		t.Fatalf("old period restored %d", used)
	}
}
