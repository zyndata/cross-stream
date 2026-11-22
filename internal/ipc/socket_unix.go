//go:build !windows

package ipc

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"strconv"
)

// DefaultSocket is where xs-hubd listens.
const DefaultSocket = "/run/crossstream/xs-hubd.sock"

func restrict(path string, mode fs.FileMode) error {
	return os.Chmod(path, mode)
}

// Chgrp gives the socket at path to the named group (for 0660 sockets read
// by unprivileged GUI users). A missing group is an error the caller logs.
func Chgrp(path, group string) error {
	g, err := user.LookupGroup(group)
	if err != nil {
		return fmt.Errorf("ipc socket group: %w", err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return fmt.Errorf("ipc socket group %s: %w", group, err)
	}
	if err := os.Chown(path, -1, gid); err != nil {
		return fmt.Errorf("ipc socket group: %w", err)
	}
	return nil
}
