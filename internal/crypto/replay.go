package crypto

// ReplayWindowSize is how far behind the highest seen counter a packet may
// arrive and still be accepted. Multipath bonding reorders heavily.
const ReplayWindowSize = 8192

const (
	blockBits  = 64
	ringBlocks = 256 // ring of 16384 bits; > ReplayWindowSize/blockBits + 1, no aliasing
)

// ReplayWindow is a sliding bitmap of received counters. It accepts
// out-of-order counters within ReplayWindowSize of the highest one seen and
// rejects duplicates and older counters. Not safe for concurrent use.
type ReplayWindow struct {
	last uint64 // highest accepted counter
	ring [ringBlocks]uint64
}

// Check reports whether counter would be accepted, without recording it.
// Use it before the (costly) AEAD open.
func (w *ReplayWindow) Check(counter uint64) bool {
	if counter > MaxCounter {
		return false
	}
	if counter > w.last {
		return true
	}
	if w.last-counter >= ReplayWindowSize {
		return false
	}
	return w.ring[(counter/blockBits)%ringBlocks]&(1<<(counter%blockBits)) == 0
}

// Newer reports whether counter is above every counter accepted so far.
func (w *ReplayWindow) Newer(counter uint64) bool {
	return counter > w.last || (w.last == 0 && w.ring[0]&1 == 0)
}

// Update records counter after a successful open. It returns false (and
// records nothing) if counter is a duplicate or too old.
func (w *ReplayWindow) Update(counter uint64) bool {
	if !w.Check(counter) {
		return false
	}
	idx := counter / blockBits
	if counter > w.last {
		cur := w.last / blockBits
		diff := idx - cur
		if diff > ringBlocks {
			diff = ringBlocks
		}
		for i := uint64(1); i <= diff; i++ {
			w.ring[(cur+i)%ringBlocks] = 0
		}
		w.last = counter
	}
	w.ring[idx%ringBlocks] |= 1 << (counter % blockBits)
	return true
}
