package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestRaisePriority(t *testing.T) {
	p := windows.CurrentProcess()
	before, err := windows.GetPriorityClass(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.SetPriorityClass(p, before) })
	if err := raisePriority(); err != nil {
		t.Fatal(err)
	}
	if got, _ := windows.GetPriorityClass(p); got != windows.ABOVE_NORMAL_PRIORITY_CLASS {
		t.Fatalf("priority class %#x, want ABOVE_NORMAL %#x", got, windows.ABOVE_NORMAL_PRIORITY_CLASS)
	}
}
