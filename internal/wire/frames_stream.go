package wire

import (
	"encoding/binary"
	"net/netip"
)

// StreamOpen opens a PEP stream to an IPv4 destination (frame 0x20, hub→srv).
type StreamOpen struct {
	StreamID uint32
	Dst      netip.AddrPort // IPv4 only
}

// FrameType returns FrameStreamOpen.
func (*StreamOpen) FrameType() FrameType { return FrameStreamOpen }

func (f *StreamOpen) appendValue(b []byte) ([]byte, error) {
	b = binary.BigEndian.AppendUint32(b, f.StreamID)
	return appendAddrPort4(b, f.Dst)
}

func decodeStreamOpen(v []byte) (Frame, error) {
	if err := exact(v, 10); err != nil {
		return nil, err
	}
	return &StreamOpen{StreamID: binary.BigEndian.Uint32(v), Dst: addrPort4(v[4:])}, nil
}

// StreamOpenResult codes.
const (
	OpenOK          uint8 = 0
	OpenRefused     uint8 = 1
	OpenTimeout     uint8 = 2
	OpenUnreachable uint8 = 3
)

// StreamOpenResult answers StreamOpen (frame 0x21, srv→hub).
type StreamOpenResult struct {
	StreamID uint32
	Code     uint8
}

// FrameType returns FrameStreamOpenResult.
func (*StreamOpenResult) FrameType() FrameType { return FrameStreamOpenResult }

func (f *StreamOpenResult) appendValue(b []byte) ([]byte, error) {
	if err := enum("open code", f.Code, OpenUnreachable); err != nil {
		return b, err
	}
	b = binary.BigEndian.AppendUint32(b, f.StreamID)
	return append(b, f.Code), nil
}

func decodeStreamOpenResult(v []byte) (Frame, error) {
	if err := exact(v, 5); err != nil {
		return nil, err
	}
	f := &StreamOpenResult{StreamID: binary.BigEndian.Uint32(v), Code: v[4]}
	if err := enum("open code", f.Code, OpenUnreachable); err != nil {
		return nil, err
	}
	return f, nil
}

// StreamData carries stream bytes starting at Offset (frame 0x22, both directions).
type StreamData struct {
	StreamID uint32
	Offset   uint64
	Data     []byte
}

// FrameType returns FrameStreamData.
func (*StreamData) FrameType() FrameType { return FrameStreamData }

func (f *StreamData) appendValue(b []byte) ([]byte, error) {
	b = binary.BigEndian.AppendUint32(b, f.StreamID)
	b = binary.BigEndian.AppendUint64(b, f.Offset)
	return append(b, f.Data...), nil
}

func decodeStreamData(v []byte) (Frame, error) {
	if err := atLeast(v, 12); err != nil {
		return nil, err
	}
	return &StreamData{
		StreamID: binary.BigEndian.Uint32(v),
		Offset:   binary.BigEndian.Uint64(v[4:]),
		Data:     v[12:],
	}, nil
}

// StreamWindow lets the peer send bytes up to MaxOffset (frame 0x23).
type StreamWindow struct {
	StreamID  uint32
	MaxOffset uint64
}

// FrameType returns FrameStreamWindow.
func (*StreamWindow) FrameType() FrameType { return FrameStreamWindow }

func (f *StreamWindow) appendValue(b []byte) ([]byte, error) {
	return appendIDOffset(b, f.StreamID, f.MaxOffset), nil
}

func decodeStreamWindow(v []byte) (Frame, error) {
	id, off, err := decodeIDOffset(v)
	if err != nil {
		return nil, err
	}
	return &StreamWindow{StreamID: id, MaxOffset: off}, nil
}

// StreamClose half-closes a stream, like FIN (frame 0x24).
type StreamClose struct {
	StreamID    uint32
	FinalOffset uint64
}

// FrameType returns FrameStreamClose.
func (*StreamClose) FrameType() FrameType { return FrameStreamClose }

func (f *StreamClose) appendValue(b []byte) ([]byte, error) {
	return appendIDOffset(b, f.StreamID, f.FinalOffset), nil
}

func decodeStreamClose(v []byte) (Frame, error) {
	id, off, err := decodeIDOffset(v)
	if err != nil {
		return nil, err
	}
	return &StreamClose{StreamID: id, FinalOffset: off}, nil
}

// StreamReset aborts a stream, like RST (frame 0x25).
type StreamReset struct {
	StreamID uint32
	Code     uint8
}

// FrameType returns FrameStreamReset.
func (*StreamReset) FrameType() FrameType { return FrameStreamReset }

func (f *StreamReset) appendValue(b []byte) ([]byte, error) {
	b = binary.BigEndian.AppendUint32(b, f.StreamID)
	return append(b, f.Code), nil
}

func decodeStreamReset(v []byte) (Frame, error) {
	if err := exact(v, 5); err != nil {
		return nil, err
	}
	return &StreamReset{StreamID: binary.BigEndian.Uint32(v), Code: v[4]}, nil
}

func appendIDOffset(b []byte, id uint32, off uint64) []byte {
	b = binary.BigEndian.AppendUint32(b, id)
	return binary.BigEndian.AppendUint64(b, off)
}

func decodeIDOffset(v []byte) (uint32, uint64, error) {
	if err := exact(v, 12); err != nil {
		return 0, 0, err
	}
	return binary.BigEndian.Uint32(v), binary.BigEndian.Uint64(v[4:]), nil
}
