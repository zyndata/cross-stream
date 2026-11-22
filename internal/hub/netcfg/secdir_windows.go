package netcfg

import (
	"fmt"
	"os"
	"os/user"

	"golang.org/x/sys/windows"
)

// SecureDir creates dir if needed and gives LocalSystem, Administrators and
// the daemon's own user full control of it and everything in it, nobody
// else anything; inherited entries (ProgramData lets every user create
// files) are cut off. An elevated daemon also takes ownership for the
// Administrators group, so a user who created the directory first keeps no
// rights. Windows has no file modes: this is what keeps the hub keys private.
func SecureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("state dir acl: %w", err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + u.Uid + ")")
	if err != nil {
		return fmt.Errorf("state dir acl: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("state dir acl: %w", err)
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	var owner *windows.SID
	if windows.GetCurrentProcessToken().IsElevated() {
		if owner, err = windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid); err != nil {
			return fmt.Errorf("state dir owner: %w", err)
		}
		info |= windows.OWNER_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, info, owner, nil, dacl, nil); err != nil {
		return fmt.Errorf("state dir acl %s: %w", dir, err)
	}
	return nil
}
