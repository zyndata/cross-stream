package server_test

import (
	"errors"
	"testing"

	"github.com/zyndata/cross-stream/internal/hub"
)

// A hub enrolled again with a new code, without being removed first (same
// key), takes the code's name on the server; the old name is gone, the
// tunnel IP stays, and the used code still fails for another key
// (field test 2026-11-25: the hub said "dell-test2", the panel the old name).
func TestEnrollAgainSameKeyRenames(t *testing.T) {
	e := startServer(t)
	first, err := e.ctl.Enroll(e.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := e.enrollHub(t, first.Code)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.ctl.Enroll(e.ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := hub.ListenUDP("")
	if err != nil {
		t.Fatal(err)
	}
	en, err := hub.EnrollWithCode(e.ctx, dir, second.Code, conn, "test", quiet())
	if err != nil || en.Name != "t2" || en.TunnelIP != "100.64.0.2" {
		t.Fatalf("enroll again: %+v %v", en, err)
	}
	hubs, err := e.ctl.Hubs(e.ctx)
	if err != nil || len(hubs) != 1 || hubs[0].Name != "t2" || hubs[0].TunnelIP != "100.64.0.2" {
		t.Fatalf("hubs %+v %v", hubs, err)
	}
	if _, err := e.enrollHub(t, second.Code); !errors.Is(err, hub.ErrNoResponse) {
		t.Fatalf("used code with another key: %v", err)
	}
}
