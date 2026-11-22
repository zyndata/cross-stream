package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zyndata/cross-stream/internal/webui"
)

// EnvPrefix is the prefix of environment overrides: every config key k can be
// set with XS_SERVER_<K> (upper case), e.g. XS_SERVER_LISTEN_PORTS=51900,443.
const EnvPrefix = "XS_SERVER_"

// Config is server.yaml.
type Config struct {
	ListenAddress   string   `yaml:"listen_address"`   // bind address, "" = all
	ListenPorts     []int    `yaml:"listen_ports"`     // UDP ports, one shared session table
	TCPPorts        []int    `yaml:"tcp_ports"`        // framed TCP transport ports ("none" in env = off)
	WSSPorts        []int    `yaml:"wss_ports"`        // WebSocket-over-TLS ports ("none" in env = off)
	WSSPath         string   `yaml:"wss_path"`         // WebSocket URL path
	PublicEndpoints []string `yaml:"public_endpoints"` // put into enrollment codes; empty = auto-detect
	// PublicPort is the port hubs dial for the main tunnel port (the first of
	// listen_ports, UDP and TCP) when a router or the HA Network section maps
	// another public port to it; 0 = the listen port itself.
	PublicPort int `yaml:"public_port"`
	// PortSource is where public_port is configured and changed from the
	// panel: "" = this file (or XS_SERVER_PUBLIC_PORT, read-only), "supervisor" =
	// the Home Assistant app's Network section (host port of the main port).
	PortSource    string        `yaml:"port_source"`
	DataDir       string        `yaml:"data_dir"`       // state.json and server.key
	TokenTTL      time.Duration `yaml:"token_ttl"`      // enrollment token lifetime
	ControlSocket string        `yaml:"control_socket"` // Unix socket for xs-ctl
	TunName       string        `yaml:"tun_name"`
	MTU           int           `yaml:"mtu"` // tunnel MTU sent to hubs
	LogLevel      string        `yaml:"log_level"`
	PEP           bool          `yaml:"pep"`             // accept PEP streams (announced in RESP)
	EgressAllow   []string      `yaml:"egress_allow"`    // private prefixes hubs may reach (egress filter exceptions)
	MinHubVersion string        `yaml:"min_hub_version"` // e.g. v1.0.0; older hubs are refused ("" = any)
	MetricsListen string        `yaml:"metrics_listen"`  // Prometheus /metrics, loopback or private address only ("" = off)
	// Web panel (phase 15): HTTPS listen address ("off" or "" = no panel);
	// a non-loopback address needs WebAllowPublic.
	WebListen      string `yaml:"web_listen"`
	WebAllowPublic bool   `yaml:"web_allow_public"`
	WebCert        string `yaml:"web_cert"` // PEM certificate and key; empty = self-signed in data_dir
	WebKey         string `yaml:"web_key"`
	// Home Assistant ingress (phase 17b): plain-HTTP panel for HA's proxy
	// ("" or "off" = none); only sources in web_ingress_allow may connect.
	WebIngressListen string   `yaml:"web_ingress_listen"`
	WebIngressAllow  []string `yaml:"web_ingress_allow"`
}

// WebEnabled reports whether the web panel is on.
func (c Config) WebEnabled() bool { return c.WebListen != "" && c.WebListen != "off" }

// IngressEnabled reports whether the Home Assistant ingress listener is on.
func (c Config) IngressEnabled() bool { return c.WebIngressListen != "" && c.WebIngressListen != "off" }

// IngressAllow returns the parsed web_ingress_allow (the config must be valid).
func (c Config) IngressAllow() []netip.Prefix {
	p, _ := webui.ParseAllow(c.WebIngressAllow)
	return p
}

// Egress returns the egress filter (the config must be valid).
func (c Config) Egress() Egress {
	e, _ := ParseEgress(c.EgressAllow)
	return e
}

// DefaultConfig returns the defaults from architecture.md and protocol.md.
func DefaultConfig() Config {
	return Config{
		ListenPorts:   []int{51900, 443},
		TCPPorts:      []int{51900},
		WSSPorts:      []int{443},
		WSSPath:       "/xs",
		DataDir:       "/data",
		TokenTTL:      24 * time.Hour,
		ControlSocket: "/run/xs-server.sock",
		TunName:       "xss0",
		MTU:           1360,
		LogLevel:      "info",
		PEP:           true,
		WebListen:     webui.DefaultListen,

		WebIngressAllow: []string{webui.DefaultIngressAllow},
	}
}

// LoadConfig reads path (a missing file means defaults), applies environment
// overrides from getenv and validates the result.
func LoadConfig(path string, getenv func(string) string) (Config, error) {
	cfg := DefaultConfig()
	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return Config{}, fmt.Errorf("read config: %w", err)
		default:
			dec := yaml.NewDecoder(bytes.NewReader(b))
			dec.KnownFields(true)
			if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
				return Config{}, fmt.Errorf("parse config %s: %w", path, err)
			}
		}
	}
	if err := applyEnv(&cfg, getenv); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyEnv overrides every yaml key that has a non-empty XS_SERVER_<KEY>.
func applyEnv(cfg *Config, getenv func(string) string) error {
	v := reflect.ValueOf(cfg).Elem()
	t := v.Type()
	for i := range t.NumField() {
		key := t.Field(i).Tag.Get("yaml")
		env := EnvPrefix + strings.ToUpper(key)
		val := strings.TrimSpace(getenv(env))
		if val == "" {
			continue
		}
		if err := setField(v.Field(i), val); err != nil {
			return fmt.Errorf("env %s: %w", env, err)
		}
	}
	return nil
}

func setField(f reflect.Value, val string) error {
	switch f.Interface().(type) {
	case string:
		f.SetString(val)
	case int:
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("not an integer: %w", err)
		}
		f.SetInt(int64(n))
	case time.Duration:
		d, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("not a duration: %w", err)
		}
		f.SetInt(int64(d))
	case bool:
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("not a boolean: %w", err)
		}
		f.SetBool(b)
	case []string:
		f.Set(reflect.ValueOf(splitList(val)))
	case []int:
		ns := []int{}
		if val == "none" {
			f.Set(reflect.ValueOf(ns))
			return nil
		}
		for _, s := range splitList(val) {
			n, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("not an integer list: %w", err)
			}
			ns = append(ns, n)
		}
		f.Set(reflect.ValueOf(ns))
	default:
		return fmt.Errorf("unsupported type %s", f.Type())
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// validPorts rejects out-of-range and duplicate ports (0 = any free port, tests).
func validPorts(kind string, ports []int) error {
	seen := map[int]bool{}
	for _, p := range ports {
		if p < 0 || p > 65535 || (p != 0 && seen[p]) {
			return fmt.Errorf("config: %s port %d invalid or duplicate", kind, p)
		}
		seen[p] = true
	}
	return nil
}

// Validate checks value ranges.
func (c Config) Validate() error {
	if len(c.ListenPorts) == 0 {
		return errors.New("config: listen_ports is empty")
	}
	if err := validPorts("listen", c.ListenPorts); err != nil {
		return err
	}
	if err := validPorts("tcp", append(append([]int{}, c.TCPPorts...), c.WSSPorts...)); err != nil {
		return err
	}
	if c.PortSource != "" && c.PortSource != "supervisor" {
		return fmt.Errorf("config: port_source %q (want empty or supervisor)", c.PortSource)
	}
	if c.PublicPort < 0 || c.PublicPort > 65535 {
		return fmt.Errorf("config: public_port %d out of range 1..65535 (0 = the listen port)", c.PublicPort)
	}
	if len(c.WSSPorts) > 0 && !strings.HasPrefix(c.WSSPath, "/") {
		return fmt.Errorf("config: wss_path %q must start with /", c.WSSPath)
	}
	switch {
	case c.DataDir == "":
		return errors.New("config: data_dir is empty")
	case c.TokenTTL <= 0:
		return errors.New("config: token_ttl must be positive")
	case c.ControlSocket == "":
		return errors.New("config: control_socket is empty")
	case c.TunName == "":
		return errors.New("config: tun_name is empty")
	case c.MTU < 576 || c.MTU > 65535:
		return fmt.Errorf("config: mtu %d out of range 576..65535", c.MTU)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: log_level %q (want debug|info|warn|error)", c.LogLevel)
	}
	if _, err := ParseEgress(c.EgressAllow); err != nil {
		return err
	}
	if c.MinHubVersion != "" {
		if _, ok := ParseVersion(c.MinHubVersion); !ok {
			return fmt.Errorf("config: min_hub_version %q (want vX.Y.Z)", c.MinHubVersion)
		}
	}
	if c.MetricsListen != "" {
		if err := checkMetricsListen(c.MetricsListen); err != nil {
			return err
		}
	}
	if c.WebEnabled() {
		if err := webui.CheckListen(c.WebListen, c.WebAllowPublic); err != nil {
			return fmt.Errorf("config: web_listen: %w", err)
		}
	}
	if c.IngressEnabled() {
		if err := webui.CheckListen(c.WebIngressListen, true); err != nil {
			return fmt.Errorf("config: web_ingress_listen: %w", err)
		}
		if len(c.WebIngressAllow) == 0 {
			return errors.New("config: web_ingress_allow is empty")
		}
	}
	if _, err := webui.ParseAllow(c.WebIngressAllow); err != nil {
		return fmt.Errorf("config: web_ingress_allow: %w", err)
	}
	if (c.WebCert == "") != (c.WebKey == "") {
		return errors.New("config: web_cert and web_key go together")
	}
	return nil
}
