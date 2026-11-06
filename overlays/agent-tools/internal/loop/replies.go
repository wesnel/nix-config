package loop

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/record"
)

// PlannedReply is one section of a reply file.
type PlannedReply struct {
	Thread  string
	Outcome string
	Body    string
	Resolve bool
}

// ReplyHeader is how every section of a reply file starts. Any line that
// begins with it is a header, so a body cannot contain such a line.
const ReplyHeader = "=== thread"

var replyHeader = regexp.MustCompile(`^=== thread (\S+) (\S+)( resolve)?$`)

// ParseReplies reads a reply file: sections, each a header line
// "=== thread THREAD OUTCOME" or "=== thread THREAD OUTCOME resolve",
// OUTCOME being how the reply settles the thread (record.Outcomes),
// followed by the reply's Markdown body up to the next header. Blank lines
// around a body are dropped; everything else, including code fences, is
// kept as written.
func ParseReplies(text string) ([]PlannedReply, error) {
	var out []PlannedReply
	var body []string
	seen := map[string]int{}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		n := i + 1
		if !strings.HasPrefix(line, ReplyHeader) {
			if len(out) == 0 && strings.TrimSpace(line) != "" {
				return nil, fmt.Errorf("line %d: text before the first %q line", n, ReplyHeader)
			}
			body = append(body, line)
			continue
		}
		m := replyHeader.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil || !slices.Contains(record.Outcomes, m[2]) {
			return nil, fmt.Errorf("line %d: %q is not %q or %q, OUTCOME being how the reply settles the thread: one of %s",
				n, line, ReplyHeader+" THREAD OUTCOME", ReplyHeader+" THREAD OUTCOME resolve", strings.Join(record.Outcomes, ", "))
		}
		if err := finishReply(out, body); err != nil {
			return nil, err
		}
		body = nil
		if first, ok := seen[m[1]]; ok {
			return nil, fmt.Errorf("line %d: %s already has a section, at line %d", n, m[1], first)
		}
		seen[m[1]] = n
		out = append(out, PlannedReply{Thread: m[1], Outcome: m[2], Resolve: m[3] != ""})
	}
	if err := finishReply(out, body); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no %q lines; see pr-loop --help", ReplyHeader)
	}
	return out, nil
}

func finishReply(out []PlannedReply, body []string) error {
	if len(out) == 0 {
		return nil
	}
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body = body[1:]
	}
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	last := &out[len(out)-1]
	if len(body) == 0 {
		return fmt.Errorf("the reply to %s is empty", last.Thread)
	}
	last.Body = strings.Join(body, "\n")
	return nil
}

// ReplyResult is what ReplyAll did with one section.
type ReplyResult struct {
	Thread   string `json:"thread"`
	PR       int    `json:"pr"`
	URL      string `json:"url,omitempty"`
	Resolved bool   `json:"resolved"`
	Skipped  string `json:"skipped,omitempty"`
}

// ReplyAll posts each planned reply and resolves the threads marked for it,
// skipping threads already resolved, and logs each thread it answers as a
// finding settled in review round round, with the reply's outcome. Every
// thread is checked before anything is posted, so one bad section leaves
// nothing half done.
//
// It runs in a bots-mode loop, or outside one on the operator's terminal,
// where it shows each reply and posts it only once the operator answers y,
// as Post does; an agent, which cannot open /dev/tty, is refused there.
func ReplyAll(plan []PlannedReply, round int) ([]ReplyResult, error) {
	if round < 1 {
		return nil, refuse("pass the review round these replies answer as `--round N`, counting from 1")
	}
	var answers *bufio.Reader
	var tty io.ReadWriteCloser
	if os.Getenv(ModeEnv) != "bots" {
		var err error
		if tty, err = openTTY(); err != nil {
			return nil, refuse("outside a bots-mode review loop, `pr-loop reply --from` posts only from the operator's "+
				"terminal, confirming each reply there, and /dev/tty cannot be opened here (%v). Put the reply file in "+
				"the report, with `pr-loop reply --from FILE --round N` for the operator to run in the worktree", err)
		}
		defer tty.Close()
		answers = bufio.NewReader(tty)
	}
	if len(plan) == 0 {
		return nil, refuse("the reply file names no threads; start each reply with a line `=== thread THREAD OUTCOME`")
	}
	policy, err := newThreadPolicy()
	if err != nil {
		return nil, err
	}
	checks, err := policy.checkAll(plan)
	if err != nil {
		return nil, err
	}
	if err := checkReplies(policy.repo.Slug, plan, checks); err != nil {
		return nil, err
	}
	return postPlannedReplies(plan, round, checks, tty, answers)
}

func postPlannedReplies(plan []PlannedReply, round int, checks []*threadCheck, tty io.ReadWriteCloser, answers *bufio.Reader) ([]ReplyResult, error) {
	results := []ReplyResult{}
	for i, r := range plan {
		result := ReplyResult{Thread: r.Thread, PR: checks[i].pr}
		if checks[i].resolved {
			result.Resolved, result.Skipped = true, "already resolved"
			results = append(results, result)
			continue
		}
		if tty != nil {
			confirmed, err := confirmReply(tty, answers, r, checks[i].pr)
			if err != nil {
				return nil, partial(results, r.Thread, "reading the answer for", err)
			}
			if !confirmed {
				result.Skipped = "not confirmed"
				results = append(results, result)
				continue
			}
		}
		url, err := postReply(checks[i].pr, r.Thread, r.Body)
		result.URL = url
		if err != nil {
			return nil, partial(results, r.Thread, "replying", err)
		}
		logEvent(plans.Event{Kind: "finding-settled", Review: fmt.Sprintf("%d.%s", round, r.Thread), Outcome: r.Outcome,
			Summary: fmt.Sprintf("thread %s on #%d", r.Thread, checks[i].pr)})
		if r.Resolve {
			if err := resolveThread(checks[i].pr, r.Thread); err != nil {
				return nil, partial(append(results, result), r.Thread, "resolving", err)
			}
			result.Resolved = true
		}
		results = append(results, result)
	}
	return results, nil
}

func confirmReply(tty io.Writer, answers *bufio.Reader, r PlannedReply, pr int) (bool, error) {
	then := ""
	if r.Resolve {
		then = ", then resolved"
	}
	fmt.Fprintf(tty, "\n=== %s on #%d, answered %s%s:\n%s\n=== Post this reply? [y/N] ", r.Thread, pr, r.Outcome, then, r.Body)
	answer, err := answers.ReadString('\n')
	if err != nil && answer == "" {
		return false, err
	}
	return strings.TrimSpace(answer) == "y", nil
}

func (policy *threadPolicy) checkAll(plan []PlannedReply) ([]*threadCheck, error) {
	checks := make([]*threadCheck, len(plan))
	var problems []string
	allRefused := true
	for i, r := range plan {
		check, err := policy.check(r.Thread)
		if err != nil {
			var refusal Refusal
			allRefused = allRefused && errors.As(err, &refusal)
			problems = append(problems, r.Thread+": "+err.Error())
		}
		checks[i] = check
	}
	if len(problems) > 0 {
		if allRefused {
			return nil, refuse("nothing was posted, since these threads cannot be answered; drop them from the file, "+
				"put them in the report, and run `pr-loop reply --from` again:\n  %s", strings.Join(problems, "\n  "))
		}
		return nil, fmt.Errorf("nothing was posted:\n  %s", strings.Join(problems, "\n  "))
	}
	return checks, nil
}

// partial reports a failure partway through ReplyAll with what it had done.
func partial(done []ReplyResult, thread, step string, err error) error {
	report, _ := json.Marshal(done)
	return fmt.Errorf("%s %s: %w; stopped there, after: %s", step, thread, err, report)
}
