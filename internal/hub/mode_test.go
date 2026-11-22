package hub

import (
	"testing"
	"time"
)

func TestModeMachine(t *testing.T) {
	const long = time.Hour // no packet ever
	type step struct {
		at      time.Duration // since start
		paths   int
		session bool
		silence time.Duration
		want    Mode
	}
	tests := []struct {
		name   string
		direct bool
		steps  []step
	}{
		{"offline until a relay", false, []step{
			{0, 0, false, long, ModeOffline}, {time.Second, 1, false, long, ModeConnecting},
			{2 * time.Second, 1, true, 0, ModeBonded}}},
		{"relay joins a hub with the tunnel up", false, []step{{0, 1, true, time.Second, ModeBonded}}},
		{"stale session is not up", false, []step{{0, 1, true, TunnelFresh, ModeConnecting}}},
		{"last relay leaves", false, []step{
			{0, 2, true, 0, ModeBonded}, {time.Second, 0, true, 0, ModeOffline}}},
		{"no direct without enrollment", false, []step{
			{0, 1, false, long, ModeConnecting}, {time.Minute, 1, false, long, ModeConnecting}}},
		{"connect timeout to direct", true, []step{
			{0, 1, false, long, ModeConnecting}, {14 * time.Second, 1, false, long, ModeConnecting},
			{15 * time.Second, 1, false, long, ModeDirect}, {20 * time.Second, 1, true, 0, ModeBonded}}},
		{"short silence stays bonded", false, []step{
			{0, 1, true, 0, ModeBonded}, {9 * time.Second, 1, true, 9 * time.Second, ModeBonded},
			{10 * time.Second, 1, true, 0, ModeBonded}, {15 * time.Second, 1, false, 5 * time.Second, ModeBonded}}},
		{"silent 10 s", false, []step{
			{0, 1, true, 0, ModeBonded}, {10 * time.Second, 1, true, BondedLoss, ModeConnecting}}},
		{"silent 10 s with direct", true, []step{
			{0, 1, true, 0, ModeBonded}, {10 * time.Second, 1, true, BondedLoss, ModeDirect},
			{30 * time.Second, 1, true, 30 * time.Second, ModeDirect},
			{40 * time.Second, 1, true, 4 * time.Second, ModeBonded}}},
		{"direct to offline", true, []step{
			{0, 1, false, long, ModeConnecting}, {15 * time.Second, 1, false, long, ModeDirect},
			{16 * time.Second, 0, false, long, ModeOffline}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Unix(1_000_000, 0)
			m := &ModeMachine{DirectEnabled: tc.direct}
			if mode, _ := m.Mode(); mode != ModeOffline {
				t.Fatalf("initial %s", mode)
			}
			prev := ModeOffline
			for i, s := range tc.steps {
				got, changed := m.Update(start.Add(s.at), ModeInputs{PathsAlive: s.paths, Session: s.session, Silence: s.silence})
				if got != s.want || changed != (got != prev) {
					t.Fatalf("step %d: %s changed=%v, want %s", i, got, changed, s.want)
				}
				if mode, since := m.Mode(); mode != got || (changed && !since.Equal(start.Add(s.at))) {
					t.Fatalf("step %d: Mode() %s since %v", i, mode, since)
				}
				prev = got
			}
		})
	}
}

func TestModeCarries(t *testing.T) {
	for m, want := range map[Mode]bool{ModeOffline: false, ModeConnecting: false, ModeBonded: true, ModeDirect: true} {
		if got := m.carries(); got != want {
			t.Errorf("%s carries %v, want %v", m, got, want)
		}
	}
}
