package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zyndata/cross-stream/internal/bypass"
	"github.com/zyndata/cross-stream/internal/hub/hublog"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
	"github.com/zyndata/cross-stream/internal/ipc"
)

// IPC methods added in phase 11.
const (
	MethodBypassGet = "bypass.get"
	MethodBypassSet = "bypass.set"
	MethodFwdList   = "forwards.list"
	MethodFwdSet    = "forwards.set"
	MethodUsageGet  = "usage.get"
)

// ForwardsParams are the params of forwards.set.
type ForwardsParams struct {
	Forwards []ForwardRule `json:"forwards"`
}

// handleRPCExt serves the phase 11 methods (handleRPC falls through here).
func (d *Daemon) handleRPCExt(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case MethodBypassGet:
		return d.BypassGet(), nil
	case MethodBypassSet:
		var r bypass.Rules
		if err := json.Unmarshal(params, &r); err != nil {
			return nil, fmt.Errorf("bypass.set: need {\"domains\": [...], \"cidrs\": [...]}")
		}
		return d.BypassSet(r)
	case MethodUsageGet:
		var p UsageParams
		if len(params) > 0 && json.Unmarshal(params, &p) != nil {
			return nil, fmt.Errorf("usage.get: params {relay, from, to}")
		}
		return d.Usage(p)
	case MethodFwdList:
		return d.ForwardsList(), nil
	case MethodFwdSet:
		var p ForwardsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("forwards.set: need {\"forwards\": [{proto, port, target, name}]}")
		}
		return d.ForwardsSet(p.Forwards)
	}
	return d.handleRPCUplinks(ctx, method, params)
}

// MethodLogsExport returns the recent daemon log, redacted (phase 14).
const MethodLogsExport = "logs.export"

// LogsExport is the result of logs.export.
type LogsExport struct {
	Version string   `json:"version"`
	Lines   []string `json:"lines"` // oldest first, redacted (hublog.Redact)
}

// LogsExport returns the kept log lines with secrets and public addresses
// removed.
func (d *Daemon) LogsExport() LogsExport {
	out := LogsExport{Version: d.cfg.Version, Lines: []string{}}
	if d.cfg.Logs != nil {
		out.Lines = d.cfg.Logs.Redacted()
	}
	return out
}

// MethodLogsLevel shows or changes the daemon's log level (phase 19).
const MethodLogsLevel = "logs.level"

// LogLevelParams are the params of logs.level; an empty Level only reads.
type LogLevelParams struct {
	Level   string `json:"level,omitempty"`    // debug or info
	ForSecs int    `json:"for_secs,omitempty"` // > 0: back to the start level after this
}

// LogLevel shows or changes the log level (xs-hubctl loglevel, GUI switch).
func (d *Daemon) LogLevel(p LogLevelParams) (hublog.LevelState, error) {
	if d.cfg.LogLevel == nil {
		return hublog.LevelState{}, errors.New("logs.level: this daemon has a fixed log level")
	}
	if p.Level == "" {
		return d.cfg.LogLevel.State(), nil
	}
	st, err := d.cfg.LogLevel.Set(p.Level, time.Duration(p.ForSecs)*time.Second)
	if err != nil {
		return st, err
	}
	d.log.Info("log level changed", "level", st.Level, "until", st.Until)
	return st, nil
}

// IPC methods added in phase 12.
const (
	MethodUplinksList   = "uplinks.list"
	MethodUplinksUpdate = "uplinks.update"
	MethodUplinksForget = "uplinks.forget"
)

// UplinkForgetParams are the params of uplinks.forget.
type UplinkForgetParams struct {
	Uplink string `json:"uplink"` // id or name
}

// handleRPCUplinks serves the phase 12 methods.
func (d *Daemon) handleRPCUplinks(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case MethodUplinksCaptive:
		var p CaptiveParams
		if err := json.Unmarshal(params, &p); err != nil || p.Uplink == "" {
			return nil, fmt.Errorf("uplinks.captive: need {\"uplink\": id or name, \"open\": bool}")
		}
		return d.UplinksCaptive(ctx, p)
	case MethodUplinksList:
		return d.UplinksList(), nil
	case MethodUplinksUpdate:
		var p uplink.Params
		if err := json.Unmarshal(params, &p); err != nil || p.Uplink == "" {
			return nil, fmt.Errorf("uplinks.update: need {\"uplink\": id or name, ...}")
		}
		return d.UplinksUpdate(p)
	case MethodLogsExport:
		return d.LogsExport(), nil
	case MethodLogsLevel:
		var p LogLevelParams
		if len(params) > 0 && json.Unmarshal(params, &p) != nil {
			return nil, fmt.Errorf("logs.level: params {\"level\": debug|info, \"for_secs\": N}")
		}
		return d.LogLevel(p)
	case MethodUplinksForget:
		var p UplinkForgetParams
		if err := json.Unmarshal(params, &p); err != nil || p.Uplink == "" {
			return nil, fmt.Errorf("uplinks.forget: need {\"uplink\": id or name}")
		}
		return nil, d.UplinksForget(p.Uplink)
	}
	return nil, ipc.ErrMethodNotFound
}
