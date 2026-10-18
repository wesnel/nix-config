// Package digest writes, model-free, the operator's standup digest as Org,
// from plan-dash export's documents (every host's plans, and this host's
// view of GitHub and of its plans' sync) and this host's quota readings,
// and decides when a scheduled run is due.
//
// The digest is committed to the notes repository and shown on the Agent
// plans page, so it holds only what those documents hold, and of GitHub no
// title: keys, states, counts, times and the links built from them. Plan
// text (titles and ready steps) appears only as lines of an example block
// under its plan's item, with Org's comma escaping, so Org parses none of
// it: no link, inline source block or call, macro, export snippet,
// headline, keyword or drawer. The flake's digest-inert check proves it
// with Org itself: it writes the digest of TestInert's corpus of such text
// and runs testdata/inert.el on it.
package digest

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/usage"
)

// Late is how long after its slot a run still writes a digest. A timer
// with Persistent=true catches up at boot on a run it missed, which may be
// days late.
const Late = 4 * time.Hour

// Slot is when a digest is due: each weekday, at Hour:Minute in Zone.
type Slot struct {
	Hour, Minute int
	Zone         *time.Location
}

// ParseSlot reads at, "HH:MM", and zone, an IANA name.
func ParseSlot(at, zone string) (Slot, error) {
	t, err := time.Parse("15:04", at)
	if err != nil {
		return Slot{}, fmt.Errorf("%q is not a time of day as HH:MM", at)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "" || zone == "Local" {
		return Slot{}, fmt.Errorf("%q is not a time zone's IANA name", zone)
	}
	return Slot{Hour: t.Hour(), Minute: t.Minute(), Zone: loc}, nil
}

// on is the slot's time on the day of t, in Zone.
func (s Slot) on(t time.Time) time.Time {
	y, m, d := t.In(s.Zone).Date()
	return time.Date(y, m, d, s.Hour, s.Minute, 0, 0, s.Zone)
}

func weekend(t time.Time) bool { return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday }

// latest is the newest weekday slot at or before t.
func (s Slot) latest(t time.Time) time.Time {
	at := s.on(t)
	if at.After(t) {
		at = s.on(at.AddDate(0, 0, -1))
	}
	for weekend(at) {
		at = s.on(at.AddDate(0, 0, -1))
	}
	return at
}

// Due is the slot a run at now writes the digest of, and the slot before
// it, which the digest counts changes since; ok is false, with why, on a
// weekend in Zone, or more than Late after the newest weekday slot.
func (s Slot) Due(now time.Time) (at, previous time.Time, why string, ok bool) {
	if weekend(now.In(s.Zone)) {
		return time.Time{}, time.Time{}, "a weekend in " + s.Zone.String(), false
	}
	at = s.latest(now)
	if now.Sub(at) > Late {
		return time.Time{}, time.Time{}, fmt.Sprintf("more than %s after the slot at %s", Late, at.Format(time.RFC3339)), false
	}
	return at, s.latest(at.Add(-time.Second)), "", true
}

// File is the path in the notes repository of host's digest for the slot
// at. Each host writes only its own, as with plans, so two hosts never
// publish the same file.
func File(kind, host string, at time.Time) (string, error) {
	if !hostName.MatchString(host) || !word.MatchString(kind) {
		return "", fmt.Errorf("%q, %q: not a host and a kind of digest for a file name", host, kind)
	}
	return at.Format("digests/2006/01-02-") + kind + "." + host + ".org", nil
}

// Inputs are what a standup is written from.
type Inputs struct {
	Export export.Export
	// Quota are this host's last readings, those it has.
	Quota []usage.Reading
	// At is the slot, Previous the one before it, and Now when it runs.
	At, Previous, Now time.Time
}

// The caps on what a digest lists. With export's caps on text, they keep
// a digest within the 600 KiB export allows a document, so the Today
// document can carry it (TestSize).
const (
	maxPlans = 60
	maxReady = 5
)

var (
	slug     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	word     = regexp.MustCompile(`^[a-z][a-z-]{0,30}$`)
	window   = regexp.MustCompile(`^[0-9]{1,6}[a-z]$`)
	planID   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}-[a-z0-9-]+\.[A-Za-z0-9_-]+$`)
	hostName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	spaces   = regexp.MustCompile(`\s+`)
	// escaped is what Org's comma escaping, org-escape-code-in-string,
	// prefixes with a comma in a block: a line starting, after any commas,
	// with a star or "#+".
	escaped = regexp.MustCompile(`^,*(\*|#\+)`)
)

// Standup is the standup digest of in, as Org.
func Standup(in Inputs) string {
	zone := in.At.Location()
	var b strings.Builder
	fmt.Fprintf(&b, "#+title: Standup %s\n", in.At.Format("2006-01-02 Mon"))
	fmt.Fprintf(&b, "# Written by plan-dash digest standup at %s, from plan-dash export's documents and this host's quota readings.\n",
		stamp(in.Now, zone))
	plans(&b, in, zone)
	github(&b, in.Export.GitHub, zone)
	quota(&b, in.Quota, zone)
	syncState(&b, in.Export.Sync, zone)
	return b.String()
}

func plans(b *strings.Builder, in Inputs, zone *time.Location) {
	fmt.Fprintf(b, "\n* Plans\nEvery host's plans not halted, from the notes repository as of %s.", stamp(in.Now, zone))
	if len(in.Export.Failed) > 0 {
		fmt.Fprintf(b, " %d could not be read.", len(in.Export.Failed))
	}
	b.WriteString("\n")
	var changed, rest []export.Plan
	for _, p := range in.Export.Plans {
		if !planID.MatchString(p.ID) {
			continue
		}
		if t, err := time.Parse(time.RFC3339, p.Updated); err == nil && t.After(in.Previous) {
			changed = append(changed, p)
		} else {
			rest = append(rest, p)
		}
	}
	listed := 0
	section := func(heading string, list []export.Plan) {
		fmt.Fprintf(b, "** %s\n", heading)
		if len(list) == 0 {
			b.WriteString("None.\n")
		}
		for i, p := range list {
			if listed == maxPlans {
				fmt.Fprintf(b, "%d more not listed.\n", len(list)-i)
				break
			}
			listed++
			planItem(b, p, zone)
		}
	}
	section(fmt.Sprintf("Changed since %s", stamp(in.Previous, zone)), changed)
	section("Unchanged", rest)
}

// planItem writes p as a list item of its ID and states, all of known
// shapes, the ID and host verbatim, followed by an example block of its
// text.
func planItem(b *strings.Builder, p export.Plan, zone *time.Location) {
	h := p.Host
	if !hostName.MatchString(h) {
		h = "unknown"
	}
	fmt.Fprintf(b, "- =%s= :: %s, on =%s=", p.ID, enum(p.Status), h)
	if t, err := time.Parse(time.RFC3339, p.Updated); err == nil {
		fmt.Fprintf(b, ", updated %s", stamp(t, zone))
	}
	done := 0
	var ready []string
	for _, s := range p.Steps {
		switch {
		case s.Keyword == "DONE" || s.Keyword == "VERIFIED":
			done++
		case s.Ready:
			ready = append(ready, s.Title)
		}
	}
	fmt.Fprintf(b, "; steps %d of %d done", done, len(p.Steps))
	if n := len(p.Questions); n > 0 {
		fmt.Fprintf(b, "; %s open for the operator", count(n, "question", "questions"))
	}
	b.WriteString("\n  #+begin_example\n")
	block(b, p.Title)
	for i, s := range ready {
		if i == maxReady {
			fmt.Fprintf(b, "  and %d more ready\n", len(ready)-maxReady)
			break
		}
		block(b, "ready: "+s)
	}
	b.WriteString("  #+end_example\n")
}

// block writes s on one line of an example block, comma-escaped as Org
// escapes a block's contents.
func block(b *strings.Builder, s string) {
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	if escaped.MatchString(s) {
		s = "," + s
	}
	fmt.Fprintf(b, "  %s\n", s)
}

func github(b *strings.Builder, g export.GitHub, zone *time.Location) {
	b.WriteString("\n* Pull requests\n")
	at, err := time.Parse(time.RFC3339, g.At)
	switch {
	case err != nil:
		b.WriteString("GitHub: no data, the fetch failed.\n")
		return
	case g.Stale:
		fmt.Fprintf(b, "GitHub as of %s, stale: the last fetch failed.\n", stamp(at, zone))
	default:
		fmt.Fprintf(b, "GitHub as of %s.\n", stamp(at, zone))
	}
	b.WriteString("** Mine\n")
	n := 0
	for _, p := range g.Mine {
		if !slug.MatchString(p.Repo) || p.Number <= 0 {
			continue
		}
		n++
		draft := "ready"
		if p.Draft {
			draft = "draft"
		}
		fmt.Fprintf(b, "- %s :: %s, CI %s, review %s, %s\n", link(p.Repo, p.Number), draft, enum(p.CI), enum(p.Review),
			count(p.Unresolved, "unresolved thread", "unresolved threads"))
	}
	shown(b, n, g.MineTotal)
	b.WriteString("** Reviews asked of the operator\n")
	n = 0
	for _, r := range g.Requests {
		if !slug.MatchString(r.Repo) || r.Number <= 0 {
			continue
		}
		n++
		who := "directly"
		if r.Team {
			who = "of a team"
		}
		fmt.Fprintf(b, "- %s :: %s", link(r.Repo, r.Number), who)
		if t, err := time.Parse(time.RFC3339, r.Requested); err == nil {
			fmt.Fprintf(b, " since %s", stamp(t, zone))
		}
		draft := ""
		if r.Draft {
			draft = ", draft"
		}
		fmt.Fprintf(b, "%s, CI %s, reviewed by pr-loop: %s\n", draft, enum(r.CI), enum(r.Record))
	}
	shown(b, n, g.RequestsTotal)
}

func shown(b *strings.Builder, n, total int) {
	switch {
	case n == 0 && total == 0:
		b.WriteString("None.\n")
	case n < total:
		fmt.Fprintf(b, "%d of %d shown.\n", n, total)
	}
}

func quota(b *strings.Builder, readings []usage.Reading, zone *time.Location) {
	b.WriteString("\n* Quota\n")
	if len(readings) == 0 {
		b.WriteString("No reading on this host with a window yet to reset.\n")
		return
	}
	for _, r := range readings {
		if !word.MatchString(r.Provider) {
			continue
		}
		fmt.Fprintf(b, "- %s :: observed %s", r.Provider, stamp(time.Unix(r.ObservedAt, 0), zone))
		for _, w := range r.Windows {
			if !window.MatchString(w.Name) {
				continue
			}
			fmt.Fprintf(b, "; %s %.0f%% used, resets %s", w.Name, w.Used, stamp(time.Unix(w.ResetsAt, 0), zone))
		}
		b.WriteString("\n")
	}
}

func syncState(b *strings.Builder, s export.Sync, zone *time.Location) {
	b.WriteString("\n* Plan sync\n")
	h := "this host"
	if hostName.MatchString(s.Host) {
		h = "=" + s.Host + "="
	}
	if !s.Repo {
		fmt.Fprintf(b, "The plans directory of %s is in no notes repository.\n", h)
		return
	}
	pushed := "never"
	if t, err := time.Parse(time.RFC3339, s.Pushed); err == nil {
		pushed = stamp(t, zone)
	}
	fetched := "never"
	if t, err := time.Parse(time.RFC3339, s.Fetched); err == nil {
		fetched = stamp(t, zone)
	}
	changes := "none unsynced"
	if s.Unsynced {
		changes = "changes not yet pushed"
	}
	fmt.Fprintf(b, "The plans of %s last reached the remote %s, %s; last fetched %s.\n", h, pushed, changes, fetched)
}

// enum is v where it is a lower-case word, else "unknown".
func enum(v string) string {
	if word.MatchString(v) {
		return v
	}
	return "unknown"
}

func link(repo string, number int) string {
	return fmt.Sprintf("[[https://github.com/%s/pull/%d][%s#%d]]", repo, number, repo, number)
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// stamp is t as an inactive Org timestamp in zone.
func stamp(t time.Time, zone *time.Location) string {
	return t.In(zone).Format("[2006-01-02 Mon 15:04]")
}
