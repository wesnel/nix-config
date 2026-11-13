package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/redcheck"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

func TestConfiguredRust(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("init: %v: %s", err, out)
	}
	path := filepath.Join(dir, ".git", "info", "agents.json")
	t.Run("without settings", func(t *testing.T) {
		opts, err := configuredOptions(redcheck.Options{Test: "true", RustTestFiles: []string{"session.rs"}})
		if err != nil || opts.Test != "true" || !slices.Equal(opts.RustTestFiles, []string{"session.rs"}) {
			t.Fatalf("explicit Rust settings without file: %+v, %v", opts, err)
		}
	})
	if err := os.WriteFile(path, []byte(`{"test":"cargo test --offline","build":"cargo test --no-run --offline","rustTestFiles":["src/**/*.rs"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		flags, want []string
	}{
		{"configured", nil, []string{"src/**/*.rs"}},
		{"explicit", []string{"session.rs"}, []string{"session.rs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := configuredOptions(redcheck.Options{RustTestFiles: tc.flags})
			if err != nil || !slices.Equal(opts.RustTestFiles, tc.want) || opts.Test != "cargo test --offline" || opts.Build != "cargo test --no-run --offline" {
				t.Fatalf("options %+v, error %v", opts, err)
			}
		})
	}
	t.Run("explicit Rust overrides unusable settings", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("not JSON"), 0o600); err != nil {
			t.Fatal(err)
		}
		opts, err := configuredOptions(redcheck.Options{Test: "true", RustTestFiles: []string{"session.rs"}})
		if err != nil || opts.Test != "true" || !slices.Equal(opts.RustTestFiles, []string{"session.rs"}) {
			t.Fatalf("explicit Rust settings with malformed file: %+v, %v", opts, err)
		}
	})
}
