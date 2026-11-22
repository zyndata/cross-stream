package tun

import (
	"errors"
	"net/netip"
)

// ErrClosed is returned by Read and Write after Close.
var ErrClosed = errors.New("tun: device closed")

// Device is a TUN interface that carries one IP packet per Read or Write.
type Device interface {
	// Name returns the interface name (e.g. "xss0").
	Name() string
	// MTU returns the interface MTU.
	MTU() int
	// Read reads one IP packet into p and returns its length.
	Read(p []byte) (int, error)
	// Write writes one IP packet. It is safe for concurrent use.
	Write(p []byte) (int, error)
	// Close removes the interface and unblocks Read.
	Close() error
}

// Config is the address setup of a TUN interface.
type Config struct {
	Name   string       // interface name, e.g. "xss0" or "xs0"
	MTU    int          // interface MTU
	Prefix netip.Prefix // local address and netmask, e.g. 100.64.0.1/16
	// DNS servers of the interface (Windows only: Mobile Hotspot gives its
	// clients a DNS server only when the shared connection has one).
	DNS []netip.Addr
}

func (c Config) validate() error {
	if c.Name == "" {
		return errors.New("tun: empty interface name")
	}
	if c.MTU < 576 || c.MTU > 65535 {
		return errors.New("tun: MTU out of range 576..65535")
	}
	if !c.Prefix.IsValid() || !c.Prefix.Addr().Is4() {
		return errors.New("tun: prefix must be a valid IPv4 prefix")
	}
	return nil
}
