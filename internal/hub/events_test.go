package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/pairing"
)

func drain(ch chan Event) []string {
	var out []string
	for {
		select {
		case e := <-ch:
			out = append(out, e.Type)
		default:
			return out
		}
	}
}

func TestEventTick(t *testing.T) {
	d := &Daemon{}
	ch, cancel := d.events.subscribe()
	defer cancel()
	rel := func(battery, rssi int, usedMB uint64) Status {
		r := RelayStatus{Relay: pairing.Relay{ID: "r1", Name: "anna", Path: pairing.PathSettings{CapMB: 100}},
			Connected: true, Stats: &control.Stats{Battery: battery, WifiRSSI: rssi}, Bond: &bond.PathStats{Used: usedMB * 1e6}}
		return Status{Relays: []RelayStatus{r}}
	}
	d.eventTick(rel(50, -50, 10))
	if got := drain(ch); len(got) != 0 {
		t.Fatalf("events at rest: %v", got)
	}
	d.eventTick(rel(24, -50, 85))
	d.eventTick(rel(20, -50, 100))
	got := drain(ch)
	if len(got) != 3 || got[0] != EvCap80 || got[1] != EvBatteryLow || got[2] != EvCap100 {
		t.Fatalf("thresholds: %v", got)
	}
	d.eventTick(rel(27, -50, 100)) // no re-arm below 30 %
	d.eventTick(rel(24, -50, 100))
	if got := drain(ch); len(got) != 0 {
		t.Fatalf("repeated: %v", got)
	}
	d.eventTick(rel(31, -50, 0)) // re-armed; cap counter reset
	d.eventTick(rel(24, -50, 0))
	if got := drain(ch); len(got) != 1 || got[0] != EvBatteryLow {
		t.Fatalf("re-arm: %v", got)
	}
	for i := range weakWifiOn {
		d.eventTick(rel(80, -80, 0))
		if got := drain(ch); (i < weakWifiOn-1) != (len(got) == 0) {
			t.Fatalf("weak wifi tick %d: %v", i, got)
		}
	}
}

func TestStreamEvents(t *testing.T) {
	d, _, _ := startHubWithRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan string, 16)
	go func() {
		_ = d.streamEvents(ctx, func(method string, params any) error {
			if method == EventNotice {
				b, _ := json.Marshal(params)
				var e Event
				_ = json.Unmarshal(b, &e)
				got <- e.Type
			}
			return nil
		})
	}()
	time.Sleep(100 * time.Millisecond)
	d.emit(Event{Type: EvModeChanged, Mode: ModeBonded})
	select {
	case typ := <-got:
		if typ != EvModeChanged {
			t.Fatalf("event %q", typ)
		}
	case <-ctx.Done():
		t.Fatal("event not streamed")
	}
}
