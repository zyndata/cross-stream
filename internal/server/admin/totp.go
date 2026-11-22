package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 default; authenticator apps expect SHA-1
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters (RFC 6238 defaults every authenticator app supports).
const (
	TOTPStep   = 30 * time.Second
	TOTPDigits = 6
	// TOTPWindow accepts codes this many steps before or after now (clock skew).
	TOTPWindow = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret, base32 without padding.
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("totp secret: %w", err)
	}
	return b32.EncodeToString(b), nil
}

// TOTPCode returns the code for secret at step counter.
func TOTPCode(secret string, counter uint64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", fmt.Errorf("totp secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:]) & 0x7fffffff
	return fmt.Sprintf("%0*d", TOTPDigits, v%1_000_000), nil
}

// TOTPCounter is the RFC 6238 step number of t.
func TOTPCounter(t time.Time) uint64 {
	return uint64(t.Unix()) / uint64(TOTPStep/time.Second)
}

// VerifyTOTP checks code against secret at now ± TOTPWindow steps and
// returns the matching step counter. Codes at or before last (the step of
// the previous successful login) are refused, so a code works once.
func VerifyTOTP(secret, code string, now time.Time, last uint64) (uint64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != TOTPDigits {
		return 0, false
	}
	c := TOTPCounter(now)
	for d := -TOTPWindow; d <= TOTPWindow; d++ {
		n := uint64(int64(c) + int64(d))
		if n <= last {
			continue
		}
		want, err := TOTPCode(secret, n)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return n, true
		}
	}
	return 0, false
}

// OTPAuthURI is the otpauth:// URI an authenticator app scans.
func OTPAuthURI(issuer, user, secret string) string {
	label := url.PathEscape(issuer + ":" + user)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(TOTPDigits))
	q.Set("period", fmt.Sprint(int(TOTPStep/time.Second)))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
