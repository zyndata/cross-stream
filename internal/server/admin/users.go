package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// User errors.
var (
	ErrUserExists   = errors.New("admin user already exists")
	ErrUserNotFound = errors.New("admin user not found")
	ErrBadLogin     = errors.New("invalid user name, password or code")
	ErrTOTPRequired = errors.New("authenticator code required")
)

// MinPasswordLen is the shortest admin password accepted.
const MinPasswordLen = 8

// UserRecord is one admin user as stored in state.json.
type UserRecord struct {
	Name    string    `json:"name"`
	Hash    string    `json:"hash"`           // argon2id PHC string
	TOTP    string    `json:"totp,omitempty"` // base32 secret, empty = off
	Created time.Time `json:"created"`
	Changed time.Time `json:"changed"` // password or TOTP change: older web sessions end
}

// UserInfo is a UserRecord without secrets.
type UserInfo struct {
	Name    string    `json:"name"`
	TOTP    bool      `json:"totp"`
	Created time.Time `json:"created"`
	Changed time.Time `json:"changed"`
}

// Info drops the secrets.
func (u UserRecord) Info() UserInfo {
	return UserInfo{Name: u.Name, TOTP: u.TOTP != "", Created: u.Created, Changed: u.Changed}
}

// UserStore persists admin users (server.State implements it).
type UserStore interface {
	Users() []UserRecord
	User(name string) (UserRecord, bool)
	// PutUser adds or replaces a user; create=true fails with ErrUserExists
	// if the name is taken, create=false with ErrUserNotFound if it is not.
	PutUser(u UserRecord, create bool) error
	DelUser(name string) error
}

// argon2id parameters (RFC 9106 second recommended option: 64 MiB, t=3).
type argonParams struct {
	mem     uint32 // KiB
	time    uint32
	threads uint8
}

var (
	productionArgon = argonParams{mem: 64 * 1024, time: 3, threads: 2}
	defaultArgon    = productionArgon // tests lower it
)

// ValidUserName reports whether name is a usable admin user name.
func ValidUserName(name string) error {
	if name == "" || len(name) > 32 {
		return errors.New("user name: 1-32 characters")
	}
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok {
			return errors.New("user name: letters, digits, '-', '_' and '.' only")
		}
	}
	return nil
}

// ValidPassword checks the password policy.
func ValidPassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return fmt.Errorf("password: at least %d characters", MinPasswordLen)
	}
	if len(pw) > 1024 {
		return errors.New("password: too long")
	}
	return nil
}

// HashPassword returns an argon2id PHC string for pw with a random salt.
func HashPassword(pw string) (string, error) {
	return hashWith(pw, defaultArgon)
}

func hashWith(pw string, p argonParams) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, p.time, p.mem, p.threads, 32)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.mem, p.time, p.threads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches the PHC string hash (constant time).
func VerifyPassword(hash, pw string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.mem, &p.time, &p.threads); err != nil {
		return false
	}
	// Refuse absurd parameters from a tampered state file (memory bomb).
	if p.mem == 0 || p.mem > 1<<20 || p.time == 0 || p.time > 16 || p.threads == 0 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, p.time, p.mem, p.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
