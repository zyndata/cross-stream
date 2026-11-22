package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

// Stream parameters: one sample per second, 5 minutes of history.
const (
	SampleEvery   = time.Second
	HistoryLength = 300
	maxSubs       = 32
)

// Sample is one second of server totals (bytes/s; RX = hub→server).
type Sample struct {
	T      int64                 `json:"t"` // Unix ms
	Total  int                   `json:"total"`
	Online int                   `json:"online"`
	RX     float64               `json:"rx"`
	TX     float64               `json:"tx"`
	Hubs   map[string][2]float64 `json:"hubs"` // name → [rx, tx]
}

// tick is what every subscriber gets each second.
type tick struct {
	Sample   Sample          `json:"sample"`
	Sessions []admin.Session `json:"sessions"`
}

// sampler polls the admin service once a second, keeps the history and
// fans the ticks out to SSE subscribers.
type sampler struct {
	adm Admin
	now func() time.Time
	log *slog.Logger

	mu      sync.Mutex
	history []Sample
	subs    map[chan []byte]struct{}
}

func newSampler(adm Admin, now func() time.Time, log *slog.Logger) *sampler {
	return &sampler{adm: adm, now: now, log: log, subs: map[chan []byte]struct{}{}}
}

func (p *sampler) run(ctx context.Context) {
	t := time.NewTicker(SampleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.sample(ctx)
		}
	}
}

func (p *sampler) sample(ctx context.Context) {
	ss, err := p.adm.Sessions(ctx)
	if err != nil {
		p.log.Debug("web sample", "err", err)
		return
	}
	hubs, err := p.adm.Hubs(ctx)
	if err != nil {
		p.log.Debug("web sample", "err", err)
		return
	}
	smp := Sample{T: p.now().UnixMilli(), Total: len(hubs), Hubs: map[string][2]float64{}}
	for _, s := range ss {
		smp.Online++
		smp.RX += s.RxRate
		smp.TX += s.TxRate
		smp.Hubs[s.Hub] = [2]float64{s.RxRate, s.TxRate}
	}
	if ss == nil {
		ss = []admin.Session{}
	}
	b, err := json.Marshal(tick{Sample: smp, Sessions: ss})
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.history = append(p.history, smp)
	if len(p.history) > HistoryLength {
		p.history = append(p.history[:0:0], p.history[len(p.history)-HistoryLength:]...)
	}
	for c := range p.subs {
		select {
		case c <- b:
		default: // slow client: it misses this tick
		}
	}
}

// subscribe returns the history so far and a channel of encoded ticks.
func (p *sampler) subscribe() ([]Sample, chan []byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.subs) >= maxSubs {
		return nil, nil, false
	}
	c := make(chan []byte, 4)
	p.subs[c] = struct{}{}
	return append([]Sample{}, p.history...), c, true
}

func (p *sampler) unsubscribe(c chan []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.subs, c)
}

// serveStream is the SSE endpoint: event "history" once, then "tick" every
// second. It ends when the session ends (logout, expiry, password change).
func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, a authed) {
	rc := http.NewResponseController(w)
	hist, ch, ok := s.stream.subscribe()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "too many live views")
		return
	}
	defer s.stream.unsubscribe(ch)
	_ = rc.SetReadDeadline(time.Time{}) // a long-lived response: no server ReadTimeout
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	hb, _ := json.Marshal(hist)
	if hist == nil {
		hb = []byte("[]")
	}
	if !sendEvent(w, rc, "history", hb) {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			if !s.sessionLive(a) {
				_ = sendEvent(w, rc, "logout", []byte("{}"))
				return
			}
			if !sendEvent(w, rc, "tick", b) {
				return
			}
		}
	}
}

func (s *Server) sessionLive(a authed) bool {
	if s.o.Ingress {
		return true
	}
	sess, ok := s.sessions.get(a.token)
	if !ok {
		return false
	}
	changed, exists := s.o.Admin.UserChanged(sess.user)
	return exists && !changed.After(sess.created)
}

func sendEvent(w http.ResponseWriter, rc *http.ResponseController, event string, data []byte) bool {
	_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return false
	}
	return rc.Flush() == nil
}
