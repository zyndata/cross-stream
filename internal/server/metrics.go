package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"
)

// counters are server-wide event counts exported on /metrics.
type counters struct {
	handshakes   atomic.Uint64 // answered INITs
	rateLimited  atomic.Uint64 // INITs dropped by the per-IP limit
	replayed     atomic.Uint64 // INITs with a stale timestamp
	versionDeny  atomic.Uint64 // INITs refused by min_hub_version
	streamDenied atomic.Uint64 // TCP/WSS connections over the per-IP cap
}

// checkMetricsListen accepts host:port where host is a loopback or private
// IPv4/IPv6 address: /metrics is never served on a public address.
func checkMetricsListen(addr string) error {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return fmt.Errorf("config: metrics_listen %q: want ip:port: %w", addr, err)
	}
	a := ap.Addr().Unmap()
	if !a.IsLoopback() && !a.IsPrivate() || TunnelPrefix.Contains(a) {
		return fmt.Errorf("config: metrics_listen %q must be a loopback or private address", addr)
	}
	return nil
}

// serveMetrics serves /metrics on ln until ctx is done. Requests from the
// tunnel network are refused, so hubs never see other hubs' counters.
func (s *Server) serveMetrics(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if from, err := netip.ParseAddrPort(r.RemoteAddr); err != nil || TunnelPrefix.Contains(from.Addr().Unmap()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		s.writeMetrics(r.Context(), w)
	})
	hs := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = hs.Close()
	}()
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("metrics: %w", err)
	}
	return nil
}

// writeMetrics writes the Prometheus text format.
func (s *Server) writeMetrics(ctx context.Context, w io.Writer) {
	var b strings.Builder
	metric := func(name, typ, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	c := &s.counters
	for _, m := range []struct {
		name, help string
		v          uint64
	}{
		{"xs_handshakes_total", "Handshakes answered.", c.handshakes.Load()},
		{"xs_handshakes_rate_limited_total", "Handshakes dropped by the per-IP rate limit.", c.rateLimited.Load()},
		{"xs_handshakes_replayed_total", "Handshakes dropped as replays (stale timestamp).", c.replayed.Load()},
		{"xs_handshakes_version_refused_total", "Handshakes refused by min_hub_version.", c.versionDeny.Load()},
		{"xs_stream_conns_refused_total", "TCP/WSS connections refused by the per-IP cap.", c.streamDenied.Load()},
	} {
		metric(m.name, "counter", m.help)
		fmt.Fprintf(&b, "%s %d\n", m.name, m.v)
	}
	sessions, _ := s.Sessions(ctx)
	metric("xs_sessions", "gauge", "Hub sessions in the table.")
	fmt.Fprintf(&b, "xs_sessions %d\n", len(sessions))
	metric("xs_hub_rx_bytes_total", "counter", "Bytes received from the hub.")
	for _, ss := range sessions {
		fmt.Fprintf(&b, "xs_hub_rx_bytes_total{hub=%q} %d\n", ss.Hub, ss.RxBytes)
	}
	metric("xs_hub_tx_bytes_total", "counter", "Bytes sent to the hub.")
	for _, ss := range sessions {
		fmt.Fprintf(&b, "xs_hub_tx_bytes_total{hub=%q} %d\n", ss.Hub, ss.TxBytes)
	}
	metric("xs_hub_paths", "gauge", "Paths of the hub session.")
	for _, ss := range sessions {
		fmt.Fprintf(&b, "xs_hub_paths{hub=%q} %d\n", ss.Hub, len(ss.Paths))
	}
	_, _ = io.WriteString(w, b.String())
}
