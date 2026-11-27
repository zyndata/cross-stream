package transport

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/zyndata/cross-stream/internal/wire"
)

// writer sends queued packets on a stream from its own goroutine, so a
// stalled connection drops packets (like a full UDP buffer) instead of
// blocking the caller.
type writer struct {
	q    chan []byte
	done chan struct{}
	once sync.Once
	err  error // set before done is closed
}

func newWriter(write func(pkt []byte) error) *writer {
	w := &writer{q: make(chan []byte, SendQueue), done: make(chan struct{})}
	go func() {
		for {
			select {
			case pkt := <-w.q:
				if err := write(pkt); err != nil {
					w.stop(err)
					return
				}
			case <-w.done:
				return
			}
		}
	}()
	return w
}

// send copies pkt into the queue.
func (w *writer) send(pkt []byte) error {
	if len(pkt) > MaxPacket {
		return fmt.Errorf("transport: packet of %d bytes", len(pkt))
	}
	select {
	case <-w.done:
		return w.err
	default:
	}
	select {
	case w.q <- append([]byte(nil), pkt...):
		return nil
	default:
		return ErrQueueFull
	}
}

// backlog is how many packets wait in the queue.
func (w *writer) backlog() int { return len(w.q) }

func (w *writer) stop(err error) {
	w.once.Do(func() {
		w.err = err
		close(w.done)
	})
}

// tcpConn frames packets as `len u16 | packet` on a TCP connection.
type tcpConn struct {
	c net.Conn
	w *writer
}

func newTCPConn(c net.Conn) *tcpConn {
	t := &tcpConn{c: c}
	t.w = newWriter(func(pkt []byte) error {
		b := make([]byte, 2, 2+len(pkt))
		binary.BigEndian.PutUint16(b, uint16(len(pkt)))
		_, err := c.Write(append(b, pkt...))
		return err
	})
	return t
}

func (t *tcpConn) Send(pkt []byte) error { return t.w.send(pkt) }

// Backlog implements Backlogger.
func (t *tcpConn) Backlog() int { return t.w.backlog() }

// Recv reads one framed packet; a packet larger than buf is an error.
func (t *tcpConn) Recv(buf []byte) (int, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(t.c, hdr[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n > len(buf) {
		return 0, fmt.Errorf("transport: packet of %d bytes, buffer %d", n, len(buf))
	}
	if _, err := io.ReadFull(t.c, buf[:n]); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, err
	}
	return n, nil
}

func (t *tcpConn) Close() error {
	t.w.stop(net.ErrClosed)
	return t.c.Close()
}

func (t *tcpConn) Transport() wire.Transport { return wire.TransportTCP }
