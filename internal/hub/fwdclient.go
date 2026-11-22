package hub

import (
	"slices"
	"sync"
	"time"

	"github.com/zyndata/cross-stream/internal/session"
	"github.com/zyndata/cross-stream/internal/wire"
)

// FwdResend is how often an unanswered FWD_REQ is repeated.
const FwdResend = 5 * time.Second

// clientFwd is the port forward list the client asks the server for.
type clientFwd struct {
	mu       sync.Mutex
	set      bool           // SetForwards ran: the list is sent even when empty
	want     []wire.Forward // full desired list
	ack      *wire.FwdAck   // last answer that covers want
	lastSent time.Time
}

// SetForwards sets the port forwards to ask the server for (sent now and
// repeated until the server answers for this list).
func (c *Client) SetForwards(l []wire.Forward) {
	c.fwd.mu.Lock()
	c.fwd.set, c.fwd.want, c.fwd.ack, c.fwd.lastSent = true, slices.Clone(l), nil, time.Time{}
	c.fwd.mu.Unlock()
	if s := c.sess.Load(); s != nil {
		c.sendForwards(s, time.Now())
	}
}

// ForwardAck returns the server's answer for the current list (nil = none yet).
func (c *Client) ForwardAck() *wire.FwdAck {
	c.fwd.mu.Lock()
	defer c.fwd.mu.Unlock()
	return c.fwd.ack
}

// sendForwards sends FWD_REQ when the list is not answered yet and the
// last request is FwdResend old.
func (c *Client) sendForwards(s *session.Session, now time.Time) {
	c.fwd.mu.Lock()
	if !c.fwd.set || c.fwd.ack != nil || now.Sub(c.fwd.lastSent) < FwdResend {
		c.fwd.mu.Unlock()
		return
	}
	c.fwd.lastSent = now
	f := &wire.FwdReq{Forwards: slices.Clone(c.fwd.want)}
	c.fwd.mu.Unlock()
	if err := s.Send(f); err != nil {
		c.Log.Debug("send FWD_REQ", "err", err)
	}
}

// resetForwards makes a new session ask again (the server forgets forwards
// with the session).
func (c *Client) resetForwards() {
	c.fwd.mu.Lock()
	c.fwd.ack, c.fwd.lastSent = nil, time.Time{}
	c.fwd.mu.Unlock()
}

// onFwdAck stores an answer that covers exactly the current list.
func (c *Client) onFwdAck(a *wire.FwdAck) {
	got := slices.Clone(a.Forwards)
	for _, r := range a.Refused {
		got = append(got, r.Forward)
	}
	c.fwd.mu.Lock()
	defer c.fwd.mu.Unlock()
	want := slices.Clone(c.fwd.want)
	cmp := func(x, y wire.Forward) int { return (int(x.Port)<<8 + int(x.Proto)) - (int(y.Port)<<8 + int(y.Proto)) }
	slices.SortFunc(got, cmp)
	slices.SortFunc(want, cmp)
	if slices.Equal(slices.Compact(got), slices.Compact(want)) {
		c.fwd.ack = a
	}
}
