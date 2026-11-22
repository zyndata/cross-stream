package hub

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zyndata/cross-stream/internal/hub/hublog"
	"github.com/zyndata/cross-stream/internal/ipc"
)

func TestLogsExportRedacted(t *testing.T) {
	d := &Daemon{cfg: DaemonConfig{Version: "v1.0.0"}}
	if got := d.LogsExport(); got.Lines == nil || len(got.Lines) != 0 {
		t.Fatalf("no ring: %+v", got)
	}
	r := hublog.NewRing(10)
	_, _ = r.Write([]byte("level=INFO msg=hotspot psk=secret123 from=203.0.113.9:4000\n"))
	d.cfg.Logs = r
	got := d.LogsExport()
	if got.Version != "v1.0.0" || len(got.Lines) != 1 {
		t.Fatalf("export %+v", got)
	}
	if l := got.Lines[0]; strings.Contains(l, "secret123") || strings.Contains(l, "203.0.113.9") {
		t.Fatalf("not redacted: %q", l)
	}
}

func TestLogLevelIPC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lvl := hublog.NewLevel(slog.LevelInfo)
	defer lvl.Stop()
	ring := hublog.NewRing(10)
	d := &Daemon{cfg: DaemonConfig{LogLevel: lvl},
		log: slog.New(slog.NewTextHandler(ring, &slog.HandlerOptions{Level: lvl.Var()}))}
	sock := filepath.Join(shortTemp(t), "ipc.sock")
	ln, err := ipc.Listen(sock, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	go ipc.Serve(ctx, ln, d.handleRPC, slog.New(slog.DiscardHandler))

	tests := []struct {
		name    string
		params  any
		want    string
		until   bool
		wantErr bool
	}{
		{"show", nil, "info", false, false},
		{"debug for a minute", LogLevelParams{Level: "debug", ForSecs: 60}, "debug", true, false},
		{"show while debug", LogLevelParams{}, "debug", true, false},
		{"bad level", LogLevelParams{Level: "loud"}, "", false, true},
		{"info for good", LogLevelParams{Level: "info"}, "info", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var st hublog.LevelState
			err := ipc.Call(ctx, sock, MethodLogsLevel, tc.params, &st)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err %v", err)
			}
			if tc.wantErr {
				return
			}
			if st.Level != tc.want || st.Until.IsZero() == tc.until || st.Start != "info" {
				t.Fatalf("state %+v", st)
			}
		})
	}
	d.log.Debug("not kept at info")
	if _, err := d.LogLevel(LogLevelParams{Level: "debug"}); err != nil {
		t.Fatal(err)
	}
	d.log.Debug("kept at debug")
	got := strings.Join(ring.Lines(), "\n")
	if strings.Contains(got, "not kept") || !strings.Contains(got, "kept at debug") {
		t.Fatalf("ring:\n%s", got)
	}
	full := newTestDaemon(t, shortTemp(t), DaemonConfig{LogLevel: lvl})
	if st := full.Status(); st.LogLevel == nil || st.LogLevel.Level != "debug" {
		t.Fatalf("status %+v", st.LogLevel)
	}
	if _, err := (&Daemon{}).LogLevel(LogLevelParams{}); err == nil {
		t.Fatal("fixed-level daemon accepted logs.level")
	}
}
