package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// KeyLen is the size of an X25519 private or public key.
const KeyLen = 32

// ErrKey is wrapped by every key parsing error.
var ErrKey = errors.New("crypto: invalid key")

// Keypair is an X25519 static keypair. String and LogValue print only the
// public key, so a Keypair is safe to log.
type Keypair struct {
	Private [KeyLen]byte
	Public  [KeyLen]byte
}

// GenerateKeypair creates a new random static keypair.
func GenerateKeypair() (Keypair, error) {
	return generateKeypair(rand.Reader)
}

func generateKeypair(r io.Reader) (Keypair, error) {
	var priv [KeyLen]byte
	if _, err := io.ReadFull(r, priv[:]); err != nil {
		return Keypair{}, fmt.Errorf("generate keypair: %w", err)
	}
	return KeypairFromPrivate(priv)
}

// KeypairFromPrivate derives the public key for priv.
func KeypairFromPrivate(priv [KeyLen]byte) (Keypair, error) {
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return Keypair{}, fmt.Errorf("derive public key: %w", err)
	}
	return Keypair{Private: priv, Public: [KeyLen]byte(pub)}, nil
}

// String returns the public key only.
func (k Keypair) String() string { return "keypair(pub=" + EncodeKey(k.Public) + ")" }

// LogValue keeps the private key out of slog output.
func (k Keypair) LogValue() slog.Value { return slog.StringValue(EncodeKey(k.Public)) }

// EncodeKey returns the standard base64 form of a key.
func EncodeKey(k [KeyLen]byte) string {
	return base64.StdEncoding.EncodeToString(k[:])
}

// ParseKey decodes a standard base64 key. Surrounding whitespace is ignored.
func ParseKey(s string) ([KeyLen]byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return [KeyLen]byte{}, fmt.Errorf("parse key: %w", ErrKey)
	}
	if len(b) != KeyLen {
		return [KeyLen]byte{}, fmt.Errorf("parse key: %d bytes: %w", len(b), ErrKey)
	}
	return [KeyLen]byte(b), nil
}

// SaveKeypair writes the private key as base64 to path with mode 0600.
// The write is atomic: a temporary file in the same directory is renamed over path.
func SaveKeypair(path string, k Keypair) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".key-*")
	if err != nil {
		return fmt.Errorf("save keypair: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("save keypair: chmod: %w", err)
	}
	if _, err = tmp.WriteString(EncodeKey(k.Private) + "\n"); err != nil {
		return fmt.Errorf("save keypair: write: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("save keypair: sync: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("save keypair: close: %w", err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save keypair: rename: %w", err)
	}
	return nil
}

// LoadKeypair reads a private key written by SaveKeypair and derives its public key.
func LoadKeypair(path string) (Keypair, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Keypair{}, fmt.Errorf("load keypair: %w", err)
	}
	priv, err := ParseKey(string(b))
	if err != nil {
		return Keypair{}, fmt.Errorf("load keypair %s: %w", filepath.Base(path), err)
	}
	return KeypairFromPrivate(priv)
}
