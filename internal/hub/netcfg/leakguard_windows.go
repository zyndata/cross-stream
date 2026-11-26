package netcfg

import (
	"fmt"
	"os"
	"sync"

	"github.com/zyndata/cross-stream/internal/wfp"
)

// LeakGuard keeps the Windows hub's DNS and IPv6 leak filters (phase 21
// W2): a dynamic WFP session that exists exactly while the TUN split routes
// do. Every copy of a LAN shares it (pointer).
type LeakGuard struct {
	mu   sync.Mutex
	app  string // xs-hubd's own path: its sockets are permitted
	luid uint64 // tunnel LUID the open session permits
	s    *wfp.Session
}

// NewLeakGuard returns the guard for this process.
func NewLeakGuard() *LeakGuard {
	app, _ := os.Executable() // "" makes ensure fail: without the app permit local paths lose DNS
	return &LeakGuard{app: app}
}

// ensure opens the filters for the tunnel luid when want, closes them
// otherwise, and re-opens a session that vanished (BFE restart). It returns
// what it changed ("" = nothing).
func (g *LeakGuard) ensure(want bool, luid uint64) (string, error) {
	if g == nil {
		return "", nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.s != nil && (!want || g.luid != luid) {
		g.s.Close()
		g.s = nil
		if !want {
			return "leak filters removed (no path)", nil
		}
	}
	if !want {
		return "", nil
	}
	if g.s != nil {
		if ok, err := wfp.SubLayerExists(); err != nil || ok {
			return "", nil
		}
		g.s.Close() // lost: open it again
		g.s = nil
	}
	if g.app == "" {
		return "", fmt.Errorf("leak filters: path of xs-hubd unknown")
	}
	s, err := wfp.Open("CrossStream hub", wfp.Wanted(true, luid, g.app))
	if err != nil {
		return "", fmt.Errorf("leak filters: %w", err)
	}
	g.s, g.luid = s, luid
	return "leak filters (DNS, IPv6)", nil
}

// Close removes the filters.
func (g *LeakGuard) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.s != nil {
		g.s.Close()
		g.s = nil
	}
}
