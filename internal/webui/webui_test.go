package webui

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func jsonField(t *testing.T, body, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("json %q: %v", body, err)
	}
	s, _ := m[key].(string)
	return s
}

func TestLoginCookieFlags(t *testing.T) {
	e := newEnv(t)
	w := e.do("POST", "/api/login", `{"user":"alice","password":"correct horse"}`, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("login %d %s", w.Code, w.Body)
	}
	raw := w.Header().Get("Set-Cookie")
	for _, want := range []string{SessionCookie + "=", "Path=/", "Max-Age=43200", "HttpOnly", "Secure", "SameSite=Strict"} {
		if !strings.Contains(raw, want) {
			t.Errorf("Set-Cookie %q lacks %q", raw, want)
		}
	}
	if strings.Contains(raw, "Domain=") {
		t.Errorf("__Host- cookie with Domain: %q", raw)
	}
	if jsonField(t, w.Body.String(), "csrf") == "" {
		t.Fatal("no CSRF token in login response")
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control %q", got)
	}
	if r := e.adm.last(); r.action != "login" || !r.ok || r.actor != "web:alice@192.0.2.1" {
		t.Fatalf("audit %+v", r)
	}
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		hdr    []string
		want   int
		errMsg string
	}{
		{"ok", `{"user":"alice","password":"correct horse"}`, nil, 200, ""},
		{"wrong password", `{"user":"alice","password":"nope"}`, nil, 401, "invalid user name"},
		{"unknown user", `{"user":"zed","password":"correct horse"}`, nil, 401, "invalid user name"},
		{"totp required", `{"user":"bob","password":"battery staple"}`, nil, 401, "totp_required"},
		{"totp wrong", `{"user":"bob","password":"battery staple","code":"000000"}`, nil, 401, "invalid user name"},
		{"totp ok", `{"user":"bob","password":"battery staple","code":"123456"}`, nil, 200, ""},
		{"form post", `user=alice`, []string{"Content-Type", "application/x-www-form-urlencoded"}, 403, "bad request"},
		{"cross origin", `{"user":"alice","password":"correct horse"}`, []string{"Origin", "https://evil.example"}, 403, "bad request"},
		{"http origin", `{"user":"alice","password":"correct horse"}`, []string{"Origin", "http://" + testHost}, 403, "bad request"},
		{"same origin", `{"user":"alice","password":"correct horse"}`, []string{"Origin", "https://" + testHost}, 200, ""},
		{"unknown field", `{"user":"alice","password":"correct horse","admin":true}`, nil, 400, "bad request body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			w := e.do("POST", "/api/login", tt.body, "", "", tt.hdr...)
			if w.Code != tt.want {
				t.Fatalf("code %d, want %d (%s)", w.Code, tt.want, w.Body)
			}
			if tt.errMsg != "" && !strings.Contains(jsonField(t, w.Body.String(), "error"), tt.errMsg) {
				t.Fatalf("body %s, want %q", w.Body, tt.errMsg)
			}
		})
	}
}

func TestLoginLockout(t *testing.T) {
	e := newEnv(t)
	bad := `{"user":"alice","password":"wrong"}`
	good := `{"user":"alice","password":"correct horse"}`
	for i := range LockoutFailures {
		if w := e.do("POST", "/api/login", bad, "", ""); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if r := e.adm.last(); r.action != "login_lockout" {
		t.Fatalf("no lockout audit entry: %+v", r)
	}
	w := e.do("POST", "/api/login", good, "", "")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("locked: %d Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	// Another IP is not locked.
	if w := e.do("POST", "/api/login", good, "", "", "RemoteAddr", "192.0.2.9:1"); w.Code != 200 {
		t.Fatalf("other IP: %d", w.Code)
	}
	e.clk.add(LockoutDuration - time.Second)
	if w := e.do("POST", "/api/login", good, "", ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("still locked: %d", w.Code)
	}
	e.clk.add(2 * time.Second)
	if w := e.do("POST", "/api/login", good, "", ""); w.Code != 200 {
		t.Fatalf("after lockout: %d %s", w.Code, w.Body)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	good := `{"user":"alice","password":"correct horse"}`
	bad := `{"user":"alice","password":"wrong"}`
	// 4 failures + 1 success: 5 attempts, no lockout (success resets the run).
	for range LoginPerMinute - 1 {
		e.do("POST", "/api/login", bad, "", "")
	}
	if w := e.do("POST", "/api/login", good, "", ""); w.Code != 200 {
		t.Fatalf("5th attempt: %d", w.Code)
	}
	if w := e.do("POST", "/api/login", good, "", ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt within a minute: %d", w.Code)
	}
	e.clk.add(time.Minute)
	if w := e.do("POST", "/api/login", good, "", ""); w.Code != 200 {
		t.Fatalf("a minute later: %d", w.Code)
	}
}

func TestCSRFAndSession(t *testing.T) {
	enroll := `{"name":"h9"}`
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		cookie bool
		csrf   string // "ok" = the session's token
		hdr    []string
		want   int
	}{
		{"read without cookie", "GET", "/api/hubs", "", false, "", nil, 401},
		{"read with cookie", "GET", "/api/hubs", "", true, "", nil, 200},
		{"write without cookie", "POST", "/api/hubs/enroll", enroll, false, "ok", nil, 401},
		{"write without token", "POST", "/api/hubs/enroll", enroll, true, "", nil, 403},
		{"write wrong token", "POST", "/api/hubs/enroll", enroll, true, "nope", nil, 403},
		{"write cross origin", "POST", "/api/hubs/enroll", enroll, true, "ok", []string{"Origin", "https://evil.example"}, 403},
		{"write ok", "POST", "/api/hubs/enroll", enroll, true, "ok", nil, 200},
		{"write same origin", "POST", "/api/hubs/enroll", enroll, true, "ok", []string{"Origin", "https://" + testHost}, 200},
		{"write as form", "POST", "/api/hubs/enroll", enroll, true, "ok", []string{"Content-Type", "text/plain"}, 415},
		{"wrong method", "GET", "/api/hubs/enroll", "", true, "", nil, 404},
		{"unknown api", "GET", "/api/nope", "", true, "", nil, 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			cookie, csrf := e.login()
			if !tt.cookie {
				cookie = ""
			}
			tok := tt.csrf
			if tok == "ok" {
				tok = csrf
			}
			w := e.do(tt.method, tt.path, tt.body, cookie, tok, tt.hdr...)
			if w.Code != tt.want {
				t.Fatalf("code %d, want %d (%s)", w.Code, tt.want, w.Body)
			}
		})
	}
}

func TestSessionEnds(t *testing.T) {
	tests := []struct {
		name string
		end  func(e *env, cookie, csrf string)
	}{
		{"expiry", func(e *env, _, _ string) { e.clk.add(SessionTTL) }},
		{"logout", func(e *env, cookie, csrf string) {
			if w := e.do("POST", "/api/logout", "{}", cookie, csrf); w.Code != 200 {
				e.t.Fatalf("logout %d", w.Code)
			}
		}},
		{"password changed", func(e *env, _, _ string) {
			e.clk.add(time.Second)
			e.adm.users["alice"].changed = e.clk.now()
		}},
		{"user deleted", func(e *env, _, _ string) { delete(e.adm.users, "alice") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			cookie, csrf := e.login()
			if w := e.do("GET", "/api/session", "", cookie, ""); w.Code != 200 || jsonField(t, w.Body.String(), "csrf") != csrf {
				t.Fatalf("session %d %s", w.Code, w.Body)
			}
			tt.end(e, cookie, csrf)
			if w := e.do("GET", "/api/session", "", cookie, ""); w.Code != 401 {
				t.Fatalf("after %s: %d", tt.name, w.Code)
			}
		})
	}
}

func TestAPI(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.login()
	tests := []struct {
		name, method, path, body string
		want                     int
		contains                 string
		audit                    string // expected last audit action ("" = not checked)
	}{
		{"hubs", "GET", "/api/hubs", "", 200, `"tunnel_ip":"100.64.0.3"`, ""},
		{"sessions", "GET", "/api/sessions", "", 200, `"addr":"198.51.100.7:4000"`, ""},
		{"info", "GET", "/api/info", "", 200, `"version":"v1.2.3"`, ""},
		{"audit", "GET", "/api/audit?n=5", "", 200, `"action":"enroll"`, ""},
		{"logs", "GET", "/api/logs", "", 200, `msg=hello`, ""},
		{"enroll", "POST", "/api/hubs/enroll", `{"name":" h3 "}`, 200, `"code":"XS1.code"`, "enroll"},
		{"enroll empty", "POST", "/api/hubs/enroll", `{"name":""}`, 400, `hub name`, ""},
		{"revoke", "POST", "/api/hubs/revoke", `{"name":"h1"}`, 200, `"ok":false`, "revoke"},
		{"revoke missing", "POST", "/api/hubs/revoke", `{"name":"zz"}`, 400, `hub not found`, ""},
		{"rename", "POST", "/api/hubs/rename", `{"name":"h1","new_name":"h1b"}`, 200, ``, "rename"},
		{"forward", "POST", "/api/hubs/forward", `{"name":"h1","n":3}`, 200, ``, "forward_allow"},
		{"forward too many", "POST", "/api/hubs/forward", `{"name":"h1","n":21}`, 400, `want 0-20`, ""},
		{"bad json", "POST", "/api/hubs/forward", `{"name":`, 400, `bad request body`, ""},
		{"tunnel port", "POST", "/api/server/tunnel-port", `{"port":51901}`, 200, ``, "tunnel_port"},
		{"tunnel port out of range", "POST", "/api/server/tunnel-port", `{"port":70000}`, 400, `out of range`, ""},
		{"restart", "POST", "/api/server/restart", ``, 200, ``, "restart"},
		{"huge body", "POST", "/api/hubs/enroll", `{"name":"` + strings.Repeat("a", maxBody) + `"}`, 400, `bad request body`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := e.do(tt.method, tt.path, tt.body, cookie, csrf)
			if w.Code != tt.want || !strings.Contains(w.Body.String(), tt.contains) {
				t.Fatalf("%d %s; want %d containing %q", w.Code, w.Body, tt.want, tt.contains)
			}
			if tt.audit != "" {
				if r := e.adm.last(); r.action != tt.audit || r.actor != "web:alice@192.0.2.1" {
					t.Fatalf("audit %+v", r)
				}
			}
		})
	}
}

func TestHeadersAndDeny(t *testing.T) {
	e := newEnv(t)
	e.srv.o.Deny = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/16")}
	for _, path := range []string{"/", "/api/hubs", "/api/nope", "/missing.js"} {
		w := e.do("GET", path, "", "", "")
		if got := w.Header().Get("Content-Security-Policy"); got != CSP {
			t.Fatalf("%s: CSP %q", path, got)
		}
		for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
			if w.Header().Get(h) == "" {
				t.Fatalf("%s: no %s", path, h)
			}
		}
	}
	if strings.Contains(CSP, "unsafe") || !strings.HasPrefix(CSP, "default-src 'self'") {
		t.Fatalf("CSP %q", CSP)
	}
	if w := e.do("GET", "/", "", "", "", "RemoteAddr", "100.64.0.5:1234"); w.Code != http.StatusForbidden {
		t.Fatalf("tunnel client: %d", w.Code)
	}
}

func TestStatic(t *testing.T) {
	e := newEnv(t)
	if w := e.do("GET", "/", "", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "nie został zbudowany") {
		t.Fatalf("placeholder: %d %s", w.Code, w.Body)
	}
	assets := fstest.MapFS{
		"index.html":       {Data: []byte(`<!doctype html><script type="module" src="/assets/app-1.js"></script>`)},
		"assets/app-1.js":  {Data: []byte(`console.log(1)`)},
		"assets/app-1.css": {Data: []byte(`body{}`)},
	}
	s := New(Options{Admin: e.adm, Assets: assets, Now: e.clk.now})
	tests := []struct {
		path, cache string
		code        int
	}{
		{"/", "no-cache", 200},
		{"/assets/app-1.js", "immutable", 200},
		{"/assets/nope.js", "", 404},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "https://"+testHost+tt.path, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tt.code || !strings.Contains(w.Header().Get("Cache-Control"), tt.cache) {
			t.Fatalf("%s: %d cache %q", tt.path, w.Code, w.Header().Get("Cache-Control"))
		}
	}
}

func TestCheckListen(t *testing.T) {
	tests := []struct {
		addr   string
		public bool
		ok     bool
	}{
		{"127.0.0.1:8443", false, true},
		{"[::1]:8443", false, true},
		{"localhost:8443", false, true},
		{"0.0.0.0:8443", false, false},
		{":8443", false, false},
		{"192.168.1.5:8443", false, false},
		{"0.0.0.0:8443", true, true},
		{"127.0.0.1", false, false},
		{"127.0.0.1:http", false, true},
		{"127.0.0.1:99999", false, false},
	}
	for _, tt := range tests {
		if err := CheckListen(tt.addr, tt.public); (err == nil) != tt.ok {
			t.Errorf("CheckListen(%q, %v) = %v", tt.addr, tt.public, err)
		}
	}
}

func TestLoadCert(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	c1, created, err := LoadCert("", "", dir, now)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	c2, created, err := LoadCert("", "", dir, now)
	if err != nil || created || Fingerprint(c1) != Fingerprint(c2) {
		t.Fatalf("reload: %v %v", created, err)
	}
	if fp := Fingerprint(c1); len(fp) != 32*3-1 || strings.Count(fp, ":") != 31 {
		t.Fatalf("fingerprint %q", fp)
	}
	c3, _, err := LoadCert(filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile), "", now)
	if err != nil || Fingerprint(c3) != Fingerprint(c1) {
		t.Fatalf("given pair: %v", err)
	}
	if _, _, err := LoadCert(filepath.Join(dir, "x.crt"), filepath.Join(dir, "x.key"), "", now); err == nil {
		t.Fatal("missing given pair accepted")
	}
}

// TestStream runs the SSE endpoint over real TLS: history first, a tick per
// sample, and a logout event once the user's password changes.
func TestStream(t *testing.T) {
	e := newEnv(t)
	ts := httptest.NewTLSServer(e.h)
	defer ts.Close()
	cookie, _ := e.login()
	e.srv.stream.sample(context.Background())
	req, _ := http.NewRequest("GET", ts.URL+"/api/stream", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	cl := ts.Client()
	cl.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	rd := bufio.NewReader(resp.Body)
	next := func() (string, string) {
		t.Helper()
		var ev, data string
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				ev = line[7:]
			case strings.HasPrefix(line, "data: "):
				data = line[6:]
			case line == "":
				return ev, data
			}
		}
	}
	if ev, data := next(); ev != "history" || !strings.Contains(data, `"online":1`) || !strings.Contains(data, `"h1":[1000,3000]`) {
		t.Fatalf("history %s %s", ev, data)
	}
	e.srv.stream.sample(context.Background())
	if ev, data := next(); ev != "tick" || !strings.Contains(data, `"total":2`) || !strings.Contains(data, `"sessions":[{"hub":"h1"`) {
		t.Fatalf("tick %s %s", ev, data)
	}
	e.adm.mu.Lock()
	e.adm.users["alice"].changed = e.clk.now().Add(time.Second)
	e.adm.mu.Unlock()
	e.srv.stream.sample(context.Background())
	if ev, _ := next(); ev != "logout" {
		t.Fatalf("after password change: %s", ev)
	}
	if _, err := io.ReadAll(rd); err != nil {
		t.Fatalf("stream did not end cleanly: %v", err)
	}
	if n := len(e.srv.stream.history); n != 3 {
		t.Fatalf("history %d", n)
	}
}
