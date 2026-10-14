package prreview

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestGuestGo(t *testing.T) {
	for _, c := range []struct{ goMod, host, want string }{
		{"module m\n\ngo 1.22\n", "go1.26.8", "go1.26.8"},
		{"module m\n\ngo 1.27.1\n", "go1.26.8", "go1.27.1"},
		{"module m\n\ngo 1.27\n", "go1.26.8", "go1.27.0"},
		{"module m\n\ngo 1.22\n\ntoolchain go1.27.2\n", "go1.26.8", "go1.27.2"},
		{"module m\n\ngo 1.22\n\ntoolchain go1.20.1\n", "go1.26.8", "go1.26.8"},
		// A toolchain line that names no release is ignored.
		{"module m\n\ngo 1.22\n\ntoolchain go1.27.2-evil\n", "go1.26.8", "go1.26.8"},
		{"module m\n\ngo 1.22\n\ntoolchain default\n", "go1.26.8", "go1.26.8"},
		{"module m\n", "go1.26.8", "go1.26.8"},
	} {
		if got := guestGo(c.goMod, c.host); got != c.want {
			t.Errorf("%q with %s: %s, want %s", c.goMod, c.host, got, c.want)
		}
	}
}

// TestDownloadEnv holds the download to the public proxy and checksum
// database, with no setting of the caller's, no git, no workspace and no
// other toolchain.
func TestDownloadEnv(t *testing.T) {
	for _, kv := range []string{"GOPRIVATE=example.com", "GONOPROXY=*", "GONOSUMDB=*", "GOINSECURE=*",
		"GOFLAGS=-insecure", "GOPROXY=direct", "GOSUMDB=off", "GOTOOLCHAIN=go1.99.0+auto", "GOWORK=/x/go.work",
		"GIT_CONFIG_GLOBAL=/x", "GOENV=/x/env", "GOVCS=*:all"} {
		name, value, _ := strings.Cut(kv, "=")
		t.Setenv(name, value)
	}
	env := downloadEnv("/cache/gomod", "/tmp/x", "local")
	want := map[string]string{"GOENV": "off", "GOTOOLCHAIN": "local", "GOWORK": "off", "GOFLAGS": "-mod=mod",
		"GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org", "GOVCS": "*:off", "GOPRIVATE": "",
		"GONOPROXY": "", "GONOSUMDB": "", "GOINSECURE": "", "GOMODCACHE": "/cache/gomod",
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0"}
	seen := map[string]int{}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		seen[name]++
		if w, ok := want[name]; ok && w != value {
			t.Errorf("%s=%q, want %q", name, value, w)
		}
		if strings.HasPrefix(name, "GO") || strings.HasPrefix(name, "GIT") || strings.HasPrefix(name, "CGO") {
			if _, ok := want[name]; !ok && name != "GOPATH" && name != "GOCACHE" {
				t.Errorf("%s reaches the download", kv)
			}
		}
	}
	for name := range want {
		if seen[name] != 1 {
			t.Errorf("%s is set %d times", name, seen[name])
		}
	}
}

// proxyFixture is a GOPROXY directory serving example.com/dep v1.0.0.
func proxyFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	v := filepath.Join(dir, "example.com", "dep", "@v")
	write(t, filepath.Join(v, "list"), "v1.0.0\n")
	write(t, filepath.Join(v, "v1.0.0.info"), `{"Version": "v1.0.0", "Time": "2024-01-01T00:00:00Z"}`)
	write(t, filepath.Join(v, "v1.0.0.mod"), "module example.com/dep\n\ngo 1.20\n")
	f, err := os.Create(filepath.Join(v, "v1.0.0.zip"))
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, text := range map[string]string{"go.mod": "module example.com/dep\n\ngo 1.20\n",
		"dep.go": "package dep\n\nfunc B() int { return 2 }\n"} {
		w, _ := z.Create("example.com/dep@v1.0.0/" + name)
		w.Write([]byte(text))
	}
	z.Close()
	f.Close()
	return dir
}

func goModulesRemote(t *testing.T, goMod string) *remote {
	return newRemote(t, func(work string) {
		write(t, filepath.Join(work, "go.mod"), goMod)
		write(t, filepath.Join(work, "r.go"), "package r\n\nimport \"example.com/dep\"\n\nfunc A() int { return dep.B() }\n")
	})
}

// withProxy serves modules from proxyFixture, and makes the module caches
// the test leaves, which go makes read-only, removable.
func withProxy(t *testing.T) {
	old, oldSum := goProxy, goSumDB
	goProxy, goSumDB = "file://"+proxyFixture(t), "off"
	root := Root()
	t.Cleanup(func() {
		goProxy, goSumDB = old, oldSum
		filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil {
				os.Chmod(p, info.Mode()|0o200)
			}
			return nil
		})
	})
}

func TestFetchModules(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go here")
	}
	r := goModulesRemote(t, "module example.com/r\n\ngo 1.20\n\nrequire example.com/dep v1.0.0\n")
	setUp(t, r)
	withProxy(t)
	pr, err := Fetch(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	mods, err := fetchModules(context.Background(), pr)
	if err != nil {
		t.Fatal(err)
	}
	if mods.NotRun != "" {
		t.Fatalf("not run: %s", mods.NotRun)
	}
	if _, err := os.Stat(filepath.Join(mods.Dir, "example.com", "dep@v1.0.0", "dep.go")); err != nil {
		t.Errorf("the module was not downloaded: %v", err)
	}
	if !strings.HasPrefix(mods.Dir, filepath.Dir(pr.Clone)+"/") || strings.HasPrefix(mods.Dir, pr.Dir+"/") {
		t.Errorf("modules in %s, not beside the clone outside the review directory", mods.Dir)
	}
}

// TestHostileGoMod: what go.mod names beyond modules from the proxy, a
// toolchain or a replacement by a path on this machine, never reaches the
// download.
func TestHostileGoMod(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go here")
	}
	secret := t.TempDir()
	write(t, filepath.Join(secret, "go.mod"), "module example.com/secret\n")
	r := goModulesRemote(t, "module example.com/r\n\ngo 1.20\n\ntoolchain go1.20.99\n\nrequire example.com/dep v1.0.0\n\n"+
		"replace example.com/dep => "+secret+"\n\nreplace (\n\texample.com/other => ../../other\n)\n")
	setUp(t, r)
	withProxy(t)
	pr := must(Fetch(context.Background(), "o/r", 7))
	mods, err := fetchModules(context.Background(), pr)
	if err != nil || mods.NotRun != "" {
		t.Fatalf("%+v, %v", mods, err)
	}
	if _, err := os.Stat(filepath.Join(mods.Dir, "example.com", "dep@v1.0.0", "dep.go")); err != nil {
		t.Errorf("the module came from the replacement, not the proxy: %v", err)
	}
	used := string(must(os.ReadFile(filepath.Join(mods.Dir, "..", "go.mod"))))
	if strings.Contains(used, "toolchain") || strings.Contains(used, "=>") {
		t.Errorf("the download's go.mod keeps what the pull request named:\n%s", used)
	}
}

// TestPrivateModuleFailsClosed: a module the public proxy does not have
// stops the run, and says why.
func TestPrivateModuleFailsClosed(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go here")
	}
	r := goModulesRemote(t, "module example.com/r\n\ngo 1.20\n\nrequire example.com/private v1.0.0\n")
	setUp(t, r)
	withProxy(t)
	pr := must(Fetch(context.Background(), "o/r", 7))
	mods, err := fetchModules(context.Background(), pr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mods.NotRun, "example.com/private") {
		t.Errorf("not run because %q", mods.NotRun)
	}
}

func TestNotAGoModule(t *testing.T) {
	r := newRemote(t, func(work string) { os.Remove(filepath.Join(work, "go.mod")) })
	setUp(t, r)
	pr := must(Fetch(context.Background(), "o/r", 7))
	mods, err := fetchModules(context.Background(), pr)
	if err != nil || !strings.Contains(mods.NotRun, "go.mod") {
		t.Errorf("%+v, %v", mods, err)
	}
}

// TestToolchainOutsideReviews: a toolchain in the review directory, which a
// review session may have written, is never what a run mounts.
func TestToolchainOutsideReviews(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go here")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	write(t, filepath.Join(Root(), "toolchains", "go1.99.0.linux-"+runtime.GOARCH, "bin", "go"), "#!/bin/sh\n")
	old, oldSum := goProxy, goSumDB
	goProxy, goSumDB = "file://"+t.TempDir(), "off"
	t.Cleanup(func() { goProxy, goSumDB = old, oldSum })
	if dir, err := prepareToolchain(context.Background(), "go1.99.0"); err == nil || strings.HasPrefix(dir, Root()) {
		t.Errorf("the review directory's toolchain was used: %s, %v", dir, err)
	}
}

// TestToolchainCopy: the toolchain module's files are read-only and lose
// their execute bits; the copy the guest runs has them back on its programs
// alone.
func TestToolchainCopy(t *testing.T) {
	from := t.TempDir()
	for _, p := range []string{"bin/go", "bin/gofmt", "pkg/tool/linux_arm64/compile", "src/fmt/print.go", "VERSION"} {
		write(t, filepath.Join(from, p), "x")
		os.Chmod(filepath.Join(from, p), 0o444)
	}
	to := filepath.Join(t.TempDir(), "go1.26.8.linux-"+runtime.GOARCH)
	if err := copyToolchain(from, to); err != nil {
		t.Fatal(err)
	}
	var executable []string
	filepath.Walk(to, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(to, p)
			if info.Mode()&0o111 != 0 {
				executable = append(executable, rel)
			}
			if info.Mode()&0o222 != 0 {
				t.Errorf("%s is writable", rel)
			}
		}
		return nil
	})
	slices.Sort(executable)
	if !slices.Equal(executable, []string{"bin/go", "bin/gofmt", "pkg/tool/linux_arm64/compile"}) {
		t.Errorf("executable: %q", executable)
	}
}
