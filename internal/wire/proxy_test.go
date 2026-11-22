package wire

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"
)

func TestProxyRequestRoundTrip(t *testing.T) {
	var tok [ProxyTokenLen]byte
	for i := range tok {
		tok[i] = byte(i)
	}
	r := ProxyRequest{Token: tok, Dst: netip.MustParseAddrPort("1.2.3.4:443")}
	b, err := AppendProxyRequest([]byte{0xEE}, r)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{0xEE}, tok[:]...), 1, 1, 2, 3, 4, 0x01, 0xBB)
	if !bytes.Equal(b, want) {
		t.Fatalf("encoding % x, want % x", b, want)
	}
	got, err := ParseProxyRequest(b[1:])
	if err != nil || got != r {
		t.Fatalf("parse = %+v, %v; want %+v", got, err, r)
	}
}

func TestProxyRequestErrors(t *testing.T) {
	if b, err := AppendProxyRequest([]byte{1}, ProxyRequest{Dst: netip.MustParseAddrPort("[::1]:80")}); !errors.Is(err, ErrInvalid) || len(b) != 1 {
		t.Fatalf("IPv6 dst: %v, len %d", err, len(b))
	}
	ok, _ := AppendProxyRequest(nil, ProxyRequest{Dst: netip.MustParseAddrPort("1.2.3.4:80")})
	v6 := append([]byte(nil), ok...)
	v6[ProxyTokenLen] = 4
	tests := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrShort},
		{"token only", ok[:ProxyTokenLen], ErrShort},
		{"short addr", ok[:ProxyRequestLen-1], ErrLength},
		{"long", append(append([]byte(nil), ok...), 0), ErrLength},
		{"atyp", v6, ErrType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseProxyRequest(tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
