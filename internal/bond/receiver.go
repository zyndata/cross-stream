package bond

import (
	"slices"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// seqDiff is the signed distance a − b in sequence space (wraps at 2^32).
func seqDiff(a, b uint32) int64 { return int64(int32(a - b)) }

const seqSetBits = 8192

// seqSet remembers which of the last seqSetBits sequence numbers (up to the
// highest seen) were received; used for duplicate detection and ACK bitmaps.
type seqSet struct {
	bits [seqSetBits / 64]uint64
	high uint32
	has  bool
}

func (s *seqSet) reset() { *s = seqSet{} }

func (s *seqSet) idx(seq uint32) (int, uint64) {
	i := seq % seqSetBits
	return int(i / 64), 1 << (i % 64)
}

// contains reports whether seq was recorded; seqs below the window are
// reported as received (too old to tell, treated as duplicates).
func (s *seqSet) contains(seq uint32) bool {
	if !s.has {
		return false
	}
	d := seqDiff(seq, s.high)
	if d > 0 {
		return false
	}
	if d <= -seqSetBits {
		return true
	}
	w, b := s.idx(seq)
	return s.bits[w]&b != 0
}

// tooOld reports whether seq is below the window (contains cannot tell).
func (s *seqSet) tooOld(seq uint32) bool {
	return s.has && seqDiff(seq, s.high) <= -seqSetBits
}

func (s *seqSet) add(seq uint32) {
	if !s.has {
		s.has, s.high = true, seq
		w, b := s.idx(seq)
		s.bits[w] |= b
		return
	}
	d := seqDiff(seq, s.high)
	if d <= -seqSetBits {
		return
	}
	if d > 0 {
		if d >= seqSetBits {
			s.bits = [seqSetBits / 64]uint64{}
		} else {
			for x := s.high + 1; x != seq; x++ {
				w, b := s.idx(x)
				s.bits[w] &^= b
			}
		}
		s.high = seq
	}
	w, b := s.idx(seq)
	s.bits[w] |= b
}

// bitmap returns the ACK bitmap below largest (bit i = largest−1−i).
func (s *seqSet) bitmap(largest uint32) uint64 {
	var m uint64
	for i := range uint32(64) {
		if seq := largest - 1 - i; seqDiff(seq, s.high) > -seqSetBits && s.contains(seq) {
			m |= 1 << i
		}
	}
	return m
}

// pureTCPAck reports whether pkt is an IPv4 TCP segment without payload,
// SYN, FIN or RST (a plain ACK, possibly with SACK options).
func pureTCPAck(pkt []byte) bool {
	if len(pkt) < 20 || pkt[0]>>4 != 4 || pkt[9] != 6 {
		return false
	}
	ihl := int(pkt[0]&0x0F) * 4
	total := int(pkt[2])<<8 | int(pkt[3])
	if ihl < 20 || total > len(pkt) || total < ihl+20 || pkt[6]&0x3F != 0 || pkt[7] != 0 { // MF or fragment offset: not a whole segment
		return false
	}
	tcp := pkt[ihl:total]
	off := int(tcp[12]>>4) * 4
	return off >= 20 && total == ihl+off && tcp[13]&0x07 == 0 && tcp[13]&0x10 != 0
}

// held is a packet waiting behind a gap (nil pkt: a TCP ACK already delivered).
type held struct {
	pkt []byte
	at  time.Time
}

// receiver is the reorder buffer and ACK generator.
type receiver struct {
	init     bool
	next     uint32
	buf      map[uint32]held
	gapSince time.Time // when the head gap started to be held
	seen     seqSet
	pending  []uint32 // received since the last ACK
	lastAck  time.Time

	passRealtime bool // min_latency: UDP/ICMP packets are not held behind gaps

	delivered, skipped, dups uint64
}

func (r *receiver) reset(seq uint32) {
	r.init, r.next = true, seq
	r.buf = make(map[uint32]held)
	r.gapSince = time.Time{}
	r.seen.reset()
}

// onIP handles one received IP frame. deliver is called for packets that
// are now in order. pkt is kept while it waits in the buffer. It returns true
// for a reliable payload the caller must handle now: reliable payloads are
// never held behind a gap (they only keep their seq's place) and are handed
// over even after their gap was skipped or when they are too old for the
// duplicate window (the PEP layer drops repeated stream frames by offset).
func (r *receiver) onIP(now time.Time, seq uint32, pkt []byte, deliver func([]byte)) bool {
	rel := IsReliable(pkt)
	if !r.init {
		r.reset(seq)
	}
	if d := seqDiff(seq, r.next); d < -ResetDistance || d > ResetDistance {
		if rel && d < 0 {
			r.pending = append(r.pending, seq)
			return true // a very late re-send, not a restarted peer
		}
		r.reset(seq)
	}
	r.pending = append(r.pending, seq)
	if r.seen.contains(seq) && (!rel || !r.seen.tooOld(seq)) {
		r.dups++
		return false
	}
	r.seen.add(seq)
	d := seqDiff(seq, r.next)
	switch {
	case d < 0: // already skipped
		if rel {
			return true
		}
		r.dups++
	case d == 0:
		r.delivered++
		if !rel {
			deliver(pkt)
		}
		r.next++
		r.drain(now, deliver)
	default:
		if rel || pureTCPAck(pkt) || (r.passRealtime && classify(pkt).realtime) {
			// Cumulative ACKs need no order: holding them behind a gap
			// would stall the TCP sender's ACK clock. Real-time packets
			// (min_latency) are worth more now than in order. Reliable
			// payloads are ordered per stream by the PEP layer.
			r.delivered++
			if !rel {
				deliver(pkt)
			}
			pkt = nil // keeps the seq's place; nothing to deliver later
		}
		r.buf[seq] = held{pkt: pkt, at: now}
		if r.gapSince.IsZero() {
			r.gapSince = now
		}
		if len(r.buf) > ReorderMax {
			r.skip(deliver)
		}
	}
	return rel
}

// drain delivers buffered packets that are now in order. After a gap was
// filled (now set) the next gap gets a full hold from now: re-sends are
// arriving. After a skip (zero now) it is held from the arrival of the packet
// just above it, so gaps that were never going to be filled (a dead path's
// packets that were too old to re-send) cost one hold together, not one each.
func (r *receiver) drain(now time.Time, deliver func([]byte)) {
	for {
		h, ok := r.buf[r.next]
		if !ok {
			break
		}
		delete(r.buf, r.next)
		if h.pkt != nil {
			r.delivered++
			deliver(h.pkt)
		}
		r.next++
	}
	switch {
	case len(r.buf) == 0:
		r.gapSince = time.Time{}
		return
	case !now.IsZero():
		r.gapSince = now
		return
	}
	r.gapSince = time.Time{}
	var low uint32
	for seq, h := range r.buf {
		if r.gapSince.IsZero() || seqDiff(seq, low) < 0 {
			low, r.gapSince = seq, h.at
		}
	}
}

// skip gives up the gap at the head and delivers up to the next gap.
func (r *receiver) skip(deliver func([]byte)) {
	if len(r.buf) == 0 {
		return
	}
	first, ok := uint32(0), false
	for s := range r.buf {
		if !ok || seqDiff(s, first) < 0 {
			first, ok = s, true
		}
	}
	r.skipped += uint64(seqDiff(first, r.next))
	r.next = first
	r.drain(time.Time{}, deliver)
}

// tick skips the head gap when it was held longer than hold. It returns the
// time the current gap expires (zero if none).
func (r *receiver) tick(now time.Time, hold time.Duration, deliver func([]byte)) time.Time {
	for len(r.buf) > 0 && now.Sub(r.gapSince) >= hold {
		r.skip(deliver)
	}
	if len(r.buf) == 0 {
		return time.Time{}
	}
	return r.gapSince.Add(hold)
}

// ackDue reports whether an ACK should be sent now, and when the next one
// is due otherwise.
func (r *receiver) ackDue(now time.Time) (bool, time.Time) {
	if len(r.pending) == 0 {
		return false, time.Time{}
	}
	due := r.lastAck.Add(AckInterval)
	return len(r.pending) >= AckEvery || !now.Before(due), due
}

// acks builds the ACK frames for the pending seqs: one frame per 64-seq
// window, newest first, at most MaxAckFrames.
func (r *receiver) acks(now time.Time) []wire.Frame {
	p := r.pending
	slices.SortFunc(p, func(a, b uint32) int { return int(seqDiff(b, a)) })
	var out []wire.Frame
	for i := 0; i < len(p) && len(out) < MaxAckFrames; {
		top := p[i]
		out = append(out, &wire.Ack{Largest: top, Bitmap: r.seen.bitmap(top)})
		for i < len(p) && seqDiff(top, p[i]) <= 64 {
			i++
		}
	}
	r.pending = r.pending[:0]
	r.lastAck = now
	return out
}

// holdTime is the reorder hold (decisions.md phase 04).
func holdTime(minSRTT, maxSRTT time.Duration) time.Duration {
	h := max(2*(maxSRTT-minSRTT)+20*time.Millisecond, maxSRTT*3/2+30*time.Millisecond)
	return min(h, MaxHold)
}
