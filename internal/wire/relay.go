package wire

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// RelayKind is the first byte of a hub↔relay data-channel datagram
// (protocol.md section 3, "Data channel" and "Proxy role").
type RelayKind uint8

// Relay data-channel kinds.
const (
	RelayToServer  RelayKind = 0xB1 // hub→relay: [B1][path_id][outer packet]
	RelayFromSrv   RelayKind = 0xB2 // relay→hub: [B2][path_id][outer packet]
	RelayKeepalive RelayKind = 0xB3 // relay→hub: [B3][path_id]
	RelayUDPOut    RelayKind = 0xB4 // hub→relay: [B4][flow_id u32][ipv4 u32][port u16][payload]
	RelayUDPIn     RelayKind = 0xB5 // relay→hub: [B5][flow_id u32][payload]
)

// Header sizes of the relay datagram kinds.
const (
	RelayPathHeaderLen   = 2
	RelayUDPOutHeaderLen = 11
	RelayUDPInHeaderLen  = 5
)

// RelayMsg is one hub↔relay data-channel datagram. Which fields are used
// depends on Kind: PathID for B1–B3, FlowID for B4/B5, Dst for B4,
// Payload for every kind except B3. Payload aliases the decoded buffer.
type RelayMsg struct {
	Kind    RelayKind
	PathID  uint8
	FlowID  uint32
	Dst     netip.AddrPort
	Payload []byte
}

// AppendRelay appends the encoding of m to b. On error b is returned unchanged.
func AppendRelay(b []byte, m RelayMsg) ([]byte, error) {
	start := len(b)
	switch m.Kind {
	case RelayToServer, RelayFromSrv:
		if len(m.Payload) < HeaderLen {
			return b, fmt.Errorf("relay 0x%02x: outer packet %d bytes: %w", byte(m.Kind), len(m.Payload), ErrShort)
		}
		b = append(b, byte(m.Kind), m.PathID)
		return append(b, m.Payload...), nil
	case RelayKeepalive:
		return append(b, byte(m.Kind), m.PathID), nil
	case RelayUDPOut:
		b = append(b, byte(m.Kind))
		b = binary.BigEndian.AppendUint32(b, m.FlowID)
		var err error
		if b, err = appendAddrPort4(b, m.Dst); err != nil {
			return b[:start], fmt.Errorf("relay 0x%02x: %w", byte(m.Kind), err)
		}
		return append(b, m.Payload...), nil
	case RelayUDPIn:
		b = append(b, byte(m.Kind))
		b = binary.BigEndian.AppendUint32(b, m.FlowID)
		return append(b, m.Payload...), nil
	}
	return b, fmt.Errorf("relay kind 0x%02x: %w", byte(m.Kind), ErrType)
}

// ParseRelay decodes one hub↔relay data-channel datagram.
func ParseRelay(d []byte) (RelayMsg, error) {
	if len(d) < 1 {
		return RelayMsg{}, fmt.Errorf("relay: empty datagram: %w", ErrShort)
	}
	m := RelayMsg{Kind: RelayKind(d[0])}
	switch m.Kind {
	case RelayToServer, RelayFromSrv:
		if len(d) < RelayPathHeaderLen+HeaderLen {
			return RelayMsg{}, fmt.Errorf("relay 0x%02x: %d bytes: %w", d[0], len(d), ErrShort)
		}
		m.PathID, m.Payload = d[1], d[RelayPathHeaderLen:]
	case RelayKeepalive:
		if len(d) != RelayPathHeaderLen {
			return RelayMsg{}, fmt.Errorf("relay keepalive: %d bytes: %w", len(d), ErrLength)
		}
		m.PathID = d[1]
	case RelayUDPOut:
		if len(d) < RelayUDPOutHeaderLen {
			return RelayMsg{}, fmt.Errorf("relay udp out: %d bytes: %w", len(d), ErrShort)
		}
		m.FlowID = binary.BigEndian.Uint32(d[1:])
		m.Dst = addrPort4(d[5:])
		m.Payload = d[RelayUDPOutHeaderLen:]
	case RelayUDPIn:
		if len(d) < RelayUDPInHeaderLen {
			return RelayMsg{}, fmt.Errorf("relay udp in: %d bytes: %w", len(d), ErrShort)
		}
		m.FlowID = binary.BigEndian.Uint32(d[1:])
		m.Payload = d[RelayUDPInHeaderLen:]
	default:
		return RelayMsg{}, fmt.Errorf("relay kind 0x%02x: %w", d[0], ErrType)
	}
	return m, nil
}
