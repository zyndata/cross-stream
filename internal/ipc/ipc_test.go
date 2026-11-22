package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// streamEnded is closed when the "ticks" stream returns (client gone).
var streamEnded = make(chan struct{}, 1)

func serve(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ipc") // short path: Unix socket paths are limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	if err := os.WriteFile(path, nil, 0o600); err != nil { // stale file is replaced
		t.Fatal(err)
	}
	ln, err := Listen(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	h := func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "echo":
			var p map[string]string
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			return p, nil
		case "fail":
			return nil, errors.New("boom")
		case "none":
			return nil, nil
		case "ticks":
			return Stream(func(ctx context.Context, notify func(string, any) error) error {
				defer func() { streamEnded <- struct{}{} }()
				for i := 0; ; i++ {
					if err := notify("tick", map[string]int{"n": i}); err != nil {
						return err
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(10 * time.Millisecond):
					}
				}
			}), nil
		}
		return nil, ErrMethodNotFound
	}
	go Serve(context.Background(), ln, h, slog.New(slog.DiscardHandler))
	return path
}

func TestCall(t *testing.T) {
	path := serve(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got map[string]string
	if err := Call(ctx, path, "echo", map[string]string{"a": "b"}, &got); err != nil || got["a"] != "b" {
		t.Fatalf("echo %v %v", got, err)
	}
	if err := Call(ctx, path, "none", nil, nil); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		method string
		code   int
		msg    string
	}{
		{"fail", CodeServer, "boom"},
		{"nope", CodeMethodNotFound, "nope"},
	}
	for _, tt := range tests {
		var e *Error
		err := Call(ctx, path, tt.method, nil, nil)
		if !errors.As(err, &e) || e.Code != tt.code || !strings.Contains(e.Message, tt.msg) {
			t.Fatalf("%s: %v", tt.method, err)
		}
	}
	if err := Call(ctx, filepath.Join(t.TempDir(), "x"), "echo", nil, nil); err == nil {
		t.Fatal("call to a missing socket succeeded")
	}
}

func TestBadRequests(t *testing.T) {
	path := serve(t)
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	rd := bufio.NewReader(c)
	tests := []struct {
		line string
		code int
	}{
		{"{", CodeParse},
		{`{"jsonrpc":"1.0","method":"echo","id":7}`, CodeInvalidRequest},
		{`{"jsonrpc":"2.0","id":8}`, CodeInvalidRequest},
	}
	for _, tt := range tests {
		if _, err := c.Write([]byte(tt.line + "\n")); err != nil {
			t.Fatal(err)
		}
		var resp Response
		line, err := rd.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(line, &resp); err != nil || resp.Error == nil || resp.Error.Code != tt.code {
			t.Fatalf("%s: %s", tt.line, line)
		}
	}
}

func TestSubscribe(t *testing.T) {
	path := serve(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []int
	errStop := errors.New("enough")
	err := Subscribe(ctx, path, "ticks", nil, func(method string, params json.RawMessage) error {
		var p map[string]int
		if method != "tick" || json.Unmarshal(params, &p) != nil {
			t.Fatalf("notification %s %s", method, params)
		}
		got = append(got, p["n"])
		if len(got) == 3 {
			return errStop
		}
		return nil
	})
	if !errors.Is(err, errStop) || len(got) != 3 || got[2] != 2 {
		t.Fatalf("subscribe %v %v", got, err)
	}
	select { // closing the client ends the server-side stream
	case <-streamEnded:
	case <-ctx.Done():
		t.Fatal("stream kept running after the client left")
	}
	var e *Error
	if err := Subscribe(ctx, path, "nope", nil, nil); !errors.As(err, &e) || e.Code != CodeMethodNotFound {
		t.Fatalf("unknown subscription: %v", err)
	}
	// A cancelled context ends Subscribe.
	sctx, scancel := context.WithCancel(ctx)
	go func() { time.Sleep(50 * time.Millisecond); scancel() }()
	if err := Subscribe(sctx, path, "ticks", nil, func(string, json.RawMessage) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	<-streamEnded
}

func TestChgrp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Chgrp(path, "no-such-group-crossstream-test"); err == nil {
		t.Fatal("missing group accepted")
	}
}
