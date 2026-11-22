package server_test

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/hub"
)

// TestProbeServer: the deploy health check enrolls its identity once,
// handshakes, and leaves no session behind (CLOSE normal).
func TestProbeServer(t *testing.T) {
	e := startServer(t)
	info, err := e.ctl.Info(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.ctl.Enroll(e.ctx, "healthcheck")
	if err != nil {
		t.Fatal(err)
	}
	eps, err := hub.ResolveEndpoints(info.Endpoints)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tests := []struct {
		name         string
		dir          string
		to           []netip.AddrPort
		code         string
		wantEnrolled bool
		wantErr      bool
	}{
		{name: "first run enrolls", dir: dir, to: eps, code: res.Code, wantEnrolled: true},
		{name: "second run only probes", dir: dir, to: eps, code: res.Code},
		{name: "enrolled endpoints", dir: dir},
		{name: "no enrollment, no code", dir: t.TempDir(), to: eps, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hub.ProbeServer(e.ctx, tc.dir, "", tc.to, tc.code, "test", quiet())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if got.Name != "healthcheck" || got.Enrolled != tc.wantEnrolled || got.TunnelIP == "" || got.RTT <= 0 {
				t.Fatalf("result %+v", got)
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				s, err := e.ctl.Sessions(e.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(s) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("session left after the probe: %+v", s)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
	if err := e.ctl.Revoke(e.ctx, "healthcheck"); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.ProbeServer(e.ctx, dir, "", eps, "", "test", quiet()); err == nil || !errors.Is(err, hub.ErrNoResponse) {
		t.Fatalf("probe after revoke: %v, want ErrNoResponse", err)
	}
}
