package admin

import (
	"context"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
)

// Backend is the set of hub operations xs-server implements. Service wraps
// it with audit logging and admin users; xs-ctl (control socket) and the web
// panel both go through Service, so they never diverge.
type Backend interface {
	// Enroll creates a single-use enrollment code for a new hub name.
	Enroll(ctx context.Context, name string) (EnrollResult, error)
	// Hubs lists enrolled hubs.
	Hubs(ctx context.Context) ([]Hub, error)
	// Revoke deletes a hub's key and closes its session at once.
	Revoke(ctx context.Context, name string) error
	// Rename changes an enrolled hub's name (its key and tunnel IP stay).
	Rename(ctx context.Context, name, newName string) error
	// Sessions lists active sessions.
	Sessions(ctx context.Context) ([]Session, error)
	// ForwardAllow sets how many public ports a hub may forward (0-20).
	ForwardAllow(ctx context.Context, name string, n int) error
	// Info describes the server (ports, endpoints, version).
	Info(ctx context.Context) (ServerInfo, error)
	// SetTunnelPort changes the configured tunnel port (applied at restart).
	SetTunnelPort(ctx context.Context, port int) error
	// Restart restarts the server.
	Restart(ctx context.Context) error
}

// ServerInfo is the server's configuration as the panel shows it.
type ServerInfo struct {
	Version       string    `json:"version"`
	Started       time.Time `json:"started"`
	UDPPorts      []int     `json:"udp_ports"`
	TCPPorts      []int     `json:"tcp_ports"`
	WSSPorts      []int     `json:"wss_ports"`
	WSSPath       string    `json:"wss_path"`
	Endpoints     []string  `json:"endpoints"` // put into enrollment codes
	EndpointsErr  string    `json:"endpoints_err,omitempty"`
	MinHubVersion string    `json:"min_hub_version"`
	PEP           bool      `json:"pep"`
	TunnelPrefix  string    `json:"tunnel_prefix"`
	TokenTTL      string    `json:"token_ttl"`
	EgressAllow   []string  `json:"egress_allow"`
	// Tunnel port: in use since the start, and as configured now in its
	// source (the HA app's Network section, server.yaml or the environment).
	TunnelPort           int      `json:"tunnel_port"`
	TunnelPortConfigured int      `json:"tunnel_port_configured"`
	TunnelPortSource     string   `json:"tunnel_port_source"` // ha, file, env, default
	TunnelPortWritable   bool     `json:"tunnel_port_writable"`
	TunnelPortErr        string   `json:"tunnel_port_err,omitempty"`
	RestartRequired      bool     `json:"restart_required"` // configured differs from the one in use
	CanRestart           bool     `json:"can_restart"`
	Reenroll             []string `json:"reenroll"` // hubs enrolled with another tunnel port
}

// Forward is one public port forwarded to a hub.
type Forward struct {
	Proto string `json:"proto"` // tcp or udp
	Port  uint16 `json:"port"`
}

// EnrollResult is a fresh XS1 code and when its token expires.
type EnrollResult struct {
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
}

// Hub is one enrolled hub.
type Hub struct {
	Name      string    `json:"name"`
	TunnelIP  string    `json:"tunnel_ip"`
	PublicKey string    `json:"public_key"`
	Enrolled  time.Time `json:"enrolled"`
	Online    bool      `json:"online"`
	FwdAllow  int       `json:"fwd_allow"`
	Forwards  []Forward `json:"forwards"` // active (session up)
	Port      int       `json:"port"`     // tunnel port in the hub's enrollment code
	Reenroll  bool      `json:"reenroll"` // enrolled with another tunnel port: must enroll again
}

// Session is one active hub session.
type Session struct {
	Hub      string       `json:"hub"`
	TunnelIP string       `json:"tunnel_ip"`
	Paths    []Path       `json:"paths"`
	RxBytes  uint64       `json:"rx_bytes"`
	TxBytes  uint64       `json:"tx_bytes"`
	RxRate   float64      `json:"rx_rate"` // bytes/s, hub→server
	TxRate   float64      `json:"tx_rate"` // bytes/s, server→hub
	LastSeen time.Time    `json:"last_seen"`
	Bond     BondCounters `json:"bond"`
	Forwards []Forward    `json:"forwards"`
}

// BondCounters are the session-wide bonding counters (server side).
type BondCounters struct {
	Queue     int    `json:"queue"`
	Reorder   int    `json:"reorder"`
	Skipped   uint64 `json:"skipped"`
	Dropped   uint64 `json:"dropped"`
	Delivered uint64 `json:"delivered"`
	Dups      uint64 `json:"dups"`
	Late      uint64 `json:"late"` // arrived after their seq was skipped
	// Mode and Redundancy come from the hub's SESSION_CONFIG.
	Mode       string `json:"mode"`
	Redundancy string `json:"redundancy"`
	Redundant  bool   `json:"redundant"`
}

// Path is one path of a session as seen by the server: observed source
// address and the bonding engine's metrics (download direction).
type Path struct {
	ID    uint8          `json:"id"`
	Name  string         `json:"name"` // relay or local link name (hub's PATH_NAME), "" = unknown
	Addr  string         `json:"addr"` // observed source ip:port
	Stats bond.PathStats `json:"stats"`
}

// Operation names on the control socket.
const (
	OpEnroll   = "enroll"
	OpHubs     = "hubs"
	OpRevoke   = "revoke"
	OpSessions = "sessions"
	OpForward  = "forward_allow"
	OpRename   = "rename"
	OpInfo     = "info"
	OpAudit    = "audit"
	// Admin users (web panel logins).
	OpUserAdd    = "admin_add"
	OpUserPasswd = "admin_passwd"
	OpUserDel    = "admin_del"
	OpUserList   = "admin_list"
	OpUserTOTP   = "admin_totp"
	// Tunnel port (configured in its source) and restart.
	OpTunnelPort = "tunnel_port"
	OpRestart    = "restart"
)

// Request is one control-socket request (one JSON object per connection).
type Request struct {
	Op   string `json:"op"`
	Name string `json:"name,omitempty"`
	N    int    `json:"n,omitempty"` // forward_allow: ports; audit: entries; tunnel_port: port
	// NewName is the rename target.
	NewName string `json:"new_name,omitempty"`
	// Password for admin_add/admin_passwd. The socket is root-only (0600)
	// and requests are never logged.
	Password string `json:"password,omitempty"`
	// Off turns TOTP off (admin_totp).
	Off bool `json:"off,omitempty"`
}

// Response is the answer to a Request. Error is set when the operation failed.
type Response struct {
	Error    string        `json:"error,omitempty"`
	Enroll   *EnrollResult `json:"enroll,omitempty"`
	Hubs     []Hub         `json:"hubs,omitempty"`
	Sessions []Session     `json:"sessions,omitempty"`
	Info     *ServerInfo   `json:"info,omitempty"`
	Audit    []AuditEntry  `json:"audit,omitempty"`
	Users    []UserInfo    `json:"users,omitempty"`
	TOTPURI  string        `json:"totp_uri,omitempty"`
}
