package pairing

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
)

func key(t *testing.T) [crypto.KeyLen]byte {
	t.Helper()
	kp, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	return kp.Public
}

func validPayload(t *testing.T) Payload {
	t.Helper()
	tok, err := enroll.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return Payload{V: 1, Hub: "hub1", SSID: "CrossStream", BSSID: "02:11:22:33:44:55", PSK: "secret123",
		IP: "10.42.0.1", CPort: control.Port, DPort: control.DataPort, HPK: crypto.EncodeKey(key(t)), Tok: tok.String()}
}

func TestPayloadRoundTrip(t *testing.T) {
	p := validPayload(t)
	headless := p
	headless.SSID, headless.BSSID, headless.PSK = "", "", ""
	for _, p := range []Payload{p, headless} {
		code, err := p.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(code, Prefix) || strings.ContainsAny(code, "+/=") {
			t.Fatalf("code %q not XSR1 base64url", code)
		}
		got, err := Decode(" " + code + "\n")
		if err != nil {
			t.Fatal(err)
		}
		if got != p {
			t.Fatalf("got %+v, want %+v", got, p)
		}
		if got.ControlAddr().String() != "10.42.0.1:51901" || got.DataAddr().String() != "10.42.0.1:51902" {
			t.Fatalf("addrs %v %v", got.ControlAddr(), got.DataAddr())
		}
		if k, err := got.HubKey(); err != nil || crypto.EncodeKey(k) != p.HPK {
			t.Fatalf("hub key %v", err)
		}
	}
}

func TestDecodeInvalid(t *testing.T) {
	enc := func(mut func(p *Payload)) string {
		p := validPayload(t)
		mut(&p)
		b, _ := json.Marshal(p)
		return Prefix + base64.RawURLEncoding.EncodeToString(b)
	}
	tests := []struct {
		name, code string
	}{
		{"no prefix", "XS1.abc"},
		{"bad base64", Prefix + "!!!"},
		{"bad json", Prefix + base64.RawURLEncoding.EncodeToString([]byte("{"))},
		{"version", enc(func(p *Payload) { p.V = 2 })},
		{"no hub", enc(func(p *Payload) { p.Hub = "" })},
		{"long ssid", enc(func(p *Payload) { p.SSID = strings.Repeat("s", 33) })},
		{"bad bssid", enc(func(p *Payload) { p.BSSID = "zz" })},
		{"ipv6", enc(func(p *Payload) { p.IP = "::1" })},
		{"bad ip", enc(func(p *Payload) { p.IP = "hub" })},
		{"port 0", enc(func(p *Payload) { p.CPort = 0 })},
		{"port high", enc(func(p *Payload) { p.DPort = 70000 })},
		{"bad hpk", enc(func(p *Payload) { p.HPK = "AAAA" })},
		{"bad tok", enc(func(p *Payload) { p.Tok = "AAAA" })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decode(tt.code); !errors.Is(err, ErrPayload) {
				t.Fatalf("err %v, want ErrPayload", err)
			}
		})
	}
	bad := validPayload(t)
	bad.IP = ""
	if _, err := bad.Encode(); !errors.Is(err, ErrPayload) {
		t.Fatalf("encode invalid: %v", err)
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestIssuer(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	base := validPayload(t)
	base.Tok = ""
	is := NewIssuer(base, c.now)

	p1, exp, err := is.Current()
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(c.t.Add(TokenTTL)) || p1.Hub != "hub1" || p1.Tok == "" {
		t.Fatalf("first code %+v exp %v", p1, exp)
	}
	if again, _, _ := is.Current(); again != p1 {
		t.Fatal("code changed before expiry")
	}

	// Pairing with the current token makes a new code; the token is single use.
	if err := is.Consume(p1.Tok); err != nil {
		t.Fatal(err)
	}
	if err := is.Consume(p1.Tok); !errors.Is(err, ErrToken) {
		t.Fatalf("reuse: %v", err)
	}
	p2, _, _ := is.Current()
	if p2.Tok == p1.Tok {
		t.Fatal("code not regenerated after pairing")
	}

	// After TTL the code is regenerated; the old token is expired.
	c.t = c.t.Add(TokenTTL)
	p3, _, _ := is.Current()
	if p3.Tok == p2.Tok {
		t.Fatal("code not regenerated after TTL")
	}
	if err := is.Consume(p2.Tok); !errors.Is(err, ErrToken) {
		t.Fatalf("expired token: %v", err)
	}

	// A rotated-out but unexpired token still works once.
	if err := is.Rotate(); err != nil {
		t.Fatal(err)
	}
	if err := is.Consume(p3.Tok); err != nil {
		t.Fatalf("older unexpired token: %v", err)
	}
	for _, bad := range []string{"", "AAAA", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if err := is.Consume(bad); !errors.Is(err, ErrToken) {
			t.Fatalf("token %q: %v", bad, err)
		}
	}

	// A hotspot change makes a new code with the new Wi-Fi fields.
	p4, _, _ := is.Current()
	if err := is.SetWiFi("NewNet", "02:00:00:00:00:01", "new-psk-123"); err != nil {
		t.Fatal(err)
	}
	p5, _, _ := is.Current()
	if p5.Tok == p4.Tok || p5.SSID != "NewNet" || p5.BSSID != "02:00:00:00:00:01" || p5.PSK != "new-psk-123" || p5.Hub != "hub1" {
		t.Fatalf("after SetWiFi %+v", p5)
	}
	if err := is.SetWiFi(strings.Repeat("x", 33), "", ""); err == nil {
		t.Fatal("invalid SSID accepted")
	}
}

func TestRegistry(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	r, err := OpenRegistry(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	ka, kb, kc := key(t), key(t), key(t)
	a, err := r.Add(ka, control.Hello{RelayName: "anna", Carrier: "Play", Limits: control.Limits{CapMB: 100}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Add(kb, control.Hello{RelayName: "bob"})
	if a.ID != "r1" || a.PathID != 1 || b.ID != "r2" || b.PathID != 2 || a.Limits.CapMB != 100 {
		t.Fatalf("a %+v b %+v", a, b)
	}
	if again, _ := r.Add(ka, control.Hello{RelayName: "other"}); again != a {
		t.Fatalf("re-add changed the relay: %+v", again)
	}
	if got, ok := r.Lookup(kb); !ok || got != b {
		t.Fatalf("lookup %+v %v", got, ok)
	}
	if err := r.Update("r2", "bobby", "Orange", control.Limits{CapMB: 5}); err != nil {
		t.Fatal(err)
	}
	if err := r.Update("r9", "x", "", control.Limits{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update unknown: %v", err)
	}

	// Forget by name; the path id is reused, ids are not.
	f, err := r.Forget("anna")
	if err != nil || f.ID != "r1" {
		t.Fatalf("forget %+v %v", f, err)
	}
	if _, ok := r.Lookup(ka); ok || !r.IsForgotten(ka) {
		t.Fatal("forgotten relay still known or not marked forgotten")
	}
	if _, err := r.Forget("anna"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forget twice: %v", err)
	}
	cc, _ := r.Add(kc, control.Hello{RelayName: "bobby"})
	if cc.ID != "r3" || cc.PathID != 1 {
		t.Fatalf("new relay %+v", cc)
	}
	if _, err := r.Forget("bobby"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("ambiguous name: %v", err)
	}

	// Persisted: reopen and check.
	r2, err := OpenRegistry(dir, c.now)
	if err != nil {
		t.Fatal(err)
	}
	list := r2.List()
	if len(list) != 2 || list[0].ID != "r3" || list[1].Name != "bobby" || list[1].Carrier != "Orange" || !r2.IsForgotten(ka) {
		t.Fatalf("reopened %+v", list)
	}
	// Re-pairing a forgotten key (fresh token) clears the forgotten mark.
	if _, err := r2.Forget("r2"); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Add(ka, control.Hello{RelayName: "anna"}); err != nil || r2.IsForgotten(ka) {
		t.Fatalf("re-pair: %v forgotten=%v", err, r2.IsForgotten(ka))
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, RegistryFile))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode())
		}
	}
}

func TestRegistryFullAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	r, _ := OpenRegistry(dir, nil)
	for i := 0; i < MaxRelays; i++ {
		if _, err := r.Add(key(t), control.Hello{RelayName: "x"}); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := r.Add(key(t), control.Hello{RelayName: "x"}); !errors.Is(err, ErrFull) {
		t.Fatalf("err %v, want ErrFull", err)
	}
	if err := os.WriteFile(filepath.Join(dir, RegistryFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRegistry(dir, nil); err == nil {
		t.Fatal("corrupt registry opened")
	}
	if _, err := OpenRegistry(filepath.Join(dir, "missing", "x"), nil); err != nil {
		t.Fatalf("missing dir should give an empty registry: %v", err)
	}
}
