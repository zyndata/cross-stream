package hub

import (
	"net/netip"
	"testing"

	"github.com/zyndata/cross-stream/internal/wire"
)

// TestConfigFramesRepeat: a changed PATH_CONFIG goes out now and
// ConfigRepeats more times (lost datagrams), then only with a full resend.
func TestConfigFramesRepeat(t *testing.T) {
	c := &Client{}
	c.AddPath(3, netip.MustParseAddrPort("10.0.0.3:1"), nil, wire.PathConfig{Priority: wire.PriorityBackup})
	count := func(full bool, capped map[uint8]bool) (int, *wire.PathConfig) {
		fs := c.configFrames(capped, full)
		if len(fs) == 0 {
			return 0, nil
		}
		return len(fs), fs[0].(*wire.PathConfig)
	}
	for i := 0; i <= ConfigRepeats; i++ {
		if n, f := count(false, nil); n != 1 || f.Priority != wire.PriorityBackup || f.PathID != 3 {
			t.Fatalf("send %d: %d frames %+v", i, n, f)
		}
	}
	if n, _ := count(false, nil); n != 0 {
		t.Fatalf("after %d repeats: %d frames", ConfigRepeats, n)
	}
	if n, _ := count(true, nil); n != 1 {
		t.Fatalf("full: %d frames", n)
	}
	if n, f := count(false, map[uint8]bool{3: true}); n != 1 || f.DirUp != wire.DirOff || f.DirDown != wire.DirOff {
		t.Fatalf("capped: %d %+v", n, f)
	}
	for i := 0; i < ConfigRepeats; i++ {
		if n, _ := count(false, map[uint8]bool{3: true}); n != 1 {
			t.Fatalf("capped repeat %d: %d frames", i, n)
		}
	}
	if n, _ := count(false, map[uint8]bool{3: true}); n != 0 {
		t.Fatalf("capped after repeats: %d frames", n)
	}
}
