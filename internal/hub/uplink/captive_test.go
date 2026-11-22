package uplink

import (
	"context"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"
)

func TestProbeCaptive(t *testing.T) {
	block := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/open", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/empty200", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://portal.example/login?mac=aa", http.StatusFound)
	})
	mux.HandleFunc("/relative", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/login")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>login</html>")) })
	mux.HandleFunc("/error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	mux.HandleFunc("/hang", func(http.ResponseWriter, *http.Request) {
		select {
		case <-block:
		case <-time.After(10 * time.Second):
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(block) }) // runs before srv.Close (LIFO): frees /hang

	var bound int
	control := func(string, string, syscall.RawConn) error { bound++; return nil }
	tests := []struct {
		path    string
		state   string
		url     string
		slowest time.Duration
	}{
		{"/open", NetOpen, "", time.Second},
		{"/empty200", NetOpen, "", time.Second},
		{"/redirect", NetCaptive, "http://portal.example/login?mac=aa", time.Second},
		{"/relative", NetCaptive, srv.URL + "/login", time.Second},
		{"/page", NetCaptive, srv.URL + "/page", time.Second},
		{"/error", NetNoAccess, "", time.Second},
		{"/hang", NetNoAccess, "", ProbeTimeout + 2*time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			start := time.Now()
			got := ProbeCaptive(context.Background(), srv.URL+tc.path, control)
			if got.State != tc.state || got.URL != tc.url {
				t.Fatalf("got %+v, want %s %q", got, tc.state, tc.url)
			}
			if tc.state == NetNoAccess && got.Err == "" {
				t.Fatal("no error text")
			}
			if d := time.Since(start); d > tc.slowest {
				t.Fatalf("took %v", d)
			}
		})
	}
	if bound != len(tests) {
		t.Fatalf("control used %d times, want %d (one fresh socket per probe)", bound, len(tests))
	}
	if got := ProbeCaptive(context.Background(), "http://127.0.0.1:1/generate_204", control); got.State != NetNoAccess {
		t.Fatalf("refused: %+v", got)
	}
}
