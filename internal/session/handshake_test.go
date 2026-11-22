package session

import (
	"errors"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/wire"
)

func TestDialerWait(t *testing.T) {
	d := &Dialer{}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for i, w := range want {
		d.attempt = i + 1
		if got := d.Wait(); got != w {
			t.Errorf("attempt %d: wait %v, want %v", i+1, got, w)
		}
	}
	d.Reset()
	if d.Attempts() != 0 || d.Wait() != time.Second {
		t.Fatalf("after reset: %d %v", d.Attempts(), d.Wait())
	}
}

func TestDialerTimestamps(t *testing.T) {
	srv, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	hub, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1000, 0)
	d := &Dialer{Local: hub, Server: srv.Public, Payload: InitPayload{V: 1, SW: "t", HubName: "h", Paths: []int{0}},
		Now: func() time.Time { return clock }}
	var last int64
	for i, step := range []time.Duration{0, 0, -time.Hour, time.Second} { // same time, then the clock steps back
		clock = clock.Add(step)
		pkt, err := d.Begin(0)
		if err != nil {
			t.Fatal(err)
		}
		r, err := crypto.NewResponder(srv)
		if err != nil {
			t.Fatal(err)
		}
		raw, _, err := r.ReadInit(pkt[wire.HeaderLen:])
		if err != nil {
			t.Fatal(err)
		}
		p, err := ParseInit(raw)
		if err != nil {
			t.Fatal(err)
		}
		if p.TS <= last {
			t.Fatalf("attempt %d: ts %d not after %d", i, p.TS, last)
		}
		last = p.TS
	}
	if want := time.Unix(1000, 0).UnixNano() + 3; last != want {
		t.Fatalf("last ts %d, want %d (monotonic past the clock step)", last, want)
	}
}

func TestParsePayloads(t *testing.T) {
	tests := []struct {
		name string
		resp bool
		in   string
		ok   bool
	}{
		{"init ok", false, `{"v":1,"sw":"x","hub_name":"a","enroll_token":null,"paths":[0],"ts":1}`, true},
		{"init no ts", false, `{"v":1,"sw":"x","hub_name":"a","enroll_token":null,"paths":[0]}`, false},
		{"init negative ts", false, `{"v":1,"sw":"x","hub_name":"a","paths":[0],"ts":-5}`, false},
		{"init version", false, `{"v":2}`, false},
		{"init junk", false, `{`, false},
		{"resp ok", true, `{"v":1,"session_id":5,"tunnel_ip":"100.64.0.2","mtu":1360}`, true},
		{"resp zero session", true, `{"v":1,"session_id":0,"tunnel_ip":"100.64.0.2","mtu":1360}`, false},
		{"resp bad ip", true, `{"v":1,"session_id":5,"tunnel_ip":"fd00::1","mtu":1360}`, false},
		{"resp bad mtu", true, `{"v":1,"session_id":5,"tunnel_ip":"100.64.0.2","mtu":100}`, false},
		{"resp junk", true, `[]`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.resp {
				_, err = ParseResp([]byte(tt.in))
			} else {
				_, err = ParseInit([]byte(tt.in))
			}
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if err != nil && !errors.Is(err, ErrPayload) {
				t.Fatalf("error %v does not wrap ErrPayload", err)
			}
		})
	}
}
