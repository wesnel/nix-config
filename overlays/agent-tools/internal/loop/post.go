package loop

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/redact"
)

// Draft writes body as the drafted answer to thread id, with outcome, in
// the thread's PR record, in a people-mode loop. It is the one command that
// takes a person's thread; only the operator posts the draft, with Post.
func Draft(id, body, outcome string) (any, error) {
	if err := requireMode("draft", "people"); err != nil {
		return nil, err
	}
	if !slices.Contains(record.Outcomes, outcome) {
		return nil, refuse("%q is not an outcome a thread is answered with; pass one of %s as `--outcome`", outcome,
			strings.Join(record.Outcomes, ", "))
	}
	if strings.TrimSpace(body) == "" {
		return nil, refuse("the draft is empty; pass its text with `--body-file FILE`")
	}
	// Post sends the text the record keeps, so a draft the record would
	// change is refused rather than posted changed.
	if record.Text(body) != body {
		return nil, refuse("the draft would be posted other than as written, since a review record keeps no control "+
			"characters but newline and tab, no file-variable markers (`Local Variables:`, `-*-`) and at most %d bytes; "+
			"rewrite it without them and run `pr-loop draft` again", record.TextCap)
	}
	if err := redact.Outgoing(body); err != nil {
		return nil, err
	}
	policy, err := newThreadPolicy()
	if err != nil {
		return nil, err
	}
	thread, err := policy.read(id)
	if err != nil {
		return nil, err
	}
	path, err := prFile(policy.repo, thread.pr)
	if err != nil {
		return nil, err
	}
	err = record.Update(path, func(p *record.PR) error {
		for i := range p.Threads {
			if p.Threads[i].ID == id {
				p.Threads[i].Draft = &record.Draft{Outcome: outcome, Body: body}
				p.Updated = time.Now()
				return nil
			}
		}
		return refuse("#%d's review record does not hold %s yet; run `pr-loop threads %d`, then draft again", thread.pr, id, thread.pr)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"thread": id, "pr": thread.pr, "outcome": outcome, "drafted": path}, nil
}

// openTTY opens the operator's terminal. Tests replace it.
var openTTY = func() (io.ReadWriteCloser, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }

// PostResult is what Post did with one draft.
type PostResult struct {
	Thread   string `json:"thread"`
	URL      string `json:"url,omitempty"`
	Resolved bool   `json:"resolved,omitempty"`
	Skipped  string `json:"skipped,omitempty"`
}

// Post shows each unposted draft of PR number's record (only threads, when
// any are named) on the terminal and posts it as a reply once the operator
// answers y there, exactly the bytes it showed. A thread answered fix or
// rebut is then resolved. Once every unresolved thread a person commented
// in has a posted answer, the people whose threads those are are asked to
// review again. It is the operator's: the guard refuses it to agents, and
// it refuses where no terminal can be opened.
func Post(number int, threads []string) (any, error) {
	tty, err := openTTY()
	if err != nil {
		return nil, refuse("`pr-loop post` asks the operator on the terminal before each reply, and /dev/tty cannot be "+
			"opened here (%v); the operator runs it from a terminal of their own", err)
	}
	defer tty.Close()
	policy, err := newThreadPolicy()
	if err != nil {
		return nil, err
	}
	repo := policy.repo
	path, err := prFile(repo, number)
	if err != nil {
		return nil, err
	}
	p, err := record.ReadPR(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refuse("#%d has no review record, so nothing is drafted; run `pr-loop threads %d` first", number, number)
	}
	if err != nil {
		return nil, err
	}
	for _, id := range threads {
		if !slices.ContainsFunc(p.Threads, func(t record.Thread) bool { return t.ID == id }) {
			return nil, refuse("#%d's review record holds no thread %s; `pr-loop threads %d` lists them", number, id, number)
		}
	}
	answers := bufio.NewReader(tty)
	results := []PostResult{}
	for _, t := range p.Threads {
		if t.Draft == nil || !t.Draft.Posted.IsZero() || (len(threads) > 0 && !slices.Contains(threads, t.ID)) {
			continue
		}
		// The bytes shown are the bytes posted: the draft is read once.
		body, outcome := t.Draft.Body, t.Draft.Outcome
		where := t.Path
		if t.Line > 0 {
			where = fmt.Sprintf("%s:%d", t.Path, t.Line)
		}
		fmt.Fprintf(tty, "\n=== %s on #%d (%s), answered %s:\n%s\n=== Post this reply? [y/N] ", t.ID, number, where, outcome, body)
		answer, err := answers.ReadString('\n')
		if err != nil && answer == "" {
			return results, fmt.Errorf("reading the answer for %s: %w", t.ID, err)
		}
		result := PostResult{Thread: t.ID}
		if strings.TrimSpace(answer) != "y" {
			result.Skipped = "not confirmed"
			results = append(results, result)
			continue
		}
		if err := redact.Outgoing(body); err != nil {
			return results, fmt.Errorf("the draft for %s: %w", t.ID, err)
		}
		thread, err := policy.read(t.ID)
		if err != nil {
			return results, err
		}
		if thread.pr != number {
			return results, refuse("%s is on #%d, not #%d; rerun `pr-loop threads %d` to fix the record", t.ID, thread.pr, number, number)
		}
		if result.URL, err = postReply(number, t.ID, body); err != nil {
			return results, err
		}
		if err := markPosted(path, t.ID, body); err != nil {
			return results, err
		}
		if (outcome == "fix" || outcome == "rebut") && !thread.resolved {
			if err := resolveThread(number, t.ID); err != nil {
				return results, err
			}
			result.Resolved = true
		}
		results = append(results, result)
	}
	rerequested, err := rerequest(repo, path, number)
	if err != nil {
		return results, err
	}
	return map[string]any{"posted": results, "rerequested": rerequested}, nil
}

// markPosted records the draft of thread id, still body, as posted now.
func markPosted(path, id, body string) error {
	return record.Update(path, func(p *record.PR) error {
		for i := range p.Threads {
			if d := p.Threads[i].Draft; p.Threads[i].ID == id && d != nil {
				if d.Body != body {
					return fmt.Errorf("the draft for %s changed while it was posted; it was posted as shown, and its record is left for the next `pr-loop post`", id)
				}
				d.Posted = time.Now()
				return nil
			}
		}
		return fmt.Errorf("the draft for %s left the record while it was posted", id)
	})
}

// rerequest asks the people whose threads on PR number those are to review
// again, once each unresolved thread a person commented in has a posted
// answer, and returns whom it asked.
func rerequest(repo *Repo, path string, number int) ([]string, error) {
	p, err := record.ReadPR(path)
	if err != nil {
		return nil, err
	}
	bots, err := botsFor(repo.Slug)
	if err != nil {
		return nil, err
	}
	var people []string
	for _, t := range p.Threads {
		var authors []string
		for _, c := range t.Comments {
			if c.Author != repo.Viewer && !isBot(bots, ThreadComment{Author: c.Author, Type: c.Type}) {
				authors = append(authors, c.Author)
			}
		}
		if len(authors) == 0 || t.Resolved {
			continue
		}
		if t.Draft == nil || t.Draft.Posted.IsZero() {
			return []string{}, nil
		}
		for _, a := range authors {
			if !slices.Contains(people, a) {
				people = append(people, a)
			}
		}
	}
	for _, person := range people {
		if err := requestReviewer(repo.Slug, number, person); err != nil {
			return nil, fmt.Errorf("asking %s to review #%d again: %w", person, number, err)
		}
	}
	return append([]string{}, people...), nil
}
