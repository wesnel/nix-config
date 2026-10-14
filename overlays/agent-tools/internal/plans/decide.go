package plans

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// Decided is what Decide did.
type Decided struct {
	Question string `json:"question"`
	Decision string `json:"decision"`
	File     string `json:"file"`
	Event    Event  `json:"event"`
}

var (
	starLine = regexp.MustCompile(`^(\*+)\s`)
	idLine   = regexp.MustCompile(`^\s*:ID:\s*(\S+)\s*$`)
)

func level(line string) int {
	if m := starLine.FindStringSubmatch(line); m != nil {
		return len(m[1])
	}
	return 0
}

// section is the lines of the level-2 heading titled title: from the
// heading to the next heading of level 1 or 2.
func section(lines []string, title string) (start, end int, ok bool) {
	for i, l := range lines {
		if level(l) == 2 && strings.TrimSpace(strings.TrimLeft(l, "*")) == title {
			for end = i + 1; end < len(lines); end++ {
				if n := level(lines[end]); n == 1 || n == 2 {
					break
				}
			}
			return i, end, true
		}
	}
	return 0, 0, false
}

// Decide settles open question qid with Wesley's answer: it moves the
// question, its recommendation and context kept, from Open questions to
// the end of Decisions as the next decision ID of its file, records the
// answer there, and logs a decided event.
func (p Plan) Decide(qid, answer string, now time.Time) (Decided, error) {
	if err := p.checkOwn(); err != nil {
		return Decided{}, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return Decided{}, refusal.New("a decision needs Wesley's answer. Pass it with `--answer 'one line'`")
	}
	if err := checkLine("answer", answer); err != nil {
		return Decided{}, err
	}
	var d Decided
	err := p.withLock(func(l *lockedPlan) error {
		qs, err := p.Questions(true)
		if err != nil {
			return err
		}
		var q *Question
		for i := range qs {
			if qs[i].ID == qid {
				q = &qs[i]
			}
		}
		if q == nil {
			return refusal.New("%q is not an open question of %s. Use an ID `agent-plan questions %s --all` lists", qid, p.Name, p.file(""))
		}
		prefix := ""
		if q.Part != "" {
			prefix = q.Part + "."
		}
		items, err := p.itemList()
		if err != nil {
			return err
		}
		next := 1
		decision := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `D([0-9]+)$`)
		for _, it := range items {
			if m := decision.FindStringSubmatch(it.ID); m != nil {
				if n, _ := strconv.Atoi(m[1]); n >= next {
					next = n + 1
				}
			}
		}
		did := fmt.Sprintf("%sD%d", prefix, next)
		err = l.edit(q.File, func(lines []string) ([]string, error) {
			text, err := moveQuestion(strings.Join(lines, "\n"), qid, did, answer)
			return strings.Split(text, "\n"), err
		})
		if err != nil {
			return err
		}
		e, err := l.log(Event{Kind: "decided", Item: did, Summary: qid + ": " + answer}, now)
		d = Decided{Question: qid, Decision: did, File: q.File, Event: e}
		return err
	})
	return d, err
}

// moveQuestion is text with question qid's subtree moved to the end of
// Decisions as decision did, answered.
func moveQuestion(text, qid, did, answer string) (string, error) {
	lines := strings.Split(text, "\n")
	start, end, ok := section(lines, "Open questions")
	if !ok {
		return "", fmt.Errorf("no Open questions heading")
	}
	var block []string
	for i := start + 1; i < end && block == nil; i++ {
		if level(lines[i]) != 3 {
			continue
		}
		j := i + 1
		for j < end && (level(lines[j]) == 0 || level(lines[j]) > 3) {
			j++
		}
		for _, l := range lines[i:j] {
			if m := idLine.FindStringSubmatch(l); m != nil && m[1] == qid {
				block = append([]string{}, lines[i:j]...)
				lines = append(lines[:i:i], lines[j:]...)
				break
			}
		}
	}
	if block == nil {
		return "", fmt.Errorf("%s is not under Open questions", qid)
	}
	var moved []string
	answered := false
	for _, l := range block {
		switch {
		case idLine.MatchString(l):
			moved = append(moved, ":ID: "+did, ":QUESTION: "+qid)
			continue
		case !answered && strings.TrimSpace(l) == ":END:":
			moved = append(moved, ":ANSWER: "+answer)
			answered = true
		}
		moved = append(moved, l)
	}
	_, end, ok = section(lines, "Decisions")
	if !ok {
		return "", fmt.Errorf("no Decisions heading")
	}
	// A section ending the file ends at its final empty line.
	if end == len(lines) && lines[end-1] == "" {
		end--
	}
	out := append(append(append([]string{}, lines[:end]...), moved...), lines[end:]...)
	return strings.Join(out, "\n"), nil
}

// StatusLine is the status line for the session its input names, else for
// the worktree its cwd is in, as Emacs's mode line sends: how many open
// questions of that plan Wesley can answer now, or nothing. It reads only
// this host's plan files and git, never the network, and says nothing on
// any error.
func (c Config) StatusLine(input []byte) string {
	var in struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	var p Plan
	found := false
	if in.SessionID != "" {
		if f, err := c.Find(FindOptions{Session: "claude:" + in.SessionID}); err == nil && len(f) > 0 {
			p, err = c.Open(f[0].Plan)
			found = err == nil
		}
	}
	if !found {
		top, _ := checkout(in.Cwd)
		if top == "" {
			return ""
		}
		if p, _, found = c.Locate("", top); !found {
			return ""
		}
	}
	qs, err := p.Questions(true)
	qs = Askable(qs)
	switch {
	case err != nil || len(qs) == 0:
		return ""
	case len(qs) == 1:
		return "1 open question · /questions"
	}
	return fmt.Sprintf("%d open questions · /questions", len(qs))
}
