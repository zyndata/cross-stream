package control

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/flynn/noise"

	"github.com/zyndata/cross-stream/internal/crypto"
)

// Channel constants (protocol.md section 3).
const (
	Port             = 51901            // hub control port (TCP)
	DataPort         = 51902            // hub data port (UDP)
	ProxyPort        = 51903            // relay proxy port (TCP, DIRECT mode)
	MaxFrame         = 65535            // largest Noise message
	HandshakeTimeout = 10 * time.Second // whole handshake must finish within this
	DeadAfter        = 5 * time.Second  // no message (stats) for this long = relay DEAD
)

// ErrFrame is wrapped by framing errors (bad length prefix).
var ErrFrame = errors.New("control: bad frame")

// Conn is one authenticated control connection: 4-byte big-endian length +
// Noise transport message per frame, each carrying one JSON message.
// Send and Recv may be used from different goroutines.
type Conn struct {
	c    net.Conn
	peer [crypto.KeyLen]byte

	wmu  sync.Mutex
	send *noise.CipherState
	rmu  sync.Mutex
	recv *noise.CipherState
	rbuf []byte
}

// Dial connects to the hub at addr (TCP) with d and runs the handshake as the
// relay (initiator) towards the hub key from the QR code.
func Dial(ctx context.Context, d *net.Dialer, addr string, local crypto.Keypair, hub [crypto.KeyLen]byte) (*Conn, error) {
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("control dial %s: %w", addr, err)
	}
	cc, err := Client(c, local, hub)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return cc, nil
}

// Client runs the relay (initiator) side of the handshake on c.
func Client(c net.Conn, local crypto.Keypair, hub [crypto.KeyLen]byte) (*Conn, error) {
	_ = c.SetDeadline(time.Now().Add(HandshakeTimeout))
	defer func() { _ = c.SetDeadline(time.Time{}) }()
	ini, err := crypto.NewInitiatorWith(local, hub, crypto.ControlPrologue)
	if err != nil {
		return nil, fmt.Errorf("control client: %w", err)
	}
	m1, err := ini.WriteInit(nil)
	if err != nil {
		return nil, fmt.Errorf("control client: %w", err)
	}
	if err := writeFrame(c, m1); err != nil {
		return nil, fmt.Errorf("control client: %w", err)
	}
	m2, err := readFrame(c, nil)
	if err != nil {
		return nil, fmt.Errorf("control client: %w", err)
	}
	_, keys, err := ini.ReadResp(m2)
	if err != nil {
		return nil, fmt.Errorf("control client: %w", err)
	}
	return &Conn{c: c, peer: hub, send: keys.Send, recv: keys.Recv}, nil
}

// Server runs the hub (responder) side of the handshake on c. The caller
// decides from Peer() and the hello message whether to accept the relay.
func Server(c net.Conn, local crypto.Keypair) (*Conn, error) {
	_ = c.SetDeadline(time.Now().Add(HandshakeTimeout))
	defer func() { _ = c.SetDeadline(time.Time{}) }()
	res, err := crypto.NewResponderWith(local, crypto.ControlPrologue)
	if err != nil {
		return nil, fmt.Errorf("control server: %w", err)
	}
	m1, err := readFrame(c, nil)
	if err != nil {
		return nil, fmt.Errorf("control server: %w", err)
	}
	_, peer, err := res.ReadInit(m1)
	if err != nil {
		return nil, fmt.Errorf("control server: %w", err)
	}
	m2, keys, err := res.WriteResp(nil)
	if err != nil {
		return nil, fmt.Errorf("control server: %w", err)
	}
	if err := writeFrame(c, m2); err != nil {
		return nil, fmt.Errorf("control server: %w", err)
	}
	return &Conn{c: c, peer: peer, send: keys.Send, recv: keys.Recv}, nil
}

// Peer returns the remote static key (relay key on the hub, hub key on the relay).
func (c *Conn) Peer() [crypto.KeyLen]byte { return c.peer }

// RemoteAddr returns the TCP peer address.
func (c *Conn) RemoteAddr() net.Addr { return c.c.RemoteAddr() }

// LocalAddr returns the local TCP address.
func (c *Conn) LocalAddr() net.Addr { return c.c.LocalAddr() }

// SetReadDeadline bounds the next Recv.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.c.SetReadDeadline(t) }

// Close closes the connection.
func (c *Conn) Close() error { return c.c.Close() }

// Send encrypts and writes one message.
func (c *Conn) Send(m Message) error {
	b, err := Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	ct, err := c.send.Encrypt(nil, nil, b)
	if err != nil {
		return fmt.Errorf("control send %s: %w", m.Type(), err)
	}
	if err := writeFrame(c.c, ct); err != nil {
		return fmt.Errorf("control send %s: %w", m.Type(), err)
	}
	return nil
}

// Recv reads, decrypts and decodes one message. A decryption error is fatal
// for the connection (the Noise nonce is out of step).
func (c *Conn) Recv() (Message, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	ct, err := readFrame(c.c, c.rbuf)
	if err != nil {
		return nil, fmt.Errorf("control recv: %w", err)
	}
	c.rbuf = ct[:0]
	pt, err := c.recv.Decrypt(nil, nil, ct)
	if err != nil {
		return nil, fmt.Errorf("control recv: %w: %w", crypto.ErrHandshake, err)
	}
	return Unmarshal(pt)
}

func writeFrame(w io.Writer, msg []byte) error {
	if len(msg) == 0 || len(msg) > MaxFrame {
		return fmt.Errorf("frame of %d bytes: %w", len(msg), ErrFrame)
	}
	buf := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(msg)), uint32(len(msg)))
	_, err := w.Write(append(buf, msg...))
	return err
}

// readFrame reads one frame, reusing buf when it is large enough.
func readFrame(r io.Reader, buf []byte) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > MaxFrame {
		return nil, fmt.Errorf("frame length %d: %w", n, ErrFrame)
	}
	if cap(buf) < int(n) {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
