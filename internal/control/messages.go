package control

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrMessage is wrapped by every message decode or validation error.
var ErrMessage = errors.New("control: invalid message")

// Message types ("t" field, protocol.md section 3).
const (
	TypeHello    = "hello"
	TypeWelcome  = "welcome"
	TypeReject   = "reject"
	TypeStats    = "stats"
	TypeLeaving  = "leaving"
	TypeEndpoint = "endpoint"
	TypeMode     = "mode"
	TypeLimits   = "limits"
	TypeForget   = "forget"
)

// Reject reasons.
const (
	RejectBadToken  = "bad_token"
	RejectForgotten = "forgotten"
	RejectHubFull   = "hub_full"
)

// Leaving reasons.
const (
	LeavingLimit      = "limit"
	LeavingBattery    = "battery"
	LeavingUser       = "user"
	LeavingNoCellular = "no_cellular"
	LeavingNoData     = "no_data"
)

// Limit enums.
const (
	PriorityNormal    = "normal"
	PrioritySecondary = "secondary"
	PriorityBackup    = "backup"

	PeriodSession = "session"
	PeriodDay     = "day"
	PeriodMonth   = "month"

	OverCapStop     = "stop"
	OverCapThrottle = "throttle"
)

// Modes of the hub (mode message).
const (
	ModeBonded = "bonded"
	ModeDirect = "direct"
)

// ProxyTokenLen is the size of the proxy session token.
const ProxyTokenLen = 32

// Message is one control-channel message.
type Message interface {
	Type() string
	validate() error
}

// Limits are the relay owner's limits (hello.limits and the limits message).
// Zero values mean "no limit" except where noted.
type Limits struct {
	MaxPriority  string `json:"max_priority,omitempty"` // normal (default), secondary, backup
	CapMB        int64  `json:"cap_mb,omitempty"`       // data cap per period, 0 = none
	CapPeriod    string `json:"cap_period,omitempty"`   // session (default), day, month
	ResetDay     int    `json:"reset_day,omitempty"`    // day of month the month period starts (1-28, default 1)
	UsedMB       int64  `json:"used_mb,omitempty"`      // already used in the current period
	OverCap      string `json:"over_cap,omitempty"`     // stop (default) or throttle
	ThrottleKbps int    `json:"throttle_kbps,omitempty"`
	BatteryMin   int    `json:"battery_min,omitempty"` // percent, 0 = none
}

// Validate checks enum values and ranges.
func (l Limits) Validate() error {
	switch l.MaxPriority {
	case "", PriorityNormal, PrioritySecondary, PriorityBackup:
	default:
		return fmt.Errorf("limits: max_priority %q: %w", l.MaxPriority, ErrMessage)
	}
	switch l.CapPeriod {
	case "", PeriodSession, PeriodDay, PeriodMonth:
	default:
		return fmt.Errorf("limits: cap_period %q: %w", l.CapPeriod, ErrMessage)
	}
	switch l.OverCap {
	case "", OverCapStop:
	case OverCapThrottle:
		if l.ThrottleKbps <= 0 {
			return fmt.Errorf("limits: throttle without throttle_kbps: %w", ErrMessage)
		}
	default:
		return fmt.Errorf("limits: over_cap %q: %w", l.OverCap, ErrMessage)
	}
	if l.CapMB < 0 || l.UsedMB < 0 || l.ThrottleKbps < 0 || l.ResetDay < 0 || l.ResetDay > 28 ||
		l.BatteryMin < 0 || l.BatteryMin > 100 {
		return fmt.Errorf("limits: value out of range: %w", ErrMessage)
	}
	return nil
}

// Hello is the relay's first message (R→H).
type Hello struct {
	RelayName string `json:"relay_name"`
	Carrier   string `json:"carrier,omitempty"`
	PairToken string `json:"pair_token,omitempty"` // first time only
	Limits    Limits `json:"limits"`
}

// Welcome accepts a relay (H→R). ServerEP is empty until the hub is enrolled.
// TCPConns (phase 20) is how many TCP connections a tcp:// ServerEP is
// spread over (0 = 1, at most MaxTCPConns).
type Welcome struct {
	RelayID  string `json:"relay_id"`
	PathID   uint8  `json:"path_id"`
	ServerEP string `json:"server_ep"`
	DataPort int    `json:"data_port"`
	TCPConns int    `json:"tcp_conns,omitempty"`
}

// MaxTCPConns bounds Welcome/Endpoint TCPConns.
const MaxTCPConns = 8

// Reject refuses a relay (H→R); the hub closes the connection after it.
type Reject struct {
	Reason string `json:"reason"`
}

// Stats is sent by the relay every second (R→H).
type Stats struct {
	BytesUp    uint64 `json:"bytes_up"`
	BytesDown  uint64 `json:"bytes_down"`
	Battery    int    `json:"battery"`
	SignalDBm  int    `json:"signal_dbm"`
	NetType    string `json:"net_type,omitempty"`
	CellularOK bool   `json:"cellular_ok"`
	WifiRSSI   int    `json:"wifi_rssi"`
	// TCP connections of a tcp:// server endpoint (phase 20): live count and
	// the connection set's cumulative retransmitted/sent segments.
	TCPConns   int    `json:"tcp_conns,omitempty"`
	TCPRetrans uint64 `json:"tcp_retrans,omitempty"`
	TCPSent    uint64 `json:"tcp_sent,omitempty"`
	// Dropped: packets the relay dropped (filters, limits, unknown sender;
	// cumulative since the relay started) — a hub-side upload diagnostic.
	Dropped uint64 `json:"dropped,omitempty"`
}

// Leaving announces that the relay stops (R→H).
type Leaving struct {
	Reason string `json:"reason"`
}

// Endpoint moves the relay's path to another server endpoint (H→R). The
// same ServerEP with another TCPConns only changes the number of TCP
// connections (no redial of the others).
type Endpoint struct {
	ServerEP string `json:"server_ep"`
	TCPConns int    `json:"tcp_conns,omitempty"`
}

// Mode tells the relay the hub mode and whether the proxy role is on (H→R).
type Mode struct {
	Mode       string `json:"mode"`
	Proxy      bool   `json:"proxy"`
	ProxyToken string `json:"proxy_token,omitempty"` // base64, 32 bytes, when Proxy
}

// LimitsUpdate changes the relay limits without reconnecting (R→H).
type LimitsUpdate struct {
	Limits Limits `json:"limits"`
}

// Forget tells the relay to delete this hub (H→R).
type Forget struct{}

// Unknown is a message with a type this version does not know; it is ignored.
type Unknown struct {
	T string
}

// Type implements Message.
func (*Hello) Type() string { return TypeHello }

// Type implements Message.
func (*Welcome) Type() string { return TypeWelcome }

// Type implements Message.
func (*Reject) Type() string { return TypeReject }

// Type implements Message.
func (*Stats) Type() string { return TypeStats }

// Type implements Message.
func (*Leaving) Type() string { return TypeLeaving }

// Type implements Message.
func (*Endpoint) Type() string { return TypeEndpoint }

// Type implements Message.
func (*Mode) Type() string { return TypeMode }

// Type implements Message.
func (*LimitsUpdate) Type() string { return TypeLimits }

// Type implements Message.
func (*Forget) Type() string { return TypeForget }

// Type implements Message.
func (u *Unknown) Type() string { return u.T }

// MaxNameLen bounds relay names and carrier strings.
const MaxNameLen = 64

func (m *Hello) validate() error {
	if m.RelayName == "" || len(m.RelayName) > MaxNameLen || len(m.Carrier) > MaxNameLen {
		return fmt.Errorf("hello: relay_name/carrier length: %w", ErrMessage)
	}
	return m.Limits.Validate()
}

func (m *Welcome) validate() error {
	if m.RelayID == "" || m.PathID == 0 || m.DataPort < 1 || m.DataPort > 65535 {
		return fmt.Errorf("welcome: relay_id/path_id/data_port: %w", ErrMessage)
	}
	return validConns("welcome", m.TCPConns)
}

func validConns(what string, n int) error {
	if n < 0 || n > MaxTCPConns {
		return fmt.Errorf("%s: tcp_conns %d: %w", what, n, ErrMessage)
	}
	return nil
}

func (m *Reject) validate() error  { return nonEmpty("reject", m.Reason) }
func (m *Leaving) validate() error { return nonEmpty("leaving", m.Reason) }
func (m *Endpoint) validate() error {
	if err := nonEmpty("endpoint", m.ServerEP); err != nil {
		return err
	}
	return validConns("endpoint", m.TCPConns)
}
func (m *Stats) validate() error   { return nil }
func (m *Forget) validate() error  { return nil }
func (u *Unknown) validate() error { return nil }

func (m *Mode) validate() error {
	if m.Mode != ModeBonded && m.Mode != ModeDirect {
		return fmt.Errorf("mode: %q: %w", m.Mode, ErrMessage)
	}
	if m.Proxy {
		tok, err := base64.StdEncoding.DecodeString(m.ProxyToken)
		if err != nil || len(tok) != ProxyTokenLen {
			return fmt.Errorf("mode: proxy_token: %w", ErrMessage)
		}
	}
	return nil
}

func (m *LimitsUpdate) validate() error { return m.Limits.Validate() }

func nonEmpty(what, s string) error {
	if s == "" {
		return fmt.Errorf("%s: empty: %w", what, ErrMessage)
	}
	return nil
}

// Marshal encodes m as a JSON object with its "t" field first.
func Marshal(m Message) ([]byte, error) {
	if _, ok := m.(*Unknown); ok {
		return nil, fmt.Errorf("marshal unknown message: %w", ErrMessage)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", m.Type(), err)
	}
	head := `{"t":"` + m.Type() + `"`
	if len(body) == 2 { // "{}"
		return []byte(head + "}"), nil
	}
	return append([]byte(head+","), body[1:]...), nil
}

// Unmarshal decodes and validates one message. Unknown types decode to
// *Unknown so newer peers can add messages.
func Unmarshal(b []byte) (Message, error) {
	var env struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("unmarshal: %w: %w", ErrMessage, err)
	}
	var m Message
	switch env.T {
	case TypeHello:
		m = &Hello{}
	case TypeWelcome:
		m = &Welcome{}
	case TypeReject:
		m = &Reject{}
	case TypeStats:
		m = &Stats{}
	case TypeLeaving:
		m = &Leaving{}
	case TypeEndpoint:
		m = &Endpoint{}
	case TypeMode:
		m = &Mode{}
	case TypeLimits:
		m = &LimitsUpdate{}
	case TypeForget:
		m = &Forget{}
	case "":
		return nil, fmt.Errorf("unmarshal: no type: %w", ErrMessage)
	default:
		return &Unknown{T: env.T}, nil
	}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w: %w", env.T, ErrMessage, err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return m, nil
}
