package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/enroll"
	"github.com/zyndata/cross-stream/internal/pep"
	"github.com/zyndata/cross-stream/internal/server/admin"
	"github.com/zyndata/cross-stream/internal/session"
	"github.com/zyndata/cross-stream/internal/stats"
	"github.com/zyndata/cross-stream/internal/wire"
)

// SessionIdleTimeout removes a session that sent nothing for this long
// (hubs send a keepalive every session.KeepaliveInterval).
const SessionIdleTimeout = 3 * time.Minute

// hubSession is one hub's session in the table. rx/tx are guarded by Server.mu.
type hubSession struct {
	hub     HubRecord
	ip      netip.Addr
	sess    *session.Session
	created time.Time
	rx, tx  stats.Rate
	tests   hubTests
	fwdReq  []wire.Forward   // last FWD_REQ list
	fwds    []wire.Forward   // accepted port forwards
	pep     *pep.Session     // nil when PEP is off
	names   map[uint8]string // PATH_NAME per path id (guarded by Server.mu)
}

// sessionFor returns the hub's session, creating it with a fresh random id.
// A rekey or hub restart reuses the session (same id, new keys).
func (s *Server) sessionFor(hub HubRecord) *hubSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if hs := s.byName[hub.Name]; hs != nil {
		return hs
	}
	id := s.newIDLocked()
	ip, _ := netip.ParseAddr(hub.TunnelIP)
	hs := &hubSession{hub: hub, ip: ip, created: s.o.Now(), names: map[uint8]string{}}
	hs.sess = session.New(id, session.Options{
		Now:          s.o.Now,
		Deliver:      func(pkt []byte) { s.uplink(hs, pkt) },
		OnState:      func(pathID uint8, st bond.State) { s.pathState(hs, pathID, st) },
		OnPathAddr:   func(pathID uint8, _ netip.AddrPort) { s.sendPathInfo(hs, pathID) },
		MaxTunnelMTU: s.o.Config.MTU,
	})
	if s.o.Config.PEP {
		hs.pep = pep.New(pep.Config{Link: hs.sess.Bond(), Dial: s.dialOrigin, Serve: s.servePEP,
			Log: s.log.With("hub", hub.Name), Now: s.o.Now})
	}
	hs.sess.Start()
	s.byID[id], s.byName[hub.Name], s.byIP[ip] = hs, hs, hs
	return hs
}

// pathState logs path changes and reports the observed address when a path
// becomes alive. It runs with the session's bond engine locked.
func (s *Server) pathState(hs *hubSession, pathID uint8, st bond.State) {
	s.log.Info("path state", "hub", hs.hub.Name, "path", pathID, "state", st)
	if st == bond.Alive {
		s.sendPathInfo(hs, pathID)
	}
}

// sendPathInfo tells the hub which public address the server sees for a path.
func (s *Server) sendPathInfo(hs *hubSession, pathID uint8) {
	a, ok := hs.sess.PathAddr(pathID)
	if !ok || !a.Addr().Unmap().Is4() {
		return
	}
	obs := netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
	if err := hs.sess.SendOn(pathID, &wire.PathInfo{PathID: pathID, Observed: obs}); err != nil {
		s.log.Debug("path info", "hub", hs.hub.Name, "path", pathID, "err", err)
	}
}

func (s *Server) newIDLocked() uint32 {
	var b [4]byte
	for {
		_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
		id := binary.BigEndian.Uint32(b[:])
		if id != 0 && s.byID[id] == nil {
			return id
		}
	}
}

func (s *Server) removeSession(hs *hubSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byName[hs.hub.Name] != hs {
		return
	}
	delete(s.byID, hs.sess.ID())
	delete(s.byName, hs.hub.Name)
	if s.byIP[hs.ip] == hs { // a renamed hub may already have a new session on this address
		delete(s.byIP, hs.ip)
	}
	hs.sess.Close()
	if hs.pep != nil {
		go hs.pep.Close() // waits for its goroutines; s.mu is held here
	}
	if len(hs.fwds) > 0 {
		go s.applyForwards() // s.mu is held here
	}
}

// closeSessions tells every hub that the server stops.
func (s *Server) closeSessions() {
	s.mu.Lock()
	all := make([]*hubSession, 0, len(s.byID))
	for _, hs := range s.byID {
		all = append(all, hs)
	}
	s.mu.Unlock()
	for _, hs := range all {
		_ = hs.sess.Send(&wire.Close{Reason: wire.CloseShutdown})
		s.removeSession(hs)
	}
}

// tickLoop samples rates and expires idle sessions once a second.
func (s *Server) tickLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick()
		}
	}
}

func (s *Server) tick() {
	now := s.o.Now()
	var idle []*hubSession
	s.mu.Lock()
	for _, hs := range s.byID {
		st := hs.sess.Stats()
		hs.rx.Sample(st.RxBytes, now)
		hs.tx.Sample(st.TxBytes, now)
		last := st.LastRecv
		if last.IsZero() {
			last = hs.created
		}
		if now.Sub(last) >= SessionIdleTimeout {
			idle = append(idle, hs)
		}
	}
	s.mu.Unlock()
	for _, hs := range idle {
		s.log.Info("session expired (idle)", "hub", hs.hub.Name, "session", hs.sess.ID())
		s.removeSession(hs)
	}
}

// Enroll implements admin.Backend.
func (s *Server) Enroll(_ context.Context, name string) (admin.EnrollResult, error) {
	ports, _ := s.codePorts()
	eps, err := Endpoints(s.o.Config.PublicEndpoints, ports, s.o.Detect)
	if err != nil {
		return admin.EnrollResult{}, err
	}
	tok, err := enroll.NewToken()
	if err != nil {
		return admin.EnrollResult{}, fmt.Errorf("enroll: %w", err)
	}
	code, err := enroll.Code{Endpoints: eps, ServerKey: s.o.Key.Public, Token: tok, Name: name}.Encode()
	if err != nil {
		return admin.EnrollResult{}, fmt.Errorf("enroll: %w", err)
	}
	if err := s.o.State.AddToken(name, tok, s.o.Config.TokenTTL); err != nil {
		return admin.EnrollResult{}, err
	}
	expires := s.o.Now().Add(s.o.Config.TokenTTL).UTC()
	s.log.Info("enrollment code created", "hub", name, "expires", expires, "endpoints", eps)
	return admin.EnrollResult{Code: code, Expires: expires}, nil
}

// Hubs implements admin.Backend.
func (s *Server) Hubs(context.Context) ([]admin.Hub, error) {
	recs := s.o.State.Hubs()
	_, tunnel := s.codePorts()
	out := make([]admin.Hub, 0, len(recs))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range recs {
		out = append(out, admin.Hub{Name: h.Name, TunnelIP: h.TunnelIP, PublicKey: h.PublicKey,
			Enrolled: h.Enrolled, Online: s.byName[h.Name] != nil, FwdAllow: h.FwdAllow, Forwards: []admin.Forward{},
			Port: h.Port, Reenroll: h.Port != 0 && h.Port != tunnel})
		if hs := s.byName[h.Name]; hs != nil {
			out[len(out)-1].Forwards = adminForwards(hs.fwds)
		}
	}
	return out, nil
}

// Revoke implements admin.Backend: the key is deleted first, then the session
// is told to close and removed, so the hub cannot reconnect.
func (s *Server) Revoke(_ context.Context, name string) error {
	if _, err := s.o.State.Revoke(name); err != nil {
		return err
	}
	s.mu.Lock()
	hs := s.byName[name]
	delete(s.initTS, name) // a hub enrolled later under this name starts fresh
	s.mu.Unlock()
	if hs != nil {
		_ = hs.sess.Send(&wire.Close{Reason: wire.CloseRevoked})
		s.removeSession(hs)
	}
	s.log.Info("hub revoked", "hub", name, "had_session", hs != nil)
	return nil
}

// Sessions implements admin.Backend.
func (s *Server) Sessions(context.Context) ([]admin.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]admin.Session, 0, len(s.byID))
	for _, hs := range s.byID {
		st := hs.sess.Stats()
		as := admin.Session{Hub: hs.hub.Name, TunnelIP: hs.hub.TunnelIP, RxBytes: st.RxBytes, TxBytes: st.TxBytes,
			RxRate: hs.rx.Value(), TxRate: hs.tx.Value(), LastSeen: st.LastRecv, Forwards: adminForwards(hs.fwds)}
		addrs := make(map[uint8]string, len(st.Paths))
		for _, p := range st.Paths {
			addrs[p.ID] = p.Addr.String()
		}
		for _, p := range st.Bond.Paths {
			as.Paths = append(as.Paths, admin.Path{ID: p.ID, Name: hs.names[p.ID], Addr: addrs[p.ID], Stats: p})
		}
		as.Bond = admin.BondCounters{Queue: st.Bond.Queue, Reorder: st.Bond.Reorder, Skipped: st.Bond.Skipped,
			Dropped: st.Bond.Dropped, Delivered: st.Bond.Delivered, Dups: st.Bond.Dups, Late: st.Bond.Late,
			Mode: st.Bond.Mode, Redundancy: st.Bond.Redundancy, Redundant: st.Bond.Redundant}
		out = append(out, as)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hub < out[j].Hub })
	return out, nil
}
