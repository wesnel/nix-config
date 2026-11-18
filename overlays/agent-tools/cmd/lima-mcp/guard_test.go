package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const testQEMU = "/nix/store/0000000000000000000000000000000q-qemu-11.1.2/bin/qemu-system-aarch64"

func TestQEMUGuard(t *testing.T) {
	c := managedConfig(t)
	// store stands in for /nix/store, where the guard's configuration
	// must be: the test cannot write the real store.
	store := t.TempDir()
	file := filepath.Join(store, "lima-guard.json")
	data := `{"home":"/Users/x","limaHome":"` + c.LimaHome + `","instance":"ngrok-agent","qemu":"` + testQEMU + `",
	"deniedReads":[],"connect":"192.168.5.100:3128","quic":"192.168.5.101:443"}`
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	// execs records what the guard would execute in its place.
	var execs [][]string
	execute := func(path string, argv []string, _ []string) error {
		execs = append(execs, append([]string{path}, argv...))
		return nil
	}

	t.Run("executes QEMU for a probe", func(t *testing.T) {
		execs = nil
		if err := guardMain([]string{"--config", file, "--", "--version"}, execute, store); err != nil {
			t.Fatal(err)
		}
		want := []string{testQEMU, testQEMU, "--version"}
		if len(execs) != 1 || !slices.Equal(execs[0], want) {
			t.Fatalf("executed %q; want %q", execs, want)
		}
	})

	t.Run("ignores a configuration written into the Lima home", func(t *testing.T) {
		// The reviewer's case: the stable link, which the relays read,
		// repointed at a configuration naming another QEMU.
		hostile := filepath.Join(c.MCPDir(), "hostile.json")
		if err := os.MkdirAll(c.MCPDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		evil := `{"home":"/nonexistent","limaHome":"` + c.LimaHome + `","instance":"ngrok-agent","qemu":"/tmp/evil",
		"deniedReads":[],"connect":"192.168.5.100:3128","quic":"192.168.5.101:443"}`
		if err := os.WriteFile(hostile, []byte(evil), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(hostile, c.ConfigLink()); err != nil {
			t.Fatal(err)
		}
		execs = nil
		if err := guardMain([]string{"--config", file, "--", "--version"}, execute, store); err != nil {
			t.Fatal(err)
		}
		if len(execs) != 1 || execs[0][0] != testQEMU {
			t.Fatalf("executed %q", execs)
		}
		execs = nil
		if err := guardMain([]string{"--config", c.ConfigLink(), "--", "--version"}, execute, store); err == nil || len(execs) != 0 {
			t.Fatalf("a configuration outside the store: err %v, executed %q", err, execs)
		}
	})

	t.Run("refuses a configuration that links out of the store", func(t *testing.T) {
		link := filepath.Join(store, "linked.json")
		if err := os.Symlink(filepath.Join(c.MCPDir(), "hostile.json"), link); err != nil {
			t.Fatal(err)
		}
		execs = nil
		if err := guardMain([]string{"--config", link, "--", "--version"}, execute, store); err == nil || len(execs) != 0 {
			t.Fatalf("err %v, executed %q", err, execs)
		}
	})

	t.Run("executes nothing for a refused launch", func(t *testing.T) {
		execs = nil
		if err := guardMain([]string{"--config", file, "--", "-nic", "user"}, execute, store); err == nil || len(execs) != 0 {
			t.Fatalf("err %v, executed %q", err, execs)
		}
	})

	t.Run("refuses without its configuration", func(t *testing.T) {
		execs = nil
		if err := guardMain([]string{"--", "--version"}, execute, store); err == nil || len(execs) != 0 {
			t.Fatalf("err %v, executed %q", err, execs)
		}
	})
}
