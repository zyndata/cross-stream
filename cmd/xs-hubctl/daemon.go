package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/ipc"
)

// daemonRunning reports whether xs-hubd answers on its IPC socket. A socket
// file left by a crashed daemon refuses connections and counts as not
// running; any other failure (e.g. no permission) counts as running, so the
// caller reports the real IPC error.
func daemonRunning(sock string) bool {
	if _, err := os.Stat(sock); errors.Is(err, fs.ErrNotExist) {
		return false
	}
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return !errors.Is(err, syscall.ECONNREFUSED)
	}
	_ = c.Close()
	return true
}

func pairQRCmd(ctx context.Context, sock string, args []string) error {
	fs := flag.NewFlagSet("pair-qr", flag.ContinueOnError)
	noQR := fs.Bool("no-qr", false, "print only the XSR1 code (default when stdout is not a terminal)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var q hub.PairQR
	if err := ipc.Call(ctx, sock, hub.MethodPairingQR, hub.QRParams{NoPNG: true}, &q); err != nil {
		return err
	}
	if !*noQR && isTerminal(os.Stdout) {
		qr, err := qrcode.New(q.Code, qrcode.Medium)
		if err != nil {
			return fmt.Errorf("qr: %w", err)
		}
		fmt.Print(qr.ToSmallString(false))
		fmt.Fprintf(os.Stderr, "valid until %s\n", q.Expires.Local().Format(time.TimeOnly))
	}
	fmt.Println(q.Code)
	return nil
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func forgetCmd(ctx context.Context, sock string, args []string) error {
	if len(args) != 1 {
		return errors.New(usage)
	}
	var r hub.RelayStatus
	if err := ipc.Call(ctx, sock, hub.MethodRelaysForget, hub.ForgetParams{Relay: args[0]}, &r); err != nil {
		return err
	}
	fmt.Printf("forgot relay %s (%s)\n", r.ID, r.Name)
	return nil
}

// printCmd calls a parameterless method and prints the result as JSON.
func printCmd(ctx context.Context, sock, method string) error {
	var res json.RawMessage
	if err := ipc.Call(ctx, sock, method, nil, &res); err != nil {
		return err
	}
	return printJSON(res)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func hotspotCmd(ctx context.Context, sock string, args []string) error {
	fs := flag.NewFlagSet("hotspot", flag.ContinueOnError)
	ssid := fs.String("ssid", "", "network name")
	psk := fs.String("psk", "", "WPA2 password (8-63 characters)")
	band := fs.String("band", "", "a (5 GHz, falls back to 2.4 GHz) or bg (2.4 GHz)")
	channel := fs.Int("channel", 0, "channel (0 = automatic)")
	country := fs.String("country", "", "regulatory country, e.g. PL")
	clientWifi := fs.String("client-wifi", "", "when the Wi-Fi card is connected to a network: share (hotspot next to it), disconnect, or ask")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *clientWifi == "ask" {
		*clientWifi = ""
	}
	var p hub.HotspotParams
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "ssid":
			p.SSID = ssid
		case "psk":
			p.PSK = psk
		case "band":
			p.Band = band
		case "channel":
			p.Channel = channel
		case "country":
			p.Country = country
		case "client-wifi":
			p.ClientWifi = clientWifi
		}
	})
	method := hub.MethodHotspotGet
	if fs.NFlag() > 0 {
		method = hub.MethodHotspotSet
		fmt.Fprintln(os.Stderr, "restarting the hotspot; relays need a new pairing code if SSID or password changed")
	}
	var info hub.HotspotInfo
	var params any
	if fs.NFlag() > 0 {
		params = p
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute) // NM may take ~1 min to restart the AP
	defer cancel()
	if err := ipc.Call(ctx, sock, method, params, &info); err != nil {
		return err
	}
	return printJSON(info)
}

// eventsCmd prints one status JSON line per second until interrupted.
func eventsCmd(ctx context.Context, sock string) error {
	err := ipc.Subscribe(ctx, sock, hub.MethodEventsSubscribe, nil, func(_ string, params json.RawMessage) error {
		_, err := fmt.Println(string(params))
		return err
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func enrollViaDaemon(ctx context.Context, sock, code string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var e hub.Enrollment
	if err := ipc.Call(ctx, sock, hub.MethodServerEnroll, hub.EnrollParams{Code: code}, &e); err != nil {
		return err
	}
	fmt.Printf("enrolled as %s, tunnel ip %s\n", e.Name, e.TunnelIP)
	return nil
}

func pathCmd(ctx context.Context, sock string, args []string) error {
	if len(args) < 1 {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("path", flag.ContinueOnError)
	prio := fs.String("priority", "", "normal, secondary or backup")
	rate := fs.Int64("rate-kbps", -1, "rate limit in kbit/s per direction (0 = none)")
	capMB := fs.Int64("cap-mb", -1, "data cap in MB (0 = none)")
	up := fs.String("up", "", "upload direction: on, backup_only or off")
	down := fs.String("down", "", "download direction: on, backup_only or off")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	p := hub.PathParams{Relay: args[0]}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["priority"] {
		p.Priority = prio
	}
	if set["up"] {
		p.Up = up
	}
	if set["down"] {
		p.Down = down
	}
	var err error
	if set["rate-kbps"] {
		if p.RateKbps, err = u32(*rate); err != nil {
			return fmt.Errorf("-rate-kbps: %w", err)
		}
	}
	if set["cap-mb"] {
		if p.CapMB, err = u32(*capMB); err != nil {
			return fmt.Errorf("-cap-mb: %w", err)
		}
	}
	var r hub.RelayStatus
	if err := ipc.Call(ctx, sock, hub.MethodRelaysUpdate, p, &r); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	return enc.Encode(r.Path)
}

func u32(v int64) (*uint32, error) {
	if v < 0 || v > 0xFFFFFFFF {
		return nil, errors.New("out of range")
	}
	x := uint32(v)
	return &x, nil
}
