package control

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
)

func mustKeypair(t *testing.T) crypto.Keypair {
	t.Helper()
	kp, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

var proxyTok = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, ProxyTokenLen))

func TestMessageRoundTrip(t *testing.T) {
	tests := []Message{
		&Hello{RelayName: "anna", Carrier: "Play", PairToken: "dG9r", Limits: Limits{
			MaxPriority: PrioritySecondary, CapMB: 500, CapPeriod: PeriodMonth, ResetDay: 5, UsedMB: 12,
			OverCap: OverCapThrottle, ThrottleKbps: 256, BatteryMin: 20}},
		&Hello{RelayName: "bob"},
		&Welcome{RelayID: "r1", PathID: 1, ServerEP: "udp://1.2.3.4:51900", DataPort: DataPort},
		&Welcome{RelayID: "r2", PathID: 2, DataPort: DataPort},
		&Welcome{RelayID: "r3", PathID: 3, ServerEP: "tcp://1.2.3.4:51900", DataPort: DataPort, TCPConns: 2},
		&Reject{Reason: RejectForgotten},
		&Stats{BytesUp: 1 << 40, BytesDown: 5, Battery: 80, SignalDBm: -95, NetType: "lte", CellularOK: true, WifiRSSI: -40},
		&Leaving{Reason: LeavingBattery},
		&Endpoint{ServerEP: "tcp://1.2.3.4:443"},
		&Endpoint{ServerEP: "tcp://1.2.3.4:443", TCPConns: 4},
		&Stats{Battery: 50, TCPConns: 2, TCPRetrans: 7, TCPSent: 900},
		&Mode{Mode: ModeBonded},
		&Mode{Mode: ModeDirect, Proxy: true, ProxyToken: proxyTok},
		&LimitsUpdate{Limits: Limits{CapMB: 100, CapPeriod: PeriodDay}},
		&Forget{},
	}
	for _, m := range tests {
		t.Run(m.Type(), func(t *testing.T) {
			b, err := Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(b, []byte(`{"t":"`+m.Type()+`"`)) {
				t.Fatalf("encoding %s does not start with the type", b)
			}
			got, err := Unmarshal(b)
			if err != nil {
				t.Fatalf("%s: %v", b, err)
			}
			if !reflect.DeepEqual(got, m) {
				t.Fatalf("got %#v, want %#v", got, m)
			}
		})
	}
}

func TestUnmarshalInvalid(t *testing.T) {
	tests := []struct {
		name, in string
	}{
		{"not json", `{`},
		{"no type", `{"relay_name":"x"}`},
		{"hello no name", `{"t":"hello"}`},
		{"hello long name", `{"t":"hello","relay_name":"` + strings.Repeat("a", MaxNameLen+1) + `"}`},
		{"hello bad priority", `{"t":"hello","relay_name":"a","limits":{"max_priority":"top"}}`},
		{"hello bad period", `{"t":"hello","relay_name":"a","limits":{"cap_period":"week"}}`},
		{"hello bad over_cap", `{"t":"hello","relay_name":"a","limits":{"over_cap":"x"}}`},
		{"hello throttle without rate", `{"t":"hello","relay_name":"a","limits":{"over_cap":"throttle"}}`},
		{"hello negative cap", `{"t":"hello","relay_name":"a","limits":{"cap_mb":-1}}`},
		{"hello reset day", `{"t":"hello","relay_name":"a","limits":{"reset_day":29}}`},
		{"hello battery", `{"t":"hello","relay_name":"a","limits":{"battery_min":101}}`},
		{"welcome path 0", `{"t":"welcome","relay_id":"r1","path_id":0,"data_port":51902}`},
		{"welcome path 256", `{"t":"welcome","relay_id":"r1","path_id":256,"data_port":51902}`},
		{"welcome no port", `{"t":"welcome","relay_id":"r1","path_id":1}`},
		{"reject empty", `{"t":"reject"}`},
		{"leaving empty", `{"t":"leaving","reason":""}`},
		{"endpoint empty", `{"t":"endpoint"}`},
		{"endpoint conns", `{"t":"endpoint","server_ep":"tcp://h:1","tcp_conns":9}`},
		{"endpoint negative conns", `{"t":"endpoint","server_ep":"tcp://h:1","tcp_conns":-1}`},
		{"welcome conns", `{"t":"welcome","relay_id":"r1","path_id":1,"data_port":51902,"tcp_conns":99}`},
		{"mode bad", `{"t":"mode","mode":"x"}`},
		{"mode proxy no token", `{"t":"mode","mode":"direct","proxy":true}`},
		{"mode proxy short token", `{"t":"mode","mode":"direct","proxy":true,"proxy_token":"AAAA"}`},
		{"stats wrong type", `{"t":"stats","bytes_up":"x"}`},
		{"limits bad", `{"t":"limits","limits":{"cap_mb":-5}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if m, err := Unmarshal([]byte(tt.in)); !errors.Is(err, ErrMessage) {
				t.Fatalf("got %#v, %v; want ErrMessage", m, err)
			}
		})
	}
}

func TestUnmarshalUnknownType(t *testing.T) {
	m, err := Unmarshal([]byte(`{"t":"future","x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := m.(*Unknown); !ok || u.Type() != "future" {
		t.Fatalf("got %#v", m)
	}
	if _, err := Marshal(&Unknown{T: "x"}); !errors.Is(err, ErrMessage) {
		t.Fatalf("marshal unknown: %v", err)
	}
	if _, err := Marshal(&Hello{}); !errors.Is(err, ErrMessage) {
		t.Fatalf("marshal invalid hello: %v", err)
	}
}

// pair runs both handshake sides over a TCP loopback connection.
func pair(t *testing.T, relay, hub crypto.Keypair, hubKey [crypto.KeyLen]byte) (*Conn, *Conn, error, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	type res struct {
		c   *Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			ch <- res{nil, err}
			return
		}
		sc, err := Server(c, hub)
		if err != nil {
			_ = c.Close()
		}
		ch <- res{sc, err}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cc, cerr := Dial(ctx, &net.Dialer{}, ln.Addr().String(), relay, hubKey)
	r := <-ch
	t.Cleanup(func() {
		if cc != nil {
			_ = cc.Close()
		}
		if r.c != nil {
			_ = r.c.Close()
		}
	})
	return cc, r.c, cerr, r.err
}

func TestConnExchange(t *testing.T) {
	relay, hub := mustKeypair(t), mustKeypair(t)
	rc, hc, rerr, herr := pair(t, relay, hub, hub.Public)
	if rerr != nil || herr != nil {
		t.Fatal(rerr, herr)
	}
	if hc.Peer() != relay.Public || rc.Peer() != hub.Public {
		t.Fatal("peer keys wrong")
	}
	for i := 0; i < 3; i++ {
		if err := rc.Send(&Hello{RelayName: "anna"}); err != nil {
			t.Fatal(err)
		}
		m, err := hc.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if h, ok := m.(*Hello); !ok || h.RelayName != "anna" {
			t.Fatalf("got %#v", m)
		}
		if err := hc.Send(&Welcome{RelayID: "r1", PathID: 1, DataPort: DataPort}); err != nil {
			t.Fatal(err)
		}
		if m, err := rc.Recv(); err != nil || m.Type() != TypeWelcome {
			t.Fatalf("got %#v %v", m, err)
		}
	}
}

func TestConnWrongHubKey(t *testing.T) {
	relay, hub, other := mustKeypair(t), mustKeypair(t), mustKeypair(t)
	_, _, rerr, herr := pair(t, relay, hub, other.Public)
	if herr == nil || !errors.Is(herr, crypto.ErrHandshake) {
		t.Fatalf("hub err %v, want ErrHandshake", herr)
	}
	if rerr == nil {
		t.Fatal("relay handshake succeeded against the wrong key")
	}
}

func TestConnTamperedFrame(t *testing.T) {
	relay, hub := mustKeypair(t), mustKeypair(t)
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	done := make(chan *Conn, 1)
	go func() {
		c, _ := Server(b, hub)
		done <- c
	}()
	rc, err := Client(a, relay, hub.Public)
	if err != nil {
		t.Fatal(err)
	}
	hc := <-done
	if hc == nil {
		t.Fatal("server handshake failed")
	}
	// Raw frame with garbage instead of a Noise message.
	go func() { _ = writeFrame(a, []byte("garbage-garbage-garbage")) }()
	if _, err := hc.Recv(); !errors.Is(err, crypto.ErrHandshake) {
		t.Fatalf("err %v, want ErrHandshake", err)
	}
	_ = rc
}

func TestReadFrameLimits(t *testing.T) {
	tests := []struct {
		name string
		n    uint32
	}{
		{"zero", 0},
		{"too big", MaxFrame + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := binary.BigEndian.AppendUint32(nil, tt.n)
			if _, err := readFrame(bytes.NewReader(in), nil); !errors.Is(err, ErrFrame) {
				t.Fatalf("err %v, want ErrFrame", err)
			}
		})
	}
	if err := writeFrame(&bytes.Buffer{}, make([]byte, MaxFrame+1)); !errors.Is(err, ErrFrame) {
		t.Fatalf("write too big: %v", err)
	}
	// Truncated frame body.
	in := append(binary.BigEndian.AppendUint32(nil, 10), 1, 2, 3)
	if _, err := readFrame(bytes.NewReader(in), nil); err == nil {
		t.Fatal("truncated frame accepted")
	}
}
