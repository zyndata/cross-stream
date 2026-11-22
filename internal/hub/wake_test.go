package hub

import (
	"testing"
	"time"
)

// TestWakeGap: when the hub's 1-s tick counts as a wake from sleep.
func TestWakeGap(t *testing.T) {
	tests := []struct {
		name       string
		mono, wall time.Duration
		wake       bool
	}{
		{"normal tick", time.Second, time.Second, false},
		{"slow tick under load", 3 * time.Second, 3 * time.Second, false},
		{"process frozen 200 s (both clocks moved)", 200 * time.Second, 200 * time.Second, true},
		{"Linux suspend (monotonic clock stood still)", time.Second, 10 * time.Minute, true},
		{"Windows sleep (monotonic clock moved)", 10 * time.Minute, time.Second, true},
		{"wall clock stepped back by NTP", time.Second, -time.Hour, false},
	}
	for _, tt := range tests {
		if got := elapsed(tt.mono, tt.wall) >= WakeGap; got != tt.wake {
			t.Errorf("%s: wake %v, want %v", tt.name, got, tt.wake)
		}
	}
	now := time.Now()
	if g := tickGap(now, now.Add(time.Second)); g != time.Second {
		t.Errorf("tickGap of a 1-s tick = %v", g)
	}
}
