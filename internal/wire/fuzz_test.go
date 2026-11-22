package wire

import (
	"bytes"
	"testing"
)

// Fuzz targets: decoding never panics, and anything that decodes re-encodes
// to the same bytes. Run one at a time: go test -run=^$ -fuzz=FuzzParseFrames ./internal/wire

func FuzzParseHeader(f *testing.F) {
	f.Add(AppendHeader(nil, Header{Type: TypeData, SessionID: 1, PathID: 2, Counter: 3}))
	f.Add([]byte{1})
	f.Fuzz(func(t *testing.T, b []byte) {
		h, payload, err := ParseHeader(b)
		if err != nil {
			return
		}
		if got := append(AppendHeader(nil, h), payload...); !bytes.Equal(got, b) {
			t.Fatalf("re-encode %x != %x", got, b)
		}
	})
}

func FuzzParseFrames(f *testing.F) {
	var all []byte
	for _, fr := range allFrames() {
		one, err := AppendFrame(nil, fr)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(one)
		all = append(all, one...)
	}
	f.Add(all)
	f.Fuzz(func(t *testing.T, b []byte) {
		frames, err := ParseFrames(b)
		if err != nil {
			return
		}
		var out []byte
		for _, fr := range frames {
			if out, err = AppendFrame(out, fr); err != nil {
				t.Fatalf("re-encode %T: %v", fr, err)
			}
		}
		if !bytes.Equal(out, b) {
			t.Fatalf("re-encode %x != %x", out, b)
		}
	})
}

func FuzzParseRelay(f *testing.F) {
	f.Add(append([]byte{0xB1, 1}, outerPacket()...))
	f.Add([]byte{0xB3, 1})
	f.Add([]byte{0xB4, 0, 0, 0, 1, 8, 8, 8, 8, 0, 53, 1})
	f.Add([]byte{0xB5, 0, 0, 0, 1, 2})
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := ParseRelay(b)
		if err != nil {
			return
		}
		out, err := AppendRelay(nil, m)
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		if !bytes.Equal(out, b) {
			t.Fatalf("re-encode %x != %x", out, b)
		}
	})
}

func FuzzParseEndpoint(f *testing.F) {
	for _, s := range []string{"udp://1.2.3.4:51900", "tcp://h.example:51900", "wss://h.example/xs",
		"wss://[2001:db8::1]:8443/x", "10.0.0.1:443", "udp://h"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		e, err := ParseEndpoint(s)
		if err != nil {
			return
		}
		again, err := ParseEndpoint(e.String())
		if err != nil || again != e {
			t.Fatalf("%q → %q → %+v, %v", s, e.String(), again, err)
		}
	})
}
