package bond

import (
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// Priority of a path (values match wire.PathConfig.Priority).
type Priority uint8

// Path priorities.
const (
	Normal    = Priority(wire.PriorityNormal)
	Backup    = Priority(wire.PriorityBackup)
	Secondary = Priority(wire.PrioritySecondary)
)

func (p Priority) String() string {
	switch p {
	case Backup:
		return "backup"
	case Secondary:
		return "secondary"
	}
	return "normal"
}

// DirMode is the use of a path in one direction (values match wire.DirOn...).
type DirMode uint8

// Direction modes.
const (
	DirOn         = DirMode(wire.DirOn)
	DirBackupOnly = DirMode(wire.DirBackupOnly)
	DirOff        = DirMode(wire.DirOff)
)

func (d DirMode) String() string {
	switch d {
	case DirBackupOnly:
		return "backup_only"
	case DirOff:
		return "off"
	}
	return "on"
}

// State of a path.
type State uint8

// Path states (architecture.md "Bonding engine").
const (
	Dead     State = iota // not usable; probed
	Alive                 // usable
	Suspect               // unacked data too old: no new data until ACKs resume
	Sleeping              // idle backup path, slow pings
)

func (s State) String() string {
	switch s {
	case Alive:
		return "alive"
	case Suspect:
		return "suspect"
	case Sleeping:
		return "sleeping"
	}
	return "dead"
}

// PathConfig is the local policy of one path. Dir applies to what this side
// sends (hub: upload, server: download).
type PathConfig struct {
	Priority Priority
	RateBps  uint64 // send rate limit in bytes/s, 0 = none
	CapBytes uint64 // sent + received bytes allowed on the path, 0 = none
	Dir      DirMode
}

// Timers and limits (architecture.md "Bonding engine", decisions.md phase 04).
const (
	PingInterval      = 200 * time.Millisecond
	DeadProbeInterval = time.Second
	SleepPingInterval = 2 * time.Second
	FastProbeFor      = 10 * time.Second // a new or re-added path probes at PingInterval this long
	MinPongTimeout    = 300 * time.Millisecond
	MissedPingsDead   = 3
	PongsToAlive      = 3
	SilenceDead       = time.Second
	SleepSilenceDead  = 5 * time.Second
	SleepAfterIdle    = 10 * time.Second
	MinSuspectAge     = 300 * time.Millisecond
	UnackedKeep       = time.Second
	MaxAttempts       = 2 // re-sends per packet
	RetransmitMaxAge  = 400 * time.Millisecond
	AckInterval       = 10 * time.Millisecond
	AckEvery          = 16 // packets
	MaxAckFrames      = 8
	MaxHold           = 300 * time.Millisecond
	QueueMaxDelay     = 100 * time.Millisecond // minimum; scales with path RTT
	QueueMaxDelayCap  = 500 * time.Millisecond
	QueueMaxPackets   = 4096
	ReorderMax        = 4096 // buffered packets before a forced skip
	ResetDistance     = 65536
	ConfigEvery       = 10 * time.Second
)

// Wire overhead of a DATA datagram: outer header + AEAD tag; each frame adds
// a 3-byte TLV header. IPOverhead is the cost of one IP packet in a datagram.
const (
	DatagramOverhead = wire.HeaderLen + 16
	IPOverhead       = DatagramOverhead + wire.FrameHeaderLen + 4
)

// Path MTU probing (outer datagram sizes).
var probeSizes = []int{1452, 1400, 1340, 1280}

// Path MTU bounds. A path where no probe got through gets MinPathMTU: the
// UDP payload of an IPv4 datagram that a 464XLAT carrier turns into a
// 1280-byte IPv6 packet (1280 - 40 - 8).
const (
	MinPathMTU   = 1232
	MinTunnelMTU = MinPathMTU - IPOverhead
	probeRetries = 2
	probeWait    = 3 * time.Second
)

// PathStats is a snapshot of one path's metrics.
type PathStats struct {
	ID          uint8         `json:"id"`
	State       string        `json:"state"`
	Priority    string        `json:"priority"`
	Dir         string        `json:"dir"`
	SRTT        time.Duration `json:"srtt"`
	Jitter      time.Duration `json:"jitter"` // rttvar
	MinRTT      time.Duration `json:"min_rtt"`
	Loss        float64       `json:"loss"`         // fraction of data packets lost (smoothed)
	BtlBw       uint64        `json:"btlbw"`        // estimated bottleneck rate, bytes/s
	Inflight    int           `json:"inflight"`     // bytes
	Cwnd        int           `json:"cwnd"`         // in-flight cap, bytes
	CC          string        `json:"cc"`           // BBR-lite phase: startup, drain, probe_bw, probe_rtt
	Pacing      uint64        `json:"pacing"`       // pacing rate, bytes/s
	DataRTT     time.Duration `json:"data_rtt"`     // send → ACK of data packets (smoothed)
	TxBytes     uint64        `json:"tx_bytes"`     // all outer bytes sent
	RxBytes     uint64        `json:"rx_bytes"`     // all outer bytes received
	TxData      uint64        `json:"tx_data"`      // IP packet bytes sent (incl. re-sends)
	RxData      uint64        `json:"rx_data"`      // IP packet bytes received
	TxRate      uint64        `json:"tx_rate"`      // bytes/s, last second
	RxRate      uint64        `json:"rx_rate"`      // bytes/s, last second
	MTU         int           `json:"mtu"`          // outer bytes, 0 = not probed yet
	Retransmit  uint64        `json:"retransmits"`  // packets re-sent from this path
	Lost        uint64        `json:"lost"`         // packets declared lost on this path
	Capped      bool          `json:"capped"`       // cap reached
	Used        uint64        `json:"used"`         // bytes counted against the cap
	Stream      bool          `json:"stream"`       // TCP/WSS transport
	Guarded     bool          `json:"guarded"`      // RTT far above the best path: no new data
	TxPkts      uint64        `json:"tx_pkts"`      // datagrams sent
	Twins       uint64        `json:"twins"`        // duplicate copies sent
	PongsNeeded int           `json:"pongs_needed"` // anti-flap: PONGs to come back from DEAD
}

// Stats is a snapshot of the engine.
type Stats struct {
	Paths      []PathStats `json:"paths"`
	Queue      int         `json:"queue"`     // packets waiting for a path
	Reliable   int         `json:"reliable"`  // reliable (PEP stream) bytes waiting for a first send
	Reorder    int         `json:"reorder"`   // packets held by the reorder buffer
	Skipped    uint64      `json:"skipped"`   // seqs given up by the reorder buffer
	Dropped    uint64      `json:"dropped"`   // packets dropped from the send queue
	Delivered  uint64      `json:"delivered"` // packets delivered in order
	Dups       uint64      `json:"dups"`      // duplicate packets received
	TunnelMTU  int         `json:"tunnel_mtu"`
	Mode       string      `json:"mode"`       // max_throughput / min_latency
	Redundancy string      `json:"redundancy"` // off / auto / on (setting)
	Redundant  bool        `json:"redundant"`  // every packet duplicated now
	Aggregate  bool        `json:"aggregate"`  // packet aggregation on
}
