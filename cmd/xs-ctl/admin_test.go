package main

import (
	"context"
	"strings"
	"testing"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

func TestReadPasswordStdin(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"secret pw\n", "secret pw", true},
		{"secret pw\r\nmore\n", "secret pw", true},
		{"no newline", "no newline", true},
		{"", "", false},
	}
	for _, tt := range tests {
		got, err := readPassword(strings.NewReader(tt.in), &strings.Builder{}, false)
		if (err == nil) != tt.ok || got != tt.want {
			t.Fatalf("%q: got %q, %v", tt.in, got, err)
		}
	}
}

func TestAdminCmdUsage(t *testing.T) {
	c := admin.Client{Socket: "/nonexistent/xs.sock"}
	tests := []struct {
		args    []string
		wantErr string
	}{
		{nil, "usage: xs-ctl admin"},
		{[]string{"add"}, "needs a user name"},
		{[]string{"totp", "-off"}, "needs a user name"},
		{[]string{"frob", "x"}, "usage: xs-ctl admin"},
		{[]string{"del", "alice"}, "connect to xs-server"},
		{[]string{"add", "alice", "-password-stdin"}, "connect to xs-server"},
	}
	for _, tt := range tests {
		var out strings.Builder
		err := adminCmd(context.Background(), c, strings.NewReader("password1\n"), &out, &out, tt.args)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Fatalf("%v: err = %v, want %q", tt.args, err, tt.wantErr)
		}
	}
}

// TestNameCommandsHelp: "-h" after enroll/revoke/rename prints the usage and
// never reaches the server (it used to enroll a hub named "-h").
func TestNameCommandsHelp(t *testing.T) {
	for _, args := range [][]string{{"enroll", "-h"}, {"revoke", "--help"}, {"rename", "a", "-help"}} {
		var out strings.Builder
		err := run(append([]string{"-socket", "/nonexistent/xs.sock"}, args...), strings.NewReader(""), &out, &out)
		if err != nil || !strings.Contains(out.String(), "usage: xs-ctl") {
			t.Errorf("%v: err %v, output %q, want the usage", args, err, out.String())
		}
	}
}
