package webui

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"unicode"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

// Home Assistant ingress (phase 17b): HA's proxy (172.30.32.2 by default)
// forwards the panel at /api/hassio_ingress/<token>/ after its own login.
const (
	// IngressUser is the audit user when HA sends no X-Remote-User-Name.
	IngressUser = "home-assistant"
	// IngressUserHeader names the HA user behind an ingress request.
	IngressUserHeader = "X-Remote-User-Name"
	// IngressCSP is CSP with framing by the same origin: HA shows the panel
	// in an iframe of its own origin, which is also the panel's origin.
	IngressCSP = "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'; form-action 'self'"
	// DefaultIngressAllow is the HA Supervisor's ingress proxy address.
	DefaultIngressAllow = "172.30.32.2"
	maxIngressUser      = 64
)

// ParseAllow parses addresses and prefixes ("172.30.32.2", "10.0.0.0/8").
func ParseAllow(list []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		s = strings.TrimSpace(s)
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, fmt.Errorf("%q: %w", s, err)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", s, err)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// ServeIngress runs the panel over plain HTTP on ln for HA ingress until
// ctx is done. A connection from outside IngressAllow is closed before a
// byte of it is read.
func (s *Server) ServeIngress(ctx context.Context, ln net.Listener) error {
	if !s.o.Ingress {
		return fmt.Errorf("web panel: ServeIngress needs Options.Ingress")
	}
	hs := s.httpServer(ctx)
	al := &allowListener{Listener: ln, allow: s.o.IngressAllow, s: s}
	return s.run(ctx, hs, func() error { return hs.Serve(al) })
}

// allowListener drops connections from sources outside allow.
type allowListener struct {
	net.Listener
	allow []netip.Prefix
	s     *Server
}

func (l *allowListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil && contains(l.allow, ap.Addr().Unmap()) {
			return c, nil
		}
		l.s.log.Debug("ingress connection refused", "from", c.RemoteAddr().String())
		_ = c.Close()
	}
}

// ingressUser is the HA user name of r, reduced to letters, digits and
// ".-_@ " (it lands in audit entries and logs).
func ingressUser(r *http.Request) string {
	var b strings.Builder
	for _, c := range strings.TrimSpace(r.Header.Get(IngressUserHeader)) {
		if b.Len() >= maxIngressUser {
			break
		}
		if unicode.IsLetter(c) || unicode.IsDigit(c) || strings.ContainsRune(".-_@ ", c) {
			b.WriteRune(c)
		}
	}
	if u := strings.TrimSpace(b.String()); u != "" {
		return u
	}
	return IngressUser
}

// ingressAuth is auth() in ingress mode: no cookie, the session is the
// process-wide one with the request's HA user; writes need the CSRF token
// (the Origin is HA's, so sameOrigin does not apply).
func (s *Server) ingressAuth(w http.ResponseWriter, r *http.Request, write bool) (authed, bool) {
	sess := s.ingress
	sess.user = ingressUser(r)
	if write && !csrfOK(sess, r.Header.Get(CSRFHeader)) {
		writeErr(w, http.StatusForbidden, "bad CSRF token")
		return authed{}, false
	}
	ctx := admin.WithActor(r.Context(), "ha:"+sess.user)
	return authed{sess: sess, ctx: ctx}, true
}
