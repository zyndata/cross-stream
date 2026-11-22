package relay

import (
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// udpFlow is one proxied UDP flow: a connected cellular socket per flow id.
type udpFlow struct {
	id   uint32
	dst  netip.AddrPort
	conn *net.UDPConn
	last atomic.Int64 // unix nanoseconds of the last datagram either way
}

// udpOut sends one B4 datagram from the hub to its destination, opening the
// flow's cellular socket on first use.
func (p *proxy) udpOut(m wire.RelayMsg) {
	f := p.udpFlow(m.FlowID, m.Dst)
	if f == nil || !p.l.r.meter.Allow(len(m.Payload)) {
		p.l.r.dropped.Add(1)
		return
	}
	if _, err := f.conn.Write(m.Payload); err != nil {
		p.l.r.cfg.Log.Debug("proxy udp out", "flow", m.FlowID, "err", err)
		return
	}
	p.l.r.up.Add(uint64(len(m.Payload)))
	f.last.Store(time.Now().UnixNano())
}

// udpFlow returns the flow for id, (re)opening it for dst; nil when refused.
func (p *proxy) udpFlow(id uint32, dst netip.AddrPort) *udpFlow {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.udp[id]
	if f != nil && f.dst == dst {
		return f
	}
	if f != nil {
		_ = f.conn.Close()
		delete(p.udp, id)
	}
	if p.closed || len(p.udp) >= ProxyMaxUDPFlows || !p.allowed(dst) {
		return nil
	}
	d := net.Dialer{Control: bindFunc(p.l.r.cfg.Binder, NetCellular, false)}
	c, err := d.DialContext(p.ctx, "udp4", dst.String())
	if err != nil {
		p.l.r.cfg.Log.Debug("proxy udp socket", "dst", dst, "err", err)
		return nil
	}
	f = &udpFlow{id: id, dst: dst, conn: c.(*net.UDPConn)}
	f.last.Store(time.Now().UnixNano())
	p.udp[id] = f
	p.wg.Add(1)
	go p.udpIn(f)
	return f
}

// udpIn sends the flow's replies to the hub as [B5][flow_id][payload].
func (p *proxy) udpIn(f *udpFlow) {
	defer p.wg.Done()
	buf := make([]byte, 65535)
	hdr, _ := wire.AppendRelay(buf[:0], wire.RelayMsg{Kind: wire.RelayUDPIn, FlowID: f.id, Payload: []byte{}})
	h := len(hdr)
	for {
		n, err := f.conn.Read(buf[h:])
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(readErrPause) // e.g. ICMP port unreachable; a dead network repeats without blocking
			continue
		}
		if !p.l.r.meter.Allow(n) {
			p.l.r.dropped.Add(1)
			continue
		}
		if _, err := p.l.wifi.WriteToUDPAddrPort(buf[:h+n], p.l.hub); err != nil {
			p.l.r.cfg.Log.Debug("proxy udp to hub", "err", err)
			continue
		}
		p.l.r.down.Add(uint64(n))
		f.last.Store(time.Now().UnixNano())
	}
}

// sweepLoop closes idle UDP flows.
func (p *proxy) sweepLoop() {
	defer p.wg.Done()
	t := time.NewTicker(min(p.udpIdle/4, 10*time.Second))
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case now := <-t.C:
			p.mu.Lock()
			for id, f := range p.udp {
				if now.Sub(time.Unix(0, f.last.Load())) >= p.udpIdle {
					_ = f.conn.Close()
					delete(p.udp, id)
				}
			}
			p.mu.Unlock()
		}
	}
}
