package export

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"html/template"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

// Mirror renders a self-contained, sanitized snapshot of one plan, or every
// host's open items when planFile is empty. It reads local sources only and
// refuses incomplete reads or HTML exceeding MaxSnapshot; lists are never capped.
func Mirror(c plans.Config, planFile string, now time.Time) ([]byte, error) {
	if _, err := os.ReadDir(c.Dir); err != nil {
		return nil, err
	}
	var all []plans.Plan
	if planFile != "" {
		p, err := c.Open(planFile)
		if err != nil {
			return nil, err
		}
		all = []plans.Plan{p}
	} else {
		var failed []string
		var err error
		all, failed, err = c.Plans()
		if err != nil {
			return nil, err
		}
		if len(failed) > 0 {
			return nil, fmt.Errorf("mirror incomplete: %d plan sources could not be read", len(failed))
		}
	}
	titles, err := c.WorkstreamTitles()
	if err != nil {
		return nil, err
	}
	sync, err := SyncOf(c, now)
	if err != nil {
		return nil, err
	}
	doc := mirrorDocument{Title: "All open plan items", Collected: stamp(now), Host: mirrorText(c.Host), Fetched: sync.Fetched, Sources: len(all)}
	if planFile != "" {
		doc.Title = "Current plan"
	}
	for _, p := range all {
		out, err := mirrorPlanOf(c, p, titles, now, planFile != "")
		if err != nil {
			return nil, fmt.Errorf("mirror incomplete: %w", err)
		}
		if planFile != "" || len(out.Steps)+len(out.Parts)+len(out.Questions)+len(out.Followups)+len(out.Waiting) > 0 {
			doc.Plans = append(doc.Plans, out)
		}
	}
	var b bytes.Buffer
	if err := mirrorTemplate.Execute(&b, doc); err != nil {
		return nil, err
	}
	if b.Len() > MaxSnapshot {
		return nil, fmt.Errorf("mirror exceeds the 256 KiB visualization limit (%d bytes)", b.Len())
	}
	return b.Bytes(), nil
}

type mirrorDocument struct {
	Title, Collected, Host, Fetched string
	Sources                         int
	Plans                           []mirrorPlan
}

type mirrorPlan struct {
	ID, Title, Goal, Status, Host, Updated, Source, Workstream string
	Steps                                                      []mirrorStep
	Parts                                                      []mirrorStep
	Questions                                                  []mirrorQuestion
	Decisions                                                  []Decision
	Followups                                                  []Followup
	Waiting                                                    []mirrorWait
}

type mirrorTiming struct{ After, Scheduled string }
type mirrorStep struct {
	Step
	mirrorTiming
}
type mirrorQuestion struct {
	Question
	mirrorTiming
	Ready bool
}
type mirrorWait struct{ ID, Title string }

func mirrorText(s string) string { return text(s, int(^uint(0)>>1)) }

func timingOf(t plans.ItemTiming) mirrorTiming {
	o := mirrorTiming{After: mirrorText(strings.Join(t.After, ", "))}
	if !t.Scheduled.IsZero() {
		layout := "2006-01-02"
		if t.ScheduledHasTime {
			layout += " 15:04"
		}
		o.Scheduled = t.Scheduled.Format(layout)
	}
	return o
}

func mirrorPlanOf(c plans.Config, p plans.Plan, titles map[string]string, now time.Time, full bool) (mirrorPlan, error) {
	st, err := p.StatusAt(now)
	if err != nil {
		return mirrorPlan{}, err
	}
	out := mirrorPlan{ID: mirrorText(p.Name), Title: mirrorText(st.Title), Status: st.State, Host: mirrorText(p.Host), Source: "Local log event"}
	if !state.MatchString(out.Status) {
		out.Status = "unknown"
	}
	if !p.Own {
		out.Source = "Last arrived log commit"
	}
	if out.Updated, err = updated(c, p); err != nil {
		return out, err
	}
	ws, err := p.Workstream()
	if err != nil {
		return out, err
	}
	if titles[ws.ID] != "" {
		ws.Title = titles[ws.ID]
	}
	out.Workstream = mirrorText(ws.Title)
	timings, err := p.ItemTiming()
	if err != nil {
		return out, err
	}
	steps, err := p.InSection("Steps")
	if err != nil {
		return out, err
	}
	out.Steps = mirrorSteps(st, steps, timings, full)
	out.Parts, err = mirrorParts(p, st, timings, full)
	if err != nil {
		return out, err
	}
	qs, err := p.Questions(true)
	if err != nil {
		return out, err
	}
	out.Questions = mirrorQuestions(qs, timings, now)
	if full {
		out.Goal, out.Decisions, err = mirrorDetails(p)
		if err != nil {
			return out, err
		}
	}
	items, err := p.OpenItems(now)
	if err != nil {
		return out, err
	}
	for _, it := range items {
		switch it.Kind {
		case "follow-up":
			out.Followups = append(out.Followups, Followup{ID: mirrorText(it.Item), Title: mirrorText(it.Title)})
		case "waiting":
			title := "Hand-back pending"
			if it.Sign != "" {
				title = "Signing pending"
			}
			out.Waiting = append(out.Waiting, mirrorWait{ID: mirrorText(it.Item), Title: title})
		}
	}
	return out, nil
}

func mirrorSteps(st plans.Status, steps map[string]bool, timings map[string]plans.ItemTiming, full bool) []mirrorStep {
	var out []mirrorStep
	for _, s := range st.Steps {
		if !steps[s.ID] || !full && (s.Verified || s.Abandoned) {
			continue
		}
		keyword := s.Keyword
		if !keywords[keyword] {
			keyword = "unknown"
		}
		out = append(out, mirrorStep{Step: Step{ID: mirrorText(s.ID), Title: mirrorText(s.Title), Keyword: keyword, Ready: slices.Contains(st.ReadySteps, s.ID)}, mirrorTiming: timingOf(timings[s.ID])})
	}
	return out
}

func mirrorParts(p plans.Plan, st plans.Status, timings map[string]plans.ItemTiming, full bool) ([]mirrorStep, error) {
	parts, err := p.Distribution()
	if err != nil {
		return nil, err
	}
	var out []mirrorStep
	for _, part := range parts {
		if !full && slices.Contains([]string{"DONE", "VERIFIED", "ABANDONED"}, part.Keyword) {
			continue
		}
		out = append(out, mirrorStep{Step: Step{ID: mirrorText(part.ID), Title: mirrorText(part.Title), Keyword: part.Keyword, Ready: slices.Contains(st.Ready, part.ID)}, mirrorTiming: timingOf(timings[part.ID])})
	}
	return out, nil
}

func mirrorQuestions(qs []plans.Question, timings map[string]plans.ItemTiming, now time.Time) []mirrorQuestion {
	var out []mirrorQuestion
	for _, q := range qs {
		mq := mirrorQuestion{Question: Question{ID: mirrorText(q.ID), Text: mirrorText(q.Question), Context: mirrorText(q.Context)}, mirrorTiming: timingOf(timings[q.ID]), Ready: len(q.Waiting) == 0}
		if priority.MatchString(q.Priority) {
			mq.Priority = q.Priority
		}
		if t := timings[q.ID]; !t.Scheduled.IsZero() {
			cutoff := now
			if !t.ScheduledHasTime {
				y, m, d := now.Date()
				cutoff = time.Date(y, m, d, 0, 0, 0, 0, now.Location())
			}
			mq.Ready = mq.Ready && !t.Scheduled.After(cutoff)
		}
		for i, a := range append([]string{q.Recommendation}, q.Alternatives...) {
			if a == "" {
				continue
			}
			label, why, _ := strings.Cut(a, " :: ")
			mq.Options = append(mq.Options, Option{Label: mirrorText(label), Why: mirrorText(why), Recommended: i == 0})
		}
		out = append(out, mq)
	}
	return out
}

func mirrorDetails(p plans.Plan) (string, []Decision, error) {
	goal, err := p.Goal("")
	if err != nil {
		return "", nil, err
	}
	ds, err := p.Decisions(true)
	if err != nil {
		return "", nil, err
	}
	var decisions []Decision
	for _, d := range ds {
		decisions = append(decisions, Decision{ID: mirrorText(d.ID), Question: mirrorText(d.Question), Answer: mirrorText(d.Answer)})
	}
	return mirrorText(strings.TrimSpace(goal)), decisions, nil
}

//go:embed mirror.html
var mirrorHTML string

var mirrorTemplate = template.Must(template.New("mirror").Parse(mirrorHTML))

// MirrorRevision identifies rendered content while ignoring only the marked
// collection timestamp. Source timestamps and the template remain significant.
func MirrorRevision(html []byte) string {
	if matches := collectionStamp.FindAllIndex(html, -1); len(matches) == 1 {
		at := matches[0]
		copy := make([]byte, 0, len(html))
		copy = append(copy, html[:at[0]]...)
		copy = append(copy, `Collected <time id="collected"></time>`...)
		html = append(copy, html[at[1]:]...)
	}
	return fmt.Sprintf("%x", sha256.Sum256(html))
}

var collectionStamp = regexp.MustCompile(`Collected <time id="collected">[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:Z|[+-][0-9]{2}:[0-9]{2})</time>`)
