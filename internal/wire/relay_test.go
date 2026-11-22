package wire

import (
	"bytes"
	"errors"
	"net/netip"
	"reflect"
	"testing"
)

func outerPacket() []byte {
	return append(AppendHeader(nil, Header{Type: TypeData, SessionID: 1, PathID: 2, Counter: 3}), 0xCC, 0xDD)
}

func TestRelayRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		m    RelayMsg
		want []byte
	}{
		{"to server", RelayMsg{Kind: RelayToServer, PathID: 2, Payload: outerPacket()},
			append([]byte{0xB1, 2}, outerPacket()...)},
		{"from server", RelayMsg{Kind: RelayFromSrv, PathID: 9, Payload: outerPacket()},
			append([]byte{0xB2, 9}, outerPacket()...)},
		{"keepalive", RelayMsg{Kind: RelayKeepalive, PathID: 4}, []byte{0xB3, 4}},
		{"udp out", RelayMsg{Kind: RelayUDPOut, FlowID: 0x01020304, Dst: netip.MustParseAddrPort("8.8.8.8:53"), Payload: []byte{0xAB}},
			[]byte{0xB4, 1, 2, 3, 4, 8, 8, 8, 8, 0, 53, 0xAB}},
		{"udp in", RelayMsg{Kind: RelayUDPIn, FlowID: 7, Payload: []byte{0xAB, 0xCD}},
			[]byte{0xB5, 0, 0, 0, 7, 0xAB, 0xCD}},
		{"udp in empty", RelayMsg{Kind: RelayUDPIn, FlowID: 7, Payload: []byte{}},
			[]byte{0xB5, 0, 0, 0, 7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := AppendRelay(nil, tt.m)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(b, tt.want) {
				t.Fatalf("got %x want %x", b, tt.want)
			}
			m, err := ParseRelay(b)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(m, tt.m) {
				t.Fatalf("got %+v want %+v", m, tt.m)
			}
		})
	}
}

func TestAppendRelayErrors(t *testing.T) {
	tests := []struct {
		name string
		m    RelayMsg
		want error
	}{
		{"short outer", RelayMsg{Kind: RelayToServer, Payload: make([]byte, HeaderLen-1)}, ErrShort},
		{"udp out v6", RelayMsg{Kind: RelayUDPOut, Dst: netip.MustParseAddrPort("[::1]:53")}, ErrInvalid},
		{"udp out zero dst", RelayMsg{Kind: RelayUDPOut}, ErrInvalid},
		{"bad kind", RelayMsg{Kind: 0xB6}, ErrType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := AppendRelay([]byte{1}, tt.m)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
			if !bytes.Equal(b, []byte{1}) {
				t.Fatalf("buffer changed on error: %x", b)
			}
		})
	}
}

func TestParseRelayErrors(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrShort},
		{"b1 no packet", []byte{0xB1, 1}, ErrShort},
		{"b2 short packet", append([]byte{0xB2, 1}, make([]byte, HeaderLen-1)...), ErrShort},
		{"b3 short", []byte{0xB3}, ErrLength},
		{"b3 long", []byte{0xB3, 1, 0}, ErrLength},
		{"b4 short", []byte{0xB4, 0, 0, 0, 1, 8, 8, 8, 8, 0}, ErrShort},
		{"b5 short", []byte{0xB5, 0, 0, 1}, ErrShort},
		{"unknown", []byte{0xB0, 0}, ErrType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseRelay(tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
		})
	}
}
