package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
	"github.com/zyndata/cross-stream/internal/server/admin"
)

// Tunnel addressing: the server is 100.64.0.1, hubs get 100.64.0.2 upwards.
var (
	TunnelPrefix = netip.MustParsePrefix("100.64.0.0/16")
	ServerIP     = netip.MustParseAddr("100.64.0.1")
)

// State errors.
var (
	ErrHubExists   = errors.New("hub already enrolled")
	ErrHubNotFound = errors.New("hub not found")
	ErrKeyEnrolled = errors.New("hub key already enrolled")
	ErrPoolFull    = errors.New("tunnel address pool exhausted")
)

// HubRecord is one enrolled hub.
type HubRecord struct {
	Name      string    `json:"name"`
	PublicKey string    `json:"public_key"` // base64 X25519
	TunnelIP  string    `json:"tunnel_ip"`
	Enrolled  time.Time `json:"enrolled"`
	FwdAllow  int       `json:"fwd_allow,omitempty"` // public ports the hub may forward
	Port      int       `json:"port,omitempty"`      // tunnel port in its enrollment code
}

type tokenJSON struct {
	Hash    string    `json:"hash"` // hex SHA-256 of the token
	Name    string    `json:"name"`
	Expires time.Time `json:"expires"`
}

type stateJSON struct {
	V      int                `json:"v"`
	Hubs   []HubRecord        `json:"hubs"`
	Tokens []tokenJSON        `json:"tokens"`
	Admins []admin.UserRecord `json:"admins,omitempty"` // web panel users (phase 15)
}

// State is the persistent server state (state.json): enrolled hubs and
// hashed enrollment tokens, web panel admin users. Every change is written atomically before it
// takes effect. Safe for concurrent use.
type State struct {
	path string
	now  func() time.Time

	mu     sync.Mutex
	hubs   map[string]HubRecord
	tokens map[enroll.TokenHash]enroll.TokenRecord
	admins map[string]admin.UserRecord
	port   int // tunnel port new hubs enroll with (SetTunnelPort)
}

// OpenState loads path, or starts empty if it does not exist.
func OpenState(path string, now func() time.Time) (*State, error) {
	if now == nil {
		now = time.Now
	}
	s := &State{path: path, now: now, hubs: map[string]HubRecord{}, tokens: map[enroll.TokenHash]enroll.TokenRecord{},
		admins: map[string]admin.UserRecord{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var sj stateJSON
	if err := json.Unmarshal(b, &sj); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	for _, h := range sj.Hubs {
		s.hubs[h.Name] = h
	}
	for _, u := range sj.Admins {
		s.admins[u.Name] = u
	}
	for _, t := range sj.Tokens {
		raw, err := hex.DecodeString(t.Hash)
		if err != nil || len(raw) != len(enroll.TokenHash{}) {
			return nil, fmt.Errorf("parse state %s: bad token hash", path)
		}
		h := enroll.TokenHash(raw)
		s.tokens[h] = enroll.TokenRecord{Hash: h, Name: t.Name, Expires: t.Expires}
	}
	return s, nil
}

// save writes the state atomically (temp file + rename). Caller holds mu.
func (s *State) save() error {
	sj := stateJSON{V: 1, Hubs: s.hubsLocked(), Tokens: []tokenJSON{}}
	for _, t := range s.tokens {
		sj.Tokens = append(sj.Tokens, tokenJSON{Hash: hex.EncodeToString(t.Hash[:]), Name: t.Name, Expires: t.Expires})
	}
	sort.Slice(sj.Tokens, func(i, j int) bool { return sj.Tokens[i].Hash < sj.Tokens[j].Hash })
	sj.Admins = s.usersLocked()
	b, err := json.MarshalIndent(sj, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	return writeFileAtomic(s.path, append(b, '\n'))
}

func writeFileAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	// os.CreateTemp creates the file with mode 0600.
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// update applies change and saves; on a failed save the change is undone.
func (s *State) update(change func() (undo func())) error {
	undo := change()
	if err := s.save(); err != nil {
		undo()
		return err
	}
	return nil
}

// AddToken stores tok for a new hub name. A pending token for the same name is
// replaced; expired tokens are pruned.
func (s *State) AddToken(name string, tok enroll.Token, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("add token: ttl must be positive")
	}
	if err := validHubName(name); err != nil {
		return fmt.Errorf("add token: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hubs[name]; ok {
		return fmt.Errorf("enroll %q: %w (revoke it first)", name, ErrHubExists)
	}
	old := s.cloneTokens()
	return s.update(func() func() {
		now := s.now()
		for h, t := range s.tokens {
			if t.Name == name || !now.Before(t.Expires) {
				delete(s.tokens, h)
			}
		}
		h := tok.Hash()
		s.tokens[h] = enroll.TokenRecord{Hash: h, Name: name, Expires: now.Add(ttl)}
		return func() { s.tokens = old }
	})
}

// Enroll consumes tok and registers key under the token's hub name with the
// lowest free tunnel address. Errors wrap enroll.ErrTokenInvalid or
// enroll.ErrTokenExpired for bad tokens.
func (s *State) Enroll(tok enroll.Token, key [crypto.KeyLen]byte) (HubRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := tok.Hash()
	rec, ok := s.tokens[h]
	if !ok {
		return HubRecord{}, enroll.ErrTokenInvalid
	}
	if !s.now().Before(rec.Expires) {
		delete(s.tokens, h)
		_ = s.save()
		return HubRecord{}, enroll.ErrTokenExpired
	}
	if _, ok := s.byKeyLocked(key); ok {
		return HubRecord{}, ErrKeyEnrolled
	}
	if _, ok := s.hubs[rec.Name]; ok {
		return HubRecord{}, ErrHubExists
	}
	ip, err := s.freeIPLocked()
	if err != nil {
		return HubRecord{}, err
	}
	hub := HubRecord{Name: rec.Name, PublicKey: crypto.EncodeKey(key), TunnelIP: ip.String(), Enrolled: s.now().UTC(),
		Port: s.port}
	err = s.update(func() func() {
		delete(s.tokens, h)
		s.hubs[hub.Name] = hub
		return func() { s.tokens[h] = rec; delete(s.hubs, hub.Name) }
	})
	if err != nil {
		return HubRecord{}, err
	}
	return hub, nil
}

// Revoke deletes an enrolled hub and any pending token for its name.
func (s *State) Revoke(name string) (HubRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hub, ok := s.hubs[name]
	pending := false
	for _, t := range s.tokens {
		pending = pending || t.Name == name
	}
	if !ok && !pending {
		return HubRecord{}, fmt.Errorf("revoke %q: %w", name, ErrHubNotFound)
	}
	old := s.cloneTokens()
	err := s.update(func() func() {
		delete(s.hubs, name)
		for h, t := range s.tokens {
			if t.Name == name {
				delete(s.tokens, h)
			}
		}
		return func() {
			s.tokens = old
			if ok {
				s.hubs[name] = hub
			}
		}
	})
	return hub, err
}

// SetForwardAllow sets how many public ports a hub may forward.
func (s *State) SetForwardAllow(name string, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hub, ok := s.hubs[name]
	if !ok {
		return fmt.Errorf("forward allow %q: %w", name, ErrHubNotFound)
	}
	return s.update(func() func() {
		h := hub
		h.FwdAllow = n
		s.hubs[name] = h
		return func() { s.hubs[name] = hub }
	})
}

// ForwardAllow returns a hub's port forward allowance (0 = none).
func (s *State) ForwardAllow(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hubs[name].FwdAllow
}

// HubByKey returns the hub enrolled with key.
func (s *State) HubByKey(key [crypto.KeyLen]byte) (HubRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byKeyLocked(key)
}

// Hubs returns all enrolled hubs sorted by name.
func (s *State) Hubs() []HubRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hubsLocked()
}

// SetTunnelPort sets the tunnel port new hubs enroll with. Hubs enrolled
// before records had a port get this one (they were enrolled with the port
// the server used until now). It returns the hubs enrolled with another port:
// they must enroll again.
func (s *State) SetTunnelPort(port int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var unset []string
	for name, h := range s.hubs {
		if h.Port == 0 {
			unset = append(unset, name)
		}
	}
	if len(unset) > 0 {
		err := s.update(func() func() {
			for _, name := range unset {
				h := s.hubs[name]
				h.Port = port
				s.hubs[name] = h
			}
			return func() {
				for _, name := range unset {
					h := s.hubs[name]
					h.Port = 0
					s.hubs[name] = h
				}
			}
		})
		if err != nil {
			return nil, err
		}
	}
	s.port = port
	return s.reenrollLocked(port), nil
}

// Reenroll lists the hubs (sorted) enrolled with a tunnel port other than port.
func (s *State) Reenroll(port int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reenrollLocked(port)
}

func (s *State) reenrollLocked(port int) []string {
	out := []string{}
	for _, h := range s.hubsLocked() {
		if h.Port != 0 && h.Port != port {
			out = append(out, h.Name)
		}
	}
	return out
}

func (s *State) hubsLocked() []HubRecord {
	out := make([]HubRecord, 0, len(s.hubs))
	for _, h := range s.hubs {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *State) byKeyLocked(key [crypto.KeyLen]byte) (HubRecord, bool) {
	enc := crypto.EncodeKey(key)
	for _, h := range s.hubs {
		if h.PublicKey == enc {
			return h, true
		}
	}
	return HubRecord{}, false
}

func (s *State) freeIPLocked() (netip.Addr, error) {
	used := make(map[netip.Addr]bool, len(s.hubs))
	for _, h := range s.hubs {
		if ip, err := netip.ParseAddr(h.TunnelIP); err == nil {
			used[ip] = true
		}
	}
	for ip := ServerIP.Next(); TunnelPrefix.Contains(ip); ip = ip.Next() {
		if b := ip.As4(); b[2] == 255 && b[3] == 255 { // broadcast of the /16
			break
		}
		if !used[ip] {
			return ip, nil
		}
	}
	return netip.Addr{}, ErrPoolFull
}

func (s *State) cloneTokens() map[enroll.TokenHash]enroll.TokenRecord {
	out := make(map[enroll.TokenHash]enroll.TokenRecord, len(s.tokens))
	for k, v := range s.tokens {
		out[k] = v
	}
	return out
}
