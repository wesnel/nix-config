package sshmux

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

func TestFits(t *testing.T) {
	const suffix = "/ssh4294967295/999.0123456789abcdef"
	limit := len(syscall.RawSockaddrUnix{}.Path) - 1
	base := "/" + strings.Repeat("a", limit-len(suffix)-1)
	for _, tc := range []struct {
		name, base string
		want       bool
	}{
		{"at the limit", base, true},
		{"one byte too long", base + "a", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Fits(tc.base); got != tc.want {
				t.Errorf("Fits(%q) = %v, want %v", tc.base, got, tc.want)
			}
		})
	}
}
