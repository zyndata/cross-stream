package session

import (
	"net/netip"
	"time"
)

// A stream path spread over several TCP connections (phase 20) reaches the
// server as several stream senders from the same IP. Packets still go on the
// connection of the newest upstream packet (per-packet round-robin reordered
// the download and halved it at 0 % loss, e2e tcploss 2026-11-25), but when
// that connection's send fails — queue full while TCP recovers a loss, or
// closed — SendOn tries the path's other connections heard within
// StreamFresh instead of dropping the packet.
const (
	StreamFresh = 3 * time.Second // a connection silent for longer is not used
	maxStreams  = 8               // per path; up to 4 per TCP path, plus redials
)

// streamConn is one live connection of a stream path.
type streamConn struct {
	conn Sender
	seen time.Time
}

// seeStreamLocked records conn as a live connection of stream path p when
// it is a stream sender from the path's IP (other packets move the path
// through setPathLocked).
func (p *Path) seeStreamLocked(conn Sender, from netip.AddrPort, now time.Time) {
	if !p.Stream || !isStream(conn) || from.Addr() != p.Addr.Addr() {
		return
	}
	for i := range p.streams {
		if p.streams[i].conn == conn {
			p.streams[i].seen = now
			return
		}
	}
	if len(p.streams) == maxStreams { // drop the one heard least recently
		old := 0
		for i := range p.streams {
			if p.streams[i].seen.Before(p.streams[old].seen) {
				old = i
			}
		}
		p.streams = append(p.streams[:old], p.streams[old+1:]...)
	}
	p.streams = append(p.streams, streamConn{conn, now})
}

// freshStreamsLocked copies the connections heard within StreamFresh into
// out (a caller-owned array: SendOn uses them after unlocking).
func (p *Path) freshStreamsLocked(now time.Time, out []Sender) []Sender {
	for _, c := range p.streams {
		if now.Sub(c.seen) < StreamFresh {
			out = append(out, c.conn)
		}
	}
	return out
}

// streamSwitchBacklog is how many packets may wait on the newest
// connection before a path's write prefers one with at most half as many
// (about 40 ms at 8 Mb/s: past the jitter of a healthy connection, well
// before a stalled one's packets outlive the receiver's reorder hold).
const streamSwitchBacklog = 32

// backlogger is a stream sender that reports its send queue length
// (transport.Backlogger).
type backlogger interface{ Backlog() int }

func backlog(c Sender) int {
	if b, ok := c.(backlogger); ok {
		return b.Backlog()
	}
	return 0
}

// write sends buf on p.Conn (the connection of the newest packet: bursts
// stay in order on one TCP connection) unless that one has a queue of
// streamSwitchBacklog and another live connection at most half of it (a TCP
// loss stalls the newest one); when the send fails (queue full while TCP
// recovers a loss, or closed) it tries the path's other live connections,
// round-robin.
func (s *Session) write(p Path, conns []Sender, buf []byte) error {
	first := p.Conn
	if bl := backlog(first); bl >= streamSwitchBacklog && len(conns) > 1 {
		best, bestBL := first, bl
		for _, c := range conns {
			if b := backlog(c); b < bestBL {
				best, bestBL = c, b
			}
		}
		if 2*bestBL <= bl {
			first = best
		}
	}
	_, err := first.WriteToUDPAddrPort(buf, p.Addr)
	if err == nil || len(conns) == 0 {
		return err
	}
	start := int(s.rr.Add(1) % uint32(len(conns)))
	for i := range conns {
		c := conns[(start+i)%len(conns)]
		if c == first {
			continue
		}
		if _, err = c.WriteToUDPAddrPort(buf, p.Addr); err == nil {
			return nil
		}
	}
	return err
}
