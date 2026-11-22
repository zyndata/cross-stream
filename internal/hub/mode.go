package hub

import "time"

// Mode is the xs-hubd state (architecture.md "Modes").
type Mode string

// Hub modes.
const (
	ModeOffline    Mode = "OFFLINE"    // no relay connected
	ModeConnecting Mode = "CONNECTING" // relays, no working tunnel yet
	ModeBonded     Mode = "BONDED"     // tunnel to the server is up
	ModeDirect     Mode = "DIRECT"     // serverless fallback: flows go through relay proxies
)

// Mode machine timers (architecture.md "Modes", decisions.md phase 06).
const (
	ConnectTimeout = 15 * time.Second // CONNECTING without a handshake → DIRECT
	BondedLoss     = 10 * time.Second // BONDED without a valid packet → DIRECT/CONNECTING
	TunnelFresh    = 5 * time.Second  // a session is up when a valid packet came within this
	ProbeEvery     = 30 * time.Second // DIRECT: handshake attempts to the server
	DrainMax       = 5 * time.Minute  // DIRECT → BONDED: direct flows kept at most this long
)

// ModeInputs is what the machine looks at once a second.
type ModeInputs struct {
	PathsAlive int           // connected relays
	Session    bool          // a session with the server exists
	Silence    time.Duration // since the last valid packet from the server
}

// TunnelUp reports a live session.
func (in ModeInputs) TunnelUp() bool { return in.Session && in.Silence < TunnelFresh }

// ModeMachine tracks the hub mode. DIRECT transitions only happen with
// DirectEnabled (the hub is enrolled and has its netstack); without it the
// hub waits in CONNECTING.
type ModeMachine struct {
	DirectEnabled bool

	mode  Mode
	since time.Time // when the current mode started
}

// Mode returns the current mode and since when it holds.
func (m *ModeMachine) Mode() (Mode, time.Time) {
	if m.mode == "" {
		return ModeOffline, m.since
	}
	return m.mode, m.since
}

// Update applies one observation and returns the new mode and whether it
// changed. Several transitions may happen in one call (OFFLINE → CONNECTING
// → BONDED when a relay connects to a hub whose tunnel is already up).
func (m *ModeMachine) Update(now time.Time, in ModeInputs) (Mode, bool) {
	if m.mode == "" {
		m.mode, m.since = ModeOffline, now
	}
	start := m.mode
	for range 4 {
		next := m.next(now, in)
		if next == m.mode {
			break
		}
		m.mode, m.since = next, now
	}
	return m.mode, m.mode != start
}

func (m *ModeMachine) next(now time.Time, in ModeInputs) Mode {
	if in.PathsAlive == 0 {
		return ModeOffline
	}
	switch m.mode {
	case ModeOffline:
		return ModeConnecting
	case ModeConnecting:
		if in.TunnelUp() {
			return ModeBonded
		}
		if m.DirectEnabled && now.Sub(m.since) >= ConnectTimeout {
			return ModeDirect
		}
	case ModeBonded:
		if in.Silence >= BondedLoss {
			if m.DirectEnabled {
				return ModeDirect
			}
			return ModeConnecting
		}
	case ModeDirect:
		if in.TunnelUp() { // the client probes the server every ProbeEvery
			return ModeBonded
		}
	}
	return m.mode
}

// carries reports whether the hub moves client traffic in mode m (through
// the tunnel or the DIRECT relay proxies).
func (m Mode) carries() bool { return m == ModeBonded || m == ModeDirect }
