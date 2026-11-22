package hub

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
)

// silentServer is a UDP endpoint that reads INITs and never answers.
func silentServer(t *testing.T) string {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := c.ReadFromUDP(buf); err != nil {
				return
			}
		}
	}()
	return "udp://" + c.LocalAddr().String()
}

// testCode returns an XS1 code for a new server at ep.
func testCode(t *testing.T, name, ep string) (string, enroll.Code) {
	t.Helper()
	kp, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := enroll.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	c := enroll.Code{Endpoints: []string{ep, "tcp://" + strings.TrimPrefix(ep, "udp://")}, ServerKey: kp.Public,
		Token: tok, Name: name}
	s, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func oldEnrollment(t *testing.T) Enrollment {
	t.Helper()
	kp, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	return Enrollment{Name: "old", ServerKey: crypto.EncodeKey(kp.Public),
		Endpoints: []string{"udp://192.0.2.1:51900"}, TunnelIP: "100.64.0.3", MTU: 1360}
}

func TestPendingEnrollmentFile(t *testing.T) {
	tok, err := enroll.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		token   string
		pending bool
		ok      bool
	}{
		{"confirmed", "", false, true},
		{"pending", tok.String(), true, true},
		{"pending with a bad token", "not-a-token", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			e := oldEnrollment(t)
			e.Token, e.Pending = tt.token, true // Pending is never saved
			if err := SaveEnrollment(dir, e); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(filepath.Join(dir, EnrollmentFile))
			if strings.Contains(string(b), `"pending"`) {
				t.Fatalf("pending flag saved: %s", b)
			}
			got, err := LoadEnrollment(dir)
			if (err == nil) != tt.ok {
				t.Fatalf("load: %v, want ok=%v", err, tt.ok)
			}
			if !tt.ok {
				return
			}
			if got.pending() != tt.pending || got.Pending {
				t.Fatalf("loaded %+v, want pending=%v", got, tt.pending)
			}
			if r := got.result(); r.Token != "" || r.Pending != tt.pending {
				t.Fatalf("RPC result %+v", r)
			}
		})
	}
}

// TestReEnrollReplacesServer: a re-enrollment whose server does not answer
// still switches the hub to the new server (pending, with the token); a
// cancelled call, a bad code or a first enrollment do not save anything new.
func TestReEnrollReplacesServer(t *testing.T) {
	ep := silentServer(t)
	tests := []struct {
		name     string
		enrolled bool
		bad      bool
		cancel   bool
		wantErr  bool
		pending  bool
	}{
		{"again, no answer: switched, pending", true, false, false, false, true},
		{"again, cancelled: old server kept", true, false, true, true, false},
		{"again, malformed code: old server kept", true, true, false, true, false},
		{"first enrollment, no answer: error", false, false, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := shortTemp(t)
			old := oldEnrollment(t)
			if tt.enrolled {
				if err := SaveEnrollment(dir, old); err != nil {
					t.Fatal(err)
				}
			}
			d := newTestDaemon(t, dir, DaemonConfig{enrollAttempts: 1})
			code, c := testCode(t, "new", ep)
			if tt.bad {
				code = "XS1.junk"
			}
			ctx, cancel := context.WithCancel(t.Context())
			if tt.cancel {
				cancel()
			}
			defer cancel()
			res, err := d.Enroll(ctx, code)
			if (err != nil) != tt.wantErr {
				t.Fatalf("enroll: %v, want error=%v", err, tt.wantErr)
			}
			if res.Pending != tt.pending || res.Token != "" {
				t.Fatalf("result %+v", res)
			}
			saved, lerr := LoadEnrollment(dir)
			switch {
			case tt.pending:
				if lerr != nil || saved.ServerKey != crypto.EncodeKey(c.ServerKey) || saved.Name != "new" ||
					saved.Token != c.Token.String() || saved.TunnelIP != old.TunnelIP || saved.Endpoints[0] != ep {
					t.Fatalf("saved %+v, %v", saved, lerr)
				}
			case tt.enrolled:
				if lerr != nil || saved.ServerKey != old.ServerKey || saved.pending() {
					t.Fatalf("old enrollment not kept: %+v, %v", saved, lerr)
				}
			default:
				if !errors.Is(lerr, os.ErrNotExist) {
					t.Fatalf("enrollment saved after a failed first enrollment: %+v, %v", saved, lerr)
				}
			}
		})
	}
}

// ctlPair returns the hub and relay ends of a control channel.
func ctlPair(t *testing.T) (hubEnd, relayEnd *control.Conn) {
	t.Helper()
	hk, _ := crypto.GenerateKeypair()
	rk, _ := crypto.GenerateKeypair()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	done := make(chan error, 1)
	go func() {
		var err error
		relayEnd, err = control.Client(b, rk, hk.Public)
		done <- err
	}()
	hubEnd, err := control.Server(a, hk)
	if err != nil || <-done != nil {
		t.Fatalf("control pair: %v", err)
	}
	return hubEnd, relayEnd
}

// TestSetServerEPResetsMovedPaths: a relay that endpointFallback moved to TCP
// goes back to the given endpoint even when the hub-wide one did not change;
// a relay already on it gets no message (it would redial).
func TestSetServerEPResetsMovedPaths(t *testing.T) {
	const udp, tcp = "udp://192.0.2.1:51900", "tcp://192.0.2.1:51900"
	d := newTestDaemon(t, shortTemp(t), DaemonConfig{})
	moved, stays := &relayPath{d: d}, &relayPath{d: d}
	moved.rec.ID, stays.rec.ID = "r1", "r2"
	var movedRelay, staysRelay *control.Conn
	moved.ctl, movedRelay = ctlPair(t)
	stays.ctl, staysRelay = ctlPair(t)
	d.mu.Lock()
	d.serverEP = udp
	moved.setEP(tcp)
	stays.setEP(udp)
	d.relays["r1"], d.relays["r2"] = moved, stays
	d.lastEP["r2"] = udp
	d.mu.Unlock()

	got := make(chan control.Message, 1)
	go func() {
		m, err := movedRelay.Recv()
		if err != nil {
			t.Error(err)
		}
		got <- m
	}()
	d.setServerEP(udp)
	if m, ok := (<-got).(*control.Endpoint); !ok || m.ServerEP != udp {
		t.Fatalf("moved relay got %#v", m)
	}
	_ = staysRelay.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if m, err := staysRelay.Recv(); err == nil {
		t.Fatalf("relay already on the endpoint got %#v (it would redial)", m)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if moved.ep != udp || moved.Stream() || stays.ep != udp {
		t.Fatalf("endpoints %q %q", moved.ep, stays.ep)
	}
	if _, ok := d.lastEP["r2"]; !ok {
		t.Fatal("last working endpoint forgotten although the server endpoint did not change")
	}
}
