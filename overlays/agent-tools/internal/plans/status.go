package plans

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Step is one step's progress.
type Step struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Part      string   `json:"part,omitempty"`
	Done      bool     `json:"done"`
	Verified  bool     `json:"verified"`
	Abandoned bool     `json:"abandoned,omitempty"`
	Commits   []string `json:"commits,omitempty"`
}

// Status is a plan's progress: its steps, which are the items whose IDs are
// S1, S2… or P1.S1…, against its log.
type Status struct {
	Plan  string `json:"plan"`
	Title string `json:"title"`
	State string `json:"status"`
	Steps []Step `json:"steps"`
	// NotDone are the steps with no done event, and DoneNotVerified those
	// with one but no verified event; abandoned steps are in neither.
	NotDone         []string `json:"notDone"`
	DoneNotVerified []string `json:"doneNotVerified"`
	// Implemented is Verified of Total, the steps not abandoned.
	Verified    int    `json:"verified"`
	Total       int    `json:"total"`
	Implemented string `json:"implemented"`
	// Ready are the parts that wait for nothing and have no started, done
	// or verified event, for them or their items; Waiting are the parts and
	// open questions that still wait, with what for.
	Ready   []string  `json:"ready"`
	Waiting []Waiting `json:"waiting"`
	// ReadySteps are the open steps that can be worked on now: none of
	// their AFTER items open, every earlier sibling settled under a parent
	// whose ORDERED property is t, and not scheduled for later, as Emacs's
	// agenda shows them.
	ReadySteps []string `json:"readySteps"`
	// KeywordDrift are the steps and parts whose heading keyword is not the
	// one their log gives them: changed in Emacs, or not written while
	// Emacs held unsaved changes to the file.
	KeywordDrift []Drift `json:"keywordDrift"`
}

// Drift is an item whose heading keyword, File, is not Log, the one its
// log gives it.
type Drift struct {
	ID   string `json:"id"`
	File string `json:"file"`
	Log  string `json:"log"`
}

// scheduledLater reports whether h is scheduled after now: for a
// timestamp with a time of day, after that time; for one without, on a
// later day, as Org's agenda reads it.
func scheduledLater(h heading, now time.Time) bool {
	if h.ScheduledHasTime {
		return h.Scheduled.After(now)
	}
	y, m, d := now.Date()
	return h.Scheduled.After(time.Date(y, m, d, 0, 0, 0, 0, now.Location()))
}

// readySteps are the plan's open steps, in file order, that can be worked
// on at now: see Status.ReadySteps.
func (p Plan) readySteps(isSettled func(string) bool, events []Event, now time.Time) ([]string, error) {
	files, err := p.files()
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		hs := parseOrg(string(data))
		for i, h := range hs {
			id := h.Props["ID"]
			if !stepPattern.MatchString(id) || planKeywords.done[stepState(events, id)] ||
				len(unsettled(afterIDs(h.Props), isSettled)) > 0 || scheduledLater(h, now) || !earlierSettled(hs, i, isSettled) {
				continue
			}
			out = append(out, id)
		}
	}
	return out, nil
}

// earlierSettled reports whether the siblings before hs[i] are settled,
// where its parent's ORDERED property says they must be.
func earlierSettled(hs []heading, i int, isSettled func(string) bool) bool {
	parent := i - 1
	for parent >= 0 && hs[parent].Level >= hs[i].Level {
		parent--
	}
	if parent < 0 || hs[parent].Props["ORDERED"] != "t" {
		return true
	}
	for j := parent + 1; j < i; j++ {
		if hs[j].Level != hs[i].Level {
			continue
		}
		if id := hs[j].Props["ID"]; id != "" && !isSettled(id) || id == "" && hs[j].Keyword != "" && !hs[j].Done {
			return false
		}
	}
	return true
}

// Status reads the plan and its log, now.
func (p Plan) Status() (Status, error) { return p.StatusAt(time.Now()) }

// StatusAt reads the plan and its log as at now.
func (p Plan) StatusAt(now time.Time) (Status, error) {
	list, err := p.itemList()
	if err != nil {
		return Status{}, err
	}
	events, err := p.Events()
	if err != nil {
		return Status{}, err
	}
	data, err := os.ReadFile(p.file(""))
	if err != nil {
		return Status{}, err
	}
	s := Status{Plan: p.file(""), Title: orgTitle(string(data)), Steps: []Step{}, NotDone: []string{},
		DoneNotVerified: []string{}, Ready: []string{}, Waiting: []Waiting{}, KeywordDrift: []Drift{}}
	open, err := p.Questions(true)
	if err != nil {
		return Status{}, err
	}
	isOpen := map[string]bool{}
	for _, q := range open {
		isOpen[q.ID] = true
	}
	isSettled := settled(list, events)
	if s.ReadySteps, err = p.readySteps(isSettled, events, now); err != nil {
		return Status{}, err
	}
	index := map[string]int{}
	for _, it := range list {
		if it.ID == p.Name {
			s.State = it.Props["STATUS"]
		}
		if partPattern.MatchString(it.ID) || isOpen[it.ID] {
			if on := unsettled(afterIDs(it.Props), isSettled); len(on) > 0 {
				s.Waiting = append(s.Waiting, Waiting{ID: it.ID, On: on})
			} else if partPattern.MatchString(it.ID) && !started(it.ID, events) {
				s.Ready = append(s.Ready, it.ID)
			}
		}
		if state := stepState(events, it.ID); projected(it.ID) && it.Keyword != state {
			s.KeywordDrift = append(s.KeywordDrift, Drift{ID: it.ID, File: it.Keyword, Log: state})
		}
		if !stepPattern.MatchString(it.ID) {
			continue
		}
		index[it.ID] = len(s.Steps)
		s.Steps = append(s.Steps, Step{ID: it.ID, Title: it.Title, Part: it.Part})
	}
	for _, e := range events {
		i, ok := index[e.Item]
		if !ok {
			continue
		}
		step := &s.Steps[i]
		switch e.Kind {
		case "done":
			step.Done, step.Abandoned = true, false
			if e.Commit != "" {
				step.Commits = append(step.Commits, e.Commit)
			}
		case "verified":
			step.Verified = true
		case "abandoned":
			step.Abandoned = true
		}
	}
	for _, step := range s.Steps {
		switch {
		case step.Abandoned:
			continue
		case !step.Done:
			s.NotDone = append(s.NotDone, step.ID)
		case !step.Verified:
			s.DoneNotVerified = append(s.DoneNotVerified, step.ID)
		default:
			s.Verified++
		}
		s.Total++
	}
	s.Implemented = fmt.Sprintf("%d/%d", s.Verified, s.Total)
	return s, nil
}

// row is an Org table row; a cell's | is written as \vert.
func row(cells ...string) string {
	var b strings.Builder
	b.WriteString("|")
	for _, c := range cells {
		if c == "" {
			b.WriteString(" |")
		} else {
			b.WriteString(" " + strings.ReplaceAll(c, "|", `\vert{}`) + " |")
		}
	}
	return b.String() + "\n"
}

func yes(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func list(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

// Org is s as Org: a table of steps, and what is left.
func (s Status) Org() string {
	var b strings.Builder
	fmt.Fprintf(&b, "* Status of %s\n", s.Title)
	b.WriteString(row("Step", "Title", "Done", "Verified", "Commit"))
	b.WriteString("|-\n")
	for _, step := range s.Steps {
		done := yes(step.Done)
		if step.Abandoned {
			done = "abandoned"
		}
		b.WriteString(row(step.ID, step.Title, done, yes(step.Verified), strings.Join(step.Commits, "; ")))
	}
	fmt.Fprintf(&b, "\n- Status: %s\n- Implemented: %s verified\n- Without done: %s\n- Done, not verified: %s\n",
		s.State, s.Implemented, list(s.NotDone), list(s.DoneNotVerified))
	fmt.Fprintf(&b, "- Parts ready to start: %s\n", list(s.Ready))
	fmt.Fprintf(&b, "- Steps ready: %s\n", list(s.ReadySteps))
	if len(s.Waiting) > 0 {
		var waits []string
		for _, w := range s.Waiting {
			waits = append(waits, w.ID+" for "+strings.Join(w.On, ", "))
		}
		fmt.Fprintf(&b, "- Waiting: %s\n", strings.Join(waits, "; "))
	}
	if len(s.KeywordDrift) > 0 {
		var drifts []string
		for _, d := range s.KeywordDrift {
			file := d.File
			if file == "" {
				file = "none"
			}
			drifts = append(drifts, fmt.Sprintf("%s (file %s, log %s)", d.ID, file, d.Log))
		}
		fmt.Fprintf(&b, "- Keyword drift: %s\n", strings.Join(drifts, "; "))
	}
	return b.String()
}
