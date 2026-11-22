package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

func TestEgressAllowed(t *testing.T) {
	e, err := ParseEgress([]string{"172.30.0.0/24", "172.17.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		proto uint8
		dst   string
		want  bool
	}{
		{ProtoTCP, "93.184.216.34:443", true},
		{ProtoUDP, "8.8.8.8:53", true},
		{ProtoTCP, "93.184.216.34:25", false},
		{ProtoTCP, "93.184.216.34:445", false},
		{ProtoUDP, "93.184.216.34:445", false},
		{ProtoUDP, "255.255.255.255:67", false},
		{ProtoUDP, "93.184.216.34:68", false},
		{ProtoTCP, "93.184.216.34:67", true}, // DHCP is UDP only
		{ProtoTCP, "10.0.0.1:80", false},
		{ProtoTCP, "172.16.5.5:80", false},
		{ProtoTCP, "172.31.255.255:80", false},
		{ProtoTCP, "172.32.0.1:80", true},
		{ProtoUDP, "192.168.1.1:53", false},
		{ProtoTCP, "169.254.169.254:80", false},
		{ProtoTCP, "172.30.0.10:80", true},   // allowed prefix
		{ProtoTCP, "172.30.1.10:80", false},  // outside the allowed /24
		{ProtoTCP, "172.30.0.10:25", false},  // ports win over the allow list
		{ProtoTCP, "172.17.0.1:2375", false}, // Docker API even when allowed
		{ProtoTCP, "172.17.0.1:2376", false}, // Docker API (TLS)
		{ProtoTCP, "172.17.0.1:8080", true},  // allowed prefix, other port
		{ProtoTCP, "100.64.0.3:80", true},    // tunnel peers are not filtered here
		{ProtoICMP, "10.0.0.1:0", false},     // any protocol to a private range
		{ProtoICMP, "93.184.216.34:0", true}, // ping the internet
		{ProtoUDP, "[::ffff:10.0.0.1]:53", false},
	}
	for _, tt := range tests {
		if got := e.Allowed(tt.proto, netip.MustParseAddrPort(tt.dst)); got != tt.want {
			t.Errorf("Allowed(%d, %s) = %v, want %v", tt.proto, tt.dst, got, tt.want)
		}
	}
}

func TestParseEgressInvalid(t *testing.T) {
	for _, s := range []string{"10.0.0.1", "fd00::/8", "nonsense", "10.0.0.0/33"} {
		if _, err := ParseEgress([]string{s}); err == nil {
			t.Errorf("ParseEgress(%q) = nil error", s)
		}
	}
}

func TestEgressNFTRules(t *testing.T) {
	n := NFT{TunName: "xss0", Prefix: TunnelPrefix, Egress: Egress{Allow: []netip.Prefix{netip.MustParsePrefix("172.30.0.0/24")}}}
	rs := n.Ruleset()
	want := []string{
		"type filter hook forward priority filter; policy accept;",
		`iifname "xss0" tcp dport { 25, 445 } drop`,
		`iifname "xss0" udp dport { 67, 68, 445 } drop`,
		`iifname "xss0" ip daddr 172.17.0.0/16 tcp dport { 2375, 2376 } drop`,
		`iifname "xss0" ip daddr { 172.30.0.0/24 } accept`,
		`iifname "xss0" ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16 } drop`,
		// the host's own services (input hook), after the forward chain
		"type filter hook input priority filter; policy accept;",
		`iifname "xss0" meta l4proto { tcp, udp } drop`,
	}
	last := -1
	for _, w := range want {
		i := strings.Index(rs, w)
		if i < 0 {
			t.Fatalf("ruleset lacks %q:\n%s", w, rs)
		}
		if i < last {
			t.Errorf("rule %q out of order", w)
		}
		last = i
	}
	if strings.Contains(NFT{TunName: "xss0", Prefix: TunnelPrefix}.Ruleset(), "accept\n\t\tiifname") {
		t.Error("allow rule present without egress_allow")
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newRateLimiter(2, 10, 3)
	a := netip.MustParseAddr("198.51.100.1")
	b := netip.MustParseAddr("198.51.100.2")
	for i := range 10 {
		if !r.Allow(a, now) {
			t.Fatalf("burst packet %d refused", i)
		}
	}
	if r.Allow(a, now) {
		t.Fatal("11th packet in a burst allowed")
	}
	if !r.Allow(b, now) {
		t.Fatal("other source limited by a's bucket")
	}
	if r.Allow(a, now.Add(400*time.Millisecond)) {
		t.Fatal("token before 0.5 s")
	}
	if !r.Allow(a, now.Add(600*time.Millisecond)) {
		t.Fatal("no token after 0.6 s at 2/s")
	}
	// Table full: refilled buckets are pruned, so new sources still work.
	r.Allow(netip.MustParseAddr("198.51.100.3"), now)
	later := now.Add(time.Minute)
	for i := range 5 {
		if !r.Allow(netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}), later) {
			t.Fatalf("new source %d refused with a full table", i)
		}
	}
	if len(r.buckets) > 3 {
		t.Fatalf("table grew to %d > max 3", len(r.buckets))
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"v1.2.3", Version{1, 2, 3}, true},
		{"v1.0.0-4-gabcdef-dirty", Version{1, 0, 0}, true},
		{"v10.20.30+build", Version{10, 20, 30}, true},
		{"1.2.3", Version{}, false},
		{"v1.2", Version{}, false},
		{"v1.2.x", Version{}, false},
		{"v01.2.3", Version{}, false},
		{"dev", Version{}, false},
		{"69c841e", Version{}, false},
		{"", Version{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseVersion(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseVersion(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestHubVersionGate(t *testing.T) {
	tests := []struct {
		min, sw string
		want    bool
	}{
		{"", "dev", true},
		{"", "", true},
		{"v1.0.0", "v1.0.0", true},
		{"v1.0.0", "v1.0.1-3-gabc", true},
		{"v1.0.0", "v2.0.0", true},
		{"v1.2.0", "v1.1.9", false},
		{"v1.0.0", "v0.99.99", false},
		{"v1.0.0", "dev", false},
		{"v1.0.0", "", false},
	}
	for _, tt := range tests {
		if got := hubVersionOK(tt.min, tt.sw); got != tt.want {
			t.Errorf("hubVersionOK(%q, %q) = %v, want %v", tt.min, tt.sw, got, tt.want)
		}
	}
}

func TestFreshInit(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &Server{initTS: map[string]int64{}, o: Options{Now: func() time.Time { return now }}}
	// A hub whose clock was an hour ahead and got corrected is not locked out.
	ahead, fixed := now.Add(time.Hour).UnixNano(), now.UnixNano()
	if !s.freshInit("c", ahead) || !s.freshInit("c", fixed) || s.freshInit("c", fixed) {
		t.Error("clock stepped back: want the corrected time accepted once")
	}
	steps := []struct {
		hub  string
		ts   int64
		want bool
	}{
		{"a", 100, true},
		{"a", 100, false}, // same INIT replayed
		{"a", 99, false},  // older INIT replayed
		{"b", 50, true},   // other hub has its own clock
		{"a", 101, true},
		{"a", 100, false},
	}
	for i, st := range steps {
		if got := s.freshInit(st.hub, st.ts); got != st.want {
			t.Errorf("step %d: freshInit(%s, %d) = %v, want %v", i, st.hub, st.ts, got, st.want)
		}
	}
}

func TestCheckMetricsListen(t *testing.T) {
	tests := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:9100", true},
		{"[::1]:9100", true},
		{"10.0.0.5:9100", true},
		{"172.18.0.2:9100", true},
		{"0.0.0.0:9100", false},
		{"203.0.113.5:9100", false},
		{"100.64.0.1:9100", false}, // tunnel network
		{":9100", false},
		{"localhost:9100", false},
	}
	for _, tt := range tests {
		if err := checkMetricsListen(tt.addr); (err == nil) != tt.ok {
			t.Errorf("checkMetricsListen(%q) = %v, want ok=%v", tt.addr, err, tt.ok)
		}
	}
	c := DefaultConfig()
	c.MetricsListen = "0.0.0.0:9100"
	if c.Validate() == nil {
		t.Error("Validate accepted a public metrics address")
	}
	c.MetricsListen, c.MinHubVersion = "", "1.0"
	if c.Validate() == nil {
		t.Error("Validate accepted min_hub_version 1.0")
	}
}

func TestMetricsHandler(t *testing.T) {
	s := &Server{byID: map[uint32]*hubSession{}}
	s.counters.handshakes.Add(3)
	s.counters.rateLimited.Add(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewUnstartedServer(nil)
	done := make(chan error, 1)
	go func() { done <- s.serveMetrics(ctx, srv.Listener) }()
	resp, err := http.Get("http://" + srv.Listener.Addr().String() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := new(strings.Builder)
	if _, err := io.Copy(body, resp.Body); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"xs_handshakes_total 3\n", "xs_handshakes_rate_limited_total 2\n", "xs_sessions 0\n",
		"# TYPE xs_hub_rx_bytes_total counter"} {
		if !strings.Contains(body.String(), w) {
			t.Errorf("metrics lack %q:\n%s", w, body)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	c, err := LoadConfig("../../deploy/server/server.example.yaml", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(c.EgressAllow) != 0 || c.MinHubVersion != "" || c.MetricsListen != "" {
		t.Fatalf("example sets hardening options: %+v", c)
	}
}

func TestRestrictedPortCoversPanelAndMetrics(t *testing.T) {
	s := &Server{o: Options{Config: Config{WebListen: "0.0.0.0:8443", WebIngressListen: "0.0.0.0:8099", MetricsListen: "127.0.0.1:9100"}}}
	for _, port := range []uint16{8443, 8099, 9100, sshPort} {
		if !s.restrictedPort(wire.ProtoTCP, port) {
			t.Errorf("tcp/%d may be forwarded to a hub", port)
		}
	}
}
