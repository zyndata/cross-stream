package hublog

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeTimer is a revert timer fired by hand.
type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (t *fakeTimer) Stop() bool { t.stopped = true; return true }

func newFakeLevel(start slog.Level) (*Level, *[]*fakeTimer, *time.Time) {
	l := NewLevel(start)
	now := time.Date(2026, 11, 21, 22, 0, 0, 0, time.UTC)
	var timers []*fakeTimer
	l.now = func() time.Time { return now }
	l.after = func(d time.Duration, f func()) stopper {
		t := &fakeTimer{d: d, f: f}
		timers = append(timers, t)
		return t
	}
	return l, &timers, &now
}

func TestLevelSet(t *testing.T) {
	tests := []struct {
		name      string
		start     slog.Level
		set       string
		d         time.Duration
		wantLevel string
		wantTimer bool
		wantErr   bool
	}{
		{"debug for a while", slog.LevelInfo, "debug", time.Minute, "debug", true, false},
		{"debug for good", slog.LevelInfo, "debug", 0, "debug", false, false},
		{"back to start needs no timer", slog.LevelInfo, "info", time.Minute, "info", false, false},
		{"info from -v start", slog.LevelDebug, "INFO", 15 * time.Minute, "info", true, false},
		{"unknown level", slog.LevelInfo, "trace", 0, "info", false, true},
		{"negative duration", slog.LevelInfo, "debug", -time.Second, "info", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, timers, now := newFakeLevel(tc.start)
			st, err := l.Set(tc.set, tc.d)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if got := l.State().Level; got != tc.wantLevel {
				t.Fatalf("level %q, want %q", got, tc.wantLevel)
			}
			if got := len(*timers) == 1; got != tc.wantTimer {
				t.Fatalf("timers %d", len(*timers))
			}
			if tc.wantTimer {
				if !st.Until.Equal(now.Add(tc.d)) || (*timers)[0].d != tc.d {
					t.Fatalf("until %v timer %v", st.Until, (*timers)[0].d)
				}
				(*timers)[0].f()
				if s := l.State(); s.Level != levelName(tc.start) || !s.Until.IsZero() {
					t.Fatalf("after revert %+v", s)
				}
			}
		})
	}
}

func TestLevelSetReplacesTimer(t *testing.T) {
	l, timers, _ := newFakeLevel(slog.LevelInfo)
	if _, err := l.Set("debug", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Set("debug", time.Hour); err != nil {
		t.Fatal(err)
	}
	first, second := (*timers)[0], (*timers)[1]
	if !first.stopped {
		t.Fatal("first timer not stopped")
	}
	first.f() // fired anyway (Stop raced): must not revert
	if l.State().Level != "debug" {
		t.Fatal("stale timer reverted the level")
	}
	second.f()
	if l.State().Level != "info" {
		t.Fatal("second timer did not revert")
	}
}

func TestLevelAppliesToRing(t *testing.T) {
	l, timers, _ := newFakeLevel(slog.LevelInfo)
	r := NewRing(10)
	log := slog.New(slog.NewTextHandler(r, &slog.HandlerOptions{Level: l.Var()}))
	log.Debug("hidden")
	if _, err := l.Set("debug", time.Minute); err != nil {
		t.Fatal(err)
	}
	log.Debug("shown")
	(*timers)[0].f()
	log.Debug("hidden again")
	got := strings.Join(r.Lines(), "\n")
	if strings.Contains(got, "hidden") || !strings.Contains(got, "shown") {
		t.Fatalf("ring:\n%s", got)
	}
}

func TestLevelRealTimer(t *testing.T) {
	l := NewLevel(slog.LevelInfo)
	if _, err := l.Set("debug", 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for l.State().Level != "info" {
		if time.Now().After(deadline) {
			t.Fatal("did not revert")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
