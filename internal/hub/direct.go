package hub

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"math"
	"net/netip"
	"os"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/direct"
	"github.com/zyndata/cross-stream/internal/tun"
	"github.com/zyndata/cross-stream/internal/wire"
)

// Where packets read from the TUN go.
const (
	routeTunnel int32 = iota // to the session
	routeDirect              // to the netstack (DIRECT)
	routeDrain               // existing direct flows to the netstack, the rest to the session
)

// PeakHalfLife is how fast a relay's measured peak rate (its DIRECT weight)
// fades.
const PeakHalfLife = 10 * time.Minute

// DirectStatus is the DIRECT part of Status.
type DirectStatus struct {
	Proxy      bool      `json:"proxy"` // relays run the proxy role
	Draining   bool      `json:"draining"`
	DrainUntil time.Time `json:"drain_until,omitzero"`
	TCP        int       `json:"tcp"`
	UDP        int       `json:"udp"`
}

// newDirect creates the netstack for DIRECT mode on dev.
func (d *Daemon) newDirect(dev tun.Device) (*direct.Stack, error) {
	return direct.New(direct.Config{
		Relays:  d.directRelays,
		Token:   d.proxyToken,
		SendUDP: d.sendProxyUDP,
		Output: func(pkt []byte) {
			if _, err := dev.Write(pkt); err != nil {
				d.log.Debug("tun write (direct)", "err", err)
			}
		},
		Intercept: d.pepIntercept,
		Log:       d.log,
	})
}

// tunLoop reads the TUN and sends each packet to the session or the
// netstack, depending on the mode.
func (d *Daemon) tunLoop(dev tun.Device, ds *direct.Stack) {
	buf := make([]byte, 65535)
	for {
		n, err := dev.Read(buf)
		if err != nil {
			if d.tunClosing.Load() || errors.Is(err, tun.ErrClosed) || errors.Is(err, os.ErrClosed) {
				return
			}
			d.log.Warn("tun read", "err", err)
			continue
		}
		pkt := buf[:n]
		switch d.route.Load() {
		case routeDirect:
			ds.Inject(pkt)
			continue
		case routeDrain:
			if ds.Owns(pkt) {
				ds.Inject(pkt)
				continue
			}
		}
		if ds.OwnsIntercepted(pkt) || d.bypassPacket(ds, pkt) || d.pepPacket(pkt) {
			ds.Inject(pkt)
			continue
		}
		if c := d.client.Load(); c != nil {
			c.SendIP(pkt)
		}
	}
}

// directRelays lists the relays new direct flows may use: connected, with a
// known data address and not switched off by the owner.
func (d *Daemon) directRelays() []direct.Relay {
	d.mu.Lock()
	rcs := d.connected()
	d.mu.Unlock()
	settings := make(map[string]wire.PathConfig)
	for _, r := range d.reg.List() {
		settings[r.ID] = pathConfig(r)
	}
	out := make([]direct.Relay, 0, len(rcs))
	for _, rc := range rcs {
		pc, ok := settings[rc.rec.ID]
		if rc.data.Load() == nil || !ok || (pc.DirUp == wire.DirOff && pc.DirDown == wire.DirOff) {
			continue
		}
		backup := pc.Priority == wire.PriorityBackup || pc.DirUp == wire.DirBackupOnly || pc.DirDown == wire.DirBackupOnly
		out = append(out, direct.Relay{ID: rc.rec.ID, Proxy: netip.AddrPortFrom(rc.ip, uint16(d.cfg.ProxyPort)),
			Backup: backup, Weight: math.Float64frombits(rc.peak.Load())})
	}
	return append(out, d.directLocals()...)
}

func (d *Daemon) proxyToken() [wire.ProxyTokenLen]byte {
	if t := d.proxyTok.Load(); t != nil {
		return *t
	}
	return [wire.ProxyTokenLen]byte{}
}

// sendProxyUDP sends a B4 datagram to a relay's data socket.
func (d *Daemon) sendProxyUDP(relayID string, b []byte) error {
	d.mu.Lock()
	rc := d.relays[relayID]
	d.mu.Unlock()
	if rc == nil {
		return errors.New("hub: relay not connected")
	}
	to := rc.data.Load()
	if to == nil {
		return errNoRelayAddr
	}
	_, err := d.data.WriteToUDPAddrPort(b, *to)
	return err
}

// modeMsg is the mode message for the current state.
func (d *Daemon) modeMsg() *control.Mode {
	m := &control.Mode{Mode: control.ModeBonded}
	if d.route.Load() == routeDirect {
		m.Mode = control.ModeDirect
	}
	if t := d.proxyTok.Load(); t != nil {
		m.Proxy, m.ProxyToken = true, base64.StdEncoding.EncodeToString(t[:])
	}
	return m
}

func (d *Daemon) sendMode(rcs []*relayPath) {
	m := d.modeMsg()
	for _, rc := range rcs {
		if err := rc.ctl.Send(m); err != nil {
			d.log.Debug("send mode", "relay", rc.rec.ID, "err", err)
		}
	}
}

func (d *Daemon) sendModeAll() {
	d.mu.Lock()
	rcs := d.connected()
	d.mu.Unlock()
	d.sendMode(rcs)
}

// applyMode acts on a mode change: DIRECT turns the relays' proxy role on
// and routes the TUN to the netstack; leaving DIRECT for BONDED drains the
// open direct flows.
func (d *Daemon) applyMode(now time.Time, prev, next Mode) {
	ds, c := d.directStack(), d.client.Load()
	if ds == nil {
		return
	}
	switch {
	case next == ModeDirect:
		if d.proxyTok.Load() == nil {
			var t [wire.ProxyTokenLen]byte
			_, _ = rand.Read(t[:])
			d.proxyTok.Store(&t)
		}
		d.route.Store(routeDirect)
		if c != nil {
			c.SetProbeEvery(ProbeEvery)
		}
		d.sendModeAll()
	case prev == ModeDirect && next == ModeBonded:
		if c != nil {
			c.SetProbeEvery(0)
		}
		st := ds.Stats()
		if st.TCP+st.UDP == 0 {
			d.endProxy("no direct flows")
			return
		}
		d.mu.Lock()
		d.drainUntil = now.Add(DrainMax)
		d.mu.Unlock()
		d.route.Store(routeDrain)
		d.sendModeAll()
		d.log.Info("draining direct flows", "tcp", st.TCP, "udp", st.UDP, "max", DrainMax)
	case prev == ModeDirect:
		if c != nil {
			c.SetProbeEvery(0)
		}
		d.endProxy("left DIRECT for " + string(next))
	}
}

// drainTick ends the drain when no direct flow is left or DrainMax passed.
func (d *Daemon) drainTick(now time.Time) {
	ds := d.directStack()
	if ds == nil || d.route.Load() != routeDrain {
		return
	}
	d.mu.Lock()
	until := d.drainUntil
	d.mu.Unlock()
	st := ds.Stats()
	switch {
	case st.TCP+st.UDP == 0:
		d.endProxy("drain finished")
	case !now.Before(until):
		d.endProxy("drain time limit")
	}
}

// endProxy resets what is left of the direct flows, routes the TUN to the
// tunnel and turns the relays' proxy role off.
func (d *Daemon) endProxy(why string) {
	n := 0
	keep := d.bp.Active() // bypass flows go on, and so does the proxy role
	if ds := d.directStack(); ds != nil {
		if keep {
			n = ds.ResetExcept(d.bp.Contains)
		} else {
			n = ds.ResetAll()
		}
	}
	d.route.Store(routeTunnel)
	if keep {
		d.log.Info("direct flows ended, proxy role kept for the bypass list", "why", why, "reset", n)
		return
	}
	had := d.proxyTok.Swap(nil) != nil
	if had {
		d.sendModeAll()
	}
	d.log.Info("direct flows ended, proxy role off", "why", why, "reset", n)
}

func (d *Daemon) directStack() *direct.Stack {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dstack
}

// updatePeak folds one stats message into the relay's peak cellular rate.
func (rc *relayPath) updatePeak(now time.Time, m *control.Stats) {
	prev, at := rc.prevStats, rc.prevAt
	rc.prevStats, rc.prevAt = m, now
	dt := now.Sub(at).Seconds()
	if prev == nil || dt <= 0 {
		return
	}
	cur, old := m.BytesUp+m.BytesDown, prev.BytesUp+prev.BytesDown
	if cur < old {
		return // relay restarted its counters
	}
	rate := float64(cur-old) / dt
	peak := math.Float64frombits(rc.peak.Load()) * math.Exp2(-dt/PeakHalfLife.Seconds())
	rc.peak.Store(math.Float64bits(max(rate, peak)))
}

// directStatus returns the DIRECT part of the status and flows per relay.
func (d *Daemon) directStatus() (*DirectStatus, map[string]int) {
	ds := d.directStack()
	if ds == nil {
		return nil, nil
	}
	st := ds.Stats()
	r := d.route.Load()
	out := &DirectStatus{Proxy: d.proxyTok.Load() != nil, Draining: r == routeDrain, TCP: st.TCP, UDP: st.UDP}
	if out.Draining {
		d.mu.Lock()
		out.DrainUntil = d.drainUntil
		d.mu.Unlock()
	}
	return out, st.Relays
}
