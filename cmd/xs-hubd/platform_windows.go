package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"

	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
)

const (
	defaultDir  = `C:\ProgramData\CrossStream`
	serviceName = "CrossStreamHub"
	logFile     = "xs-hubd.log"
	maxLogBytes = 10 << 20 // the log file starts over above this at service start
)

// hotspotConfig sets up Windows Mobile Hotspot; its adapter becomes the LAN
// once it runs. Windows has no Ethernet share port and no nft MSS rule.
func hotspotConfig(_ context.Context, cfg *hub.DaemonConfig, wifi, eth, natMode string) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("the hotspot needs administrator rights: run xs-hubd as the CrossStreamHub service (deploy/hub-windows/install.ps1)")
	}
	if eth != "" || wifi != "" {
		return errors.New("-eth-iface and -wifi-iface are not supported on Windows (Mobile Hotspot picks the Wi-Fi adapter)")
	}
	cfg.LanIface, cfg.DNS, cfg.ClampMSS = "", true, true
	nat, err := useHotspotNAT(natMode)
	if err != nil {
		return err
	}
	if nat {
		// Mobile Hotspot stays on any profile; xs-hubd NATs its clients
		// into the tunnel (Windows 10 has no profile for the TUN).
		cfg.HotspotNAT = netcfg.WinHotspotSubnet
		cfg.Hotspot = &netcfg.WinHotspot{}
	} else {
		cfg.Hotspot = &netcfg.WinHotspot{Share: cfg.TunName}
	}
	cfg.Inhibitor = &netcfg.PowerRequest{}
	return nil
}

// runService runs xs-hubd under the service control manager (it reports
// false when started from a console). Arguments come from the service's
// command line; logs go to xs-hubd.log in the state directory.
func runService(run func(context.Context, []string, io.Writer) error) (bool, error) {
	if ok, err := svc.IsWindowsService(); err != nil || !ok {
		return false, nil
	}
	h := &handler{run: run, args: os.Args[1:]}
	if err := svc.Run(serviceName, h); err != nil {
		return true, fmt.Errorf("service: %w", err)
	}
	return true, h.err
}

type handler struct {
	run  func(context.Context, []string, io.Writer) error
	args []string
	err  error
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	out, closeLog := openLog(h.args)
	defer closeLog()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.run(ctx, h.args, out) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case h.err = <-done:
			if h.err != nil {
				_, _ = fmt.Fprintln(out, "xs-hubd:", h.err)
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				cancel()
				h.err = <-done
				return false, 0
			}
		}
	}
}

// openLog opens <dir>/xs-hubd.log (dir from -dir in args, else the default).
func openLog(args []string) (io.Writer, func()) {
	dir := defaultDir
	for i, a := range args {
		switch {
		case (a == "-dir" || a == "--dir") && i+1 < len(args):
			dir = args[i+1]
		case strings.HasPrefix(a, "-dir="), strings.HasPrefix(a, "--dir="):
			_, dir, _ = strings.Cut(a, "=")
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return io.Discard, func() {}
	}
	if err := netcfg.SecureDir(dir); err != nil {
		return io.Discard, func() {}
	}
	path := filepath.Join(dir, logFile)
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogBytes {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return io.Discard, func() {}
	}
	return f, func() { _ = f.Close() }
}

// win11Build is the first Windows 11 build: Windows 10 shows no connection
// profile for the Wintun adapter, so Mobile Hotspot cannot share the tunnel.
const win11Build = 22000

// useHotspotNAT resolves -hotspot-nat (auto = on for Windows 10).
func useHotspotNAT(mode string) (bool, error) {
	switch mode {
	case "on":
		return true, nil
	case "off":
		return false, nil
	case "auto":
		return windows.RtlGetVersion().BuildNumber < win11Build, nil
	}
	return false, fmt.Errorf("-hotspot-nat %q: want auto, on or off", mode)
}

// raisePriority moves xs-hubd to ABOVE_NORMAL_PRIORITY_CLASS so packet
// forwarding keeps up while the laptop is busy (a build, a game).
func raisePriority() error {
	if err := windows.SetPriorityClass(windows.CurrentProcess(), windows.ABOVE_NORMAL_PRIORITY_CLASS); err != nil {
		return fmt.Errorf("set priority class: %w", err)
	}
	return nil
}
