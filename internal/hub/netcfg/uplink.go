package netcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcSys is the sysctl tree (tests use a temporary directory).
type ProcSys string

// DefaultProcSys is the real sysctl tree.
const DefaultProcSys ProcSys = "/proc/sys"

func (p ProcSys) rpPath(iface string) string {
	return filepath.Join(string(p), "net", "ipv4", "conf", iface, "rp_filter")
}

// SetRPFilter sets the reverse path filter of iface (2 = loose: replies
// from the server arrive on an uplink that is not the main default route)
// and returns the previous value.
func (p ProcSys) SetRPFilter(iface string, v int) (old int, err error) {
	if strings.ContainsAny(iface, "/.") || iface == "" {
		return 0, fmt.Errorf("netcfg: bad interface name %q", iface)
	}
	b, err := os.ReadFile(p.rpPath(iface))
	if err != nil {
		return 0, fmt.Errorf("netcfg: rp_filter %s: %w", iface, err)
	}
	old, err = strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("netcfg: rp_filter %s: %w", iface, err)
	}
	if old == v {
		return old, nil
	}
	if err := os.WriteFile(p.rpPath(iface), []byte(strconv.Itoa(v)+"\n"), 0o644); err != nil {
		return old, fmt.Errorf("netcfg: set rp_filter %s: %w", iface, err)
	}
	return old, nil
}
