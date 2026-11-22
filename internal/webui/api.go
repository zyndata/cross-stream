package webui

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

const maxBody = 8 << 10

func (s *Server) routes() {
	m := http.NewServeMux()
	if !s.o.Ingress { // ingress: HA does the login; /api/login and /api/logout answer 404
		m.HandleFunc("POST /api/login", s.login)
		m.HandleFunc("POST /api/logout", s.auth(true, s.logout))
	}
	m.HandleFunc("GET /api/session", s.auth(false, func(w http.ResponseWriter, _ *http.Request, a authed) {
		v := sessionView{User: a.sess.user, CSRF: a.sess.csrf, Ingress: s.o.Ingress}
		if !s.o.Ingress {
			v.Expires = &a.sess.expires
		}
		writeJSON(w, http.StatusOK, v)
	}))
	m.HandleFunc("GET /api/hubs", s.auth(false, func(w http.ResponseWriter, r *http.Request, _ authed) {
		hubs, err := s.o.Admin.Hubs(r.Context())
		reply(w, hubs, err)
	}))
	m.HandleFunc("GET /api/sessions", s.auth(false, func(w http.ResponseWriter, r *http.Request, _ authed) {
		ss, err := s.o.Admin.Sessions(r.Context())
		reply(w, ss, err)
	}))
	m.HandleFunc("GET /api/info", s.auth(false, func(w http.ResponseWriter, r *http.Request, _ authed) {
		in, err := s.o.Admin.Info(r.Context())
		reply(w, infoView{ServerInfo: in, WebFingerprint: Fingerprint(s.o.Cert)}, err)
	}))
	m.HandleFunc("GET /api/audit", s.auth(false, func(w http.ResponseWriter, r *http.Request, _ authed) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 || n > admin.AuditKeep {
			n = 200
		}
		writeJSON(w, http.StatusOK, s.o.Admin.AuditLog(r.Context(), n))
	}))
	m.HandleFunc("GET /api/logs", s.auth(false, func(w http.ResponseWriter, r *http.Request, _ authed) {
		writeJSON(w, http.StatusOK, s.o.Admin.Logs(r.Context(), LogLines))
	}))
	m.HandleFunc("GET /api/stream", s.auth(false, s.serveStream))
	m.HandleFunc("POST /api/hubs/enroll", s.auth(true, func(w http.ResponseWriter, r *http.Request, a authed) {
		var req struct{ Name string }
		if decode(w, r, &req) {
			res, err := s.o.Admin.Enroll(a.ctx, strings.TrimSpace(req.Name))
			reply(w, res, err)
		}
	}))
	m.HandleFunc("POST /api/hubs/revoke", s.auth(true, func(w http.ResponseWriter, r *http.Request, a authed) {
		var req struct{ Name string }
		if decode(w, r, &req) {
			reply(w, ok{}, s.o.Admin.Revoke(a.ctx, req.Name))
		}
	}))
	m.HandleFunc("POST /api/hubs/rename", s.auth(true, func(w http.ResponseWriter, r *http.Request, a authed) {
		var req struct {
			Name    string
			NewName string `json:"new_name"`
		}
		if decode(w, r, &req) {
			reply(w, ok{}, s.o.Admin.Rename(a.ctx, req.Name, strings.TrimSpace(req.NewName)))
		}
	}))
	m.HandleFunc("POST /api/hubs/forward", s.auth(true, func(w http.ResponseWriter, r *http.Request, a authed) {
		var req struct {
			Name string
			N    int
		}
		if decode(w, r, &req) {
			reply(w, ok{}, s.o.Admin.ForwardAllow(a.ctx, req.Name, req.N))
		}
	}))
	// The panel only changes the port where it is configured (PortSource);
	// it keeps no copy. Applied at the next start.
	m.HandleFunc("POST /api/server/tunnel-port", s.auth(true, func(w http.ResponseWriter, r *http.Request, a authed) {
		var req struct{ Port int }
		if decode(w, r, &req) {
			reply(w, ok{}, s.o.Admin.SetTunnelPort(a.ctx, req.Port))
		}
	}))
	m.HandleFunc("POST /api/server/restart", s.auth(true, func(w http.ResponseWriter, _ *http.Request, a authed) {
		reply(w, ok{}, s.o.Admin.Restart(a.ctx))
	}))
	m.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusNotFound, "not found")
	})
	m.Handle("/", s.static())
	s.mux = m
}

type ok struct {
	OK bool `json:"ok"`
}

type sessionView struct {
	User    string     `json:"user"`
	CSRF    string     `json:"csrf"`
	Expires *time.Time `json:"expires,omitempty"`
	Ingress bool       `json:"ingress,omitempty"` // behind HA ingress: no login/logout
}

type infoView struct {
	admin.ServerInfo
	WebFingerprint string `json:"web_fingerprint"`
}

// authed is a request with a valid session; ctx carries the audit actor.
type authed struct {
	sess  session
	token string
	ctx   context.Context
}

// auth wraps h: a valid session cookie is required, and for writes also the
// same origin and the session's CSRF token.
func (s *Server) auth(write bool, h func(http.ResponseWriter, *http.Request, authed)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.o.Ingress {
			if a, ok := s.ingressAuth(w, r, write); ok {
				h(w, r, a)
			}
			return
		}
		c, err := r.Cookie(SessionCookie)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
		sess, ok := s.sessions.get(c.Value)
		if ok {
			changed, exists := s.o.Admin.UserChanged(sess.user)
			if !exists || changed.After(sess.created) {
				s.sessions.drop(c.Value)
				ok = false
			}
		}
		if !ok {
			writeErr(w, http.StatusUnauthorized, "session expired")
			return
		}
		if write && (!sameOrigin(r) || !csrfOK(sess, r.Header.Get(CSRFHeader))) {
			writeErr(w, http.StatusForbidden, "bad CSRF token or origin")
			return
		}
		ctx := admin.WithActor(r.Context(), "web:"+sess.user+"@"+clientIP(r))
		h(w, r, authed{sess: sess, token: c.Value, ctx: ctx})
	}
}

type loginReq struct {
	User     string `json:"user"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !isJSON(r) || !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "bad request origin or type")
		return
	}
	ip := clientIP(r)
	if wait := s.limiter.allow(ip); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Round(time.Second)/time.Second)+1))
		writeErr(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}
	var req loginReq
	if !decode(w, r, &req) {
		return
	}
	user := req.User
	if len(user) > 64 {
		user = user[:64]
	}
	actx := admin.WithActor(r.Context(), "web:"+user+"@"+ip)
	err := s.o.Admin.Authenticate(r.Context(), req.User, req.Password, req.Code)
	switch {
	case errors.Is(err, admin.ErrTOTPRequired):
		writeErr(w, http.StatusUnauthorized, "totp_required")
		return
	case err != nil:
		locked := s.limiter.result(ip, false)
		s.o.Admin.Record(actx, "login", user, admin.ErrBadLogin)
		if locked {
			s.o.Admin.Record(actx, "login_lockout", ip, nil)
			s.log.Warn("web login locked out", "ip", ip, "for", LockoutDuration)
		}
		writeErr(w, http.StatusUnauthorized, "invalid user name, password or code")
		return
	}
	s.limiter.result(ip, true)
	token, sess := s.sessions.create(req.User)
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: token, Path: "/", MaxAge: int(SessionTTL / time.Second),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	s.o.Admin.Record(actx, "login", req.User, nil)
	writeJSON(w, http.StatusOK, sessionView{User: sess.user, CSRF: sess.csrf, Expires: &sess.expires})
}

func (s *Server) logout(w http.ResponseWriter, _ *http.Request, a authed) {
	s.sessions.drop(a.token)
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	s.o.Admin.Record(a.ctx, "logout", a.sess.user, nil)
	writeJSON(w, http.StatusOK, ok{OK: true})
}

// static serves the built panel, or a placeholder when it was not built
// (plain `go build` without the node stage).
func (s *Server) static() http.Handler {
	h := s.staticFiles()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) staticFiles() http.Handler {
	if s.o.Assets != nil {
		if _, err := fs.Stat(s.o.Assets, "index.html"); err == nil {
			files := http.FileServerFS(s.o.Assets)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // hashed names
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
			})
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholder))
	})
}

const placeholder = `<!doctype html>
<html lang="pl"><head><meta charset="utf-8"><title>CrossStream</title></head>
<body><h1>CrossStream</h1><p>Panel WWW nie został zbudowany w tym pliku binarnym
(obraz Docker buduje go w etapie node; lokalnie: <code>make webui</code>, potem go build).
API działa pod /api/.</p></body></html>
`

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !isJSON(r) {
		writeErr(w, http.StatusUnsupportedMediaType, "want application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body")
		return false
	}
	return true
}

// reply writes v, or err as 400 (admin errors are messages for the owner).
func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
