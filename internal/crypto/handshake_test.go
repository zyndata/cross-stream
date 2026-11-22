package crypto

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustKeypair(t testing.TB) Keypair {
	t.Helper()
	k, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// handshake runs a full IK exchange and returns hub and server keys.
func handshake(t testing.TB, hub, srv Keypair) (Keys, Keys) {
	t.Helper()
	ini, err := NewInitiator(hub, srv.Public)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := ini.WriteInit([]byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewResponder(srv)
	if err != nil {
		t.Fatal(err)
	}
	p1, peer, err := res.ReadInit(m1)
	if err != nil {
		t.Fatal(err)
	}
	if string(p1) != `{"v":1}` || peer != hub.Public {
		t.Fatalf("init payload %q peer %x", p1, peer)
	}
	m2, srvKeys, err := res.WriteResp([]byte(`{"session_id":5}`))
	if err != nil {
		t.Fatal(err)
	}
	p2, hubKeys, err := ini.ReadResp(m2)
	if err != nil {
		t.Fatal(err)
	}
	if string(p2) != `{"session_id":5}` {
		t.Fatalf("resp payload %q", p2)
	}
	return hubKeys, srvKeys
}

func TestHandshakeKeysMatch(t *testing.T) {
	hub, srv := mustKeypair(t), mustKeypair(t)
	hk, sk := handshake(t, hub, srv)
	tests := []struct {
		name     string
		from, to *DataCipher
	}{
		{"hub to server", NewDataCipher(hk.Send), NewDataCipher(sk.Recv)},
		{"server to hub", NewDataCipher(sk.Send), NewDataCipher(hk.Recv)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := tt.from.Seal(nil, []byte("hdr"), []byte("ping"), 0)
			pt, err := tt.to.Open(nil, []byte("hdr"), ct, 0)
			if err != nil || string(pt) != "ping" {
				t.Fatalf("got %q %v", pt, err)
			}
		})
	}
	// The two directions use different keys.
	ct := NewDataCipher(hk.Send).Seal(nil, nil, []byte("x"), 0)
	if _, err := NewDataCipher(hk.Recv).Open(nil, nil, ct, 0); err == nil {
		t.Fatal("send and recv keys are equal")
	}
}

func TestHandshakeWrongServerKey(t *testing.T) {
	hub, srv, other := mustKeypair(t), mustKeypair(t), mustKeypair(t)
	ini, _ := NewInitiator(hub, other.Public)
	m1, err := ini.WriteInit(nil)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := NewResponder(srv)
	if _, _, err := res.ReadInit(m1); !errors.Is(err, ErrHandshake) {
		t.Fatalf("err %v, want ErrHandshake", err)
	}
}

func TestHandshakePrologue(t *testing.T) {
	hub, srv := mustKeypair(t), mustKeypair(t)
	tests := []struct {
		name         string
		ini, res     string
		wantMismatch bool
	}{
		{"both control", ControlPrologue, ControlPrologue, false},
		{"tunnel init on control responder", Prologue, ControlPrologue, true},
		{"control init on tunnel responder", ControlPrologue, Prologue, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ini, err := NewInitiatorWith(hub, srv.Public, tt.ini)
			if err != nil {
				t.Fatal(err)
			}
			m1, err := ini.WriteInit([]byte("hello"))
			if err != nil {
				t.Fatal(err)
			}
			res, err := NewResponderWith(srv, tt.res)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = res.ReadInit(m1)
			if tt.wantMismatch != (err != nil) {
				t.Fatalf("err %v, want mismatch %v", err, tt.wantMismatch)
			}
		})
	}
}

func TestHandshakeMalformed(t *testing.T) {
	hub, srv := mustKeypair(t), mustKeypair(t)
	ini, _ := NewInitiator(hub, srv.Public)
	m1, _ := ini.WriteInit([]byte("hello"))
	tampered := bytes.Clone(m1)
	tampered[len(tampered)-1] ^= 1
	tests := []struct {
		name string
		msg  []byte
	}{
		{"empty", nil},
		{"short", m1[:20]},
		{"truncated", m1[:len(m1)-1]},
		{"tampered", tampered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, _ := NewResponder(srv)
			if _, _, err := res.ReadInit(tt.msg); !errors.Is(err, ErrHandshake) {
				t.Fatalf("err %v, want ErrHandshake", err)
			}
		})
	}
	// Tampered response is rejected by the initiator.
	res, _ := NewResponder(srv)
	if _, _, err := res.ReadInit(m1); err != nil {
		t.Fatal(err)
	}
	m2, _, _ := res.WriteResp(nil)
	m2[len(m2)-1] ^= 1
	if _, _, err := ini.ReadResp(m2); !errors.Is(err, ErrHandshake) {
		t.Fatalf("err %v, want ErrHandshake", err)
	}
}

func TestHandshakeOutOfOrder(t *testing.T) {
	hub, srv := mustKeypair(t), mustKeypair(t)
	ini, _ := NewInitiator(hub, srv.Public)
	if _, _, err := ini.ReadResp(make([]byte, 64)); !errors.Is(err, ErrHandshake) {
		t.Fatalf("read before write: %v", err)
	}
	res, _ := NewResponder(srv)
	if _, _, err := res.WriteResp(nil); !errors.Is(err, ErrHandshake) {
		t.Fatalf("write before read: %v", err)
	}
	m1, _ := ini.WriteInit(nil)
	if _, err := ini.WriteInit(nil); !errors.Is(err, ErrHandshake) {
		t.Fatalf("second init: %v", err)
	}
	if _, _, err := res.ReadInit(m1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := res.ReadInit(m1); !errors.Is(err, ErrHandshake) {
		t.Fatalf("second read init: %v", err)
	}
}

func TestHandshakeLowOrderServerKey(t *testing.T) {
	hub := mustKeypair(t)
	var zero [KeyLen]byte
	ini, err := NewInitiator(hub, zero)
	if err != nil {
		return // rejected up front: fine
	}
	if _, err := ini.WriteInit(nil); !errors.Is(err, ErrHandshake) {
		t.Fatalf("err %v, want ErrHandshake", err)
	}
}
