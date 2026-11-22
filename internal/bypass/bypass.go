// Package bypass holds the hub's bypass list (architecture.md "Bypass list"):
// domains (suffix match) and IPv4 CIDRs whose traffic leaves through a
// relay's carrier IP instead of the server. Domain rules become addresses
// when the hub DNS forwarder answers for them.
package bypass

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Limits of the list and of the learned addresses.
const (
	MaxRules = 200             // domains and CIDRs, each
	MinTTL   = 5 * time.Minute // a learned address stays at least this long
	MaxTTL   = 24 * time.Hour  // and at most this long
	maxIPs   = 16384           // learned addresses kept at once
	maxName  = 253             // DNS name length
	maxLabel = 63              // DNS label length
)

// ErrRule is returned for an entry that is not a domain or an IPv4 CIDR.
var ErrRule = errors.New("bypass: bad entry")

// Rules is the bypass list as the owner sets it (bypass.json, IPC).
type Rules struct {
	Domains []string `json:"domains"`
	CIDRs   []string `json:"cidrs"`
}

// Empty reports a list without entries.
func (r Rules) Empty() bool { return len(r.Domains) == 0 && len(r.CIDRs) == 0 }

// Normalize checks every entry and returns the list in canonical form:
// lower-case domains without "*." or dots at the ends, CIDRs masked (a bare
// address becomes /32), duplicates removed, sorted.
func (r Rules) Normalize() (Rules, error) {
	out := Rules{Domains: []string{}, CIDRs: []string{}}
	for _, d := range r.Domains {
		n, err := normDomain(d)
		if err != nil {
			return Rules{}, err
		}
		out.Domains = append(out.Domains, n)
	}
	for _, c := range r.CIDRs {
		p, err := parseCIDR(c)
		if err != nil {
			return Rules{}, err
		}
		out.CIDRs = append(out.CIDRs, p.String())
	}
	slices.Sort(out.Domains)
	slices.Sort(out.CIDRs)
	out.Domains, out.CIDRs = slices.Compact(out.Domains), slices.Compact(out.CIDRs)
	if len(out.Domains) > MaxRules || len(out.CIDRs) > MaxRules {
		return Rules{}, fmt.Errorf("%w: at most %d domains and %d CIDRs", ErrRule, MaxRules, MaxRules)
	}
	return out, nil
}

func normDomain(s string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(s))
	d = strings.TrimPrefix(d, "*.")
	d = strings.Trim(d, ".")
	if d == "" || len(d) > maxName {
		return "", fmt.Errorf("%w: domain %q", ErrRule, s)
	}
	for _, l := range strings.Split(d, ".") {
		if l == "" || len(l) > maxLabel || l[0] == '-' || l[len(l)-1] == '-' {
			return "", fmt.Errorf("%w: domain %q", ErrRule, s)
		}
		for _, c := range l {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return "", fmt.Errorf("%w: domain %q", ErrRule, s)
			}
		}
	}
	return d, nil
}

func parseCIDR(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
		return netip.PrefixFrom(a, 32), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("%w: CIDR %q (IPv4 only)", ErrRule, s)
	}
	return p.Masked(), nil
}

// Set decides per destination address whether a packet bypasses the
// tunnel. Contains is on the packet path: it takes a read lock only.
type Set struct {
	active atomic.Bool // the list is not empty

	mu       sync.RWMutex
	domains  []string
	prefixes []netip.Prefix
	ips      map[netip.Addr]learned
}

type learned struct {
	expires time.Time
	domain  string // the rule that matched
}

// NewSet returns an empty set.
func NewSet() *Set { return &Set{ips: make(map[netip.Addr]learned)} }

// SetRules replaces the list (already normalized). Learned addresses of
// removed domains are dropped.
func (s *Set) SetRules(r Rules) {
	var prefixes []netip.Prefix
	for _, c := range r.CIDRs {
		if p, err := parseCIDR(c); err == nil {
			prefixes = append(prefixes, p)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.domains, s.prefixes = slices.Clone(r.Domains), prefixes
	for ip, l := range s.ips {
		if !slices.Contains(s.domains, l.domain) {
			delete(s.ips, ip)
		}
	}
	s.active.Store(len(s.domains)+len(s.prefixes) > 0)
}

// Active reports a non-empty list.
func (s *Set) Active() bool { return s.active.Load() }

// rule returns the domain rule name falls under ("" = none).
func (s *Set) rule(name string) string {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	for _, d := range s.domains {
		if name == d || strings.HasSuffix(name, "."+d) {
			return d
		}
	}
	return ""
}

// Learn records the addresses of a DNS answer for name when name falls under
// a domain rule; ttl is kept within [MinTTL, MaxTTL]. It reports whether the
// name matched.
func (s *Set) Learn(name string, ips []netip.Addr, ttl time.Duration, now time.Time) bool {
	if !s.Active() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.rule(name)
	if d == "" {
		return false
	}
	exp := now.Add(min(max(ttl, MinTTL), MaxTTL))
	for _, ip := range ips {
		if !ip.Is4() {
			continue
		}
		if _, ok := s.ips[ip]; !ok && len(s.ips) >= maxIPs {
			continue
		}
		if old, ok := s.ips[ip]; !ok || old.expires.Before(exp) {
			s.ips[ip] = learned{expires: exp, domain: d}
		}
	}
	return true
}

// Contains reports whether traffic to ip bypasses the tunnel. Expired
// addresses stay until Expire runs (at most a tick later).
func (s *Set) Contains(ip netip.Addr) bool {
	if !s.Active() {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.ips[ip]; ok {
		return true
	}
	for _, p := range s.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Expire drops learned addresses past their TTL.
func (s *Set) Expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ip, l := range s.ips {
		if !now.Before(l.expires) {
			delete(s.ips, ip)
		}
	}
}

// Learned returns the number of learned addresses.
func (s *Set) Learned() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.ips)
}
