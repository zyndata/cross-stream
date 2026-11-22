package server

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	yamlFile := write("server.yaml", "listen_ports: [51900]\npublic_endpoints: [vpn.example.org]\ntoken_ttl: 2h\n")
	empty := write("empty.yaml", "")
	typo := write("typo.yaml", "listen_port: [1]\n")

	tests := []struct {
		name  string
		path  string
		env   map[string]string
		check func(Config) bool
		ok    bool
	}{
		{"missing file = defaults", filepath.Join(dir, "none.yaml"), nil,
			func(c Config) bool { return reflect.DeepEqual(c, DefaultConfig()) }, true},
		{"empty file = defaults", empty, nil,
			func(c Config) bool { return reflect.DeepEqual(c, DefaultConfig()) }, true},
		{"yaml", yamlFile, nil, func(c Config) bool {
			return reflect.DeepEqual(c.ListenPorts, []int{51900}) && c.PublicEndpoints[0] == "vpn.example.org" && c.TokenTTL == 2*time.Hour
		}, true},
		{"env overrides yaml", yamlFile, map[string]string{
			"XS_SERVER_LISTEN_PORTS":      "443, 8443",
			"XS_SERVER_PUBLIC_ENDPOINTS":  "203.0.113.5,udp://[2001:db8::1]:51900",
			"XS_SERVER_TOKEN_TTL":         "30m",
			"XS_SERVER_MTU":               "1300",
			"XS_SERVER_DATA_DIR":          "/srv/xs",
			"XS_SERVER_LISTEN_ADDRESS":    "0.0.0.0",
			"XS_SERVER_CONTROL_SOCKET":    "/tmp/x.sock",
			"XS_SERVER_TUN_NAME":          "xst0",
			"XS_SERVER_LOG_LEVEL":         "debug",
			"XS_SERVER_UNRELATED_SETTING": "ignored",
		}, func(c Config) bool {
			return reflect.DeepEqual(c.ListenPorts, []int{443, 8443}) && len(c.PublicEndpoints) == 2 &&
				c.TokenTTL == 30*time.Minute && c.MTU == 1300 && c.DataDir == "/srv/xs" &&
				c.ListenAddress == "0.0.0.0" && c.ControlSocket == "/tmp/x.sock" && c.TunName == "xst0" && c.LogLevel == "debug"
		}, true},
		{"unknown yaml key", typo, nil, nil, false},
		{"bad env int", "", map[string]string{"XS_SERVER_MTU": "big"}, nil, false},
		{"bad env list", "", map[string]string{"XS_SERVER_LISTEN_PORTS": "1,x"}, nil, false},
		{"bad env duration", "", map[string]string{"XS_SERVER_TOKEN_TTL": "1 day"}, nil, false},
		{"duplicate port", "", map[string]string{"XS_SERVER_LISTEN_PORTS": "443,443"}, nil, false},
		{"port range", "", map[string]string{"XS_SERVER_LISTEN_PORTS": "70000"}, nil, false},
		{"mtu range", "", map[string]string{"XS_SERVER_MTU": "100"}, nil, false},
		{"pep on by default, off by env", "", map[string]string{"XS_SERVER_PEP": "false"},
			func(c Config) bool { return !c.PEP && DefaultConfig().PEP }, true},
		{"bad env bool", "", map[string]string{"XS_SERVER_PEP": "maybe"}, nil, false},
		{"bad log level", "", map[string]string{"XS_SERVER_LOG_LEVEL": "loud"}, nil, false},
		{"negative ttl", "", map[string]string{"XS_SERVER_TOKEN_TTL": "-1h"}, nil, false},
		{"web panel on loopback by default", "", nil, func(c Config) bool { return c.WebEnabled() && c.WebListen == "127.0.0.1:8443" }, true},
		{"web panel off", "", map[string]string{"XS_SERVER_WEB_LISTEN": "off"}, func(c Config) bool { return !c.WebEnabled() }, true},
		{"web public bind refused", "", map[string]string{"XS_SERVER_WEB_LISTEN": "0.0.0.0:8443"}, nil, false},
		{"web public bind allowed", "", map[string]string{"XS_SERVER_WEB_LISTEN": "0.0.0.0:8443", "XS_SERVER_WEB_ALLOW_PUBLIC": "true"},
			func(c Config) bool { return c.WebAllowPublic }, true},
		{"web cert without key", "", map[string]string{"XS_SERVER_WEB_CERT": "/data/c.pem"}, nil, false},
		{"ingress off by default, HA proxy allowed", "", nil, func(c Config) bool {
			return !c.IngressEnabled() && len(c.IngressAllow()) == 1 && c.IngressAllow()[0].String() == "172.30.32.2/32"
		}, true},
		{"ingress on", "", map[string]string{"XS_SERVER_WEB_INGRESS_LISTEN": "0.0.0.0:8099", "XS_SERVER_WEB_LISTEN": "off"},
			func(c Config) bool { return c.IngressEnabled() && !c.WebEnabled() }, true},
		{"ingress allow from env", "", map[string]string{"XS_SERVER_WEB_INGRESS_LISTEN": ":8099", "XS_SERVER_WEB_INGRESS_ALLOW": "10.9.0.5, 10.8.0.0/16"},
			func(c Config) bool {
				return len(c.IngressAllow()) == 2 && c.IngressAllow()[1].String() == "10.8.0.0/16"
			}, true},
		{"ingress bad allow", "", map[string]string{"XS_SERVER_WEB_INGRESS_ALLOW": "ha-proxy"}, nil, false},
		{"ingress bad listen", "", map[string]string{"XS_SERVER_WEB_INGRESS_LISTEN": "8099"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := LoadConfig(tt.path, func(k string) string { return tt.env[k] })
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && !tt.check(c) {
				t.Fatalf("config %+v", c)
			}
		})
	}
}

func TestEveryConfigKeyHasEnvOverride(t *testing.T) {
	typ := reflect.TypeOf(Config{})
	for i := range typ.NumField() {
		key := typ.Field(i).Tag.Get("yaml")
		if key == "" {
			t.Errorf("field %s has no yaml key", typ.Field(i).Name)
		}
		if err := setField(reflect.New(typ.Field(i).Type).Elem(), "1"); err != nil && !strings.Contains(err.Error(), "not a") {
			t.Errorf("field %s: %v", typ.Field(i).Name, err)
		}
	}
}

func TestEndpoints(t *testing.T) {
	detect := func() (netip.Addr, error) { return netip.MustParseAddr("192.0.2.5"), nil }
	fail := func() (netip.Addr, error) { return netip.Addr{}, errors.New("no route") }
	ports := Ports{UDP: []int{51900, 443}}
	all := Ports{UDP: []int{51900}, TCP: []int{51900}, WSS: []int{443}, WSSPath: "/xs"}
	tests := []struct {
		name   string
		public []string
		detect func() (netip.Addr, error)
		want   []string
		ok     bool
	}{
		{"detected", nil, detect, []string{"udp://192.0.2.5:51900", "udp://192.0.2.5:443"}, true},
		{"detect fails", nil, fail, nil, false},
		{"no detector", nil, nil, nil, false},
		{"bare host", []string{"vpn.example.org"}, nil, []string{"udp://vpn.example.org:51900", "udp://vpn.example.org:443"}, true},
		{"host:port", []string{"203.0.113.9:6000"}, nil, []string{"udp://203.0.113.9:6000"}, true},
		{"ipv6 bare", []string{"2001:db8::1"}, nil, []string{"udp://[2001:db8::1]:51900", "udp://[2001:db8::1]:443"}, true},
		{"url kept", []string{"wss://vpn.example.org/xs"}, nil, []string{"wss://vpn.example.org/xs"}, true},
		{"all transports", []string{"h.example"}, nil, []string{"udp://h.example:51900", "tcp://h.example:51900", "wss://h.example:443/xs"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ports
			if tt.name == "all transports" {
				p = all
			}
			got, err := Endpoints(tt.public, p, tt.detect)
			if (err == nil) != tt.ok || (tt.ok && !reflect.DeepEqual(got, tt.want)) {
				t.Fatalf("Endpoints = %v, %v; want %v ok=%v", got, err, tt.want, tt.ok)
			}
		})
	}
}

func TestNFT(t *testing.T) {
	var calls []string
	n := NFT{TunName: "xss0", Prefix: TunnelPrefix, Run: func(_ context.Context, stdin string, args ...string) error {
		calls = append(calls, strings.Join(args, " ")+"|"+stdin)
		return nil
	}}
	rs := n.Ruleset()
	for _, want := range []string{"table ip crossstream {", "delete table ip crossstream",
		`ip saddr 100.64.0.0/16 oifname != "xss0" masquerade`, "type nat hook postrouting priority srcnat"} {
		if !strings.Contains(rs, want) {
			t.Errorf("ruleset lacks %q:\n%s", want, rs)
		}
	}
	if err := n.Setup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := n.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "-f -|table ip crossstream") || calls[1] != "delete table ip crossstream|" {
		t.Fatalf("nft calls %q", calls)
	}
	n.Run = func(context.Context, string, ...string) error { return errors.New("boom") }
	if n.Setup(context.Background()) == nil || n.Cleanup(context.Background()) == nil {
		t.Fatal("nft errors not returned")
	}
}

func TestNewRequiresDeps(t *testing.T) {
	if _, err := New(Options{Config: DefaultConfig()}); err == nil {
		t.Fatal("New without State/TUN succeeded")
	}
	bad := DefaultConfig()
	bad.MTU = 1
	if _, err := New(Options{Config: bad}); err == nil {
		t.Fatal("New with invalid config succeeded")
	}
}
