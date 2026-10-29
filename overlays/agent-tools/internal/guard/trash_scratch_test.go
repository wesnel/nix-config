package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrashScratch(t *testing.T) {
	root := fakeHome(t)
	scratch := filepath.Join(root, "scratch")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{scratch, outside} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte(`{"plans":{"dir":"`+filepath.Join(root, "plans")+`","host":"box"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	old := scratchRoots
	scratchRoots = func() []string { return []string{scratch} }
	t.Cleanup(func() { scratchRoots = old })
	for _, test := range []struct {
		name, dir string
		deny      bool
	}{
		{"reviewer outside scratch", outside, true},
		{"reviewer in scratch", scratch, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, _ := json.Marshal(map[string]any{"tool_name": "Bash", "agent_id": "reviewer", "cwd": test.dir, "tool_input": map[string]string{"command": "trash-scratch new"}})
			var out bytes.Buffer
			Main([]string{"--reviewer"}, bytes.NewReader(data), &out, io.Discard)
			if denied := strings.Contains(out.String(), `"permissionDecision":"deny"`); denied != test.deny {
				t.Errorf("denied %v, want %v: %s", denied, test.deny, out.String())
			}
		})
	}
}

func TestTrashAdvice(t *testing.T) {
	for _, test := range []struct {
		name               string
		configured, onPath bool
		want               string
	}{
		{"configured wrapper", true, true, "trash-scratch"},
		{"configured wrapper without ordinary trash", true, false, "trash-scratch"},
		{"ordinary trash", false, true, "trash PATH"},
		{"no recoverable route", false, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "config.json")
			settings := `{"guard":{}}`
			if test.configured {
				settings = `{"guard":{"excludedCommands":["trash-scratch *"]}}`
			}
			if err := os.WriteFile(config, []byte(settings), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENT_TOOLS_CONFIG", config)
			old := trashOnPath
			trashOnPath = func() bool { return test.onPath }
			t.Cleanup(func() { trashOnPath = old })
			reason := judge(t, t.TempDir(), "rm new")
			if test.want == "" {
				if reason != "" {
					t.Errorf("unexpected refusal: %s", reason)
				}
			} else if !strings.Contains(reason, test.want) || !saysInstead(reason) {
				t.Errorf("refusal does not name configured recovery %q: %s", test.want, reason)
			}
		})
	}
}
