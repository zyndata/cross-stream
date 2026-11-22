package main

import (
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// IPC names of xs-hubd added in phase 12 (internal/hub/rpc_ext.go).
const (
	methodUplinksUpdate = "uplinks.update"
	methodUplinksForget = "uplinks.forget"
	// phase 19
	methodUplinksCaptive = "uplinks.captive"
)

// uplinkStatus mirrors hub.UplinkStatus (status.uplinks).
type uplinkStatus struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Kind      string        `json:"kind"` // usb, bluetooth, modem, ethernet, wifi
	Iface     string        `json:"iface"`
	Enabled   bool          `json:"enabled"`
	TTL       bool          `json:"ttl"`
	Path      pathSettings  `json:"path"`
	Limits    UplinkLimits  `json:"limits"`
	State     string        `json:"state"` // off, absent, limit, connecting, up
	Present   bool          `json:"present"`
	Addr      string        `json:"addr"`
	Transport string        `json:"transport"`
	UsedNow   uint64        `json:"used_period_bytes"`
	Bond      *pathStats    `json:"bond"`
	Modem     *modemInfo    `json:"modem"`
	SameIPAs  string        `json:"same_ip_as"`
	Flows     int           `json:"direct_flows"`
	Captive   *captiveProbe `json:"captive"`
}

// captiveProbe mirrors hub.CaptiveStatus (the login-page probe, phase 19).
type captiveProbe struct {
	State      string `json:"state"` // open, captive, no_internet
	URL        string `json:"url"`
	Rechecking bool   `json:"rechecking"`
}

// captiveResult mirrors hub.CaptiveResult.
type captiveResult struct {
	State    string   `json:"state"`
	URL      string   `json:"url"`
	Route    []string `json:"route"`
	RouteErr string   `json:"route_err"`
	Hint     string   `json:"hint"`
}

// UplinkLimits are the data limits of a local link (as in the relay app).
type UplinkLimits struct {
	CapMB        int64  `json:"cap_mb"`
	CapPeriod    string `json:"cap_period"` // session, day, month
	ResetDay     int    `json:"reset_day"`
	OverCap      string `json:"over_cap"` // stop, throttle
	ThrottleKbps int    `json:"throttle_kbps"`
}

type modemInfo struct {
	Tech     string  `json:"tech"`
	Operator string  `json:"operator"`
	Quality  int     `json:"quality"`
	RSRP     float64 `json:"rsrp_dbm"`
}

// UplinkView is one row of the "Łącza lokalne" section.
type UplinkView struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Kind        string       `json:"kind"`
	Iface       string       `json:"iface"`
	Enabled     bool         `json:"enabled"`
	TTL         bool         `json:"ttl"`
	State       string       `json:"state"`
	Present     bool         `json:"present"`
	Addr        string       `json:"addr,omitempty"`
	Transport   string       `json:"transport,omitempty"`
	PathState   string       `json:"path_state,omitempty"`
	UpMbps      float64      `json:"up_mbps"`
	DownMbps    float64      `json:"down_mbps"`
	HasPath     bool         `json:"has_path"`
	RTTms       float64      `json:"rtt_ms"`
	LossPct     float64      `json:"loss_pct"`
	Priority    string       `json:"priority"`
	Up          string       `json:"up"`
	Down        string       `json:"down"`
	RateKbps    uint32       `json:"rate_kbps"`
	Limits      UplinkLimits `json:"limits"`
	UsedMB      float64      `json:"used_mb"` // current cap period
	Tech        string       `json:"tech,omitempty"`
	Operator    string       `json:"operator,omitempty"`
	SignalDBm   int          `json:"signal_dbm"` // modem RSRP, 0 = unknown
	Quality     int          `json:"quality"`    // modem signal quality %, 0 = unknown
	WeakCell    bool         `json:"weak_cell"`
	SameIPAs    string       `json:"same_ip_as,omitempty"`
	DirectFlows int          `json:"direct_flows"`
	// NoInternet: the last probe found no internet behind the link (the
	// login page state is State "captive").
	NoInternet bool `json:"no_internet,omitempty"`
}

// UplinkParams are what the frontend changes on a local link; nil fields
// keep their value.
type UplinkParams struct {
	Uplink     string        `json:"uplink"`
	Name       *string       `json:"name,omitempty"`
	Enabled    *bool         `json:"enabled,omitempty"`
	TTL        *bool         `json:"ttl,omitempty"`
	Priority   *string       `json:"priority,omitempty"`
	RateKbps   *uint32       `json:"rate_kbps,omitempty"`
	Up         *string       `json:"up,omitempty"`
	Down       *string       `json:"down,omitempty"`
	Limits     *UplinkLimits `json:"limits,omitempty"`
	ResetUsage bool          `json:"reset_usage,omitempty"`
}

// SetUplink changes a local link's settings.
func (a *App) SetUplink(p UplinkParams) error {
	return a.call(callTimeout, methodUplinksUpdate, p, nil)
}

// ForgetUplink removes a local link from the hub's list.
func (a *App) ForgetUplink(id string) error {
	return a.call(callTimeout, methodUplinksForget, map[string]string{"uplink": id}, nil)
}

// CaptiveLogin checks a local link for a login page now and, when there
// is one, routes its host through the link and opens it in the default
// browser. It returns "" when the page was opened, else a message for the
// owner (internet works / no internet / the hint).
func (a *App) CaptiveLogin(id string) (string, error) {
	var r captiveResult
	if err := a.call(captiveTimeout, methodUplinksCaptive, map[string]any{"uplink": id, "open": true}, &r); err != nil {
		return "", err
	}
	return a.captiveOpen(r), nil
}

// captiveTimeout covers the probe and the portal host lookup (5 s each).
const captiveTimeout = 15 * time.Second

func (a *App) captiveOpen(r captiveResult) string {
	switch {
	case r.State == "open":
		return "Internet działa — logowanie nie jest potrzebne"
	case r.State != "captive":
		return "Brak internetu na tym łączu"
	case r.RouteErr != "" || r.URL == "":
		return or(r.Hint, "Zaloguj się w tej sieci na telefonie lub wyłącz hub na chwilę")
	}
	if a.openURL != nil {
		a.openURL(r.URL)
	} else {
		runtime.BrowserOpenURL(a.context(), r.URL)
	}
	return ""
}

// uplink turns one status entry into a row; the weak-cellular hint of a
// modem uses the same thresholds and debouncing as relays.
func (t *tracker) uplink(u uplinkStatus) UplinkView {
	uv := UplinkView{ID: u.ID, Name: u.Name, Kind: u.Kind, Iface: u.Iface, Enabled: u.Enabled, TTL: u.TTL,
		State: u.State, Present: u.Present, Addr: u.Addr, Transport: u.Transport,
		Priority: or(u.Path.Priority, defaultPrio), Up: or(u.Path.Up, defaultDirOn), Down: or(u.Path.Down, defaultDirOn),
		RateKbps: u.Path.RateKbps, Limits: u.Limits, UsedMB: float64(u.UsedNow) / 1e6, SameIPAs: u.SameIPAs,
		DirectFlows: u.Flows}
	uv.NoInternet = u.Captive != nil && u.Captive.State == "no_internet"
	uv.Limits.CapPeriod = or(uv.Limits.CapPeriod, "session")
	uv.Limits.OverCap = or(uv.Limits.OverCap, "stop")
	if b := u.Bond; b != nil {
		uv.PathState = b.State
		uv.UpMbps, uv.DownMbps = float64(b.TxRate)/bytesPerMbit, float64(b.RxRate)/bytesPerMbit
		if b.SRTT > 0 {
			uv.HasPath, uv.RTTms, uv.LossPct = true, ms(b.SRTT), b.Loss*100
		}
	}
	if m := u.Modem; m != nil {
		uv.Tech, uv.Operator, uv.SignalDBm, uv.Quality = strings.ToUpper(m.Tech), m.Operator, int(m.RSRP), m.Quality
	}
	if u.Kind == "modem" && u.State == "up" {
		s := &relayStats{SignalDBm: uv.SignalDBm, CellularOK: true}
		_, uv.WeakCell, _ = t.relayHints(u.ID, s, uv.HasPath, u.Bond)
	} else {
		delete(t.hints, u.ID)
	}
	return uv
}
