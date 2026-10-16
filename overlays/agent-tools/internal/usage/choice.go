package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// Choice is Wesley's answer to the quota ask for a provider: "keep" going,
// or "postpone" the task, until the window that prompted it resets.
type Choice struct {
	Provider  string `json:"provider"`
	Choice    string `json:"choice"`
	Until     int64  `json:"until"`
	DecidedAt int64  `json:"decided_at"`
}

// Choices are the answers Choose takes.
var Choices = []string{"keep", "postpone"}

// Binding is the window a choice for r lasts for: the one used most at or
// over Warn, the later reset breaking a tie; else the one used most.
func Binding(r Reading) (Window, bool) {
	var best Window
	found := false
	for _, w := range r.Windows {
		if !found || w.Used > best.Used || (w.Used == best.Used && w.ResetsAt > best.ResetsAt) {
			best, found = w, true
		}
	}
	return best, found
}

// CheckChoice refuses a provider or choice Choose does not take.
func CheckChoice(provider, choice string) error {
	if !slices.Contains(Providers, provider) {
		return refusal.New("%q is not a provider. Pass one of %s", provider, strings.Join(Providers, ", "))
	}
	if !slices.Contains(Choices, choice) {
		return refusal.New("%q is not a choice. Pass %s", choice, strings.Join(Choices, " or "))
	}
	return nil
}

// Choose records choice for provider until its binding window resets, or
// for five hours when this host has no reading of it.
func (s State) Choose(provider, choice string, now time.Time) (Choice, error) {
	if err := CheckChoice(provider, choice); err != nil {
		return Choice{}, err
	}
	c := Choice{Provider: provider, Choice: choice, Until: now.Add(5 * time.Hour).Unix(), DecidedAt: now.Unix()}
	if r, ok := s.Reading(provider, now); ok {
		w, _ := Binding(r)
		c.Until = w.ResetsAt
	}
	return c, s.write("choice-"+provider+".json", c)
}

// Choice is the provider's recorded choice while it lasts.
func (s State) Choice(provider string, now time.Time) (Choice, bool) {
	if !providerName.MatchString(provider) {
		return Choice{}, false
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, "choice-"+provider+".json"))
	if err != nil {
		return Choice{}, false
	}
	var c Choice
	if json.Unmarshal(data, &c) != nil || c.Provider != provider || !slices.Contains(Choices, c.Choice) || c.Until <= now.Unix() {
		return Choice{}, false
	}
	return c, true
}

// mark is how the status line shows a choice, or "".
func (c Choice) mark(now time.Time) string {
	if c.Choice == "postpone" {
		return "wait→" + Clock(c.Until, now)
	}
	return ""
}

// QuestionText is the question a plan records for a choice, with the
// answers it may have.
type QuestionText struct {
	Text, Recommendation string
	Alternatives         []string
	// Answers are the recorded answers by choice, when Wesley's own words
	// are not given.
	Answers map[string]string
}

var (
	providerTitles = map[string]string{"claude": "Claude", "codex": "Codex"}
	windowTitles   = map[string]string{"5h": "5-hour", "7d": "weekly"}
)

// Title is the provider's name in a sentence.
func Title(provider string) string {
	if t, ok := providerTitles[provider]; ok {
		return t
	}
	return provider
}

// WindowTitle is the window's name in a sentence.
func WindowTitle(name string) string {
	if t, ok := windowTitles[name]; ok {
		return t
	}
	return name
}

// Question is the question for a choice about r, known being whether this
// host has a reading.
func Question(r Reading, known bool, now time.Time) QuestionText {
	w, ok := Binding(r)
	until := "for five hours"
	text := Title(r.Provider) + "'s quota is unknown on this host: keep going, or postpone the task for five hours?"
	if known && ok {
		until = "until " + Clock(w.ResetsAt, now)
		text = fmt.Sprintf("%s's %s window is at %d%%, resetting %s: keep going, or postpone the task until then?",
			Title(r.Provider), WindowTitle(w.Name), int(math.Floor(w.Used)), Clock(w.ResetsAt, now))
	}
	postpone := "Postpone the task " + until
	return QuestionText{
		Text:           text,
		Recommendation: "Keep going",
		Alternatives:   []string{postpone},
		Answers:        map[string]string{"keep": "Keep going", "postpone": postpone},
	}
}

// Summary is a provider's reading and choice, as `agent-plan usage`
// reports them.
type Summary struct {
	Provider string   `json:"provider"`
	Reading  *Reading `json:"reading,omitempty"`
	Choice   *Choice  `json:"choice,omitempty"`
}

// Summary is every provider's reading and choice.
func (s State) Summary(now time.Time) []Summary {
	var out []Summary
	for _, p := range Providers {
		sum := Summary{Provider: p}
		if r, ok := s.Reading(p, now); ok {
			sum.Reading = &r
		}
		if c, ok := s.Choice(p, now); ok {
			sum.Choice = &c
		}
		out = append(out, sum)
	}
	return out
}

// SummaryText is sums as lines of text.
func SummaryText(sums []Summary, now time.Time) string {
	var b strings.Builder
	for _, sum := range sums {
		var parts []string
		if sum.Reading != nil {
			var ws []string
			for _, w := range sum.Reading.Windows {
				ws = append(ws, fmt.Sprintf("%s %d%% resets %s", w.Name, int(math.Floor(w.Used)), Clock(w.ResetsAt, now)))
			}
			parts = append(parts, fmt.Sprintf("%s (seen %s)", strings.Join(ws, ", "), Clock(sum.Reading.ObservedAt, now)))
		} else {
			parts = append(parts, "unknown")
		}
		if sum.Choice != nil {
			parts = append(parts, fmt.Sprintf("choice %s until %s", sum.Choice.Choice, Clock(sum.Choice.Until, now)))
		}
		fmt.Fprintf(&b, "%s: %s\n", sum.Provider, strings.Join(parts, "; "))
	}
	return b.String()
}
