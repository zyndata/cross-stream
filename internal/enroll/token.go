package enroll

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// TokenLen is the size of an enrollment token.
const TokenLen = 16

// DefaultTTL is how long a fresh token stays valid.
const DefaultTTL = 24 * time.Hour

// Token store errors.
var (
	ErrTokenInvalid = errors.New("enroll: token unknown or already used")
	ErrTokenExpired = errors.New("enroll: token expired")
	ErrTokenExists  = errors.New("enroll: token already stored")
)

// Token is a random single-use enrollment secret.
type Token [TokenLen]byte

// TokenHash is what a store keeps instead of the token.
type TokenHash [sha256.Size]byte

// NewToken returns a random token.
func NewToken() (Token, error) {
	return newToken(rand.Reader)
}

func newToken(r io.Reader) (Token, error) {
	var t Token
	if _, err := io.ReadFull(r, t[:]); err != nil {
		return Token{}, fmt.Errorf("new token: %w", err)
	}
	return t, nil
}

// String returns the standard base64 form (as used in the XS1 code and INIT payload).
func (t Token) String() string { return base64.StdEncoding.EncodeToString(t[:]) }

// ParseToken decodes the standard base64 form of a token.
func ParseToken(s string) (Token, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != TokenLen {
		return Token{}, fmt.Errorf("parse token: %w", ErrTokenInvalid)
	}
	return Token(b), nil
}

// Hash returns SHA-256 of the token. Tokens are 128-bit random, so no salt is needed.
func (t Token) Hash() TokenHash { return sha256.Sum256(t[:]) }

// TokenRecord is one stored token: its hash, the hub name it enrolls and its expiry.
type TokenRecord struct {
	Hash    TokenHash
	Name    string
	Expires time.Time
}

// TokenStore keeps hashed, single-use enrollment tokens with a TTL.
type TokenStore interface {
	// Add stores tok (hashed) for name, valid for ttl.
	Add(name string, tok Token, ttl time.Duration) error
	// Consume removes tok and returns its name. An expired token is removed
	// and reported as ErrTokenExpired.
	Consume(tok Token) (string, error)
	// Prune removes expired tokens and returns how many it removed.
	Prune() int
}

// MemoryStore is an in-memory TokenStore, safe for concurrent use.
type MemoryStore struct {
	mu   sync.Mutex
	now  func() time.Time
	recs map[TokenHash]TokenRecord
}

// NewMemoryStore returns an empty store. now is the clock (time.Now if nil).
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{now: now, recs: make(map[TokenHash]TokenRecord)}
}

// Add implements TokenStore.
func (s *MemoryStore) Add(name string, tok Token, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("add token: ttl %v must be positive", ttl)
	}
	if err := validName(name); err != nil {
		return fmt.Errorf("add token: %w", err)
	}
	h := tok.Hash()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.recs[h]; ok {
		return ErrTokenExists
	}
	s.recs[h] = TokenRecord{Hash: h, Name: name, Expires: s.now().Add(ttl)}
	return nil
}

// Consume implements TokenStore.
func (s *MemoryStore) Consume(tok Token) (string, error) {
	h := tok.Hash()
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.recs[h]
	if !ok {
		return "", ErrTokenInvalid
	}
	delete(s.recs, h)
	if !s.now().Before(rec.Expires) {
		return "", ErrTokenExpired
	}
	return rec.Name, nil
}

// Prune implements TokenStore.
func (s *MemoryStore) Prune() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	n := 0
	for h, rec := range s.recs {
		if !now.Before(rec.Expires) {
			delete(s.recs, h)
			n++
		}
	}
	return n
}
