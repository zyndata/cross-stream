package bypass

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   Rules
		want Rules
		err  bool
	}{
		{"empty", Rules{}, Rules{Domains: []string{}, CIDRs: []string{}}, false},
		{"canonical", Rules{Domains: []string{" *.Bank.PL. ", "bank.pl", ".mbank.pl"}, CIDRs: []string{"1.2.3.4", "10.1.2.3/8", "10.0.0.0/8"}},
			Rules{Domains: []string{"bank.pl", "mbank.pl"}, CIDRs: []string{"1.2.3.4/32", "10.0.0.0/8"}}, false},
		{"bad domain", Rules{Domains: []string{"ba nk.pl"}}, Rules{}, true},
		{"empty label", Rules{Domains: []string{"a..b"}}, Rules{}, true},
		{"hyphen edge", Rules{Domains: []string{"-a.pl"}}, Rules{}, true},
		{"just dots", Rules{Domains: []string{"..."}}, Rules{}, true},
		{"ipv6", Rules{CIDRs: []string{"2001:db8::/32"}}, Rules{}, true},
		{"garbage cidr", Rules{CIDRs: []string{"10.0.0.0/33"}}, Rules{}, true},
	}
	for _, tt := range tests {
		got, err := tt.in.Normalize()
		if (err != nil) != tt.err {
			t.Errorf("%s: err %v", tt.name, err)
			continue
		}
		if err != nil {
			if !errors.Is(err, ErrRule) {
				t.Errorf("%s: err %v is not ErrRule", tt.name, err)
			}
			continue
		}
		if !slices.Equal(got.Domains, tt.want.Domains) || !slices.Equal(got.CIDRs, tt.want.CIDRs) {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
	many := Rules{}
	for i := range MaxRules + 1 {
		many.Domains = append(many.Domains, fmt.Sprintf("d%d.pl", i))
	}
	if _, err := many.Normalize(); err == nil {
		t.Error("too many domains accepted")
	}
}

func TestSet(t *testing.T) {
	s := NewSet()
	ip := netip.MustParseAddr
	now := time.Unix(1_000_000, 0)
	if s.Active() || s.Contains(ip("1.2.3.4")) || s.Learn("bank.pl", []netip.Addr{ip("1.2.3.4")}, time.Hour, now) {
		t.Fatal("empty set matches")
	}
	r, err := Rules{Domains: []string{"bank.pl"}, CIDRs: []string{"10.9.0.0/16"}}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	s.SetRules(r)
	if !s.Active() || !s.Contains(ip("10.9.1.1")) || s.Contains(ip("10.8.1.1")) {
		t.Fatal("CIDR rule")
	}
	tests := []struct {
		name  string
		match bool
	}{
		{"bank.pl", true}, {"WWW.Bank.PL.", true}, {"online.bank.pl", true}, {"mbank.pl", false}, {"bank.pl.evil", false},
	}
	for i, tt := range tests {
		a := netip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)})
		if got := s.Learn(tt.name, []netip.Addr{a, ip("::1")}, 10*time.Second, now); got != tt.match || s.Contains(a) != tt.match {
			t.Errorf("%s: learn %v contains %v, want %v", tt.name, got, s.Contains(a), tt.match)
		}
	}
	if s.Learned() != 3 { // IPv6 ignored
		t.Fatalf("learned %d", s.Learned())
	}
	// TTL is lifted to MinTTL.
	s.Expire(now.Add(MinTTL - time.Second))
	if !s.Contains(ip("192.0.2.1")) {
		t.Fatal("expired before MinTTL")
	}
	s.Expire(now.Add(MinTTL))
	if s.Contains(ip("192.0.2.1")) || s.Learned() != 0 {
		t.Fatal("not expired after MinTTL")
	}
	// A longer TTL is capped at MaxTTL; removing the domain drops its addresses.
	s.Learn("bank.pl", []netip.Addr{ip("192.0.2.9")}, 30*24*time.Hour, now)
	s.Expire(now.Add(MaxTTL - time.Second))
	if !s.Contains(ip("192.0.2.9")) {
		t.Fatal("expired before MaxTTL")
	}
	s.SetRules(Rules{CIDRs: []string{"10.9.0.0/16"}})
	if s.Contains(ip("192.0.2.9")) || !s.Active() {
		t.Fatal("address of a removed domain kept")
	}
	s.SetRules(Rules{})
	if s.Active() || s.Contains(ip("10.9.1.1")) {
		t.Fatal("cleared set still active")
	}
}
