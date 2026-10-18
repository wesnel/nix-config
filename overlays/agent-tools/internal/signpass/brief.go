package signpass

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
)

// Brief makes a pass print only what someone reading its output later needs:
// what it will sign, one line for the touches, a progress line every
// progressEvery signatures, one line per push, and anything git reports as an
// error, in full. Git's own account of a rebase or a push is left out.
var Brief bool

// progressEvery is how many signatures pass between progress lines in a
// brief pass.
const progressEvery = 25

// StdoutIsTerminal reports whether standard output is a terminal, which is
// where a pass keeps git's own output.
func StdoutIsTerminal() bool { return isTerminal(os.Stdout) }

var rebasing = regexp.MustCompile(`^Rebasing \((\d+)/(\d+)\)`)

// briefStderr takes in what a rebase writes to standard error, which git
// ends with a carriage return at each step it starts, reports its progress,
// and keeps the rest.
type briefStderr struct {
	label string
	out   io.Writer
	// all is everything written; kept is all but the progress.
	all, kept bytes.Buffer
	seg       []byte
	total     int
	printed   int
}

func (w *briefStderr) Write(p []byte) (int, error) {
	w.all.Write(p)
	for _, c := range p {
		w.seg = append(w.seg, c)
		if c == '\r' || c == '\n' {
			w.flush()
		}
	}
	return len(p), nil
}

func (w *briefStderr) flush() {
	line := string(w.seg)
	w.seg = w.seg[:0]
	m := rebasing.FindStringSubmatch(line)
	if m == nil {
		w.kept.WriteString(line)
		return
	}
	n, _ := strconv.Atoi(m[1])
	w.total, _ = strconv.Atoi(m[2])
	// Step n has started, so the n-1 before it are signed.
	if done := n - 1; done > 0 && done%progressEvery == 0 {
		w.report(done)
	}
}

func (w *briefStderr) report(done int) {
	if done != w.printed {
		fmt.Fprintf(w.out, "signing %s: %d/%d\n", w.label, done, w.total)
		w.printed = done
	}
}

// finish ends the stream: after a rebase that went through, the last step is
// signed too.
func (w *briefStderr) finish(ok bool) {
	if len(w.seg) > 0 {
		w.flush()
	}
	if ok && w.total > 0 {
		w.report(w.total)
	}
}

// runGit runs git attached to the terminal for a command that asks for a key
// touch, and returns its exit code and what it wrote to standard error. A
// brief pass shows that only when the command fails, and the progress of a
// rebase of the branch label as it goes.
func runGit(ctx context.Context, env []string, label string, args ...string) (int, string) {
	if !Brief {
		return git.AttachedStderr(ctx, env, args...)
	}
	w := &briefStderr{label: label, out: os.Stderr}
	code := git.AttachedQuiet(ctx, env, w, args...)
	w.finish(code == 0)
	if code != 0 {
		os.Stderr.Write(w.kept.Bytes())
	}
	return code, w.all.String()
}

// announce says what a brief pass will do and what it asks of the operator.
func announce(commits, branches, pushBranches, urls, touches int) {
	line := fmt.Sprintf("signing %s on %s", plural(commits, "commit"), plural(branches, "branch"))
	if pushBranches > 0 {
		line += fmt.Sprintf("; pushing %s to %s", plural(pushBranches, "branch"), plural(urls, "URL"))
	}
	fmt.Fprintf(os.Stderr, "%s; up to %s\n", line, plural(touches, "key touch"))
	announceTouch()
}

// announceTouch is the one line that tells the operator what to do.
func announceTouch() {
	fmt.Fprintln(os.Stderr, "touch the key when it blinks")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "ch") {
		return fmt.Sprintf("%d %ses", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// pushShare counts what a push plan sends: the branches it pushes, and the
// push URLs they go to, each of which is one SSH login and so one touch.
func pushShare(byRemote map[string][]target) (branches, urls int) {
	for remote, targets := range byRemote {
		out, _ := git.Run("remote", "get-url", "--push", "--all", remote)
		urls += len(bytes.Fields([]byte(out)))
		branches += len(targets)
	}
	return branches, urls
}
