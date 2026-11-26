package server

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
	"github.com/zyndata/cross-stream/internal/server/admin"
)

// Rename changes an enrolled hub's name; a pending token for the old name
// is dropped. The key and tunnel IP stay.
func (s *State) Rename(name, newName string) error {
	if err := validHubName(newName); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hub, ok := s.hubs[name]
	if !ok {
		return fmt.Errorf("rename %q: %w", name, ErrHubNotFound)
	}
	if _, taken := s.hubs[newName]; taken || newName == name {
		return fmt.Errorf("rename to %q: %w", newName, ErrHubExists)
	}
	for _, t := range s.tokens {
		if t.Name == newName {
			return fmt.Errorf("rename to %q: an enrollment code for this name is pending", newName)
		}
	}
	return s.update(func() func() {
		h := hub
		h.Name = newName
		delete(s.hubs, name)
		s.hubs[newName] = h
		return func() { delete(s.hubs, newName); s.hubs[name] = hub }
	})
}

// EnrollKnown applies an enrollment code sent by a key that is already
// enrolled (the hub was enrolled again with a new code without being
// removed first): the code is consumed and the hub takes its name and the
// current tunnel port; key and tunnel IP stay. It returns the record as it
// is afterwards. An unknown or used code (enroll.ErrTokenInvalid), an expired
// one or a name held by another hub (ErrHubExists) leave the record as it
// was; the caller keeps serving the hub under its old name.
func (s *State) EnrollKnown(tok enroll.Token, key [crypto.KeyLen]byte) (HubRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hub, ok := s.byKeyLocked(key)
	if !ok {
		return HubRecord{}, ErrHubNotFound
	}
	h := tok.Hash()
	rec, ok := s.tokens[h]
	if !ok {
		return hub, enroll.ErrTokenInvalid
	}
	if !s.now().Before(rec.Expires) {
		delete(s.tokens, h)
		_ = s.save()
		return hub, enroll.ErrTokenExpired
	}
	if other, taken := s.hubs[rec.Name]; taken && other.PublicKey != hub.PublicKey {
		return hub, fmt.Errorf("enroll again as %q: %w", rec.Name, ErrHubExists)
	}
	nh := hub
	nh.Name, nh.Port = rec.Name, s.port
	err := s.update(func() func() {
		delete(s.tokens, h)
		delete(s.hubs, hub.Name)
		s.hubs[nh.Name] = nh
		return func() { s.tokens[h] = rec; delete(s.hubs, nh.Name); s.hubs[hub.Name] = hub }
	})
	if err != nil {
		return hub, err
	}
	return nh, nil
}

// validHubName applies the enrollment code's name rule (enroll.MaxNameLen,
// printable characters only, no leading "-").
func validHubName(name string) error {
	if name == "" || len(name) > enroll.MaxNameLen {
		return fmt.Errorf("hub name: 1-%d bytes", enroll.MaxNameLen)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("hub name: must not start with '-'")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("hub name: printable characters only")
		}
	}
	return nil
}

// Users implements admin.UserStore.
func (s *State) Users() []admin.UserRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usersLocked()
}

func (s *State) usersLocked() []admin.UserRecord {
	out := make([]admin.UserRecord, 0, len(s.admins))
	for _, u := range s.admins {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// User implements admin.UserStore.
func (s *State) User(name string) (admin.UserRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.admins[name]
	return u, ok
}

// PutUser implements admin.UserStore.
func (s *State) PutUser(u admin.UserRecord, create bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.admins[u.Name]
	switch {
	case create && ok:
		return fmt.Errorf("%q: %w", u.Name, admin.ErrUserExists)
	case !create && !ok:
		return fmt.Errorf("%q: %w", u.Name, admin.ErrUserNotFound)
	}
	return s.update(func() func() {
		s.admins[u.Name] = u
		return func() {
			if ok {
				s.admins[u.Name] = old
			} else {
				delete(s.admins, u.Name)
			}
		}
	})
}

// DelUser implements admin.UserStore.
func (s *State) DelUser(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.admins[name]
	if !ok {
		return fmt.Errorf("%q: %w", name, admin.ErrUserNotFound)
	}
	return s.update(func() func() {
		delete(s.admins, name)
		return func() { s.admins[name] = old }
	})
}
