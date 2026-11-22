package enroll

import (
	"encoding/base64"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

func sampleCode(t *testing.T) Code {
	t.Helper()
	tok, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	var spk [KeyLen]byte
	for i := range spk {
		spk[i] = byte(i + 1)
	}
	return Code{
		Endpoints: []string{"udp://vpn.example.com:51900", "udp://vpn.example.com:443", "tcp://vpn.example.com:51900", "wss://vpn.example.com:443/xs"},
		ServerKey: spk,
		Token:     tok,
		Name:      "laptop-hub",
	}
}

func rawCode(json string) string {
	return Prefix + base64.RawURLEncoding.EncodeToString([]byte(json))
}

func TestCodeRoundTrip(t *testing.T) {
	c := sampleCode(t)
	s, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, Prefix) || strings.ContainsAny(s[len(Prefix):], "+/=") {
		t.Fatalf("not XS1 base64url: %s", s)
	}
	for _, in := range []string{s, "  " + s + "\n", s + "=="} {
		got, err := ParseCode(in)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, c) {
			t.Fatalf("got %+v", got)
		}
	}
}

func TestCodeJSONLayout(t *testing.T) {
	c := sampleCode(t)
	s, _ := c.Encode()
	raw, err := base64.RawURLEncoding.DecodeString(s[len(Prefix):])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"v":1`, `"ep":[`, `"spk":"`, `"tok":"` + c.Token.String() + `"`, `"name":"laptop-hub"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("%s missing in %s", key, raw)
		}
	}
}

func TestParseCodeErrors(t *testing.T) {
	spk := base64.StdEncoding.EncodeToString(make([]byte, 31))
	good := `"spk":"AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=","tok":"AAECAwQFBgcICQoLDA0ODw=="`
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"no prefix", "XS2.abc"},
		{"bad base64", Prefix + "!!!"},
		{"not json", rawCode("hello")},
		{"trailing json", rawCode(`{"v":1} {}`)},
		{"version 2", rawCode(`{"v":2,"ep":["udp://h:1"],` + good + `,"name":"a"}`)},
		{"no endpoints", rawCode(`{"v":1,"ep":[],` + good + `,"name":"a"}`)},
		{"bad scheme", rawCode(`{"v":1,"ep":["http://h:1"],` + good + `,"name":"a"}`)},
		{"udp no port", rawCode(`{"v":1,"ep":["udp://h"],` + good + `,"name":"a"}`)},
		{"udp with path", rawCode(`{"v":1,"ep":["udp://h:1/x"],` + good + `,"name":"a"}`)},
		{"wss no host", rawCode(`{"v":1,"ep":["wss:///xs"],` + good + `,"name":"a"}`)},
		{"credentials", rawCode(`{"v":1,"ep":["tcp://u:p@h:1"],` + good + `,"name":"a"}`)},
		{"unparsable url", rawCode(`{"v":1,"ep":["udp://h:1:%zz"],` + good + `,"name":"a"}`)},
		{"short spk", rawCode(`{"v":1,"ep":["udp://h:1"],"spk":"` + spk + `","tok":"AAECAwQFBgcICQoLDA0ODw==","name":"a"}`)},
		{"zero spk", rawCode(`{"v":1,"ep":["udp://h:1"],"spk":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","tok":"AAECAwQFBgcICQoLDA0ODw==","name":"a"}`)},
		{"bad tok", rawCode(`{"v":1,"ep":["udp://h:1"],"spk":"AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=","tok":"AAEC","name":"a"}`)},
		{"zero tok", rawCode(`{"v":1,"ep":["udp://h:1"],"spk":"AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=","tok":"AAAAAAAAAAAAAAAAAAAAAA==","name":"a"}`)},
		{"no name", rawCode(`{"v":1,"ep":["udp://h:1"],` + good + `,"name":""}`)},
		{"control char name", rawCode(`{"v":1,"ep":["udp://h:1"],` + good + `,"name":"a\u0007"}`)},
		{"too long", Prefix + strings.Repeat("A", MaxCodeLen)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCode(tt.in); !errors.Is(err, ErrCode) {
				t.Fatalf("err %v, want ErrCode", err)
			}
		})
	}
}

func TestEncodeCodeErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Code)
	}{
		{"too many endpoints", func(c *Code) { c.Endpoints = make([]string, MaxEndpoints+1) }},
		{"long name", func(c *Code) { c.Name = strings.Repeat("n", MaxNameLen+1) }},
		{"zero key", func(c *Code) { c.ServerKey = [KeyLen]byte{} }},
		{"code too long", func(c *Code) {
			c.Endpoints = nil
			for range MaxEndpoints {
				c.Endpoints = append(c.Endpoints, "wss://h:443/"+strings.Repeat("p", 300))
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := sampleCode(t)
			tt.mutate(&c)
			if _, err := c.Encode(); !errors.Is(err, ErrCode) {
				t.Fatalf("err %v, want ErrCode", err)
			}
		})
	}
}

func TestToken(t *testing.T) {
	a, _ := NewToken()
	b, _ := NewToken()
	if a == b {
		t.Fatal("two tokens are equal")
	}
	got, err := ParseToken(a.String())
	if err != nil || got != a {
		t.Fatalf("parse: %v", err)
	}
	if _, err := ParseToken("AAEC"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("short token: %v", err)
	}
	if _, err := newToken(iotest.ErrReader(errors.New("no entropy"))); err == nil {
		t.Fatal("want rand error")
	}
	if h := a.Hash(); h == b.Hash() || h != Token(a).Hash() {
		t.Fatal("hash not deterministic or collides")
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestMemoryStore(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tests := []struct {
		name    string
		advance time.Duration
		reuse   bool
		want    error
	}{
		{"fresh", 0, false, nil},
		{"just before expiry", DefaultTTL - time.Second, false, nil},
		{"at expiry", DefaultTTL, false, ErrTokenExpired},
		{"used twice", 0, true, ErrTokenInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk.t = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			s := NewMemoryStore(clk.now)
			tok, _ := NewToken()
			if err := s.Add("hub-a", tok, DefaultTTL); err != nil {
				t.Fatal(err)
			}
			clk.t = clk.t.Add(tt.advance)
			if tt.reuse {
				if _, err := s.Consume(tok); err != nil {
					t.Fatal(err)
				}
			}
			name, err := s.Consume(tok)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
			if err == nil && name != "hub-a" {
				t.Fatalf("name %q", name)
			}
			// Whatever happened, the token is gone now.
			if _, err := s.Consume(tok); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("after consume: %v", err)
			}
		})
	}
}

func TestMemoryStoreAddErrors(t *testing.T) {
	s := NewMemoryStore(nil)
	tok, _ := NewToken()
	if err := s.Add("a", tok, 0); err == nil {
		t.Fatal("zero ttl accepted")
	}
	for _, bad := range []string{"", "-h", "--help"} {
		if err := s.Add(bad, tok, time.Hour); !errors.Is(err, ErrCode) {
			t.Fatalf("name %q: %v", bad, err)
		}
	}
	if err := s.Add("a", tok, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("b", tok, time.Hour); !errors.Is(err, ErrTokenExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := s.Consume(Token{1}); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestMemoryStoreStoresOnlyHashes(t *testing.T) {
	s := NewMemoryStore(nil)
	tok, _ := NewToken()
	_ = s.Add("a", tok, time.Hour)
	for h, rec := range s.recs {
		if h != tok.Hash() || rec.Hash != tok.Hash() {
			t.Fatal("record not keyed by hash")
		}
		if strings.Contains(string(h[:]), string(tok[:])) {
			t.Fatal("raw token in store")
		}
	}
}

func TestMemoryStorePrune(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s := NewMemoryStore(clk.now)
	short, _ := NewToken()
	long, _ := NewToken()
	_ = s.Add("short", short, time.Minute)
	_ = s.Add("long", long, time.Hour)
	if n := s.Prune(); n != 0 {
		t.Fatalf("pruned %d", n)
	}
	clk.t = clk.t.Add(2 * time.Minute)
	if n := s.Prune(); n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	if name, err := s.Consume(long); err != nil || name != "long" {
		t.Fatalf("long token: %q %v", name, err)
	}
}

func TestMemoryStoreConcurrentSingleUse(t *testing.T) {
	s := NewMemoryStore(nil)
	tok, _ := NewToken()
	_ = s.Add("a", tok, time.Hour)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Consume(tok); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("token consumed %d times", wins)
	}
}

var _ TokenStore = (*MemoryStore)(nil)

func TestParseCodeLegacyEndpoints(t *testing.T) {
	good := `"spk":"AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=","tok":"AAECAwQFBgcICQoLDA0ODw=="`
	c, err := ParseCode(rawCode(`{"v":1,"ep":["203.0.113.5:51900","wss://h.example/xs"],` + good + `,"name":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"udp://203.0.113.5:51900", "wss://h.example:443/xs"}
	if !slices.Equal(c.Endpoints, want) {
		t.Fatalf("endpoints %q, want %q", c.Endpoints, want)
	}
}
