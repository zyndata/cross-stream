package xscore

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/relay"
)

type stopEvent struct{ kind, reason string }

type chanCallback chan stopEvent

func (c chanCallback) OnStopped(kind, reason string) { c <- stopEvent{kind, reason} }

// fakeHub accepts control connections on loopback and welcomes every relay.
type fakeHub struct {
	key   crypto.Keypair
	ln    net.Listener
	data  *net.UDPConn
	hello chan *control.Hello
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	key, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	data, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHub{key: key, ln: ln, data: data, hello: make(chan *control.Hello, 4)}
	t.Cleanup(func() { _ = ln.Close(); _ = data.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h.serve(t, c)
		}
	}()
	return h
}

func (h *fakeHub) serve(t *testing.T, c net.Conn) {
	cc, err := control.Server(c, h.key)
	if err != nil {
		_ = c.Close()
		return
	}
	t.Cleanup(func() { _ = cc.Close() })
	for {
		m, err := cc.Recv()
		if err != nil {
			return
		}
		if hello, ok := m.(*control.Hello); ok {
			h.hello <- hello
			_ = cc.Send(&control.Welcome{RelayID: "r1", PathID: 1, DataPort: h.data.LocalAddr().(*net.UDPAddr).Port})
		}
	}
}

func (h *fakeHub) qr(t *testing.T) string {
	t.Helper()
	tok, err := enroll.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	p := pairing.Payload{V: 1, Hub: "hub1", SSID: "CrossStream-x", BSSID: "02:11:22:33:44:55", PSK: "secretsecret",
		IP: "127.0.0.1", CPort: h.ln.Addr().(*net.TCPAddr).Port, DPort: h.data.LocalAddr().(*net.UDPAddr).Port,
		HPK: crypto.EncodeKey(h.key.Public), Tok: tok.String()}
	code, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func noBind(int64, uintptr) error { return nil }

func stats(t *testing.T, c *Core) statsJSON {
	t.Helper()
	var s statsJSON
	if err := json.Unmarshal([]byte(c.Stats()), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func waitConnected(t *testing.T, c *Core) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !stats(t, c).Connected {
		if time.Now().After(deadline) {
			t.Fatal("relay did not connect")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitStop(t *testing.T, cb chanCallback) stopEvent {
	t.Helper()
	select {
	case e := <-cb:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("OnStopped not called")
		return stopEvent{}
	}
}

func TestCoreStartStop(t *testing.T) {
	h := newFakeHub(t)
	c, err := newCore("", "phone", "Play", noBind)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{NetWiFi, NetCellular} {
		if err := c.SetNetwork(n, 100); err != nil {
			t.Fatal(err)
		}
	}
	qr := h.qr(t)
	dir := t.TempDir()
	cb := make(chanCallback, 1)

	// First start pairs (token sent), second start is already paired.
	for i, wantToken := range []bool{true, false} {
		if err := c.Start(dir, qr, `{"cap_mb":100,"cap_period":"day","used_mb":3}`, cb); err != nil {
			t.Fatal(err)
		}
		if err := c.Start(dir, qr, "", cb); err == nil {
			t.Fatal("second Start while running succeeded")
		}
		hello := <-h.hello
		if (hello.PairToken != "") != wantToken || hello.RelayName != "phone" || hello.Carrier != "Play" ||
			hello.Limits.CapMB != 100 || hello.Limits.UsedMB != 3 {
			t.Fatalf("run %d: hello %+v", i, hello)
		}
		waitConnected(t, c)
		s := stats(t, c)
		if !s.Running || s.Hub != "hub1" || s.RelayID != "r1" || s.PathID != 1 || !s.WiFi || !s.Cellular {
			t.Fatalf("run %d: stats %+v", i, s)
		}
		c.Stop()
		if e := waitStop(t, cb); e.kind != StopUser {
			t.Fatalf("run %d: stop %+v", i, e)
		}
		if s := stats(t, c); s.Running || s.Connected || s.LastError != "" {
			t.Fatalf("run %d: stats after stop %+v", i, s)
		}
	}
}

func TestCoreNetworkLostAndBack(t *testing.T) {
	h := newFakeHub(t)
	c, err := newCore("", "phone", "", noBind)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetNetwork(NetCellular, 7)
	cb := make(chanCallback, 1)
	if err := c.Start(t.TempDir(), h.qr(t), "", cb); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	time.Sleep(300 * time.Millisecond) // no Wi-Fi yet: dials fail
	if s := stats(t, c); s.Connected || s.WiFi {
		t.Fatalf("connected without Wi-Fi: %+v", s)
	}
	_ = c.SetNetwork(NetWiFi, 5)
	<-h.hello
	waitConnected(t, c)
	_ = c.SetNetwork(NetWiFi, 6) // new network: reconnect
	select {
	case <-h.hello:
	case <-time.After(3 * time.Second):
		t.Fatal("no reconnect after the Wi-Fi network changed")
	}
}

func TestCoreErrors(t *testing.T) {
	if _, err := NewCore("", "", ""); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := NewCore(filepath.Join(t.TempDir(), "missing"), "n", ""); err == nil {
		t.Fatal("missing log dir accepted")
	}
	logDir := t.TempDir()
	c, err := NewCore(logDir, "phone", "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Start(t.TempDir(), "XSR1.bad", "", nil); err == nil {
		t.Fatal("bad QR accepted")
	}
	h := newFakeHub(t)
	if err := c.Start("", h.qr(t), "", nil); err == nil {
		t.Fatal("empty state dir accepted")
	}
	for _, lim := range []string{"{", `{"cap_period":"week"}`} {
		if err := c.Start(t.TempDir(), h.qr(t), lim, nil); err == nil {
			t.Fatalf("limits %s accepted", lim)
		}
		if err := c.SetLimits(lim); err == nil {
			t.Fatalf("SetLimits %s accepted", lim)
		}
	}
	if err := c.SetLimits(`{"cap_mb":5}`); err != nil {
		t.Fatal(err)
	}
	if err := c.StopWith("bored"); err == nil {
		t.Fatal("unknown stop reason accepted")
	}
	if err := c.SetNetwork("bluetooth", 1); err == nil {
		t.Fatal("unknown network kind accepted")
	}
	c.Stop() // not running: no-op
	if s := stats(t, c); s.Running {
		t.Fatalf("running: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(logDir, LogFile)); err != nil {
		t.Fatal(err)
	}
}

func TestCoreStopWithAndLimits(t *testing.T) {
	h := newFakeHub(t)
	c, err := newCore("", "phone", "", noBind)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetNetwork(NetWiFi, 1)
	_ = c.SetNetwork(NetCellular, 2)
	cb := make(chanCallback, 1)
	if err := c.Start(t.TempDir(), h.qr(t), "", cb); err != nil {
		t.Fatal(err)
	}
	<-h.hello
	waitConnected(t, c)
	if err := c.SetLimits(`{"cap_mb":50,"cap_period":"month","reset_day":3}`); err != nil {
		t.Fatal(err)
	}
	if err := c.StopWith(control.LeavingNoData); err != nil {
		t.Fatal(err)
	}
	if e := waitStop(t, cb); e.kind != StopLimit || e.reason != control.LeavingNoData {
		t.Fatalf("stop %+v", e)
	}
	if s := stats(t, c); s.LastError != "limit: no_data" {
		t.Fatalf("stats %+v", s)
	}
}

func TestParseQR(t *testing.T) {
	h := newFakeHub(t)
	tests := []struct {
		name string
		qr   string
		ok   bool
	}{
		{"valid", h.qr(t), true},
		{"no prefix", "hello", false},
		{"bad base64", "XSR1.***", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ParseQR(tt.qr)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if !tt.ok {
				return
			}
			var q qrJSON
			if err := json.Unmarshal([]byte(out), &q); err != nil {
				t.Fatal(err)
			}
			if q != (qrJSON{Hub: "hub1", HPK: crypto.EncodeKey(h.key.Public), SSID: "CrossStream-x", BSSID: "02:11:22:33:44:55", PSK: "secretsecret"}) {
				t.Fatalf("got %+v", q)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	c, err := newCore("", "phone", "", noBind)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		err        error
		kind, want string
	}{
		{nil, StopUser, control.LeavingUser},
		{&relay.StoppedError{Reason: control.LeavingBattery}, StopLimit, control.LeavingBattery},
		{&relay.StoppedError{Reason: control.LeavingUser}, StopUser, control.LeavingUser},
		{&relay.RejectedError{Reason: "hub full"}, StopRejected, "hub full"},
		{relay.ErrForgotten, StopForgotten, ""},
		{net.ErrClosed, StopError, net.ErrClosed.Error()},
	}
	for _, tt := range tests {
		kind, reason := c.classify(tt.err, t.TempDir())
		if kind != tt.kind || reason != tt.want {
			t.Errorf("classify(%v) = %q %q, want %q %q", tt.err, kind, reason, tt.kind, tt.want)
		}
	}
}
