package gitenv

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// git runs git in dir with the test process's environment, as a package's own
// setup helper does.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return run(t, dir, os.Environ(), args...)
}

// bare runs git in dir without the variables that locate a repository.
func bare(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(Location, name) {
			env = append(env, kv)
		}
	}
	return run(t, dir, append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"), args...)
}

func run(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// decoy makes a repository standing in for the caller's and points every
// git command of the test process at it, as git does for `rebase --exec`.
func decoy(t *testing.T) (snapshot func() string) {
	t.Helper()
	dir := t.TempDir()
	bare(t, dir, "init", "-q", "-b", "main")
	bare(t, dir, "-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false",
		"commit", "-q", "--allow-empty", "-m", "decoy")
	snapshot = func() string {
		config, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
		if err != nil {
			t.Fatal(err)
		}
		return string(config) + bare(t, dir, "for-each-ref") + bare(t, dir, "status", "--porcelain")
	}
	t.Setenv("GIT_DIR", filepath.Join(dir, ".git"))
	t.Setenv("GIT_WORK_TREE", dir)
	return snapshot
}

func TestIsolate(t *testing.T) {
	// The sequence is what a package's test setup does.
	t.Run("keeps setup out of the caller's repository", func(t *testing.T) {
		snapshot := decoy(t)
		before := snapshot()
		t.Setenv("GIT_CONFIG_GLOBAL", "/nonexistent")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
		Isolate()

		scratch := t.TempDir()
		git(t, scratch, "init", "-q", "-b", "main")
		git(t, scratch, "init", "-q", "--bare", filepath.Join(scratch, "fork.git"))
		git(t, scratch, "remote", "add", "fork", "https://example.invalid/fork.git")
		git(t, scratch, "-c", "user.name=T", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "x")
		git(t, scratch, "config", "core.bare", "true")

		if after := snapshot(); after != before {
			t.Errorf("the caller's repository changed:\n--- before\n%s\n--- after\n%s", before, after)
		}
		if got := os.Getenv("GIT_CONFIG_GLOBAL"); got != "/dev/null" {
			t.Errorf("GIT_CONFIG_GLOBAL = %q", got)
		}
		if got := os.Getenv("GIT_CONFIG_NOSYSTEM"); got != "1" {
			t.Errorf("GIT_CONFIG_NOSYSTEM = %q", got)
		}
		if remotes := git(t, scratch, "remote"); strings.TrimSpace(remotes) != "fork" {
			t.Errorf("remotes of the scratch repository: %q", remotes)
		}
	})

	// `git -c` hands its pairs down in these variables; left in place they
	// would override the isolated configuration.
	t.Run("drops the caller's git -c pairs", func(t *testing.T) {
		pairs := map[string]string{
			"GIT_CONFIG_PARAMETERS": "'core.pager'='x'",
			"GIT_CONFIG_COUNT":      "1",
			"GIT_CONFIG_KEY_0":      "include.path",
			"GIT_CONFIG_VALUE_0":    "/secret",
		}
		for name, value := range pairs {
			t.Setenv(name, value)
		}
		t.Setenv("GIT_CONFIGURED", "kept")
		SetNoConfig()

		for name := range pairs {
			if value, ok := os.LookupEnv(name); ok {
				t.Errorf("%s survived SetNoConfig: %q", name, value)
			}
		}
		if got := os.Getenv("GIT_CONFIGURED"); got != "kept" {
			t.Errorf("GIT_CONFIGURED = %q, want it kept", got)
		}
	})

	// Packages must be isolated by their own TestMain, not by setup helpers
	// that remember to be careful.
	t.Run("in each package's TestMain", func(t *testing.T) {
		if testing.Short() {
			t.Skip("runs other packages' tests")
		}
		snapshot := decoy(t)
		before := snapshot()
		cmd := exec.Command("go", "test", "-count=1", "../git", "../repos", "../pushguard")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("go test: %v\n%s", err, out)
		}
		if after := snapshot(); after != before {
			t.Errorf("the caller's repository changed:\n--- before\n%s\n--- after\n%s", before, after)
		}
	})
}

// After Isolate in TestMain, a request that escapes the test's own servers
// fails rather than reaching the internet, by HTTP and HTTPS alike, while
// a server the test starts on localhost still answers.
func TestIsolateRefusesTheNetwork(t *testing.T) {
	for _, url := range []string{"http://example.com/", "https://example.com/"} {
		t.Run(url, func(t *testing.T) {
			resp, err := http.Get(url)
			if err == nil {
				resp.Body.Close()
				t.Fatalf("GET %s reached the network: %s", url, resp.Status)
			}
			if proxy := os.Getenv("HTTPS_PROXY"); !strings.HasPrefix(proxy, "http://127.0.0.1:") {
				t.Errorf("GET %s failed (%v), but HTTPS_PROXY is %q", url, err, proxy)
			}
		})
	}
	t.Run("a local server", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "local")
		}))
		defer server.Close()
		resp, err := http.Get(server.URL)
		if err != nil {
			t.Fatalf("GET %s: %v", server.URL, err)
		}
		defer resp.Body.Close()
		if body, _ := io.ReadAll(resp.Body); string(body) != "local" {
			t.Errorf("GET %s: %q", server.URL, body)
		}
	})
}

func TestWithout(t *testing.T) {
	env := []string{"GIT_DIR=/r/.git", "PATH=/bin", "GIT_WORK_TREE=/r", "GIT_DIRECTORY=x", "GIT_CONFIG_GLOBAL=/dev/null"}
	if got := strings.Join(Without(env), " "); got != "PATH=/bin GIT_DIRECTORY=x GIT_CONFIG_GLOBAL=/dev/null" {
		t.Errorf("Without: %s", got)
	}
}

// TestWithoutConfig covers the environment of git that reads no
// configuration but the repository's: the caller's own global file, its
// `git -c` pairs and its location are gone, and nothing else is.
func TestWithoutConfig(t *testing.T) {
	env := []string{"GIT_DIR=/r/.git", "PATH=/bin", "GIT_CONFIG_GLOBAL=/home/u/.gitconfig", "GIT_CONFIG=/c",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=include.path", "GIT_CONFIG_VALUE_0=/secret",
		"GIT_CONFIG_PARAMETERS='core.pager'='x'", "GIT_CONFIGURED=kept", "GIT_ATTR_NOSYSTEM=1"}
	got := strings.Join(WithoutConfig(env), " ")
	if got != "PATH=/bin GIT_CONFIGURED=kept GIT_ATTR_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1" {
		t.Errorf("WithoutConfig: %s", got)
	}
}
