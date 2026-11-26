package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
	"github.com/zyndata/cross-stream/internal/pep"
	"github.com/zyndata/cross-stream/internal/session"
	"github.com/zyndata/cross-stream/internal/tun"
	"github.com/zyndata/cross-stream/internal/wire"
)

// Client errors.
var (
	ErrRevoked    = errors.New("hub: enrollment revoked by the server")
	ErrNoResponse = errors.New("hub: no handshake response (server down, or code used, expired or revoked)")
)

// Uplink returns the path to send a handshake on: its path id and a sender
// that delivers outer packets to the server (e.g. through a relay).
type Uplink func() (pathID uint8, via session.Sender, ok bool)

// Client is a single-path tunnel client. It reaches the server either with
// its own UDP socket (Conn + Endpoints, xs-hubctl test mode, path 0) or
// through relays (Uplink; the caller feeds received packets to HandlePacket).
type Client struct {
	Key       crypto.Keypair
	ServerKey [crypto.KeyLen]byte
	Endpoints []netip.AddrPort // server UDP endpoints for Conn, tried in turn
	Conn      *net.UDPConn     // owned by the client, closed when Enroll/Run return
	Uplink    Uplink           // used when Conn is nil
	Name      string
	Version   string
	Token     *enroll.Token // set to enroll (Enroll, or Run of a pending enrollment)
	TUN       tun.Device    // nil for Enroll; owned and closed by Run
	// OnEnrolled is called by Run once, when the first handshake that
	// carried Token succeeds (the pending enrollment is confirmed); Run then
	// drops Token.
	OnEnrolled func(session.RespPayload)
	Log        *slog.Logger
	// OverflowMbps: secondary paths stay unused while normal paths send at
	// least this much (0 = off); sent to the server in SESSION_CONFIG.
	OverflowMbps uint16
	// Bond: redundancy, mode and aggregation until SetBondOpts.
	Bond BondOpts
	// ExternalTUN: the caller reads TUN and passes packets to SendIP; Run
	// only writes to TUN and does not close it.
	ExternalTUN bool
	// PathName returns a path's display name (relay or local link name),
	// sent to the server in PATH_NAME for its web panel; nil = none.
	PathName func(id uint8) string

	// overflow overrides OverflowMbps once SetOverflow ran (overflowSet).
	overflow    atomic.Uint32
	overflowSet atomic.Bool
	bondOpts    atomic.Pointer[BondOpts] // overrides Bond once SetBondOpts ran
	tests       clientTests
	fwd         clientFwd
	once        sync.Once
	resps       chan respMsg
	closed      chan uint8
	sess        atomic.Pointer[session.Session]
	pep         atomic.Pointer[pep.Session] // nil: the server does not accept PEP streams
	stopping    atomic.Bool
	lastRecv    atomic.Int64 // unix nanoseconds of the last valid packet from the server
	probe       atomic.Int64 // handshake retry interval override (DIRECT), 0 = backoff

	pmu   sync.Mutex
	paths map[uint8]*clientPath // relay paths (daemon mode), kept across sessions
	used  map[uint8]uint64      // bytes counted against each path's cap
	obs   map[uint8]netip.AddrPort
}

type respMsg struct {
	h    wire.Header
	msg  []byte
	from netip.AddrPort
	via  session.Sender
}

func (c *Client) init() {
	c.once.Do(func() {
		if c.Log == nil {
			c.Log = slog.Default()
		}
		c.resps = make(chan respMsg, 4)
		c.closed = make(chan uint8, 1)
		c.paths = make(map[uint8]*clientPath)
		c.used = make(map[uint8]uint64)
		c.obs = make(map[uint8]netip.AddrPort)
	})
}

func (c *Client) start() error {
	if (c.Conn == nil || len(c.Endpoints) == 0) && c.Uplink == nil {
		return errors.New("hub client: Conn and Endpoints, or Uplink, are required")
	}
	c.init()
	if c.Conn != nil {
		go c.recvLoop()
	}
	return nil
}

func (c *Client) closeConn() {
	if c.Conn != nil {
		_ = c.Conn.Close()
	}
}

// uplink returns the path for the next handshake attempt: the session's
// best path when it is a known relay path, else the Uplink choice.
func (c *Client) uplink() (uint8, session.Sender, bool) {
	if c.Conn != nil {
		return 0, c.Conn, true
	}
	if s := c.sess.Load(); s != nil {
		if id, ok := s.Bond().BestPath(); ok {
			if st, _ := s.Bond().PathState(id); st == bond.Alive {
				c.pmu.Lock()
				p := c.paths[id]
				c.pmu.Unlock()
				if p != nil {
					return id, p.via, true
				}
			}
		}
	}
	return c.Uplink()
}

// Up reports whether a session with the server is established.
func (c *Client) Up() bool { return c.sess.Load() != nil }

// LastRecv returns when the last valid packet from the server arrived (zero
// if none yet).
func (c *Client) LastRecv() time.Time {
	if n := c.lastRecv.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

// SetProbeEvery makes handshake attempts at least d apart (DIRECT mode
// probes the server); 0 restores the normal backoff.
func (c *Client) SetProbeEvery(d time.Duration) { c.probe.Store(int64(d)) }

// SendIP sends one IP packet from the TUN through the session (ExternalTUN).
func (c *Client) SendIP(pkt []byte) {
	if s := c.sess.Load(); s != nil {
		s.SendIP(pkt)
	}
}

// Enroll runs the enrollment handshake (Token must be set) with at most
// attempts INIT packets and returns the server's response.
func (c *Client) Enroll(ctx context.Context, attempts int) (session.RespPayload, error) {
	if c.Token == nil {
		return session.RespPayload{}, errors.New("hub client: no enrollment token")
	}
	if err := c.start(); err != nil {
		return session.RespPayload{}, err
	}
	defer c.closeConn()
	resp, _, _, err := c.handshake(ctx, attempts)
	return resp, err
}

// Run keeps a session up until ctx is done or the hub is revoked: handshake
// with retry, keepalive PINGs, rekey, reconnect when the server goes silent.
func (c *Client) Run(ctx context.Context) error {
	if c.TUN == nil {
		return errors.New("hub client: TUN is required")
	}
	if err := c.start(); err != nil {
		return err
	}
	defer func() {
		c.stopping.Store(true)
		c.setPEP(nil)
		c.closeConn()
		if !c.ExternalTUN {
			_ = c.TUN.Close()
		}
	}()
	if !c.ExternalTUN {
		go c.tunLoop()
	}
	for {
		resp, keys, r, err := c.handshake(ctx, 0)
		if err != nil {
			return err
		}
		if c.Token != nil { // Token is only read by handshake, in this goroutine
			c.Token = nil
			if c.OnEnrolled != nil {
				c.OnEnrolled(resp)
			}
		}
		c.install(resp, keys, r)
		if err := c.maintain(ctx); err != nil {
			return err
		}
	}
}

// handshake sends INITs with backoff until a valid RESP arrives (attempts 0 =
// no limit). Each attempt goes to the next endpoint (Conn) or the current
// uplink; with no uplink available the attempt is only waited out.
func (c *Client) handshake(ctx context.Context, attempts int) (session.RespPayload, crypto.Keys, respMsg, error) {
	init := session.InitPayload{V: session.ProtocolVersion, SW: c.Version, HubName: c.Name, Paths: []int{0}}
	if c.Token != nil {
		s := c.Token.String()
		init.EnrollToken = &s
	}
	d := &session.Dialer{Local: c.Key, Server: c.ServerKey, Payload: init}
	for attempts == 0 || d.Attempts() < attempts {
		pid, via, ok := c.uplink()
		init.Paths = []int{int(pid)}
		d.Payload = init
		pkt, err := d.Begin(pid)
		if err != nil {
			return session.RespPayload{}, crypto.Keys{}, respMsg{}, err
		}
		var to netip.AddrPort
		if len(c.Endpoints) > 0 {
			to = c.Endpoints[(d.Attempts()-1)%len(c.Endpoints)]
		}
		if !ok {
			c.Log.Debug("no uplink for the handshake yet")
		} else if _, err := via.WriteToUDPAddrPort(pkt, to); err != nil {
			c.Log.Warn("send handshake", "to", to, "path", pid, "err", err)
		}
		wait := d.Wait()
		if p := time.Duration(c.probe.Load()); p > wait {
			wait = p
		}
		timer := time.NewTimer(wait)
	wait:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return session.RespPayload{}, crypto.Keys{}, respMsg{}, ctx.Err()
			case <-timer.C:
				break wait
			case r := <-c.resps:
				resp, keys, err := d.Finish(r.h, r.msg)
				if err != nil {
					c.Log.Debug("handshake response ignored", "err", err)
					continue
				}
				timer.Stop()
				return resp, keys, r, nil
			}
		}
		c.Log.Info("no handshake response, retrying", "to", to, "path", pid, "attempt", d.Attempts())
	}
	return session.RespPayload{}, crypto.Keys{}, respMsg{}, ErrNoResponse
}

// install starts a new session or rekeys the current one, records the path
// the response came on (it may be a different relay than before), then sends
// a PING so the server confirms the new keys and learns the path.
func (c *Client) install(resp session.RespPayload, keys crypto.Keys, r respMsg) {
	s := c.sess.Load()
	if s == nil || s.ID() != resp.SessionID {
		if s != nil {
			s.Close()
		}
		s = session.New(resp.SessionID, session.Options{Deliver: c.deliver, MaxTunnelMTU: resp.MTU,
			OnState: func(id uint8, st bond.State) { c.Log.Info("path state", "path", id, "state", st) }})
		s.InstallKeys(keys, true)
		s.Bond().SetOverflow(uint64(c.overflowMbps()) * 1_000_000 / 8)
		c.applyBondOpts(s)
		c.loadPaths(s)
		s.AddPath(r.h.PathID, r.from, r.via, true)
		s.Start()
		select { // a CLOSE of the previous session, queued during the handshake
		case <-c.closed:
		default:
		}
		c.sess.Store(s)
		var ps *pep.Session
		if resp.PEP {
			ps = pep.New(pep.Config{Link: s.Bond(), Log: c.Log})
		}
		c.setPEP(ps)
		c.Log.Info("session up", "session", resp.SessionID, "tunnel_ip", resp.TunnelIP, "mtu", resp.MTU,
			"server", r.from, "path", r.h.PathID, "pep", resp.PEP)
	} else {
		s.InstallKeys(keys, true)
		s.AddPath(r.h.PathID, r.from, r.via, true)
		c.Log.Debug("session rekeyed", "session", resp.SessionID, "path", r.h.PathID)
	}
	c.lastRecv.Store(time.Now().UnixNano())
	c.ping(s)
	c.sendConfig(s, true)
	c.resetForwards()
	c.sendForwards(s, time.Now())
}

// PEP returns the stream session of the current server session, or nil
// when the server does not accept PEP streams.
func (c *Client) PEP() *pep.Session { return c.pep.Load() }

// setPEP replaces the stream session; the old one resets its streams.
func (c *Client) setPEP(ps *pep.Session) {
	if old := c.pep.Swap(ps); old != nil {
		old.Close()
	}
}

// deliver writes a packet from the server to the TUN.
func (c *Client) deliver(pkt []byte) {
	if c.TUN == nil {
		return
	}
	if _, err := c.TUN.Write(pkt); err != nil {
		c.Log.Debug("tun write", "err", err)
	}
}

func (c *Client) ping(s *session.Session) {
	if err := s.Send(&wire.Ping{TS: uint64(time.Now().UnixMicro())}); err != nil {
		c.Log.Debug("ping", "err", err)
	}
}

// maintain watches the session. It returns nil when a new handshake is due
// (rekey, server silent or closed the session), ErrRevoked, or ctx's error.
func (c *Client) maintain(ctx context.Context) error {
	s := c.sess.Load()
	start := time.Now()
	lastPing, lastConfig, lastTick := start, start, start
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.Send(&wire.Close{Reason: wire.CloseNormal})
			s.Close()
			return ctx.Err()
		case reason := <-c.closed:
			c.sess.Store(nil)
			c.setPEP(nil) // no new TCP flows into streams of a dead session
			s.Close()
			if reason == wire.CloseRevoked {
				return ErrRevoked
			}
			c.Log.Info("server closed the session, reconnecting", "reason", reason)
			return nil
		case now := <-t.C:
			if gap := tickGap(lastTick, now); gap >= WakeGap {
				c.Log.Info("woke after a pause, reconnecting", "gap", gap.Round(time.Second))
				return nil
			}
			lastTick = now
			last := s.Stats().LastRecv
			if last.Before(start) {
				last = start
			}
			switch {
			case now.Sub(last) >= session.DeadAfter:
				c.Log.Warn("server silent, reconnecting", "silent", now.Sub(last).Round(time.Second))
				return nil
			case s.NeedsRekey():
				return nil
			case now.Sub(lastPing) >= session.KeepaliveInterval:
				c.ping(s)
				lastPing = now
			}
			full := now.Sub(lastConfig) >= bond.ConfigEvery
			if full {
				lastConfig = now
			}
			c.sendConfig(s, full)
			c.sendForwards(s, now)
		}
	}
}

func (c *Client) recvLoop() {
	buf := make([]byte, 65535)
	for {
		n, from, err := c.Conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		c.HandlePacket(buf[:n], netip.AddrPortFrom(from.Addr().Unmap(), from.Port()), c.Conn)
	}
}

// HandlePacket processes one outer packet from the server that arrived from
// `from` over via (the sender replies on that path go back through). It does
// not keep pkt.
func (c *Client) HandlePacket(pkt []byte, from netip.AddrPort, via session.Sender) {
	c.init()
	h, payload, err := wire.ParseHeader(pkt)
	if err != nil {
		return
	}
	switch h.Type {
	case wire.TypeHandshakeResp:
		select {
		case c.resps <- respMsg{h: h, msg: append([]byte(nil), payload...), from: from, via: via}:
		default:
		}
	case wire.TypeData:
		c.handleData(h, pkt, from, via)
	}
}

func (c *Client) handleData(h wire.Header, pkt []byte, from netip.AddrPort, via session.Sender) {
	s := c.sess.Load()
	if s == nil || h.SessionID != s.ID() {
		return
	}
	frames, err := s.Open(h, pkt, from, via)
	if err != nil {
		c.Log.Debug("data rejected", "err", err)
		return
	}
	c.lastRecv.Store(time.Now().UnixNano())
	for _, f := range frames {
		switch f := f.(type) {
		case *wire.PathInfo:
			c.pmu.Lock()
			if c.obs[f.PathID] != f.Observed {
				c.Log.Info("path public address", "path", f.PathID, "observed", f.Observed)
			}
			c.obs[f.PathID] = f.Observed
			c.pmu.Unlock()
		case *wire.TestData:
			c.tests.counter.Add(f.TestID, h.PathID, len(pkt), time.Now())
		case *wire.TestResult:
			c.tests.result(f)
		case *wire.FwdAck:
			c.onFwdAck(f)
		case *wire.StreamOpen, *wire.StreamOpenResult, *wire.StreamData, *wire.StreamWindow,
			*wire.StreamClose, *wire.StreamReset:
			if ps := c.pep.Load(); ps != nil {
				ps.Handle(f)
			}
		case *wire.Close:
			select {
			case c.closed <- f.Reason:
			default:
			}
		}
	}
}

func (c *Client) tunLoop() {
	buf := make([]byte, 65535)
	for {
		n, err := c.TUN.Read(buf)
		if err != nil {
			if c.stopping.Load() || errors.Is(err, tun.ErrClosed) || errors.Is(err, os.ErrClosed) {
				return
			}
			c.Log.Warn("tun read", "err", err)
			continue
		}
		if s := c.sess.Load(); s != nil {
			s.SendIP(buf[:n])
		}
	}
}

// ListenUDP opens the hub's UDP socket. With via set, it binds to that
// interface's first IPv4 address, so traffic leaves through it.
func ListenUDP(via string) (*net.UDPConn, error) {
	local := &net.UDPAddr{}
	if via != "" {
		ifc, err := net.InterfaceByName(via)
		if err != nil {
			return nil, fmt.Errorf("interface %s: %w", via, err)
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return nil, fmt.Errorf("interface %s: %w", via, err)
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				local.IP = ipn.IP.To4()
				break
			}
		}
		if local.IP == nil {
			return nil, fmt.Errorf("interface %s has no IPv4 address", via)
		}
	}
	pc, err := netcfg.ListenConfig().ListenPacket(context.Background(), "udp4", local.String())
	if err != nil {
		return nil, fmt.Errorf("listen udp: %w", err)
	}
	return pc.(*net.UDPConn), nil
}
