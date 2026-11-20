package review

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/redcheck"
)

func TestCheckEvidence(t *testing.T) {
	f := setUp(t, true)
	writeSettings(t, f, `{"checks":[{"name":"lint","command":"false"}]}`)
	r, err := resolve(f.repo, f.base+".."+f.tip)
	if err != nil {
		t.Fatal(err)
	}
	defer r.release()
	for _, tc := range []struct {
		name    string
		edit    func(map[string]any)
		missing bool
		want    string
	}{
		{name: "completed failure accepted"},
		{name: "missing evidence", missing: true, want: "git -C"},
		{name: "wrong base", edit: func(m map[string]any) { m["base"] = f.tip }, want: "endpoints"},
		{name: "wrong tip", edit: func(m map[string]any) { m["tip"] = f.base }, want: "endpoints"},
		{name: "missing checks", edit: func(m map[string]any) { m["checks"] = []any{} }, want: "configured"},
		{name: "changed command", edit: func(m map[string]any) { m["checks"].([]any)[0].(map[string]any)["command"] = "true" }, want: "configured"},
		{name: "unknown name", edit: func(m map[string]any) { m["checks"].([]any)[0].(map[string]any)["name"] = "other" }, want: "configured"},
		{name: "extra duplicate", edit: func(m map[string]any) { m["checks"] = append(m["checks"].([]any), m["checks"].([]any)[0]) }, want: "configured"},
		{name: "duplicate names", edit: func(m map[string]any) { m["checks"] = append(m["checks"].([]any), m["checks"].([]any)[0]) }, want: "configured"},
		{name: "missing run", edit: func(m map[string]any) { delete(m["checks"].([]any)[0].(map[string]any), "run") }, want: "complete"},
		{name: "missing exit", edit: func(m map[string]any) {
			delete(m["checks"].([]any)[0].(map[string]any)["run"].(map[string]any), "exit")
		}, want: "complete"},
		{name: "signal exit", edit: func(m map[string]any) { m["checks"].([]any)[0].(map[string]any)["run"].(map[string]any)["exit"] = -1 }, want: "complete"},
		{name: "missing output", edit: func(m map[string]any) {
			delete(m["checks"].([]any)[0].(map[string]any)["run"].(map[string]any), "output")
		}, want: "complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeSettings(t, f, `{"checks":[{"name":"lint","command":"false"}]}`)
			if tc.name == "duplicate names" {
				writeSettings(t, f, `{"checks":[{"name":"lint","command":"false"},{"name":"build","command":"true"}]}`)
			}
			envelope := map[string]any{"base": f.base, "tip": f.tip, "checks": []any{map[string]any{"name": "lint", "command": "false", "run": map[string]any{"exit": 7, "output": "failed"}}}}
			if tc.edit != nil {
				tc.edit(envelope)
			}
			data, _ := json.Marshal(map[string]any{"suites": []any{}, "commits": []any{}, "tipChecks": envelope})
			file := "evidence.json"
			if tc.missing {
				file = ""
				data = nil
			}
			a, err := testAdequacy(r, file, data, io.Discard)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q refusal, got %+v, %v", tc.want, a, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("completed failing check refused: %v", err)
			}
		})
	}
}

func TestCheckSettingsReadFailure(t *testing.T) {
	f := setUp(t, true)
	writeSettings(t, f, `{"checks":[{"name":"lint","command":"true"}]}`)
	r, err := resolve(f.repo, f.base+".."+f.tip)
	if err != nil {
		t.Fatal(err)
	}
	defer r.release()
	if err := usableSettings(r); err != nil {
		t.Fatal(err)
	}
	writeSettings(t, f, `{"checks":`)
	if a, err := testAdequacy(r, "", nil, io.Discard); err == nil {
		t.Fatalf("settings read failure bypassed required checks: %+v", a)
	}
}

func TestCheckHandoffIntegration(t *testing.T) {
	for _, withChecks := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "session"}[withChecks], func(t *testing.T) {
			f := setUpAdequacy(t, redcheck.FailsAsAsserted)
			writeSettings(t, f.fixture, `{}`)
			args := []string{"--reviewer", "codex", "--plan", f.plan}
			state := "absent"
			if withChecks {
				writeSettings(t, f.fixture, `{"checks":[{"name":"lint","command":"echo tested; exit 7"}]}`)
				opts := redcheck.Options{Dir: f.repo}
				if err := json.Unmarshal([]byte(`{"Checks":[{"name":"lint","command":"echo tested; exit 7"}]}`), &opts); err != nil {
					t.Fatal(err)
				}
				result, err := redcheck.Do(t.Context(), f.base+".."+f.tip, opts)
				if err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(f.repo, "evidence.json")
				if err := os.WriteFile(file, must(json.Marshal(result)), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--red-check", file)
				state = "session"
			}
			code, out, stderr := runDelegate(t, append(args, f.base+".."+f.tip)...)
			if code != 0 {
				t.Fatalf("review exit %d: %s", code, stderr)
			}
			rec, err := record.ReadRange(out.Record)
			if err != nil {
				t.Fatal(err)
			}
			if len(rec.Rounds) != 1 || !strings.Contains(string(must(json.Marshal(rec.Rounds[0]))), `"TipChecks":"`+state+`"`) || rec.Rounds[0].RedCheck != "absent" {
				t.Fatalf("record presence: %+v", rec.Rounds)
			}
			if withChecks {
				prompt, err := os.ReadFile(filepath.Join(f.log, "prompt"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(prompt), "lint: exit 7") || !strings.Contains(string(prompt), "tested") {
					t.Fatal("actual reviewer missed check evidence")
				}
				t.Setenv("FAKE_CODEX", "critique")
				t.Setenv("FAKE_CHECKED", `["N1","N2","E1"]`)
				f.claudeSays(t, "diagrammer", diagramsReply(codeDiagram(f.tip, 3)))
				diagramArgs := append([]string{}, args...)
				diagramArgs[1] = "diagram"
				if code, _, stderr := runDelegate(t, append(diagramArgs, f.base+".."+f.tip)...); code != 0 {
					t.Fatalf("diagram: %d %s", code, stderr)
				}
				critic := string(must(os.ReadFile(filepath.Join(f.log, "prompt"))))
				drawer := claudeArgs(t, f.fixture, "diagrammer", 1)
				if !strings.Contains(critic, "lint: exit 7") || !strings.Contains(drawer[len(drawer)-1], "lint: exit 7") {
					t.Fatal("actual drawing or critique missed check evidence")
				}
				if code, _, stderr := delegateCmd(t, "availability", "--disable", "codex"); code != 0 {
					t.Fatalf("disable: %d %s", code, stderr)
				}
				code, _, stderr = runDelegate(t, append(args, f.base+".."+f.tip)...)
				if code != 4 || !strings.Contains(stderr, "--red-check "+shellWord(filepath.Join(f.repo, "evidence.json"))) {
					t.Fatalf("fallback lost evidence: exit %d, %s", code, stderr)
				}
			}
		})
	}
}

func TestCheckRoundOutput(t *testing.T) {
	f := setUp(t, true)
	r, err := resolve(f.repo, f.base+".."+f.tip)
	if err != nil {
		t.Fatal(err)
	}
	defer r.release()
	for _, state := range []string{"session", "absent"} {
		t.Run(state, func(t *testing.T) {
			rec := &record.Range{}
			// The completed run's fields are supplied as JSON so this test
			// also exercises the public round output without a new-field dependency.
			var rd record.Round
			json.Unmarshal([]byte(`{"ID":"R1","RedCheck":"absent","TipChecks":"`+state+`"}`), &rd)
			rec.Rounds = []record.Round{rd}
			data, _ := json.Marshal(showRange("record.org", rec))
			if !strings.Contains(string(data), `"tipChecks":"`+state+`"`) {
				t.Fatalf("public round lost evidence: %s", data)
			}
		})
	}
	rec := &record.Range{}
	appendRuns(rec, []*typeRun{{name: "codex"}}, time.Now(), nil, nil)
	data, _ := json.Marshal(showRange("record.org", rec))
	if !strings.Contains(string(data), `"tipChecks":"absent"`) {
		t.Fatalf("new round lacks explicit absence: %s", data)
	}
}

func TestCheckPrompts(t *testing.T) {
	f := setUp(t, true)
	writeSettings(t, f, `{"checks":[{"name":"lint","command":"false"},{"name":"build","command":"true"}]}`)
	r, err := resolve(f.repo, f.base+".."+f.tip)
	if err != nil {
		t.Fatal(err)
	}
	defer r.release()
	data, _ := json.Marshal(map[string]any{"suites": []any{}, "commits": []any{}, "tipChecks": map[string]any{"base": f.base, "tip": f.tip, "checks": []any{map[string]any{"name": "lint", "command": "false", "run": map[string]any{"exit": 7, "output": strings.Repeat(".", 20000) + "\npassword=concealed-value"}}, map[string]any{"name": "build", "command": "true", "run": map[string]any{"exit": 0, "output": "built"}}}}})
	a, err := testAdequacy(r, "evidence.json", data, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for name := range reviewerTypes {
		t.Run(name, func(t *testing.T) {
			in, err := typeInputs(t.Context(), inputs{r: r, description: "## Reverting\nRevert the commit.", descriptionFrom: "test description", printed: true}, name, a, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			text := prompt(in)
			if reviewerTypes[name].diagram {
				text = diagramFallback(in)
				_, critic, ok := strings.Cut(text, "# Critique task")
				if !ok || !strings.Contains(critic, "lint: exit 7") {
					t.Fatal("fallback critic missed check statuses")
				}
			}
			if !strings.Contains(text, "lint: exit 7") || !strings.Contains(text, "build: exit 0") || !strings.Contains(text, "REDACTED") || !strings.Contains(text, strings.Repeat(".", 100)) || strings.Contains(text, "concealed-value") || strings.Contains(text, strings.Repeat(".", 17000)) {
				t.Fatalf("missing status or unsafe logs in %s prompt", name)
			}
		})
	}
}
