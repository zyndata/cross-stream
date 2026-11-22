package main

import "time"

// The structs below mirror the parts of the xs-hubd IPC JSON the GUI uses
// (internal/hub Status, HotspotInfo, PairQR). The GUI does not import
// internal/hub to keep the daemon's netstack out of the GUI binary;
// status_test.go checks them against the real types.

type hubStatus struct {
	Hub          string         `json:"hub"`
	Mode         string         `json:"mode"`
	ModeSince    time.Time      `json:"mode_since"`
	Clients      int            `json:"clients"`
	Enrolled     bool           `json:"enrolled"`
	Pending      bool           `json:"enroll_pending"`
	TunnelIP     string         `json:"tunnel_ip"`
	TunnelUp     bool           `json:"tunnel_up"`
	TunnelError  string         `json:"tunnel_error"`
	ServerEP     string         `json:"server_ep"`
	Bond         *struct{}      `json:"bond"`
	Direct       *directStatus  `json:"direct"`
	PEP          *pepStatus     `json:"pep"`
	Warnings     []string       `json:"warnings"`
	OverflowMbps uint16         `json:"overflow_mbps"`
	BondMode     string         `json:"bond_mode"`
	Redundancy   string         `json:"redundancy"`
	Redundant    bool           `json:"redundant"`
	Testing      bool           `json:"speed_test_running"`
	Relays       []relayStatus  `json:"relays"`
	Uplinks      []uplinkStatus `json:"uplinks"`
	LogLevel     *logLevel      `json:"log_level"`
}

type directStatus struct {
	Draining bool `json:"draining"`
	TCP      int  `json:"tcp"`
	UDP      int  `json:"udp"`
}

// pepStatus is hub.PEPStatus.
type pepStatus struct {
	On        bool `json:"on"`
	Available bool `json:"available"`
	Streams   int  `json:"streams"`
	Flows     int  `json:"flows"`
}

// pepSettings is the PEP part of hub.Settings (settings.get / settings.set).
type pepSettings struct {
	PEP     string   `json:"pep"`
	Exclude []string `json:"pep_exclude"`
}

type relayStatus struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Carrier     string       `json:"carrier"`
	Path        pathSettings `json:"path"`
	Limits      relayLimits  `json:"limits"`
	Connected   bool         `json:"connected"`
	Transport   string       `json:"transport"`
	Stats       *relayStats  `json:"stats"`
	Bond        *pathStats   `json:"bond"`
	DirectFlows int          `json:"direct_flows"`
	LastReason  string       `json:"last_reason"`
	LastSeen    time.Time    `json:"last_seen"`
	Since       time.Time    `json:"since"`
}

type pathSettings struct {
	Priority string `json:"priority"`
	RateKbps uint32 `json:"rate_kbps"`
	CapMB    uint32 `json:"cap_mb"`
	Up       string `json:"up"`
	Down     string `json:"down"`
}

type relayLimits struct {
	MaxPriority string `json:"max_priority"`
	CapMB       int64  `json:"cap_mb"`
	BatteryMin  int    `json:"battery_min"`
}

type relayStats struct {
	BytesUp    uint64 `json:"bytes_up"`
	BytesDown  uint64 `json:"bytes_down"`
	Battery    int    `json:"battery"`
	SignalDBm  int    `json:"signal_dbm"`
	NetType    string `json:"net_type"`
	CellularOK bool   `json:"cellular_ok"`
	WifiRSSI   int    `json:"wifi_rssi"`
}

type pathStats struct {
	State  string        `json:"state"`
	SRTT   time.Duration `json:"srtt"`
	Jitter time.Duration `json:"jitter"`
	Loss   float64       `json:"loss"`
	TxRate uint64        `json:"tx_rate"`
	RxRate uint64        `json:"rx_rate"`
	Capped bool          `json:"capped"`
	Used   uint64        `json:"used"`
}

// HotspotInfo is the result of hotspot.get / hotspot.set / hotspot.power.
type HotspotInfo struct {
	Enabled  bool            `json:"enabled"`
	On       bool            `json:"on"`
	Settings HotspotSettings `json:"settings"`
	State    struct {
		Iface   string `json:"iface"`
		Band    string `json:"band"`
		Channel int    `json:"channel"`
		// ChannelWhy/Scan: how xs-hubd chose the channel (phase 14).
		ChannelWhy string   `json:"channel_why"`
		Scan       string   `json:"scan"`
		EthIface   string   `json:"eth_iface"`
		Warnings   []string `json:"warnings"`
	} `json:"state"`
	Clients int    `json:"clients"`
	Error   string `json:"error"`
}

// HotspotSettings are the user-settable hotspot fields.
type HotspotSettings struct {
	SSID    string `json:"ssid"`
	PSK     string `json:"psk"`
	Band    string `json:"band"`
	Channel int    `json:"channel"`
	Country string `json:"country"`
	// ClientWifi: share, disconnect or "" (ask) when the card is a Wi-Fi client.
	ClientWifi string `json:"client_wifi"`
}

// PairQR is the result of pairing.qr.
type PairQR struct {
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
	PNG     string    `json:"png"`
}

// SpeedTestParams are the params of speedtest.run.
type SpeedTestParams struct {
	Relay string `json:"relay,omitempty"` // empty = all relays at once
	Dir   string `json:"dir,omitempty"`
	Secs  int    `json:"secs,omitempty"`
	MaxMB uint16 `json:"max_mb,omitempty"`
}

// SpeedPath is one relay's result in one direction.
type SpeedPath struct {
	Relay string  `json:"relay"`
	Dir   string  `json:"dir"`
	Mbps  float64 `json:"mbps"`
	RTTms float64 `json:"rtt_ms"`
	Loss  float64 `json:"loss"`
	// AtLeast: Mbps is a lower bound (the path took everything the test sent).
	AtLeast bool `json:"at_least,omitempty"`
}

// SpeedResult is the result of speedtest.run (and one entry of speedtest.results).
type SpeedResult struct {
	At       time.Time   `json:"at"`
	Relay    string      `json:"relay"`
	Secs     int         `json:"secs"`
	MaxMB    uint16      `json:"max_mb"`
	Paths    []SpeedPath `json:"paths"`
	DownMbps float64     `json:"down_mbps"`
	UpMbps   float64     `json:"up_mbps"`
	SentMB   float64     `json:"sent_mb"`
}

// Enrollment is the result of server.enroll.
type Enrollment struct {
	Name      string   `json:"name"`
	Endpoints []string `json:"endpoints"`
	TunnelIP  string   `json:"tunnel_ip"`
	MTU       int      `json:"mtu"`
	Pending   bool     `json:"pending"` // the new server has not answered yet
}
