package hub

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRunRestart: a restart request (re-enrollment) ends Run with
// ErrRestart; a request while the caller is stopping ends it normally.
func TestRunRestart(t *testing.T) {
	tests := []struct {
		name        string
		cancelOuter bool
		want        error
	}{
		{"restart", false, ErrRestart},
		{"stopping anyway", true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDaemon(t, shortTemp(t), DaemonConfig{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- d.Run(ctx) }()
			for start := time.Now(); ; time.Sleep(5 * time.Millisecond) {
				d.mu.Lock()
				ready := d.stopRun != nil
				d.mu.Unlock()
				if ready {
					break
				}
				if time.Since(start) > 5*time.Second {
					t.Fatal("Run did not start")
				}
			}
			if tt.cancelOuter {
				cancel()
			}
			d.requestRestart(10 * time.Millisecond)
			select {
			case err := <-done:
				if !errors.Is(err, tt.want) && err != tt.want {
					t.Fatalf("Run: %v, want %v", err, tt.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not end")
			}
		})
	}
}
