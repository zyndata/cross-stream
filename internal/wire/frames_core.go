package wire

import (
	"encoding/binary"
	"fmt"
)

// IP carries one tunnelled IPv4 packet (frame 0x10).
type IP struct {
	Seq    uint32
	Packet []byte
}

// FrameType returns FrameIP.
func (*IP) FrameType() FrameType { return FrameIP }

func (f *IP) appendValue(b []byte) ([]byte, error) {
	b = binary.BigEndian.AppendUint32(b, f.Seq)
	return append(b, f.Packet...), nil
}

func decodeIP(v []byte) (Frame, error) {
	if err := atLeast(v, 4); err != nil {
		return nil, err
	}
	return &IP{Seq: binary.BigEndian.Uint32(v), Packet: v[4:]}, nil
}

// Ack acknowledges IP sequence numbers (frame 0x11): bit i of Bitmap means
// Largest-1-i was received.
type Ack struct {
	Largest uint32
	Bitmap  uint64
}

// FrameType returns FrameAck.
func (*Ack) FrameType() FrameType { return FrameAck }

func (f *Ack) appendValue(b []byte) ([]byte, error) {
	b = binary.BigEndian.AppendUint32(b, f.Largest)
	return binary.BigEndian.AppendUint64(b, f.Bitmap), nil
}

func decodeAck(v []byte) (Frame, error) {
	if err := exact(v, 12); err != nil {
		return nil, err
	}
	return &Ack{Largest: binary.BigEndian.Uint32(v), Bitmap: binary.BigEndian.Uint64(v[4:])}, nil
}

// Ping is a liveness/RTT probe (frame 0x12). Padding zero bytes follow the
// timestamp when the ping doubles as a path MTU probe.
type Ping struct {
	TS      uint64 // sender monotonic µs
	Padding int
}

// FrameType returns FramePing.
func (*Ping) FrameType() FrameType { return FramePing }

func (f *Ping) appendValue(b []byte) ([]byte, error) {
	if f.Padding < 0 || f.Padding > MaxFrameValue-8 {
		return b, fmt.Errorf("ping padding %d: %w", f.Padding, ErrLength)
	}
	b = binary.BigEndian.AppendUint64(b, f.TS)
	b = append(b, make([]byte, f.Padding)...)
	return b, nil
}

func decodePing(v []byte) (Frame, error) {
	if err := atLeast(v, 8); err != nil {
		return nil, err
	}
	for _, c := range v[8:] {
		if c != 0 {
			return nil, fmt.Errorf("ping padding not zero: %w", ErrInvalid)
		}
	}
	return &Ping{TS: binary.BigEndian.Uint64(v), Padding: len(v) - 8}, nil
}

// Pong answers a Ping (frame 0x13). ProbeLen is the size of the received PING datagram.
type Pong struct {
	TS       uint64
	ProbeLen uint16
}

// FrameType returns FramePong.
func (*Pong) FrameType() FrameType { return FramePong }

func (f *Pong) appendValue(b []byte) ([]byte, error) {
	b = binary.BigEndian.AppendUint64(b, f.TS)
	return binary.BigEndian.AppendUint16(b, f.ProbeLen), nil
}

func decodePong(v []byte) (Frame, error) {
	if err := exact(v, 10); err != nil {
		return nil, err
	}
	return &Pong{TS: binary.BigEndian.Uint64(v), ProbeLen: binary.BigEndian.Uint16(v[8:])}, nil
}

// Close reasons. Decoding accepts any value (newer peers may add reasons).
const (
	CloseNormal   uint8 = 0
	CloseRevoked  uint8 = 1 // hub enrollment revoked by the server admin
	CloseShutdown uint8 = 2 // server is stopping
)

// Close ends the session (frame 0x16).
type Close struct {
	Reason uint8
}

// FrameType returns FrameClose.
func (*Close) FrameType() FrameType { return FrameClose }

func (f *Close) appendValue(b []byte) ([]byte, error) { return append(b, f.Reason), nil }

func decodeClose(v []byte) (Frame, error) {
	if err := exact(v, 1); err != nil {
		return nil, err
	}
	return &Close{Reason: v[0]}, nil
}

// Redundancy modes of SessionConfig.
const (
	RedundancyOff  uint8 = 0
	RedundancyAuto uint8 = 1
	RedundancyOn   uint8 = 2
)

// SessionConfig sets session-wide options (frame 0x17, hub→srv).
type SessionConfig struct {
	Redundancy   uint8
	OverflowMbps uint16 // secondary paths stay unused below this normal-path rate; 0 = off
	Mode         uint8  // ModeMaxThroughput or ModeMinLatency
}

// Hub modes of SessionConfig.
const (
	ModeMaxThroughput uint8 = 0
	ModeMinLatency    uint8 = 1
)

// FrameType returns FrameSessionConfig.
func (*SessionConfig) FrameType() FrameType { return FrameSessionConfig }

func (f *SessionConfig) appendValue(b []byte) ([]byte, error) {
	if err := enum("redundancy", f.Redundancy, RedundancyOn); err != nil {
		return b, err
	}
	if err := enum("mode", f.Mode, ModeMinLatency); err != nil {
		return b, err
	}
	b = append(b, f.Redundancy)
	b = binary.BigEndian.AppendUint16(b, f.OverflowMbps)
	return append(b, f.Mode), nil
}

func decodeSessionConfig(v []byte) (Frame, error) {
	if err := exact(v, 4); err != nil {
		return nil, err
	}
	f := &SessionConfig{Redundancy: v[0], OverflowMbps: binary.BigEndian.Uint16(v[1:]), Mode: v[3]}
	if err := enum("redundancy", f.Redundancy, RedundancyOn); err != nil {
		return nil, err
	}
	if err := enum("mode", f.Mode, ModeMinLatency); err != nil {
		return nil, err
	}
	return f, nil
}
