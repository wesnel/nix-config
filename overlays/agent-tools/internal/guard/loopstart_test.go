package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A file tool cannot put a loop's variables into the settings a session
// takes its environment from, as a shell write to them cannot.
func TestLoopSettings(t *testing.T) {
	for _, c := range []struct {
		name  string
		deny  bool
		tool  string
		input map[string]string
	}{
		{"a project's settings", true, "Write", map[string]string{"file_path": "WORK/.claude/settings.local.json", "content": `{"env": {"PR_LOOP_MODE": "bots"}}`}},
		{"the user's settings", true, "Edit", map[string]string{"file_path": "HOME/.claude/settings.json", "old_string": `"env": {}`, "new_string": `"env": {"PR_LOOP_SESSION": "1"}`}},
		{"Codex's config", true, "Write", map[string]string{"file_path": "HOME/.codex/config.toml", "content": "[shell_environment_policy]\nset = { PR_LOOP_MODE = \"bots\" }\n"}},
		{"another setting", false, "Write", map[string]string{"file_path": "WORK/.claude/settings.local.json", "content": `{"env": {"GOFLAGS": "-mod=mod"}}`}},
		{"a README naming them", false, "Write", map[string]string{"file_path": "WORK/README.md", "content": "PR_LOOP_MODE is the operator's."}},
	} {
		t.Run(caseName(c.deny, c.name), func(t *testing.T) {
			h := fakeHome(t)
			work := filepath.Join(h, "work")
			data, _ := json.Marshal(c.input)
			input := []byte(strings.NewReplacer("HOME", h, "WORK", work).Replace(string(data)))
			reason := check(c.tool, input, baseline, work)
			if (reason != "") != c.deny {
				t.Errorf("%s %s: deny=%v, reason %q", c.tool, input, !c.deny, reason)
			}
			if c.deny && reason != loopStart {
				t.Errorf("%s: reason %q, want the loop-start reason", c.name, reason)
			}
		})
	}
}

// A harness started with the loop's variables is a loop session, however
// its command line hands them over: a Claude Code settings file or text, a
// Codex profile, or a Codex config override. Only the operator's launchers
// start one.
func TestLoopStartHarness(t *testing.T) {
	loopEnv := `{"env": {"PR_LOOP_MODE": "bots"}}`
	loopProfile := "[shell_environment_policy]\nset = { PR_LOOP_MODE = \"bots\" }\n"
	for _, c := range []struct {
		name    string
		deny    bool
		command string
	}{
		{"the launcher", true, "review-loop --harness codex --bots 12"},
		{"the launcher by path", true, "/nix/store/x/bin/review-loop --harness claude --people 12"},
		{"the Claude launcher", true, "claude-review-loop --bots 12"},
		{"a settings file", true, "claude --settings loop.json"},
		{"a settings file after =", true, "claude --settings=WORK/loop.json -p hi"},
		{"settings text", true, `claude --settings '{"env":{"PR_LOOP_SESSION":"1"}}'`},
		{"settings that expand", true, `claude --settings "$F"`},
		{"other settings", false, "claude --settings plain.json -p hi"},
		{"missing settings", false, "claude --settings missing.json"},
		{"a Codex profile", true, "codex -p loop"},
		{"a Codex profile after =", true, "codex --profile=loop exec hi"},
		{"a Codex profile joined", true, "codex -ploop"},
		{"a Codex profile in CODEX_HOME", true, "CODEX_HOME=OTHER codex --profile other"},
		{"another Codex profile", false, "codex -p plain"},
		{"a missing Codex profile", false, "codex -p absent"},
		{"a config override", true, `codex -c 'shell_environment_policy.set.PR_LOOP_MODE="bots"'`},
		{"a joined config override", true, `codex -c'shell_environment_policy.set.PR_LOOP_MODE="bots"'`},
		{"a long config override", true, `codex --config='shell_environment_policy.set={PR_LOOP_SESSION="1"}'`},
		{"another config override", false, `codex -c model=o3 exec 'say what PR_LOOP_MODE means'`},
		{"a prompt after --", false, `codex exec -- '-c PR_LOOP_MODE is the operators'`},
		{"Claude's -c", false, "claude -c 'explain PR_LOOP_MODE'"},
	} {
		t.Run(caseName(c.deny, c.name), func(t *testing.T) {
			h := fakeHome(t)
			work := filepath.Join(h, "work")
			other := filepath.Join(h, "other-codex")
			for path, text := range map[string]string{
				filepath.Join(work, "loop.json"):                loopEnv,
				filepath.Join(work, "plain.json"):               `{"env": {"GOFLAGS": "-mod=mod"}}`,
				filepath.Join(h, ".codex", "loop.config.toml"):  loopProfile,
				filepath.Join(h, ".codex", "plain.config.toml"): "model = \"o3\"\n",
				filepath.Join(other, "other.config.toml"):       loopProfile,
			} {
				os.MkdirAll(filepath.Dir(path), 0o755)
				if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("CODEX_HOME", "")
			command := strings.NewReplacer("WORK", work, "OTHER", other).Replace(c.command)
			reason := judge(t, work, command)
			if (reason != "") != c.deny {
				t.Errorf("%s: deny=%v, reason %q", command, !c.deny, reason)
			}
			if c.deny && reason != loopStart {
				t.Errorf("%s: reason %q, want the loop-start reason", command, reason)
			}
		})
	}
}

// The settings are found through links, and by any name the file system
// folds to theirs: in case, and in Unicode, which APFS folds as well.
func TestLoopSettingsAliases(t *testing.T) {
	h := fakeHome(t)
	work := filepath.Join(h, "work")
	os.MkdirAll(filepath.Join(work, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(h, ".codex"), 0o755)
	os.WriteFile(filepath.Join(work, ".claude", "settings.local.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(h, ".codex", "config.toml"), []byte(""), 0o644)
	os.Symlink(filepath.Join(work, ".claude", "settings.local.json"), filepath.Join(work, "cfg.json"))
	os.Symlink(filepath.Join(work, ".claude", "settings.json"), filepath.Join(work, "dangling.json"))
	os.Symlink(filepath.Join(work, ".claude"), filepath.Join(work, "dir"))
	os.Symlink(filepath.Join(h, ".codex", "config.toml"), filepath.Join(work, "codex.toml"))
	cases := map[string]bool{
		filepath.Join(work, "cfg.json"):                                  true,
		filepath.Join(work, "dangling.json"):                             true,
		filepath.Join(work, "dir", "settings.local.json"):                true,
		filepath.Join(work, "codex.toml"):                                true,
		filepath.Join(work, "x", "..", ".claude", "settings.local.json"): true,
		filepath.Join(work, "notes.json"):                                false,
		filepath.Join(work, ".Claude", "settings.local.json"):            true,
		filepath.Join(work, ".claude", "SETTINGS.local.json"):            true,
		filepath.Join(work, ".claude", "ſettings.local.json"):            true,
		filepath.Join(work, ".claude", "settings.local.jſon"):            true,
		filepath.Join(work, ".claude", "ſettings.json"):                  true,
		filepath.Join(work, "new", ".CLAUDE", "settings.json"):           true,
		filepath.Join(h, ".codex", "conﬁg.toml"):                         true,
		filepath.Join(work, ".claude", "notes.json"):                     false,
	}
	if runtime.GOOS == "darwin" {
		// A folded spelling of the directory itself, which only a folding
		// volume opens as the same one.
		cases[filepath.Join(h, ".Codex", "Config.toml")] = true
	}
	for path, deny := range cases {
		data, _ := json.Marshal(map[string]string{"file_path": path, "content": `{"env": {"PR_LOOP_MODE": "bots"}}`})
		if reason := check("Write", data, baseline, work); (reason == loopStart) != deny {
			t.Errorf("Write %s: reason %q, want deny=%v", path, reason, deny)
		}
	}
}

// A hard link to the settings is one of them at any depth, and outside the
// directory the session runs in.
func TestLoopSettingsHardLinks(t *testing.T) {
	h := fakeHome(t)
	work := filepath.Join(h, "work")
	elsewhere := t.TempDir()
	os.MkdirAll(filepath.Join(work, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(work, "x", "y"), 0o755)
	settings := filepath.Join(work, ".claude", "settings.local.json")
	os.WriteFile(settings, []byte("{}"), 0o644)
	plain := filepath.Join(work, "x", "notes.json")
	os.WriteFile(plain, []byte("{}"), 0o644)
	cases := map[string]bool{plain: false}
	for _, p := range []string{filepath.Join(work, "hard.json"), filepath.Join(work, "x", "hard.json"),
		filepath.Join(work, "x", "y", "hard.json"), filepath.Join(elsewhere, "hard.json")} {
		if err := os.Link(settings, p); err != nil {
			t.Fatal(err)
		}
		cases[p] = true
	}
	for path, deny := range cases {
		data, _ := json.Marshal(map[string]string{"file_path": path, "content": `{"env": {"PR_LOOP_MODE": "bots"}}`})
		if reason := check("Write", data, baseline, work); (reason == loopStart) != deny {
			t.Errorf("Write %s: reason %q, want deny=%v", path, reason, deny)
		}
	}
}
