package server

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// Ports are the listening ports a bare public host expands to.
type Ports struct {
	UDP, TCP, WSS []int
	WSSPath       string
}

// Endpoints builds the endpoint list for enrollment codes. Each entry of
// public is a full URL ("udp://host:port", "wss://host/xs") kept as is, a
// "host:port" (→ udp://host:port) or a bare host (→ udp://host:<p> for every
// UDP port, then tcp:// and wss:// for the stream ports: the hub's fallback
// order). With no public entries the address from detect is used.
func Endpoints(public []string, ports Ports, detect func() (netip.Addr, error)) ([]string, error) {
	if len(public) == 0 {
		if detect == nil {
			return nil, fmt.Errorf("no public endpoints configured; set %sPUBLIC_ENDPOINTS", EnvPrefix)
		}
		addr, err := detect()
		if err != nil {
			return nil, fmt.Errorf("detect public address (set %sPUBLIC_ENDPOINTS): %w", EnvPrefix, err)
		}
		public = []string{addr.String()}
	}
	var out []string
	for _, e := range public {
		switch {
		case strings.Contains(e, "://"):
			out = append(out, e)
		case hasPort(e):
			out = append(out, "udp://"+e)
		default:
			host := strings.Trim(e, "[]")
			for _, p := range ports.UDP {
				out = append(out, "udp://"+net.JoinHostPort(host, strconv.Itoa(p)))
			}
			for _, p := range ports.TCP {
				out = append(out, "tcp://"+net.JoinHostPort(host, strconv.Itoa(p)))
			}
			for _, p := range ports.WSS {
				out = append(out, "wss://"+net.JoinHostPort(host, strconv.Itoa(p))+ports.WSSPath)
			}
		}
	}
	return out, nil
}

// WithPublic returns p with the main tunnel port replaced by public in the
// UDP and TCP lists (a router or the HA Network section maps public to main).
// public 0 or equal to main leaves p as is.
func (p Ports) WithPublic(main, public int) Ports {
	if public == 0 || public == main {
		return p
	}
	sub := func(ports []int) []int {
		out := make([]int, len(ports))
		for i, n := range ports {
			out[i] = n
			if n == main {
				out[i] = public
			}
		}
		return out
	}
	p.UDP, p.TCP = sub(p.UDP), sub(p.TCP)
	return p
}

// codePorts returns the ports written into enrollment codes and the tunnel
// port hubs dial (public_port, else the main listen port as bound).
func (s *Server) codePorts() (Ports, int) {
	var ports Ports
	for _, a := range s.Addrs() {
		ports.UDP = append(ports.UDP, int(a.Port()))
	}
	ports.TCP, ports.WSS = s.StreamPorts()
	ports.WSSPath = s.o.Config.WSSPath
	main := s.o.Config.ListenPorts[0]
	if len(ports.UDP) > 0 {
		main = ports.UDP[0]
	}
	tunnel := main
	if s.o.Config.PublicPort != 0 {
		tunnel = s.o.Config.PublicPort
	}
	return ports.WithPublic(main, tunnel), tunnel
}

func hasPort(hostport string) bool {
	_, port, err := net.SplitHostPort(hostport)
	return err == nil && port != ""
}

// DetectOutbound returns the local IPv4 address used for the default route.
// A UDP "connect" sends no packet; it only selects the route. Behind NAT or in
// Docker bridge mode this is a private address, so production servers set
// public_endpoints.
func DetectOutbound() (netip.Addr, error) {
	c, err := net.Dial("udp4", "192.0.2.1:9") // TEST-NET-1, never contacted
	if err != nil {
		return netip.Addr{}, fmt.Errorf("detect outbound address: %w", err)
	}
	defer func() { _ = c.Close() }()
	ap, err := netip.ParseAddrPort(c.LocalAddr().String())
	if err != nil {
		return netip.Addr{}, fmt.Errorf("detect outbound address: %w", err)
	}
	return ap.Addr().Unmap(), nil
}
