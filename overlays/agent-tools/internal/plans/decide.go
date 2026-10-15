package plans

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// DecideOptions settle a question: Answer is Wesley's answer, or with Drop
// why the question is dropped. Supersedes names a settled question this
// decision replaces.
type DecideOptions struct {
	Answer, Supersedes, Role string
	Drop                     bool
}

// Decided is what Decide did.
type Decided struct {
	Question   string `json:"question"`
	State      string `json:"state"`
	Answer     string `json:"answer"`
	Supersedes string `json:"supersedes,omitempty"`
	File       string `json:"file"`
	Event      Event  `json:"event"`
}

// Decide settles open question qid in place: DECIDED, or DROPPED with
// Drop, with CLOSED and the answer written as Org does, and tagged
// ARCHIVE so Org folds it. A question it supersedes becomes SUPERSEDED.
// The log records a decided event.
func (p Plan) Decide(qid string, o DecideOptions, now time.Time) (Decided, error) {
	if err := p.checkOwn(); err != nil {
		return Decided{}, err
	}
	o.Answer = strings.TrimSpace(o.Answer)
	if o.Answer == "" {
		return Decided{}, refusal.New("a decision needs Wesley's answer, or why the question is dropped. Pass it with `--answer 'one line'`")
	}
	if err := checkLine("answer", o.Answer); err != nil {
		return Decided{}, err
	}
	if o.Drop && o.Supersedes != "" {
		return Decided{}, refusal.New("a dropped question settles nothing, so it supersedes nothing. Pass `--drop` or `--supersedes`, not both")
	}
	state := kwDecided
	if o.Drop {
		state = kwDropped
	}
	var d Decided
	err := p.withLock(func(l *lockedPlan) error {
		items, err := p.items()
		if err != nil {
			return err
		}
		q, ok := items[qid]
		if !ok || !questionID.MatchString(qid) || q.Keyword == "" || q.Done {
			return refusal.New("%q is not an open question of %s. Use an ID `agent-plan questions %s --all --waiting` lists", qid, p.Name, p.file(""))
		}
		var target item
		if o.Supersedes != "" {
			target, ok = items[o.Supersedes]
			switch {
			case !ok || !questionID.MatchString(o.Supersedes) || target.Keyword == "":
				return refusal.New("%q is not a question of %s. Pass the ID of the decision this one replaces, as `agent-plan decisions %s --all` lists it", o.Supersedes, p.Name, p.file(""))
			case !target.Done:
				return refusal.New("%s is still open, so there is no decision to supersede. Drop it with `agent-plan decide %s %s --drop --answer 'why'`", o.Supersedes, p.file(""), o.Supersedes)
			}
		}
		err = l.edit(q.File, func(lines []string) ([]string, error) {
			return settle(lines, qid, state, o.Answer, "", o.Supersedes, now)
		})
		if err != nil {
			return err
		}
		if o.Supersedes != "" {
			err = l.edit(target.File, func(lines []string) ([]string, error) {
				at := headingWithID(lines, o.Supersedes)
				if at < 0 {
					return nil, fmt.Errorf("%s left %s while being superseded", o.Supersedes, target.File)
				}
				return setKeyword(lines, at, kwSuperseded, fileKeywords(strings.Join(lines, "\n")), now), nil
			})
			if err != nil {
				return err
			}
		}
		e, err := l.log(Event{Kind: "decided", Item: qid, Summary: o.Answer, Role: o.Role}, now)
		d = Decided{Question: qid, State: state, Answer: o.Answer, Supersedes: o.Supersedes, File: q.File, Event: e}
		return err
	})
	return d, err
}

// settle is lines with the question whose ID is id settled as state, with
// its answer, who decided it unless Wesley did, and the decision it
// supersedes.
func settle(lines []string, id, state, answer, decidedBy, supersedes string, now time.Time) ([]string, error) {
	at := headingWithID(lines, id)
	if at < 0 {
		return nil, fmt.Errorf("%s is no longer in its file", id)
	}
	ks := fileKeywords(strings.Join(lines, "\n"))
	if !ks.done[state] {
		return nil, refusal.New("the plan file of %s does not define %s as a done keyword. Restore its `#+TODO:` lines as `agent-plan new` writes them, then run the command again", id, state)
	}
	lines = setKeyword(lines, at, state, ks, now)
	lines = setProperty(lines, at, "ANSWER", answer)
	if decidedBy != "" {
		lines = setProperty(lines, at, "DECIDED_BY", decidedBy)
	}
	if supersedes != "" {
		lines = setProperty(lines, at, "SUPERSEDES", supersedes)
	}
	return addTag(lines, at, archiveTag), nil
}

// StatusLine is the status line for the session its input names, else for
// the worktree its cwd is in, as Emacs's mode line sends: "review owed"
// where the session committed since the plan's last review receipt, and
// how many open questions of that plan Wesley can answer now, or nothing.
// It reads only
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
	owed := ""
	if reviewOwed(p, in.SessionID) {
		owed = "review owed · "
	}
	qs, err := p.Questions(true)
	qs = Askable(qs)
	switch {
	case err != nil || len(qs) == 0:
		return strings.TrimSuffix(owed, " · ")
	case len(qs) == 1:
		return owed + "1 open question · /questions"
	}
	return owed + fmt.Sprintf("%d open questions · /questions", len(qs))
}

// reviewOwed reports whether session's last commit in p's log comes after
// the log's last review receipt. It reads the log alone; StopCheck is
// the one that compares a receipt with the change.
func reviewOwed(p Plan, session string) bool {
	events, err := p.Events()
	if err != nil || session == "" {
		return false
	}
	for i := len(events) - 1; i >= 0; i-- {
		switch e := events[i]; {
		case e.Kind == receiptKind:
			return false
		case e.Kind == "committed" && e.Session == session:
			return true
		}
	}
	return false
}
