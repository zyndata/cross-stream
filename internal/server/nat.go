package server

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

// Firewall installs and removes the NAT rules for tunnel traffic.
type Firewall interface {
	Setup(ctx context.Context) error
	Cleanup(ctx context.Context) error
	// Forward replaces the port forward DNAT rules.
	Forward(ctx context.Context, rules []ForwardRule) error
}

// NFTTable is the only nftables table xs-server touches. In Docker host mode
// the rules live in the host namespace, so nothing else may be changed.
const NFTTable = "crossstream"

// NFT manages the masquerade rule with the nft command.
type NFT struct {
	TunName string
	Prefix  netip.Prefix
	Egress  Egress // forward filter on traffic from the tunnel
	// Run executes nft with stdin; nil uses the nft binary from PATH.
	Run func(ctx context.Context, stdin string, args ...string) error
}

// Ruleset returns the nft script that (re)creates the crossstream table:
// tunnel sources leaving through any other interface are masqueraded; the
// prerouting chain holds the port forwards (ForwardScript); the forward
// chain is the egress filter.
func (n NFT) Ruleset() string {
	return fmt.Sprintf(`table ip %[1]s
delete table ip %[1]s
table ip %[1]s {
	chain prerouting {
		type nat hook prerouting priority dstnat; policy accept;
	}
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr %[2]s oifname != "%[3]s" masquerade
	}
%[4]s}
`, NFTTable, n.Prefix.Masked(), n.TunName, n.Egress.nftChain(n.TunName))
}

// Setup implements Firewall. It is idempotent: a stale table from a crashed
// run is replaced.
func (n NFT) Setup(ctx context.Context) error {
	if err := n.run(ctx, n.Ruleset(), "-f", "-"); err != nil {
		return fmt.Errorf("nft setup: %w", err)
	}
	return nil
}

// Cleanup implements Firewall: it deletes the crossstream table only.
func (n NFT) Cleanup(ctx context.Context) error {
	if err := n.run(ctx, "", "delete", "table", "ip", NFTTable); err != nil {
		return fmt.Errorf("nft cleanup: %w", err)
	}
	return nil
}

func (n NFT) run(ctx context.Context, stdin string, args ...string) error {
	if n.Run != nil {
		return n.Run(ctx, stdin, args...)
	}
	cmd := exec.CommandContext(ctx, "nft", args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nft %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}

// NoFirewall does nothing (tests, or NAT handled outside xs-server).
type NoFirewall struct{}

// Setup implements Firewall.
func (NoFirewall) Setup(context.Context) error { return nil }

// Cleanup implements Firewall.
func (NoFirewall) Cleanup(context.Context) error { return nil }
