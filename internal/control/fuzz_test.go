package control

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Fuzz targets (phase 14): control messages arrive from relays, so decoding
// must never panic, and anything accepted must survive a round trip.
// Run one at a time: go test -run=^$ -fuzz=FuzzUnmarshal ./internal/control

func FuzzUnmarshal(f *testing.F) {
	for _, m := range []Message{
		&Hello{RelayName: "Pixel", Carrier: "Orange", Limits: Limits{CapMB: 500, CapPeriod: "day"}},
		&Welcome{RelayID: "r1", PathID: 3, ServerEP: "udp://203.0.113.1:51900", DataPort: DataPort},
		&Reject{Reason: "full"},
		&Stats{BytesUp: 10, BytesDown: 20, Battery: 80, NetType: "LTE"},
		&Leaving{Reason: "user"},
		&Endpoint{ServerEP: "udp://203.0.113.1:51900"},
		&Forget{},
	} {
		b, err := Marshal(m)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Add([]byte(`{"t":"future","x":1}`))
	f.Add([]byte(`{"t":"stats","bytes_up":-1}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := Unmarshal(b)
		if err != nil {
			return
		}
		if _, ok := m.(*Unknown); ok {
			return
		}
		enc, err := Marshal(m)
		if err != nil {
			t.Fatalf("accepted %s does not marshal: %v", m.Type(), err)
		}
		m2, err := Unmarshal(enc)
		if err != nil {
			t.Fatalf("re-decode %s: %v", enc, err)
		}
		enc2, err := Marshal(m2)
		if err != nil || !bytes.Equal(enc, enc2) {
			t.Fatalf("round trip %s != %s (%v)", enc, enc2, err)
		}
	})
}

func FuzzReadFrame(f *testing.F) {
	f.Add(binary.BigEndian.AppendUint32(nil, 3), []byte("abc"))
	f.Add(binary.BigEndian.AppendUint32(nil, MaxFrame+1), []byte{})
	f.Add([]byte{0, 0}, []byte{})
	f.Fuzz(func(t *testing.T, hdr, body []byte) {
		in := append(append([]byte{}, hdr...), body...)
		got, err := readFrame(bytes.NewReader(in), nil)
		if err != nil {
			return
		}
		if len(got) == 0 || len(got) > MaxFrame || len(in) < 4+len(got) {
			t.Fatalf("frame of %d bytes from %d input bytes", len(got), len(in))
		}
		if n := binary.BigEndian.Uint32(in[:4]); int(n) != len(got) || !bytes.Equal(got, in[4:4+n]) {
			t.Fatalf("frame does not match its length prefix %d", n)
		}
	})
}
