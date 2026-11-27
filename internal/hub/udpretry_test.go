package hub

import (
	"testing"
	"time"
)

// TestEndpointStepUDPRetry: a path that fell back to TCP tries UDP again
// after udpRetryFirst of working (only while another path is ALIVE), keeps
// UDP once it worked for udpRetryConfirm, and goes back to TCP with a doubled
// gap when UDP is still dead.
func TestEndpointStepUDPRetry(t *testing.T) {
	const udp, tcp = "udp://192.0.2.1:51900", "tcp://192.0.2.1:51900"
	eps := []string{udp, tcp}
	const wait = 5 * time.Second
	t0 := time.Unix(1_700_000_000, 0)
	type ev struct {
		at         time.Duration
		working    bool
		otherAlive bool
		wantMoved  bool
		wantEP     string
	}
	tests := []struct {
		name    string
		steps   []ev
		wantGap time.Duration
	}{
		{"dead UDP falls back to TCP", []ev{
			{0, false, true, false, udp},
			{4 * time.Second, false, true, false, udp},
			{5 * time.Second, false, true, true, tcp},
		}, 0},
		{"UDP works again after the retry", []ev{
			{0, true, true, false, tcp},
			{udpRetryFirst - time.Second, true, true, false, tcp},
			{udpRetryFirst, true, true, true, udp},
			{udpRetryFirst + 2*time.Second, true, true, false, udp},
			{udpRetryFirst + udpRetryConfirm, true, true, false, udp},
			{udpRetryFirst + udpRetryConfirm + time.Hour, true, true, false, udp}, // stays on UDP
		}, 0},
		{"UDP still dead: back to TCP, gap doubles", []ev{
			{0, true, true, false, tcp},
			{udpRetryFirst, true, true, true, udp},
			{udpRetryFirst + time.Second, false, true, false, udp},
			{udpRetryFirst + time.Second + wait, false, true, true, tcp},
			{udpRetryFirst + time.Minute, true, true, false, tcp},
			{udpRetryFirst + 2*udpRetryFirst, true, true, false, tcp},
			{udpRetryFirst + time.Second + wait + 2*udpRetryFirst, true, true, true, udp},
		}, 2 * udpRetryFirst},
		{"no other ALIVE path: no try", []ev{
			{0, true, false, false, tcp},
			{udpRetryFirst, true, false, false, tcp},
			{3 * udpRetryFirst, true, false, false, tcp},
		}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var tr epTrack
			tr.setEP(tc.steps[0].wantEP)
			for i, s := range tc.steps {
				moved := tr.step(t0.Add(s.at), eps, s.working, s.otherAlive, wait)
				if moved != s.wantMoved || tr.ep != s.wantEP {
					t.Fatalf("step %d (+%v): moved %v ep %s, want %v %s", i, s.at, moved, tr.ep, s.wantMoved, s.wantEP)
				}
			}
			if tc.wantGap != 0 && tr.retryGap != tc.wantGap {
				t.Errorf("retry gap %v, want %v", tr.retryGap, tc.wantGap)
			}
		})
	}
	var tr epTrack // the gap stops at udpRetryMax
	tr.setEP(tcp)
	tr.retryGap = udpRetryMax
	now := t0
	for _, s := range []bool{true, true} {
		tr.step(now, eps, s, true, wait)
		now = now.Add(udpRetryMax)
	}
	tr.step(now, eps, false, true, wait)
	tr.step(now.Add(wait), eps, false, true, wait)
	if tr.ep != tcp || tr.retryGap != udpRetryMax {
		t.Errorf("after a failed try at the max gap: ep %s gap %v, want %s %v", tr.ep, tr.retryGap, tcp, udpRetryMax)
	}
}
