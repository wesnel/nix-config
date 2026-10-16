package review

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/loop"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/relay"
)

// relayInputs are what a relayed review is given on stdin in place of its
// files: the client reads them where it runs, so the host never opens a
// path its caller names.
type relayInputs struct {
	Goal  string `json:"goal,omitempty"`
	Items string `json:"items,omitempty"`
	Prior string `json:"prior,omitempty"`
	Reply string `json:"reply,omitempty"`
	// Description is the pull request description a compat review's
	// prompt takes its Reverting section from.
	Description string `json:"description,omitempty"`
	// Top is the worktree as the caller sees it, which the prompt names
	// for the reviewer to read the change in: a sandbox may see it at
	// another path than the host.
	Top string `json:"top,omitempty"`
}

// relayedFlags are the flags a relayed review takes: a prompt's printing
// or a reply's check, with its files on stdin.
var relayedFlags = []string{"print-prompt", "check", "focus", "goal", "items", "prior", "description", "reviewer"}

// relayedFiles are the flags of relayedFlags that name a file, which the
// client reads and sends on stdin.
var relayedFiles = []string{"check", "goal", "items", "prior", "description"}

// relayedRefusal is why the relay's caller may not run the review flags
// describe, or "": it may print a prompt or check a reply, each file given
// as -, and never run Codex, which reviews outside the sandbox the loop
// is confined to.
func relayedRefusal(flags *flag.FlagSet, printPrompt bool, check string) string {
	const narrow = "a relayed review only prints a prompt (--print-prompt or --reviewer subagent) or checks a reply " +
		"(--check), with --focus and its files given as -, which git fresh-review in the sandbox sends; run anything else " +
		"outside the loop, or hand the operator the command"
	why := ""
	flags.Visit(func(f *flag.Flag) {
		switch {
		case !slices.Contains(relayedFlags, f.Name),
			f.Name == "reviewer" && f.Value.String() != "subagent",
			slices.Contains(relayedFiles, f.Name) && f.Value.String() != "-":
			why = narrow
		}
	})
	if !printPrompt && check == "" {
		why = narrow
	}
	return why
}

// relayedTimeout bounds a relayed review's work, since the relay waits for
// its program however long it runs. It is longer than prreview's
// LocalTimeout, which bounds red-check's run in the VM alone, so that a run
// finishing within that is not cut short by the fetches around it.
const relayedTimeout = 45 * time.Minute

// loopOwn refuses a relayed review of anything but one of the review
// loop's branches, from the worktree the loop gave it, up to the commit
// checked out there. A receipt covers the commits of its range, and the
// plan that takes it is found from the worktree, so a review of another
// range, or from another worktree, could meet a route another branch
// owes, or log in a main session's plan.
func loopOwn(r *span) error {
	branch, _ := git.Run("-C", r.top, "branch", "--show-current")
	head, err := git.Run("-C", r.top, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	want, ok, err := loop.Worktree(r.common, branch)
	if err != nil {
		return err
	}
	same := func(a, b string) bool {
		a, _ = filepath.EvalSymlinks(a)
		b, _ = filepath.EvalSymlinks(b)
		return a != "" && a == b
	}
	if branch == "" || !ok || !same(want, r.top) {
		return refusal.New("%s is not the worktree `pr-loop worktrees` made for one of the loop's branches, so a review "+
			"relayed from it is not the loop's to log; run it from the worktree of the branch under review, "+
			".pr-loop/<number>, or hand back to the operator", r.top)
	}
	if r.tip != head {
		return refusal.New("%s ends at %s, not at %s, the tip of %s checked out here; review the branch's own commits, "+
			"as `git fresh-review ... BASE..%s`", r.given, r.tip, head, branch, branch)
	}
	return nil
}

// readRelayed reads the inputs a relayed review's client sent on stdin.
func readRelayed(stdin io.Reader) (relayInputs, error) {
	var in relayInputs
	data, err := io.ReadAll(stdin)
	if err != nil {
		return in, err
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return in, refusal.New("the relayed review's input is not the JSON git fresh-review sends (%v); run "+
			"`git fresh-review` in the sandbox, which sends its files on stdin", err)
	}
	return in, nil
}

// forward sends a prompt's printing or a reply's check to the relay at
// url as argv, with the files files names read here, where the caller can
// see them, and passed on stdin; it relays the result.
func forward(url string, argv []string, files map[string]string, given string, stdin io.Reader, stdout,
	stderr io.Writer) int {
	var in relayInputs
	into := map[string]*string{"goal": &in.Goal, "items": &in.Items, "prior": &in.Prior, "check": &in.Reply,
		"description": &in.Description}
	for _, f := range relayedFiles {
		path := files[f]
		if path == "" {
			continue
		}
		data, err := readFile(path, stdin)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return 1
		}
		*into[f] = data
		argv = append(argv, "--"+f, "-")
	}
	in.Top, _ = git.Toplevel()
	data, err := json.Marshal(in)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	r, err := relay.Call(url, "git-fresh-review", append(argv, given), data)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	io.WriteString(stdout, r.Stdout)
	io.WriteString(stderr, r.Stderr)
	return r.Code
}

// inLoop reports whether this runs for a review loop: behind the relay,
// or in a loop session.
func inLoop() bool {
	return os.Getenv(relay.RelayEnv) == "1" || os.Getenv("PR_LOOP_SESSION") != ""
}
