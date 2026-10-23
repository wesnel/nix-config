package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// PlannedReply is one section of a reply file.
type PlannedReply struct {
	Thread  string
	Body    string
	Resolve bool
}

// ReplyHeader is how every section of a reply file starts. Any line that
// begins with it is a header, so a body cannot contain such a line.
const ReplyHeader = "=== thread"

var replyHeader = regexp.MustCompile(`^=== thread (\S+)( resolve)?$`)

// ParseReplies reads a reply file: sections, each a header line
// "=== thread THREAD" or "=== thread THREAD resolve", followed by the reply's
// Markdown body up to the next header. Blank lines around a body are
// dropped; everything else, including code fences, is kept as written.
func ParseReplies(text string) ([]PlannedReply, error) {
	var out []PlannedReply
	var body []string
	seen := map[string]int{}
	finish := func() error {
		if len(out) == 0 {
			body = nil
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
		body = nil
		return nil
	}
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
		if m == nil {
			return nil, fmt.Errorf("line %d: %q is not %q or %q", n, line, ReplyHeader+" THREAD", ReplyHeader+" THREAD resolve")
		}
		if err := finish(); err != nil {
			return nil, err
		}
		if first, ok := seen[m[1]]; ok {
			return nil, fmt.Errorf("line %d: %s already has a section, at line %d", n, m[1], first)
		}
		seen[m[1]] = n
		out = append(out, PlannedReply{Thread: m[1], Resolve: m[2] != ""})
	}
	if err := finish(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no %q lines; see pr-loop --help", ReplyHeader)
	}
	return out, nil
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
// skipping threads already resolved. Every thread is checked before anything
// is posted, so one bad section leaves nothing half done.
func ReplyAll(plan []PlannedReply) ([]ReplyResult, error) {
	if err := requireMode("reply", "bots"); err != nil {
		return nil, err
	}
	if len(plan) == 0 {
		return nil, refuse("the reply file names no threads; start each reply with a line `=== thread THREAD`")
	}
	policy, err := newThreadPolicy()
	if err != nil {
		return nil, err
	}
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
	if err := checkReplies(policy.repo.Slug, plan, checks); err != nil {
		return nil, err
	}
	results := []ReplyResult{}
	for i, r := range plan {
		result := ReplyResult{Thread: r.Thread, PR: checks[i].pr}
		if checks[i].resolved {
			result.Resolved, result.Skipped = true, "already resolved"
			results = append(results, result)
			continue
		}
		if result.URL, err = postReply(checks[i].pr, r.Thread, r.Body); err != nil {
			return nil, partial(results, r.Thread, "replying", err)
		}
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

// partial reports a failure partway through ReplyAll with what it had done.
func partial(done []ReplyResult, thread, step string, err error) error {
	report, _ := json.Marshal(done)
	return fmt.Errorf("%s %s: %w; stopped there, after: %s", step, thread, err, report)
}
