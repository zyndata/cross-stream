package server

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Sysctl is one kernel setting from setup-cases.md section 4.
type Sysctl struct {
	Key, Value string
	Namespaced bool // per network namespace (a container can own it)
}

// HardeningSysctls are the kernel settings for a server that forwards hub
// traffic: loose reverse-path filtering (asymmetric paths through the
// tunnel), no ICMP redirects, fq + BBR for the server's own TCP sockets
// (PEP origins, TCP/WSS transports) and no core dumps of setuid programs.
var HardeningSysctls = []Sysctl{
	{"net.ipv4.conf.all.rp_filter", "2", true},
	{"net.ipv4.conf.default.rp_filter", "2", true},
	{"net.ipv4.conf.all.accept_redirects", "0", true},
	{"net.ipv4.conf.default.accept_redirects", "0", true},
	{"net.ipv4.conf.all.secure_redirects", "0", true},
	{"net.ipv4.conf.default.secure_redirects", "0", true},
	{"net.ipv4.conf.all.send_redirects", "0", true},
	{"net.ipv4.conf.default.send_redirects", "0", true},
	{"net.ipv4.tcp_congestion_control", "bbr", true},
	{"net.core.default_qdisc", "fq", false},
	{"fs.suid_dumpable", "0", false},
}

// ApplySysctls makes each setting under procSys (normally /proc/sys) match,
// writing only the ones that differ. A setting that cannot be written (a
// read-only /proc/sys in a container, a host-wide key, a missing module) is
// logged once and returned in missing, so the host can set it
// (deploy/server/90-crossstream.conf). It never fails the start.
func ApplySysctls(procSys string, want []Sysctl, log *slog.Logger) (missing []Sysctl) {
	for _, s := range want {
		path := filepath.Join(procSys, filepath.FromSlash(strings.ReplaceAll(s.Key, ".", "/")))
		cur, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(cur)) == s.Value {
			continue
		}
		if err == nil {
			err = os.WriteFile(path, []byte(s.Value+"\n"), 0o644)
		}
		if err != nil {
			where := "set it on the host"
			if s.Namespaced {
				where = "set it in the container (compose sysctls) or on the host"
			}
			log.Warn("sysctl not applied: "+where, "key", s.Key, "want", s.Value,
				"have", strings.TrimSpace(string(cur)), "err", shortErr(err))
			missing = append(missing, s)
			continue
		}
		log.Info("sysctl set", "key", s.Key, "value", s.Value)
	}
	return missing
}

func shortErr(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return fmt.Sprint(pe.Err)
	}
	return err.Error()
}
