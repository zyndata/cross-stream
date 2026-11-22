package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// methodLogsExport is xs-hubd's redacted log (internal/hub/rpc_ext.go, phase 14).
const methodLogsExport = "logs.export"

type logsExport struct {
	Version string   `json:"version"`
	Lines   []string `json:"lines"`
}

// ExportLogs asks where to save the hub's recent log (already redacted by
// xs-hubd) and writes it there. It returns the path, or "" when cancelled.
func (a *App) ExportLogs() (string, error) {
	var res logsExport
	if err := a.call(callTimeout, methodLogsExport, nil, &res); err != nil {
		return "", err
	}
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	path, err := runtime.SaveFileDialog(ctx, runtime.SaveDialogOptions{
		Title:           "Zapisz logi huba",
		DefaultFilename: "crossstream-hub-log-" + time.Now().Format("20060102-1504") + ".txt",
		Filters:         []runtime.FileFilter{{DisplayName: "Tekst (*.txt)", Pattern: "*.txt"}},
	})
	if err != nil || path == "" {
		return "", err
	}
	if err := os.WriteFile(path, []byte(logText(res)), 0o600); err != nil {
		return "", fmt.Errorf("zapis: %w", err)
	}
	return path, nil
}

// logText is the exported file: a header line, then the log lines.
func logText(r logsExport) string {
	return fmt.Sprintf("# xs-hubd %s log, redacted (codes, secrets, keys, MACs, public IPs)\n%s\n",
		r.Version, strings.Join(r.Lines, "\n"))
}

// methodLogsLevel is xs-hubd's run-time log level (internal/hub/rpc_ext.go, phase 19).
const methodLogsLevel = "logs.level"

// verboseFor is how long the GUI switch "Szczegółowe logi" keeps debug on.
const verboseFor = 15 * time.Minute

// logLevel is hublog.LevelState.
type logLevel struct {
	Level string    `json:"level"`
	Start string    `json:"start"`
	Until time.Time `json:"until,omitzero"`
}

type logLevelParams struct {
	Level   string `json:"level"`
	ForSecs int    `json:"for_secs,omitempty"`
}

// SetVerboseLogs turns debug logging on for 15 minutes, or back to info.
func (a *App) SetVerboseLogs(on bool) (logLevel, error) {
	p := logLevelParams{Level: "info"}
	if on {
		p = logLevelParams{Level: "debug", ForSecs: int(verboseFor / time.Second)}
	}
	var res logLevel
	if err := a.call(callTimeout, methodLogsLevel, p, &res); err != nil {
		return logLevel{}, err
	}
	return res, nil
}
