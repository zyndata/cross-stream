package crypto

import (
	"errors"
	"fmt"

	"github.com/flynn/noise"
)

// Prologues are mixed into every handshake hash; both sides must use the same
// value. Each channel has its own, so a handshake for one cannot be used on the other.
const (
	Prologue        = "CrossStream/1"     // hub-server tunnel
	ControlPrologue = "CrossStream-ctl/1" // hub-relay control channel
)

// ErrHandshake is wrapped by every handshake failure.
var ErrHandshake = errors.New("crypto: handshake failed")

// Keys are the two transport CipherStates of a finished handshake.
// Send encrypts towards the peer, Recv decrypts from it.
type Keys struct {
	Send *noise.CipherState
	Recv *noise.CipherState
}

func newHandshake(initiator bool, local Keypair, peer []byte, prologue string) (*noise.HandshakeState, error) {
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s),
		Pattern:       noise.HandshakeIK,
		Initiator:     initiator,
		Prologue:      []byte(prologue),
		StaticKeypair: noise.DHKey{Private: local.Private[:], Public: local.Public[:]},
		PeerStatic:    peer,
	})
	if err != nil {
		return nil, fmt.Errorf("new handshake: %w", err)
	}
	return hs, nil
}

// Initiator is the hub side of Noise_IK_25519_ChaChaPoly_BLAKE2s.
// It knows the server static key in advance (from the enrollment code).
type Initiator struct {
	hs *noise.HandshakeState
}

// NewInitiator starts a tunnel handshake from local towards the server key.
func NewInitiator(local Keypair, server [KeyLen]byte) (*Initiator, error) {
	return NewInitiatorWith(local, server, Prologue)
}

// NewInitiatorWith starts a handshake towards peer with the given prologue.
func NewInitiatorWith(local Keypair, peer [KeyLen]byte, prologue string) (*Initiator, error) {
	hs, err := newHandshake(true, local, peer[:], prologue)
	if err != nil {
		return nil, err
	}
	return &Initiator{hs: hs}, nil
}

// WriteInit returns Noise message 1 carrying payload (encrypted).
func (i *Initiator) WriteInit(payload []byte) ([]byte, error) {
	msg, _, _, err := i.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, fmt.Errorf("write init: %w: %w", ErrHandshake, err)
	}
	return msg, nil
}

// ReadResp consumes Noise message 2 and returns its payload and the transport keys.
func (i *Initiator) ReadResp(msg []byte) ([]byte, Keys, error) {
	payload, toResp, toInit, err := i.hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, Keys{}, fmt.Errorf("read resp: %w: %w", ErrHandshake, err)
	}
	if toResp == nil || toInit == nil {
		return nil, Keys{}, fmt.Errorf("read resp: handshake not complete: %w", ErrHandshake)
	}
	return payload, Keys{Send: toResp, Recv: toInit}, nil
}

// Responder is the server side of the IK handshake.
type Responder struct {
	hs *noise.HandshakeState
}

// NewResponder prepares to answer one tunnel handshake with the server keypair.
func NewResponder(local Keypair) (*Responder, error) {
	return NewResponderWith(local, Prologue)
}

// NewResponderWith prepares to answer one handshake with the given prologue.
func NewResponderWith(local Keypair, prologue string) (*Responder, error) {
	hs, err := newHandshake(false, local, nil, prologue)
	if err != nil {
		return nil, err
	}
	return &Responder{hs: hs}, nil
}

// ReadInit consumes Noise message 1 and returns its payload and the
// initiator's static key. The caller must authorise that key (known hub or
// valid enroll token) before calling WriteResp.
func (r *Responder) ReadInit(msg []byte) ([]byte, [KeyLen]byte, error) {
	payload, _, _, err := r.hs.ReadMessage(nil, msg)
	if err != nil {
		return nil, [KeyLen]byte{}, fmt.Errorf("read init: %w: %w", ErrHandshake, err)
	}
	peer := r.hs.PeerStatic()
	if len(peer) != KeyLen {
		return nil, [KeyLen]byte{}, fmt.Errorf("read init: peer key %d bytes: %w", len(peer), ErrHandshake)
	}
	return payload, [KeyLen]byte(peer), nil
}

// WriteResp returns Noise message 2 carrying payload and the transport keys.
func (r *Responder) WriteResp(payload []byte) ([]byte, Keys, error) {
	msg, toResp, toInit, err := r.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, Keys{}, fmt.Errorf("write resp: %w: %w", ErrHandshake, err)
	}
	if toResp == nil || toInit == nil {
		return nil, Keys{}, fmt.Errorf("write resp: handshake not complete: %w", ErrHandshake)
	}
	return msg, Keys{Send: toInit, Recv: toResp}, nil
}
