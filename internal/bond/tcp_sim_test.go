package bond

import (
	"encoding/binary"
	"testing"
	"time"
)

// --- a minimal inner TCP for the simulator: bulk flows from node a to b ---
//
// SACK-based loss recovery (3 later segments sacked), CUBIC-like
// multiplicative decrease (0.7) with Reno growth, RTO with a 200 ms floor.
// Segments are IPv4/TCP-shaped so the bond treats pure ACKs as it does in
// the field (delivered at once past gaps). Header fields reused:
// src port = flow id, seq / ack as usual, IPv4 source address = SACKed seq.

const (
	tcpPayload = 1180 - 40 // fits the 1232-byte path MTU (tunnel MTU 1195 in the field)
	tcpMaxWnd  = 3000      // packets (~3.5 MB, Linux autotuning range)
	tcpMinRTO  = 200 * time.Millisecond
)

type tcpSeg struct {
	sent   time.Time
	sacked bool
	lost   bool
	retx   bool
}

type tcpFlow struct {
	s  *sim
	id uint16

	// sender (node a)
	cwnd, ssthresh float64
	next, una      uint32
	segs           map[uint32]*tcpSeg
	highSack       uint32
	inRecovery     bool
	recoverAt      uint32
	inPipe         int      // segments counted in flight (RFC 6675 pipe)
	lostQ          []uint32 // lost segments waiting for a re-send
	lossScan       uint32   // segments below were already checked for loss
	srtt, rttvar   time.Duration
	rtoAt          time.Time
	losses, rtos   int

	// receiver (node b)
	rcvNext uint32
	ooo     map[uint32]bool
	bytes   int
}

// tcpFlows runs n bulk flows a → b; it installs the delivery hooks.
func (s *sim) tcpFlows(n int) []*tcpFlow {
	fs := make([]*tcpFlow, n)
	for i := range fs {
		fs[i] = &tcpFlow{s: s, id: uint16(i), cwnd: 10, ssthresh: 1e9, segs: map[uint32]*tcpSeg{}, ooo: map[uint32]bool{}}
	}
	// Deliver runs under the engine lock: the flows react in a separate event.
	s.b.hook = func(pkt []byte) {
		pkt = append([]byte(nil), pkt...)
		s.at(s.now, func() { fs[binary.BigEndian.Uint16(pkt[20:])].onData(pkt) })
	}
	s.a.hook = func(pkt []byte) {
		pkt = append([]byte(nil), pkt...)
		s.at(s.now, func() { fs[binary.BigEndian.Uint16(pkt[20:])].onAck(pkt) })
	}
	for _, f := range fs {
		f.trySend()
	}
	return fs
}

func tcpBytes(fs []*tcpFlow) int {
	n := 0
	for _, f := range fs {
		n += f.bytes
	}
	return n
}

func (f *tcpFlow) segment(payload int, seq, ack, sack uint32) []byte {
	p := tcpPacket(payload, 0x10)
	binary.BigEndian.PutUint32(p[12:], sack)
	binary.BigEndian.PutUint16(p[20:], f.id)
	binary.BigEndian.PutUint32(p[24:], seq)
	binary.BigEndian.PutUint32(p[28:], ack)
	return p
}

// counted reports whether sg is part of the pipe.
func (sg *tcpSeg) counted() bool { return !sg.sacked && (!sg.lost || sg.retx) }

func (f *tcpFlow) xmit(seq uint32) {
	f.s.a.e.Send(f.segment(tcpPayload, seq, 0, 0))
	f.s.a.poke()
	if f.rtoAt.IsZero() {
		f.armRTO()
	}
}

func (f *tcpFlow) trySend() {
	for f.inPipe < int(f.cwnd) {
		if len(f.lostQ) > 0 {
			seq := f.lostQ[0]
			f.lostQ = f.lostQ[1:]
			if sg := f.segs[seq]; sg != nil && sg.lost && !sg.retx && !sg.sacked {
				sg.retx, sg.sent = true, f.s.now
				f.inPipe++
				f.xmit(seq)
			}
			continue
		}
		switch {
		case f.next-f.una < tcpMaxWnd:
			f.segs[f.next] = &tcpSeg{sent: f.s.now}
			f.inPipe++
			f.xmit(f.next)
			f.next++
		default:
			return
		}
	}
}

func (f *tcpFlow) rto() time.Duration {
	if f.srtt == 0 {
		return time.Second
	}
	return max(tcpMinRTO, f.srtt+4*f.rttvar)
}

func (f *tcpFlow) armRTO() {
	at := f.s.now.Add(f.rto())
	f.rtoAt = at
	f.s.at(at, func() {
		if !f.rtoAt.Equal(at) || f.una == f.next {
			return
		}
		f.rtos++
		f.ssthresh = max(f.cwnd*0.7, 2)
		f.cwnd = 1
		f.inRecovery = false
		f.lostQ = f.lostQ[:0]
		for seq := f.una; seq != f.next; seq++ {
			if sg := f.segs[seq]; sg != nil && !sg.sacked {
				if sg.counted() {
					f.inPipe--
				}
				sg.lost, sg.retx = true, false
				f.lostQ = append(f.lostQ, seq)
			}
		}
		f.armRTO()
		f.trySend()
	})
}

func (f *tcpFlow) rttSample(rtt time.Duration) {
	if f.srtt == 0 {
		f.srtt, f.rttvar = rtt, rtt/2
		return
	}
	d := f.srtt - rtt
	if d < 0 {
		d = -d
	}
	f.rttvar = (3*f.rttvar + d) / 4
	f.srtt = (7*f.srtt + rtt) / 8
}

func (f *tcpFlow) onAck(pkt []byte) {
	ack := binary.BigEndian.Uint32(pkt[28:])
	sack := binary.BigEndian.Uint32(pkt[12:])
	newly := 0
	if sg := f.segs[sack]; sg != nil && !sg.sacked && seqDiff(sack, f.una) >= 0 {
		if sg.counted() {
			f.inPipe--
		}
		sg.sacked = true
		newly++
		if !sg.retx {
			f.rttSample(f.s.now.Sub(sg.sent))
		}
		if seqDiff(sack, f.highSack) > 0 {
			f.highSack = sack
		}
	}
	for seqDiff(ack, f.una) > 0 {
		if sg := f.segs[f.una]; sg != nil && !sg.sacked {
			newly++
			if sg.counted() {
				f.inPipe--
			}
		}
		delete(f.segs, f.una)
		f.una++
	}
	if newly > 0 {
		f.armRTO()
		if f.una == f.next {
			f.rtoAt = time.Time{}
		}
	}
	if f.inRecovery && seqDiff(f.una, f.recoverAt) >= 0 {
		f.inRecovery = false
	}
	lost := false
	if seqDiff(f.lossScan, f.una) < 0 {
		f.lossScan = f.una
	}
	for ; seqDiff(f.lossScan+3, f.highSack) <= 0 && f.lossScan != f.next; f.lossScan++ {
		if sg := f.segs[f.lossScan]; sg != nil && !sg.sacked && !sg.lost {
			sg.lost, lost = true, true
			f.inPipe--
			f.lostQ = append(f.lostQ, f.lossScan)
		}
	}
	if lost && !f.inRecovery {
		f.losses++
		f.inRecovery, f.recoverAt = true, f.next
		f.ssthresh = max(f.cwnd*0.7, 2)
		f.cwnd = f.ssthresh
	}
	if !f.inRecovery {
		for range newly {
			if f.cwnd < f.ssthresh {
				f.cwnd++
			} else {
				f.cwnd += 1 / f.cwnd
			}
		}
		f.cwnd = min(f.cwnd, tcpMaxWnd)
	}
	f.trySend()
}

func (f *tcpFlow) onData(pkt []byte) {
	seq := binary.BigEndian.Uint32(pkt[24:])
	switch d := seqDiff(seq, f.rcvNext); {
	case d == 0:
		f.rcvNext++
		f.bytes += tcpPayload
		for f.ooo[f.rcvNext] {
			delete(f.ooo, f.rcvNext)
			f.rcvNext++
			f.bytes += tcpPayload
		}
	case d > 0:
		f.ooo[seq] = true
	}
	f.s.b.e.Send(f.segment(0, 0, f.rcvNext, seq))
	f.s.b.poke()
}

// cellular2 is the phase 07 field setup: a phone on LTE behind 464XLAT and a
// SIM modem, both with deep buffers and bursty (batched) delivery.
var cellular2 = []linkSpec{
	{mbps: 9, rtt: 50 * time.Millisecond, jitter: 5 * time.Millisecond, burst: 30 * time.Millisecond, bBurst: 40 * time.Millisecond, stall: 200 * time.Millisecond, queue: 600 * time.Millisecond, mtu: 1232},
	{mbps: 6, rtt: 40 * time.Millisecond, jitter: 5 * time.Millisecond, burst: 30 * time.Millisecond, bBurst: 40 * time.Millisecond, stall: 200 * time.Millisecond, queue: 600 * time.Millisecond},
}

// tcpRun carries flows TCP flows over specs for d (after 1 s of path
// probing); paths listed in off do not carry data from a. It returns the
// goodput of the last 30 s in Mb/s.
func tcpRun(s *sim, flows int, d time.Duration, off ...uint8) (float64, []*tcpFlow) {
	for _, id := range off {
		s.a.e.SetPathConfig(id, PathConfig{Dir: DirOff})
	}
	s.run(time.Second)
	fs := s.tcpFlows(flows)
	s.run(d - 30*time.Second)
	at, b := s.now, tcpBytes(fs)
	s.run(30 * time.Second)
	return float64(tcpBytes(fs)-b) * 8 / s.now.Sub(at).Seconds() / 1e6, fs
}

func logPaths(s *sim, fs []*tcpFlow) {
	s.t.Helper()
	for _, p := range s.a.e.Stats().Paths {
		s.t.Logf("  path %d: %s btlbw %.2f Mb/s pacing %.2f srtt %v data rtt %v min %v cwnd %d lost %d", p.ID, p.CC,
			float64(p.BtlBw)*8/1e6, float64(p.Pacing)*8/1e6, p.SRTT, p.DataRTT, p.MinRTT, p.Cwnd, p.Lost)
	}
	st := s.a.e.Stats()
	losses, rtos := 0, 0
	for _, f := range fs {
		losses, rtos = losses+f.losses, rtos+f.rtos
	}
	s.t.Logf("  queue drops %d, receiver skipped %d, tcp loss episodes %d, RTOs %d", st.Dropped, s.b.e.Stats().Skipped, losses, rtos)
}

// TestSimTCPBondsCellular: 4 TCP downloads over two bursty, stalling,
// deep-buffered cellular paths beat the best single path, and the paths
// leave STARTUP (phase 07 field test: bonded was slower than either path
// alone, both paths stuck in STARTUP with an inflated estimate).
func TestSimTCPBondsCellular(t *testing.T) {
	const d = 50 * time.Second
	for seed := uint64(1); seed <= 5; seed++ {
		single := 0.0
		for _, id := range []uint8{1, 2} {
			s := newSimSeed(t, cellular2, seed)
			got, fs := tcpRun(s, 4, d, 3-id)
			t.Logf("seed %d path %d alone: %.2f Mb/s", seed, id, got)
			logPaths(s, fs)
			single = max(single, got)
		}
		s := newSimSeed(t, cellular2, seed)
		start := s.now
		var leftStartup [2]time.Duration
		var watch func()
		watch = func() {
			for i, p := range s.a.e.Stats().Paths {
				if leftStartup[i] == 0 && p.CC == "probe_bw" {
					leftStartup[i] = s.now.Sub(start)
				}
			}
			s.at(s.now.Add(100*time.Millisecond), watch)
		}
		s.at(s.now, watch)
		got, fs := tcpRun(s, 4, d)
		t.Logf("seed %d bonded: %.2f Mb/s (%.2f × best single %.2f), reached probe_bw after %v", seed, got, got/single, single, leftStartup)
		logPaths(s, fs)
		if got < 1.3*single {
			t.Errorf("seed %d: bonded %.2f Mb/s, want ≥ 1.3 × best single path %.2f", seed, got, single)
		}
		for i, at := range leftStartup {
			if at == 0 || at > 30*time.Second {
				t.Errorf("seed %d: path %d reached probe_bw after %v, want within 30 s", seed, i+1, at)
			}
		}
		for i, p := range s.a.e.Stats().Paths {
			// Field: pacing 2.89 × an estimate 5× too high flooded the links.
			if link := cellular2[i].mbps; float64(p.Pacing)*8/1e6 > 2*link {
				t.Errorf("seed %d: path %d paces at %.1f Mb/s, want ≤ 2 × its %.0f Mb/s link", seed, p.ID, float64(p.Pacing)*8/1e6, link)
			}
		}
	}
}
