package prreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/version"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var (
	// goProxy and goSumDB are where modules and their checksums come from;
	// tests serve a directory and turn the database off.
	goProxy = "https://proxy.golang.org"
	goSumDB = "sum.golang.org"
)

// Modules are a pull request's Go modules, downloaded on this side for a VM
// with no network.
type Modules struct {
	// Dir is the module cache, beside the clone and outside the review
	// directory, which a review session may write.
	Dir string
	// Go is the Go release the guest runs: go.mod's, never older than
	// this machine's.
	Go string
	// NotRun says why the pull request cannot run, or "".
	NotRun string
}

// release matches a Go release's name, as a toolchain module's version has it.
var release = regexp.MustCompile(`^go1\.\d+(\.\d+|(rc|beta)\d+)?$`)

// guestGo is the Go release for a go.mod: its toolchain line where that
// names a release, else its go line, and never older than host.
func guestGo(goMod, host string) string {
	want := ""
	if m := regexp.MustCompile(`(?m)^\s*go\s+(\S+)\s*$`).FindStringSubmatch(goMod); m != nil && release.MatchString("go"+m[1]) {
		want = "go" + m[1]
		// A go line may name a language version, go1.N, whose first
		// release is go1.N.0 from Go 1.21 on.
		if regexp.MustCompile(`^go1\.\d+$`).MatchString(want) && version.Compare(want, "go1.21") >= 0 {
			want += ".0"
		}
	}
	if m := regexp.MustCompile(`(?m)^\s*toolchain\s+(\S+)\s*$`).FindStringSubmatch(goMod); m != nil && release.MatchString(m[1]) {
		want = m[1]
	}
	if want == "" || version.Compare(want, host) < 0 {
		return host
	}
	return want
}

// downloadEnv is the whole environment of a download: the public proxy and
// checksum database, no private paths, no git, no workspace and no
// settings of the caller's. toolchain is GOTOOLCHAIN.
func downloadEnv(modcache, tmp, toolchain string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + tmp,
		"TMPDIR=" + tmp,
		"GOENV=off",
		"GOTOOLCHAIN=" + toolchain,
		"GOWORK=off",
		"GOFLAGS=-mod=mod",
		"GOPROXY=" + goProxy,
		"GOSUMDB=" + goSumDB,
		"GOVCS=*:off",
		"GOPRIVATE=",
		"GONOPROXY=",
		"GONOSUMDB=",
		"GOINSECURE=",
		"GOMODCACHE=" + modcache,
		"GOPATH=" + filepath.Join(tmp, "gopath"),
		"GOCACHE=" + filepath.Join(tmp, "gocache"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	}
}

// hostGo is this machine's go release, or "" without one.
func hostGo(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "go", "env", "GOVERSION")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOENV=off")
	cmd.Dir = os.TempDir()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// goRun runs go in dir with env, returning its stdout and why it failed.
func goRun(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir, cmd.Env = dir, env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// fetchModules downloads the modules the pull request's go.mod and go.sum in
// moduleDir name, and nothing else of it: no other file is read, so no code
// of it or of a module runs here. go.mod's toolchain line and its
// replacements by a path are dropped first, so the toolchain is ours and
// every module comes from the proxy, checked against go.sum and the checksum
// database. moduleDir is repos.Settings' ModuleDir, which repos holds inside
// the checkout.
func fetchModules(ctx context.Context, pr *PR, moduleDir string) (*Modules, error) {
	goModPath := path.Join(moduleDir, "go.mod")
	goMod, err := gitOut(ctx, pr.Clone, nil, "cat-file", "blob", pr.Head+":"+goModPath)
	if err != nil {
		where := "at its top"
		if goModPath != "go.mod" {
			where = "at " + goModPath + ", the moduleDir wgn.home.llm.repos names"
		}
		return &Modules{NotRun: "the pull request has no go.mod " + where + ", so there is no Go test to run"}, nil
	}
	host := hostGo(ctx)
	if host == "" {
		return &Modules{NotRun: "go is not installed here, so its modules could not be downloaded"}, nil
	}
	mods := &Modules{Go: guestGo(string(goMod), host)}
	work := filepath.Join(filepath.Dir(pr.Clone), pr.Head[:12]+"-go")
	mods.Dir = filepath.Join(work, "mod")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "pr-review-go-")
	if err != nil {
		return nil, err
	}
	defer func() {
		goRun(context.Background(), tmp, downloadEnv(filepath.Join(tmp, "x"), tmp, "local"), "clean", "-cache")
		os.RemoveAll(tmp)
	}()
	if err := os.WriteFile(filepath.Join(work, "go.mod"), goMod, 0o644); err != nil {
		return nil, err
	}
	goSum, _ := gitOut(ctx, pr.Clone, nil, "cat-file", "blob", pr.Head+":"+path.Join(moduleDir, "go.sum"))
	if err := os.WriteFile(filepath.Join(work, "go.sum"), goSum, 0o644); err != nil {
		return nil, err
	}
	// The host's go downloads, through a release of go.mod's own where it
	// is older than that, which the go command fetches and checks itself.
	toolchain := "local"
	if version.Compare(host, mods.Go) < 0 {
		toolchain = mods.Go
	}
	env := downloadEnv(mods.Dir, tmp, toolchain)
	edit := []string{"mod", "edit", "-toolchain=none"}
	out, err := goRun(ctx, work, env, "mod", "edit", "-json")
	if err != nil {
		return &Modules{NotRun: "its go.mod could not be read: " + clean(err.Error(), 2048)}, nil
	}
	var parsed struct {
		Replace []struct {
			Old struct{ Path, Version string }
			New struct{ Path, Version string }
		}
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	for _, r := range parsed.Replace {
		if r.New.Version == "" {
			old := r.Old.Path
			if r.Old.Version != "" {
				old += "@" + r.Old.Version
			}
			edit = append(edit, "-dropreplace="+old)
		}
	}
	if _, err := goRun(ctx, work, env, edit...); err != nil {
		return nil, err
	}
	if _, err := goRun(ctx, work, env, "mod", "download", "all"); err != nil {
		return &Modules{Dir: mods.Dir, Go: mods.Go,
			NotRun: "its modules could not be downloaded from the public proxy: " + clean(err.Error(), 2048)}, nil
	}
	return mods, nil
}

// prepareToolchain is a directory holding the Go release for the guest:
// golang.org/toolchain's module for Linux on this architecture, checked by
// the proxy and the checksum database, copied with its programs executable
// into pr-review-toolchains beside the review directory, once per release.
// It is reused unchecked, so it stays outside the review directory, where a
// review session may write.
func prepareToolchain(ctx context.Context, release string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	name := release + ".linux-" + runtime.GOARCH
	dir := filepath.Join(filepath.Dir(root), "pr-review-toolchains", name)
	if _, err := os.Stat(filepath.Join(dir, "bin", "go")); err == nil {
		return dir, nil
	}
	tmp, err := os.MkdirTemp("", "pr-review-toolchain-")
	if err != nil {
		return "", err
	}
	modcache := filepath.Join(tmp, "mod")
	defer func() {
		goRun(context.Background(), tmp, downloadEnv(modcache, tmp, "local"), "clean", "-modcache")
		os.RemoveAll(tmp)
	}()
	out, err := goRun(ctx, tmp, downloadEnv(modcache, tmp, "local"), "mod", "download", "-json",
		"golang.org/toolchain@v0.0.1-"+name)
	if err != nil {
		return "", err
	}
	var module struct{ Dir string }
	if err := json.Unmarshal(out, &module); err != nil || module.Dir == "" {
		return "", fmt.Errorf("go mod download gave no directory for %s: %v", name, err)
	}
	staging := dir + ".tmp"
	os.RemoveAll(staging)
	if err := copyToolchain(module.Dir, staging); err != nil {
		return "", err
	}
	return dir, os.Rename(staging, dir)
}

// copyToolchain copies a toolchain module's tree, every file read-only and
// only its programs, in bin and pkg/tool, executable.
func copyToolchain(from, to string) error {
	return filepath.Walk(from, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		target := filepath.Join(to, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		mode := os.FileMode(0o444)
		if slash := filepath.ToSlash(rel); strings.HasPrefix(slash, "bin/") || strings.HasPrefix(slash, "pkg/tool/") {
			mode = 0o555
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Chmod(target, mode)
	})
}
