package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"
)

// Audit limits: entries kept in memory, file size before rotation (one
// previous file, audit.jsonl.1, is kept).
const (
	AuditKeep    = 1000
	AuditMaxFile = 1 << 20
)

// AuditEntry is one admin action.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`  // "xs-ctl" or "web:<user>@<ip>"
	Action string    `json:"action"` // enroll, revoke, rename, forward_allow, login, …
	Target string    `json:"target,omitempty"`
	Detail string    `json:"detail,omitempty"`
	OK     bool      `json:"ok"`
	Error  string    `json:"error,omitempty"`
}

// Audit is the append-only audit log: newest AuditKeep entries in memory,
// all entries appended to a JSON-lines file (path "" = memory only).
type Audit struct {
	path string
	now  func() time.Time

	mu      sync.Mutex
	entries []AuditEntry
}

// OpenAudit loads the newest entries of path. A missing file is fine.
func OpenAudit(path string, now func() time.Time) (*Audit, error) {
	if now == nil {
		now = time.Now
	}
	a := &Audit{path: path, now: now}
	if path == "" {
		return a, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, fmt.Errorf("audit log: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		var e AuditEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			a.appendLocked(e)
		}
	}
	return a, nil
}

func (a *Audit) appendLocked(e AuditEntry) {
	a.entries = append(a.entries, e)
	if len(a.entries) > AuditKeep {
		a.entries = append(a.entries[:0:0], a.entries[len(a.entries)-AuditKeep:]...)
	}
}

// Record appends an entry for ctx's actor. err != nil marks it failed.
func (a *Audit) Record(ctx context.Context, action, target, detail string, err error) error {
	e := AuditEntry{Time: a.now().UTC(), Actor: Actor(ctx), Action: action, Target: target, Detail: detail, OK: err == nil}
	if err != nil {
		e.Error = err.Error()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.appendLocked(e)
	if a.path == "" {
		return nil
	}
	return a.writeLocked(e)
}

func (a *Audit) writeLocked(e AuditEntry) error {
	if st, err := os.Stat(a.path); err == nil && st.Size() > AuditMaxFile {
		if err := os.Rename(a.path, a.path+".1"); err != nil {
			return fmt.Errorf("audit rotate: %w", err)
		}
	}
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("audit log: %w", err)
	}
	b, _ := json.Marshal(e)
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("audit log: %w", werr)
	}
	return nil
}

// Last returns up to n newest entries, newest first.
func (a *Audit) Last(n int) []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n <= 0 || n > len(a.entries) {
		n = len(a.entries)
	}
	out := make([]AuditEntry, 0, n)
	for i := len(a.entries) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, a.entries[i])
	}
	return out
}

type actorKey struct{}

// WithActor returns ctx carrying who performs admin operations (audit log).
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// Actor returns ctx's actor, "unknown" if none was set.
func Actor(ctx context.Context) string {
	if a, ok := ctx.Value(actorKey{}).(string); ok && a != "" {
		return a
	}
	return "unknown"
}
