package enroll

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/zyndata/cross-stream/internal/wire"
)

// Prefix starts every enrollment code.
const Prefix = "XS1."

// Limits that keep a pasted code bounded.
const (
	MaxCodeLen   = 4096
	MaxEndpoints = 16
	MaxNameLen   = 64
	KeyLen       = 32
)

// ErrCode is wrapped by every enrollment code error.
var ErrCode = errors.New("enroll: invalid code")

// Code is the content of an XS1 enrollment code (protocol.md section 2).
type Code struct {
	Endpoints []string     // e.g. udp://host:51900, tcp://host:51900, wss://host:443/xs
	ServerKey [KeyLen]byte // server static X25519 public key
	Token     Token        // single-use enrollment token
	Name      string       // hub name chosen by the server admin
}

type codeJSON struct {
	V    int      `json:"v"`
	EP   []string `json:"ep"`
	SPK  string   `json:"spk"`
	Tok  string   `json:"tok"`
	Name string   `json:"name"`
}

// Encode returns the XS1 text form of c.
func (c Code) Encode() (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(codeJSON{
		V:    1,
		EP:   c.Endpoints,
		SPK:  base64.StdEncoding.EncodeToString(c.ServerKey[:]),
		Tok:  c.Token.String(),
		Name: c.Name,
	})
	if err != nil {
		return "", fmt.Errorf("encode code: %w", err)
	}
	s := Prefix + base64.RawURLEncoding.EncodeToString(b)
	if len(s) > MaxCodeLen {
		return "", fmt.Errorf("code %d chars: %w", len(s), ErrCode)
	}
	return s, nil
}

// ParseCode decodes a pasted XS1 code. Surrounding whitespace and base64
// padding are accepted.
func ParseCode(s string) (Code, error) {
	s = strings.TrimSpace(s)
	if len(s) > MaxCodeLen {
		return Code{}, fmt.Errorf("code %d chars: %w", len(s), ErrCode)
	}
	rest, ok := strings.CutPrefix(s, Prefix)
	if !ok {
		return Code{}, fmt.Errorf("missing %q prefix: %w", Prefix, ErrCode)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(rest, "="))
	if err != nil {
		return Code{}, fmt.Errorf("code base64: %w", ErrCode)
	}
	var j codeJSON
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&j); err != nil {
		return Code{}, fmt.Errorf("code json: %w", ErrCode)
	}
	if dec.More() {
		return Code{}, fmt.Errorf("code json: trailing data: %w", ErrCode)
	}
	if j.V != 1 {
		return Code{}, fmt.Errorf("code version %d: %w", j.V, ErrCode)
	}
	spk, err := base64.StdEncoding.DecodeString(j.SPK)
	if err != nil || len(spk) != KeyLen {
		return Code{}, fmt.Errorf("server key: %w", ErrCode)
	}
	tok, err := ParseToken(j.Tok)
	if err != nil {
		return Code{}, fmt.Errorf("token: %w", ErrCode)
	}
	c := Code{Endpoints: j.EP, ServerKey: [KeyLen]byte(spk), Token: tok, Name: j.Name}
	if err := c.validate(); err != nil {
		return Code{}, err
	}
	if c.Endpoints, err = normalizeEndpoints(c.Endpoints); err != nil {
		return Code{}, err
	}
	return c, nil
}

func (c Code) validate() error {
	if len(c.Endpoints) == 0 || len(c.Endpoints) > MaxEndpoints {
		return fmt.Errorf("%d endpoints: %w", len(c.Endpoints), ErrCode)
	}
	if _, err := normalizeEndpoints(c.Endpoints); err != nil {
		return err
	}
	if c.ServerKey == [KeyLen]byte{} {
		return fmt.Errorf("zero server key: %w", ErrCode)
	}
	if c.Token == (Token{}) {
		return fmt.Errorf("zero token: %w", ErrCode)
	}
	return validName(c.Name)
}

// normalizeEndpoints checks every endpoint URI and rewrites it in canonical
// form; a legacy "host:port" becomes udp://host:port.
func normalizeEndpoints(eps []string) ([]string, error) {
	out := make([]string, 0, len(eps))
	for _, ep := range eps {
		e, err := wire.ParseEndpoint(ep)
		if err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", ep, ErrCode)
		}
		out = append(out, e.String())
	}
	return out, nil
}

// validName: 1..MaxNameLen bytes of printable characters, not starting with
// "-" (a CLI flag typed after the command became a hub named "-h").
func validName(name string) error {
	if name == "" || len(name) > MaxNameLen {
		return fmt.Errorf("name length %d: %w", len(name), ErrCode)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("name %q starts with '-': %w", name, ErrCode)
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("name has non-printable characters: %w", ErrCode)
		}
	}
	return nil
}
