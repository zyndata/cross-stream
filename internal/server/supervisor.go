package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// SupervisorURL is the Home Assistant Supervisor API as seen from an app.
const SupervisorURL = "http://supervisor"

// SupervisorPort keeps the tunnel port in the Home Assistant app's own
// Network section: the host ports mapped to the container's main tunnel port
// (UDP and TCP). The app may read and change its own mapping and restart
// itself with its token, without extra permissions (/addons/self/...).
type SupervisorPort struct {
	URL    string // SupervisorURL; tests use an httptest server
	Token  string // SUPERVISOR_TOKEN
	Main   int    // container port, the first listen port (51900)
	Client *http.Client
}

// Name implements PortSource.
func (SupervisorPort) Name() string { return "ha" }

// Writable implements PortSource.
func (SupervisorPort) Writable() bool { return true }

// Configured implements PortSource: the host port mapped to Main/udp.
func (s SupervisorPort) Configured(ctx context.Context) (int, error) {
	mapping, err := s.network(ctx)
	if err != nil {
		return 0, err
	}
	p := mapping[s.key("udp")]
	if p == nil {
		return 0, fmt.Errorf("home assistant: port %s is not mapped in the app's Network section", s.key("udp"))
	}
	return *p, nil
}

// Set implements PortSource: maps Main/udp and Main/tcp to port and keeps
// the other mappings as they are.
func (s SupervisorPort) Set(ctx context.Context, port int) error {
	mapping, err := s.network(ctx)
	if err != nil {
		return err
	}
	mapping[s.key("udp")], mapping[s.key("tcp")] = &port, &port
	_, err = s.call(ctx, http.MethodPost, "/addons/self/options", map[string]any{"network": mapping})
	return err
}

// Restart asks the Supervisor to restart this app.
func (s SupervisorPort) Restart(ctx context.Context) error {
	_, err := s.call(ctx, http.MethodPost, "/addons/self/restart", nil)
	return err
}

func (s SupervisorPort) key(proto string) string { return strconv.Itoa(s.Main) + "/" + proto }

func (s SupervisorPort) network(ctx context.Context) (map[string]*int, error) {
	data, err := s.call(ctx, http.MethodGet, "/addons/self/info", nil)
	if err != nil {
		return nil, err
	}
	var info struct {
		Network map[string]*int `json:"network"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("home assistant: app info: %w", err)
	}
	if info.Network == nil {
		info.Network = map[string]*int{}
	}
	return info.Network, nil
}

// call sends one Supervisor request and returns its "data" member.
func (s SupervisorPort) call(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	if s.Token == "" {
		return nil, errors.New("home assistant: no SUPERVISOR_TOKEN")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("home assistant: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, s.URL+path, rd)
	if err != nil {
		return nil, fmt.Errorf("home assistant: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := s.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("home assistant %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Result  string          `json:"result"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("home assistant %s %s: %w", method, path, err)
	}
	if err := json.Unmarshal(b, &out); err != nil || resp.StatusCode != http.StatusOK || out.Result != "ok" {
		msg := out.Message
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("home assistant %s %s: %s", method, path, msg)
	}
	return out.Data, nil
}
