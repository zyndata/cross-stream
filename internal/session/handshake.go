package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/wire"
)

// ProtocolVersion is the "v" field of handshake payloads.
const ProtocolVersion = 1

// Handshake retry: wait 1 s, 2 s, 4 s, then 8 s per attempt.
const (
	RetryFirst = time.Second
	RetryMax   = 8 * time.Second
)

// ErrPayload is wrapped by handshake payload decode errors.
var ErrPayload = errors.New("session: invalid handshake payload")

// InitPayload is the JSON inside Noise message 1 (protocol.md section 1).
type InitPayload struct {
	V           int     `json:"v"`
	SW          string  `json:"sw"`
	HubName     string  `json:"hub_name"`
	EnrollToken *string `json:"enroll_token"` // base64 token, null once enrolled
	Paths       []int   `json:"paths"`
	TS          int64   `json:"ts"` // send time, Unix ns; strictly increasing per hub (replay check)
}

// RespPayload is the JSON inside Noise message 2.
type RespPayload struct {
	V         int    `json:"v"`
	SessionID uint32 `json:"session_id"`
	TunnelIP  string `json:"tunnel_ip"`
	MTU       int    `json:"mtu"`
	PEP       bool   `json:"pep,omitempty"` // server accepts PEP streams (phase 13)
}

// ParseInit decodes and checks an INIT payload.
func ParseInit(b []byte) (InitPayload, error) {
	var p InitPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return InitPayload{}, fmt.Errorf("init payload: %w: %w", ErrPayload, err)
	}
	if p.V != ProtocolVersion {
		return InitPayload{}, fmt.Errorf("init payload: version %d: %w", p.V, ErrPayload)
	}
	if p.TS <= 0 {
		return InitPayload{}, fmt.Errorf("init payload: no timestamp: %w", ErrPayload)
	}
	return p, nil
}

// ParseResp decodes and checks a RESP payload.
func ParseResp(b []byte) (RespPayload, error) {
	var p RespPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return RespPayload{}, fmt.Errorf("resp payload: %w: %w", ErrPayload, err)
	}
	if p.V != ProtocolVersion || p.SessionID == 0 || p.MTU < 576 || p.MTU > 65535 {
		return RespPayload{}, fmt.Errorf("resp payload: bad v/session/mtu: %w", ErrPayload)
	}
	if ip, err := netip.ParseAddr(p.TunnelIP); err != nil || !ip.Is4() {
		return RespPayload{}, fmt.Errorf("resp payload: tunnel ip %q: %w", p.TunnelIP, ErrPayload)
	}
	return p, nil
}

// Dialer drives the initiator (hub) side of a handshake. Every attempt uses a
// fresh Noise state, and only the response to the latest attempt is accepted,
// so retransmissions can never leave hub and server on different keys.
type Dialer struct {
	Local   crypto.Keypair
	Server  [crypto.KeyLen]byte
	Payload InitPayload
	Now     func() time.Time // nil = time.Now

	init    *crypto.Initiator
	attempt int
	lastTS  int64
}

// Begin starts a new attempt and returns a complete HANDSHAKE_INIT packet
// for pathID.
func (d *Dialer) Begin(pathID uint8) ([]byte, error) {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	// Strictly increasing even if the clock steps back while the hub runs.
	d.lastTS = max(now().UnixNano(), d.lastTS+1)
	p := d.Payload
	p.TS = d.lastTS
	payload, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("dialer: %w", err)
	}
	ini, err := crypto.NewInitiator(d.Local, d.Server)
	if err != nil {
		return nil, fmt.Errorf("dialer: %w", err)
	}
	msg, err := ini.WriteInit(payload)
	if err != nil {
		return nil, fmt.Errorf("dialer: %w", err)
	}
	d.init = ini
	d.attempt++
	pkt := wire.AppendHeader(make([]byte, 0, wire.HeaderLen+len(msg)),
		wire.Header{Type: wire.TypeHandshakeInit, PathID: pathID})
	return append(pkt, msg...), nil
}

// Wait is how long to wait for the response to the current attempt.
func (d *Dialer) Wait() time.Duration {
	w := RetryFirst
	for i := 1; i < d.attempt && w < RetryMax; i++ {
		w *= 2
	}
	return min(w, RetryMax)
}

// Attempts returns how many INIT packets Begin produced since the last Reset.
func (d *Dialer) Attempts() int { return d.attempt }

// Reset forgets the pending attempt and the retry count.
func (d *Dialer) Reset() { d.init, d.attempt = nil, 0 }

// Finish consumes a HANDSHAKE_RESP (h = parsed header, msg = payload after
// it) for the latest attempt and returns the response and transport keys.
func (d *Dialer) Finish(h wire.Header, msg []byte) (RespPayload, crypto.Keys, error) {
	if d.init == nil {
		return RespPayload{}, crypto.Keys{}, errors.New("dialer: no handshake in progress")
	}
	raw, keys, err := d.init.ReadResp(msg)
	if err != nil {
		return RespPayload{}, crypto.Keys{}, fmt.Errorf("dialer: %w", err)
	}
	resp, err := ParseResp(raw)
	if err != nil {
		return RespPayload{}, crypto.Keys{}, err
	}
	if resp.SessionID != h.SessionID {
		return RespPayload{}, crypto.Keys{}, fmt.Errorf("dialer: header session %d != payload %d: %w",
			h.SessionID, resp.SessionID, ErrPayload)
	}
	d.Reset()
	return resp, keys, nil
}

// Respond builds the HANDSHAKE_RESP packet for an INIT read by r and returns
// it with the transport keys. pathID is the path the INIT arrived on.
func Respond(r *crypto.Responder, pathID uint8, resp RespPayload) ([]byte, crypto.Keys, error) {
	payload, err := json.Marshal(resp)
	if err != nil {
		return nil, crypto.Keys{}, fmt.Errorf("respond: %w", err)
	}
	msg, keys, err := r.WriteResp(payload)
	if err != nil {
		return nil, crypto.Keys{}, fmt.Errorf("respond: %w", err)
	}
	pkt := wire.AppendHeader(make([]byte, 0, wire.HeaderLen+len(msg)),
		wire.Header{Type: wire.TypeHandshakeResp, SessionID: resp.SessionID, PathID: pathID})
	return append(pkt, msg...), keys, nil
}
