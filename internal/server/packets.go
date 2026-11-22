package server

import (
	"errors"
	"net"
	"net/netip"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
	"github.com/zyndata/cross-stream/internal/session"
	"github.com/zyndata/cross-stream/internal/wire"
)

// readLoop handles outer packets from one UDP socket.
func (s *Server) readLoop(c *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, from, err := c.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Debug("udp read", "err", err)
			continue
		}
		s.handlePacket(buf[:n], netip.AddrPortFrom(from.Addr().Unmap(), from.Port()), c)
	}
}

// handlePacket handles one outer packet from a UDP socket or a stream
// connection; replies go back through c.
func (s *Server) handlePacket(pkt []byte, from netip.AddrPort, c session.Sender) {
	h, payload, err := wire.ParseHeader(pkt)
	if err != nil {
		return
	}
	switch h.Type {
	case wire.TypeHandshakeInit:
		s.handleInit(c, from, h, payload)
	case wire.TypeData:
		s.handleData(c, from, h, pkt)
	}
}

// handleInit answers a HANDSHAKE_INIT from a known hub or one with a valid
// enrollment token. Anything else is dropped without a response. The path is
// only recorded once DATA with the new keys arrives, so a replayed INIT
// cannot redirect traffic; its timestamp must also beat the hub's last one.
// Sources sending INITs too fast are dropped before any DH work.
func (s *Server) handleInit(c session.Sender, from netip.AddrPort, h wire.Header, msg []byte) {
	if !s.hsRate.Allow(from.Addr(), s.o.Now()) {
		s.counters.rateLimited.Add(1)
		s.log.Debug("handshake rate limited", "from", from)
		return
	}
	r, err := crypto.NewResponder(s.o.Key)
	if err != nil {
		s.log.Error("responder", "err", err)
		return
	}
	raw, peer, err := r.ReadInit(msg)
	if err != nil {
		s.log.Debug("handshake rejected", "from", from, "err", err)
		return
	}
	init, err := session.ParseInit(raw)
	if err != nil {
		s.log.Info("handshake payload rejected", "from", from, "err", err)
		return
	}
	if !hubVersionOK(s.o.Config.MinHubVersion, init.SW) {
		s.log.Warn("hub refused: software older than min_hub_version", "hub_name", init.HubName,
			"sw", init.SW, "min_hub_version", s.o.Config.MinHubVersion, "from", from)
		s.counters.versionDeny.Add(1)
		return
	}
	hub, ok := s.o.State.HubByKey(peer)
	if !ok {
		if hub, ok = s.enrollHub(init, peer, from); !ok {
			return
		}
	}
	if !s.freshInit(hub.Name, init.TS) {
		s.counters.replayed.Add(1)
		s.log.Info("replayed or stale handshake dropped", "hub", hub.Name, "from", from)
		return
	}
	hs := s.sessionFor(hub)
	// A revoke or rename may have landed since the lookup above: the session
	// must not outlive the record it was made for.
	if cur, ok := s.o.State.HubByKey(peer); !ok || cur.Name != hub.Name {
		s.removeSession(hs)
		s.log.Info("handshake dropped: hub record changed meanwhile", "hub", hub.Name, "from", from)
		return
	}
	resp, keys, err := session.Respond(r, h.PathID, session.RespPayload{
		V: session.ProtocolVersion, SessionID: hs.sess.ID(), TunnelIP: hub.TunnelIP, MTU: s.o.Config.MTU,
		PEP: hs.pep != nil,
	})
	if err != nil {
		s.log.Error("handshake response", "hub", hub.Name, "err", err)
		return
	}
	hs.sess.InstallKeys(keys, false)
	if _, err := c.WriteToUDPAddrPort(resp, from); err != nil {
		s.log.Warn("send handshake response", "hub", hub.Name, "err", err)
		return
	}
	s.counters.handshakes.Add(1)
	s.log.Info("handshake", "hub", hub.Name, "session", hs.sess.ID(), "path", h.PathID, "from", from, "sw", init.SW)
}

// freshInit records ts as the hub's newest INIT time; false when it is not
// newer than the last accepted one (a replay).
func (s *Server) freshInit(hub string, ts int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := s.initTS[hub]
	// A stored time far ahead of the server clock came from a hub whose
	// clock was wrong and has been corrected since: do not lock it out.
	if ahead := last > s.o.Now().Add(initClockSlack).UnixNano(); ts <= last && !ahead {
		return false
	}
	s.initTS[hub] = ts
	return true
}

// initClockSlack is how far ahead of the server clock an accepted INIT time
// may be before a lower one replaces it.
const initClockSlack = 2 * time.Minute

// enrollHub registers peer with the INIT's enroll token.
func (s *Server) enrollHub(init session.InitPayload, peer [crypto.KeyLen]byte, from netip.AddrPort) (HubRecord, bool) {
	if init.EnrollToken == nil {
		s.log.Info("handshake from unknown hub key dropped", "from", from)
		return HubRecord{}, false
	}
	tok, err := enroll.ParseToken(*init.EnrollToken)
	if err != nil {
		s.log.Info("enrollment rejected", "from", from, "err", err)
		return HubRecord{}, false
	}
	hub, err := s.o.State.Enroll(tok, peer)
	if err != nil {
		s.log.Info("enrollment rejected", "from", from, "err", err)
		return HubRecord{}, false
	}
	s.log.Info("hub enrolled", "hub", hub.Name, "tunnel_ip", hub.TunnelIP, "from", from)
	return hub, true
}

// handleData opens a DATA packet and acts on its frames.
func (s *Server) handleData(c session.Sender, from netip.AddrPort, h wire.Header, pkt []byte) {
	s.mu.Lock()
	hs := s.byID[h.SessionID]
	s.mu.Unlock()
	if hs == nil {
		return
	}
	frames, err := hs.sess.Open(h, pkt, from, c)
	if err != nil {
		s.log.Debug("data rejected", "hub", hs.hub.Name, "err", err)
		return
	}
	for _, f := range frames {
		switch f := f.(type) {
		case *wire.PathConfig:
			hs.sess.SetPathConfig(f.PathID, PathConfigDown(f))
		case *wire.PathName:
			s.mu.Lock()
			if f.Name == "" {
				delete(hs.names, f.PathID)
			} else {
				hs.names[f.PathID] = f.Name // at most 256 entries (u8 path id)
			}
			s.mu.Unlock()
		case *wire.PathGone:
			s.log.Info("path gone", "hub", hs.hub.Name, "path", f.PathID)
			hs.sess.RemovePath(f.PathID)
		case *wire.SessionConfig:
			b := hs.sess.Bond()
			b.SetOverflow(uint64(f.OverflowMbps) * 1_000_000 / 8)
			b.SetRedundancy(bond.Redundancy(f.Redundancy))
			b.SetMode(bond.Mode(f.Mode))
		case *wire.TestReq:
			s.onTestReq(hs, f)
		case *wire.FwdReq:
			s.onFwdReq(hs, f)
		case *wire.TestData:
			hs.tests.counter.Add(f.TestID, h.PathID, len(pkt), s.o.Now()) // ids without a TEST_REQ are ignored
		case *wire.StreamOpen, *wire.StreamOpenResult, *wire.StreamData, *wire.StreamWindow,
			*wire.StreamClose, *wire.StreamReset:
			if hs.pep != nil {
				hs.pep.Handle(f)
			}
		case *wire.Close:
			s.log.Info("hub closed session", "hub", hs.hub.Name, "reason", f.Reason)
			s.removeSession(hs)
			return
		}
	}
}

// PathConfigDown converts a PATH_CONFIG frame to the server's (download
// direction) bond policy.
func PathConfigDown(f *wire.PathConfig) bond.PathConfig {
	return bond.PathConfig{Priority: bond.Priority(f.Priority), RateBps: uint64(f.RateKbps) * 1000 / 8,
		CapBytes: uint64(f.CapMB) * 1_000_000, Dir: bond.DirMode(f.DirDown)}
}

// uplink writes a hub's IPv4 packet to the TUN if its source is the hub's own
// tunnel address (no spoofing other hubs or the internet).
func (s *Server) uplink(hs *hubSession, pkt []byte) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 || netip.AddrFrom4([4]byte(pkt[12:16])) != hs.ip {
		s.log.Debug("uplink packet dropped (not IPv4 from hub address)", "hub", hs.hub.Name)
		return
	}
	if _, err := s.o.TUN.Write(pkt); err != nil {
		s.log.Debug("tun write", "err", err)
	}
}

// tunLoop sends packets read from the TUN to the hub owning their destination.
func (s *Server) tunLoop() {
	buf := make([]byte, 65535)
	fails := 0
	for {
		n, err := s.o.TUN.Read(buf)
		if err != nil {
			if s.closing.Load() {
				return
			}
			// A broken device returns at once on every read: do not spin.
			if fails++; fails == 1 || fails%100 == 0 {
				s.log.Warn("tun read", "err", err, "failures", fails)
			}
			time.Sleep(min(time.Duration(fails)*10*time.Millisecond, time.Second))
			continue
		}
		fails = 0
		pkt := buf[:n]
		if n < 20 || pkt[0]>>4 != 4 {
			continue
		}
		s.mu.Lock()
		hs := s.byIP[netip.AddrFrom4([4]byte(pkt[16:20]))]
		s.mu.Unlock()
		if hs == nil {
			continue
		}
		hs.sess.SendIP(pkt)
	}
}
