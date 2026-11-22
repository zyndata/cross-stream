// Package xscore is the gomobile API of the CrossStream relay core (Android
// app). It is a thin wrapper over internal/relay that uses only types gomobile
// can bind: string, bool, int, int64, error and interfaces.
package xscore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/relay"
)

// Stop kinds passed to Callback.OnStopped.
const (
	StopUser      = "user"      // Stop was called
	StopLimit     = "limit"     // a limit (cap, battery, no data) stopped the relay; reason says which
	StopRejected  = "rejected"  // the hub rejected the relay; reason from the hub
	StopForgotten = "forgotten" // the hub forgot this relay; the paired hub was deleted
	StopError     = "error"     // anything else; reason is the error text
)

// Network kinds for SetNetwork.
const (
	NetWiFi     = "wifi"
	NetCellular = "cellular"
)

// Callback receives events from the core on a Go thread.
type Callback interface {
	// OnStopped is called once when a started relay ends.
	OnStopped(kind, reason string)
}

// Core is the relay core of the Android service: one relay at a time, each
// started with the state directory of the hub it connects to.
type Core struct {
	name, carrier string
	log           *slog.Logger
	logFile       *rollingFile // nil without a log dir
	binder        *relay.HandleBinder
	dev           atomic.Pointer[relay.DeviceInfo]

	mu      sync.Mutex
	r       *relay.Relay
	hub     string
	cancel  context.CancelFunc
	lastErr string
}

// LogFile is the core's log file name inside the log dir given to NewCore.
const LogFile = "core.log"

// NewCore returns a core. name and carrier are shown on the hub. With a
// logDir the core also writes a rolling, redacted logDir/core.log (Close
// closes it).
func NewCore(logDir, name, carrier string) (*Core, error) {
	return newCore(logDir, name, carrier, nil)
}

func newCore(logDir, name, carrier string, bind func(int64, uintptr) error) (*Core, error) {
	if name == "" {
		return nil, errors.New("xscore: name is required")
	}
	c := &Core{name: name, carrier: carrier, binder: relay.NewHandleBinder(bind)}
	var w io.Writer = os.Stderr
	if logDir != "" {
		f, err := openRolling(filepath.Join(logDir, LogFile), LogMaxBytes)
		if err != nil {
			return nil, fmt.Errorf("xscore: %w", err)
		}
		c.logFile, w = f, io.MultiWriter(os.Stderr, f)
	}
	c.log = slog.New(slog.NewTextHandler(w, nil))
	c.dev.Store(&relay.DeviceInfo{Battery: 100, CellularOK: true})
	return c, nil
}

// Close stops a running relay and closes the log file.
func (c *Core) Close() {
	c.Stop()
	if c.logFile != nil {
		_ = c.logFile.Close()
	}
}

// parseLimits reads control.Limits JSON; "" means no limits.
func parseLimits(s string) (control.Limits, error) {
	var l control.Limits
	if s == "" {
		return l, nil
	}
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		return l, fmt.Errorf("xscore: limits: %w", err)
	}
	if err := l.Validate(); err != nil {
		return l, fmt.Errorf("xscore: %w", err)
	}
	return l, nil
}

// Start pairs with (first time) or connects to the hub in the XSR1 code and
// relays until Stop, a limit, or a reject/forget from the hub; cb.OnStopped
// reports the end. dir keeps this hub's relay key and pairing (one dir per
// hub); limits is control.Limits JSON ("" = none). It fails if a relay is
// already running.
func (c *Core) Start(dir, qr, limits string, cb Callback) error {
	if dir == "" {
		return errors.New("xscore: state dir is required")
	}
	p, err := pairing.Decode(qr)
	if err != nil {
		return fmt.Errorf("xscore: pairing code: %w", err)
	}
	lim, err := parseLimits(limits)
	if err != nil {
		return err
	}
	key, err := relay.LoadOrCreateKey(dir)
	if err != nil {
		return fmt.Errorf("xscore: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.r != nil {
		return errors.New("xscore: relay already running")
	}
	r, err := relay.New(relay.Config{
		Key: key, Hub: p, Name: c.name, Carrier: c.carrier, Limits: lim, Paired: relay.IsPaired(dir, p.HPK),
		Binder: c.binder, Device: func() relay.DeviceInfo { return *c.dev.Load() },
		OnPaired: func(w control.Welcome) {
			if err := relay.SavePaired(dir, relay.PairedHub{HPK: p.HPK, RelayID: w.RelayID}); err != nil {
				c.log.Warn("save paired hub", "err", err)
			}
		},
		Log: c.log,
	})
	if err != nil {
		return fmt.Errorf("xscore: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.r, c.hub, c.cancel, c.lastErr = r, p.Hub, cancel, ""
	go c.run(ctx, r, dir, cb)
	return nil
}

// SetLimits changes the running relay's limits (control.Limits JSON) and
// tells the hub; without a running relay it only validates them.
func (c *Core) SetLimits(limits string) error {
	l, err := parseLimits(limits)
	if err != nil {
		return err
	}
	c.mu.Lock()
	r := c.r
	c.mu.Unlock()
	if r == nil {
		return nil
	}
	if err := r.SetLimits(l); err != nil {
		return fmt.Errorf("xscore: %w", err)
	}
	return nil
}

// StopWith ends the running relay with a leaving reason decided by the app:
// "battery", "no_data", "no_cellular" or "limit" (Callback.OnStopped then
// reports kind "limit"), or "user".
func (c *Core) StopWith(reason string) error {
	switch reason {
	case control.LeavingUser, control.LeavingLimit, control.LeavingBattery, control.LeavingNoData, control.LeavingNoCellular:
	default:
		return fmt.Errorf("xscore: unknown stop reason %q", reason)
	}
	c.mu.Lock()
	r := c.r
	c.mu.Unlock()
	if r != nil {
		r.Stop(reason)
	}
	return nil
}

func (c *Core) run(ctx context.Context, r *relay.Relay, dir string, cb Callback) {
	kind, reason := c.classify(r.Run(ctx), dir)
	c.mu.Lock()
	if c.r == r {
		c.r, c.cancel = nil, nil
		if kind != StopUser {
			c.lastErr = kind + ": " + reason
		}
	}
	c.mu.Unlock()
	c.log.Info("relay ended", "kind", kind, "reason", reason)
	if cb != nil {
		cb.OnStopped(kind, reason)
	}
}

func (c *Core) classify(err error, dir string) (kind, reason string) {
	var rej *relay.RejectedError
	var stopped *relay.StoppedError
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		return StopUser, control.LeavingUser
	case errors.Is(err, relay.ErrForgotten):
		if ferr := relay.ForgetPaired(dir); ferr != nil {
			c.log.Warn("forget paired hub", "err", ferr)
		}
		return StopForgotten, ""
	case errors.As(err, &rej):
		return StopRejected, rej.Reason
	case errors.As(err, &stopped):
		if stopped.Reason == control.LeavingUser {
			return StopUser, stopped.Reason
		}
		return StopLimit, stopped.Reason
	}
	return StopError, err.Error()
}

// Stop ends the running relay (the hub gets leaving "user"); it returns at
// once and Callback.OnStopped follows.
func (c *Core) Stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// SetNetwork reports the Android network handle (Network.getNetworkHandle())
// for kind "wifi" (the hub's hotspot) or "cellular"; 0 = lost. A changed
// handle reconnects the running relay so its sockets use the new network.
func (c *Core) SetNetwork(kind string, handle int64) error {
	var n relay.Network
	switch kind {
	case NetWiFi:
		n = relay.NetWiFi
	case NetCellular:
		n = relay.NetCellular
	default:
		return fmt.Errorf("xscore: unknown network kind %q", kind)
	}
	if !c.binder.SetNetwork(n, handle) {
		return nil
	}
	c.log.Info("network changed", "kind", kind, "available", handle != 0)
	c.mu.Lock()
	r := c.r
	c.mu.Unlock()
	if r != nil {
		r.Reconnect()
	}
	return nil
}

// SetDevice reports device state for the stats sent to the hub (battery %,
// cellular signal dBm, net type lte/nr/..., cellular validated, hub Wi-Fi RSSI).
func (c *Core) SetDevice(battery, signalDBm int, netType string, cellularOK bool, wifiRSSI int) {
	c.dev.Store(&relay.DeviceInfo{Battery: battery, SignalDBm: signalDBm, NetType: netType,
		CellularOK: cellularOK, WifiRSSI: wifiRSSI})
}

// statsJSON is the Stats document read by the app.
type statsJSON struct {
	Running    bool   `json:"running"`
	Connected  bool   `json:"connected"`
	Hub        string `json:"hub,omitempty"`
	RelayID    string `json:"relay_id,omitempty"`
	PathID     uint8  `json:"path_id,omitempty"`
	BytesUp    uint64 `json:"bytes_up"`
	BytesDown  uint64 `json:"bytes_down"`
	PeriodUsed uint64 `json:"period_used"`
	Dropped    uint64 `json:"dropped"`
	ProxyFlows int    `json:"proxy_flows"`
	WiFi       bool   `json:"wifi"`
	Cellular   bool   `json:"cellular"`
	LastError  string `json:"last_error,omitempty"`
}

// Stats returns the relay state as JSON (see statsJSON).
func (c *Core) Stats() string {
	c.mu.Lock()
	r, hub, lastErr := c.r, c.hub, c.lastErr
	c.mu.Unlock()
	s := statsJSON{Running: r != nil, Hub: hub, LastError: lastErr,
		WiFi: c.binder.Handle(relay.NetWiFi) != 0, Cellular: c.binder.Handle(relay.NetCellular) != 0}
	if r != nil {
		st := r.Stats()
		s.Connected, s.RelayID, s.PathID = st.Connected, st.RelayID, st.PathID
		s.BytesUp, s.BytesDown, s.PeriodUsed, s.Dropped, s.ProxyFlows = st.BytesUp, st.BytesDown, st.PeriodUsed, st.Dropped, st.ProxyFlows
	}
	b, _ := json.Marshal(s) // plain struct: cannot fail
	return string(b)
}

// qrJSON is what the app needs from an XSR1 code to join the hub's Wi-Fi.
type qrJSON struct {
	Hub   string `json:"hub"`
	HPK   string `json:"hpk"`
	SSID  string `json:"ssid"`
	BSSID string `json:"bssid"`
	PSK   string `json:"psk"`
}

// ParseQR validates an XSR1 pairing code and returns {hub, hpk, ssid, bssid, psk}
// as JSON.
func ParseQR(qr string) (string, error) {
	p, err := pairing.Decode(qr)
	if err != nil {
		return "", fmt.Errorf("xscore: pairing code: %w", err)
	}
	b, err := json.Marshal(qrJSON{Hub: p.Hub, HPK: p.HPK, SSID: p.SSID, BSSID: p.BSSID, PSK: p.PSK})
	if err != nil {
		return "", fmt.Errorf("xscore: %w", err)
	}
	return string(b), nil
}
