package wire

import (
	"errors"
	"testing"
)

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		in   string
		want Endpoint
		str  string
	}{
		{"udp://1.2.3.4:51900", Endpoint{TransportUDP, "1.2.3.4", 51900, ""}, "udp://1.2.3.4:51900"},
		{"1.2.3.4:443", Endpoint{TransportUDP, "1.2.3.4", 443, ""}, "udp://1.2.3.4:443"},
		{"tcp://srv.example:51900/", Endpoint{TransportTCP, "srv.example", 51900, ""}, "tcp://srv.example:51900"},
		{"wss://srv.example/xs", Endpoint{TransportWSS, "srv.example", 443, "/xs"}, "wss://srv.example:443/xs"},
		{"wss://srv.example:8443", Endpoint{TransportWSS, "srv.example", 8443, "/"}, "wss://srv.example:8443/"},
		{"udp://[2001:db8::1]:5", Endpoint{TransportUDP, "2001:db8::1", 5, ""}, "udp://[2001:db8::1]:5"},
	}
	for _, tt := range tests {
		got, err := ParseEndpoint(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseEndpoint(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
			continue
		}
		if got.String() != tt.str {
			t.Errorf("%q String() = %q, want %q", tt.in, got.String(), tt.str)
		}
	}
	for _, bad := range []string{"", "udp://h", "udp://h:0", "udp://h:70000", "tcp://h:1/x", "http://h:1",
		"udp://u:p@h:1", "wss://h/x?q=1", "udp://:5", "wss://h:x/"} {
		if _, err := ParseEndpoint(bad); !errors.Is(err, ErrEndpoint) {
			t.Errorf("ParseEndpoint(%q) err = %v, want ErrEndpoint", bad, err)
		}
	}
}

func TestTransportRank(t *testing.T) {
	if TransportUDP.Rank() >= TransportTCP.Rank() || TransportTCP.Rank() >= TransportWSS.Rank() {
		t.Fatal("rank order")
	}
	if TransportUDP.Stream() || !TransportTCP.Stream() || !TransportWSS.Stream() {
		t.Fatal("stream")
	}
}
