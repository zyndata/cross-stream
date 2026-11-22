package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
)

// EnrollAttempts is how many INITs enrollment sends (waits 1+2+4+8 s: one
// lost answer on a phone path must not fail it; xs-hubctl waits 30 s).
const EnrollAttempts = 4

// EnrollWithCode enrolls this hub with an XS1 code: it loads or creates the
// hub key in dir, runs the handshake with the code's token over conn and
// saves the result. conn is closed on return.
func EnrollWithCode(ctx context.Context, dir, code string, conn *net.UDPConn, version string, log *slog.Logger) (Enrollment, error) {
	return enrollWithCode(ctx, dir, code, conn, nil, version, log)
}

// enrollWithCode is EnrollWithCode sending to "to" instead of the code's
// endpoints when it is not empty (the saved enrollment keeps the code's).
func enrollWithCode(ctx context.Context, dir, code string, conn *net.UDPConn, to []netip.AddrPort, version string, log *slog.Logger) (Enrollment, error) {
	c, err := enroll.ParseCode(code)
	if err != nil {
		_ = conn.Close()
		return Enrollment{}, fmt.Errorf("enroll: %w", err)
	}
	eps := to
	if len(eps) == 0 {
		if eps, err = ResolveEndpoints(c.Endpoints); err != nil {
			_ = conn.Close()
			return Enrollment{}, err
		}
	}
	key, err := LoadOrCreateKey(dir)
	if err != nil {
		_ = conn.Close()
		return Enrollment{}, err
	}
	cl := &Client{Key: key, ServerKey: c.ServerKey, Endpoints: eps, Conn: conn, Name: c.Name,
		Version: version, Token: &c.Token, Log: log}
	resp, err := cl.Enroll(ctx, EnrollAttempts)
	if err != nil {
		return Enrollment{}, fmt.Errorf("enroll: %w", err)
	}
	e := Enrollment{Name: c.Name, ServerKey: crypto.EncodeKey(c.ServerKey), Endpoints: c.Endpoints,
		TunnelIP: resp.TunnelIP, MTU: resp.MTU}
	if err := SaveEnrollment(dir, e); err != nil {
		return Enrollment{}, err
	}
	return e, nil
}

// ResolveEndpoints returns the addresses of the udp:// endpoints (other
// transports arrive in phase 10).
func ResolveEndpoints(eps []string) ([]netip.AddrPort, error) {
	var out []netip.AddrPort
	for _, ep := range eps {
		u, err := url.Parse(ep)
		if err != nil || u.Scheme != "udp" {
			continue
		}
		ua, err := net.ResolveUDPAddr("udp4", u.Host)
		if err != nil {
			return nil, fmt.Errorf("resolve endpoint %s: %w", ep, err)
		}
		ap := ua.AddrPort()
		out = append(out, netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()))
	}
	if len(out) == 0 {
		return nil, errors.New("no usable udp:// endpoint")
	}
	return out, nil
}
