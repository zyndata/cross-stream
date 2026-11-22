package tun

import (
	"bytes"
	"errors"
	"net/netip"
	"testing"
)

func TestPipe(t *testing.T) {
	a, b := Pipe("a0", "b0", 1500)
	if a.Name() != "a0" || b.Name() != "b0" || a.MTU() != 1500 {
		t.Fatalf("names/MTU: %s %s %d", a.Name(), b.Name(), a.MTU())
	}
	pkt := []byte{0x45, 1, 2, 3}
	if _, err := a.Write(pkt); err != nil {
		t.Fatal(err)
	}
	pkt[1] = 9 // the pipe must have copied
	buf := make([]byte, 1500)
	n, err := b.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], []byte{0x45, 1, 2, 3}) {
		t.Fatalf("read %x, %v", buf[:n], err)
	}
	if _, err := b.Write(make([]byte, 1501)); err == nil {
		t.Fatal("oversized write accepted")
	}
	if _, err := a.Write(pkt); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Read(make([]byte, 2)); err == nil {
		t.Fatal("short buffer accepted")
	}
	_ = a.Close()
	if _, err := b.Read(buf); !errors.Is(err, ErrClosed) {
		t.Fatalf("read after close: %v", err)
	}
	if _, err := b.Write(pkt); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	good := Config{Name: "xss0", MTU: 1360, Prefix: netip.MustParsePrefix("100.64.0.1/16")}
	tests := []struct {
		name string
		mod  func(*Config)
		ok   bool
	}{
		{"valid", func(*Config) {}, true},
		{"no name", func(c *Config) { c.Name = "" }, false},
		{"small mtu", func(c *Config) { c.MTU = 100 }, false},
		{"big mtu", func(c *Config) { c.MTU = 70000 }, false},
		{"no prefix", func(c *Config) { c.Prefix = netip.Prefix{} }, false},
		{"ipv6", func(c *Config) { c.Prefix = netip.MustParsePrefix("fd00::1/64") }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := good
			tt.mod(&c)
			if err := c.validate(); (err == nil) != tt.ok {
				t.Fatalf("validate = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}
