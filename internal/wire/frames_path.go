package wire

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"unicode"
	"unicode/utf8"
)

// Path priorities of PathConfig.
const (
	PriorityNormal    uint8 = 0
	PriorityBackup    uint8 = 1
	PrioritySecondary uint8 = 2
)

// Direction modes of PathConfig (per direction).
const (
	DirOn         uint8 = 0
	DirBackupOnly uint8 = 1
	DirOff        uint8 = 2
)

// PathConfig sets per-path policy (frame 0x14, hub→srv). Zero rate/cap means unlimited.
type PathConfig struct {
	PathID   uint8
	Priority uint8
	RateKbps uint32
	CapMB    uint32
	DirUp    uint8
	DirDown  uint8
}

// FrameType returns FramePathConfig.
func (*PathConfig) FrameType() FrameType { return FramePathConfig }

func (f *PathConfig) validate() error {
	if err := enum("priority", f.Priority, PrioritySecondary); err != nil {
		return err
	}
	if err := enum("dir_up", f.DirUp, DirOff); err != nil {
		return err
	}
	return enum("dir_down", f.DirDown, DirOff)
}

func (f *PathConfig) appendValue(b []byte) ([]byte, error) {
	if err := f.validate(); err != nil {
		return b, err
	}
	b = append(b, f.PathID, f.Priority)
	b = binary.BigEndian.AppendUint32(b, f.RateKbps)
	b = binary.BigEndian.AppendUint32(b, f.CapMB)
	return append(b, f.DirUp, f.DirDown), nil
}

func decodePathConfig(v []byte) (Frame, error) {
	if err := exact(v, 12); err != nil {
		return nil, err
	}
	f := &PathConfig{
		PathID:   v[0],
		Priority: v[1],
		RateKbps: binary.BigEndian.Uint32(v[2:]),
		CapMB:    binary.BigEndian.Uint32(v[6:]),
		DirUp:    v[10],
		DirDown:  v[11],
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return f, nil
}

// PathGone tells the server a relay left (frame 0x15, hub→srv).
type PathGone struct {
	PathID uint8
}

// FrameType returns FramePathGone.
func (*PathGone) FrameType() FrameType { return FramePathGone }

func (f *PathGone) appendValue(b []byte) ([]byte, error) { return append(b, f.PathID), nil }

func decodePathGone(v []byte) (Frame, error) {
	if err := exact(v, 1); err != nil {
		return nil, err
	}
	return &PathGone{PathID: v[0]}, nil
}

// PathInfo reports the address the server sees for a path (frame 0x1D, srv→hub).
type PathInfo struct {
	PathID   uint8
	Observed netip.AddrPort // IPv4 only
}

// FrameType returns FramePathInfo.
func (*PathInfo) FrameType() FrameType { return FramePathInfo }

func (f *PathInfo) appendValue(b []byte) ([]byte, error) {
	b = append(b, f.PathID)
	return appendAddrPort4(b, f.Observed)
}

func decodePathInfo(v []byte) (Frame, error) {
	if err := exact(v, 7); err != nil {
		return nil, err
	}
	return &PathInfo{PathID: v[0], Observed: addrPort4(v[1:])}, nil
}

// MaxPathName is the longest path name in PATH_NAME, in bytes.
const MaxPathName = 32

// PathName tells the server a path's display name (frame 0x1E, hub to
// server: the relay or local link name, shown in the server web panel).
type PathName struct {
	PathID uint8
	Name   string // valid UTF-8, printable, at most MaxPathName bytes
}

// FrameType returns FramePathName.
func (*PathName) FrameType() FrameType { return FramePathName }

func validPathName(n string) error {
	if len(n) > MaxPathName || !utf8.ValidString(n) {
		return fmt.Errorf("path name: %w", ErrInvalid)
	}
	for _, r := range n {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("path name: non-printable character: %w", ErrInvalid)
		}
	}
	return nil
}

func (f *PathName) appendValue(b []byte) ([]byte, error) {
	if err := validPathName(f.Name); err != nil {
		return b, err
	}
	return append(append(b, f.PathID), f.Name...), nil
}

func decodePathName(v []byte) (Frame, error) {
	if err := atLeast(v, 1); err != nil {
		return nil, err
	}
	n := string(v[1:])
	if err := validPathName(n); err != nil {
		return nil, err
	}
	return &PathName{PathID: v[0], Name: n}, nil
}

// TruncPathName drops non-printable characters and shortens name to
// MaxPathName bytes on a rune boundary.
func TruncPathName(name string) string {
	out := make([]rune, 0, len(name))
	size := 0
	for _, r := range name {
		if !unicode.IsPrint(r) {
			continue
		}
		if size+utf8.RuneLen(r) > MaxPathName {
			break
		}
		size += utf8.RuneLen(r)
		out = append(out, r)
	}
	return string(out)
}

// Speed test directions of TestReq.
const (
	TestDown uint8 = 0
	TestUp   uint8 = 1
)

// TestAllPaths in TestReq.PathID selects every path.
const TestAllPaths uint8 = 0xFF

// TestReq starts a speed test (frame 0x18, hub→srv).
type TestReq struct {
	TestID     uint16
	PathID     uint8
	Dir        uint8
	DurationMS uint16
	MaxMB      uint16
}

// FrameType returns FrameTestReq.
func (*TestReq) FrameType() FrameType { return FrameTestReq }

func (f *TestReq) appendValue(b []byte) ([]byte, error) {
	if err := enum("dir", f.Dir, TestUp); err != nil {
		return b, err
	}
	b = binary.BigEndian.AppendUint16(b, f.TestID)
	b = append(b, f.PathID, f.Dir)
	b = binary.BigEndian.AppendUint16(b, f.DurationMS)
	return binary.BigEndian.AppendUint16(b, f.MaxMB), nil
}

func decodeTestReq(v []byte) (Frame, error) {
	if err := exact(v, 8); err != nil {
		return nil, err
	}
	f := &TestReq{
		TestID:     binary.BigEndian.Uint16(v),
		PathID:     v[2],
		Dir:        v[3],
		DurationMS: binary.BigEndian.Uint16(v[4:]),
		MaxMB:      binary.BigEndian.Uint16(v[6:]),
	}
	if err := enum("dir", f.Dir, TestUp); err != nil {
		return nil, err
	}
	return f, nil
}

// TestData is speed test filler (frame 0x19). The receiver counts and drops it.
type TestData struct {
	TestID uint16
	Filler []byte
}

// FrameType returns FrameTestData.
func (*TestData) FrameType() FrameType { return FrameTestData }

func (f *TestData) appendValue(b []byte) ([]byte, error) {
	b = binary.BigEndian.AppendUint16(b, f.TestID)
	return append(b, f.Filler...), nil
}

func decodeTestData(v []byte) (Frame, error) {
	if err := atLeast(v, 2); err != nil {
		return nil, err
	}
	return &TestData{TestID: binary.BigEndian.Uint16(v), Filler: v[2:]}, nil
}

// TestResult reports what one path received (frame 0x1A, receiver→sender).
type TestResult struct {
	TestID     uint16
	PathID     uint8
	Bytes      uint64
	DurationUS uint32
}

// FrameType returns FrameTestResult.
func (*TestResult) FrameType() FrameType { return FrameTestResult }

func (f *TestResult) appendValue(b []byte) ([]byte, error) {
	b = binary.BigEndian.AppendUint16(b, f.TestID)
	b = append(b, f.PathID)
	b = binary.BigEndian.AppendUint64(b, f.Bytes)
	return binary.BigEndian.AppendUint32(b, f.DurationUS), nil
}

func decodeTestResult(v []byte) (Frame, error) {
	if err := exact(v, 15); err != nil {
		return nil, err
	}
	return &TestResult{
		TestID:     binary.BigEndian.Uint16(v),
		PathID:     v[2],
		Bytes:      binary.BigEndian.Uint64(v[3:]),
		DurationUS: binary.BigEndian.Uint32(v[11:]),
	}, nil
}

// Port forward protocols.
const (
	ProtoTCP uint8 = 6
	ProtoUDP uint8 = 17
)

// MaxForwards is the largest number of entries in FwdReq/FwdAck.
const MaxForwards = 0xFF

// Forward is one port forward entry.
type Forward struct {
	Proto uint8
	Port  uint16
}

// FwdReq is the full desired port forward list (frame 0x1B, hub→srv).
type FwdReq struct {
	Forwards []Forward
}

// FrameType returns FrameFwdReq.
func (*FwdReq) FrameType() FrameType { return FrameFwdReq }

func (f *FwdReq) appendValue(b []byte) ([]byte, error) { return appendForwards(b, f.Forwards) }

func decodeFwdReq(v []byte) (Frame, error) {
	l, err := decodeForwards(v)
	if err != nil {
		return nil, err
	}
	return &FwdReq{Forwards: l}, nil
}

// Why the server refused a port forward (FwdAck.Refused).
const (
	FwdNotAllowed uint8 = 1 // over the hub's allowance (xs-ctl forward allow)
	FwdRestricted uint8 = 2 // a server port (listen ports, 22)
	FwdOutOfRange uint8 = 3 // outside 1024-49151
	FwdInUse      uint8 = 4 // forwarded to another hub
)

// FwdRefusal is a refused FwdReq entry and the reason.
type FwdRefusal struct {
	Forward
	Reason uint8
}

// FwdAck is the accepted subset of a FwdReq (frame 0x1C, srv→hub),
// optionally followed by the refused entries with a reason:
// `count u8 + count × (proto u8, port u16, reason u8)`.
type FwdAck struct {
	Forwards []Forward
	Refused  []FwdRefusal
}

// FrameType returns FrameFwdAck.
func (*FwdAck) FrameType() FrameType { return FrameFwdAck }

func (f *FwdAck) appendValue(b []byte) ([]byte, error) {
	b, err := appendForwards(b, f.Forwards)
	if err != nil || len(f.Refused) == 0 {
		return b, err
	}
	if len(f.Refused) > MaxForwards {
		return b, fmt.Errorf("%d refused forwards: %w", len(f.Refused), ErrLength)
	}
	b = append(b, uint8(len(f.Refused)))
	for _, r := range f.Refused {
		if err := validProto(r.Proto); err != nil {
			return b, err
		}
		b = append(b, r.Proto)
		b = binary.BigEndian.AppendUint16(b, r.Port)
		b = append(b, r.Reason)
	}
	return b, nil
}

func decodeFwdAck(v []byte) (Frame, error) {
	if err := atLeast(v, 1); err != nil {
		return nil, err
	}
	end := 1 + 3*int(v[0])
	if err := atLeast(v, end); err != nil {
		return nil, err
	}
	l, err := decodeForwards(v[:end])
	if err != nil {
		return nil, err
	}
	f := &FwdAck{Forwards: l}
	rest := v[end:]
	if len(rest) == 0 {
		return f, nil
	}
	n := int(rest[0])
	if n == 0 { // a present refused section is never empty (one encoding per frame)
		return nil, fmt.Errorf("empty refused section: %w", ErrInvalid)
	}
	if err := exact(rest, 1+4*n); err != nil {
		return nil, err
	}
	f.Refused = make([]FwdRefusal, n)
	for i := range f.Refused {
		e := rest[1+4*i:]
		f.Refused[i] = FwdRefusal{Forward: Forward{Proto: e[0], Port: binary.BigEndian.Uint16(e[1:])}, Reason: e[3]}
		if err := validProto(e[0]); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func validProto(p uint8) error {
	if p != ProtoTCP && p != ProtoUDP {
		return fmt.Errorf("proto %d: %w", p, ErrInvalid)
	}
	return nil
}

func appendForwards(b []byte, l []Forward) ([]byte, error) {
	if len(l) > MaxForwards {
		return b, fmt.Errorf("%d forwards: %w", len(l), ErrLength)
	}
	b = append(b, uint8(len(l)))
	for _, e := range l {
		if err := validProto(e.Proto); err != nil {
			return b, err
		}
		b = append(b, e.Proto)
		b = binary.BigEndian.AppendUint16(b, e.Port)
	}
	return b, nil
}

func decodeForwards(v []byte) ([]Forward, error) {
	if err := atLeast(v, 1); err != nil {
		return nil, err
	}
	n := int(v[0])
	if err := exact(v, 1+3*n); err != nil {
		return nil, err
	}
	l := make([]Forward, n)
	for i := range l {
		e := v[1+3*i:]
		l[i] = Forward{Proto: e[0], Port: binary.BigEndian.Uint16(e[1:])}
		if err := validProto(l[i].Proto); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// appendAddrPort4 appends ipv4 u32 + port u16. ap must hold an IPv4 address.
func appendAddrPort4(b []byte, ap netip.AddrPort) ([]byte, error) {
	a := ap.Addr().Unmap()
	if !a.Is4() {
		return b, fmt.Errorf("address %v is not IPv4: %w", ap.Addr(), ErrInvalid)
	}
	ip := a.As4()
	b = append(b, ip[:]...)
	return binary.BigEndian.AppendUint16(b, ap.Port()), nil
}

// addrPort4 decodes ipv4 u32 + port u16; v must hold at least 6 bytes.
func addrPort4(v []byte) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte(v[:4])), binary.BigEndian.Uint16(v[4:]))
}
