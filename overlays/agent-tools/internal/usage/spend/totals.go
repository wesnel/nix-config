package spend

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"text/tabwriter"
	"time"
)

// Totals are tokens by kind as a cost-state records them, which does not
// split cache writes by lifetime.
type Totals struct {
	Input      int64 `json:"input"`
	CacheWrite int64 `json:"cacheWrite"`
	CacheRead  int64 `json:"cacheRead"`
	Output     int64 `json:"output"`
}

// Total is every kind summed.
func (t Totals) Total() int64 { return t.Input + t.CacheWrite + t.CacheRead + t.Output }

func (t *Totals) add(u Totals) {
	t.Input += u.Input
	t.CacheWrite += u.CacheWrite
	t.CacheRead += u.CacheRead
	t.Output += u.Output
}

// share is t's share that part is of all, kind by kind: a cost-state's
// tokens of each kind spread over the period as the records of that kind
// are. A kind the records lack spreads as their total does.
func (t Totals) share(part, all Totals) Totals {
	whole := 0.0
	if all.Total() > 0 {
		whole = float64(part.Total()) / float64(all.Total())
	}
	s := func(n, p, a int64) int64 {
		f := whole
		if a > 0 {
			f = float64(p) / float64(a)
		}
		return int64(math.Round(float64(n) * f))
	}
	return Totals{Input: s(t.Input, part.Input, all.Input), CacheWrite: s(t.CacheWrite, part.CacheWrite, all.CacheWrite),
		CacheRead: s(t.CacheRead, part.CacheRead, all.CacheRead), Output: s(t.Output, part.Output, all.Output)}
}

func (t Tokens) totals() Totals {
	return Totals{Input: t.Input, CacheWrite: t.cacheWrite(), CacheRead: t.CacheRead, Output: t.Output}
}

// Coverage is, per kind, the percent of the recorded totals the
// per-message records account for.
type Coverage struct {
	Input      float64 `json:"input"`
	CacheWrite float64 `json:"cacheWrite"`
	CacheRead  float64 `json:"cacheRead"`
	Output     float64 `json:"output"`
}

// TotalRow is one label's sessions' totals in the period.
type TotalRow struct {
	Label    string `json:"label"`
	Sessions int    `json:"sessions"`
	// Live are the sessions still running: with no cost-state, whose
	// per-message records stand in, or with records after their last
	// one, which count on top of it.
	Live int `json:"live"`
	// Short are the sessions whose last cost-state holds fewer tokens than
	// the per-message records before it, which then stand in.
	Short  int    `json:"short"`
	Tokens Totals `json:"tokens"`
	Total  int64  `json:"total"`
	// Previous is the label's total in the previous period.
	Previous int64 `json:"previous"`
	// Covered is over the cost-states counted; nil when none is.
	Covered *Coverage `json:"covered,omitempty"`
}

// ModelRow is one model's sessions' totals in the period.
type ModelRow struct {
	Model  string `json:"model"`
	Tokens Totals `json:"tokens"`
	Total  int64  `json:"total"`
}

// costState is a session's totals by model from the last
// `type:"cost-state"` line in its transcript, which Claude Code writes at
// each end or /clear: cumulative since the session started, resumes
// included, its sub-agents' requests and every model's too, which the
// per-message records are not. Its outputTokens include thinkingTokens.
// Its dollar figures are never read.
//
// It holds what the session did before it was written: its own records on
// lines before it, and every record, a sub-agent's too, made before the
// time of the first line after it (next), the resumed session's first;
// with no line after it, every record. A sub-agent still at work when its
// session ends is counted only as far as the cost-state counts it.
type costState struct {
	models map[string]Totals
	// line is its line in the transcript, from 1.
	line int
	// start is the session's start, where a cost-state with no records
	// before it counts.
	start, next time.Time
}

// covers reports whether the cost-state holds q.
func (c costState) covers(q *request) bool {
	if q.agent == "" && q.line > 0 {
		return q.line < c.line
	}
	return c.next.IsZero() || q.at.Before(c.next)
}

func (c costState) total() Totals {
	var t Totals
	for _, u := range c.models {
		t.add(u)
	}
	return t
}

var costStateType = []byte(`"type":"cost-state"`)

// modelName is what a model's name must be to become a label: Claude
// Code's names, with a context-size suffix such as [1m].
var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:_\[\]-]{0,99}$`)

func label(model string) string {
	if modelName.MatchString(model) {
		return model
	}
	return Unknown
}

// parseCostState reads a cost-state line, and its session ID.
func parseCostState(line []byte) (string, costState, bool) {
	var in struct {
		Type       string `json:"type"`
		SessionID  string `json:"sessionId"`
		StartTime  int64  `json:"startTime"`
		ModelUsage map[string]struct {
			Input      int64 `json:"inputTokens"`
			Output     int64 `json:"outputTokens"`
			CacheRead  int64 `json:"cacheReadInputTokens"`
			CacheWrite int64 `json:"cacheCreationInputTokens"`
		} `json:"modelUsage"`
	}
	if json.Unmarshal(line, &in) != nil || in.Type != "cost-state" {
		return "", costState{}, false
	}
	c := costState{models: map[string]Totals{}}
	if in.StartTime > 0 {
		c.start = time.UnixMilli(in.StartTime)
	}
	for model, u := range in.ModelUsage {
		t := c.models[label(model)]
		t.add(Totals{Input: u.Input, CacheWrite: u.CacheWrite, CacheRead: u.CacheRead, Output: u.Output})
		c.models[label(model)] = t
	}
	return in.SessionID, c, true
}

// sessionTotal is a session's whole total and how it was counted.
type sessionTotal struct {
	total       int64
	live, short bool
}

// records are a session's per-message tokens: over its life, in the
// period, in the previous one, and in the period by model.
type records struct {
	all, now, before Totals
	models           map[string]Totals
}

func (p *records) add(q labelled, t Totals, from, since, until time.Time) {
	p.all.add(t)
	switch {
	case !q.at.Before(since) && q.at.Before(until):
		p.now.add(t)
		if p.models == nil {
			p.models = map[string]Totals{}
		}
		m := p.models[q.model]
		m.add(t)
		p.models[q.model] = m
	case !q.at.Before(from) && q.at.Before(since):
		p.before.add(t)
	}
}

// sessionTotals fills r's Totals, TotalsAll and Models, and returns each
// session's whole total. A session's cost-state counts in a period kind
// by kind, in proportion to the records before it that were made in the
// period; records after it count as they are.
func sessionTotals(r *Report, reqs []labelled, states map[string]costState, from, since, until time.Time) map[string]sessionTotal {
	type sess struct {
		label     string
		labelAt   time.Time
		labelMain bool
		// covered are the records the cost-state, if any, holds; after
		// are those it does not.
		covered, after records
	}
	by := map[string]*sess{}
	var order []string
	for _, q := range reqs {
		if q.session == Unknown {
			continue
		}
		s := by[q.session]
		if s == nil {
			s = &sess{}
			by[q.session] = s
			order = append(order, q.session)
		}
		main := q.agent == ""
		if s.labelAt.IsZero() || main && !s.labelMain || main == s.labelMain && q.at.Before(s.labelAt) {
			s.label, s.labelAt, s.labelMain = q.labels[r.By], q.at, main
		}
		p := &s.covered
		if state, ok := states[q.session]; ok && !state.covers(q.request) {
			p = &s.after
		}
		p.add(q, q.tokens.totals(), from, since, until)
	}

	whole := map[string]sessionTotal{}
	rows := map[string]*TotalRow{}
	covered := map[string]*[2]Totals{}
	all := &TotalRow{Label: "total"}
	allCovered := &[2]Totals{}
	models := map[string]*ModelRow{}
	for _, id := range order {
		s := by[id]
		state, recorded := states[id]
		sum := state.total()
		short := recorded && sum.Total() < s.covered.all.Total()
		recorded = recorded && !short

		// The records stand in, unless a cost-state holds them.
		now, before, inModels := s.covered.now, s.covered.before, s.covered.models
		var stateNow Totals
		if recorded {
			// Spread as the records it holds were made, or with none, all
			// at the session's start.
			spread := func(t, part Totals, from, until time.Time) Totals {
				switch {
				case s.covered.all.Total() > 0:
					return t.share(part, s.covered.all)
				case !state.start.IsZero() && !state.start.Before(from) && state.start.Before(until):
					return t
				}
				return Totals{}
			}
			stateNow, before = spread(sum, s.covered.now, since, until), spread(sum, s.covered.before, from, since)
			now = stateNow
			inModels = map[string]Totals{}
			for model, t := range state.models {
				inModels[model] = spread(t, s.covered.now, since, until)
			}
		}
		now.add(s.after.now)
		before.add(s.after.before)
		live := !recorded && !short || s.after.all.Total() > 0
		if recorded {
			whole[id] = sessionTotal{total: sum.Total() + s.after.all.Total(), live: live}
		} else {
			whole[id] = sessionTotal{total: s.covered.all.Total() + s.after.all.Total(), live: live, short: short}
		}

		targets := []*TotalRow{all}
		covers := []*[2]Totals{allCovered}
		if r.By != "role" {
			if rows[s.label] == nil {
				rows[s.label], covered[s.label] = &TotalRow{Label: s.label}, &[2]Totals{}
			}
			targets, covers = append(targets, rows[s.label]), append(covers, covered[s.label])
		}
		for i, x := range targets {
			x.Previous += before.Total()
			if now.Total() == 0 {
				continue
			}
			x.Sessions++
			switch {
			case short:
				x.Short++
			case live:
				x.Live++
			}
			if recorded {
				covers[i][0].add(s.covered.now)
				covers[i][1].add(stateNow)
			}
			x.Tokens.add(now)
			x.Total += now.Total()
		}
		for _, m := range []map[string]Totals{inModels, s.after.models} {
			for model, t := range m {
				if models[model] == nil {
					models[model] = &ModelRow{Model: model}
				}
				models[model].Tokens.add(t)
				models[model].Total += t.Total()
			}
		}
	}

	coverage := func(c *[2]Totals) *Coverage {
		if c[1].Total() == 0 {
			return nil
		}
		pct := func(part, whole int64) float64 {
			if whole == 0 {
				return 0
			}
			return 100 * float64(part) / float64(whole)
		}
		return &Coverage{Input: pct(c[0].Input, c[1].Input), CacheWrite: pct(c[0].CacheWrite, c[1].CacheWrite),
			CacheRead: pct(c[0].CacheRead, c[1].CacheRead), Output: pct(c[0].Output, c[1].Output)}
	}
	all.Covered = coverage(allCovered)
	r.TotalsAll = *all
	r.Totals = []TotalRow{}
	for label, x := range rows {
		if x.Sessions == 0 && x.Previous == 0 {
			continue
		}
		x.Covered = coverage(covered[label])
		r.Totals = append(r.Totals, *x)
	}
	sort.Slice(r.Totals, func(i, j int) bool {
		a, b := r.Totals[i], r.Totals[j]
		if (a.Label == Unknown) != (b.Label == Unknown) {
			return b.Label == Unknown
		}
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.Label < b.Label
	})
	r.Models = []ModelRow{}
	for _, m := range models {
		if m.Total > 0 {
			r.Models = append(r.Models, *m)
		}
	}
	sort.Slice(r.Models, func(i, j int) bool {
		if r.Models[i].Total != r.Models[j].Total {
			return r.Models[i].Total > r.Models[j].Total
		}
		return r.Models[i].Model < r.Models[j].Model
	})
	return whole
}

func (c *Coverage) text() string {
	if c == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%% / %.0f%% / %.0f%% / %.0f%%", c.Input, c.CacheWrite, c.CacheRead, c.Output)
}

// totalsText writes the session totals and the models.
func (r Report) totalsText(w io.Writer) {
	fmt.Fprintf(w, "\nSession totals by %s\n\n", r.By)
	t := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(t, "%s\tsessions\tstill running\tshort\tinput\tcache write\tcache read\toutput\ttotal\tvs previous\tbreakdown covers\t\n",
		r.By)
	for _, x := range append(append([]TotalRow{}, r.Totals...), r.TotalsAll) {
		fmt.Fprintf(t, "%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", x.Label, x.Sessions, x.Live, x.Short, count(x.Tokens.Input),
			count(x.Tokens.CacheWrite), count(x.Tokens.CacheRead), count(x.Tokens.Output), count(x.Total),
			change(x.Total, x.Previous), x.Covered.text())
	}
	t.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Session totals come from each session's last cost-state, its sub-agents and every model included,")
	fmt.Fprintln(w, "scaled to the period kind by kind, by the share of the records before it made in it. A session still")
	fmt.Fprintln(w, "running has no cost-state yet, and its per-message records stand in, or has records after its last")
	fmt.Fprintln(w, "one, which count on top of it as they are. A short one's cost-state holds fewer tokens than the")
	fmt.Fprintln(w, "records before it, which stand in. breakdown covers: the share of input / cache write / cache read /")
	fmt.Fprintln(w, "output of the cost-states counted that the records before them account for.")
	if r.By == "role" {
		fmt.Fprintln(w, "A cost-state is a whole session's, so by role there is only the total.")
	}

	if len(r.Models) > 0 {
		fmt.Fprintf(w, "\nModels, from the session totals\n\n")
		t = tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintln(t, "model\tinput\tcache write\tcache read\toutput\ttotal\t")
		for _, m := range r.Models {
			fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\t\n", m.Model, count(m.Tokens.Input), count(m.Tokens.CacheWrite),
				count(m.Tokens.CacheRead), count(m.Tokens.Output), count(m.Total))
		}
		t.Flush()
	}
}
