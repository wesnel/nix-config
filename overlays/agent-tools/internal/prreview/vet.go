package prreview

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/repos"
	"github.com/wesnel/agent-tools/internal/review"
)

// vetContract is the vetting Codex's instructions.
//
//go:embed vet.md
var vetContract string

// vetSchema is the JSON Schema of its reply, in Codex's strict subset.
//
//go:embed vet.schema.json
var vetSchema []byte

// Flag is a red flag: something a VM cannot contain, or that the vet could
// not clear. Path is "" for one about the whole pull request.
type Flag struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

// Execute is something the change would run, by the vetting Codex's
// account.
type Execute struct {
	Path  string `json:"path"`
	Line  *int   `json:"line"`
	What  string `json:"what"`
	Where string `json:"where"`
}

// Vet is what the vet found: Verdict is run only with no red flag.
type Vet struct {
	Verdict  string    `json:"verdict"`
	RedFlags []Flag    `json:"redFlags"`
	Executes []Execute `json:"executes"`
	// Neutralised are what the pull request asks for that pr-loop never
	// does on this side, so they need no flag.
	Neutralised []string `json:"neutralised"`
	Reasons     []string `json:"reasons"`
}

// hostFiles are files that run, or name programs that run, on this machine
// or in an agent when a checkout is opened, which no VM contains.
var hostFiles = []struct {
	match func(p string) bool
	why   string
}{
	{func(p string) bool { return path.Base(p) == ".envrc" }, "direnv runs it on entering the directory"},
	{func(p string) bool { return strings.HasPrefix(path.Base(p), ".dir-locals") }, "Emacs evaluates it on opening a file below it"},
	{func(p string) bool { return path.Base(p) == ".mcp.json" }, "agents start the MCP servers it names"},
	{inDir(".claude"), "Claude Code loads its settings, hooks and skills"},
	{inDir(".codex"), "Codex loads its config, which names programs it runs"},
	{inDir(".eca"), "ECA loads its config, which names programs it runs"},
	{inDir(".vscode"), "editors run its tasks"},
}

// inDir matches a path with dir among its directories.
func inDir(dir string) func(string) bool {
	return func(p string) bool {
		parts := strings.Split(p, "/")
		return slices.Contains(parts[:len(parts)-1], dir)
	}
}

// vet vets the pull request: red flags for what would run outside a VM,
// found here, and the vetting Codex's account of what would run and whether
// running the tests is safe. A vet Codex cannot make is a red flag.
func vet(ctx context.Context, pr *PR, model string, timeout time.Duration, log io.Writer) (*Vet, error) {
	v := &Vet{RedFlags: []Flag{}, Executes: []Execute{}, Neutralised: []string{}, Reasons: []string{}}
	changed, err := gitRun(ctx, pr.Clone, "diff", "--name-only", "--no-renames", "--diff-filter=AMT", "-z",
		pr.Base, pr.Head)
	if err != nil {
		return nil, err
	}
	for _, p := range strings.Split(strings.Trim(changed, "\x00"), "\x00") {
		if p == "" {
			continue
		}
		for _, h := range hostFiles {
			if h.match(p) {
				v.RedFlags = append(v.RedFlags, Flag{Path: p, Why: h.why})
				break
			}
		}
		base := path.Base(p)
		switch {
		case base == "flake.nix" || base == "shell.nix":
			if text := blobText(ctx, pr, p); strings.Contains(text, "shellHook") {
				v.RedFlags = append(v.RedFlags, Flag{Path: p, Why: "its shellHook runs in `nix develop` and direnv's `use flake`"})
			}
		case strings.HasPrefix(p, ".github/workflows/"):
			if text := blobText(ctx, pr, p); strings.Contains(text, "pull_request_target") {
				v.RedFlags = append(v.RedFlags, Flag{Path: p, Why: "it runs on pull_request_target, with the repository's secrets"})
			}
		case base == ".gitattributes" || base == ".lfsconfig" || base == ".gitmodules":
			v.Neutralised = append(v.Neutralised, p+": git runs no filter, LFS or submodule here")
		case p == "go.mod":
			text := blobText(ctx, pr, p)
			if regexp.MustCompile(`(?m)^\s*toolchain\s`).MatchString(text) {
				v.Neutralised = append(v.Neutralised, "go.mod toolchain: the Go toolchain is ours")
			}
			if localReplace.MatchString(text) {
				v.Neutralised = append(v.Neutralised, "go.mod replace with a local path: modules come only from the proxy")
			}
		}
	}
	links, err := gitRun(ctx, pr.Clone, "ls-tree", "-r", "-z", pr.Head)
	if err != nil {
		return nil, err
	}
	for _, entry := range strings.Split(links, "\x00") {
		if mode, rest, ok := strings.Cut(entry, " "); ok && mode == "120000" {
			if _, name, ok := strings.Cut(rest, "\t"); ok {
				v.Neutralised = append(v.Neutralised, name+": a link, checked out as a file holding its target")
			}
		}
	}

	codex := vetWithCodex(ctx, pr, model, timeout, log)
	if codex.err != nil {
		v.RedFlags = append(v.RedFlags, Flag{Why: "the vetting Codex could not run: " + clean(codex.err.Error(), 1024)})
	} else {
		for _, e := range codex.reply.Executes {
			e.Path, e.What = clean(e.Path, 1024), clean(e.What, 1024)
			v.Executes = append(v.Executes, e)
		}
		for _, r := range codex.reply.Reasons {
			v.Reasons = append(v.Reasons, clean(r, 1024))
		}
		if codex.reply.Verdict != "run" {
			v.RedFlags = append(v.RedFlags, Flag{Why: "the vetting Codex says not to run it: " + strings.Join(v.Reasons, " ")})
		}
	}
	v.Verdict = "run"
	if len(v.RedFlags) > 0 {
		v.Verdict = "dont-run"
	}
	return v, nil
}

// localReplace matches a go.mod replace directive whose target is a path.
var localReplace = regexp.MustCompile(`(?m)^\s*(replace\s+)?\S+(\s+\S+)?\s+=>\s+(\.{1,2}/|/)`)

func blobText(ctx context.Context, pr *PR, p string) string {
	data, _ := gitOut(ctx, pr.Clone, nil, "cat-file", "blob", pr.Head+":"+p)
	return string(data)
}

type vetReply struct {
	Verdict  string    `json:"verdict"`
	Executes []Execute `json:"executes"`
	Reasons  []string  `json:"reasons"`
}

type vetResult struct {
	reply *vetReply
	err   error
}

// vetWithCodex has Codex, confined and read-only, vet a checkout of the head.
func vetWithCodex(ctx context.Context, pr *PR, model string, timeout time.Duration, log io.Writer) vetResult {
	if why := codexMissing(); why != "" {
		return vetResult{err: errors.New(why)}
	}
	settings, err := repos.For(pr.Repo)
	if err != nil {
		return vetResult{err: err}
	}
	test := "none is configured, so nothing would run"
	if settings.Test != "" {
		test = "`" + settings.Test + "`"
	}
	parent := filepath.Join(filepath.Dir(pr.Clone), "codex")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return vetResult{err: err}
	}
	dir, err := os.MkdirTemp(parent, "vet-")
	if err != nil {
		return vetResult{err: err}
	}
	tree := filepath.Join(dir, "tree")
	defer func() {
		RemoveCheckout(pr.Clone, tree)
		os.RemoveAll(dir)
		// Left in place while another run is using it.
		os.Remove(parent)
	}()
	if err := Checkout(ctx, pr.Clone, tree, pr.Head); err != nil {
		return vetResult{err: err}
	}
	prompt := vetContract + fmt.Sprintf("\n# This pull request\n\n%s#%d, BASE..TIP %s..%s. The working directory is "+
		"a checkout of TIP; `git diff %s %s` shows the change. The test command is %s.\n",
		pr.Repo, pr.Number, pr.Base, pr.Head, pr.Base, pr.Head, test)
	reply, err := review.CodexRun(ctx, review.Codex{Checkout: tree, Prompt: prompt, Schema: vetSchema, Model: model,
		Timeout: timeout, Log: log, Git: func(args ...string) (string, error) { return gitRun(ctx, tree, args...) }})
	if err != nil {
		return vetResult{err: err}
	}
	var parsed vetReply
	if err := json.Unmarshal([]byte(reply.Text), &parsed); err != nil {
		return vetResult{err: fmt.Errorf("its reply is not the JSON the schema describes: %w", err)}
	}
	return vetResult{reply: &parsed}
}
