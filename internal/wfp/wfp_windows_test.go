package wfp

import (
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// udpSend sends one datagram to addr and returns the send error (WFP
// authorizes a UDP flow at its first send).
func udpSend(t *testing.T, addr string) error {
	t.Helper()
	c, err := net.Dial("udp4", addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_, err = c.Write([]byte("x"))
	return err
}

// TestLeakFilters (elevated only, run on the Windows hub laptop next to the
// phase 19 elevated tests): with the filters in place a DNS connect outside
// the tunnel fails with WSAEACCES, other ports do not, an IPv6 connect fails
// at once; closing the session leaves no sublayer (and so no filter). The
// permitted app is another program, so the test binary itself is filtered;
// the tunnel LUID matches no interface.
func TestLeakFilters(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated (administrator) process")
	}
	const dnsAddr, otherAddr = "192.0.2.1:53", "192.0.2.1:54" // TEST-NET-1: never answers
	if err := udpSend(t, dnsAddr); err != nil {
		t.Skipf("no IPv4 route to send a test datagram: %v", err)
	}
	if ok, err := SubLayerExists(); err != nil || ok {
		t.Fatalf("sublayer before the test: %v %v", ok, err)
	}
	s, err := Open("CrossStream test", Wanted(true, 0x1, `C:\Windows\System32\svchost.exe`))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := SubLayerExists(); err != nil || !ok {
		s.Close()
		t.Fatalf("sublayer while open: %v %v", ok, err)
	}
	// A blocked UDP send still reports success (WFP drops the datagram), so
	// the port-53 block is checked with a TCP connect: refused at once.
	if _, err := net.DialTimeout("tcp4", dnsAddr, 2*time.Second); !errors.Is(err, windows.WSAEACCES) {
		t.Errorf("TCP DNS connect outside the tunnel: %v, want WSAEACCES", err)
	}
	if _, err := net.DialTimeout("tcp4", otherAddr, time.Second); errors.Is(err, windows.WSAEACCES) {
		t.Errorf("port 54 blocked too: %v", err)
	}
	t0 := time.Now()
	c, err := net.DialTimeout("tcp6", "[2001:db8::1]:80", 5*time.Second)
	took := time.Since(t0)
	if err == nil {
		_ = c.Close()
		t.Error("IPv6 connect outside the tunnel succeeded")
	} else if took > time.Second {
		t.Errorf("IPv6 connect failed after %v (%v), want at once", took, err)
	} else {
		t.Logf("IPv6 connect: %v after %v", err, took)
	}
	s.Close()
	s.Close() // twice is fine
	if ok, err := SubLayerExists(); err != nil || ok {
		t.Fatalf("sublayer after Close: %v %v", ok, err)
	}
	if err := udpSend(t, dnsAddr); err != nil {
		t.Errorf("DNS send after Close: %v", err)
	}
}
