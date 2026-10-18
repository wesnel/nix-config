package tty

import (
	"os"
	"path/filepath"
	"testing"
)

// A character device that is not a terminal is not one.
func TestIsTerminal(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	file, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	for name, f := range map[string]*os.File{"/dev/null": null, "a file": file, "a pipe": w} {
		if IsTerminal(f) {
			t.Errorf("%s is a terminal", name)
		}
	}
	if tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		defer tty.Close()
		if !IsTerminal(tty) {
			t.Error("/dev/tty is not a terminal")
		}
	} else {
		t.Logf("no controlling terminal to check: %v", err)
	}
}
