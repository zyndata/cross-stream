package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/zyndata/cross-stream/internal/ipc"
)

// IPC names of xs-hubd (internal/hub/rpc.go).
const (
	methodSubscribe    = "events.subscribe"
	eventStatus        = "events.status"
	methodPairingQR    = "pairing.qr"
	methodRelaysUpdate = "relays.update"
	methodRelaysForget = "relays.forget"
	methodSettingsSet  = "settings.set"
	methodSettingsGet  = "settings.get"
	methodHotspotGet   = "hotspot.get"
	methodHotspotSet   = "hotspot.set"
	methodHotspotPower = "hotspot.power"
	methodServerEnroll = "server.enroll"
	methodSpeedTestRun = "speedtest.run"
	methodSpeedTests   = "speedtest.results"

	// EventView is the frontend event carrying a View.
	EventView   = "view"
	retryEvery  = 2 * time.Second
	callTimeout = 20 * time.Second
	// enrollTimeout covers the enrollment handshake through a relay.
	enrollTimeout = 2 * time.Minute
	// speedTestTimeout covers a speed test in both directions (2 × 30 s + waits).
	speedTestTimeout = 3 * time.Minute
)

// App is the Wails backend: an xs-hubd IPC client and nothing else.
type App struct {
	sock string
	log  *slog.Logger
	emit func(ctx context.Context, v View)
	// notify shows hub events as OS notifications (notify.go).
	notify *notifier
	// svc starts and stops xs-hubd (service.go).
	svc *service
	// upd checks for and installs a newer release (updates.go).
	upd *updater
	// openURL replaces runtime.BrowserOpenURL (tests).
	openURL func(url string)

	// starting: startHub runs; wake: nudges the status loop after a start.
	starting atomic.Bool
	wake     chan struct{}

	mu       sync.Mutex
	ctx      context.Context
	last     View
	startErr string    // why the last start failed ("" = it did not)
	wasUp    bool      // a status arrived in this session (a later outage is "lost")
	chosenAt time.Time // the Wi-Fi choice was sent: xs-hubd is starting again
}

// NewApp returns the backend for the xs-hubd socket at sock.
func NewApp(sock string, log *slog.Logger) *App {
	return &App{sock: sock, log: log, last: View{State: stateNoDaemon, Relays: []RelayView{}, Uplinks: []UplinkView{}}, notify: newNotifier(), svc: newService(), upd: newUpdater(version),
		wake: make(chan struct{}, 1), emit: func(ctx context.Context, v View) { runtime.EventsEmit(ctx, EventView, v) }}
}

func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
	go a.watch(ctx)
	go a.checkAtStart(ctx)
}

// watch follows the status stream and reconnects every retryEvery.
func (a *App) watch(ctx context.Context) {
	tr := newTracker()
	a.startHub(ctx)
	for {
		err := ipc.Subscribe(ctx, a.sock, methodSubscribe, nil, func(method string, params json.RawMessage) error {
			if method == eventNotice {
				a.onEvent(params)
				return nil
			}
			if method == eventClientWifi {
				return a.askClientWifi(ctx, params)
			}
			if method != eventStatus {
				return nil
			}
			var st hubStatus
			if err := json.Unmarshal(params, &st); err != nil {
				return fmt.Errorf("status: %w", err)
			}
			a.mu.Lock()
			a.wasUp, a.startErr = true, ""
			a.mu.Unlock()
			a.publish(ctx, tr.view(st, time.Now()))
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		state, msg := classify(err)
		a.mu.Lock()
		startErr, lost := a.startErr, a.wasUp && state == stateNoDaemon
		a.mu.Unlock()
		if state == stateNoDaemon && startErr != "" {
			msg = startErr // why the GUI could not start it
		}
		a.log.Debug("status stream", "state", state, "err", err)
		a.mu.Lock()
		if a.last.State == stateWifi && state == stateNoDaemon { // xs-hubd stopped asking (choice or network left)
			a.chosenAt = time.Now()
		}
		restarting := state == stateNoDaemon && time.Since(a.chosenAt) < choiceRestart
		a.mu.Unlock()
		if restarting {
			msg, lost = "", false
		}
		a.publish(ctx, View{State: state, Error: msg, Starting: a.starting.Load() || restarting, Lost: lost,
			Relays: []RelayView{}, Uplinks: []UplinkView{}})
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		case <-time.After(retryEvery):
		}
	}
}

func (a *App) publish(ctx context.Context, v View) {
	a.mu.Lock()
	a.last = v
	a.mu.Unlock()
	a.emit(ctx, v)
}

// classify maps an IPC error to a view state and a message.
func classify(err error) (state, msg string) {
	var ie *ipc.Error
	switch {
	case err == nil:
		return stateNoDaemon, ""
	case errors.As(err, &ie):
		return stateError, ie.Message
	case errors.Is(err, fs.ErrPermission):
		return stateNoAccess, err.Error()
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
		return stateNoDaemon, err.Error()
	}
	return stateError, err.Error()
}

// call runs one IPC method; errors come back to the frontend as text.
func (a *App) call(timeout time.Duration, method string, params, result any) error {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := ipc.Call(cctx, a.sock, method, params, result)
	if err == nil {
		return nil
	}
	state, msg := classify(err)
	a.log.Warn("ipc call", "method", method, "state", state, "err", err)
	return errors.New(state + ": " + msg)
}

// Platform is the OS the GUI runs on ("windows": Mobile Hotspot picks the
// channel, so the frontend hides channel and country).
func (a *App) Platform() string { return goruntime.GOOS }

// Current returns the last view (the frontend asks once on load).
func (a *App) Current() View {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// PairQR returns the current pairing code with its QR PNG.
func (a *App) PairQR() (PairQR, error) {
	var q PairQR
	return q, a.call(callTimeout, methodPairingQR, nil, &q)
}

// RelayParams change a relay's path settings; nil fields stay.
type RelayParams struct {
	Priority *string `json:"priority,omitempty"`
	Up       *string `json:"up,omitempty"`
	Down     *string `json:"down,omitempty"`
	RateKbps *uint32 `json:"rate_kbps,omitempty"`
	CapMB    *uint32 `json:"cap_mb,omitempty"`
}

// SetRelay changes priority, directions, rate limit or data cap of a relay.
func (a *App) SetRelay(id string, p RelayParams) error {
	return a.call(callTimeout, methodRelaysUpdate, struct {
		Relay string `json:"relay"`
		RelayParams
	}{id, p}, nil)
}

// Forget removes a relay; it must be paired again to come back.
func (a *App) Forget(id string) error {
	return a.call(callTimeout, methodRelaysForget, map[string]string{"relay": id}, nil)
}

// SetOverflow sets the hub-wide overflow threshold (0 = off).
func (a *App) SetOverflow(mbps uint16) error {
	return a.call(callTimeout, methodSettingsSet, map[string]uint16{"overflow_mbps": mbps}, nil)
}

// SetBond sets the hub mode (max_throughput, min_latency) and redundancy
// (off, auto, on); an empty value stays.
func (a *App) SetBond(mode, redundancy string) error {
	p := map[string]string{}
	if mode != "" {
		p["mode"] = mode
	}
	if redundancy != "" {
		p["redundancy"] = redundancy
	}
	return a.call(callTimeout, methodSettingsSet, p, nil)
}

// PEPSettings returns the PEP setting (off, on) and its exclusions.
func (a *App) PEPSettings() (pepSettings, error) {
	var s pepSettings
	err := a.call(callTimeout, methodSettingsGet, nil, &s)
	if s.Exclude == nil {
		s.Exclude = []string{}
	}
	return s, err
}

// SetPEP turns PEP off or on and replaces its exclusions (nil = keep).
func (a *App) SetPEP(on bool, exclude []string) error {
	p := map[string]any{"pep": "off"}
	if on {
		p["pep"] = "on"
	}
	if exclude != nil {
		p["pep_exclude"] = exclude
	}
	return a.call(callTimeout, methodSettingsSet, p, nil)
}

// SpeedTest measures the relays' raw capacity (uses their mobile data).
func (a *App) SpeedTest(p SpeedTestParams) (SpeedResult, error) {
	var r SpeedResult
	return r, a.call(speedTestTimeout, methodSpeedTestRun, p, &r)
}

// SpeedTests returns the stored speed test results, newest first.
func (a *App) SpeedTests() ([]SpeedResult, error) {
	var r []SpeedResult
	return r, a.call(callTimeout, methodSpeedTests, nil, &r)
}

// Hotspot returns the hotspot settings and state.
func (a *App) Hotspot() (HotspotInfo, error) {
	var h HotspotInfo
	return h, a.call(callTimeout, methodHotspotGet, nil, &h)
}

// SetHotspot saves the hotspot settings and restarts the AP with them.
func (a *App) SetHotspot(s HotspotSettings) (HotspotInfo, error) {
	var h HotspotInfo
	return h, a.call(enrollTimeout, methodHotspotSet, s, &h)
}

// HotspotPower stops or starts the AP.
func (a *App) HotspotPower(on bool) (HotspotInfo, error) {
	var h HotspotInfo
	return h, a.call(enrollTimeout, methodHotspotPower, map[string]bool{"on": on}, &h)
}

// Enroll registers the hub with a server using an XS1 code.
func (a *App) Enroll(code string) (Enrollment, error) {
	var e Enrollment
	return e, a.call(enrollTimeout, methodServerEnroll, map[string]string{"code": code}, &e)
}
