package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// HeaderLen is the size of the outer packet header. The header is the AEAD
// associated data of a DATA packet.
const HeaderLen = 14

// PacketType is the first byte of an outer packet.
type PacketType uint8

// Outer packet types (protocol.md section 1).
const (
	TypeHandshakeInit PacketType = 0x01
	TypeHandshakeResp PacketType = 0x02
	TypeData          PacketType = 0x03
)

// Decode errors. Every decode error wraps one of these.
var (
	ErrShort   = errors.New("wire: buffer too short")
	ErrLength  = errors.New("wire: bad length")
	ErrInvalid = errors.New("wire: invalid field value")
	ErrType    = errors.New("wire: unknown type")
)

// Header is the outer packet header: type u8, session_id u32, path_id u8, counter u64.
type Header struct {
	Type      PacketType
	SessionID uint32
	PathID    uint8
	Counter   uint64
}

func (t PacketType) known() bool {
	return t >= TypeHandshakeInit && t <= TypeData
}

// AppendHeader appends the 14-byte encoding of h to b.
func AppendHeader(b []byte, h Header) []byte {
	b = append(b, byte(h.Type))
	b = binary.BigEndian.AppendUint32(b, h.SessionID)
	b = append(b, h.PathID)
	return binary.BigEndian.AppendUint64(b, h.Counter)
}

// ParseHeader decodes the outer header of packet and returns it with the
// remaining payload. The payload aliases packet.
func ParseHeader(packet []byte) (Header, []byte, error) {
	if len(packet) < HeaderLen {
		return Header{}, nil, fmt.Errorf("header: %d bytes: %w", len(packet), ErrShort)
	}
	h := Header{
		Type:      PacketType(packet[0]),
		SessionID: binary.BigEndian.Uint32(packet[1:5]),
		PathID:    packet[5],
		Counter:   binary.BigEndian.Uint64(packet[6:14]),
	}
	if !h.Type.known() {
		return Header{}, nil, fmt.Errorf("header: packet type 0x%02x: %w", packet[0], ErrType)
	}
	return h, packet[HeaderLen:], nil
}
