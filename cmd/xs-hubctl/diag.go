package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/hub/hublog"
	"github.com/zyndata/cross-stream/internal/ipc"
)

// keyCmd shows where the hub key lives, or seals it with the TPM.
func keyCmd(dir string, args []string) error {
	switch {
	case len(args) == 0:
		where := "none yet"
		if _, err := os.Stat(filepath.Join(dir, hub.TPMKeyFile)); err == nil {
			where = "sealed by the TPM (" + hub.TPMKeyFile + ")"
		} else if _, err := os.Stat(filepath.Join(dir, hub.KeyFile)); err == nil {
			where = "file " + hub.KeyFile + " (0600); xs-hubctl key migrate-tpm seals it"
		}
		fmt.Println("hub key:", where)
		return nil
	case len(args) == 1 && args[0] == "migrate-tpm":
		kp, err := hub.MigrateKeyToTPM(dir)
		if err != nil {
			return err
		}
		fmt.Printf("hub key %s sealed by the TPM; %s removed (identity unchanged)\n",
			crypto.EncodeKey(kp.Public), hub.KeyFile)
		return nil
	default:
		return errors.New("usage: xs-hubctl key [migrate-tpm]")
	}
}

// logsCmd prints the daemon's recent log, redacted, or saves it with -o.
func logsCmd(ctx context.Context, sock string, args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	out := fs.String("o", "", "write to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: xs-hubctl logs [-o file]")
	}
	var res hub.LogsExport
	if err := ipc.Call(ctx, sock, hub.MethodLogsExport, nil, &res); err != nil {
		return err
	}
	text := fmt.Sprintf("# xs-hubd %s log, redacted (codes, secrets, keys, MACs, public IPs)\n%s\n",
		res.Version, strings.Join(res.Lines, "\n"))
	if *out == "" {
		_, err := io.WriteString(os.Stdout, text)
		return err
	}
	if err := os.WriteFile(*out, []byte(text), 0o600); err != nil {
		return fmt.Errorf("logs: %w", err)
	}
	fmt.Printf("%d log lines saved to %s\n", len(res.Lines), *out)
	return nil
}

// loglevelCmd shows or changes xs-hubd's log level at run time.
func loglevelCmd(ctx context.Context, sock string, args []string) error {
	p, err := parseLoglevel(args)
	if err != nil {
		return err
	}
	var st hublog.LevelState
	if err := ipc.Call(ctx, sock, hub.MethodLogsLevel, p, &st); err != nil {
		return err
	}
	fmt.Println(formatLevel(st))
	return nil
}

const loglevelUsage = "usage: xs-hubctl loglevel [debug|info] [-for 15m]"

// parseLoglevel accepts the level before or after -for.
func parseLoglevel(args []string) (hub.LogLevelParams, error) {
	var p hub.LogLevelParams
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		p.Level, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("loglevel", flag.ContinueOnError)
	dur := fs.Duration("for", 0, "go back to the start level (-v) after this, e.g. 15m (0 = stay)")
	if err := fs.Parse(args); err != nil {
		return p, err
	}
	if fs.NArg() == 1 && p.Level == "" {
		p.Level = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return p, errors.New(loglevelUsage)
	}
	if p.Level != "" {
		if _, err := hublog.ParseLevel(p.Level); err != nil {
			return p, err
		}
	}
	if *dur < 0 || (*dur > 0 && p.Level == "") {
		return p, errors.New(loglevelUsage)
	}
	p.ForSecs = int(dur.Round(time.Second) / time.Second)
	if *dur > 0 && p.ForSecs == 0 {
		p.ForSecs = 1
	}
	return p, nil
}

func formatLevel(st hublog.LevelState) string {
	s := "log level " + st.Level
	if !st.Until.IsZero() {
		s += fmt.Sprintf(", back to %s at %s", st.Start, st.Until.Local().Format("15:04:05"))
	}
	return s
}
