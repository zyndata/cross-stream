package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeSupervisor serves /addons/self/info, /options and /restart.
type fakeSupervisor struct {
	mu       sync.Mutex
	network  map[string]*int
	posted   map[string]any
	restarts int
	fail     bool
}

func (f *fakeSupervisor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"result":"error","message":"bad token"}`)
		return
	}
	if f.fail {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"result":"error","message":"port in use"}`)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /addons/self/info":
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "ok", "data": map[string]any{"network": f.network}})
	case "POST /addons/self/options":
		_ = json.NewDecoder(r.Body).Decode(&f.posted)
		_, _ = io.WriteString(w, `{"result":"ok","data":{}}`)
	case "POST /addons/self/restart":
		f.restarts++
		_, _ = io.WriteString(w, `{"result":"ok","data":{}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func intp(n int) *int { return &n }

func TestSupervisorPort(t *testing.T) {
	f := &fakeSupervisor{network: map[string]*int{"51900/udp": intp(51900), "51900/tcp": intp(51900), "9100/tcp": nil}}
	ts := httptest.NewServer(f)
	defer ts.Close()
	ctx := context.Background()
	sup := SupervisorPort{URL: ts.URL, Token: "tok", Main: 51900}

	if p, err := sup.Configured(ctx); err != nil || p != 51900 {
		t.Fatalf("Configured = %d, %v", p, err)
	}
	if err := sup.Set(ctx, 51901); err != nil {
		t.Fatal(err)
	}
	net, _ := f.posted["network"].(map[string]any)
	if net["51900/udp"] != float64(51901) || net["51900/tcp"] != float64(51901) {
		t.Fatalf("posted network %v", f.posted)
	}
	if v, ok := net["9100/tcp"]; !ok || v != nil {
		t.Fatalf("other mappings must be kept as they are: %v", net)
	}
	if err := sup.Restart(ctx); err != nil || f.restarts != 1 {
		t.Fatalf("restart: %v, %d", err, f.restarts)
	}

	errCases := []struct {
		name string
		sup  SupervisorPort
		prep func()
		want string
	}{
		{"no token", SupervisorPort{URL: ts.URL, Main: 51900}, nil, "SUPERVISOR_TOKEN"},
		{"wrong token", SupervisorPort{URL: ts.URL, Token: "x", Main: 51900}, nil, "bad token"},
		{"port not mapped", sup, func() { f.network["51900/udp"] = nil }, "not mapped"},
		{"supervisor refuses", sup, func() { f.fail = true }, "port in use"},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prep != nil {
				f.mu.Lock()
				tc.prep()
				f.mu.Unlock()
			}
			if _, err := tc.sup.Configured(ctx); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestFilePort(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	tests := []struct {
		name    string
		initial string // "" = no file
		want    []string
	}{
		{"no file", "", []string{"public_port: 51901"}},
		{"key added, comments kept", "# my server\nlisten_ports: [51900, 443] # both\n",
			[]string{"# my server", "# both", "public_port: 51901"}},
		{"key replaced", "public_port: 6000 # router maps it\nmtu: 1360\n",
			[]string{"public_port: 51901 # router maps it", "mtu: 1360"}},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".yaml")
			if tc.initial != "" {
				if err := os.WriteFile(path, []byte(tc.initial), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fp := FilePort{Path: path}
			if err := fp.Set(ctx, 51901); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(path)
			for _, w := range tc.want {
				if !strings.Contains(string(b), w) {
					t.Fatalf("case %d: %q missing in\n%s", i, w, b)
				}
			}
			if p, err := fp.Configured(ctx); err != nil || p != 51901 {
				t.Fatalf("Configured = %d, %v", p, err)
			}
			if _, err := LoadConfig(path, func(string) string { return "" }); err != nil {
				t.Fatalf("the written file must still load: %v", err)
			}
		})
	}
	bad := filepath.Join(dir, "list.yaml")
	_ = os.WriteFile(bad, []byte("- a\n- b\n"), 0o600)
	if err := (FilePort{Path: bad}).Set(ctx, 51901); err == nil {
		t.Fatal("a non-mapping file must be refused")
	}
	if err := (FixedPort{From: "env", Port: 6000}).Set(ctx, 1); !errors.Is(err, ErrPortReadOnly) {
		t.Fatalf("env source: %v", err)
	}
}

func TestPortSourceConfig(t *testing.T) {
	for _, tc := range []struct {
		val string
		ok  bool
	}{{"", true}, {"supervisor", true}, {"consul", false}} {
		_, err := LoadConfig("", func(k string) string {
			if k == "XS_SERVER_PORT_SOURCE" {
				return tc.val
			}
			return ""
		})
		if (err == nil) != tc.ok {
			t.Errorf("port_source %q: err = %v", tc.val, err)
		}
	}
}

func TestCheckTunnelPort(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MetricsListen = "127.0.0.1:9100"
	s := &Server{o: Options{Config: cfg}}
	tests := []struct {
		port int
		ok   bool
	}{
		{51901, true}, {51900, true}, {0, false}, {70000, false},
		{443, false}, {8443, false}, {9100, false},
	}
	for _, tc := range tests {
		if err := s.checkTunnelPort(tc.port); (err == nil) != tc.ok {
			t.Errorf("port %d: err = %v, want ok=%v", tc.port, err, tc.ok)
		}
	}
}
