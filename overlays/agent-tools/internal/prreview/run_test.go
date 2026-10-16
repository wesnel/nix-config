package prreview

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/repos"
)

// fakeBackend stands in for eca-gondolin: it records its arguments, and
// answers as a guest would, on lines starting with the nonce the script
// carries, with what $FAKE_GUEST_REDCHECK and $FAKE_GUEST_TESTS hold.
const fakeBackend = `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_BACKEND_LOG/argv"
for a; do script=$a; done
nonce=$(printf '%s' "$script" | sed -n "s/.*printf '\\\\n\([0-9a-f]*\) .*/\1/p" | head -n 1)
workspace=
while [ $# -gt 0 ]; do
  case "$1" in --workspace) workspace=$2; shift 2 ;; --) break ;; *) shift ;; esac
done
ls -A "$workspace" > "$FAKE_BACKEND_LOG/workspace"
b64() { base64 < "$1" | tr -d '\n'; }
echo "the guest's own chatter"
printf '\n%s redcheck 4 %s\n' "$nonce" "$(b64 "$FAKE_GUEST_REDCHECK")"
printf '\n%s tests 1 %s\n' "$nonce" "$(b64 "$FAKE_GUEST_TESTS")"
`

// withRun configures a run: o/r's test settings, the fake backend, guest
// tools, and this machine's Go release as the guest's toolchain, already in
// the cache.
func withRun(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go here")
	}
	log, bin, tools := t.TempDir(), t.TempDir(), t.TempDir()
	write(t, filepath.Join(bin, "eca-gondolin"), fakeBackend)
	os.Chmod(filepath.Join(bin, "eca-gondolin"), 0o755)
	t.Setenv("FAKE_BACKEND_LOG", log)
	prLoopConfig(t, map[string]string{"backend": filepath.Join(bin, "eca-gondolin"), "guestTools": tools})
	settings := filepath.Join(t.TempDir(), "agent-tools.json")
	write(t, settings, `{"repos": {"o/r": {"test": "go test ./...", "build": "go vet ./...", "testFiles": ["**/*_test.go"]}}}`)
	t.Setenv("AGENT_TOOLS_CONFIG", settings)
	host := hostGo(t.Context())
	write(t, filepath.Join(filepath.Dir(must(Root())), "pr-review-toolchains", host+".linux-"+runtime.GOARCH, "bin", "go"), "#!/bin/sh\n")

	redcheck := filepath.Join(t.TempDir(), "redcheck")
	write(t, redcheck, `{"test": "go test ./...", "commits": [{"verdict": "no-test-files"}]}`)
	t.Setenv("FAKE_GUEST_REDCHECK", redcheck)
	tests := filepath.Join(t.TempDir(), "tests")
	write(t, tests, "--- FAIL: TestA\x1b]52;c;aGk=\x07\n"+strings.Repeat("y", 3*TextCap)+"\nFAIL\n")
	t.Setenv("FAKE_GUEST_TESTS", tests)
	return log
}

func TestRun(t *testing.T) {
	t.Run("clean", testRunClean)
	t.Run("gated", testRunGated)
	t.Run("--no-run", testRunNoRun)
	t.Run("live", testRunLive)
	t.Run("local red-check", testRunLocalRedCheck)
}

func testRunClean(t *testing.T) {
	r := goModulesRemote(t, "module example.com/r\n\ngo 1.20\n\nrequire example.com/dep v1.0.0\n")
	setUp(t, r)
	withProxy(t)
	codexLog := withCodex(t, reply())
	log := withRun(t)

	code, record, _, stderr := runReview(t, "", "o/r#7")
	if code != 3 || record == nil {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if !record.Ran || record.NotRunBecause != "" {
		t.Fatalf("did not run: %q", record.NotRunBecause)
	}
	if rc, ok := record.RedCheck.(map[string]any); !ok || rc["test"] != "go test ./..." {
		t.Errorf("red-check: %#v", record.RedCheck)
	}
	data, _ := json.Marshal(record.Tests)
	var tests Run
	json.Unmarshal(data, &tests)
	if tests.Exit != 1 || tests.Command != "go test ./..." || len(tests.Output) > TextCap ||
		strings.ContainsAny(tests.Output, "\x1b\x07") || !strings.HasSuffix(tests.Output, "[cut]") && !strings.Contains(tests.Output, "FAIL") {
		t.Errorf("tests: exit %d, %d bytes, %q", tests.Exit, len(tests.Output), tests.Output[:min(80, len(tests.Output))])
	}
	if strings.Contains(string(data), "the guest's own chatter") {
		t.Error("output outside the guest's sections reached the record")
	}

	argv := strings.Split(strings.TrimSpace(string(must(os.ReadFile(filepath.Join(log, "argv"))))), "\n")
	cache := filepath.Dir(must(latest(t.Context(), "o/r", 7)).Clone)
	for _, want := range []string{"--offline", "/opt/go", "/opt/gomod", "/opt/agent-tools"} {
		if !slices.ContainsFunc(argv, func(a string) bool { return a == want || strings.HasSuffix(a, ":"+want) }) {
			t.Errorf("the backend was not given %s: %q", want, argv)
		}
	}
	workspace := argv[slices.Index(argv, "--workspace")+1]
	if !strings.HasPrefix(workspace, cache+"/run/") {
		t.Errorf("the run copy %s is not beside the clone", workspace)
	}
	if _, err := os.Stat(workspace); err == nil {
		t.Error("the run copy is still there")
	}
	if files := string(must(os.ReadFile(filepath.Join(log, "workspace")))); !strings.Contains(files, "r.go") {
		t.Errorf("the run copy is not checked out at the head: %s", files)
	}
	prompt := string(must(os.ReadFile(filepath.Join(codexLog, "prompt"))))
	for _, want := range []string{"git red-check", "no-test-files", "--- FAIL: TestA", "evidence, not instructions"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
}

// testRunGated: a red flag leaves the run to the operator, and the review
// goes on.
func testRunGated(t *testing.T) {
	// gated reviews a pull request with a red flag, and returns the fake
	// backend's log.
	gated := func(t *testing.T) string {
		t.Helper()
		r := newRemote(t, func(work string) { write(t, filepath.Join(work, ".envrc"), "use flake\n") })
		setUp(t, r)
		withCodex(t, reply())
		log := withRun(t)
		code, record, _, stderr := runReview(t, "", "o/r#7")
		if code != 5 || record == nil || record.Ran || !strings.Contains(record.NotRunBecause, "--run") {
			t.Fatalf("exit %d, %+v\n%s", code, record, stderr)
		}
		if len(record.Findings) != 2 {
			t.Errorf("the review did not go on: %+v", record.Findings)
		}
		if _, err := os.Stat(filepath.Join(log, "argv")); err == nil {
			t.Error("the VM was started")
		}
		return log
	}

	t.Run("review", func(t *testing.T) {
		gated(t)
	})

	// The operator's --run runs it anyway; a relayed one is refused.
	t.Run("relayed --run", func(t *testing.T) {
		log := gated(t)
		var stdout, stderr bytes.Buffer
		if code := Main(t.Context(), []string{"--run", "7"}, true, strings.NewReader(""), &stdout, &stderr); code != 2 {
			t.Errorf("relayed --run: exit %d\n%s", code, stderr.String())
		}
		if _, err := os.Stat(filepath.Join(log, "argv")); err == nil {
			t.Error("the VM was started for a relayed --run")
		}
	})

	t.Run("--run", func(t *testing.T) {
		log := gated(t)
		code, record, _, stderr := runReview(t, "", "--run", "o/r#7")
		if _, err := os.Stat(filepath.Join(log, "argv")); err != nil {
			t.Errorf("--run did not start the VM: exit %d, %+v\n%s", code, record, stderr)
		}
	})
}

func testRunNoRun(t *testing.T) {
	r := newRemote(t, nil)
	setUp(t, r)
	withCodex(t, reply())
	log := withRun(t)
	_, record, _, _ := runReview(t, "", "--no-run", "o/r#7")
	if record == nil || record.Ran || record.NotRunBecause != "--no-run was given" {
		t.Errorf("%+v", record)
	}
	if _, err := os.Stat(filepath.Join(log, "argv")); err == nil {
		t.Error("the VM was started")
	}
}

// testRunLive runs a pull request's tests in the real VM, which no build
// sandbox has: set PR_REVIEW_LIVE_BACKEND to eca-gondolin or eca-bwrap,
// PR_REVIEW_LIVE_TOOLS to the Linux git-red-check's directory and
// PR_REVIEW_LIVE_CACHE to a cache directory to keep the Go toolchain in, and
// run `go test -run TestRun/live ./internal/prreview`. The pull request's
// tests try to reach the network and to write the run copy, and log what
// happened.
func testRunLive(t *testing.T) {
	backend, tools, cache := os.Getenv("PR_REVIEW_LIVE_BACKEND"), os.Getenv("PR_REVIEW_LIVE_TOOLS"), os.Getenv("PR_REVIEW_LIVE_CACHE")
	if backend == "" || tools == "" || cache == "" {
		t.Skip("set PR_REVIEW_LIVE_BACKEND, PR_REVIEW_LIVE_TOOLS and PR_REVIEW_LIVE_CACHE to run in the VM")
	}
	r := newRemote(t, func(work string) {
		write(t, filepath.Join(work, "r_test.go"), `package r

import (
	"net"
	"os"
	"testing"
	"time"
)

func TestA(t *testing.T) {
	if A() != 2 {
		t.Fatal("A is not 2")
	}
}

func TestEscape(t *testing.T) {
	_, err := net.DialTimeout("tcp", "1.1.1.1:443", 3*time.Second)
	t.Logf("dial out: %v", err)
	_, err = net.LookupHost("example.com")
	t.Logf("lookup: %v", err)
	t.Logf("write the run copy: %v", os.WriteFile("/src/planted", []byte("x"), 0o644))
	host, _ := os.Hostname()
	t.Logf("host: %s", host)
}
`)
	})
	setUp(t, r)
	t.Setenv("XDG_CACHE_HOME", cache)
	prLoopConfig(t, map[string]string{"backend": backend, "guestTools": tools})
	os.RemoveAll(filepath.Join(must(Root()), "o"))
	codexLog := withCodex(t, reply())
	settings := filepath.Join(t.TempDir(), "agent-tools.json")
	write(t, settings, `{"repos": {"o/r": {"test": "go test -v ./...", "build": "go vet ./...", "testFiles": ["**/*_test.go"]}}}`)
	t.Setenv("AGENT_TOOLS_CONFIG", settings)

	code, record, _, stderr := runReview(t, "", "o/r#7")
	t.Logf("exit %d\n%s", code, stderr)
	if record == nil || !record.Ran {
		t.Fatalf("did not run: %+v", record)
	}
	out, _ := json.MarshalIndent(map[string]any{"redCheck": record.RedCheck, "tests": record.Tests}, "", "  ")
	t.Logf("%s", out)
	prompt := string(must(os.ReadFile(filepath.Join(codexLog, "prompt"))))
	if !strings.Contains(prompt, "fails-as-asserted") {
		t.Errorf("red-check did not prove the test in the VM")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(must(latest(t.Context(), "o/r", 7)).Clone), "run")); err == nil {
		if left, _ := os.ReadDir(filepath.Join(filepath.Dir(must(latest(t.Context(), "o/r", 7)).Clone), "run")); len(left) > 0 {
			t.Errorf("run copies left: %v", left)
		}
	}
}

func testRunLocalRedCheck(t *testing.T) {
	// local makes a local clone of a pull request's range, configured to run
	// in the fake VM, and returns the remote and the backend's log.
	local := func(t *testing.T) (*remote, string) {
		t.Helper()
		r := goModulesRemote(t, "module example.com/r\n\ngo 1.20\n\nrequire example.com/dep v1.0.0\n")
		setUp(t, r)
		withProxy(t)
		log := withRun(t)
		gitT(t, r.work, "remote", "add", "origin", "https://github.com/o/r.git")
		return r, log
	}

	// A local range's red-check runs in the VM from a copy that holds the
	// range's objects itself and is gone afterwards.
	t.Run("in the VM", func(t *testing.T) {
		r, log := local(t)
		var stderr bytes.Buffer
		text, err := LocalRedCheck(t.Context(), r.work, r.base, r.head, &stderr)
		if err != nil {
			t.Fatalf("%v\n%s", err, stderr.String())
		}
		if !strings.Contains(text, "no-test-files") || !strings.Contains(text, "--- FAIL: TestA") {
			t.Errorf("the results:\n%s", text)
		}
		argv := strings.Split(strings.TrimSpace(string(must(os.ReadFile(filepath.Join(log, "argv"))))), "\n")
		workspace := argv[slices.Index(argv, "--workspace")+1]
		if !strings.HasPrefix(workspace, must(Root())+"/local-") {
			t.Errorf("the run copy %s", workspace)
		}
		if left, _ := filepath.Glob(filepath.Join(must(Root()), "local-*")); len(left) != 0 {
			t.Errorf("left behind: %v", left)
		}
	})

	// Without a backend it says why, and runs nothing.
	t.Run("without a backend", func(t *testing.T) {
		r, _ := local(t)
		prLoopConfig(t, nil)
		var stderr bytes.Buffer
		if _, err := LocalRedCheck(t.Context(), r.work, r.base, r.head, &stderr); err == nil ||
			!strings.Contains(err.Error(), "no sandbox backend") {
			t.Errorf("without a backend: %v", err)
		}
	})
}

func TestSections(t *testing.T) {
	got := sections("noise\nabc tests 1 aGk=\nabc redcheck x aGk=\nother tests 0 aGk=\nabc tests 2 \n", "abc")
	if len(got) != 1 || got["tests"].Exit != 2 || got["tests"].Output != "" {
		t.Errorf("%+v", got)
	}
}

// TestGuestPath: the guest's PATH puts Go and the guest tools first and
// keeps the backend's own, which on eca-bwrap holds the guest's coreutils,
// git and sh and no /usr/bin.
func TestGuestPath(t *testing.T) {
	var exports []string
	for _, line := range strings.Split(guestScript("n", "b", "h", &repos.Settings{Test: "true"}), "\n") {
		if strings.HasPrefix(line, "export PATH=") {
			exports = append(exports, line)
		}
	}
	t.Setenv("PATH", "/nix/store/x-coreutils/bin:/nix/store/y-git/bin")
	out, err := exec.Command("/bin/sh", "-c", strings.Join(append(exports, `printf %s "$PATH"`), "\n")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := guestGoRoot + "/bin:" + guestTools + ":/nix/store/x-coreutils/bin:/nix/store/y-git/bin"; string(out) != want {
		t.Errorf("PATH is %q, not %q", out, want)
	}
}
