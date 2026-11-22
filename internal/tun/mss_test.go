package tun

import (
	"encoding/binary"
	"testing"
)

// synPacket builds an IPv4 TCP packet with the given flags and options and a
// valid checksum.
func synPacket(flags byte, opts []byte) []byte {
	tcpLen := 20 + len(opts)
	p := make([]byte, 20+tcpLen)
	p[0], p[8], p[9] = 0x45, 64, 6
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	copy(p[12:], []byte{10, 42, 0, 23, 1, 1, 1, 1})
	tcp := p[20:]
	binary.BigEndian.PutUint16(tcp[0:], 40000)
	binary.BigEndian.PutUint16(tcp[2:], 443)
	tcp[12], tcp[13] = byte(tcpLen/4)<<4, flags
	copy(tcp[20:], opts)
	binary.BigEndian.PutUint16(tcp[16:], tcpChecksum(p))
	return p
}

func tcpChecksum(p []byte) uint16 {
	tcp := p[20:]
	sum := uint32(0)
	add := func(b []byte) {
		for len(b) > 1 {
			sum += uint32(binary.BigEndian.Uint16(b))
			b = b[2:]
		}
		if len(b) == 1 {
			sum += uint32(b[0]) << 8
		}
	}
	add(p[12:20])
	sum += 6 + uint32(len(tcp))
	c := binary.BigEndian.Uint16(tcp[16:])
	tcp[16], tcp[17] = 0, 0
	add(tcp)
	binary.BigEndian.PutUint16(tcp[16:], c)
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func TestClampMSS(t *testing.T) {
	mss1460 := []byte{2, 4, 0x05, 0xb4}
	tests := []struct {
		name    string
		flags   byte
		opts    []byte
		max     int32
		changed bool
		want    uint16
	}{
		{"syn lowered", 0x02, append([]byte{1, 1}, append(mss1460, 1, 1)...), 1240, true, 1240},
		{"syn-ack lowered", 0x12, append(mss1460, 4, 2, 1, 1), 1240, true, 1240},
		{"already small", 0x02, mss1460, 1460, false, 1460},
		{"not syn", 0x10, mss1460, 1240, false, 1460},
		{"off", 0x02, mss1460, 0, false, 1460},
		{"no mss option", 0x02, []byte{4, 2, 1, 1}, 1240, false, 0},
		{"bad option length", 0x02, []byte{3, 9, 0, 0}, 1240, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := synPacket(tt.flags, tt.opts)
			if got := clampMSS(p, tt.max); got != tt.changed {
				t.Fatalf("changed = %v, want %v", got, tt.changed)
			}
			if binary.BigEndian.Uint16(p[36:]) != tcpChecksum(p) {
				t.Fatal("TCP checksum wrong after clamp")
			}
			if tt.want == 0 {
				return
			}
			for i := 40; i+4 <= len(p); i++ {
				if p[i] == 2 && p[i+1] == 4 {
					if got := binary.BigEndian.Uint16(p[i+2:]); got != tt.want {
						t.Fatalf("mss = %d, want %d", got, tt.want)
					}
					return
				}
			}
			t.Fatal("mss option lost")
		})
	}
}

func FuzzClampMSS(f *testing.F) {
	f.Add(synPacket(0x02, []byte{2, 4, 0x05, 0xb4}), int32(1240))
	f.Fuzz(func(_ *testing.T, p []byte, maxMSS int32) {
		clampMSS(p, maxMSS) // must not panic
	})
}
