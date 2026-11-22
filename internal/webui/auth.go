package webui

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"
)

// Session and login limits (architecture.md, phase 15).
const (
	SessionTTL       = 12 * time.Hour
	SessionCookie    = "__Host-xs_session"
	CSRFHeader       = "X-CSRF-Token"
	LoginPerMinute   = 5                // attempts per client IP per minute
	LockoutFailures  = 5                // failures in a row before a lockout
	LockoutDuration  = 10 * time.Minute // lockout of that IP
	maxSessions      = 256
	maxLimiterIPs    = 4096
	loginRateWindow  = time.Minute
	sessionTokenSize = 32
)

// session is one logged-in browser.
type session struct {
	user    string
	csrf    string
	created time.Time
	expires time.Time
}

// sessions is the in-memory session table, keyed by the SHA-256 of the
// cookie token (a memory dump does not reveal usable cookies).
type sessions struct {
	now func() time.Time

	mu sync.Mutex
	m  map[[32]byte]*session
}

func newSessions(now func() time.Time) *sessions {
	return &sessions{now: now, m: map[[32]byte]*session{}}
}

func randToken() string {
	b := make([]byte, sessionTokenSize)
	_, _ = rand.Read(b) // Go 1.24+: never returns an error (crashes instead)
	return base64.RawURLEncoding.EncodeToString(b)
}

// create starts a session for user and returns its cookie token.
func (s *sessions) create(user string) (token string, sess session) {
	now := s.now()
	token = randToken()
	sess = session{user: user, csrf: randToken(), created: now, expires: now.Add(SessionTTL)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if len(s.m) >= maxSessions { // drop the oldest
		var oldest [32]byte
		var t time.Time
		for k, v := range s.m {
			if t.IsZero() || v.created.Before(t) {
				oldest, t = k, v.created
			}
		}
		delete(s.m, oldest)
	}
	s.m[sha256.Sum256([]byte(token))] = &sess
	return token, sess
}

// get returns the live session for token.
func (s *sessions) get(token string) (session, bool) {
	if token == "" {
		return session{}, false
	}
	k := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	if !ok {
		return session{}, false
	}
	if !s.now().Before(v.expires) {
		delete(s.m, k)
		return session{}, false
	}
	return *v, true
}

// drop ends the session for token.
func (s *sessions) drop(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, sha256.Sum256([]byte(token)))
}

func (s *sessions) pruneLocked(now time.Time) {
	for k, v := range s.m {
		if !now.Before(v.expires) {
			delete(s.m, k)
		}
	}
}

// csrfOK compares the request's CSRF token with the session's in constant time.
func csrfOK(sess session, got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(sess.csrf), []byte(got)) == 1
}

// loginLimiter enforces LoginPerMinute and the lockout per client IP.
type loginLimiter struct {
	now func() time.Time

	mu sync.Mutex
	m  map[string]*ipState
}

type ipState struct {
	attempts    []time.Time // within the last minute
	failures    int         // in a row
	lockedUntil time.Time
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	return &loginLimiter{now: now, m: map[string]*ipState{}}
}

// allow records an attempt from ip; it returns 0 when the attempt may go
// on, else how long the client must wait.
func (l *loginLimiter) allow(ip string) time.Duration {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.m[ip]
	if st == nil {
		if len(l.m) >= maxLimiterIPs {
			l.pruneLocked(now)
		}
		if len(l.m) >= maxLimiterIPs {
			// Table full of active IPs: refuse rather than forget lockouts.
			return loginRateWindow
		}
		st = &ipState{}
		l.m[ip] = st
	}
	if now.Before(st.lockedUntil) {
		return st.lockedUntil.Sub(now)
	}
	kept := st.attempts[:0]
	for _, t := range st.attempts {
		if now.Sub(t) < loginRateWindow {
			kept = append(kept, t)
		}
	}
	st.attempts = kept
	if len(st.attempts) >= LoginPerMinute {
		return loginRateWindow - now.Sub(st.attempts[0])
	}
	st.attempts = append(st.attempts, now)
	return 0
}

// result records the outcome of an allowed attempt; locked is true when
// this failure started a lockout.
func (l *loginLimiter) result(ip string, ok bool) (locked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.m[ip]
	if st == nil {
		return false
	}
	if ok {
		st.failures = 0
		return false
	}
	st.failures++
	if st.failures >= LockoutFailures {
		st.failures = 0
		st.lockedUntil = l.now().Add(LockoutDuration)
		return true
	}
	return false
}

func (l *loginLimiter) pruneLocked(now time.Time) {
	for ip, st := range l.m {
		if now.Before(st.lockedUntil) {
			continue
		}
		// A run of failures is forgotten after LockoutDuration without an
		// attempt; otherwise failed logins would fill the table for good.
		idleFor := loginRateWindow
		if st.failures > 0 {
			idleFor = LockoutDuration
		}
		if len(st.attempts) == 0 || now.Sub(st.attempts[len(st.attempts)-1]) >= idleFor {
			delete(l.m, ip)
		}
	}
}
