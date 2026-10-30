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

func TestSuites(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("init: %v: %s", err, out)
	}
	path := filepath.Join(dir, ".git", "info", "agents.json")
	flagSuite := []redcheck.Suite{{Name: "flags", Test: "true", InlineTestFiles: []string{"session.rs"}}}
	t.Run("flags without settings", func(t *testing.T) {
		suites, err := args{test: "true", inlineFiles: globs{"session.rs"}}.suites()
		if err != nil || !slices.EqualFunc(suites, flagSuite, redcheck.Suite.Equal) {
			t.Fatalf("suites %+v, error %v", suites, err)
		}
	})
	t.Run("inline test files without a test command", func(t *testing.T) {
		if suites, err := (args{inlineFiles: globs{"session.rs"}}).suites(); err == nil {
			t.Fatalf("suites %+v, want a refusal", suites)
		}
	})
	if err := os.WriteFile(path, []byte(`{"test":"cargo test --offline","build":"cargo test --no-run --offline","rustTestFiles":["src/**/*.rs"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("configured", func(t *testing.T) {
		want := []redcheck.Suite{{Name: "default", Test: "cargo test --offline", Build: "cargo test --no-run --offline", InlineTestFiles: []string{"src/**/*.rs"}}}
		suites, err := args{}.suites()
		if err != nil || !slices.EqualFunc(suites, want, redcheck.Suite.Equal) {
			t.Fatalf("suites %+v, error %v", suites, err)
		}
	})
	t.Run("flags in place of unusable settings", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("not JSON"), 0o600); err != nil {
			t.Fatal(err)
		}
		suites, err := args{test: "true", inlineFiles: globs{"session.rs"}}.suites()
		if err != nil || !slices.EqualFunc(suites, flagSuite, redcheck.Suite.Equal) {
			t.Fatalf("suites %+v, error %v", suites, err)
		}
	})
}
