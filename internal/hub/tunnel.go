package hub

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
	"github.com/zyndata/cross-stream/internal/tun"
)

// ErrRestart ends Run when the daemon asked to be started again (a new
// enrollment replaced server.json): the caller runs a fresh Daemon from the
// same config.
var ErrRestart = errors.New("xs-hubd restart requested")

// requestRestart ends Run after delay (the RPC that asked for it answers
// first) with ErrRestart.
func (d *Daemon) requestRestart(delay time.Duration) {
	d.restartReq.Store(true)
	d.mu.Lock()
	stop := d.stopRun
	d.mu.Unlock()
	if stop != nil {
		time.AfterFunc(delay, stop)
	}
}

// tunConfig is the TUN setup of an enrollment.
func (d *Daemon) tunConfig(e Enrollment) (tun.Config, error) {
	ip, err := netip.ParseAddr(e.TunnelIP)
	if err != nil {
		return tun.Config{}, fmt.Errorf("enrollment tunnel ip: %w", err)
	}
	var dns []netip.Addr // Windows: Mobile Hotspot hands its clients a DNS server only if xs0 has one
	for _, u := range d.cfg.DNSUpstreams {
		dns = append(dns, u.Addr())
	}
	return tun.Config{Name: d.cfg.TunName, MTU: e.MTU, Prefix: netip.PrefixFrom(ip, 32), DNS: dns}, nil
}

// startTunnel creates the TUN (or takes the one made before the hotspot),
// LAN routing and the session client.
func (d *Daemon) startTunnel(e Enrollment) error {
	serverKey, err := crypto.ParseKey(e.ServerKey)
	if err != nil {
		return fmt.Errorf("enrollment server key: %w", err)
	}
	tc, err := d.tunConfig(e)
	if err != nil {
		return err
	}
	ip := tc.Prefix.Addr()
	d.mu.Lock()
	dev := d.preTUN
	d.preTUN = nil
	d.mu.Unlock()
	if dev == nil {
		if dev, err = d.cfg.NewTUN(tc); err != nil {
			return err
		}
	}
	if d.cfg.HotspotNAT.IsValid() {
		dev = tun.NAT(dev, d.cfg.HotspotNAT, ip, nil)
	}
	if d.cfg.ClampMSS {
		d.mss.Store(int32(e.MTU - 40))
		dev = tun.ClampMSS(dev, &d.mss)
	}
	ds, err := d.newDirect(dev)
	if err != nil {
		_ = dev.Close()
		return err
	}
	d.mu.Lock()
	d.tunDev, d.dstack = dev, ds
	d.mu.Unlock()
	d.netTunnel(e)
	go d.reshareHotspot()
	c := &Client{Key: d.hubKey, ServerKey: serverKey, Name: e.Name, Version: d.cfg.Version, TUN: dev,
		ExternalTUN: true, Uplink: d.uplink, Log: d.log, OverflowMbps: uint16(d.overflow.Load()), PathName: d.pathName}
	if o, err := d.Settings().bondOpts(); err == nil {
		c.Bond = o
	}
	d.client.Store(c)
	d.pushForwards()
	d.setServerEP(firstEndpoint(e.Endpoints))
	d.mu.Lock()
	rcs := d.connected()
	d.mu.Unlock()
	for _, rc := range rcs {
		d.addPath(rc)
	}
	d.addLocalPaths(c)
	tctx, cancel := context.WithCancel(d.ctx)
	done := make(chan struct{})
	d.mu.Lock()
	d.tunnelStop, d.tunnelDone, d.tunnelErr = cancel, done, nil
	d.mu.Unlock()
	d.log.Info("tunnel interface up", "tun", d.cfg.TunName, "ip", ip, "lan", d.lan.Masked())
	go d.tunLoop(dev, ds)
	go func() {
		defer close(done)
		err := c.Run(tctx)
		d.client.CompareAndSwap(c, nil)
		if errors.Is(err, ErrRevoked) {
			d.log.Error("enrollment revoked by the server: enroll again with a new code")
		} else if err != nil && !errors.Is(err, context.Canceled) {
			d.log.Error("tunnel stopped", "err", err)
		}
		d.mu.Lock()
		d.tunnelErr = err
		d.mu.Unlock()
	}()
	return nil
}

func (d *Daemon) stopTunnel() {
	d.mu.Lock()
	stop, done := d.tunnelStop, d.tunnelDone
	d.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// How an enrollment reaches the server.
const (
	enrollViaPhone = "phone"      // at least one relay (local links may share the attempts)
	enrollViaLocal = "local link" // local uplinks only
	enrollViaOwn   = "own"        // the computer's own connection (nothing else connected)
)

// ErrNoAnswerHere is the enrollment error when the server did not answer
// through a local link or the computer's own connection: most often the
// server sits in the same home network behind a router without hairpin NAT.
var ErrNoAnswerHere = errors.New("no answer from the server through this computer's connection or local link " +
	"(a server in the same home network is often unreachable by its public address: no hairpin NAT on the router); " +
	"pair a phone and enroll through it, or check that the code is new")

// enrollError wraps an enrollment failure; no answer without a phone gets
// the likely cause.
func enrollError(err error, route string) error {
	if errors.Is(err, ErrNoResponse) && route != enrollViaPhone {
		return fmt.Errorf("enroll: %w", ErrNoAnswerHere)
	}
	return fmt.Errorf("enroll: %w", err)
}

// restartDelay lets an enroll RPC answer before a re-enrolled daemon restarts.
const restartDelay = 500 * time.Millisecond

// Enroll enrolls this hub with an XS1 code through a connected relay or
// local link (or, with neither, the computer's own connection), saves
// the result and brings the tunnel up. An enrolled hub enrolls again (new
// server or a revoked hub): the old session stops for the handshake, the new
// server.json replaces the old one only on success, and the daemon restarts
// either way (the TUN, routes and DIRECT state are rebuilt from server.json).
func (d *Daemon) Enroll(ctx context.Context, code string) (Enrollment, error) {
	c, err := enroll.ParseCode(code)
	if err != nil {
		return Enrollment{}, fmt.Errorf("enroll: %w", err)
	}
	ep := firstEndpoint(c.Endpoints)
	if ep == "" {
		return Enrollment{}, errors.New("enroll: the code has no udp:// endpoint")
	}
	d.mu.Lock()
	again := d.enr != nil
	if d.enrolling {
		d.mu.Unlock()
		return Enrollment{}, errors.New("enroll: an enrollment is already running")
	}
	route := enrollViaPhone
	switch {
	case len(d.relays) == 0 && len(d.locals) == 0:
		route = enrollViaOwn
	case len(d.relays) == 0:
		route = enrollViaLocal
	}
	d.enrolling = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.enrolling = false
		d.mu.Unlock()
	}()
	if again {
		d.log.Info("enrolling again: stopping the current session", "name", c.Name)
		d.stopTunnel()
		defer d.requestRestart(restartDelay)
	}
	cl := &Client{Key: d.hubKey, ServerKey: c.ServerKey, Name: c.Name, Version: d.cfg.Version, Token: &c.Token,
		Uplink: d.uplink, Log: d.log}
	if route == enrollViaOwn {
		// No phone and no local link: the computer's own connection (the
		// routing table; no TUN exists before the first enrollment).
		eps, err := ResolveEndpoints(c.Endpoints)
		if err != nil {
			return Enrollment{}, err
		}
		conn, err := ListenUDP("")
		if err != nil {
			return Enrollment{}, fmt.Errorf("enroll: %w", err)
		}
		cl.Conn, cl.Endpoints, cl.Uplink = conn, eps, nil
		d.log.Info("enrolling through this computer's own connection (no phone or local link connected)", "name", c.Name)
	} else {
		d.setServerEP(ep)
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if _, _, ok := d.uplink(); ok {
				break // a local path dials its new endpoint first
			}
		}
		d.client.Store(cl)
	}
	resp, err := cl.Enroll(ctx, EnrollAttempts)
	d.client.CompareAndSwap(cl, nil)
	if err != nil {
		return Enrollment{}, enrollError(err, route)
	}
	e := Enrollment{Name: c.Name, ServerKey: crypto.EncodeKey(c.ServerKey), Endpoints: c.Endpoints,
		TunnelIP: resp.TunnelIP, MTU: resp.MTU}
	if err := SaveEnrollment(d.cfg.Dir, e); err != nil {
		return Enrollment{}, err
	}
	d.log.Info("hub enrolled", "name", e.Name, "tunnel_ip", e.TunnelIP, "again", again)
	if again {
		return e, nil // the restart brings the new tunnel up
	}
	d.mu.Lock()
	d.enr = &e
	d.mu.Unlock()
	return e, d.startTunnel(e)
}
