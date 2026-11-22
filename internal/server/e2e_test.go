package server_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/server"
	"github.com/zyndata/cross-stream/internal/server/admin"
	"github.com/zyndata/cross-stream/internal/tun"
)

var origin = netip.MustParseAddr("192.0.2.10")

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ipv4 builds a minimal IPv4/UDP-like packet with payload tag.
func ipv4(src, dst netip.Addr, tag byte) []byte {
	p := make([]byte, 28)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], 28)
	p[8], p[9] = 64, 17
	s, d := src.As4(), dst.As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	p[27] = tag
	return p
}

// collect reads every packet from dev into a channel (one reader per device).
func collect(dev tun.Device) <-chan []byte {
	ch := make(chan []byte, 256)
	go func() {
		buf := make([]byte, 2000)
		for {
			n, err := dev.Read(buf)
			if err != nil {
				return
			}
			ch <- append([]byte(nil), buf[:n]...)
		}
	}()
	return ch
}

// readTag waits for a packet carrying tag.
func readTag(t *testing.T, ch <-chan []byte, tag byte, within time.Duration) []byte {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case p := <-ch:
			if len(p) == 28 && p[27] == tag {
				return p
			}
		case <-deadline:
			return nil
		}
	}
}

type env struct {
	ctx    context.Context
	srv    *server.Server
	ctl    admin.Client
	origin tun.Device // internet side of the server TUN
	inet   <-chan []byte
	done   chan error
}

func startServer(t *testing.T) *env { t.Helper(); return startServerFW(t, nil) }

func startServerFW(t *testing.T, fw server.Firewall) *env {
	t.Helper()
	return startServerWith(t, func(o *server.Options) { o.Firewall = fw })
}

// startServerWith starts a server; opt may change its options.
func startServerWith(t *testing.T, opt func(*server.Options)) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := server.OpenState(filepath.Join(dir, "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	srvTun, originTun := tun.Pipe("xss0", "inet", 1360)
	cfg := server.DefaultConfig()
	cfg.ListenAddress, cfg.ListenPorts = "127.0.0.1", []int{0}
	cfg.TCPPorts, cfg.WSSPorts = []int{0}, []int{0}
	cfg.DataDir = dir
	cfg.ControlSocket = filepath.Join(dir, "ctl.sock")
	o := server.Options{Config: cfg, Key: key, State: st, TUN: srvTun, Log: quiet(),
		Detect: func() (netip.Addr, error) { return netip.MustParseAddr("127.0.0.1"), nil }}
	opt(&o)
	srv, err := server.New(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Listen(ctx); err != nil {
		cancel()
		t.Skipf("listen (unix sockets needed): %v", err)
	}
	e := &env{ctx: ctx, srv: srv, ctl: admin.Client{Socket: cfg.ControlSocket}, origin: originTun, inet: collect(originTun), done: make(chan error, 1)}
	go func() { e.done <- srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-e.done:
			if err != nil {
				t.Errorf("server run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	return e
}

func (e *env) enrollHub(t *testing.T, code string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	conn, err := hub.ListenUDP("")
	if err != nil {
		t.Fatal(err)
	}
	_, err = hub.EnrollWithCode(e.ctx, dir, code, conn, "test", quiet())
	return dir, err
}

func connect(t *testing.T, e *env, dir string) (tun.Device, <-chan []byte, chan error, context.CancelFunc) {
	t.Helper()
	app, in, done, cancel, _ := connectClient(t, e, dir)
	return app, in, done, cancel
}

func connectClient(t *testing.T, e *env, dir string) (tun.Device, <-chan []byte, chan error, context.CancelFunc, *hub.Client) {
	t.Helper()
	en, err := hub.LoadEnrollment(dir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := hub.LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	sk, _ := crypto.ParseKey(en.ServerKey)
	eps, err := hub.ResolveEndpoints(en.Endpoints)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := hub.ListenUDP("")
	if err != nil {
		t.Fatal(err)
	}
	hubTun, app := tun.Pipe("xs0", "app", 1360)
	ctx, cancel := context.WithCancel(e.ctx)
	c := &hub.Client{Key: key, ServerKey: sk, Endpoints: eps, Conn: conn, Name: en.Name, Version: "test", TUN: hubTun, Log: quiet()}
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return app, collect(app), done, cancel, c
}

func TestEnrollConnectForwardRevoke(t *testing.T) {
	e := startServer(t)
	res, err := e.ctl.Enroll(e.ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := e.enrollHub(t, res.Code)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	en, _ := hub.LoadEnrollment(dir)
	if en.TunnelIP != "100.64.0.2" || en.Name != "t1" || en.MTU != 1360 {
		t.Fatalf("enrollment %+v", en)
	}
	hubIP := netip.MustParseAddr(en.TunnelIP)

	app, appIn, done, cancel := connect(t, e, dir)
	defer cancel()

	// Uplink: retry until the session is up.
	var up []byte
	for i := 0; i < 50 && up == nil; i++ {
		if _, err := app.Write(ipv4(hubIP, origin, 1)); err != nil {
			t.Fatal(err)
		}
		up = readTag(t, e.inet, 1, 100*time.Millisecond)
	}
	if !bytes.Equal(up, ipv4(hubIP, origin, 1)) {
		t.Fatalf("uplink packet %x", up)
	}
	// Downlink.
	if _, err := e.origin.Write(ipv4(origin, hubIP, 2)); err != nil {
		t.Fatal(err)
	}
	if readTag(t, appIn, 2, 2*time.Second) == nil {
		t.Fatal("downlink packet not delivered")
	}
	// A spoofed source is dropped; the next valid packet still passes.
	_, _ = app.Write(ipv4(netip.MustParseAddr("100.64.0.99"), origin, 3))
	_, _ = app.Write(ipv4(hubIP, origin, 4))
	if p := readTag(t, e.inet, 3, 300*time.Millisecond); p != nil {
		t.Fatal("spoofed packet forwarded")
	}

	sess, err := e.ctl.Sessions(e.ctx)
	if err != nil || len(sess) != 1 || sess[0].Hub != "t1" || len(sess[0].Paths) != 1 || sess[0].RxBytes == 0 {
		t.Fatalf("sessions %+v %v", sess, err)
	}
	hubs, err := e.ctl.Hubs(e.ctx)
	if err != nil || len(hubs) != 1 || !hubs[0].Online {
		t.Fatalf("hubs %+v %v", hubs, err)
	}

	// Reusing the same code fails.
	if _, err := e.enrollHub(t, res.Code); !errors.Is(err, hub.ErrNoResponse) {
		t.Fatalf("code reuse: %v", err)
	}
	// Enrolling the same name again needs a revoke first.
	if _, err := e.ctl.Enroll(e.ctx, "t1"); err == nil {
		t.Fatal("second enroll of an enrolled name accepted")
	}

	// Revoke drops the session within 1 s.
	start := time.Now()
	if err := e.ctl.Revoke(e.ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, hub.ErrRevoked) {
			t.Fatalf("hub run ended with %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("hub not told about the revoke within 1 s")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("revoke took %v", d)
	}
	if sess, _ := e.ctl.Sessions(e.ctx); len(sess) != 0 {
		t.Fatalf("sessions after revoke: %+v", sess)
	}
	if err := e.ctl.Revoke(e.ctx, "t1"); err == nil {
		t.Fatal("revoking twice succeeded")
	}
}

func TestServerShutdownClosesHubSession(t *testing.T) {
	e := startServer(t)
	res, err := e.ctl.Enroll(e.ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := e.enrollHub(t, res.Code)
	if err != nil {
		t.Fatal(err)
	}
	app, _, done, cancel := connect(t, e, dir)
	en, _ := hub.LoadEnrollment(dir)
	ip := netip.MustParseAddr(en.TunnelIP)
	var up []byte
	for i := 0; i < 50 && up == nil; i++ {
		_, _ = app.Write(ipv4(ip, origin, 7))
		up = readTag(t, e.inet, 7, 100*time.Millisecond)
	}
	if up == nil {
		t.Fatal("session never came up")
	}
	cancel() // hub stops and sends CLOSE
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("hub run: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, _ := e.ctl.Sessions(e.ctx); len(s) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server kept the session after the hub's CLOSE")
}
