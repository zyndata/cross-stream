package server_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zyndata/cross-stream/internal/server"
)

// The panel and xs-ctl change the tunnel port only in its source; the
// server reports "restart required" until it starts with the new port.
func TestTunnelPortThroughControlSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	var restarts atomic.Int32
	e := startServerWith(t, func(o *server.Options) {
		o.Ports = server.FilePort{Path: path}
		o.Restart = func(context.Context) error { restarts.Add(1); return nil }
	})
	in, err := e.ctl.Info(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if in.TunnelPortSource != "file" || !in.TunnelPortWritable || in.RestartRequired || !in.CanRestart ||
		in.TunnelPortConfigured != in.TunnelPort {
		t.Fatalf("before: %+v", in)
	}
	if err := e.ctl.SetTunnelPort(e.ctx, 70000); err == nil {
		t.Fatal("out of range port accepted")
	}
	if err := e.ctl.SetTunnelPort(e.ctx, 51901); err != nil {
		t.Fatal(err)
	}
	in, err = e.ctl.Info(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !in.RestartRequired || in.TunnelPortConfigured != 51901 || in.TunnelPort == 51901 {
		t.Fatalf("after set: %+v", in)
	}
	for _, ep := range in.Endpoints {
		if strings.Contains(ep, ":51901") {
			t.Fatalf("codes must keep the running port until the restart: %v", in.Endpoints)
		}
	}
	if err := e.ctl.Restart(e.ctx); err != nil || restarts.Load() != 1 {
		t.Fatalf("restart: %v, %d", err, restarts.Load())
	}
}

func TestTunnelPortReadOnly(t *testing.T) {
	e := startServerWith(t, func(o *server.Options) { o.Ports = server.FixedPort{From: "env", Port: 6000} })
	if err := e.ctl.SetTunnelPort(e.ctx, 51901); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("env source: %v", err)
	}
	in, err := e.ctl.Info(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if in.TunnelPortWritable || in.CanRestart || in.TunnelPortSource != "env" {
		t.Fatalf("info %+v", in)
	}
	if err := e.ctl.Restart(e.ctx); err == nil {
		t.Fatal("restart without a restarter must fail")
	}
}
