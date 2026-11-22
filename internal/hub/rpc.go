package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/hub/hublog"
	"github.com/zyndata/cross-stream/internal/ipc"
	"github.com/zyndata/cross-stream/internal/pairing"
)

// IPC methods of xs-hubd.
const (
	MethodStatus          = "status"
	MethodRelaysList      = "relays.list"
	MethodRelaysUpdate    = "relays.update"
	MethodRelaysForget    = "relays.forget"
	MethodPairingQR       = "pairing.qr"
	MethodServerEnroll    = "server.enroll"
	MethodHotspotGet      = "hotspot.get"
	MethodHotspotSet      = "hotspot.set"
	MethodEventsSubscribe = "events.subscribe"
	MethodHotspotPower    = "hotspot.power"
	MethodSettingsGet     = "settings.get"
	MethodSettingsSet     = "settings.set"
	MethodSpeedTestRun    = "speedtest.run"
	MethodSpeedTestList   = "speedtest.results"
	// EventStatus is the notification events.subscribe sends every second
	// (params = Status); typed events come as EventNotice (events.go).
	EventStatus = "events.status"
	// EventEvery is the events.subscribe interval.
	EventEvery = time.Second
	qrPNGSize  = 320
)

// Status is the result of MethodStatus.
type Status struct {
	Hub       string    `json:"hub"`
	Mode      Mode      `json:"mode"`
	ModeSince time.Time `json:"mode_since,omitzero"`
	Clients   int       `json:"clients"` // hotspot stations
	Inhibited bool      `json:"sleep_inhibited"`
	Enrolled  bool      `json:"enrolled"`
	// EnrollPending: re-enrolled, the new server has not answered yet (the
	// tunnel keeps trying with the code; TunnelIP is empty until then).
	EnrollPending bool          `json:"enroll_pending,omitempty"`
	TunnelIP      string        `json:"tunnel_ip,omitempty"`
	TunnelUp      bool          `json:"tunnel_up"`
	TunnelError   string        `json:"tunnel_error,omitempty"`
	ServerEP      string        `json:"server_ep,omitempty"`
	TunnelMTU     int           `json:"tunnel_mtu,omitempty"`
	Bond          *BondStatus   `json:"bond,omitempty"`
	Direct        *DirectStatus `json:"direct,omitempty"`
	PEP           *PEPStatus    `json:"pep,omitempty"`
	Warnings      []string      `json:"warnings,omitempty"`
	// OverflowMbps is the hub-wide overflow setting (settings.get).
	OverflowMbps uint16 `json:"overflow_mbps"`
	// BondMode (max_throughput / min_latency) and Redundancy (off / auto /
	// on) are settings; Redundant is true while every packet (on) or the
	// real-time ones (auto, triggered) are duplicated.
	BondMode   string        `json:"bond_mode"`
	Redundancy string        `json:"redundancy"`
	Redundant  bool          `json:"redundant"`
	Testing    bool          `json:"speed_test_running"`
	Relays     []RelayStatus `json:"relays"`
	// Uplinks are the hub.s own known local links (phase 12).
	Uplinks []UplinkStatus `json:"uplinks"`
	// LogLevel is the daemon's log level and when it reverts (phase 19).
	LogLevel *hublog.LevelState `json:"log_level,omitempty"`
}

// BondStatus are the hub-side bonding counters of the session.
type BondStatus struct {
	Queue     int    `json:"queue"`
	Reorder   int    `json:"reorder"`
	Skipped   uint64 `json:"skipped"`
	Dropped   uint64 `json:"dropped"`
	Delivered uint64 `json:"delivered"`
	Dups      uint64 `json:"dups"`
}

// RelayStatus is one paired relay in Status.
type RelayStatus struct {
	pairing.Relay
	Connected bool            `json:"connected"`
	Since     time.Time       `json:"since,omitzero"`
	DataAddr  string          `json:"data_addr,omitempty"`
	Endpoint  string          `json:"endpoint,omitempty"`
	Transport string          `json:"transport,omitempty"` // udp, tcp or wss (from Endpoint)
	Observed  string          `json:"observed,omitempty"`  // public address the server sees (PATH_INFO)
	Stats     *control.Stats  `json:"stats,omitempty"`
	Bond      *bond.PathStats `json:"bond,omitempty"` // path metrics (upload direction)
	// DirectFlows are the open DIRECT flows through this relay.
	DirectFlows int `json:"direct_flows"`
	// LastReason and LastSeen: why and when the relay last disconnected
	// (since xs-hubd started), e.g. "leaving: limit".
	LastReason string    `json:"last_reason,omitempty"`
	LastSeen   time.Time `json:"last_seen,omitzero"`
	// Priority is the effective priority: the owner's choice lowered to the
	// relay's limits.max_priority.
	Priority string `json:"priority"`
}

// PathParams are the params of relays.update; nil fields keep their value.
type PathParams struct {
	Relay    string  `json:"relay"` // id or name
	Priority *string `json:"priority,omitempty"`
	RateKbps *uint32 `json:"rate_kbps,omitempty"`
	CapMB    *uint32 `json:"cap_mb,omitempty"`
	Up       *string `json:"up,omitempty"`
	Down     *string `json:"down,omitempty"`
}

func (p PathParams) apply(s *pairing.PathSettings) {
	if p.Priority != nil {
		s.Priority = *p.Priority
	}
	if p.RateKbps != nil {
		s.RateKbps = *p.RateKbps
	}
	if p.CapMB != nil {
		s.CapMB = *p.CapMB
	}
	if p.Up != nil {
		s.Up = *p.Up
	}
	if p.Down != nil {
		s.Down = *p.Down
	}
}

// PairQR is the result of pairing.qr.
type PairQR struct {
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
	PNG     string    `json:"png,omitempty"` // QR code image, base64 PNG
}

// QRParams are the optional params of pairing.qr.
type QRParams struct {
	NoPNG bool `json:"no_png,omitempty"`
}

// ForgetParams are the params of relays.forget.
type ForgetParams struct {
	Relay string `json:"relay"` // id or name
}

// EnrollParams are the params of server.enroll.
type EnrollParams struct {
	Code string `json:"code"`
}

func (d *Daemon) handleRPC(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case MethodStatus:
		return d.Status(), nil
	case MethodRelaysList:
		return d.Status().Relays, nil
	case MethodPairingQR:
		var p QRParams
		if len(params) > 0 && json.Unmarshal(params, &p) != nil {
			return nil, fmt.Errorf("pairing.qr: params {\"no_png\": bool}")
		}
		return d.PairQR(!p.NoPNG)
	case MethodRelaysForget:
		var p ForgetParams
		if err := json.Unmarshal(params, &p); err != nil || p.Relay == "" {
			return nil, fmt.Errorf("relays.forget: need {\"relay\": id or name}")
		}
		return d.Forget(p.Relay)
	case MethodServerEnroll:
		var p EnrollParams
		if err := json.Unmarshal(params, &p); err != nil || p.Code == "" {
			return nil, fmt.Errorf("server.enroll: need {\"code\": XS1 code}")
		}
		return d.Enroll(ctx, p.Code)
	case MethodRelaysUpdate:
		var p PathParams
		if err := json.Unmarshal(params, &p); err != nil || p.Relay == "" {
			return nil, fmt.Errorf("relays.update: need {\"relay\": id or name, ...}")
		}
		return d.SetPath(p.Relay, p)
	case MethodHotspotGet:
		return d.HotspotGet(), nil
	case MethodHotspotSet:
		var p HotspotParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("hotspot.set: %w", err)
		}
		return d.HotspotSet(ctx, p)
	case MethodHotspotPower:
		var p HotspotPowerParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("hotspot.power: need {\"on\": bool}")
		}
		return d.HotspotPower(ctx, p.On)
	case MethodSettingsGet:
		return d.Settings(), nil
	case MethodSettingsSet:
		var p SettingsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("settings.set: %w", err)
		}
		return d.SetSettings(p)
	case MethodSpeedTestRun:
		var p SpeedTestParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("speedtest.run: %w", err)
			}
		}
		return d.SpeedTest(ctx, p)
	case MethodSpeedTestList:
		return d.SpeedTests()
	case MethodEventsSubscribe:
		return ipc.Stream(d.streamEvents), nil
	}
	return d.handleRPCExt(ctx, method, params)
}

// PairQR returns the current XSR1 pairing code, when it expires and, with
// png, the QR code as a base64 PNG for the GUI.
func (d *Daemon) PairQR(png bool) (PairQR, error) {
	p, exp, err := d.issuer.Current()
	if err != nil {
		return PairQR{}, err
	}
	code, err := p.Encode()
	if err != nil {
		return PairQR{}, err
	}
	q := PairQR{Code: code, Expires: exp}
	if png {
		img, err := qrcode.Encode(code, qrcode.Medium, qrPNGSize)
		if err != nil {
			return PairQR{}, fmt.Errorf("pairing qr: %w", err)
		}
		q.PNG = base64.StdEncoding.EncodeToString(img)
	}
	return q, nil
}

// PairCode returns only the current XSR1 code.
func (d *Daemon) PairCode() (string, error) {
	q, err := d.PairQR(false)
	return q.Code, err
}

// Status returns the hub state for xs-hubctl and the GUI.
func (d *Daemon) Status() Status {
	p, _, _ := d.issuer.Current()
	d.mu.Lock()
	st := Status{Hub: p.Hub, Enrolled: d.enr != nil, ServerEP: d.serverEP, Clients: d.clients, Inhibited: d.inhibit,
		OverflowMbps: uint16(d.overflow.Load()), Relays: []RelayStatus{}}
	set := d.settings.Load()
	st.BondMode, st.Redundancy = set.Mode, set.Redundancy
	st.Mode, st.ModeSince = d.modes.Mode()
	if d.enr != nil {
		st.EnrollPending = d.enr.pending()
		if !st.EnrollPending {
			st.TunnelIP = d.enr.TunnelIP
		}
	}
	if d.tunnelErr != nil {
		st.TunnelError = d.tunnelErr.Error()
	}
	conn := make(map[string]*relayPath, len(d.relays))
	eps := make(map[string]string, len(d.relays))
	for id, rc := range d.relays {
		conn[id], eps[id] = rc, rc.ep
	}
	gone := make(map[string]goneInfo, len(d.gone))
	for id, g := range d.gone {
		gone[id] = g
	}
	d.mu.Unlock()
	var rep PathReport
	if c := d.client.Load(); c != nil {
		st.TunnelUp = c.Up() && time.Since(c.LastRecv()) < TunnelFresh
		rep = c.Report()
	}
	var perRelay map[string]int
	st.Direct, perRelay = d.directStatus()
	st.PEP = d.pepStatus()
	paths := make(map[uint8]bond.PathStats)
	if rep.Up {
		st.TunnelMTU = rep.TunnelMTU
		b := rep.Bond
		st.Bond = &BondStatus{Queue: b.Queue, Reorder: b.Reorder, Skipped: b.Skipped, Dropped: b.Dropped,
			Delivered: b.Delivered, Dups: b.Dups}
		st.Redundant = b.Redundant
		for _, p := range b.Paths {
			paths[p.ID] = p
		}
	}
	for _, rec := range d.reg.List() {
		rs := RelayStatus{Relay: rec, DirectFlows: perRelay[rec.ID], Priority: effectivePriority(rec)}
		if g, ok := gone[rec.ID]; ok {
			rs.LastReason, rs.LastSeen = g.Reason, g.At
		}
		if rc := conn[rec.ID]; rc != nil {
			rs.Connected, rs.Since, rs.Stats, rs.Endpoint = true, rc.since, rc.stats.Load(), eps[rec.ID]
			rs.Transport = transportOf(rs.Endpoint)
			if a := rc.data.Load(); a != nil {
				rs.DataAddr = a.String()
			}
		}
		if p, ok := paths[rec.PathID]; ok { // also while the relay is away: DEAD, probed
			rs.Bond = &p
		}
		if a, ok := rep.Observed[rec.PathID]; ok {
			rs.Observed = a.String()
		}
		st.Relays = append(st.Relays, rs)
	}
	st.Uplinks = d.uplinkStatus(rep, perRelay)
	st.Warnings = sharedIPWarnings(st.Relays, st.Uplinks)
	if d.cfg.LogLevel != nil {
		ls := d.cfg.LogLevel.State()
		st.LogLevel = &ls
	}
	return st
}
