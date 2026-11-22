package direct

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"

	"github.com/zyndata/cross-stream/internal/wire"
)

// Netstack settings.
const (
	nicID         = 1
	queueLen      = 1024 // outbound packets waiting for the TUN
	maxInFlight   = 512  // TCP connections being set up at once
	DialTimeout   = 15 * time.Second
	UDPIdle       = 60 * time.Second
	DefaultMTU    = 1500
	sweepInterval = 10 * time.Second
)

// Config configures a Stack.
type Config struct {
	MTU int // netstack MTU (0 = DefaultMTU)
	// Relays returns the relays new flows may use now.
	Relays func() []Relay
	// Token returns the current proxy session token.
	Token func() [wire.ProxyTokenLen]byte
	// SendUDP sends a B4 datagram to a relay's data socket.
	SendUDP func(relayID string, datagram []byte) error
	// Output writes one IP packet from the netstack to the TUN.
	Output func(pkt []byte)
	// Dial opens TCP connections to relay proxies (nil = net.Dialer).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Intercept, when set, may claim a new TCP flow (PEP): the returned
	// relay's Dial carries it. Intercepted flows are not DIRECT flows:
	// Stats counts them apart and ResetAll / ResetExcept keep them; only
	// ResetRelay of their relay id or Close ends them.
	Intercept func(k FlowKey) (Relay, bool)
	UDPIdle   time.Duration // 0 = UDPIdle
	Log       *slog.Logger
	Now       func() time.Time
}

// Stack terminates the clients' TCP and UDP flows from the TUN in a gVisor
// netstack and forwards each through one relay's proxy role (DIRECT mode).
type Stack struct {
	cfg    Config
	st     *stack.Stack
	ep     *channel.Endpoint
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	closed   bool
	sel      *selector
	flows    map[FlowKey]*flow
	byID     map[uint32]*flow // UDP flows by flow id
	icp      atomic.Int64     // intercepted flows open (OwnsIntercepted fast path)
	perRelay map[string]int
	nextID   uint32
}

// New creates the netstack; Close releases it.
func New(cfg Config) (*Stack, error) {
	if cfg.Relays == nil || cfg.Token == nil || cfg.SendUDP == nil || cfg.Output == nil {
		return nil, errors.New("direct: Relays, Token, SendUDP and Output are required")
	}
	if cfg.MTU <= 0 {
		cfg.MTU = DefaultMTU
	}
	if cfg.Dial == nil {
		d := &net.Dialer{}
		cfg.Dial = d.DialContext
	}
	if cfg.UDPIdle <= 0 {
		cfg.UDPIdle = UDPIdle
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Stack{cfg: cfg, sel: newSelector(), flows: make(map[FlowKey]*flow), byID: make(map[uint32]*flow),
		perRelay: make(map[string]int)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.st = stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	s.ep = channel.New(queueLen, uint32(cfg.MTU), "")
	if err := s.setup(); err != nil {
		s.st.Close()
		return nil, err
	}
	s.wg.Add(2)
	go s.outputLoop()
	go s.sweepLoop()
	return s, nil
}

func (s *Stack) setup() error {
	if err := s.st.CreateNIC(nicID, s.ep); err != nil {
		return fmt.Errorf("direct: create NIC: %s", err)
	}
	// Accept packets for any address and answer from any address: the
	// netstack stands in for every destination.
	if err := s.st.SetPromiscuousMode(nicID, true); err != nil {
		return fmt.Errorf("direct: promiscuous mode: %s", err)
	}
	if err := s.st.SetSpoofing(nicID, true); err != nil {
		return fmt.Errorf("direct: spoofing: %s", err)
	}
	s.st.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: nicID}})
	sack := tcpip.TCPSACKEnabled(true)
	if err := s.st.SetTransportProtocolOption(tcp.ProtocolNumber, &sack); err != nil {
		return fmt.Errorf("direct: tcp sack: %s", err)
	}
	s.st.SetTransportProtocolHandler(tcp.ProtocolNumber, tcp.NewForwarder(s.st, 0, maxInFlight, s.handleTCP).HandlePacket)
	s.st.SetTransportProtocolHandler(udp.ProtocolNumber, udp.NewForwarder(s.st, s.handleUDP).HandlePacket)
	return nil
}

// Inject passes one IP packet from the TUN to the netstack. Only IPv4 TCP
// and UDP are handled; anything else (ICMP, IPv6) is dropped. pkt is copied.
func (s *Stack) Inject(pkt []byte) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 || (pkt[9] != protoTCP && pkt[9] != protoUDP) {
		return
	}
	pb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(pkt)})
	s.ep.InjectInbound(header.IPv4ProtocolNumber, pb)
	pb.DecRef()
}

// outputLoop writes the netstack's packets to the TUN.
func (s *Stack) outputLoop() {
	defer s.wg.Done()
	for {
		pb := s.ep.ReadContext(s.ctx)
		if pb == nil {
			return
		}
		v := pb.ToView()
		s.cfg.Output(v.AsSlice())
		v.Release()
		pb.DecRef()
	}
}

// sweepLoop expires sticky destinations and idle UDP flows.
func (s *Stack) sweepLoop() {
	defer s.wg.Done()
	t := time.NewTicker(min(sweepInterval, s.cfg.UDPIdle/4))
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			now := s.cfg.Now()
			s.mu.Lock()
			s.sel.expire(now)
			s.mu.Unlock()
			s.reset(func(f *flow) bool {
				return f.key.Proto == protoUDP && now.Sub(time.Unix(0, f.last.Load())) >= s.cfg.UDPIdle
			})
		}
	}
}

// Close ends every flow and releases the netstack.
func (s *Stack) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.reset(func(*flow) bool { return true })
	s.cancel()
	s.wg.Wait()
	s.st.Close()
	s.ep.Close()
	s.st.Wait()
}
