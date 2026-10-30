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
		got, err := suites("", "", nil, lookup)
		if err != nil || !reflect.DeepEqual(got, configured) {
			t.Errorf("%+v, %v", got, err)
		}
	})

	t.Run("the settings' error without flags", func(t *testing.T) {
		if got, err := suites("", "", nil, broken); err == nil {
			t.Errorf("settings that do not load gave %+v", got)
		}
	})

	// The flags' suite replaces the configured ones, and needs no settings.
	t.Run("the flags' suite in place of the configured ones", func(t *testing.T) {
		want := []redcheck.Suite{{Name: "flags", Test: "sh t.sh", Build: "sh -n t.sh", TestFiles: []string{"t.sh"}}}
		for name, l := range map[string]func() (*repos.Settings, error){"with settings": lookup, "without": broken} {
			got, err := suites("sh t.sh", "sh -n t.sh", []string{"t.sh"}, l)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("%s: %+v, %v", name, got, err)
			}
		}
	})

	for _, c := range []struct {
		name, test, build string
		testFiles         []string
	}{
		{"--test alone", "go test ./...", "", nil},
		{"--test-files alone", "", "", []string{"*_test.go"}},
		{"--build alone", "", "go vet ./...", nil},
		{"--test and --build", "go test ./...", "go vet ./...", nil},
	} {
		t.Run("refused: "+c.name, func(t *testing.T) {
			got, err := suites(c.test, c.build, c.testFiles, lookup)
			var r refusal.Refusal
			if !errors.As(err, &r) || !refusal.SaysInstead(err.Error()) {
				t.Errorf("%+v, %v", got, err)
			}
		})
	}
}
