package webui

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

const haProxy = "172.30.32.2:40000"

func newIngressEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	allow, err := ParseAllow([]string{DefaultIngressAllow})
	if err != nil {
		t.Fatal(err)
	}
	e.srv = New(Options{Admin: e.adm, Now: e.clk.now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Deny: []netip.Prefix{netip.MustParsePrefix("100.64.0.0/16")}, Ingress: true, IngressAllow: allow})
	e.h = e.srv.Handler()
	return e
}

func TestIngressRequests(t *testing.T) {
	e := newIngressEnv(t)
	csrf := e.srv.ingress.csrf
	tests := []struct {
		name, method, path, body, token string
		hdr                             []string
		code                            int
		actor                           string // audit actor of a write ("" = no record expected)
	}{
		{"foreign source", "GET", "/api/hubs", "", "", []string{"RemoteAddr", "192.0.2.9:1234"}, 403, ""},
		{"tunnel source", "GET", "/api/hubs", "", "", []string{"RemoteAddr", "100.64.0.5:1234"}, 403, ""},
		{"no cookie read", "GET", "/api/hubs", "", "", []string{"RemoteAddr", haProxy}, 200, ""},
		{"panel page", "GET", "/", "", "", []string{"RemoteAddr", haProxy}, 200, ""},
		{"write without token", "POST", "/api/hubs/revoke", `{"name":"h1"}`, "",
			[]string{"RemoteAddr", haProxy, IngressUserHeader, "alice"}, 403, ""},
		{"write with wrong token", "POST", "/api/hubs/revoke", `{"name":"h1"}`, "nope",
			[]string{"RemoteAddr", haProxy}, 403, ""},
		{"write with token", "POST", "/api/hubs/revoke", `{"name":"h1"}`, csrf,
			[]string{"RemoteAddr", haProxy, IngressUserHeader, "alice", "Origin", "https://ha.example.net"}, 200, "ha:alice"},
		{"write without user", "POST", "/api/hubs/revoke", `{"name":"h1"}`, csrf,
			[]string{"RemoteAddr", haProxy}, 200, "ha:" + IngressUser},
		{"user sanitised", "POST", "/api/hubs/revoke", `{"name":"h1"}`, csrf,
			[]string{"RemoteAddr", haProxy, IngressUserHeader, "bob\"\x1b[31m;x"}, 200, "ha:bob31mx"},
		{"login gone", "POST", "/api/login", `{"user":"alice","password":"correct horse"}`, "",
			[]string{"RemoteAddr", haProxy}, 404, ""},
		{"logout gone", "POST", "/api/logout", `{}`, csrf, []string{"RemoteAddr", haProxy}, 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(e.adm.records)
			w := e.do(tt.method, tt.path, tt.body, "", tt.token, tt.hdr...)
			if w.Code != tt.code {
				t.Fatalf("code %d, want %d: %s", w.Code, tt.code, w.Body)
			}
			if got := w.Header().Get("Content-Security-Policy"); got != IngressCSP {
				t.Fatalf("CSP %q", got)
			}
			if c := w.Result().Cookies(); len(c) != 0 {
				t.Fatalf("cookie set: %v", c)
			}
			if tt.actor == "" {
				if len(e.adm.records) != before {
					t.Fatalf("unexpected audit record %+v", e.adm.last())
				}
				return
			}
			if r := e.adm.last(); r.actor != tt.actor {
				t.Fatalf("audit actor %q, want %q", r.actor, tt.actor)
			}
		})
	}
}

func TestIngressSessionAndHeaders(t *testing.T) {
	e := newIngressEnv(t)
	w := e.do("GET", "/api/session", "", "", "", "RemoteAddr", haProxy, IngressUserHeader, "alice")
	if w.Code != 200 {
		t.Fatalf("session: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if jsonField(t, body, "user") != "alice" || jsonField(t, body, "csrf") != e.srv.ingress.csrf ||
		!strings.Contains(body, `"ingress":true`) || strings.Contains(body, "expires") {
		t.Fatalf("session body %s", body)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options %q", got)
	}
	if strings.Contains(IngressCSP, "unsafe") || !strings.Contains(IngressCSP, "frame-ancestors 'self'") ||
		strings.Replace(IngressCSP, "frame-ancestors 'self'", "frame-ancestors 'none'", 1) != CSP {
		t.Fatalf("IngressCSP must differ from CSP only in frame-ancestors: %q", IngressCSP)
	}
	// The normal panel keeps its login and DENY framing.
	n := newEnv(t)
	if w := n.do("GET", "/api/hubs", "", "", ""); w.Code != http.StatusUnauthorized ||
		w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("normal panel: %d %q", w.Code, w.Header().Get("X-Frame-Options"))
	}
}

func TestParseAllow(t *testing.T) {
	tests := []struct {
		in   []string
		want string
		err  bool
	}{
		{[]string{"172.30.32.2"}, "[172.30.32.2/32]", false},
		{[]string{"10.1.2.3/8", " ::ffff:192.0.2.1 "}, "[10.0.0.0/8 192.0.2.1/32]", false},
		{[]string{"fd00::1"}, "[fd00::1/128]", false},
		{[]string{"nope"}, "", true},
		{[]string{"10.0.0.0/99"}, "", true},
	}
	for _, tt := range tests {
		got, err := ParseAllow(tt.in)
		if (err != nil) != tt.err {
			t.Fatalf("%v: err %v", tt.in, err)
		}
		if err == nil && fmtPrefixes(got) != tt.want {
			t.Fatalf("%v: got %s, want %s", tt.in, fmtPrefixes(got), tt.want)
		}
	}
}

func fmtPrefixes(ps []netip.Prefix) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.String()
	}
	return "[" + strings.Join(s, " ") + "]"
}

// TestServeIngressListener checks the source filter on a real listener: a
// refused client is disconnected without any response.
func TestServeIngressListener(t *testing.T) {
	tests := []struct {
		name  string
		allow string
		ok    bool
	}{
		{"allowed", "127.0.0.1", true},
		{"refused", "192.0.2.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			allow, _ := ParseAllow([]string{tt.allow})
			s := New(Options{Admin: e.adm, Now: e.clk.now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
				Ingress: true, IngressAllow: allow})
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- s.ServeIngress(ctx, ln) }()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Errorf("ServeIngress: %v", err)
				}
			}()
			c, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.WriteString(c, "GET /api/hubs HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"); err != nil && tt.ok {
				t.Fatal(err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			if !tt.ok {
				if err == nil {
					t.Fatalf("refused client got a response: %s", resp.Status)
				}
				return
			}
			if err != nil || resp.StatusCode != 200 {
				t.Fatalf("allowed client: %v %v", resp, err)
			}
		})
	}
}

func TestServeIngressNeedsOption(t *testing.T) {
	s := New(Options{Admin: newFakeAdmin(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := s.ServeIngress(context.Background(), nil); err == nil {
		t.Fatal("ServeIngress without Options.Ingress succeeded")
	}
}
