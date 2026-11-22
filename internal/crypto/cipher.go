package crypto

import (
	"errors"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/flynn/noise"
)

// TagLen is the AEAD tag size added by Seal.
const TagLen = 16

// MaxCounter is the last usable nonce; Noise reserves 2^64-1.
const MaxCounter = math.MaxUint64 - 1

// Errors returned by DataCipher.
var (
	ErrOpen             = errors.New("crypto: authentication failed")
	ErrCounterExhausted = errors.New("crypto: send counter exhausted")
)

// DataCipher encrypts DATA packets in one direction with ChaCha20-Poly1305.
// The nonce is the packet counter (sent in clear in the outer header) and the
// outer header is the associated data. Seal and Open are safe for concurrent use.
type DataCipher struct {
	c    noise.Cipher
	next atomic.Uint64
}

// NewDataCipher wraps one transport CipherState from a finished handshake.
func NewDataCipher(cs *noise.CipherState) *DataCipher {
	return &DataCipher{c: cs.Cipher()}
}

// NextCounter reserves the next send counter (0, 1, 2, ...). Each value is
// returned once, so a nonce is never reused even across paths.
func (d *DataCipher) NextCounter() (uint64, error) {
	n := d.next.Add(1) - 1
	if n > MaxCounter {
		d.next.Store(MaxCounter + 1) // stay exhausted, never wrap
		return 0, ErrCounterExhausted
	}
	return n, nil
}

// Seal appends the encryption of plaintext to dst, authenticating header.
// counter must come from NextCounter and match the counter in header.
func (d *DataCipher) Seal(dst, header, plaintext []byte, counter uint64) []byte {
	return d.c.Encrypt(dst, counter, header, plaintext)
}

// Open appends the decryption of ciphertext to dst after checking the tag over
// ciphertext and header. Check the replay window before and update it after.
func (d *DataCipher) Open(dst, header, ciphertext []byte, counter uint64) ([]byte, error) {
	if counter > MaxCounter {
		return nil, fmt.Errorf("open: counter %d: %w", counter, ErrOpen)
	}
	out, err := d.c.Decrypt(dst, counter, header, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("open: %w", ErrOpen)
	}
	return out, nil
}
