package hub

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zyndata/cross-stream/internal/bypass"
	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/direct"
	"github.com/zyndata/cross-stream/internal/hub/hublog"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
	"github.com/zyndata/cross-stream/internal/hub/usage"
	"github.com/zyndata/cross-stream/internal/ipc"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/tun"
	"github.com/zyndata/cross-stream/internal/wire"
)

// DaemonConfig configures xs-hubd.
type DaemonConfig struct {
	Dir      string // state directory (keys, enrollment, registry, hotspot)
	LanIface string // interface relays and clients are on (Wi-Fi AP, or eth0 headless)
	TunName  string // default "xs0"
	Socket   string // IPC socket; "" = no IPC
	// SocketGroup gets read/write access to the socket ("" = root only).
	SocketGroup string
	CtlPort     int // default control.Port; -1 = any free port (tests)
	DataPort    int // default control.DataPort; -1 = any free port (tests)
	ProxyPort   int // relays' proxy port, default control.ProxyPort (tests)
	Version     string
	NetSetup    bool // configure policy routing and nft for the LAN subnets
	Log         *slog.Logger
	// Hotspot runs the AP on LanIface (nil = headless: LanIface is
	// configured already); EthIface is the optional Ethernet share port.
	Hotspot  HotspotController
	EthIface string
	// DNS serves DNS on the port addresses, forwarded through the tunnel.
	DNS          bool
	DNSUpstreams []netip.AddrPort
	// Inhibitor holds a sleep/lid lock while clients or relays are present.
	Inhibitor SleepInhibitor
	// OverflowMbps: secondary paths stay unused while normal paths send at
	// least this much (0 = off).
	OverflowMbps uint16
	// Logs keeps the recent log lines Log writes, for logs.export (nil = none).
	Logs *hublog.Ring
	// LogLevel is Log's level, changeable through logs.level (nil = fixed).
	LogLevel *hublog.Level
	// ClampMSS lowers the MSS of TCP SYNs on the TUN itself (Windows: no
	// firewall rule does it there).
	ClampMSS bool
	// HotspotNAT (Windows NAT mode): clients from this subnet get the tunnel
	// address in xs-hubd (tun.NAT) and IP forwarding is on; zero = off.
	HotspotNAT netip.Prefix

	// Test hooks: LanPrefix replaces the LanIface lookup, NewTUN replaces
	// tun.Create, ResolveMAC replaces netcfg.ResolveMAC.
	LanPrefix  netip.Prefix
	NewTUN     func(tun.Config) (tun.Device, error)
	ResolveMAC func(net.HardwareAddr) (netip.Addr, bool)
	// StatsNow stamps relay stats messages (default time.Now; tests).
	StatsNow func() time.Time
	// ClientWifiRecheck: how often AskClientWifi checks whether the Wi-Fi
	// client connection is still there (0 = 5 s).
	ClientWifiRecheck time.Duration

	// Local uplinks (phase 12). ScanUplinks lists the qualifying interfaces
	// (default uplink.Scan without the LAN, share port and tunnel);
	// UplinkControl binds a socket to an interface (default
	// netcfg.BindDevice with DF); ModemInfo asks ModemManager (default
	// mmcli; nil = never).
	ScanUplinks   func() ([]uplink.Iface, error)
	UplinkControl func(iface string) func(network, address string, c syscall.RawConn) error
	ModemInfo     func(ctx context.Context) map[string]uplink.Modem
	// CaptiveURL is the captive portal probe of local uplinks (default
	// uplink.DefaultProbeURL; "off" = no probes). ProbeCaptive and
	// PortalRoute replace the probe and the portal host route (tests).
	CaptiveURL   string
	ProbeCaptive func(ctx context.Context, iface string) uplink.Probe
	PortalRoute  func(ctx context.Context, r netcfg.HostRoute, add bool) error
	// TCPConns > 0 fixes the number of TCP connections of every tcp:// path
	// (hidden test override, xs-hubd -tcp-conns); 0 = connPolicy decides.
	TCPConns int
}

// Daemon is the hub: relay control and data servers, the relay registry,
// pairing codes and one tunnel session to the server through a relay.
type Daemon struct {
	cfg      DaemonConfig
	log      *slog.Logger
	hubKey   crypto.Keypair
	relayKey crypto.Keypair
	lan      netip.Prefix // hub LAN address and prefix
	reg      *pairing.Registry
	issuer   *pairing.Issuer
	data     *net.UDPConn
	ctlLn    net.Listener
	ctx      context.Context
	client   atomic.Pointer[Client]

	// Restart (re-enrollment): Run ends with ErrRestart and the caller runs
	// a fresh daemon on the new server.json.
	stopRun    context.CancelFunc
	restartReq atomic.Bool

	// wifi is the configured hotspot card (Linux; "" = Windows). The AP may
	// run on a virtual interface next to it (cfg.LanIface then differs).
	wifi string

	// DIRECT mode (direct.go).
	route      atomic.Int32 // routeTunnel, routeDirect or routeDrain
	proxyTok   atomic.Pointer[[wire.ProxyTokenLen]byte]
	tunClosing atomic.Bool

	// Bypass list (bypass.go).
	bp       *bypass.Set
	bpRules  atomic.Pointer[bypass.Rules]
	bpReady  atomic.Bool // a relay can proxy bypass traffic
	bpWarned atomic.Bool

	// Port forwards (forwards.go).
	fwdMu    sync.Mutex
	fwdRules []ForwardRule
	fwdAddrs map[string]netip.Addr // target → resolved client address

	usage *usage.Store // usage history (usagehist.go)

	// Local uplinks (uplinks.go): upMu serializes the manager and is taken
	// before mu; locals is guarded by mu.
	upMu   sync.Mutex
	upReg  *uplink.Registry
	up     uplinkState
	nftMu  sync.Mutex // serialises netLAN snapshot + ApplyRules (see applyLAN)
	hsTry  int        // handshake attempts, rotates the uplink choice (mu)
	locals map[string]*localPath

	// Typed events (events.go); alerts is used by systemLoop only.
	events eventBus
	alerts map[string]*relayAlerts

	mu         sync.Mutex
	relays     map[string]*relayPath // connected relays by id
	enr        *Enrollment
	serverEP   string
	lastEP     map[string]string // relay id → endpoint its path last worked on
	enrolling  bool
	tunnelStop context.CancelFunc
	tunnelDone chan struct{}
	tunnelErr  error
	netLAN     netcfg.LAN // LAN routing (NetSetup)
	netErr     string     // last watchdog error (logged once)
	tunDev     tun.Device
	preTUN     tun.Device    // Windows: created before the hotspot, taken by startTunnel
	mss        atomic.Int32  // MSS clamp of the TUN (ClampMSS)
	dstack     *direct.Stack // DIRECT netstack, set with the TUN
	drainUntil time.Time

	// Hotspot, mode and power (system.go).
	hsMu    sync.Mutex // serializes hotspot changes
	hs      HotspotSettings
	hsState netcfg.HotspotState
	hsErr   string
	hsOff   bool // stopped over IPC (hotspot.power)
	clients int
	modes   ModeMachine
	inhibit bool

	// Hub settings and relay history for the GUI (settings.go).
	setMu    sync.Mutex
	overflow atomic.Uint32            // Mbps
	settings atomic.Pointer[Settings] // redundancy, mode, aggregation (overflow: above)
	pepRules atomic.Pointer[pepRules] // parsed Settings.PEPExclude
	testMu   sync.Mutex               // one speed test at a time
	gone     map[string]goneInfo
}

// NewDaemon loads keys and the registry and binds the LAN sockets.
func NewDaemon(cfg DaemonConfig) (*Daemon, error) {
	if cfg.TunName == "" {
		cfg.TunName = "xs0"
	}
	cfg.CtlPort = portOr(cfg.CtlPort, control.Port)
	cfg.DataPort = portOr(cfg.DataPort, control.DataPort)
	if cfg.ProxyPort == 0 {
		cfg.ProxyPort = control.ProxyPort
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.NewTUN == nil {
		cfg.NewTUN = tun.Create
	}
	if cfg.ResolveMAC == nil {
		cfg.ResolveMAC = netcfg.ResolveMAC
	}
	if cfg.UplinkControl == nil {
		cfg.UplinkControl = func(iface string) func(string, string, syscall.RawConn) error {
			return netcfg.BindDevice(iface, true)
		}
	}
	if cfg.CaptiveURL == "" {
		cfg.CaptiveURL = uplink.DefaultProbeURL
	}
	if cfg.ModemInfo == nil {
		cfg.ModemInfo = func(ctx context.Context) map[string]uplink.Modem { return uplink.Modems(ctx, uplink.ExecRun) }
	}
	d := &Daemon{cfg: cfg, log: cfg.Log, relays: make(map[string]*relayPath), lan: cfg.LanPrefix,
		gone: make(map[string]goneInfo), lastEP: make(map[string]string), bp: bypass.NewSet()}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("hub state dir: %w", err)
	}
	d.overflow.Store(uint32(cfg.OverflowMbps))
	s, ok, err := loadSettings(cfg.Dir)
	if err != nil {
		return nil, err
	}
	if ok { // saved over IPC: wins over the flag
		d.overflow.Store(uint32(s.OverflowMbps))
	}
	d.settings.Store(&s)
	r, err := parsePEPExclude(s.PEPExclude)
	if err != nil {
		return nil, fmt.Errorf("hub settings: %w", err)
	}
	d.pepRules.Store(&r)
	if err := d.loadBypass(); err != nil {
		return nil, err
	}
	if err := d.loadForwards(); err != nil {
		return nil, err
	}
	if d.usage, err = openUsage(cfg.Dir); err != nil {
		return nil, err
	}
	if err := d.openUplinks(); err != nil {
		return nil, err
	}
	if d.cfg.ScanUplinks == nil {
		d.cfg.ScanUplinks = d.defaultScan
	}
	d.wifi = cfg.LanIface
	d.createTUNFirst()
	if cfg.Hotspot != nil {
		if err := d.startHotspot(); err != nil {
			d.closePreTUN()
			return nil, err
		}
	} else if !d.lan.IsValid() {
		if d.lan, err = netcfg.InterfaceIPv4(cfg.LanIface); err != nil {
			return nil, err
		}
	}
	if err := d.open(); err != nil {
		if cfg.Hotspot != nil {
			d.stopHotspot()
		}
		d.closePreTUN()
		return nil, err
	}
	return d, nil
}

// createTUNFirst creates the TUN of an enrolled hub before Mobile Hotspot
// starts (Windows): the hotspot then shares the tunnel's profile from the
// start instead of restarting a few seconds later, which dropped the
// phones (phase 19). Failure only logs: startTunnel tries again.
func (d *Daemon) createTUNFirst() {
	if _, ok := d.cfg.Hotspot.(tunnelSharer); !ok {
		return
	}
	e, err := LoadEnrollment(d.cfg.Dir)
	if err != nil {
		return
	}
	c, err := d.tunConfig(e)
	if err != nil {
		return
	}
	if d.preTUN, err = d.cfg.NewTUN(c); err != nil {
		d.log.Warn("tunnel interface before the hotspot", "err", err)
		d.preTUN = nil
	}
}

func (d *Daemon) closePreTUN() {
	if d.preTUN != nil {
		_ = d.preTUN.Close()
		d.preTUN = nil
	}
}

// open loads keys and the registry, binds the LAN sockets and makes the
// pairing issuer.
func (d *Daemon) open() error {
	cfg := d.cfg
	var err error
	if d.hubKey, err = LoadOrCreateKey(cfg.Dir); err != nil {
		return err
	}
	_, serr := os.Stat(filepath.Join(cfg.Dir, TPMKeyFile))
	d.log.Info("hub key loaded", "key", d.hubKey, "tpm", serr == nil)
	if d.relayKey, err = loadOrCreateKeyFile(cfg.Dir, RelayKeyFile); err != nil {
		return err
	}
	if d.reg, err = pairing.OpenRegistry(cfg.Dir, nil); err != nil {
		return err
	}
	if e, err := LoadEnrollment(cfg.Dir); err == nil {
		d.enr = &e
		d.serverEP = firstEndpoint(e.Endpoints)
	} else if !errors.Is(err, fs.ErrNotExist) {
		d.log.Error("enrollment file unreadable: the hub starts as not enrolled", "err", err)
	}
	ip := d.lan.Addr().String()
	if d.ctlLn, err = net.Listen("tcp4", net.JoinHostPort(ip, strconv.Itoa(cfg.CtlPort))); err != nil {
		return fmt.Errorf("control listen: %w", err)
	}
	ua := net.UDPAddrFromAddrPort(netip.AddrPortFrom(d.lan.Addr(), uint16(cfg.DataPort)))
	if d.data, err = net.ListenUDP("udp4", ua); err != nil {
		_ = d.ctlLn.Close()
		return fmt.Errorf("data listen: %w", err)
	}
	d.cfg.CtlPort = d.ctlLn.Addr().(*net.TCPAddr).Port
	d.cfg.DataPort = d.data.LocalAddr().(*net.UDPAddr).Port
	name, _ := os.Hostname()
	if d.enr != nil {
		name = d.enr.Name
	}
	if name == "" {
		name = "hub"
	}
	base := pairing.Payload{V: pairing.Version, Hub: name, IP: ip,
		CPort: d.cfg.CtlPort, DPort: d.cfg.DataPort, HPK: crypto.EncodeKey(d.relayKey.Public)}
	if d.cfg.Hotspot != nil {
		base.SSID, base.PSK, base.BSSID = d.hs.SSID, d.hs.PSK, bssid(d.hs, d.hsState)
	}
	d.issuer = pairing.NewIssuer(base, nil)
	if _, _, err := d.issuer.Current(); err != nil {
		_ = d.ctlLn.Close()
		_ = d.data.Close()
		return err
	}
	return nil
}

func portOr(p, def int) int {
	switch p {
	case 0:
		return def
	case -1:
		return 0
	}
	return p
}

// Run serves relays and the IPC socket and keeps the tunnel up (once
// enrolled) until ctx ends; then it restores the network configuration.
func (d *Daemon) Run(parent context.Context) error {
	ctx, stopRun := context.WithCancel(parent)
	defer stopRun()
	d.mu.Lock()
	d.ctx, d.stopRun = ctx, stopRun
	d.mu.Unlock()
	defer func() { _ = d.ctlLn.Close(); _ = d.data.Close() }()
	if d.cfg.Hotspot != nil {
		defer d.stopHotspot()
	}
	if d.cfg.NetSetup {
		if err := d.setupNet(ctx); err != nil {
			return err
		}
		defer d.cleanupNet()
	}
	if d.cfg.Socket != "" {
		ln, err := ipc.Listen(d.cfg.Socket, 0o660)
		if err != nil {
			return err
		}
		if d.cfg.SocketGroup != "" {
			if err := ipc.Chgrp(d.cfg.Socket, d.cfg.SocketGroup); err != nil {
				d.log.Warn("IPC socket stays root-only", "err", err)
			}
		}
		defer func() { _ = ln.Close(); _ = os.Remove(d.cfg.Socket) }()
		// Stop accepting at once: the shutdown below takes seconds, and a
		// GUI that still got an answer would not start the hub again.
		stopLn := context.AfterFunc(ctx, func() { _ = ln.Close() })
		defer stopLn()
		go ipc.Serve(ctx, ln, d.handleRPC, d.log)
	}
	if d.cfg.DNS {
		go d.runDNS(ctx)
	}
	if d.cfg.Inhibitor != nil {
		defer func() { _ = d.cfg.Inhibitor.Set(false) }()
	}
	go d.acceptLoop()
	go d.dataLoop()
	go d.tickLoop(ctx)
	go d.systemLoop(ctx)
	// Local paths outlive ctx until the tunnel has said CLOSE: they may be
	// the only way to the server.
	upCtx, upCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer upCancel()
	upDone := make(chan struct{})
	go func() { defer close(upDone); d.uplinkLoop(upCtx) }()
	d.log.Info("hub listening", "lan", d.lan, "control", d.cfg.CtlPort, "data", d.cfg.DataPort)
	d.mu.Lock()
	e := d.enr
	d.mu.Unlock()
	if e != nil {
		if err := d.startTunnel(*e); err != nil {
			return err
		}
	} else {
		d.log.Info("hub not enrolled yet: run xs-hubctl enroll <XS1 code> (through a paired phone, a local link or this computer's connection)")
	}
	<-ctx.Done()
	d.stopTunnel()
	d.closePreTUN()
	upCancel()
	<-upDone // local paths stopped
	d.saveUsage(time.Now())
	d.mu.Lock()
	for _, rc := range d.relays {
		_ = rc.ctl.Close()
	}
	dev, ds := d.tunDev, d.dstack
	d.mu.Unlock()
	if ds != nil {
		ds.Close()
	}
	if dev != nil {
		d.tunClosing.Store(true)
		_ = dev.Close()
	}
	if d.restartReq.Load() && parent.Err() == nil {
		return ErrRestart
	}
	return nil
}

// setServerEP records the server endpoint relays forward to and tells every
// connected relay when it changes.
func (d *Daemon) setServerEP(ep string) {
	d.mu.Lock()
	if d.serverEP == ep {
		d.mu.Unlock()
		return
	}
	d.serverEP = ep
	rcs := d.connected()
	lps := d.localPaths()
	for _, lp := range lps {
		lp.setEP(ep)
	}
	for _, rc := range rcs {
		rc.setEP(ep)
	}
	clear(d.lastEP)
	d.mu.Unlock()
	for _, rc := range rcs {
		d.sendEndpoint(rc)
	}
	for _, lp := range lps {
		lp.redial()
	}
}

// firstEndpoint returns the first endpoint in fallback order (UDP first).
func firstEndpoint(eps []string) string {
	if o := orderedEndpoints(eps); len(o) > 0 {
		return o[0]
	}
	return ""
}
