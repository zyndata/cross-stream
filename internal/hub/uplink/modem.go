package uplink

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// Modem is what ModemManager reports about a modem (read only: the hub
// never configures modems).
type Modem struct {
	Tech     string  `json:"tech,omitempty"`     // access technology: lte, 5gnr, umts…
	Operator string  `json:"operator,omitempty"` // network name
	Quality  int     `json:"quality,omitempty"`  // signal quality, percent
	RSRP     float64 `json:"rsrp_dbm,omitempty"` // LTE/NR RSRP, 0 = unknown
}

// Run runs a command and returns its output (exec in production).
type Run func(ctx context.Context, name string, args ...string) (string, error)

// keyValues parses `mmcli -K` output ("key : value" lines).
func keyValues(s string) map[string][]string {
	out := make(map[string][]string)
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if i := strings.Index(k, ".value["); i >= 0 {
			k = k[:i]
		}
		if v != "" && v != "--" {
			out[k] = append(out[k], v)
		}
	}
	return out
}

func first(kv map[string][]string, k string) string {
	if v := kv[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// ParseModem parses `mmcli -m N -K` and `mmcli -m N --signal-get -K`
// output into the net interfaces of the modem and what it reports.
func ParseModem(info, signal string) (ifaces []string, m Modem) {
	kv := keyValues(info)
	for _, p := range kv["modem.generic.ports"] {
		if name, ok := strings.CutSuffix(p, " (net)"); ok {
			ifaces = append(ifaces, name)
		}
	}
	m.Tech = strings.Join(kv["modem.generic.access-technologies"], ",")
	m.Operator = first(kv, "modem.3gpp.operator-name")
	m.Quality, _ = strconv.Atoi(first(kv, "modem.generic.signal-quality.value"))
	sg := keyValues(signal)
	for _, k := range []string{"modem.signal.5g.rsrp", "modem.signal.lte.rsrp"} {
		if v, err := strconv.ParseFloat(first(sg, k), 64); err == nil && v < 0 {
			m.RSRP = v
			break
		}
	}
	return ifaces, m
}

// Modems asks ModemManager (mmcli) about every modem and returns them by
// net interface. A missing mmcli or ModemManager gives an empty map.
func Modems(ctx context.Context, run Run) map[string]Modem {
	out := make(map[string]Modem)
	list, err := run(ctx, "mmcli", "-L", "-K")
	if err != nil {
		return out
	}
	for _, path := range keyValues(list)["modem-list"] {
		info, err := run(ctx, "mmcli", "-m", path, "-K")
		if err != nil {
			continue
		}
		signal, _ := run(ctx, "mmcli", "-m", path, "--signal-get", "-K")
		ifaces, m := ParseModem(info, signal)
		for _, i := range ifaces {
			out[i] = m
		}
	}
	return out
}

// ExecRun runs a command (Run for production).
func ExecRun(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}
