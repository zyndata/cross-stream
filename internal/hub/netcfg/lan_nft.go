//go:build !windows

package netcfg

import "context"

// LoopGuard reports whether LAN uses Blocked (the nft loop guard).
const LoopGuard = true

// OwnTrafficAlways reports whether the laptop's own traffic always uses the tunnel (no
// switch). False on Linux: LAN.OwnTraffic decides (phase 21).
const OwnTrafficAlways = false

// Setup enables IPv4 forwarding and installs the policy rules and the nft
// table, replacing leftovers of a crashed run. Routes need the ports up:
// call Ensure next (and from the watchdog).
func (l LAN) Setup(ctx context.Context) error { return l.setupNft(ctx) }

// Ensure re-applies whatever is missing (watchdog: NM or DHCP rewrote the
// routes, the TUN came or went) and returns what it fixed.
func (l LAN) Ensure(ctx context.Context) ([]string, error) { return l.ensureNft(ctx) }

// ApplyRules re-applies the nft table (MSS clamp, loop guard, forwards, TTL).
func (l LAN) ApplyRules(ctx context.Context) error { return l.applyNft(ctx) }

// Cleanup removes the policy rules, table 100 and the nft table.
func (l LAN) Cleanup(ctx context.Context) error { return l.cleanupNft(ctx) }
