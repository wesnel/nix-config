package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/limaimport"
)

// guestHome makes the fake limactl run the guest's commands on this host,
// with a directory of the test's as the guest user's home, which it
// returns: the test cannot boot a VM.
func (v *vm) guestHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(v.cfg.LimaHome, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"home": dir, "path": os.Getenv("PATH")})
	if err := os.WriteFile(filepath.Join(v.cfg.LimaHome, "fake-guest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// repository makes a repository with an origin/main, a commit beyond it
// and an untracked file, and returns it.
func repository(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q")
	run("commit", "-q", "--allow-empty", "-m", "first")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-q", "--allow-empty", "-m", "second")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// noCredentials points the guard at credential paths of the test's own.
func noCredentials(t *testing.T) {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{"guard":{"credentialPaths":["~/.aws/"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_TOOLS_CONFIG", config)
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

func TestManagedImport(t *testing.T) {
	t.Run("carries the workspace into the attested guest", func(t *testing.T) {
		noCredentials(t)
		v := newVM(t)
		home := v.guestHome(t)
		workdir := filepath.Join(home, v.cfg.Workdir)
		if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		src := repository(t, map[string]string{"tools/ngrok/run.go": "package main\n"})
		if err := limaimport.Export(t.Context(), "git", src, v.cfg.Outbox, "work"); err != nil {
			t.Fatal(err)
		}
		r := v.run(t, "lima_dev_import", `{"name":"work"}`)
		if r.Status != "succeeded" || r.ExitCode == nil || *r.ExitCode != 0 {
			t.Fatalf("import: %+v", r)
		}
		head := headOf(t, src)
		if got := headOf(t, workdir); got != head {
			t.Errorf("guest HEAD %s, want the source's %s", got, head)
		}
		if !strings.Contains(r.Output, head) {
			t.Errorf("the job's output does not name HEAD %s: %q", head, r.Output)
		}
		if data, err := os.ReadFile(filepath.Join(workdir, "notes.txt")); err != nil || string(data) != "untracked\n" {
			t.Errorf("the guest's untracked notes.txt: %q, %v", data, err)
		}
		calls := v.calls(t)
		if !strings.Contains(calls, "copy "+v.cfg.Imports()+"/") || !strings.Contains(calls, " ngrok-agent:"+home+"/.lima-import-") {
			t.Errorf("the bundle was not copied from the private copy into the guest:\n%s", calls)
		}
		if entries, err := os.ReadDir(v.cfg.Imports()); err != nil || len(entries) != 0 {
			t.Errorf("the private copy is left behind: %v, %v", entries, err)
		}
	})

	t.Run("refuses a credential before the guest sees it", func(t *testing.T) {
		noCredentials(t)
		v := newVM(t)
		v.guestHome(t)
		if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		src := repository(t, map[string]string{"deploy/.aws/credentials": "[default]\n"})
		if err := limaimport.Export(t.Context(), "git", src, v.cfg.Outbox, "work"); err != nil {
			t.Fatal(err)
		}
		before := v.calls(t)
		r := v.run(t, "lima_dev_import", `{"name":"work"}`)
		if r.Status != "failed" || !strings.Contains(r.Error, "deploy/.aws/credentials") {
			t.Fatalf("import: %+v, want a failure naming the credential", r)
		}
		if after := strings.TrimPrefix(v.calls(t), before); strings.Contains(after, "copy ") || strings.Contains(after, "shell ") {
			t.Errorf("the bundle reached the guest:\n%s", after)
		}
	})

	t.Run("refuses while the VM does not attest", func(t *testing.T) {
		noCredentials(t)
		v := newVM(t)
		src := repository(t, nil)
		if err := limaimport.Export(t.Context(), "git", src, v.cfg.Outbox, "work"); err != nil {
			t.Fatal(err)
		}
		r := v.run(t, "lima_dev_import", `{"name":"work"}`)
		if r.Status != "failed" || !strings.Contains(r.Error, "absent") {
			t.Fatalf("import: %+v, want a failure naming the VM's state", r)
		}
	})

	// Its Lima home is under the test's directory, whose path the
	// configuration takes only of letters, digits and ._/+-, so the test's
	// name holds no other.
	t.Run("refuses a bad name", func(t *testing.T) {
		v := newVM(t)
		for _, args := range []string{`{"name":"../work"}`, `{"name":"Work"}`, `{}`, `{"name":"work","extra":1}`} {
			text, failed := v.tool(t, "lima_dev_import", args)
			if !failed || !strings.Contains(text, "invalid arguments") {
				t.Errorf("lima_dev_import %s: %q, failed=%v; want invalid arguments", args, text, failed)
			}
		}
	})

	// The test holds the workdir as a running guest command does, rather
	// than race a real one to it.
	t.Run("refuses while a guest command runs", func(t *testing.T) {
		noCredentials(t)
		v := newVM(t)
		v.guestHome(t)
		if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		if err := limaimport.Export(t.Context(), "git", repository(t, nil), v.cfg.Outbox, "work"); err != nil {
			t.Fatal(err)
		}
		v.s.managed.workdir.RLock()
		before := v.calls(t)
		r := v.run(t, "lima_dev_import", `{"name":"work"}`)
		v.s.managed.workdir.RUnlock()
		if r.Status != "failed" || !strings.Contains(r.Error, "running in the guest") {
			t.Fatalf("import: %+v, want a failure saying the guest is busy", r)
		}
		if after := strings.TrimPrefix(v.calls(t), before); strings.Contains(after, "copy ") {
			t.Errorf("the bundle reached the guest:\n%s", after)
		}
		if r := v.run(t, "lima_dev_import", `{"name":"work"}`); r.Status != "succeeded" {
			t.Errorf("import once the command ended: %+v", r)
		}
	})

	t.Run("refuses a guest command beside an import", func(t *testing.T) {
		v := newVM(t)
		if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		v.s.managed.workdir.Lock()
		r := v.run(t, "lima_dev_exec_start", `{"script":"echo hi"}`)
		v.s.managed.workdir.Unlock()
		if r.Status != "failed" || !strings.Contains(r.Error, "import is running") {
			t.Fatalf("exec: %+v, want a failure saying an import runs", r)
		}
	})

	// A post-checkout hook in the guest moves its branch, so the import
	// ends somewhere other than the bundle's HEAD.
	t.Run("fails when the guest does not end at the bundle's HEAD", func(t *testing.T) {
		noCredentials(t)
		v := newVM(t)
		home := v.guestHome(t)
		if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		src := repository(t, map[string]string{"README": "one\n"})
		if err := limaimport.Export(t.Context(), "git", src, v.cfg.Outbox, "work"); err != nil {
			t.Fatal(err)
		}
		if r := v.run(t, "lima_dev_import", `{"name":"work"}`); r.Status != "succeeded" {
			t.Fatalf("first import: %+v", r)
		}
		hook := filepath.Join(home, v.cfg.Workdir, ".git", "hooks", "post-checkout")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\ngit update-ref refs/heads/work refs/remotes/import/main\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		r := v.run(t, "lima_dev_import", `{"name":"work"}`)
		if r.Status != "failed" || !strings.Contains(r.Error, headOf(t, src)) {
			t.Fatalf("import: %+v, want a failure naming the bundle's HEAD", r)
		}
	})
}
