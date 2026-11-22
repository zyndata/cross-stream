package bond

import (
	"errors"
	"time"
)

// ReliableBacklogMax is how many reliable payload bytes may wait for a first
// transmission before SendReliable reports the backlog full (PEP stream data
// then waits for Room; control frames are still accepted).
const ReliableBacklogMax = 256 << 10

// ErrNotReliable is returned for a payload that is not a stream frame block.
var ErrNotReliable = errors.New("bond: reliable payload must start with a stream frame type (0x20-0x2F)")

// IsReliable reports whether a sequenced payload is a reliable block of
// stream frames (first byte 0x20–0x2F, a frame type) rather than an IPv4
// packet (first nibble 4). Both share the IP frame's seq space
// (protocol.md "Frames", decisions.md phase 13).
func IsReliable(pkt []byte) bool { return len(pkt) > 0 && pkt[0]>>4 == 2 }

// SendReliable queues a reliable payload (copied): encoded stream frames that
// are re-sent until acked, never dropped from the queue and delivered by the
// peer's Receive at once (no reorder hold). full reports whether the backlog
// waiting for a first transmission reached ReliableBacklogMax; the caller
// should then wait for Room before queueing more stream data.
func (e *Engine) SendReliable(b []byte) (full bool, err error) {
	if !IsReliable(b) {
		return false, ErrNotReliable
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.cfg.Now()
	e.relq = append(e.relq, queued{pkt: append([]byte(nil), b...), at: now})
	e.relBytes += len(b)
	e.flush(now)
	if len(e.relq) > 0 {
		e.kick()
	}
	full = e.relBytes >= ReliableBacklogMax
	if full {
		e.relWaiting = true
	}
	return full, nil
}

// Room signals once after SendReliable reported a full backlog and the
// backlog fell below ReliableBacklogMax again.
func (e *Engine) Room() <-chan struct{} { return e.room }

// ReliableBacklog returns the reliable bytes waiting for a first transmission.
func (e *Engine) ReliableBacklog() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.relBytes
}

// noteRoom wakes a waiting SendReliable caller when the backlog has room.
func (e *Engine) noteRoom() {
	if !e.relWaiting || e.relBytes >= ReliableBacklogMax {
		return
	}
	e.relWaiting = false
	select {
	case e.room <- struct{}{}:
	default:
	}
}

// popReliable takes the head of the reliable queue.
func (e *Engine) popReliable() queued {
	q := e.relq[0]
	e.relq[0] = queued{}
	e.relq = e.relq[1:]
	if len(e.relq) == 0 {
		e.relq = nil
	}
	e.relBytes -= len(q.pkt)
	return q
}

// queuedSizes returns the outer sizes of the first two packets waiting to
// be sent (IP queue first, then the reliable queue); 0 for a missing one.
func (e *Engine) queuedSizes() (first, second int) {
	n := 0
	for _, q := range [][]queued{e.queue, e.relq} {
		for _, x := range q {
			if n == 0 {
				first = IPOverhead + len(x.pkt)
			} else {
				return first, IPOverhead + len(x.pkt)
			}
			n++
		}
	}
	return first, 0
}

// retryable reports whether a lost transmission may be sent again: reliable
// payloads always, IP packets within MaxAttempts and RetransmitMaxAge.
func retryable(now time.Time, sp *sentPacket) bool {
	return IsReliable(sp.pkt) || (sp.attempts < MaxAttempts && now.Sub(sp.first) < RetransmitMaxAge)
}
