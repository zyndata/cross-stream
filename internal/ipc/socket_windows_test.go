package ipc

import (
	"path/filepath"
	"testing"
)

func TestChgrpSID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.sock")
	ln, err := Listen(path, 0o660)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if err := Chgrp(path, "S-1-5-32-545"); err != nil { // BUILTIN\Users
		t.Fatal(err)
	}
	if err := Chgrp(path, "S-1-bogus"); err == nil {
		t.Fatal("bad SID accepted")
	}
}
