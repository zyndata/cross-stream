package ipc

import (
	"fmt"
	"io/fs"
	"os/user"
	"strings"

	"golang.org/x/sys/windows"
)

// DefaultSocket is where xs-hubd listens (AF_UNIX, Windows 10 1803+).
const DefaultSocket = `C:\ProgramData\CrossStream\xs-hubd.sock`

// ownerOnly is the socket DACL without a group: LocalSystem,
// Administrators and the daemon's own user, no inherited entries.
func ownerOnly() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("ipc socket acl: %w", err)
	}
	return "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + u.Uid + ")", nil
}

func restrict(path string, _ fs.FileMode) error {
	sddl, err := ownerOnly()
	if err != nil {
		return err
	}
	return setDACL(path, sddl)
}

// Chgrp lets the members of a local group (a name such as "crossstream",
// or a SID "S-1-5-...", which avoids localised names) read and write the
// socket at path, i.e. connect to it.
func Chgrp(path, group string) error {
	sid := group
	if !strings.HasPrefix(group, "S-1-") {
		g, err := user.LookupGroup(group)
		if err != nil {
			return fmt.Errorf("ipc socket group: %w", err)
		}
		sid = g.Gid
	} else if _, err := windows.StringToSid(group); err != nil {
		return fmt.Errorf("ipc socket group %s: %w", group, err)
	}
	sddl, err := ownerOnly()
	if err != nil {
		return err
	}
	return setDACL(path, sddl+"(A;;FRFW;;;"+sid+")")
}

func setDACL(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("ipc socket acl: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("ipc socket acl: %w", err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("ipc socket acl %s: %w", path, err)
	}
	return nil
}
