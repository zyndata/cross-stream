// Package hublog keeps the hub daemon's recent log lines in memory and
// redacts them for export (GUI "Eksportuj logi", xs-hubctl logs): the owner
// can send a log to someone without leaking codes, secrets or addresses.
package hublog

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// DefaultLines is how many log lines xs-hubd keeps (about 20 min at info level
// on a busy hub, a few hundred kB).
const DefaultLines = 2000

// Ring is an io.Writer that keeps the last N lines written to it. Give it to
// slog.NewTextHandler (through io.MultiWriter with stderr): one record is one
// Write.
type Ring struct {
	mu    sync.Mutex
	lines []string
	next  int
	full  bool
}

// NewRing returns a ring of n lines (n > 0).
func NewRing(n int) *Ring { return &Ring{lines: make([]string, n)} }

// Write implements io.Writer.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for l := range strings.SplitSeq(strings.TrimRight(string(p), "\n"), "\n") {
		r.lines[r.next] = l
		r.next = (r.next + 1) % len(r.lines)
		r.full = r.full || r.next == 0
	}
	return len(p), nil
}

// Lines returns the kept lines, oldest first, unredacted.
func (r *Ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]string(nil), r.lines[:r.next]...)
	}
	return append(append([]string(nil), r.lines[r.next:]...), r.lines[:r.next]...)
}

// Redacted returns Lines with Redact applied to each.
func (r *Ring) Redacted() []string {
	lines := r.Lines()
	for i, l := range lines {
		lines[i] = Redact(l)
	}
	return lines
}

var (
	// key=value attributes whose value is a secret (slog text format).
	secretAttr = regexp.MustCompile(`\b([A-Za-z_]*(?:psk|password|passwd|secret|token|code)[A-Za-z_]*)=("(?:[^"\\]|\\.)*"|\S+)`)
	// Enrollment (XS1.) and pairing (XSR1.) codes anywhere in a line.
	codeRe = regexp.MustCompile(`\bXSR?1\.[A-Za-z0-9_\-.=+/]+`)
	// Base64 X25519 keys (44 characters, one '=').
	// No \b in front: a key may start with '+' or '/', which is no word
	// character. Group 1 is the character before the key, group 2 the key.
	keyRe  = regexp.MustCompile(`(^|[^A-Za-z0-9+/])([A-Za-z0-9+/]{43}=)`)
	macRe  = regexp.MustCompile(`\b((?:[0-9A-Fa-f]{2}[:-]){2}[0-9A-Fa-f]{2})[:-][0-9A-Fa-f]{2}[:-][0-9A-Fa-f]{2}[:-][0-9A-Fa-f]{2}\b`)
	ipv4Re = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
)

// Redact removes what should not leave the machine from one log line:
// secret attributes (psk, password, token, code…), enrollment and pairing
// codes, keys (first 6 characters kept), the device part of MAC addresses
// (vendor prefix kept) and the host part of public IPv4 addresses (first
// two octets kept). Private, tunnel (100.64/10), loopback and link-local
// addresses stay: they explain the hub's own network. IPv6 addresses are
// not touched (the hub's data paths are IPv4).
func Redact(line string) string {
	line = secretAttr.ReplaceAllString(line, "$1=[redacted]")
	line = codeRe.ReplaceAllStringFunc(line, func(s string) string {
		return s[:strings.IndexByte(s, '.')+1] + "[redacted]"
	})
	line = keyRe.ReplaceAllStringFunc(line, func(s string) string {
		i := len(s) - 44 // the key is the last 44 bytes
		return s[:i+6] + "…"
	})
	line = macRe.ReplaceAllString(line, "$1:xx:xx:xx")
	return ipv4Re.ReplaceAllStringFunc(line, func(s string) string {
		a, err := netip.ParseAddr(s)
		if err != nil || !public(a) {
			return s
		}
		b := a.As4()
		return strconv.Itoa(int(b[0])) + "." + strconv.Itoa(int(b[1])) + ".x.x"
	})
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func public(a netip.Addr) bool {
	return a.IsGlobalUnicast() && !a.IsPrivate() && !cgnat.Contains(a)
}
