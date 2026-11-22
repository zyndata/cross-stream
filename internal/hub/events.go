package hub

import (
	"context"
	"sync"
	"time"
)

// EventNotice is the events.subscribe notification that carries one Event
// (architecture.md "Desktop notifications").
const EventNotice = "events.event"

// Event types.
const (
	EvRelayJoined  = "relay_joined"
	EvRelayLeft    = "relay_left"   // Reason: e.g. "leaving: limit", "control lost"
	EvCap80        = "cap_80"       // owner cap on the path 80 % used
	EvCap100       = "cap_100"      // owner cap reached
	EvBatteryLow   = "battery_low"  // relay battery below BatteryLowPct
	EvModeChanged  = "mode_changed" // Mode, Reason = previous mode
	EvWeakWifi     = "weak_wifi"    // relay's Wi-Fi to the hub is weak
	EvSpeedTestEnd = "speedtest_done"
)

// Event thresholds.
const (
	BatteryLowPct = 25
	batteryRearm  = 30  // battery_low again only after the battery got above this
	WeakWifiDBm   = -75 // as the GUI hint
	weakWifiOn    = 5   // seconds below WeakWifiDBm before the event
	weakWifiOff   = 15  // seconds above it before it can fire again
	eventBuffer   = 64
)

// Event is one typed hub event.
type Event struct {
	Type   string    `json:"type"`
	At     time.Time `json:"at"`
	Relay  string    `json:"relay,omitempty"` // relay id
	Name   string    `json:"name,omitempty"`  // relay name
	Reason string    `json:"reason,omitempty"`
	Mode   Mode      `json:"mode,omitempty"`
	Value  int       `json:"value,omitempty"` // battery %, RSSI dBm, cap MB, test download Mb/s
	Up     int       `json:"up,omitempty"`    // test upload Mb/s
}

// eventBus fans events out to the IPC subscribers (slow ones lose events).
type eventBus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func (b *eventBus) subscribe() (chan Event, func()) {
	ch := make(chan Event, eventBuffer)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = make(map[chan Event]struct{})
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *eventBus) emit(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (d *Daemon) emit(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	d.events.emit(e)
}

// relayAlerts is the edge-trigger state of one relay's threshold events.
type relayAlerts struct {
	cap80, cap100 bool
	batteryLow    bool
	weakN, okN    int
	weak          bool
}

// eventTick (systemLoop, once a second) turns relay stats into threshold
// events: path cap 80 % / 100 %, battery low, weak Wi-Fi.
func (d *Daemon) eventTick(st Status) {
	if d.alerts == nil {
		d.alerts = make(map[string]*relayAlerts)
	}
	for _, r := range st.Relays {
		a := d.alerts[r.ID]
		if a == nil {
			a = &relayAlerts{}
			d.alerts[r.ID] = a
		}
		ev := Event{Relay: r.ID, Name: r.Name}
		if r.Path.CapMB > 0 && r.Bond != nil {
			usedMB := float64(r.Bond.Used) / 1e6
			capMB := float64(r.Path.CapMB)
			if hit := usedMB >= 0.8*capMB; hit != a.cap80 {
				if a.cap80 = hit; hit {
					d.emitWith(ev, EvCap80, int(r.Path.CapMB))
				}
			}
			if hit := usedMB >= capMB; hit != a.cap100 {
				if a.cap100 = hit; hit {
					d.emitWith(ev, EvCap100, int(r.Path.CapMB))
				}
			}
		}
		if !r.Connected || r.Stats == nil {
			a.weakN, a.okN = 0, 0
			continue
		}
		if b := r.Stats.Battery; b > 0 {
			switch {
			case !a.batteryLow && b < BatteryLowPct:
				a.batteryLow = true
				d.emitWith(ev, EvBatteryLow, b)
			case a.batteryLow && b >= batteryRearm:
				a.batteryLow = false
			}
		}
		if rssi := r.Stats.WifiRSSI; rssi != 0 && rssi < WeakWifiDBm {
			a.weakN, a.okN = a.weakN+1, 0
			if !a.weak && a.weakN >= weakWifiOn {
				a.weak = true
				d.emitWith(ev, EvWeakWifi, rssi)
			}
		} else {
			a.okN, a.weakN = a.okN+1, 0
			if a.weak && a.okN >= weakWifiOff {
				a.weak = false
			}
		}
	}
}

func (d *Daemon) emitWith(e Event, typ string, v int) {
	e.Type, e.Value = typ, v
	d.emit(e)
}

// streamEvents sends the status every EventEvery and each event as it
// happens (events.subscribe).
func (d *Daemon) streamEvents(ctx context.Context, notify func(string, any) error) error {
	ch, cancel := d.events.subscribe()
	defer cancel()
	t := time.NewTicker(EventEvery)
	defer t.Stop()
	if err := notify(EventStatus, d.Status()); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-ch:
			if err := notify(EventNotice, e); err != nil {
				return err
			}
		case <-t.C:
			if err := notify(EventStatus, d.Status()); err != nil {
				return err
			}
		}
	}
}
