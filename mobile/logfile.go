package xscore

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
)

// LogMaxBytes is the size at which core.log rolls over to core.log.1 (one
// old copy is kept).
const LogMaxBytes = 256 * 1024

// Secret patterns: pairing/enrollment codes (they carry the hub's Wi-Fi
// password and a token) and key=value or "key":"value" pairs for passwords
// and tokens must never reach a shared log.
var (
	codePattern   = regexp.MustCompile(`XS[RE]1\.[A-Za-z0-9_\-=+/]+`)
	secretPattern = regexp.MustCompile(`(?i)("?(?:psk|password|passphrase|tok|token|pair_token)"?\s*[=:]\s*"?)[^"\s,}]+`)
)

// Redact replaces secrets in a log line.
func Redact(s string) string {
	s = codePattern.ReplaceAllString(s, "<code>")
	return secretPattern.ReplaceAllString(s, "${1}<redacted>")
}

// rollingFile is an append-only log file that keeps one old copy.
type rollingFile struct {
	mu     sync.Mutex
	path   string
	max    int64
	f      *os.File
	size   int64
	closed bool
}

func openRolling(path string, maxBytes int64) (*rollingFile, error) {
	r := &rollingFile{path: path, max: maxBytes}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rollingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("log file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("log file: %w", err)
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Write appends p with secrets redacted, rolling the file first if it is full.
func (r *rollingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, errors.New("log file closed")
	}
	if r.f != nil && r.size >= r.max {
		_ = r.f.Close()
		r.f = nil
		_ = os.Rename(r.path, r.path+".1") // on failure the old file is appended to again
	}
	if r.f == nil { // after a roll, or an earlier roll whose reopen failed
		if err := r.open(); err != nil {
			return 0, err
		}
		if r.size >= r.max {
			r.size = 0 // the roll failed: try again after another max bytes
		}
	}
	n, err := r.f.WriteString(Redact(string(p)))
	r.size += int64(n)
	if err != nil {
		return 0, fmt.Errorf("log write: %w", err)
	}
	return len(p), nil
}

// Close closes the file; later writes fail.
func (r *rollingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
