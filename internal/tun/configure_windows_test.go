package tun

import "testing"

func TestAdapterGUID(t *testing.T) {
	a, b, c := AdapterGUID("xs0"), AdapterGUID("xs0"), AdapterGUID("xs1")
	if a != b {
		t.Fatal("GUID not stable for the same name")
	}
	if a == c {
		t.Fatal("different names share a GUID")
	}
	if a.Data3>>12 != 4 || a.Data4[0]>>6 != 2 {
		t.Fatalf("GUID %v is not a version 4 / RFC 4122 layout", a)
	}
}
