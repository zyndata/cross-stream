package wire

import (
	"bytes"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		h    Header
	}{
		{"init", Header{Type: TypeHandshakeInit, PathID: 3}},
		{"resp", Header{Type: TypeHandshakeResp, SessionID: 7, PathID: 1}},
		{"data max", Header{Type: TypeData, SessionID: 0xFFFFFFFF, PathID: 0xFF, Counter: 0xFFFFFFFFFFFFFFFF}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := AppendHeader(nil, tt.h)
			b = append(b, 0xAA, 0xBB)
			if len(b) != HeaderLen+2 {
				t.Fatalf("len %d", len(b))
			}
			h, payload, err := ParseHeader(b)
			if err != nil {
				t.Fatal(err)
			}
			if h != tt.h || !bytes.Equal(payload, []byte{0xAA, 0xBB}) {
				t.Fatalf("got %+v %x", h, payload)
			}
		})
	}
}

func TestHeaderLayout(t *testing.T) {
	b := AppendHeader(nil, Header{Type: TypeData, SessionID: 0x01020304, PathID: 0x05, Counter: 0x060708090A0B0C0D})
	want := []byte{3, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}
	if !bytes.Equal(b, want) {
		t.Fatalf("got %x want %x", b, want)
	}
}

func TestParseHeaderErrors(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrShort},
		{"13 bytes", make([]byte, 13), ErrShort},
		{"type 0", make([]byte, 14), ErrType},
		{"type 4", append([]byte{4}, make([]byte, 13)...), ErrType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := ParseHeader(tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
		})
	}
}

func allFrames() []Frame {
	return []Frame{
		&IP{Seq: 9, Packet: []byte{0x45, 0, 0, 20}},
		&IP{Seq: 0, Packet: []byte{}},
		&Ack{Largest: 100, Bitmap: 0xF0F0},
		&Ping{TS: 123456},
		&Ping{TS: 1, Padding: 1300},
		&Pong{TS: 123456, ProbeLen: 1400},
		&PathConfig{PathID: 2, Priority: PrioritySecondary, RateKbps: 10000, CapMB: 500, DirUp: DirBackupOnly, DirDown: DirOff},
		&PathConfig{PathID: 1, Priority: PriorityBackup},
		&PathGone{PathID: 4},
		&Close{Reason: 2},
		&SessionConfig{Redundancy: RedundancyAuto, OverflowMbps: 12, Mode: ModeMinLatency},
		&TestReq{TestID: 5, PathID: TestAllPaths, Dir: TestUp, DurationMS: 5000, MaxMB: 50},
		&TestData{TestID: 5, Filler: bytes.Repeat([]byte{0x55}, 100)},
		&TestResult{TestID: 5, PathID: 1, Bytes: 1 << 40, DurationUS: 5_000_000},
		&FwdReq{Forwards: []Forward{{ProtoTCP, 8080}, {ProtoUDP, 51820}}},
		&FwdReq{Forwards: []Forward{}},
		&FwdAck{Forwards: []Forward{{ProtoTCP, 8080}}},
		&FwdAck{Forwards: []Forward{}, Refused: []FwdRefusal{{Forward{ProtoUDP, 22}, FwdRestricted}, {Forward{ProtoTCP, 80}, FwdOutOfRange}}},
		&PathInfo{PathID: 3, Observed: netip.MustParseAddrPort("203.0.113.7:40000")},
		&PathName{PathID: 3, Name: "Telefon Ani"},
		&PathName{PathID: 200, Name: ""},
		&StreamOpen{StreamID: 77, Dst: netip.MustParseAddrPort("198.51.100.1:443")},
		&StreamOpenResult{StreamID: 77, Code: OpenTimeout},
		&StreamData{StreamID: 77, Offset: 1 << 33, Data: []byte("hello")},
		&StreamWindow{StreamID: 77, MaxOffset: 65536},
		&StreamClose{StreamID: 77, FinalOffset: 99},
		&StreamReset{StreamID: 77, Code: 9},
		&Unknown{Type: 0x7F, Value: []byte{1, 2, 3}},
	}
}

func TestFramesRoundTrip(t *testing.T) {
	var payload []byte
	for _, f := range allFrames() {
		var err error
		one, err := AppendFrame(nil, f)
		if err != nil {
			t.Fatalf("%T: %v", f, err)
		}
		got, rest, err := ReadFrame(one)
		if err != nil {
			t.Fatalf("%T: %v", f, err)
		}
		if len(rest) != 0 || !reflect.DeepEqual(got, f) {
			t.Fatalf("%T: got %+v rest %d", f, got, len(rest))
		}
		if payload, err = AppendFrame(payload, f); err != nil {
			t.Fatal(err)
		}
	}
	frames, err := ParseFrames(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frames, allFrames()) {
		t.Fatal("concatenated frames differ")
	}
}

func TestFrameLayout(t *testing.T) {
	b, err := AppendFrame(nil, &PathConfig{PathID: 1, Priority: 2, RateKbps: 3, CapMB: 4, DirUp: 1, DirDown: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x14, 0, 12, 1, 2, 0, 0, 0, 3, 0, 0, 0, 4, 1, 2}
	if !bytes.Equal(b, want) {
		t.Fatalf("got %x want %x", b, want)
	}
	b, _ = AppendFrame(nil, &Ping{TS: 1, Padding: 3})
	if want := []byte{0x12, 0, 11, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0}; !bytes.Equal(b, want) {
		t.Fatalf("ping got %x want %x", b, want)
	}
}

func TestAppendFrameErrors(t *testing.T) {
	big := make([]Forward, 256)
	for i := range big {
		big[i] = Forward{ProtoTCP, uint16(i)}
	}
	tests := []struct {
		name string
		f    Frame
		want error
	}{
		{"priority 3", &PathConfig{Priority: 3}, ErrInvalid},
		{"dir_up 3", &PathConfig{DirUp: 3}, ErrInvalid},
		{"dir_down 3", &PathConfig{DirDown: 3}, ErrInvalid},
		{"redundancy 3", &SessionConfig{Redundancy: 3}, ErrInvalid},
		{"test dir 2", &TestReq{Dir: 2}, ErrInvalid},
		{"fwd proto", &FwdReq{Forwards: []Forward{{Proto: 1, Port: 1}}}, ErrInvalid},
		{"fwd too many", &FwdAck{Forwards: big}, ErrLength},
		{"path info v6", &PathInfo{Observed: netip.MustParseAddrPort("[2001:db8::1]:1")}, ErrInvalid},
		{"path name long", &PathName{Name: strings.Repeat("a", MaxPathName+1)}, ErrInvalid},
		{"path name control", &PathName{Name: "a\nb"}, ErrInvalid},
		{"path name utf8", &PathName{Name: "\xff"}, ErrInvalid},
		{"stream open zero", &StreamOpen{}, ErrInvalid},
		{"open code 4", &StreamOpenResult{Code: 4}, ErrInvalid},
		{"ping padding neg", &Ping{Padding: -1}, ErrLength},
		{"ping padding big", &Ping{Padding: MaxFrameValue}, ErrLength},
		{"ip too big", &IP{Packet: make([]byte, MaxFrameValue)}, ErrLength},
		{"unknown known type", &Unknown{Type: FrameIP}, ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := []byte{0xEE}
			b, err := AppendFrame(prefix, tt.f)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
			if !bytes.Equal(b, prefix) {
				t.Fatalf("buffer changed on error: %x", b)
			}
		})
	}
}

func TestPathInfoMappedIPv4(t *testing.T) {
	ap := netip.AddrPortFrom(netip.MustParseAddr("::ffff:192.0.2.1"), 5)
	b, err := AppendFrame(nil, &PathInfo{PathID: 1, Observed: ap})
	if err != nil {
		t.Fatal(err)
	}
	f, _, err := ReadFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.(*PathInfo).Observed; got != netip.MustParseAddrPort("192.0.2.1:5") {
		t.Fatalf("got %v", got)
	}
}

func tlv(t FrameType, v ...byte) []byte {
	return append([]byte{byte(t), byte(len(v) >> 8), byte(len(v))}, v...)
}

func TestReadFrameErrors(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrShort},
		{"2 bytes", []byte{0x10, 0}, ErrShort},
		{"value truncated", []byte{0x10, 0, 5, 1, 2}, ErrShort},
		{"unknown truncated", []byte{0x7F, 0, 1}, ErrShort},
		{"ip short", tlv(FrameIP, 1, 2, 3), ErrLength},
		{"ack short", tlv(FrameAck, make([]byte, 11)...), ErrLength},
		{"ack long", tlv(FrameAck, make([]byte, 13)...), ErrLength},
		{"ping short", tlv(FramePing, make([]byte, 7)...), ErrLength},
		{"ping dirty padding", tlv(FramePing, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1), ErrInvalid},
		{"pong long", tlv(FramePong, make([]byte, 11)...), ErrLength},
		{"path config short", tlv(FramePathConfig, make([]byte, 11)...), ErrLength},
		{"path config priority", tlv(FramePathConfig, 1, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0), ErrInvalid},
		{"path config dir", tlv(FramePathConfig, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 7), ErrInvalid},
		{"path gone empty", tlv(FramePathGone), ErrLength},
		{"close long", tlv(FrameClose, 1, 2), ErrLength},
		{"session config bad", tlv(FrameSessionConfig, 3, 0, 0, 0), ErrInvalid},
		{"session config bad mode", tlv(FrameSessionConfig, 0, 0, 0, 2), ErrInvalid},
		{"session config short", tlv(FrameSessionConfig, 1), ErrLength},
		{"session config empty", tlv(FrameSessionConfig), ErrLength},
		{"test req dir", tlv(FrameTestReq, 0, 1, 2, 2, 0, 0, 0, 0), ErrInvalid},
		{"test req short", tlv(FrameTestReq, 0, 1), ErrLength},
		{"test data short", tlv(FrameTestData, 1), ErrLength},
		{"test result short", tlv(FrameTestResult, make([]byte, 14)...), ErrLength},
		{"fwd empty", tlv(FrameFwdReq), ErrLength},
		{"fwd count mismatch", tlv(FrameFwdReq, 2, 6, 0, 80), ErrLength},
		{"fwd proto", tlv(FrameFwdAck, 1, 1, 0, 80), ErrInvalid},
		{"fwd ack short", tlv(FrameFwdAck), ErrLength},
		{"fwd ack refused short", tlv(FrameFwdAck, 0, 1, 6, 0, 22), ErrLength},
		{"fwd ack refused proto", tlv(FrameFwdAck, 0, 1, 1, 0, 22, 2), ErrInvalid},
		{"fwd ack accepted short", tlv(FrameFwdAck, 2, 6, 0, 80), ErrLength},
		{"path info short", tlv(FramePathInfo, make([]byte, 6)...), ErrLength},
		{"stream open short", tlv(FrameStreamOpen, make([]byte, 9)...), ErrLength},
		{"open result code", tlv(FrameStreamOpenResult, 0, 0, 0, 1, 4), ErrInvalid},
		{"open result short", tlv(FrameStreamOpenResult, 0, 0, 0, 1), ErrLength},
		{"stream data short", tlv(FrameStreamData, make([]byte, 11)...), ErrLength},
		{"fwd ack empty refused section", tlv(FrameFwdAck, 0, 0), ErrInvalid},
		{"stream window long", tlv(FrameStreamWindow, make([]byte, 13)...), ErrLength},
		{"stream close short", tlv(FrameStreamClose, make([]byte, 4)...), ErrLength},
		{"stream reset short", tlv(FrameStreamReset, make([]byte, 4)...), ErrLength},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := ReadFrame(tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("err %v, want %v", err, tt.want)
			}
			if len(tt.in) == 0 {
				return // an empty DATA payload is valid for ParseFrames
			}
			if _, err := ParseFrames(tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("ParseFrames err %v, want %v", err, tt.want)
			}
		})
	}
}

func TestParseFramesEmpty(t *testing.T) {
	frames, err := ParseFrames(nil)
	if err != nil || len(frames) != 0 {
		t.Fatalf("got %v %v", frames, err)
	}
}

func TestDecodedSlicesDoNotOverrun(t *testing.T) {
	b, _ := AppendFrame(nil, &IP{Seq: 1, Packet: []byte{1, 2}})
	b, _ = AppendFrame(b, &Close{Reason: 1})
	f, _, err := ReadFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	p := f.(*IP).Packet
	_ = append(p, 0xFF) // must not overwrite the next frame
	if b[len(b)-4] != byte(FrameClose) {
		t.Fatal("append through decoded slice overwrote the input")
	}
}

func TestTruncPathName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Telefon Ani", "Telefon Ani"},
		{"a\tb\nc", "abc"},
		{strings.Repeat("ż", 20), strings.Repeat("ż", 16)},
		{strings.Repeat("a", 31) + "ż", strings.Repeat("a", 31)},
	}
	for _, tt := range tests {
		if got := TruncPathName(tt.in); got != tt.want || validPathName(got) != nil {
			t.Errorf("TruncPathName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
