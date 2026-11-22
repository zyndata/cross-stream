package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/skip2/go-qrcode"
	"golang.org/x/term"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

const adminUsage = `usage: xs-ctl admin <command>
  add <user> [-password-stdin]     create a web panel admin (asks for the password)
  passwd <user> [-password-stdin]  change a password (ends the user's web sessions)
  del <user>                       delete an admin
  list                             admins and whether TOTP is on
  totp <user> [-off] [-no-qr]      turn TOTP on (prints the otpauth URI and a QR code
                                   for an authenticator app) or off`

// adminCmd runs "admin add|passwd|del|list|totp".
func adminCmd(ctx context.Context, c admin.Client, stdin io.Reader, stdout, stderr io.Writer, args []string) error {
	if len(args) == 0 {
		return errors.New(adminUsage)
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("xs-ctl admin "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	pwStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin")
	off := fs.Bool("off", false, "turn TOTP off")
	noQR := fs.Bool("no-qr", false, "print the URI only")
	var user string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		user, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if user == "" && sub != "list" {
		return fmt.Errorf("admin %s needs a user name\n%s", sub, adminUsage)
	}
	switch sub {
	case "add", "passwd":
		pw, err := readPassword(stdin, stderr, *pwStdin)
		if err != nil {
			return err
		}
		// The prompt may have taken longer than the command timeout.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if sub == "add" {
			err = c.AddUser(ctx, user, pw)
		} else {
			err = c.SetPassword(ctx, user, pw)
		}
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "%s: password set\n", user)
	case "del":
		if err := c.DelUser(ctx, user); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, "deleted", user)
	case "list":
		users, err := c.Users(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "USER\tTOTP\tCREATED\tCHANGED")
		for _, u := range users {
			_, _ = fmt.Fprintf(tw, "%s\t%v\t%s\t%s\n", u.Name, u.TOTP, u.Created.Format(time.RFC3339), u.Changed.Format(time.RFC3339))
		}
		_ = tw.Flush()
	case "totp":
		uri, err := c.SetTOTP(ctx, user, !*off)
		if err != nil {
			return err
		}
		if *off {
			_, _ = fmt.Fprintf(stdout, "%s: TOTP off\n", user)
			return nil
		}
		if !*noQR {
			qr, err := qrcode.New(uri, qrcode.Medium)
			if err != nil {
				return fmt.Errorf("qr: %w", err)
			}
			_, _ = fmt.Fprint(stdout, qr.ToSmallString(false))
		}
		_, _ = fmt.Fprintln(stdout, uri)
		_, _ = fmt.Fprintf(stderr, "%s: TOTP on; scan the code in an authenticator app (the web login asks for it from now on)\n", user)
	default:
		return errors.New(adminUsage)
	}
	return nil
}

// readPassword asks twice on a terminal without echo, or reads one line
// from stdin (-password-stdin or no terminal).
func readPassword(stdin io.Reader, stderr io.Writer, fromStdin bool) (string, error) {
	f, isFile := stdin.(*os.File)
	if fromStdin || !isFile || !term.IsTerminal(int(f.Fd())) {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			return "", fmt.Errorf("read password: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	ask := func(prompt string) (string, error) {
		_, _ = fmt.Fprint(stderr, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		_, _ = fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return string(b), nil
	}
	pw, err := ask("Password: ")
	if err != nil {
		return "", err
	}
	again, err := ask("Repeat: ")
	if err != nil {
		return "", err
	}
	if pw != again {
		return "", errors.New("passwords do not match")
	}
	return pw, nil
}

// renameCmd runs "rename <name> <new-name>".
func renameCmd(ctx context.Context, c admin.Client, stdout io.Writer, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: xs-ctl rename <name> <new-name>")
	}
	if err := c.Rename(ctx, args[0], args[1]); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "renamed %s to %s\n", args[0], args[1])
	return nil
}

// infoCmd prints the server info.
func infoCmd(ctx context.Context, c admin.Client, stdout io.Writer) error {
	in, err := c.Info(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	row := func(k string, v any) { _, _ = fmt.Fprintf(tw, "%s\t%v\n", k, v) }
	row("version", in.Version)
	row("started", in.Started.Format(time.RFC3339))
	row("udp ports", in.UDPPorts)
	row("tcp ports", in.TCPPorts)
	row("wss ports", fmt.Sprintf("%v %s", in.WSSPorts, in.WSSPath))
	tp := fmt.Sprint(in.TunnelPort)
	if in.RestartRequired {
		tp += fmt.Sprintf(" (configured %d: restart required)", in.TunnelPortConfigured)
	}
	row("tunnel port", tp+", in "+sourceNames[in.TunnelPortSource])
	row("endpoints", strings.Join(in.Endpoints, ", ")+in.EndpointsErr)
	row("min hub version", in.MinHubVersion)
	row("pep", in.PEP)
	row("token ttl", in.TokenTTL)
	row("egress allow", in.EgressAllow)
	if len(in.Reenroll) > 0 {
		row("enroll again", strings.Join(in.Reenroll, ", ")+" (enrolled with another tunnel port: revoke + new code)")
	}
	return tw.Flush()
}

// auditCmd prints the newest audit entries, oldest first.
func auditCmd(ctx context.Context, c admin.Client, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("xs-ctl audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	n := fs.Int("n", 50, "entries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	entries, err := c.AuditLog(ctx, *n)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIME\tACTOR\tACTION\tTARGET\tRESULT")
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		res := "ok"
		if !e.OK {
			res = "failed: " + e.Error
		}
		target := strings.TrimSpace(e.Target + " " + e.Detail)
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Time.Local().Format(time.DateTime), e.Actor, e.Action, target, res)
	}
	return tw.Flush()
}
