package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

// MaxMessage bounds one request or response line.
const MaxMessage = 1 << 20

// JSON-RPC 2.0 error codes.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeServer         = -32000 // handler returned an error
)

// ErrMethodNotFound is returned by a Handler for unknown methods.
var ErrMethodNotFound = errors.New("ipc: method not found")

// Request is one JSON-RPC 2.0 request (one line).
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      int64           `json:"id"`
}

// Response is one JSON-RPC 2.0 response (one line).
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
	ID      int64           `json:"id"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("ipc error %d: %s", e.Code, e.Message) }

// Handler answers one method call. The result is marshalled to JSON; a
// Stream result turns the connection into a notification stream.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Stream is a Handler result for subscriptions: after the reply
// {"subscribed": true} the server runs it on the connection; it sends
// notifications with notify until ctx ends (client gone) or it returns.
type Stream func(ctx context.Context, notify func(method string, params any) error) error

// Notification is a JSON-RPC 2.0 notification line (no id).
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Subscribed is the result of a request that started a Stream.
type Subscribed struct {
	Subscribed bool `json:"subscribed"`
}

// Listen creates the Unix socket at path (removing a stale one) with mode
// (on Windows: owner-only ACL; mode is ignored).
func Listen(path string, mode fs.FileMode) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ipc listen: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("ipc listen: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("ipc listen: %w", err)
	}
	if err := restrict(path, mode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("ipc listen: %w", err)
	}
	return ln, nil
}

// Serve answers requests on ln until ln is closed. Each connection may send
// several requests, one JSON object per line, answered in order.
func Serve(ctx context.Context, ln net.Listener, h Handler, log *slog.Logger) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			time.Sleep(50 * time.Millisecond) // transient: the socket must outlive it
			continue
		}
		go serveConn(ctx, c, h, log)
	}
}

func serveConn(ctx context.Context, c net.Conn, h Handler, log *slog.Logger) {
	defer func() { _ = c.Close() }()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 4096), MaxMessage)
	enc := json.NewEncoder(c)
	for sc.Scan() {
		var req Request
		var stream Stream
		resp := Response{JSONRPC: "2.0"}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			resp.Error = &Error{Code: CodeParse, Message: err.Error()}
		} else if resp.ID = req.ID; req.JSONRPC != "2.0" || req.Method == "" {
			resp.Error = &Error{Code: CodeInvalidRequest, Message: "need jsonrpc 2.0 and a method"}
		} else {
			resp.Result, stream, resp.Error = call(ctx, h, req)
		}
		if err := enc.Encode(resp); err != nil {
			log.Debug("ipc write", "err", err)
			return
		}
		if stream != nil {
			runStream(ctx, c, sc, enc, stream, log)
			return
		}
	}
}

// runStream runs a subscription until the client closes the connection (any
// further request lines are ignored), ctx ends or the stream returns.
func runStream(ctx context.Context, c net.Conn, sc *bufio.Scanner, enc *json.Encoder, s Stream, log *slog.Logger) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for sc.Scan() {
		}
		cancel()
	}()
	notify := func(method string, params any) error {
		n := Notification{JSONRPC: "2.0", Method: method}
		if params != nil {
			b, err := json.Marshal(params)
			if err != nil {
				return fmt.Errorf("ipc notify %s: %w", method, err)
			}
			n.Params = b
		}
		_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return enc.Encode(n)
	}
	if err := s(ctx, notify); err != nil && ctx.Err() == nil {
		log.Debug("ipc stream", "err", err)
	}
}

func call(ctx context.Context, h Handler, req Request) (json.RawMessage, Stream, *Error) {
	res, err := h(ctx, req.Method, req.Params)
	if errors.Is(err, ErrMethodNotFound) {
		return nil, nil, &Error{Code: CodeMethodNotFound, Message: "method not found: " + req.Method}
	}
	if err != nil {
		return nil, nil, &Error{Code: CodeServer, Message: err.Error()}
	}
	stream, _ := res.(Stream)
	if stream != nil {
		res = Subscribed{Subscribed: true}
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, nil, &Error{Code: CodeServer, Message: err.Error()}
	}
	return b, stream, nil
}

// Call sends one request to the socket at path and decodes the result into
// result (may be nil). A JSON-RPC error is returned as *Error.
func Call(ctx context.Context, path, method string, params, result any) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("ipc connect %s (is xs-hubd running?): %w", path, err)
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	} else {
		_ = c.SetDeadline(time.Now().Add(time.Minute))
	}
	req := Request{JSONRPC: "2.0", Method: method, ID: 1}
	if params != nil {
		if req.Params, err = json.Marshal(params); err != nil {
			return fmt.Errorf("ipc %s: %w", method, err)
		}
	}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return fmt.Errorf("ipc %s: %w", method, err)
	}
	var resp Response
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&resp); err != nil {
		return fmt.Errorf("ipc %s: %w", method, err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("ipc %s result: %w", method, err)
		}
	}
	return nil
}

// Subscribe sends a subscription request and calls fn for every
// notification until ctx ends, the server closes the stream or fn fails.
func Subscribe(ctx context.Context, path, method string, params any, fn func(method string, params json.RawMessage) error) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("ipc connect %s (is xs-hubd running?): %w", path, err)
	}
	defer func() { _ = c.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	req := Request{JSONRPC: "2.0", Method: method, ID: 1}
	if params != nil {
		if req.Params, err = json.Marshal(params); err != nil {
			return fmt.Errorf("ipc %s: %w", method, err)
		}
	}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return fmt.Errorf("ipc %s: %w", method, err)
	}
	dec := json.NewDecoder(bufio.NewReader(c))
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		return fmt.Errorf("ipc %s: %w", method, err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	for {
		var n Notification
		if err := dec.Decode(&n); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("ipc %s stream: %w", method, err)
		}
		if err := fn(n.Method, n.Params); err != nil {
			return err
		}
	}
}
