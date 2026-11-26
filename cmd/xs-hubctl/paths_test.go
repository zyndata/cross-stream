package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/pairing"
)

func TestWriteSample(t *testing.T) {
	start := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	ps := bond.PathStats{ID: 3, State: "alive", CC: "probe_bw", SRTT: 62 * time.Millisecond,
		MinRTT: 40 * time.Millisecond, BDPRTT: 55 * time.Millisecond, DataRTT: 80 * time.Millisecond, BtlBw: 5_000_000, Pacing: 5_000_000,
		TxRate: 2_500_000, RxRate: 125_000, Inflight: 200 * 1024, Cwnd: 400 * 1024, InflightHi: 300 * 1024,
		AppLimited: true, Loss: 0.012, Lost: 15, Retransmit: 12}
	st := hub.Status{
		Bond: &hub.BondStatus{Queue: 7, Dropped: 9},
		PEP:  &hub.PEPStatus{Backlog: 64 * 1024},
		Relays: []hub.RelayStatus{
			{Relay: pairing.Relay{Name: "S23, Ultra"}, Bond: &ps, Stats: &control.Stats{Dropped: 40}},
			{Relay: pairing.Relay{Name: "away"}}, // no path: skipped
		},
	}
	prevPS := ps
	prevPS.Lost, prevPS.Retransmit = 10, 2
	prev := pathSample{rows: []pathRow{{b: prevPS, dropped: 31}}, dropped: 4}

	tests := []struct {
		name string
		prev *pathSample
		want string
	}{
		{"first sample: counters since start", nil,
			"2.0,3,S23  Ultra,alive,probe_bw,62,40,55,80,40.0,40.0,20.0,1.0,200,400,300,true,1.2,15,12,40,7,9,64\n"},
		{"deltas since the previous sample", &prev,
			"2.0,3,S23  Ultra,alive,probe_bw,62,40,55,80,40.0,40.0,20.0,1.0,200,400,300,true,1.2,5,10,9,7,5,64\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			if err := writeSample(&b, start, tc.prev, sampleOf(start.Add(2*time.Second), st)); err != nil {
				t.Fatal(err)
			}
			if b.String() != tc.want {
				t.Errorf("got  %q\nwant %q", b.String(), tc.want)
			}
			if got, want := strings.Count(tc.want, ","), strings.Count(pathsHeader, ","); got != want {
				t.Errorf("columns: line %d, header %d", got, want)
			}
		})
	}
}

func TestDelta(t *testing.T) {
	tests := []struct{ cur, prev, want uint64 }{{10, 4, 6}, {4, 4, 0}, {3, 9, 3}}
	for _, tc := range tests {
		if got := delta(tc.cur, tc.prev); got != tc.want {
			t.Errorf("delta(%d, %d) = %d, want %d", tc.cur, tc.prev, got, tc.want)
		}
	}
}
