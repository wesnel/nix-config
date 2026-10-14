package plans

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// Event is one entry of a plan's log.
type Event struct {
	ID       string `json:"event"`
	Time     string `json:"time"`
	Kind     string `json:"kind"`
	Item     string `json:"item"`
	Summary  string `json:"summary,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Role     string `json:"role,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Review   string `json:"review,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Tokens   int64  `json:"tokens,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

var (
	// Kinds are what an event records about its item.
	Kinds = []string{"planned", "plan-reviewed", "started", "done", "blocked", "abandoned", "verified",
		"reviewed", "finding-settled", "decided", "handed-back"}
	// Roles are who logs an event.
	Roles = []string{"single", "parent", "subagent", "reviewer", "design-reviewer"}
	// Outcomes are the review-change skill's triage outcomes.
	Outcomes = []string{"fix", "rebut", "duplicate-of", "belongs-in", "defer", "needs-wesley"}
	// Efforts are the reasoning efforts a harness takes.
	Efforts = []string{"low", "medium", "high", "xhigh", "max"}

	reviewPattern = regexp.MustCompile(`^[0-9]+\.[A-Za-z0-9_-]+$`)
)

func oneOf(value string, allowed []string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}

// check refuses an event the log must not hold.
func (p Plan) check(e Event) error {
	for what, v := range map[string]string{"summary": e.Summary, "agent": e.Agent, "branch": e.Branch,
		"commit": e.Commit, "evidence": e.Evidence, "model": e.Model, "item": e.Item, "kind": e.Kind} {
		if err := checkLine(what, v); err != nil {
			return err
		}
	}
	switch {
	case !oneOf(e.Kind, Kinds):
		return refusal.New("%q is not an event kind. Use one of `%s`", e.Kind, strings.Join(Kinds, "`, `"))
	case e.Role != "" && !oneOf(e.Role, Roles):
		return refusal.New("%q is not a role. Use one of `%s`", e.Role, strings.Join(Roles, "`, `"))
	case e.Outcome != "" && !oneOf(e.Outcome, Outcomes):
		return refusal.New("%q is not a triage outcome. Use one of `%s`, as the `review-change` skill defines them", e.Outcome, strings.Join(Outcomes, "`, `"))
	case e.Review != "" && !reviewPattern.MatchString(e.Review):
		return refusal.New("review %q is not <round>.<finding id>. Pass it as `--review 1.F3`", e.Review)
	case e.Kind == "finding-settled" && (e.Review == "" || e.Outcome == ""):
		return refusal.New("a settled finding needs the finding and its outcome. Pass `--review <round>.<id> --outcome <outcome>`")
	case e.Effort != "" && !oneOf(e.Effort, Efforts):
		return refusal.New("%q is not an effort. Use one of `%s`", e.Effort, strings.Join(Efforts, "`, `"))
	case e.Tokens < 0:
		return refusal.New("tokens cannot be negative. Pass the total the harness reported, as `--tokens 12345`")
	case strings.ContainsAny(e.Agent+e.Branch+e.Model, " \t"):
		return refusal.New("agent, branch and model are single words. Pass them without spaces, as `--model opus`")
	}
	items, err := p.items()
	if err != nil {
		return err
	}
	if _, ok := items[e.Item]; !ok {
		return refusal.New("%q is not an item of %s. Add it to the plan file first, or use an ID `agent-plan status %s` lists", e.Item, p.Name, p.Dir)
	}
	return nil
}

// Log appends e to the plan's log, with a new event ID and now's time, and
// returns it as written.
func (p Plan) Log(e Event, now time.Time) (Event, error) {
	if err := p.check(e); err != nil {
		return Event{}, err
	}
	e.ID, e.Time = newUUID(), now.Format("[2006-01-02 Mon 15:04]")
	var b strings.Builder
	b.WriteString("* " + e.Kind + " " + e.Item)
	if e.Summary != "" {
		b.WriteString(" — " + e.Summary)
	}
	tokens := ""
	if e.Tokens > 0 {
		tokens = strconv.FormatInt(e.Tokens, 10)
	}
	b.WriteString("\n:PROPERTIES:\n")
	for _, kv := range [][2]string{{"EVENT", e.ID}, {"AGENT", e.Agent}, {"ROLE", e.Role}, {"ITEM", e.Item},
		{"KIND", e.Kind}, {"BRANCH", e.Branch}, {"COMMIT", e.Commit}, {"EVIDENCE", e.Evidence},
		{"REVIEW", e.Review}, {"OUTCOME", e.Outcome}, {"TOKENS", tokens}, {"MODEL", e.Model},
		{"EFFORT", e.Effort}} {
		if kv[1] != "" {
			b.WriteString(":" + kv[0] + ": " + kv[1] + "\n")
		}
	}
	// The drawer comes straight after the heading, or Org does not read
	// it as one.
	b.WriteString(":END:\n" + e.Time + "\n")
	return e, p.append(b.String())
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// append adds text to log.org in one write, under an exclusive lock on
// log.org.lock, and syncs it. The lock file records the log's size and
// digest after each append; a log that no longer matches them was changed
// some other way, and is refused.
func (p Plan) append(text string) error {
	lock, err := os.OpenFile(p.log()+".lock", os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	recorded, err := io.ReadAll(lock)
	if err != nil {
		return err
	}
	log, err := os.OpenFile(p.log(), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer log.Close()
	current, err := io.ReadAll(log)
	if err != nil {
		return err
	}
	if len(recorded) > 0 && strings.TrimSpace(string(recorded)) != digest(current) {
		return refusal.New("%s has changed since agent-plan last appended to it, and the log is append-only. Leave it as it is, and tell Wesley which plan's log changed", p.log())
	}
	if len(current) > 0 && current[len(current)-1] != '\n' {
		text = "\n" + text
	}
	if n, err := log.Write([]byte(text)); err != nil || n != len(text) {
		return fmt.Errorf("appending to %s: wrote %d of %d bytes: %v", p.log(), n, len(text), err)
	}
	if err := log.Sync(); err != nil {
		return err
	}
	if err := lock.Truncate(0); err != nil {
		return err
	}
	if _, err := lock.WriteAt([]byte(digest(append(current, text...))+"\n"), 0); err != nil {
		return err
	}
	return lock.Sync()
}

func digest(data []byte) string {
	return fmt.Sprintf("%d %x", len(data), sha256.Sum256(data))
}

// Events reads the plan's log, oldest first.
func (p Plan) Events() ([]Event, error) {
	data, err := os.ReadFile(p.log())
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, h := range parseOrg(string(data)) {
		if h.Props["EVENT"] == "" {
			continue
		}
		e := Event{ID: h.Props["EVENT"], Kind: h.Props["KIND"], Item: h.Props["ITEM"], Agent: h.Props["AGENT"],
			Role: h.Props["ROLE"], Branch: h.Props["BRANCH"], Commit: h.Props["COMMIT"],
			Evidence: h.Props["EVIDENCE"], Review: h.Props["REVIEW"], Outcome: h.Props["OUTCOME"],
			Model: h.Props["MODEL"], Effort: h.Props["EFFORT"]}
		e.Tokens, _ = strconv.ParseInt(h.Props["TOKENS"], 10, 64)
		if _, summary, ok := strings.Cut(h.Title, " — "); ok {
			e.Summary = summary
		}
		for _, line := range h.Body {
			if strings.HasPrefix(line, "[") {
				e.Time = strings.TrimSpace(line)
				break
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// TokenRow totals tokens by event kind, model and effort.
type TokenRow struct {
	Kind   string `json:"kind"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Events int    `json:"events"`
	Tokens int64  `json:"tokens"`
}

// tokenRows totals the events that report tokens.
func tokenRows(events []Event) []TokenRow {
	rows := map[[3]string]*TokenRow{}
	for _, e := range events {
		if e.Tokens <= 0 {
			continue
		}
		key := [3]string{e.Kind, e.Model, e.Effort}
		if rows[key] == nil {
			rows[key] = &TokenRow{Kind: e.Kind, Model: e.Model, Effort: e.Effort}
		}
		rows[key].Events++
		rows[key].Tokens += e.Tokens
	}
	out := []TokenRow{}
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return a.Kind+"\x00"+a.Model+"\x00"+a.Effort < b.Kind+"\x00"+b.Model+"\x00"+b.Effort
	})
	return out
}
