package direct

import (
	"context"
	"errors"
	"net"
	"os"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"

	"github.com/zyndata/cross-stream/internal/wire"
)

// udpWriter writes reply datagrams to the client.
type udpWriter interface {
	Write(b []byte) (int, error)
}

// handleUDP runs for the first datagram of each client UDP flow (DNS
// included): the flow is bound to a relay and its datagrams travel on the
// data channel as B4/B5.
func (s *Stack) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	k := FlowKey{Proto: protoUDP, Src: addrPort(id.RemoteAddress, id.RemotePort), Dst: addrPort(id.LocalAddress, id.LocalPort)}
	f, ok := s.add(k)
	if !ok {
		return false
	}
	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		s.remove(f)
		return false
	}
	conn := gonet.NewUDPConn(&wq, ep)
	s.mu.Lock()
	f.udp = conn
	s.mu.Unlock()
	if f.relay.Dial != nil {
		return s.localUDP(f, conn)
	}
	if !s.setAbort(f, func() { _ = conn.Close() }) {
		return true
	}
	go s.udpLoop(f, conn)
	return true
}

// localUDP runs a UDP flow on a local uplink: a socket of its own to the
// destination, replies straight back to the client.
func (s *Stack) localUDP(f *flow, conn *gonet.UDPConn) bool {
	ctx, cancel := context.WithTimeout(s.ctx, DialTimeout)
	remote, err := f.relay.Dial(ctx, "udp4", f.key.Dst.String())
	cancel()
	if err != nil {
		s.cfg.Log.Debug("direct udp: local dial", "uplink", f.relay.ID, "dst", f.key.Dst, "err", err)
		_ = conn.Close()
		s.remove(f)
		return true
	}
	if !s.setAbort(f, func() { _ = conn.Close(); _ = remote.Close() }) {
		_ = remote.Close()
		return true
	}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := remote.Read(buf)
			if err != nil {
				_ = conn.Close() // ends udpLoop
				return
			}
			f.last.Store(s.cfg.Now().UnixNano())
			if _, err := conn.Write(buf[:n]); err != nil {
				s.cfg.Log.Debug("direct udp: reply", "uplink", f.relay.ID, "err", err)
			}
		}
	}()
	go s.flowLoop(f, conn, 0, func(b []byte) error {
		_, err := remote.Write(b)
		return err
	}, func() { _ = remote.Close() })
	return true
}

// udpLoop sends the client's datagrams of one flow to its relay until the
// flow is idle for UDPIdle or reset.
func (s *Stack) udpLoop(f *flow, conn *gonet.UDPConn) {
	hdr, _ := wire.AppendRelay(nil, wire.RelayMsg{Kind: wire.RelayUDPOut, FlowID: f.id, Dst: f.key.Dst})
	s.flowLoop(f, conn, len(hdr), func(b []byte) error {
		copy(b, hdr)
		return s.cfg.SendUDP(f.relay.ID, b)
	}, nil)
}

// flowLoop reads the client's datagrams of one flow (after head free bytes
// in the buffer) and passes each to send until the flow is idle for UDPIdle
// or reset; done runs at the end.
func (s *Stack) flowLoop(f *flow, conn *gonet.UDPConn, head int, send func([]byte) error, done func()) {
	defer s.remove(f)
	defer func() {
		_ = conn.Close()
		if done != nil {
			done()
		}
	}()
	buf := make([]byte, head+65535)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(s.cfg.UDPIdle))
		n, err := conn.Read(buf[head:])
		if err != nil {
			var ne net.Error
			if (errors.As(err, &ne) && ne.Timeout()) || errors.Is(err, os.ErrDeadlineExceeded) {
				if s.cfg.Now().Sub(time.Unix(0, f.last.Load())) < s.cfg.UDPIdle {
					continue // replies keep the flow alive
				}
			}
			return
		}
		f.last.Store(s.cfg.Now().UnixNano())
		if err := send(buf[:head+n]); err != nil {
			s.cfg.Log.Debug("direct udp: send", "relay", f.relay.ID, "err", err)
		}
	}
}

// HandleUDP delivers a B5 reply from a relay to its flow's client. Replies
// for unknown flows, or from another relay than the flow's, are dropped.
func (s *Stack) HandleUDP(relayID string, flowID uint32, payload []byte) {
	s.mu.Lock()
	f := s.byID[flowID]
	var w udpWriter
	if f != nil && f.relay.ID == relayID {
		w = f.udp
	}
	s.mu.Unlock()
	if w == nil {
		return
	}
	f.last.Store(s.cfg.Now().UnixNano())
	if _, err := w.Write(payload); err != nil {
		s.cfg.Log.Debug("direct udp: reply", "flow", flowID, "err", err)
	}
}
