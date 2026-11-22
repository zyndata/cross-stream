package server

import (
	"reflect"
	"testing"
	"time"
)

func TestPublicPortConfig(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want int
		ok   bool
	}{
		{"default is the listen port", nil, 0, true},
		{"from env", map[string]string{"XS_SERVER_PUBLIC_PORT": "51901"}, 51901, true},
		{"out of range", map[string]string{"XS_SERVER_PUBLIC_PORT": "70000"}, 0, false},
		{"negative", map[string]string{"XS_SERVER_PUBLIC_PORT": "-1"}, 0, false},
		{"not a number", map[string]string{"XS_SERVER_PUBLIC_PORT": "high"}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := LoadConfig("", func(k string) string { return tt.env[k] })
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && c.PublicPort != tt.want {
				t.Fatalf("public_port = %d, want %d", c.PublicPort, tt.want)
			}
		})
	}
}

func TestPortsWithPublic(t *testing.T) {
	all := Ports{UDP: []int{51900, 443}, TCP: []int{51900}, WSS: []int{443}, WSSPath: "/xs"}
	tests := []struct {
		name         string
		main, public int
		want         []string
	}{
		{"unchanged (0)", 51900, 0, []string{"udp://h.example:51900", "udp://h.example:443", "tcp://h.example:51900", "wss://h.example:443/xs"}},
		{"unchanged (same)", 51900, 51900, []string{"udp://h.example:51900", "udp://h.example:443", "tcp://h.example:51900", "wss://h.example:443/xs"}},
		{"main moved, 443 kept", 51900, 51901, []string{"udp://h.example:51901", "udp://h.example:443", "tcp://h.example:51901", "wss://h.example:443/xs"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Endpoints([]string{"h.example"}, all.WithPublic(tt.main, tt.public), nil)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tt.want)
			}
		})
	}
	if all.UDP[0] != 51900 || all.TCP[0] != 51900 {
		t.Fatalf("WithPublic changed its receiver: %+v", all)
	}
}

func TestStateTunnelPort(t *testing.T) {
	s, clk, path := openState(t)
	enrollHub := func(st *State, name string) HubRecord {
		t.Helper()
		tok := newToken(t)
		if err := st.AddToken(name, tok, time.Hour); err != nil {
			t.Fatal(err)
		}
		h, err := st.Enroll(tok, newKey(t))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	// A hub from before records had a port.
	if h := enrollHub(s, "old"); h.Port != 0 {
		t.Fatalf("port before SetTunnelPort = %d", h.Port)
	}
	if stale, err := s.SetTunnelPort(51900); err != nil || len(stale) != 0 {
		t.Fatalf("first start: %v, %v", stale, err)
	}
	if h := enrollHub(s, "new"); h.Port != 51900 {
		t.Fatalf("new hub port = %d", h.Port)
	}

	// Restart with another tunnel port: both hubs must enroll again.
	s2, err := OpenState(path, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := s2.SetTunnelPort(51901)
	if err != nil || !reflect.DeepEqual(stale, []string{"new", "old"}) {
		t.Fatalf("after change: %v, %v", stale, err)
	}
	if _, err := s2.Revoke("old"); err != nil {
		t.Fatal(err)
	}
	if h := enrollHub(s2, "old"); h.Port != 51901 {
		t.Fatalf("re-enrolled port = %d", h.Port)
	}
	if got := s2.Reenroll(51901); !reflect.DeepEqual(got, []string{"new"}) {
		t.Fatalf("Reenroll = %v", got)
	}
	if got := s2.Reenroll(51900); !reflect.DeepEqual(got, []string{"old"}) {
		t.Fatalf("Reenroll(back to 51900) = %v", got)
	}
}
