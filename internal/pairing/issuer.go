package pairing

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zyndata/cross-stream/internal/enroll"
)

// TokenTTL is how long a pairing token (and the QR showing it) is valid.
const TokenTTL = 10 * time.Minute

// ErrToken is returned when a pairing token is unknown, used or expired.
var ErrToken = errors.New("pairing: bad token")

// Issuer hands out the current pairing code. Each code carries a fresh
// single-use token valid for TokenTTL; a new code is made when the current
// one expires and after every successful pairing. Older unused tokens stay
// valid until they expire. Safe for concurrent use.
type Issuer struct {
	mu      sync.Mutex
	now     func() time.Time
	store   *enroll.MemoryStore
	base    Payload
	cur     Payload
	expires time.Time
}

// NewIssuer returns an issuer for codes built from base (Tok is ignored).
// now is the clock (time.Now if nil).
func NewIssuer(base Payload, now func() time.Time) *Issuer {
	if now == nil {
		now = time.Now
	}
	return &Issuer{now: now, store: enroll.NewMemoryStore(now), base: base}
}

// Current returns the current code and its expiry, making a new one first if
// there is none or it has expired.
func (i *Issuer) Current() (Payload, time.Time, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.cur.Tok == "" || !i.now().Before(i.expires) {
		if err := i.rotateLocked(); err != nil {
			return Payload{}, time.Time{}, err
		}
	}
	return i.cur, i.expires, nil
}

// SetWiFi changes the hotspot fields of future codes (after a hotspot
// change) and makes a new current code; unused tokens stay valid.
func (i *Issuer) SetWiFi(ssid, bssid, psk string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	old := i.base
	i.base.SSID, i.base.BSSID, i.base.PSK = ssid, bssid, psk
	if err := i.rotateLocked(); err != nil {
		i.base = old
		return err
	}
	return nil
}

// Rotate makes a new current code (the old token stays valid until it expires).
func (i *Issuer) Rotate() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.rotateLocked()
}

func (i *Issuer) rotateLocked() error {
	i.store.Prune()
	tok, err := enroll.NewToken()
	if err != nil {
		return fmt.Errorf("pairing token: %w", err)
	}
	if err := i.store.Add("relay", tok, TokenTTL); err != nil {
		return fmt.Errorf("pairing token: %w", err)
	}
	p := i.base
	p.V, p.Tok = Version, tok.String()
	if _, err := p.Encode(); err != nil {
		return err
	}
	i.cur, i.expires = p, i.now().Add(TokenTTL)
	return nil
}

// Consume spends a pairing token. A valid token is removed (single use) and,
// if it was the one in the current code, the next Current makes a new code.
func (i *Issuer) Consume(token string) error {
	tok, err := enroll.ParseToken(token)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrToken, err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, err := i.store.Consume(tok); err != nil {
		return fmt.Errorf("%w: %w", ErrToken, err)
	}
	if token == i.cur.Tok {
		i.cur = Payload{}
	}
	return nil
}
