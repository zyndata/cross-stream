package wire

import (
	"fmt"
	"net/netip"
)

// Direct-mode proxy request on relay TCP 51903 (protocol.md section 3, "Proxy
// role"): session_token (32 B) | atyp u8 | addr | port u16. The relay answers
// with one reply byte, then the stream is raw.
const (
	ProxyTokenLen   = 32
	ProxyAtypIPv4   = 1
	ProxyRequestLen = ProxyTokenLen + 1 + 4 + 2 // IPv4 request
)

// Proxy reply codes (SOCKS5-like, decisions.md phase 06).
const (
	ProxyOK          byte = 0x00
	ProxyFailure     byte = 0x01 // general failure
	ProxyNotAllowed  byte = 0x02 // bad session token or destination not allowed
	ProxyUnreachable byte = 0x04 // network or host unreachable, or dial timeout
	ProxyRefused     byte = 0x05 // connection refused
	ProxyBadAddress  byte = 0x08 // address type not supported
	ProxyCapReached  byte = 0x09 // the relay's data cap is reached
)

// ProxyRequest is the header the hub sends on a new proxy connection.
type ProxyRequest struct {
	Token [ProxyTokenLen]byte
	Dst   netip.AddrPort // IPv4 only
}

// AppendProxyRequest appends the encoding of r to b. On error b is returned
// unchanged.
func AppendProxyRequest(b []byte, r ProxyRequest) ([]byte, error) {
	start := len(b)
	b = append(b, r.Token[:]...)
	b = append(b, ProxyAtypIPv4)
	b, err := appendAddrPort4(b, r.Dst)
	if err != nil {
		return b[:start], fmt.Errorf("proxy request: %w", err)
	}
	return b, nil
}

// ParseProxyRequest decodes an IPv4 proxy request of exactly ProxyRequestLen
// bytes. An address type other than IPv4 returns ErrType (reply
// ProxyBadAddress).
func ParseProxyRequest(b []byte) (ProxyRequest, error) {
	var r ProxyRequest
	if len(b) < ProxyTokenLen+1 {
		return r, fmt.Errorf("proxy request: %d bytes: %w", len(b), ErrShort)
	}
	copy(r.Token[:], b)
	if atyp := b[ProxyTokenLen]; atyp != ProxyAtypIPv4 {
		return r, fmt.Errorf("proxy request: address type %d: %w", atyp, ErrType)
	}
	if len(b) != ProxyRequestLen {
		return r, fmt.Errorf("proxy request: %d bytes: %w", len(b), ErrLength)
	}
	r.Dst = addrPort4(b[ProxyTokenLen+1:])
	return r, nil
}
