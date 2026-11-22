package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/session"
	"github.com/zyndata/cross-stream/internal/wire"
)

// ProbeAttempts is how many INITs a probe sends (waits 1+2+4 s).
const ProbeAttempts = 3

// Probe runs one handshake with the hub's enrolled identity and ends the new
// session at once with CLOSE normal: a health check that the server answers
// handshakes (deploy, phase 17). Token must be nil; Conn is closed on return.
func (c *Client) Probe(ctx context.Context, attempts int) (session.RespPayload, error) {
	if c.Token != nil {
		return session.RespPayload{}, errors.New("hub client: probe with an enrollment token")
	}
	if c.Conn == nil {
		return session.RespPayload{}, errors.New("hub client: probe needs Conn and Endpoints")
	}
	if err := c.start(); err != nil {
		return session.RespPayload{}, err
	}
	defer c.closeConn()
	resp, keys, r, err := c.handshake(ctx, attempts)
	if err != nil {
		return session.RespPayload{}, err
	}
	s := session.New(resp.SessionID, session.Options{Deliver: func([]byte) {}, MaxTunnelMTU: resp.MTU})
	defer s.Close()
	s.InstallKeys(keys, true)
	s.AddPath(r.h.PathID, r.from, r.via, true)
	if err := s.SendOn(r.h.PathID, &wire.Close{Reason: wire.CloseNormal}); err != nil {
		return resp, fmt.Errorf("probe close: %w", err)
	}
	return resp, nil
}

// ProbeResult is what ProbeServer reports.
type ProbeResult struct {
	Name     string
	Enrolled bool // the probe identity was enrolled by this call
	RTT      time.Duration
	TunnelIP string
}

// ProbeServer checks that the server answers a handshake, using the hub
// identity in dir, sending from interface via ("" = routing table). to,
// when not empty, replaces the enrolled endpoints (e.g. 127.0.0.1:51900 on
// the server host, where the public name may not loop back). With no
// enrollment in dir and a code, it enrolls first.
func ProbeServer(ctx context.Context, dir, via string, to []netip.AddrPort, code, version string, log *slog.Logger) (ProbeResult, error) {
	var res ProbeResult
	e, err := LoadEnrollment(dir)
	if errors.Is(err, os.ErrNotExist) && code != "" {
		conn, err := ListenUDP(via)
		if err != nil {
			return res, err
		}
		if e, err = enrollWithCode(ctx, dir, code, conn, to, version, log); err != nil {
			return res, err
		}
		res.Enrolled = true
	} else if err != nil {
		return res, err
	}
	key, err := LoadOrCreateKey(dir)
	if err != nil {
		return res, err
	}
	serverKey, err := crypto.ParseKey(e.ServerKey)
	if err != nil {
		return res, fmt.Errorf("enrollment server key: %w", err)
	}
	eps := to
	if len(eps) == 0 {
		if eps, err = ResolveEndpoints(e.Endpoints); err != nil {
			return res, err
		}
	}
	conn, err := ListenUDP(via)
	if err != nil {
		return res, err
	}
	c := &Client{Key: key, ServerKey: serverKey, Endpoints: eps, Conn: conn, Name: e.Name, Version: version, Log: log}
	start := time.Now()
	resp, err := c.Probe(ctx, ProbeAttempts)
	if err != nil {
		return res, fmt.Errorf("probe: %w", err)
	}
	res.Name, res.RTT, res.TunnelIP = e.Name, time.Since(start), resp.TunnelIP
	return res, nil
}
