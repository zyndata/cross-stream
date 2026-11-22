package wire

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Transport is how a relay reaches a server endpoint.
type Transport string

// Transports (architecture.md "Server transports").
const (
	TransportUDP Transport = "udp"
	TransportTCP Transport = "tcp"
	TransportWSS Transport = "wss"
)

// Rank orders transports for the per-path fallback: UDP → TCP → WSS.
func (t Transport) Rank() int {
	switch t {
	case TransportUDP:
		return 0
	case TransportTCP:
		return 1
	case TransportWSS:
		return 2
	}
	return 3
}

// Stream reports whether the transport is a reliable byte stream (TCP, WSS).
func (t Transport) Stream() bool { return t == TransportTCP || t == TransportWSS }

// DefaultWSSPath is the URL path of the WebSocket endpoint.
const DefaultWSSPath = "/xs"

// ErrEndpoint is returned for a malformed endpoint URI.
var ErrEndpoint = errors.New("wire: bad endpoint")

// Endpoint is a server endpoint URI: udp://host:port, tcp://host:port or
// wss://host[:port]/path. A bare host:port means udp://host:port.
type Endpoint struct {
	Transport Transport
	Host      string // name or IP (IPv6 without brackets)
	Port      uint16
	Path      string // wss only, starts with "/"
}

// ParseEndpoint parses an endpoint URI (or a legacy host:port).
func ParseEndpoint(s string) (Endpoint, error) {
	if !strings.Contains(s, "://") {
		s = "udp://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return Endpoint{}, fmt.Errorf("%w %q", ErrEndpoint, s)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return Endpoint{}, fmt.Errorf("%w %q: only scheme://host:port[/path]", ErrEndpoint, s)
	}
	e := Endpoint{Transport: Transport(u.Scheme), Host: u.Hostname()}
	if !validHost(e.Host) {
		return Endpoint{}, fmt.Errorf("%w %q: host", ErrEndpoint, s)
	}
	port := u.Port()
	switch e.Transport {
	case TransportUDP, TransportTCP:
		if port == "" || (u.Path != "" && u.Path != "/") {
			return Endpoint{}, fmt.Errorf("%w %q: want %s://host:port", ErrEndpoint, s, u.Scheme)
		}
	case TransportWSS:
		if port == "" {
			port = "443"
		}
		e.Path = u.Path
		if e.Path == "" {
			e.Path = "/"
		}
		if !validPath(e.Path) {
			return Endpoint{}, fmt.Errorf("%w %q: path", ErrEndpoint, s)
		}
	default:
		return Endpoint{}, fmt.Errorf("%w %q: scheme %q", ErrEndpoint, s, u.Scheme)
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil || p == 0 {
		return Endpoint{}, fmt.Errorf("%w %q: port", ErrEndpoint, s)
	}
	e.Port = uint16(p)
	return e, nil
}

// validHost accepts a DNS name or an IP address (IPv6 without zone).
func validHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	if strings.Contains(h, ":") {
		a, err := netip.ParseAddr(h)
		return err == nil && a.Is6() && a.Zone() == ""
	}
	for _, r := range h {
		if !nameRune(r) {
			return false
		}
	}
	return true
}

// nameRune: letters, digits, '.', '-', '_'.
func nameRune(r rune) bool {
	switch {
	case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return true
	}
	return r == '.' || r == '-' || r == '_'
}

// validPath accepts plain URL paths ("/xs"), nothing that needs escaping.
func validPath(p string) bool {
	if !strings.HasPrefix(p, "/") || len(p) > 128 {
		return false
	}
	for _, r := range p {
		if !nameRune(r) && r != '/' && r != '~' {
			return false
		}
	}
	return true
}

// HostPort returns "host:port" for dialing.
func (e Endpoint) HostPort() string {
	return net.JoinHostPort(e.Host, strconv.Itoa(int(e.Port)))
}

// String formats the endpoint as a URI; ParseEndpoint(e.String()) == e.
func (e Endpoint) String() string {
	s := string(e.Transport) + "://" + e.HostPort()
	if e.Transport == TransportWSS {
		s += e.Path
	}
	return s
}
