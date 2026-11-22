package pairing

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
)

// Prefix starts every pairing code.
const Prefix = "XSR1."

// Version is the payload "v" field.
const Version = 1

// ErrPayload is wrapped by every pairing code decode error.
var ErrPayload = errors.New("pairing: invalid XSR1 code")

// Payload is the JSON inside an XSR1 pairing code (protocol.md section 3).
// SSID, BSSID and PSK are empty when the hub runs headless (no hotspot).
type Payload struct {
	V     int    `json:"v"`
	Hub   string `json:"hub"`
	SSID  string `json:"ssid"`
	BSSID string `json:"bssid"`
	PSK   string `json:"psk"`
	IP    string `json:"ip"`
	CPort int    `json:"cport"`
	DPort int    `json:"dport"`
	HPK   string `json:"hpk"` // hub control-channel static public key, base64
	Tok   string `json:"tok"` // pairing token, base64 (16 bytes)
}

// Encode returns the XSR1 code for p.
func (p Payload) Encode() (string, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode pairing code: %w", err)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// Decode parses and validates an XSR1 code.
func Decode(code string) (Payload, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(code), Prefix)
	if !ok {
		return Payload{}, fmt.Errorf("missing %s prefix: %w", Prefix, ErrPayload)
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return Payload{}, fmt.Errorf("base64: %w: %w", ErrPayload, err)
	}
	var p Payload
	if err := json.Unmarshal(b, &p); err != nil {
		return Payload{}, fmt.Errorf("json: %w: %w", ErrPayload, err)
	}
	if err := p.validate(); err != nil {
		return Payload{}, err
	}
	return p, nil
}

// HubKey returns the decoded hub control-channel key.
func (p Payload) HubKey() ([crypto.KeyLen]byte, error) {
	k, err := crypto.ParseKey(p.HPK)
	if err != nil {
		return k, fmt.Errorf("hpk: %w: %w", ErrPayload, err)
	}
	return k, nil
}

// ControlAddr returns the hub control endpoint (ip:cport); the zero
// (invalid) AddrPort for a payload whose IP does not parse.
func (p Payload) ControlAddr() netip.AddrPort { return p.addr(p.CPort) }

// DataAddr returns the hub data endpoint (ip:dport); the zero (invalid)
// AddrPort for a payload whose IP does not parse.
func (p Payload) DataAddr() netip.AddrPort { return p.addr(p.DPort) }

func (p Payload) addr(port int) netip.AddrPort {
	ip, err := netip.ParseAddr(p.IP)
	if err != nil {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(ip, uint16(port))
}

func (p Payload) validate() error {
	if p.V != Version {
		return fmt.Errorf("version %d: %w", p.V, ErrPayload)
	}
	if p.Hub == "" || len(p.Hub) > 64 || len(p.SSID) > 32 || len(p.PSK) > 63 {
		return fmt.Errorf("hub/ssid/psk length: %w", ErrPayload)
	}
	if p.BSSID != "" {
		if hw, err := net.ParseMAC(p.BSSID); err != nil || len(hw) != 6 {
			return fmt.Errorf("bssid %q: %w", p.BSSID, ErrPayload)
		}
	}
	if ip, err := netip.ParseAddr(p.IP); err != nil || !ip.Is4() {
		return fmt.Errorf("ip %q: %w", p.IP, ErrPayload)
	}
	if p.CPort < 1 || p.CPort > 65535 || p.DPort < 1 || p.DPort > 65535 {
		return fmt.Errorf("ports %d/%d: %w", p.CPort, p.DPort, ErrPayload)
	}
	if _, err := p.HubKey(); err != nil {
		return err
	}
	if _, err := enroll.ParseToken(p.Tok); err != nil {
		return fmt.Errorf("tok: %w: %w", ErrPayload, err)
	}
	return nil
}
