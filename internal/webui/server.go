package webui

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

// Admin is what the panel needs from admin.Service (tests use a fake).
type Admin interface {
	Authenticate(ctx context.Context, user, password, code string) error
	UserChanged(user string) (time.Time, bool)
	Users(ctx context.Context) []admin.UserInfo
	Record(ctx context.Context, action, target string, err error)
	Enroll(ctx context.Context, name string) (admin.EnrollResult, error)
	Hubs(ctx context.Context) ([]admin.Hub, error)
	Revoke(ctx context.Context, name string) error
	Rename(ctx context.Context, name, newName string) error
	Sessions(ctx context.Context) ([]admin.Session, error)
	ForwardAllow(ctx context.Context, name string, n int) error
	Info(ctx context.Context) (admin.ServerInfo, error)
	SetTunnelPort(ctx context.Context, port int) error
	Restart(ctx context.Context) error
	AuditLog(ctx context.Context, n int) []admin.AuditEntry
	Logs(ctx context.Context, n int) []string
}

// CSP is the Content-Security-Policy of every response: everything from
// the panel's own origin, no inline scripts or styles, no eval, no framing.
const CSP = "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

// LogLines is how many recent log lines the panel shows.
const LogLines = 200

// Options configure the panel.
type Options struct {
	Admin  Admin
	Assets fs.FS          // built panel (index.html, assets/); nil = placeholder page
	Deny   []netip.Prefix // client networks refused (the tunnel network: hubs never reach the panel)
	Cert   tls.Certificate
	Log    *slog.Logger
	Now    func() time.Time
	// Ingress serves the panel behind Home Assistant ingress (ServeIngress):
	// plain HTTP, only from IngressAllow, no login (HA authenticates; the
	// user comes from X-Remote-User-Name), writes still need the CSRF token.
	Ingress      bool
	IngressAllow []netip.Prefix
}

// Server is the web panel.
type Server struct {
	o        Options
	log      *slog.Logger
	sessions *sessions
	limiter  *loginLimiter
	stream   *sampler
	mux      *http.ServeMux
	ingress  session // Ingress: the per-process CSRF token every request shares
}

// New returns a panel; Serve starts it.
func New(o Options) *Server {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Server{o: o, log: o.Log, sessions: newSessions(o.Now), limiter: newLoginLimiter(o.Now)}
	if o.Ingress {
		s.ingress = session{user: IngressUser, csrf: randToken(), created: o.Now()}
	}
	s.stream = newSampler(o.Admin, o.Now, o.Log)
	s.routes()
	return s
}

// Handler is the panel's HTTP handler (security headers, deny list, routes).
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if s.o.Ingress { // HA shows the panel in an iframe of its own origin
			h.Set("Content-Security-Policy", IngressCSP)
			h.Set("X-Frame-Options", "SAMEORIGIN")
		} else {
			h.Set("Content-Security-Policy", CSP)
			h.Set("X-Frame-Options", "DENY")
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if from, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
			a := from.Addr().Unmap()
			if contains(s.o.Deny, a) || (s.o.Ingress && !contains(s.o.IngressAllow, a)) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		} else if s.o.Ingress {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

// Serve runs the panel over TLS on ln until ctx is done.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	hs := s.httpServer(ctx)
	hs.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{s.o.Cert}}
	if len(s.o.Admin.Users(ctx)) == 0 {
		s.log.Warn("web panel has no admin users yet: run xs-ctl admin add <user>")
	}
	return s.run(ctx, hs, func() error { return hs.ServeTLS(ln, "", "") })
}

// httpServer returns the http.Server shared by the HTTPS and ingress listeners.
func (s *Server) httpServer(ctx context.Context) *http.Server {
	return &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		// Browsers that do not trust the self-signed certificate yet log a
		// handshake error per connection: keep that at debug level.
		ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
}

// run serves hs with serve until ctx is done, then shuts it down.
func (s *Server) run(ctx context.Context, hs *http.Server, serve func() error) error {
	go s.stream.run(ctx)
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	err := serve()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("web panel: %w", err)
}

func contains(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientIP is the request's peer address (no proxy headers are trusted).
func clientIP(r *http.Request) string {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap().String()
	}
	return r.RemoteAddr
}

// sameOrigin reports whether the Origin header, when present, names this host.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Scheme == "https" && u.Host == r.Host
}

// isJSON reports whether the request body is declared as JSON (a cross-site
// form cannot send that without a CORS preflight, which we never answer).
func isJSON(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}
