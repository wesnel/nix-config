package main

import (
	"errors"
	"os"
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
