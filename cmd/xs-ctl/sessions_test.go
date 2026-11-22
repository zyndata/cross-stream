package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

func TestWatchSessions(t *testing.T) {
	calls := 0
	fetch := func(context.Context) ([]admin.Session, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("server restarting")
		}
		return []admin.Session{{Hub: "hub1", TunnelIP: "100.64.0.2", RxBytes: uint64(calls)}}, nil
	}
	var out strings.Builder
	if err := watchSessions(context.Background(), fetch, &out, 10*time.Millisecond, true, 3); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if calls != 3 || len(lines) != 3 {
		t.Fatalf("calls %d, lines %q", calls, lines)
	}
	var ss []admin.Session
	if err := json.Unmarshal([]byte(lines[2]), &ss); err != nil || ss[0].RxBytes != 3 {
		t.Fatalf("third line %q: %v", lines[2], err)
	}
	if !strings.Contains(lines[1], "server restarting") {
		t.Fatalf("error not shown: %q", lines[1])
	}

	out = strings.Builder{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := watchSessions(ctx, fetch, &out, time.Hour, false, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\033[2J") || !strings.Contains(out.String(), "hub1") {
		t.Fatalf("table refresh %q", out.String())
	}
}

func TestSessionsUsage(t *testing.T) {
	var out strings.Builder
	for _, args := range [][]string{{"extra"}, {"-watch", "-interval", "1ms"}} {
		if err := sessionsCmd(admin.Client{Socket: "/nonexistent"}, &out, &out, args); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
