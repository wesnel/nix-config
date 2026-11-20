package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/redcheck"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/repos"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

// configured stands in for repos.Lookup, whose settings file a sandboxed
// test run may not write; internal/repos's tests read the real file.
var configured = []redcheck.Suite{{Name: "go", Test: "go test ./...", TestFiles: []string{"**/*_test.go"}}}

func lookup() (*repos.Settings, error) { return &repos.Settings{Suites: configured}, nil }

// broken is a lookup whose settings do not load.
func broken() (*repos.Settings, error) { return nil, errors.New("the settings do not load") }

func TestSuites(t *testing.T) {
	t.Run("the configured suites without flags", func(t *testing.T) {
		got, err := args{}.suites(lookup)
		if err != nil || !reflect.DeepEqual(got, configured) {
			t.Errorf("%+v, %v", got, err)
		}
	})

	t.Run("the settings' error without flags", func(t *testing.T) {
		if got, err := (args{}).suites(broken); err == nil {
			t.Errorf("settings that do not load gave %+v", got)
		}
	})

	// The flags' suite replaces the configured ones, and needs no settings.
	t.Run("the flags' suite in place of the configured ones", func(t *testing.T) {
		for _, c := range []struct {
			name string
			a    args
			want redcheck.Suite
		}{
			{"test files", args{test: "sh t.sh", build: "sh -n t.sh", testFiles: globs{"t.sh"}},
				redcheck.Suite{Name: "flags", Test: "sh t.sh", Build: "sh -n t.sh", TestFiles: []string{"t.sh"}}},
			{"inline test files", args{test: "cargo test", inlineFiles: globs{"src/*.rs"}},
				redcheck.Suite{Name: "flags", Test: "cargo test", InlineTestFiles: []string{"src/*.rs"}}},
		} {
			for name, l := range map[string]func() (*repos.Settings, error){"with settings": lookup, "without": broken} {
				got, err := c.a.suites(l)
				if err != nil || len(got) != 1 || !got[0].Equal(c.want) {
					t.Errorf("%s, %s: %+v, %v", c.name, name, got, err)
				}
			}
		}
	})

	for _, c := range []struct {
		name string
		a    args
	}{
		{"--test alone", args{test: "go test ./..."}},
		{"--test-files alone", args{testFiles: globs{"*_test.go"}}},
		{"--inline-test-files alone", args{inlineFiles: globs{"src/*.rs"}}},
		{"--build alone", args{build: "go vet ./..."}},
		{"--test and --build", args{test: "go test ./...", build: "go vet ./..."}},
	} {
		t.Run("refused: "+c.name, func(t *testing.T) {
			got, err := c.a.suites(lookup)
			var r refusal.Refusal
			if !errors.As(err, &r) || !refusal.SaysInstead(err.Error()) {
				t.Errorf("%+v, %v", got, err)
			}
		})
	}
}

func TestCLIConfiguredChecks(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "git-red-check")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	git("init", "-q")
	git("-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base")
	git("-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "tip")
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "agents.json"), []byte(`{"checks":[{"name":"lint","command":"echo named-result; exit 7"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{"HEAD^..HEAD"}, {"--test", "true", "--test-files", "*.test", "HEAD^..HEAD"}} {
		cmd := exec.Command(bin, argv...)
		cmd.Dir = dir
		out, err := cmd.Output()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 {
			t.Fatalf("want exit 3: %v, %s", err, out)
		}
		var got struct {
			TipChecks struct {
				Checks []struct {
					Name string
					Run  struct {
						Exit   int
						Output string
					}
				}
			}
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.TipChecks.Checks) != 1 || got.TipChecks.Checks[0].Run.Exit != 7 || got.TipChecks.Checks[0].Run.Output != "named-result" {
			t.Fatalf("flags bypassed checks: %s", out)
		}
	}
}
