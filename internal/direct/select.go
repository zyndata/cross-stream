package direct

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// Relay selection constants (architecture.md "Modes", decisions.md phase 06).
const (
	StickyFor     = 10 * time.Minute // a destination IP keeps its relay this long after its last new flow
	DefaultWeight = 125_000          // bytes/s (1 Mb/s): weight of a relay with no measured rate
)

// Relay is a relay DIRECT flows can go through.
type Relay struct {
	ID     string
	Proxy  netip.AddrPort // the relay's proxy listener (relay IP, TCP 51903)
	Backup bool           // used only when no other relay is available
	Weight float64        // measured rate in bytes/s; below DefaultWeight counts as DefaultWeight
	// Dial, when set, reaches destinations itself (a hub local uplink: a
	// socket bound to the interface) instead of through a relay proxy.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

type sticky struct {
	relay string
	until time.Time
}

// selector assigns new flows to relays: sticky per destination IP, else
// the relay with the fewest flows relative to its weight. Not locked.
type selector struct {
	sticky map[netip.Addr]sticky
}

func newSelector() *selector { return &selector{sticky: make(map[netip.Addr]sticky)} }

// candidates returns the relays a new flow may use: every non-backup relay,
// or the backup relays when there is none. Sorted by id.
func candidates(relays []Relay) []Relay {
	var normal, backup []Relay
	for _, r := range relays {
		if r.Backup {
			backup = append(backup, r)
		} else {
			normal = append(normal, r)
		}
	}
	out := normal
	if len(out) == 0 {
		out = backup
	}
	slices.SortFunc(out, func(a, b Relay) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// pick returns the relay for a new flow to dst. flows counts the open flows
// per relay id.
func (s *selector) pick(now time.Time, dst netip.Addr, relays []Relay, flows map[string]int) (Relay, bool) {
	cands := candidates(relays)
	if len(cands) == 0 {
		return Relay{}, false
	}
	if st, ok := s.sticky[dst]; ok && now.Before(st.until) {
		if i := slices.IndexFunc(cands, func(r Relay) bool { return r.ID == st.relay }); i >= 0 {
			s.sticky[dst] = sticky{relay: st.relay, until: now.Add(StickyFor)}
			return cands[i], true
		}
	}
	best, bestScore := 0, 0.0
	for i, r := range cands {
		score := float64(flows[r.ID]+1) / max(r.Weight, DefaultWeight)
		if i == 0 || score < bestScore {
			best, bestScore = i, score
		}
	}
	s.sticky[dst] = sticky{relay: cands[best].ID, until: now.Add(StickyFor)}
	return cands[best], true
}

// expire drops sticky entries that ran out.
func (s *selector) expire(now time.Time) {
	for ip, st := range s.sticky {
		if !now.Before(st.until) {
			delete(s.sticky, ip)
		}
	}
}
