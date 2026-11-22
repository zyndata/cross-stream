package uplink

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// DefaultProbeURL answers 204 with no body on an open internet connection.
const DefaultProbeURL = "http://connectivitycheck.gstatic.com/generate_204"

// ProbeTimeout bounds one captive portal probe.
const ProbeTimeout = 5 * time.Second

// Captive portal probe outcomes (UplinkStatus.Captive).
const (
	NetOpen     = "open"        // 204: internet works
	NetCaptive  = "captive"     // redirect or a page instead of 204: a login page
	NetNoAccess = "no_internet" // no answer, an error status or a broken connection
)

// Probe is the result of one captive portal probe.
type Probe struct {
	State string `json:"state"`         // NetOpen, NetCaptive or NetNoAccess
	URL   string `json:"url,omitempty"` // the login page (NetCaptive)
	Err   string `json:"err,omitempty"` // why NetNoAccess
}

// ProbeCaptive asks probeURL once over sockets made by control (bound to
// the uplink), following no redirects: 204 (or 200 with an empty body) is
// NetOpen, a redirect or 200 with a body is NetCaptive (URL = the redirect
// target, or probeURL for a page served in place), anything else NetNoAccess.
// The host name is resolved by the system resolver.
func ProbeCaptive(ctx context.Context, probeURL string, control func(network, address string, c syscall.RawConn) error) Probe {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	d := &net.Dialer{Control: control, Timeout: ProbeTimeout}
	tr := &http.Transport{DialContext: d.DialContext, DisableKeepAlives: true, Proxy: nil,
		ResponseHeaderTimeout: ProbeTimeout}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return Probe{State: NetNoAccess, Err: err.Error()}
	}
	req.Header.Set("User-Agent", "CrossStream-hub")
	resp, err := cl.Do(req)
	if err != nil {
		return Probe{State: NetNoAccess, Err: probeErr(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNoContent:
		return Probe{State: NetOpen}
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		loc, err := resp.Location()
		if err != nil {
			return Probe{State: NetNoAccess, Err: fmt.Sprintf("redirect %d without a usable Location", resp.StatusCode)}
		}
		return Probe{State: NetCaptive, URL: loc.String()}
	case resp.StatusCode == http.StatusOK:
		n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
		if n == 0 {
			return Probe{State: NetOpen}
		}
		return Probe{State: NetCaptive, URL: probeURL}
	}
	return Probe{State: NetNoAccess, Err: "HTTP " + resp.Status}
}

// probeErr shortens a client error (no URL repeated).
func probeErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return err.Error()
}
