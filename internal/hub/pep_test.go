package hub

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/zyndata/cross-stream/internal/direct"
)

func TestParsePEPExclude(t *testing.T) {
	tests := []struct {
		name    string
		list    []string
		dst     string
		want    bool // excluded
		wantErr bool
	}{
		{"empty", nil, "93.184.216.34:443", false, false},
		{"port", []string{"22"}, "93.184.216.34:22", true, false},
		{"other port", []string{"22"}, "93.184.216.34:443", false, false},
		{"range", []string{"5000-5100"}, "1.2.3.4:5050", true, false},
		{"range end", []string{"5000-5100"}, "1.2.3.4:5101", false, false},
		{"cidr", []string{"10.0.0.0/8"}, "10.9.9.9:80", true, false},
		{"address", []string{" 192.0.2.7 "}, "192.0.2.7:80", true, false},
		{"tunnel network always", nil, "100.64.0.5:80", true, false},
		{"loopback always", nil, "127.0.0.1:80", true, false},
		{"multicast always", nil, "239.1.1.1:80", true, false},
		{"bad word", []string{"ssh"}, "", false, true},
		{"port 0", []string{"0"}, "", false, true},
		{"reversed range", []string{"10-5"}, "", false, true},
		{"ipv6", []string{"2001:db8::/32"}, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := parsePEPExclude(tt.list)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err %v, want error %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got := r.excluded(netip.MustParseAddrPort(tt.dst)); got != tt.want {
				t.Errorf("excluded(%s) = %v, want %v", tt.dst, got, tt.want)
			}
		})
	}
}

// tcpPkt builds an IPv4 TCP header with flags to dst.
func tcpPkt(dst string, flags byte, frag uint16) []byte {
	ap := netip.MustParseAddrPort(dst)
	p := make([]byte, 40)
	p[0], p[9] = 0x45, 6
	binary.BigEndian.PutUint16(p[6:], frag)
	a := ap.Addr().As4()
	copy(p[16:], a[:])
	binary.BigEndian.PutUint16(p[22:], ap.Port())
	p[33] = flags
	return p
}

func TestTCPSYN(t *testing.T) {
	tests := []struct {
		name string
		pkt  []byte
		want bool
	}{
		{"syn", tcpPkt("1.2.3.4:443", 0x02, 0), true},
		{"syn-ack", tcpPkt("1.2.3.4:443", 0x12, 0), false},
		{"ack", tcpPkt("1.2.3.4:443", 0x10, 0), false},
		{"fragment", tcpPkt("1.2.3.4:443", 0x02, 0x2000), false},
		{"udp", func() []byte { p := tcpPkt("1.2.3.4:443", 0x02, 0); p[9] = 17; return p }(), false},
		{"short", []byte{0x45, 0, 0}, false},
	}
	for _, tt := range tests {
		dst, ok := tcpSYN(tt.pkt)
		if ok != tt.want || (ok && dst != netip.MustParseAddrPort("1.2.3.4:443")) {
			t.Errorf("%s: %v %v", tt.name, dst, ok)
		}
	}
}

func TestPEPSettings(t *testing.T) {
	dir := shortTemp(t)
	d := newTestDaemon(t, dir, DaemonConfig{})
	if s := d.Settings(); s.PEP != DefaultPEP || s.PEPExclude == nil {
		t.Fatalf("default pep %q exclude %v", s.PEP, s.PEPExclude)
	}
	for _, bad := range []string{`{"pep":"maybe"}`, `{"pep_exclude":["ssh"]}`} {
		if _, err := d.handleRPC(context.Background(), MethodSettingsSet, json.RawMessage(bad)); err == nil {
			t.Errorf("settings.set %s accepted", bad)
		}
	}
	res, err := d.handleRPC(context.Background(), MethodSettingsSet, json.RawMessage(`{"pep":"on","pep_exclude":["22","10.0.0.0/8"]}`))
	if err != nil || res.(Settings).PEP != PEPOn || len(res.(Settings).PEPExclude) != 2 {
		t.Fatalf("set %v %v", res, err)
	}
	if !d.pepRules.Load().excluded(netip.MustParseAddrPort("8.8.8.8:22")) {
		t.Error("new exclusions not applied")
	}
	// No tunnel yet: PEP is on but not active, SYNs stay in packet mode.
	if d.pepActive() || d.pepPacket(tcpPkt("1.2.3.4:443", 0x02, 0)) {
		t.Error("PEP active without a server session")
	}
	if _, ok := d.pepIntercept(direct.FlowKey{Proto: 6, Dst: netip.MustParseAddrPort("1.2.3.4:443")}); ok {
		t.Error("intercepted without a server session")
	}
	if st := d.Status().PEP; st == nil || !st.On || st.Available {
		t.Errorf("status %+v", st)
	}
	_ = d.ctlLn.Close()
	_ = d.data.Close()
	d2 := newTestDaemon(t, dir, DaemonConfig{})
	if s := d2.Settings(); s.PEP != PEPOn || len(s.PEPExclude) != 2 || !d2.pepRules.Load().excluded(netip.MustParseAddrPort("10.1.1.1:80")) {
		t.Errorf("after restart %+v", s)
	}
}
