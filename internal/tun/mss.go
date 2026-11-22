package tun

import (
	"encoding/binary"
	"sync/atomic"
)

// ClampMSS wraps dev so that the MSS option of TCP SYN packets in both
// directions is lowered to *mss (when > 0) — what an nft "tcp option maxseg
// size set" rule does on Linux; used where no such rule exists (Windows).
func ClampMSS(dev Device, mss *atomic.Int32) Device {
	return &mssDevice{Device: dev, mss: mss}
}

type mssDevice struct {
	Device
	mss *atomic.Int32
}

func (m *mssDevice) Read(p []byte) (int, error) {
	n, err := m.Device.Read(p)
	if err == nil {
		clampMSS(p[:n], m.mss.Load())
	}
	return n, err
}

func (m *mssDevice) Write(p []byte) (int, error) {
	clampMSS(p, m.mss.Load())
	return m.Device.Write(p)
}

// clampMSS lowers the MSS option of an IPv4 TCP SYN to max, fixing the TCP
// checksum incrementally (RFC 1624). It reports whether it changed pkt.
func clampMSS(pkt []byte, maxMSS int32) bool {
	if maxMSS <= 0 || len(pkt) < 20 || pkt[0]>>4 != 4 || pkt[9] != 6 {
		return false
	}
	if binary.BigEndian.Uint16(pkt[6:])&0x1fff != 0 { // not the first fragment
		return false
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || len(pkt) < ihl+20 {
		return false
	}
	tcp := pkt[ihl:]
	if tcp[13]&0x02 == 0 { // SYN
		return false
	}
	doff := int(tcp[12]>>4) * 4
	if doff < 20 || len(tcp) < doff {
		return false
	}
	opts := tcp[20:doff]
	for i := 0; i < len(opts); {
		switch kind := opts[i]; kind {
		case 0:
			return false
		case 1:
			i++
			continue
		}
		if i+1 >= len(opts) || opts[i+1] < 2 || i+int(opts[i+1]) > len(opts) {
			return false
		}
		if opts[i] == 2 && opts[i+1] == 4 {
			old := binary.BigEndian.Uint16(opts[i+2:])
			if int32(old) <= maxMSS {
				return false
			}
			nw := uint16(maxMSS)
			binary.BigEndian.PutUint16(opts[i+2:], nw)
			sum := uint32(^binary.BigEndian.Uint16(tcp[16:])) + uint32(^old) + uint32(nw)
			for sum>>16 != 0 {
				sum = sum&0xffff + sum>>16
			}
			binary.BigEndian.PutUint16(tcp[16:], ^uint16(sum))
			return true
		}
		i += int(opts[i+1])
	}
	return false
}
