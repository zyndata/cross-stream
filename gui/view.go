package main

import (
	"time"
)

// Hint thresholds (docs/decisions.md, phase 09).
const (
	weakWifiDBm   = -75  // relay ↔ hub Wi-Fi RSSI below this is weak (phase 19: was -67)
	weakCellDBm   = -110 // cellular RSRP below this is weak
	weakCellLoss  = 0.05 // path loss above this counts as weak cellular
	hintMarginDB  = 3    // a shown hint clears only this much above the threshold
	hintClearLoss = 0.03 // ... and below this loss
	hintOnAfter   = 5    // statuses (s) a condition must hold before a hint shows
	hintOffAfter  = 15   // statuses (s) it must be gone before the hint clears
	historyLen    = 300  // chart samples (one per status, 5 min)
	bytesPerMbit  = 1_000_000 / 8
	defaultPrio   = "normal"
	defaultDirOn  = "on"
	stateOK       = "ok"
	stateNoDaemon = "daemon" // socket missing or refused: xs-hubd is not running
	stateNoAccess = "group"  // permission denied: user not in group crossstream
	stateError    = "error"
	stateWifi     = "wifi" // xs-hubd waits for the client Wi-Fi choice (View.ClientWifi)
)

// View is what the frontend renders; the backend sends one per status.
type View struct {
	State        string        `json:"state"` // ok, daemon, group, error, wifi
	ClientWifi   *clientWifiQ  `json:"client_wifi,omitempty"`
	Error        string        `json:"error,omitempty"`
	Starting     bool          `json:"starting,omitempty"` // the GUI is starting xs-hubd now
	Lost         bool          `json:"lost,omitempty"`     // xs-hubd ran in this session and stopped
	Hub          string        `json:"hub"`
	Mode         string        `json:"mode"` // OFFLINE, CONNECTING, BONDED, DIRECT
	ModeSince    time.Time     `json:"mode_since"`
	Clients      int           `json:"clients"`
	Enrolled     bool          `json:"enrolled"`
	Pending      bool          `json:"enroll_pending,omitempty"` // re-enrolled, the new server has not answered yet
	TunnelUp     bool          `json:"tunnel_up"`
	TunnelError  string        `json:"tunnel_error,omitempty"`
	ServerEP     string        `json:"server_ep,omitempty"`
	Direct       *directStatus `json:"direct,omitempty"`
	PEP          *pepStatus    `json:"pep,omitempty"`
	Warnings     []string      `json:"warnings,omitempty"`
	OverflowMbps uint16        `json:"overflow_mbps"`
	BondMode     string        `json:"bond_mode"`  // max_throughput, min_latency
	Redundancy   string        `json:"redundancy"` // off, auto, on
	Redundant    bool          `json:"redundant"`  // duplicating now
	Testing      bool          `json:"testing"`    // a speed test runs
	UpMbps       float64       `json:"up_mbps"`
	DownMbps     float64       `json:"down_mbps"`
	History      []Sample      `json:"history"`
	Relays       []RelayView   `json:"relays"`
	Uplinks      []UplinkView  `json:"uplinks"`
	LogLevel     *logLevel     `json:"log_level,omitempty"` // nil: an older xs-hubd
}

// Sample is one point of the throughput chart.
type Sample struct {
	T    int64   `json:"t"` // unix ms
	Up   float64 `json:"up"`
	Down float64 `json:"down"`
}

// RelayView is one row of the relays table.
type RelayView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Carrier     string    `json:"carrier"`
	Connected   bool      `json:"connected"`
	Transport   string    `json:"transport,omitempty"`  // udp, tcp, wss (relay → server)
	PathState   string    `json:"path_state,omitempty"` // bond path state (ALIVE, DEAD, ...)
	UpMbps      float64   `json:"up_mbps"`
	DownMbps    float64   `json:"down_mbps"`
	HasPath     bool      `json:"has_path"` // RTT/jitter/loss are known
	RTTms       float64   `json:"rtt_ms"`
	JitterMs    float64   `json:"jitter_ms"`
	LossPct     float64   `json:"loss_pct"`
	UsedMB      float64   `json:"used_mb"`     // this connection, relay counters
	Battery     int       `json:"battery"`     // -1 = unknown
	NetType     string    `json:"net_type"`    // lte, nr, ...
	SignalDBm   int       `json:"signal_dbm"`  // 0 = unknown
	WifiRSSI    int       `json:"wifi_rssi"`   // 0 = unknown
	WeakWifi    bool      `json:"weak_wifi"`   // ask to move the phone closer to the hub
	WeakCell    bool      `json:"weak_cell"`   // ask to move the phone (window, higher)
	CellularOK  bool      `json:"cellular_ok"` // relay reports working mobile data
	Priority    string    `json:"priority"`
	MaxPriority string    `json:"max_priority"` // the phone owner's cap (lock icon)
	Up          string    `json:"up"`
	Down        string    `json:"down"`
	RateKbps    uint32    `json:"rate_kbps"`
	CapMB       uint32    `json:"cap_mb"`
	Capped      bool      `json:"capped"`
	CapUsedMB   float64   `json:"cap_used_mb"`
	DirectFlows int       `json:"direct_flows"`
	LastReason  string    `json:"last_reason,omitempty"`
	LastSeen    time.Time `json:"last_seen,omitzero"`
}

// tracker turns successive statuses into views: rates from byte counter
// deltas when the tunnel is down, and the chart history.
type tracker struct {
	prev    map[string]counter
	hints   map[string]*relayHints
	history []Sample
}

// relayHints debounce the hints so they do not blink on noisy RSSI/loss.
type relayHints struct {
	wifi, cell, noData debounced
}

// debounced turns on after hintOnAfter true samples in a row and off after
// hintOffAfter false ones.
type debounced struct {
	on bool
	n  int
}

func (d *debounced) update(cond bool) bool {
	if cond == d.on {
		d.n = 0
		return d.on
	}
	d.n++
	if (!d.on && d.n >= hintOnAfter) || (d.on && d.n >= hintOffAfter) {
		d.on, d.n = cond, 0
	}
	return d.on
}

type counter struct {
	up, down uint64
	at       time.Time
}

func newTracker() *tracker {
	return &tracker{prev: make(map[string]counter), hints: make(map[string]*relayHints)}
}

func (t *tracker) view(st hubStatus, now time.Time) View {
	v := View{State: stateOK, Hub: st.Hub, Mode: st.Mode, ModeSince: st.ModeSince, Clients: st.Clients,
		Enrolled: st.Enrolled, Pending: st.Pending, TunnelUp: st.TunnelUp, TunnelError: st.TunnelError, ServerEP: st.ServerEP,
		Direct: st.Direct, PEP: st.PEP, Warnings: st.Warnings, LogLevel: st.LogLevel, OverflowMbps: st.OverflowMbps, BondMode: st.BondMode,
		Redundancy: st.Redundancy, Redundant: st.Redundant, Testing: st.Testing, Relays: []RelayView{}, Uplinks: []UplinkView{}}
	seen := make(map[string]bool, len(st.Relays))
	for _, r := range st.Relays {
		rv := t.relay(r, st.Bond != nil, now)
		seen[r.ID] = true
		v.UpMbps += rv.UpMbps
		v.DownMbps += rv.DownMbps
		v.Relays = append(v.Relays, rv)
	}
	for _, u := range st.Uplinks {
		uv := t.uplink(u)
		seen[u.ID] = true
		v.UpMbps += uv.UpMbps
		v.DownMbps += uv.DownMbps
		v.Uplinks = append(v.Uplinks, uv)
	}
	for id := range t.prev {
		if !seen[id] {
			delete(t.prev, id)
		}
	}
	for id := range t.hints {
		if !seen[id] {
			delete(t.hints, id)
		}
	}
	t.history = append(t.history, Sample{T: now.UnixMilli(), Up: v.UpMbps, Down: v.DownMbps})
	if n := len(t.history) - historyLen; n > 0 {
		t.history = append(t.history[:0], t.history[n:]...)
	}
	v.History = append([]Sample(nil), t.history...)
	return v
}

func (t *tracker) relay(r relayStatus, bonded bool, now time.Time) RelayView {
	rv := RelayView{ID: r.ID, Name: r.Name, Carrier: r.Carrier, Connected: r.Connected, Transport: r.Transport, Battery: -1,
		Priority: or(r.Path.Priority, defaultPrio), MaxPriority: or(r.Limits.MaxPriority, defaultPrio), Up: or(r.Path.Up, defaultDirOn), Down: or(r.Path.Down, defaultDirOn),
		RateKbps: r.Path.RateKbps, CapMB: r.Path.CapMB, DirectFlows: r.DirectFlows,
		LastReason: r.LastReason, LastSeen: r.LastSeen}
	if b := r.Bond; b != nil {
		rv.PathState, rv.Capped, rv.CapUsedMB = b.State, b.Capped, float64(b.Used)/1e6
		if b.SRTT > 0 {
			rv.HasPath = true
			rv.RTTms, rv.JitterMs, rv.LossPct = ms(b.SRTT), ms(b.Jitter), b.Loss*100
		}
	}
	s := r.Stats
	if s == nil || !r.Connected {
		delete(t.prev, r.ID)
		delete(t.hints, r.ID)
		return rv
	}
	rv.UsedMB = float64(s.BytesUp+s.BytesDown) / 1e6
	rv.Battery, rv.NetType, rv.SignalDBm, rv.WifiRSSI, rv.CellularOK = s.Battery, s.NetType, s.SignalDBm, s.WifiRSSI, s.CellularOK
	rv.WeakWifi, rv.WeakCell, rv.CellularOK = t.relayHints(r.ID, s, rv.HasPath, r.Bond)
	if bonded && r.Bond != nil {
		rv.UpMbps, rv.DownMbps = float64(r.Bond.TxRate)/bytesPerMbit, float64(r.Bond.RxRate)/bytesPerMbit
	} else if p, ok := t.prev[r.ID]; ok && now.After(p.at) && s.BytesUp >= p.up && s.BytesDown >= p.down {
		sec := now.Sub(p.at).Seconds()
		rv.UpMbps = float64(s.BytesUp-p.up) / bytesPerMbit / sec
		rv.DownMbps = float64(s.BytesDown-p.down) / bytesPerMbit / sec
	}
	t.prev[r.ID] = counter{up: s.BytesUp, down: s.BytesDown, at: now}
	return rv
}

// relayHints applies thresholds with hysteresis and debouncing; 0 dBm is
// unknown and never weak.
func (t *tracker) relayHints(id string, s *relayStats, hasPath bool, b *pathStats) (weakWifi, weakCell, cellOK bool) {
	h := t.hints[id]
	if h == nil {
		h = &relayHints{}
		t.hints[id] = h
	}
	wifiLimit, cellLimit, lossLimit := weakWifiDBm, weakCellDBm, weakCellLoss
	if h.wifi.on {
		wifiLimit += hintMarginDB
	}
	if h.cell.on {
		cellLimit, lossLimit = cellLimit+hintMarginDB, hintClearLoss
	}
	weakWifi = h.wifi.update(s.WifiRSSI != 0 && s.WifiRSSI < wifiLimit)
	weakCell = h.cell.update((s.SignalDBm != 0 && s.SignalDBm < cellLimit) || (hasPath && b.Loss > lossLimit))
	cellOK = !h.noData.update(!s.CellularOK)
	return weakWifi, weakCell, cellOK
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
