package direct

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/zyndata/cross-stream/internal/wire"
)

// ProxyError is a relay's refusal of a proxy connection.
type ProxyError struct{ Code byte }

func (e *ProxyError) Error() string {
	return fmt.Sprintf("direct: relay proxy refused (code %#02x)", e.Code)
}

func addrPort(a tcpip.Address, port uint16) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4(a.As4()), port)
}

// handleTCP runs for each client SYN: it opens the proxy connection first,
// so the client sees a reset when no relay can reach the destination.
func (s *Stack) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	k := FlowKey{Proto: protoTCP, Src: addrPort(id.RemoteAddress, id.RemotePort), Dst: addrPort(id.LocalAddress, id.LocalPort)}
	f, ok := s.add(k)
	if !ok {
		r.Complete(true)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, DialTimeout)
	var remote net.Conn
	var err error
	if f.relay.Dial != nil {
		remote, err = f.relay.Dial(ctx, "tcp4", k.Dst.String())
	} else {
		remote, err = DialProxy(ctx, s.cfg.Dial, f.relay.Proxy, s.cfg.Token(), k.Dst)
	}
	cancel()
	if err != nil {
		s.cfg.Log.Debug("direct tcp: proxy dial", "relay", f.relay.ID, "dst", k.Dst, "err", err)
		s.remove(f)
		r.Complete(true)
		return
	}
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		s.remove(f)
		_ = remote.Close()
		r.Complete(true)
		return
	}
	r.Complete(false)
	local := gonet.NewTCPConn(&wq, ep)
	if !s.setAbort(f, func() { ep.Abort(); _ = remote.Close() }) {
		return
	}
	defer s.remove(f)
	splice(local, remote)
}

// DialProxy opens a proxy connection to a relay for dst and checks the
// relay's reply (protocol.md section 3, "Proxy role").
func DialProxy(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error),
	relay netip.AddrPort, token [wire.ProxyTokenLen]byte, dst netip.AddrPort) (net.Conn, error) {
	req, err := wire.AppendProxyRequest(nil, wire.ProxyRequest{Token: token, Dst: dst})
	if err != nil {
		return nil, fmt.Errorf("direct: %w", err)
	}
	c, err := dial(ctx, "tcp4", relay.String())
	if err != nil {
		return nil, fmt.Errorf("direct: dial relay %s: %w", relay, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = c.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	code := []byte{wire.ProxyFailure}
	if _, err = c.Write(req); err == nil {
		_, err = io.ReadFull(c, code)
	}
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("direct: relay %s: %w", relay, err)
	}
	if code[0] != wire.ProxyOK {
		_ = c.Close()
		return nil, &ProxyError{Code: code[0]}
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

// halfCloser is a stream whose write side can be closed alone.
type halfCloser interface {
	net.Conn
	CloseWrite() error
}

// splice copies both ways; EOF on one side half-closes the other. It returns
// when both directions ended or one failed, with both connections closed.
func splice(a, b net.Conn) {
	errc := make(chan error, 2)
	cp := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if err == nil {
			if hc, ok := dst.(halfCloser); ok {
				err = hc.CloseWrite()
			} else {
				err = errors.New("no half-close")
			}
		}
		errc <- err
	}
	go cp(a, b)
	go cp(b, a)
	for range 2 {
		if err := <-errc; err != nil {
			_ = a.Close()
			_ = b.Close()
		}
	}
	_ = a.Close()
	_ = b.Close()
}
