package hub

import (
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/pairing"
)

func TestCountUsage(t *testing.T) {
	st, err := openUsage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{usage: st, relays: map[string]*relayPath{}}
	rc := &relayPath{rec: pairing.Relay{ID: "r1", Name: "anna"}}
	now := time.Now()
	feed := func(up, down uint64) {
		m := &control.Stats{BytesUp: up, BytesDown: down}
		d.countUsage(rc, m, now)
		rc.updatePeak(now, m)
	}
	feed(1000, 5000) // baseline
	feed(1500, 9000)
	feed(100, 200) // relay restarted its counters
	rep := st.Get("", "", "")
	if len(rep.Relays) != 1 || rep.Relays[0].Up != 600 || rep.Relays[0].Down != 4200 || rep.Relays[0].Name != "anna" {
		t.Fatalf("report %+v", rep)
	}
	d.noteFailover(now) // no ctx/session: not counted
	if st.Get("", "", "").Failovers != 0 {
		t.Fatal("failover counted without a tunnel")
	}
	info, err := (&Daemon{usage: st, cfg: DaemonConfig{Dir: t.TempDir()}}).Usage(UsageParams{Relay: "anna"})
	if err != nil || len(info.Relays) != 1 || info.SpeedTests == nil {
		t.Fatalf("usage %+v %v", info, err)
	}
}
