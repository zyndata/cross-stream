package hublog

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Level is the daemon's log level, changeable at run time (xs-hubctl
// loglevel, GUI "Detailed logs"). Give Var() to the slog handler: every
// writer behind it (stderr and the Ring) follows the change.
type Level struct {
	v     slog.LevelVar
	start slog.Level

	mu    sync.Mutex
	until time.Time // zero = no revert pending
	timer stopper   // pending revert
	after afterFunc // time.AfterFunc, replaced in tests
	now   func() time.Time
}

type stopper interface{ Stop() bool }

type afterFunc func(d time.Duration, f func()) stopper

// NewLevel returns a level that starts (and reverts) at start.
func NewLevel(start slog.Level) *Level {
	l := &Level{start: start, now: time.Now,
		after: func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }}
	l.v.Set(start)
	return l
}

// Var is the leveler for slog.HandlerOptions.Level.
func (l *Level) Var() *slog.LevelVar { return &l.v }

// LevelState is the current level and when it goes back to the start value.
type LevelState struct {
	Level string    `json:"level"`          // "debug" or "info" (or another slog name)
	Start string    `json:"start"`          // the value from -v
	Until time.Time `json:"until,omitzero"` // revert time; zero = stays
}

// State returns the current level.
func (l *Level) State() LevelState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return LevelState{Level: levelName(l.v.Level()), Start: levelName(l.start), Until: l.until}
}

// Set changes the level to name ("debug" or "info"). With d > 0 it goes back
// to the start value after d; a new Set replaces a pending revert.
func (l *Level) Set(name string, d time.Duration) (LevelState, error) {
	lv, err := ParseLevel(name)
	if err != nil {
		return LevelState{}, err
	}
	if d < 0 {
		return LevelState{}, fmt.Errorf("log level: negative duration %s", d)
	}
	l.mu.Lock()
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	l.until = time.Time{}
	l.v.Set(lv)
	if d > 0 && lv != l.start {
		l.until = l.now().Add(d)
		var t stopper
		t = l.after(d, func() { l.revert(&t) })
		l.timer = t
	}
	l.mu.Unlock()
	return l.State(), nil
}

// revert goes back to the start value unless a later Set replaced the timer.
func (l *Level) revert(t *stopper) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.timer == nil || l.timer != *t {
		return
	}
	l.timer, l.until = nil, time.Time{}
	l.v.Set(l.start)
}

// Stop cancels a pending revert (daemon shutdown).
func (l *Level) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
}

// ParseLevel accepts "debug" and "info" (any case).
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	}
	return 0, fmt.Errorf("log level %q: want debug or info", name)
}

func levelName(l slog.Level) string { return strings.ToLower(l.String()) }
