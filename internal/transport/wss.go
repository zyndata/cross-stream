package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/zyndata/cross-stream/internal/wire"
)

// wsConn carries one outer packet per binary WebSocket message.
type wsConn struct {
	ws     *websocket.Conn
	w      *writer
	ctx    context.Context // ends when the connection is closed
	cancel context.CancelFunc
	// deadline of Recv in Unix ns (0 = none)
	deadline atomic.Int64
}

func newWSConn(ws *websocket.Conn) *wsConn {
	ws.SetReadLimit(MaxPacket + 1)
	ctx, cancel := context.WithCancel(context.Background())
	c := &wsConn{ws: ws, ctx: ctx, cancel: cancel}
	c.w = newWriter(func(pkt []byte) error {
		wctx, wcancel := context.WithTimeout(ctx, 30*time.Second)
		defer wcancel()
		return ws.Write(wctx, websocket.MessageBinary, pkt)
	})
	return c
}

func (c *wsConn) Send(pkt []byte) error { return c.w.send(pkt) }

// Backlog implements Backlogger.
func (c *wsConn) Backlog() int { return c.w.backlog() }

// setReadDeadline bounds the following Recv calls (zero = no bound).
func (c *wsConn) setReadDeadline(t time.Time) error {
	c.deadline.Store(t.UnixNano())
	if t.IsZero() {
		c.deadline.Store(0)
	}
	return nil
}

func (c *wsConn) Recv(buf []byte) (int, error) {
	ctx := c.ctx
	if d := c.deadline.Load(); d != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.Unix(0, d))
		defer cancel()
	}
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return 0, err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		if len(data) > len(buf) {
			return 0, fmt.Errorf("transport: packet of %d bytes, buffer %d", len(data), len(buf))
		}
		return copy(buf, data), nil
	}
}

func (c *wsConn) Close() error {
	c.w.stop(net.ErrClosed)
	c.cancel()
	return c.ws.CloseNow()
}

func (c *wsConn) Transport() wire.Transport { return wire.TransportWSS }

// dialWSS opens a WebSocket over TLS to addr. The server certificate is not
// verified: Noise authenticates the server, TLS only makes the traffic look
// like HTTPS (architecture.md "Server transports").
func dialWSS(ctx context.Context, nd net.Dialer, addr netip.AddrPort, ep wire.Endpoint, timeout time.Duration) (Conn, error) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, err := nd.DialContext(ctx, "tcp4", addr.String())
			if err == nil {
				tuneTCP(c)
			}
			return c, err
		},
		TLSClientConfig: &tls.Config{ServerName: ep.Host, InsecureSkipVerify: true, //nolint:gosec // Noise authenticates the server
			MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: timeout,
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "https://"+ep.HostPort()+ep.Path, &websocket.DialOptions{HTTPClient: &http.Client{Transport: tr}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		tr.CloseIdleConnections()
		return nil, fmt.Errorf("transport: dial %s: %w", ep, err)
	}
	return newWSConn(ws), nil
}
