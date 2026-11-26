package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/hub/hublog"
	"github.com/zyndata/cross-stream/internal/ipc"
)

// TestLogLevelMatchesHub decodes a real hub.Status log level into the GUI struct.
func TestLogLevelMatchesHub(t *testing.T) {
	until := time.Now().UTC().Truncate(time.Second)
	b, err := json.Marshal(hub.Status{LogLevel: &hublog.LevelState{Level: "debug", Start: "info", Until: until}})
	if err != nil {
		t.Fatal(err)
	}
	var got hubStatus
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.LogLevel == nil || *got.LogLevel != (logLevel{"debug", "info", until}) {
		t.Fatalf("log level %+v", got.LogLevel)
	}
	if v := newTracker().view(got, time.Now()); v.LogLevel != got.LogLevel {
		t.Fatalf("view log level %+v", v.LogLevel)
	}
}

// TestSetVerboseLogs checks the switch against a real xs-hubd log level
// served over IPC.
func TestSetVerboseLogs(t *testing.T) {
	dir, err := os.MkdirTemp("", "xsgui") // short path: unix socket limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := ipc.Listen(sock, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lvl := hublog.NewLevel(slog.LevelInfo)
	defer lvl.Stop()
	go ipc.Serve(ctx, ln, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != hub.MethodLogsLevel {
			return nil, ipc.ErrMethodNotFound
		}
		var p hub.LogLevelParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return lvl.Set(p.Level, time.Duration(p.ForSecs)*time.Second)
	}, slog.New(slog.DiscardHandler))

	a := &App{tr: testTexts(langPL), sock: sock, log: slog.New(slog.DiscardHandler)}
	tests := []struct {
		on    bool
		want  string
		until bool
	}{{true, "debug", true}, {false, "info", false}}
	for _, tc := range tests {
		got, err := a.SetVerboseLogs(tc.on)
		if err != nil {
			t.Fatal(err)
		}
		if got.Level != tc.want || got.Until.IsZero() == tc.until {
			t.Fatalf("on=%v: %+v", tc.on, got)
		}
		if tc.until && time.Until(got.Until) < verboseFor-time.Minute {
			t.Fatalf("until %v", got.Until)
		}
	}
	if methodLogsLevel != hub.MethodLogsLevel {
		t.Fatal("method name differs from internal/hub")
	}
}
