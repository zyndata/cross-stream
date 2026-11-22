package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/datacap"
	"github.com/zyndata/cross-stream/internal/pairing"
)

// Timers (protocol.md section 3).
const (
	StatsInterval     = time.Second
	KeepaliveInterval = 5 * time.Second
	WelcomeTimeout    = 10 * time.Second
	RetryFirst        = time.Second
	RetryMax          = 8 * time.Second
)

// ErrForgotten ends Run when the hub tells the relay to forget it; the caller
// must delete the hub.
var ErrForgotten = errors.New("relay: hub forgot this relay")

// RejectedError ends Run when the hub rejects the relay.
type RejectedError struct{ Reason string }

func (e *RejectedError) Error() string { return "relay: rejected by hub: " + e.Reason }

// StoppedError ends Run after Stop or a limit (cap, battery) stopped the relay.
type StoppedError struct{ Reason string }

func (e *StoppedError) Error() string { return "relay: stopped: " + e.Reason }

// DeviceInfo is what the platform reports for the stats message.
type DeviceInfo struct {
	Battery    int // percent
	SignalDBm  int
	NetType    string // lte, nr, ...
	CellularOK bool   // cellular network validated
	WifiRSSI   int
}

// Config configures a Relay.
type Config struct {
	Key     crypto.Keypair  // relay static key (kept on the phone)
	Hub     pairing.Payload // from the hub's XSR1 code
	Name    string
	Carrier string
	Limits  control.Limits
	Paired  bool              // the hub already accepted this relay: no pair token is sent
	Binder  Binder            // nil = NoBinder
	Device  func() DeviceInfo // nil = full battery, cellular OK
	// OnPaired is called after the first welcome so the caller can persist
	// that this hub accepted the relay.
	OnPaired func(control.Welcome)
	// ProxyPort is the DIRECT-mode proxy listener port (0 = control.ProxyPort,
	// -1 = any free port, tests).
	ProxyPort int
	// ListenIP is the local address of the relay's data and proxy sockets
	// (zero = all addresses, as on the phone). Tests use 127.0.0.1, so a
	// desktop firewall does not ask about every fresh test binary.
	ListenIP netip.Addr
	Log      *slog.Logger
	Now      func() time.Time
	// TCPConns > 0 fixes the number of TCP connections of a tcp:// server
	// endpoint, overriding the hub's choice (hidden test override,
	// xs-relaysim -tcp-conns).
	TCPConns int

	proxyIdle     time.Duration // tests: TCP and UDP proxy idle timeout
	allowLoopback bool          // tests: proxy destinations on 127.0.0.0/8
	endpointRetry time.Duration // tests: EndpointRetry
	// tests: name lookup for server endpoints (default net.DefaultResolver)
	lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Stats is a snapshot of the relay state.
type Stats struct {
	Connected  bool
	RelayID    string
	PathID     uint8
	ServerEP   string
	BytesUp    uint64 // to the server over cellular, since New
	BytesDown  uint64 // from the server over cellular, since New
	PeriodUsed uint64 // bytes counted against the cap in the current period
	Dropped    uint64 // packets dropped by filters or limits
	ProxyPort  int    // proxy role listening port, 0 while off
	ProxyFlows int    // open proxy flows (TCP + UDP)
}

// Relay is the relay core: control client, data forwarding, limits, stats.
type Relay struct {
	cfg    Config
	hubKey [crypto.KeyLen]byte
	meter  *datacap.Meter
	paired atomic.Bool

	up, down, dropped atomic.Uint64

	mu   sync.Mutex
	sess *link // current hub session, nil while connecting
	stop chan string
	kick chan struct{} // Reconnect: skip the retry wait
}

// New checks cfg and returns a relay; Run starts it.
func New(cfg Config) (*Relay, error) {
	hk, err := cfg.Hub.HubKey()
	if err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}
	if _, err := cfg.Hub.Encode(); err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}
	if cfg.Name == "" {
		return nil, errors.New("relay: name is required")
	}
	if err := cfg.Limits.Validate(); err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}
	if cfg.Binder == nil {
		cfg.Binder = NoBinder{}
	}
	if cfg.Device == nil {
		cfg.Device = func() DeviceInfo { return DeviceInfo{Battery: 100, CellularOK: true} }
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	r := &Relay{cfg: cfg, hubKey: hk, meter: datacap.New(cfg.Limits, cfg.Now), stop: make(chan string, 1), kick: make(chan struct{}, 1)}
	r.paired.Store(cfg.Paired)
	return r, nil
}

// Run connects to the hub and relays until ctx ends, Stop is called, a limit
// stops the relay, or the hub rejects or forgets it. A lost connection is
// retried with backoff 1, 2, 4, 8 s.
func (r *Relay) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wait := RetryFirst
	repaired := false
	for {
		start := r.cfg.Now()
		err := r.session(ctx)
		var rej *RejectedError
		var stopped *StoppedError
		switch {
		case errors.As(err, &rej) && !repaired && r.paired.Load() && r.cfg.Hub.Tok != "" &&
			(rej.Reason == control.RejectForgotten || rej.Reason == control.RejectBadToken):
			// The hub forgot this relay while it was away. The code it was
			// started with may be a fresh one: try once more with its token.
			repaired = true
			r.paired.Store(false)
			r.cfg.Log.Info("hub does not know this relay any more, pairing again", "reason", rej.Reason)
			continue
		case errors.As(err, &rej), errors.As(err, &stopped), errors.Is(err, ErrForgotten):
			return err
		case ctx.Err() != nil:
			return ctx.Err()
		}
		if r.cfg.Now().Sub(start) > 2*RetryMax {
			wait = RetryFirst // the session was up for a while: retry quickly
		}
		r.cfg.Log.Info("hub connection lost, retrying", "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case reason := <-r.stop:
			return &StoppedError{Reason: reason}
		case <-r.kick:
			wait = RetryFirst
			continue
		case <-time.After(wait):
		}
		wait = min(wait*2, RetryMax)
	}
}

// Stop ends Run: the hub is told why (leaving message) when connected.
// reason is one of the control.Leaving* values.
func (r *Relay) Stop(reason string) {
	select {
	case r.stop <- reason:
	default:
	}
}

// Reconnect drops the current hub session (if any) and retries at once. The
// platform calls it when a network handle changes: sockets stay bound to the
// network they were created on.
func (r *Relay) Reconnect() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
	r.mu.Lock()
	s := r.sess
	r.mu.Unlock()
	if s != nil {
		_ = s.ctl.Close()
	}
}

// SetLimits changes the limits live and tells the hub.
func (r *Relay) SetLimits(l control.Limits) error {
	if err := l.Validate(); err != nil {
		return err
	}
	r.meter.SetLimits(l)
	r.mu.Lock()
	s := r.sess
	r.mu.Unlock()
	if s != nil {
		return s.ctl.Send(&control.LimitsUpdate{Limits: l})
	}
	return nil
}

// Stats returns a snapshot of counters and the connection state.
func (r *Relay) Stats() Stats {
	used, _ := r.meter.State()
	st := Stats{BytesUp: r.up.Load(), BytesDown: r.down.Load(), PeriodUsed: used, Dropped: r.dropped.Load()}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sess; s != nil {
		st.Connected, st.RelayID, st.PathID = true, s.welcome.RelayID, s.welcome.PathID
		if u := s.up.Load(); u != nil {
			st.ServerEP = u.uri
		}
		if p := s.proxy.Load(); p != nil {
			st.ProxyPort, st.ProxyFlows = p.port(), p.flows()
		}
	}
	return st
}

// session runs one control connection and the data sockets that belong to it.
func (r *Relay) session(ctx context.Context) error {
	d := &net.Dialer{
		Timeout:         WelcomeTimeout,
		Control:         controlFunc(r.cfg.Binder, NetWiFi),
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: 5 * time.Second, Interval: 2 * time.Second, Count: 3},
	}
	ctl, err := control.Dial(ctx, d, r.cfg.Hub.ControlAddr().String(), r.cfg.Key, r.hubKey)
	if err != nil {
		return err
	}
	defer func() { _ = ctl.Close() }()
	// On cancel the hub is told first, then the channel closes: closing
	// alone looks like a dead relay to the hub (5 s timeout, failover count).
	stopCtl := context.AfterFunc(ctx, func() {
		_ = ctl.Send(&control.Leaving{Reason: control.LeavingUser})
		_ = ctl.Close()
	})
	defer stopCtl()

	w, err := r.hello(ctl)
	if err != nil {
		return err
	}
	l, err := r.openLink(ctx, ctl, w)
	if err != nil {
		return err
	}
	defer l.close()
	r.mu.Lock()
	r.sess = l
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.sess = nil
		r.mu.Unlock()
	}()
	r.cfg.Log.Info("relay connected", "hub", r.cfg.Hub.Hub, "relay_id", w.RelayID, "path", w.PathID, "server", w.ServerEP)
	return l.run(ctx)
}

// hello sends hello and waits for welcome or reject.
func (r *Relay) hello(ctl *control.Conn) (control.Welcome, error) {
	h := &control.Hello{RelayName: r.cfg.Name, Carrier: r.cfg.Carrier, Limits: r.meter.Limits()}
	if !r.paired.Load() {
		h.PairToken = r.cfg.Hub.Tok
	}
	if err := ctl.Send(h); err != nil {
		return control.Welcome{}, err
	}
	_ = ctl.SetReadDeadline(r.cfg.Now().Add(WelcomeTimeout))
	defer func() { _ = ctl.SetReadDeadline(time.Time{}) }()
	for {
		m, err := ctl.Recv()
		if err != nil {
			return control.Welcome{}, err
		}
		switch m := m.(type) {
		case *control.Welcome:
			if !r.paired.Swap(true) && r.cfg.OnPaired != nil {
				r.cfg.OnPaired(*m)
			}
			return *m, nil
		case *control.Reject:
			return control.Welcome{}, &RejectedError{Reason: m.Reason}
		case *control.Forget:
			return control.Welcome{}, ErrForgotten
		}
	}
}
