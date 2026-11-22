package main

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"syscall"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/ipc"
)

func TestHints(t *testing.T) {
	tests := []struct {
		name               string
		stats              relayStats
		loss               float64
		weakWifi, weakCell bool
	}{
		{"unknown values", relayStats{CellularOK: true}, 0, false, false},
		{"good", relayStats{WifiRSSI: -50, SignalDBm: -90}, 0.01, false, false},
		{"wifi at threshold", relayStats{WifiRSSI: -75, SignalDBm: -90}, 0, false, false},
		{"weak wifi", relayStats{WifiRSSI: -76, SignalDBm: -90}, 0, true, false},
		{"cell at threshold", relayStats{SignalDBm: -110}, 0, false, false},
		{"weak cell signal", relayStats{SignalDBm: -111}, 0, false, true},
		{"weak cell by loss", relayStats{SignalDBm: -80}, 0.06, false, true},
		{"both", relayStats{WifiRSSI: -80, SignalDBm: -120}, 0, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := tt.stats
			r := relayStatus{ID: "r1", Connected: true, Stats: &st,
				Bond: &pathStats{State: "ALIVE", SRTT: 50 * time.Millisecond, Loss: tt.loss}}
			tr := newTracker()
			var v View
			for range hintOnAfter {
				v = tr.view(hubStatus{Relays: []relayStatus{r}}, time.Now())
			}
			if rv := v.Relays[0]; rv.WeakWifi != tt.weakWifi || rv.WeakCell != tt.weakCell {
				t.Fatalf("weak wifi %v cell %v", rv.WeakWifi, rv.WeakCell)
			}
		})
	}
}

func TestHintDebounce(t *testing.T) {
	tr := newTracker()
	at := time.Unix(0, 0)
	step := func(rssi int, cellOK bool) RelayView {
		at = at.Add(time.Second)
		st := relayStats{WifiRSSI: rssi, CellularOK: cellOK}
		return tr.view(hubStatus{Relays: []relayStatus{{ID: "r1", Connected: true, Stats: &st}}}, at).Relays[0]
	}
	// Flapping around the threshold never shows the hint.
	for i := range 20 {
		if rv := step(map[bool]int{true: -80, false: -60}[i%2 == 0], true); rv.WeakWifi {
			t.Fatalf("flapping showed the hint at %d", i)
		}
	}
	for i := range hintOnAfter {
		if rv := step(-80, true); rv.WeakWifi != (i == hintOnAfter-1) {
			t.Fatalf("on after %d: %v", i+1, rv.WeakWifi)
		}
	}
	// Back just above the threshold (inside the margin): stays shown.
	for range 2 * hintOffAfter {
		if rv := step(-74, true); !rv.WeakWifi {
			t.Fatal("cleared inside the margin")
		}
	}
	for i := range hintOffAfter {
		if rv := step(-60, true); rv.WeakWifi != (i < hintOffAfter-1) {
			t.Fatalf("off after %d: %v", i+1, rv.WeakWifi)
		}
	}
	// A single no-data report does not flag the relay.
	if rv := step(-60, false); !rv.CellularOK {
		t.Fatal("one sample flagged no data")
	}
}

func TestRelayViewDefaults(t *testing.T) {
	v := newTracker().view(hubStatus{Mode: "OFFLINE", Relays: []relayStatus{{ID: "r1", Name: "S23",
		LastReason: "leaving: limit"}}}, time.Now())
	rv := v.Relays[0]
	if rv.Priority != "normal" || rv.Up != "on" || rv.Down != "on" || rv.Battery != -1 || rv.HasPath ||
		rv.LastReason != "leaving: limit" || rv.Connected {
		t.Fatalf("defaults %+v", rv)
	}
}

func TestRates(t *testing.T) {
	tr := newTracker()
	t0 := time.Unix(1000, 0)
	relay := func(up, down uint64, b *pathStats) relayStatus {
		return relayStatus{ID: "r1", Connected: true, Stats: &relayStats{BytesUp: up, BytesDown: down}, Bond: b}
	}
	// DIRECT (no bond): rates from counter deltas.
	v := tr.view(hubStatus{Relays: []relayStatus{relay(0, 0, nil)}}, t0)
	if v.UpMbps != 0 || v.DownMbps != 0 {
		t.Fatalf("first sample %v %v", v.UpMbps, v.DownMbps)
	}
	v = tr.view(hubStatus{Relays: []relayStatus{relay(250_000, 1_000_000, nil)}}, t0.Add(2*time.Second))
	if !near(v.UpMbps, 1) || !near(v.DownMbps, 4) {
		t.Fatalf("delta rates %v %v", v.UpMbps, v.DownMbps)
	}
	// Counters reset (relay reconnected): no negative rate.
	v = tr.view(hubStatus{Relays: []relayStatus{relay(10, 10, nil)}}, t0.Add(3*time.Second))
	if v.UpMbps != 0 || v.DownMbps != 0 {
		t.Fatalf("after reset %v %v", v.UpMbps, v.DownMbps)
	}
	// Bonded: bond path rates, summed over relays.
	b := &pathStats{TxRate: 125_000, RxRate: 625_000}
	r2 := relay(0, 0, b)
	r2.ID = "r2"
	v = tr.view(hubStatus{Bond: &struct{}{}, Relays: []relayStatus{relay(0, 0, b), r2}}, t0.Add(4*time.Second))
	if !near(v.UpMbps, 2) || !near(v.DownMbps, 10) || !near(v.Relays[1].DownMbps, 5) {
		t.Fatalf("bond rates %v %v", v.UpMbps, v.DownMbps)
	}
	if len(v.History) != 4 || v.History[3].Down != v.DownMbps {
		t.Fatalf("history %v", v.History)
	}
}

func TestHistoryCap(t *testing.T) {
	tr := newTracker()
	t0 := time.Unix(0, 0)
	var v View
	for i := range historyLen + 25 {
		v = tr.view(hubStatus{}, t0.Add(time.Duration(i)*time.Second))
	}
	if len(v.History) != historyLen || v.History[0].T != t0.Add(25*time.Second).UnixMilli() {
		t.Fatalf("history len %d first %d", len(v.History), v.History[0].T)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		err   error
		state string
	}{
		{fmt.Errorf("ipc connect: %w", fs.ErrNotExist), stateNoDaemon},
		{fmt.Errorf("ipc connect: %w", syscall.ECONNREFUSED), stateNoDaemon},
		{fmt.Errorf("ipc connect: %w", fs.ErrPermission), stateNoAccess},
		{fmt.Errorf("x: %w", &ipc.Error{Code: ipc.CodeServer, Message: "unknown relay"}), stateError},
		{errors.New("boom"), stateError},
	}
	for _, tt := range tests {
		if got, _ := classify(tt.err); got != tt.state {
			t.Errorf("%v: %s, want %s", tt.err, got, tt.state)
		}
	}
	if _, msg := classify(&ipc.Error{Message: "unknown relay"}); msg != "unknown relay" {
		t.Errorf("ipc message %q", msg)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLogText(t *testing.T) {
	got := logText(logsExport{Version: "v1.0.0", Lines: []string{"a", "b"}})
	if got != "# xs-hubd v1.0.0 log, redacted (codes, secrets, keys, MACs, public IPs)\na\nb\n" {
		t.Fatalf("logText = %q", got)
	}
}
