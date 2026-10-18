package main

import (
	"syscall"
	"testing"
)

// On Linux the command dies with usage run, in a group of its own or not.
func TestChildAttrDeathSignal(t *testing.T) {
	for _, group := range []bool{true, false} {
		if a := childAttr(group); a.Pdeathsig != syscall.SIGTERM || a.Setpgid != group {
			t.Errorf("group %v: %+v", group, a)
		}
	}
}
