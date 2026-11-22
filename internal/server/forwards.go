package server

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zyndata/cross-stream/internal/server/admin"
	"github.com/zyndata/cross-stream/internal/wire"
)

// Port forwarding limits (architecture.md "Port forwarding").
const (
	MaxForwardsPerHub = 20
	FwdPortMin        = 1024
	FwdPortMax        = 49151
	sshPort           = 22
	fwdApplyTimeout   = 10 * time.Second
)

// ForwardRule is one public port DNATed to a hub's tunnel address.
type ForwardRule struct {
	Proto uint8 // wire.ProtoTCP or wire.ProtoUDP
	Port  uint16
	To    netip.Addr
}

func protoName(p uint8) string {
	if p == wire.ProtoUDP {
		return "udp"
	}
	return "tcp"
}

// ForwardScript returns the nft script that replaces the DNAT rules: only
// packets addressed to the server itself (fib daddr type local) are
// forwarded, so routed traffic (Docker host mode) is never caught.
func (n NFT) ForwardScript(rules []ForwardRule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "flush chain ip %s prerouting\n", NFTTable)
	for _, r := range rules {
		fmt.Fprintf(&b, "add rule ip %s prerouting fib daddr type local %s dport %d dnat to %s:%d\n",
			NFTTable, protoName(r.Proto), r.Port, r.To, r.Port)
	}
	return b.String()
}

// Forward implements Firewall.
func (n NFT) Forward(ctx context.Context, rules []ForwardRule) error {
	if err := n.run(ctx, n.ForwardScript(rules), "-f", "-"); err != nil {
		return fmt.Errorf("nft forwards: %w", err)
	}
	return nil
}

// Forward implements Firewall.
func (NoFirewall) Forward(context.Context, []ForwardRule) error { return nil }

// restrictedPort reports the server's own ports (UDP listen, TCP, WSS, the
// web panel, ingress and metrics listeners) and 22.
func (s *Server) restrictedPort(proto uint8, port uint16) bool {
	if port == sshPort {
		return true
	}
	if proto != wire.ProtoUDP {
		for _, l := range []string{s.o.Config.WebListen, s.o.Config.WebIngressListen, s.o.Config.MetricsListen} {
			if _, p, err := net.SplitHostPort(l); err == nil && p == strconv.Itoa(int(port)) {
				return true
			}
		}
	}
	var ports []int
	if proto == wire.ProtoUDP {
		ports = slices.Clone(s.o.Config.ListenPorts)
		for _, a := range s.Addrs() {
			ports = append(ports, int(a.Port()))
		}
	} else {
		tcp, wss := s.StreamPorts()
		ports = slices.Concat(s.o.Config.TCPPorts, s.o.Config.WSSPorts, tcp, wss)
	}
	return slices.Contains(ports, int(port))
}

// decideForwards splits a hub's request into accepted and refused entries.
// owner maps ports already forwarded to other hubs.
func (s *Server) decideForwards(req []wire.Forward, allow int, owner map[wire.Forward]string) ([]wire.Forward, []wire.FwdRefusal) {
	allow = min(allow, MaxForwardsPerHub)
	acc := []wire.Forward{}
	var ref []wire.FwdRefusal
	for _, f := range req {
		reason := uint8(0)
		switch {
		case slices.Contains(acc, f):
			continue // duplicate entry
		case f.Port < FwdPortMin || f.Port > FwdPortMax:
			reason = wire.FwdOutOfRange
		case s.restrictedPort(f.Proto, f.Port):
			reason = wire.FwdRestricted
		case owner[f] != "":
			reason = wire.FwdInUse
		case len(acc) >= allow:
			reason = wire.FwdNotAllowed
		}
		if reason != 0 {
			ref = append(ref, wire.FwdRefusal{Forward: f, Reason: reason})
			continue
		}
		acc = append(acc, f)
	}
	return acc, ref
}

// onFwdReq answers a hub's port forward list and updates the DNAT rules.
func (s *Server) onFwdReq(hs *hubSession, f *wire.FwdReq) {
	allow := s.o.State.ForwardAllow(hs.hub.Name)
	s.mu.Lock()
	if s.byName[hs.hub.Name] != hs {
		s.mu.Unlock()
		return
	}
	owner := make(map[wire.Forward]string)
	for _, o := range s.byID {
		if o != hs {
			for _, fw := range o.fwds {
				owner[fw] = o.hub.Name
			}
		}
	}
	hs.fwdReq = slices.Clone(f.Forwards)
	acc, ref := s.decideForwards(f.Forwards, allow, owner)
	changed := !slices.Equal(acc, hs.fwds)
	hs.fwds = acc
	s.mu.Unlock()
	if changed {
		s.log.Info("port forwards", "hub", hs.hub.Name, "accepted", len(acc), "refused", len(ref))
		go s.applyForwards() // nft runs for tens of ms: not on the packet loop
	}
	if err := hs.sess.Send(&wire.FwdAck{Forwards: acc, Refused: ref}); err != nil {
		s.log.Debug("send FWD_ACK", "hub", hs.hub.Name, "err", err)
	}
}

// applyForwards rebuilds the DNAT rules from every session's forwards.
func (s *Server) applyForwards() {
	s.fwdMu.Lock()
	defer s.fwdMu.Unlock()
	s.mu.Lock()
	var rules []ForwardRule
	for _, hs := range s.byID {
		for _, f := range hs.fwds {
			rules = append(rules, ForwardRule{Proto: f.Proto, Port: f.Port, To: hs.ip})
		}
	}
	s.mu.Unlock()
	slices.SortFunc(rules, func(a, b ForwardRule) int {
		return (int(a.Port)<<8 + int(a.Proto)) - (int(b.Port)<<8 + int(b.Proto))
	})
	ctx, cancel := context.WithTimeout(context.Background(), fwdApplyTimeout)
	defer cancel()
	if err := s.o.Firewall.Forward(ctx, rules); err != nil {
		s.log.Error("port forwards not applied", "err", err)
	}
}

// ForwardAllow implements admin.Backend.
func (s *Server) ForwardAllow(_ context.Context, name string, n int) error {
	if n < 0 || n > MaxForwardsPerHub {
		return fmt.Errorf("forward allow: %d: want 0-%d", n, MaxForwardsPerHub)
	}
	if err := s.o.State.SetForwardAllow(name, n); err != nil {
		return err
	}
	s.log.Info("port forward allowance", "hub", name, "ports", n)
	s.mu.Lock()
	hs := s.byName[name]
	var req []wire.Forward
	if hs != nil {
		req = slices.Clone(hs.fwdReq)
	}
	s.mu.Unlock()
	if hs != nil && req != nil {
		s.onFwdReq(hs, &wire.FwdReq{Forwards: req})
	}
	return nil
}

// adminForwards converts forwards for the admin socket.
func adminForwards(l []wire.Forward) []admin.Forward {
	out := make([]admin.Forward, 0, len(l))
	for _, f := range l {
		out = append(out, admin.Forward{Proto: protoName(f.Proto), Port: f.Port})
	}
	return out
}
