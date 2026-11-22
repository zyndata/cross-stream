package wire

import (
	"encoding/binary"
	"fmt"
)

// FrameHeaderLen is the TLV overhead of one frame: type u8 + len u16.
const FrameHeaderLen = 3

// MaxFrameValue is the largest value a frame can carry.
const MaxFrameValue = 0xFFFF

// FrameType is the type byte of a frame inside a DATA payload.
type FrameType uint8

// Frame types (protocol.md section 1, "Frames").
const (
	FrameIP               FrameType = 0x10
	FrameAck              FrameType = 0x11
	FramePing             FrameType = 0x12
	FramePong             FrameType = 0x13
	FramePathConfig       FrameType = 0x14
	FramePathGone         FrameType = 0x15
	FrameClose            FrameType = 0x16
	FrameSessionConfig    FrameType = 0x17
	FrameTestReq          FrameType = 0x18
	FrameTestData         FrameType = 0x19
	FrameTestResult       FrameType = 0x1A
	FrameFwdReq           FrameType = 0x1B
	FrameFwdAck           FrameType = 0x1C
	FramePathInfo         FrameType = 0x1D
	FramePathName         FrameType = 0x1E
	FrameStreamOpen       FrameType = 0x20
	FrameStreamOpenResult FrameType = 0x21
	FrameStreamData       FrameType = 0x22
	FrameStreamWindow     FrameType = 0x23
	FrameStreamClose      FrameType = 0x24
	FrameStreamReset      FrameType = 0x25
)

// Frame is one TLV element of a DATA payload. Byte slices in decoded frames
// alias the input buffer.
type Frame interface {
	FrameType() FrameType
	// appendValue appends the frame value (without TLV header) to b.
	appendValue(b []byte) ([]byte, error)
}

type decodeFunc func(v []byte) (Frame, error)

// decoder returns the value decoder for t, or nil for an unknown type.
func decoder(t FrameType) decodeFunc {
	switch t {
	case FrameIP:
		return decodeIP
	case FrameAck:
		return decodeAck
	case FramePing:
		return decodePing
	case FramePong:
		return decodePong
	case FramePathConfig:
		return decodePathConfig
	case FramePathGone:
		return decodePathGone
	case FrameClose:
		return decodeClose
	case FrameSessionConfig:
		return decodeSessionConfig
	case FrameTestReq:
		return decodeTestReq
	case FrameTestData:
		return decodeTestData
	case FrameTestResult:
		return decodeTestResult
	case FrameFwdReq:
		return decodeFwdReq
	case FrameFwdAck:
		return decodeFwdAck
	case FramePathInfo:
		return decodePathInfo
	case FramePathName:
		return decodePathName
	case FrameStreamOpen:
		return decodeStreamOpen
	case FrameStreamOpenResult:
		return decodeStreamOpenResult
	case FrameStreamData:
		return decodeStreamData
	case FrameStreamWindow:
		return decodeStreamWindow
	case FrameStreamClose:
		return decodeStreamClose
	case FrameStreamReset:
		return decodeStreamReset
	}
	return nil
}

// AppendFrame appends the TLV encoding of f to b. On error b is returned unchanged.
func AppendFrame(b []byte, f Frame) ([]byte, error) {
	start := len(b)
	b = append(b, byte(f.FrameType()), 0, 0)
	out, err := f.appendValue(b)
	if err != nil {
		return b[:start], fmt.Errorf("frame 0x%02x: %w", byte(f.FrameType()), err)
	}
	n := len(out) - start - FrameHeaderLen
	if n > MaxFrameValue {
		return out[:start], fmt.Errorf("frame 0x%02x: value %d bytes: %w", byte(f.FrameType()), n, ErrLength)
	}
	binary.BigEndian.PutUint16(out[start+1:], uint16(n))
	return out, nil
}

// ReadFrame decodes the first frame of b and returns it with the rest of b.
// A frame of unknown type is returned as *Unknown so the caller can skip it.
func ReadFrame(b []byte) (Frame, []byte, error) {
	if len(b) < FrameHeaderLen {
		return nil, nil, fmt.Errorf("frame header: %d bytes: %w", len(b), ErrShort)
	}
	t := FrameType(b[0])
	n := int(binary.BigEndian.Uint16(b[1:3]))
	if len(b)-FrameHeaderLen < n {
		return nil, nil, fmt.Errorf("frame 0x%02x: value %d of %d bytes: %w", b[0], len(b)-FrameHeaderLen, n, ErrShort)
	}
	v := b[FrameHeaderLen : FrameHeaderLen+n : FrameHeaderLen+n]
	rest := b[FrameHeaderLen+n:]
	dec := decoder(t)
	if dec == nil {
		return &Unknown{Type: t, Value: v}, rest, nil
	}
	f, err := dec(v)
	if err != nil {
		return nil, nil, fmt.Errorf("frame 0x%02x: %w", b[0], err)
	}
	return f, rest, nil
}

// ParseFrames decodes every frame of a DATA payload. Unknown frame types
// are returned as *Unknown. Any malformed frame fails the whole payload.
func ParseFrames(payload []byte) ([]Frame, error) {
	var frames []Frame
	for len(payload) > 0 {
		f, rest, err := ReadFrame(payload)
		if err != nil {
			return nil, err
		}
		frames = append(frames, f)
		payload = rest
	}
	return frames, nil
}

// Unknown is a frame of a type this version does not know. Receivers ignore it.
type Unknown struct {
	Type  FrameType
	Value []byte
}

// FrameType returns the raw type byte.
func (f *Unknown) FrameType() FrameType { return f.Type }

func (f *Unknown) appendValue(b []byte) ([]byte, error) {
	if decoder(f.Type) != nil {
		return b, fmt.Errorf("unknown frame with known type: %w", ErrInvalid)
	}
	return append(b, f.Value...), nil
}

// exact checks a fixed-size frame value.
func exact(v []byte, n int) error {
	if len(v) != n {
		return fmt.Errorf("value %d bytes, want %d: %w", len(v), n, ErrLength)
	}
	return nil
}

// atLeast checks a variable-size frame value.
func atLeast(v []byte, n int) error {
	if len(v) < n {
		return fmt.Errorf("value %d bytes, want >= %d: %w", len(v), n, ErrLength)
	}
	return nil
}

// enum checks that a field value is at most maxVal.
func enum(name string, val, maxVal uint8) error {
	if val > maxVal {
		return fmt.Errorf("%s %d (max %d): %w", name, val, maxVal, ErrInvalid)
	}
	return nil
}
