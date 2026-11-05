package gitenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
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
}

// noTestMain names the packages whose tests run no git, and so need no
// TestMain. A package that gains one that isolates must leave this list.
var noTestMain = map[string]bool{
	"internal/agentplans": true,
	"internal/env":        true,
	"internal/goproxy":    true,
	"internal/hostpaths":  true,
}

// Packages must be isolated by their own TestMain, not by setup helpers
// that remember to be careful: every package of the module with tests has a
// TestMain that calls Isolate before m.Run. TestIsolate shows what Isolate
// keeps out of the caller's repository; this reads the source alone.
func TestEveryTestMainIsolates(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tested := map[string]bool{}   // package directory: has test files
	isolated := map[string]bool{} // package directory: its TestMain isolates
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		dir, _ := filepath.Rel(root, filepath.Dir(path))
		tested[dir] = true
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "TestMain" && isolatesFirst(fn) {
				isolated[dir] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tested["internal/gitenv"] {
		t.Fatalf("found no tests under %s: %v", root, tested)
	}
	for dir := range tested {
		switch {
		case noTestMain[dir] && isolated[dir]:
			t.Errorf("%s isolates in its TestMain: remove it from noTestMain", dir)
		case !noTestMain[dir] && !isolated[dir]:
			t.Errorf("%s has no TestMain that calls gitenv.Isolate() before m.Run()", dir)
		}
	}
}

// isolatesFirst reports whether the TestMain fn calls Isolate, as
// gitenv.Isolate or in this package, before it calls Run on its argument.
func isolatesFirst(fn *ast.FuncDecl) bool {
	params := fn.Type.Params.List
	if len(params) != 1 || len(params[0].Names) != 1 {
		return false
	}
	m := params[0].Names[0].Name
	var isolate, run token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			if f.Name == "Isolate" && !isolate.IsValid() {
				isolate = call.Pos()
			}
		case *ast.SelectorExpr:
			x, _ := f.X.(*ast.Ident)
			switch {
			case x != nil && x.Name == "gitenv" && f.Sel.Name == "Isolate" && !isolate.IsValid():
				isolate = call.Pos()
			case x != nil && x.Name == m && f.Sel.Name == "Run" && !run.IsValid():
				run = call.Pos()
			}
		}
		return true
	})
	return isolate.IsValid() && run.IsValid() && isolate < run
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

// TestWithNoConfig covers an Env that reads no git configuration but the
// repository's, as SetNoConfig leaves this process's: the variables that
// give configuration are gone, NoConfig is set, and the rest, its location
// and directory too, is kept.
func TestWithNoConfig(t *testing.T) {
	t.Parallel()
	before := env.New("/work", "GIT_DIR=/r/.git", "PATH=/bin", "GIT_CONFIG_GLOBAL=/home/u/.gitconfig", "GIT_CONFIG=/c",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=include.path", "GIT_CONFIG_VALUE_0=/secret",
		"GIT_CONFIG_PARAMETERS='core.pager'='x'", "GIT_CONFIGURED=kept", "GIT_CONFIG_NOSYSTEM=0")
	got := WithNoConfig(before)
	if s := strings.Join(got.Environ(), " "); s != "GIT_DIR=/r/.git PATH=/bin GIT_CONFIGURED=kept "+
		"GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1" {
		t.Errorf("WithNoConfig: %s", s)
	}
	if got.Dir() != "/work" {
		t.Errorf("WithNoConfig moved to %q", got.Dir())
	}
	if before.Get("GIT_CONFIG") != "/c" {
		t.Error("WithNoConfig changed the Env it was given")
	}
}

func TestWithout(t *testing.T) {
	t.Parallel()
	env := []string{"GIT_DIR=/r/.git", "PATH=/bin", "GIT_WORK_TREE=/r", "GIT_DIRECTORY=x", "GIT_CONFIG_GLOBAL=/dev/null"}
	if got := strings.Join(Without(env), " "); got != "PATH=/bin GIT_DIRECTORY=x GIT_CONFIG_GLOBAL=/dev/null" {
		t.Errorf("Without: %s", got)
	}
}

// TestWithoutConfig covers the environment of git that reads no
// configuration but the repository's: the caller's own global file, its
// `git -c` pairs and its location are gone, and nothing else is.
func TestWithoutConfig(t *testing.T) {
	t.Parallel()
	env := []string{"GIT_DIR=/r/.git", "PATH=/bin", "GIT_CONFIG_GLOBAL=/home/u/.gitconfig", "GIT_CONFIG=/c",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=include.path", "GIT_CONFIG_VALUE_0=/secret",
		"GIT_CONFIG_PARAMETERS='core.pager'='x'", "GIT_CONFIGURED=kept", "GIT_ATTR_NOSYSTEM=1"}
	got := strings.Join(WithoutConfig(env), " ")
	if got != "PATH=/bin GIT_CONFIGURED=kept GIT_ATTR_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1" {
		t.Errorf("WithoutConfig: %s", got)
	}
}
