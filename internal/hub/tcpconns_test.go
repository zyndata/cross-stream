package hub

import (
	"log/slog"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/transport"
)

func TestConnPolicy(t *testing.T) {
	type phase struct {
		secs    int
		loss    float64 // retransmitted / sent
		perSec  uint64  // sent per second
		restart bool    // counters start again (new connection set)
		want    int     // choice at the end of the phase
	}
	tests := []struct {
		name   string
		fixed  int
		phases []phase
	}{
		{"clean stays default", 0, []phase{{60, 0, 1000, false, 2}}},
		{"2 % stays default", 0, []phase{{60, 0.02, 1000, false, 2}}},
		{"5 % goes lossy within the window", 0, []phase{{9, 0.05, 1000, false, 2}, {2, 0.05, 1000, false, 4}}},
		{"lossy then calm needs 30 s", 0, []phase{
			{12, 0.05, 1000, false, 4}, {25, 0, 1000, false, 4}, {6, 0, 1000, false, 2}}},
		{"lossy, 2 % keeps 4", 0, []phase{{12, 0.05, 1000, false, 4}, {60, 0.02, 1000, false, 4}}},
		{"light traffic decides nothing", 0, []phase{{60, 0.5, 5, false, 2}}},
		{"idle while lossy keeps 4", 0, []phase{{12, 0.05, 1000, false, 4}, {60, 0, 0, false, 4}}},
		{"counter restart", 0, []phase{{8, 0.05, 1000, false, 2}, {8, 0.05, 1000, true, 2}, {3, 0.05, 1000, false, 4}}},
		{"override", 3, []phase{{30, 0.1, 1000, false, 3}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newConnPolicy(tt.fixed)
			now := time.Unix(1_700_000_000, 0)
			var sent, retrans float64
			got := p.conns()
			for _, ph := range tt.phases {
				if ph.restart {
					sent, retrans = 0, 0
				}
				for range ph.secs {
					now = now.Add(time.Second)
					sent += float64(ph.perSec)
					retrans += ph.loss * float64(ph.perSec)
					got, _ = p.update(now, uint64(retrans), uint64(sent))
				}
				if got != ph.want {
					t.Fatalf("after %+v: %d connections, want %d", ph, got, ph.want)
				}
			}
		})
	}
}

// fakeSet is a transport.Conn with counters and a resize hook.
type fakeSet struct {
	transport.Conn
	ctr  transport.TCPCounters
	want int
}

func (f *fakeSet) TCPCounters() transport.TCPCounters { return f.ctr }
func (f *fakeSet) SetConns(n int)                     { f.want = n }

// TestLocalPathConns: a local tcp:// path resizes its own connection set
// when the policy moves, and reports the live count.
func TestLocalPathConns(t *testing.T) {
	d := &Daemon{log: slog.New(slog.DiscardHandler)}
	lp := &localPath{d: d, id: "u1"}
	lp.initConns(0)
	if lp.tcpConns() != TCPConnsDefault || lp.liveConns() != 0 {
		t.Fatalf("start: %d / %d", lp.tcpConns(), lp.liveConns())
	}
	f := &fakeSet{ctr: transport.TCPCounters{Conns: 2}}
	var c transport.Conn = f
	lp.conn.Store(&c)
	now := time.Unix(1_700_000_000, 0)
	for range 12 {
		now = now.Add(time.Second)
		f.ctr.Sent += 1000
		f.ctr.Retrans += 50
		lp.tickConns(now)
	}
	if f.want != TCPConnsLossy || lp.tcpConns() != TCPConnsLossy || lp.liveConns() != 2 {
		t.Fatalf("lossy: resized to %d, choice %d, live %d", f.want, lp.tcpConns(), lp.liveConns())
	}
}
