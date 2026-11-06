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
	t.Parallel()
	ctx, root := fakeHome(t)
	scratch := filepath.Join(root, "scratch")
	ctx = withVars(ctx, "TMPDIR="+scratch)
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
	ctx = withVars(ctx, "AGENT_TOOLS_CONFIG="+config)
	for _, test := range []struct {
		name, dir string
		deny      bool
	}{
		{"reviewer outside scratch", outside, true},
		{"reviewer in scratch", scratch, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data, _ := json.Marshal(map[string]any{"tool_name": "Bash", "agent_id": "reviewer", "cwd": test.dir, "tool_input": map[string]string{"command": "trash-scratch new"}})
			var out bytes.Buffer
			mainIn(ctx, []string{"--reviewer"}, bytes.NewReader(data), &out, io.Discard)
			if denied := strings.Contains(out.String(), `"permissionDecision":"deny"`); denied != test.deny {
				t.Errorf("denied %v, want %v: %s", denied, test.deny, out.String())
			}
		})
	}
}

func TestTrashAdvice(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			config := filepath.Join(t.TempDir(), "config.json")
			settings := `{"guard":{}}`
			if test.configured {
				settings = `{"guard":{"excludedCommands":["trash-scratch *"]}}`
			}
			if err := os.WriteFile(config, []byte(settings), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx := withVars(guardCtx(t), "AGENT_TOOLS_CONFIG="+config)
			if test.onPath {
				ctx = withTrash(t, ctx)
			}
			reason := judge(ctx, t.TempDir(), "rm new")
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
