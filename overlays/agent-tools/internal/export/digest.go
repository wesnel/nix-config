package export

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/diagram"
)

// The demo digest's format, as the export reads it: Org, with a level-1
// heading per change, whose property drawer holds PLAN (an id: link) and
// PR (OWNER/NAME#N, or the pull request's GitHub URL). Under it, each
// verified demo step is a sh source block followed by its result in an
// example block; each diagram a mermaid source block, after an optional
// #+caption: line, in the grammar diagram.ValidSource takes; each
// recording or screenshot an Org link to an https URL. Text before the
// first change is the digest's summary; a change's text is its steps'
// (the paragraph just before each) or its summary. Anything else the
// format has no place for is dropped with the line it starts on and why.
// A digest whose level-1 headings carry no PR or PLAN is free text, which
// the page shows as Text alone.

// The most of each list a digest shows.
const (
	maxChanges        = 20
	maxDemoSteps      = 20
	maxDigestDiagrams = 5
	maxDigestLinks    = 20
	maxDropped        = 30
)

// The caps on a digest's text: a result in runes, a URL and a diagram's
// source in bytes, the latter what the page's renderer takes.
const (
	resultCap  = 4000
	maxURL     = 2000
	maxMermaid = 20000
)

var (
	changeHeading = regexp.MustCompile(`^\*(?:[ \t]+(.*))?$`)
	headingTags   = regexp.MustCompile(`[ \t]+:[A-Za-z0-9_@#%:]+:[ \t]*$`)
	planning      = regexp.MustCompile(`^[ \t]*(?:CLOSED|SCHEDULED|DEADLINE):`)
	drawerStart   = regexp.MustCompile(`^[ \t]*:PROPERTIES:[ \t]*$`)
	drawerEnd     = regexp.MustCompile(`^[ \t]*:END:[ \t]*$`)
	property      = regexp.MustCompile(`^[ \t]*:([A-Za-z0-9_-]+):(?:[ \t]+(.*))?$`)
	blockStart    = regexp.MustCompile(`(?i)^([ \t]*)#\+begin_([A-Za-z0-9_-]+)(?:[ \t]+([^ \t]+))?`)
	captionLine   = regexp.MustCompile(`(?i)^[ \t]*#\+caption:[ \t]*(.*)$`)
	keywordLine   = regexp.MustCompile(`^[ \t]*#\+`)
	commentLine   = regexp.MustCompile(`^[ \t]*#(?:[ \t]|$)`)
	orgLink       = regexp.MustCompile(`\[\[([^\[\]]+)\](?:\[([^\[\]]*)\])?\]`)
	prShort       = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99})#([1-9][0-9]{0,9})$`)
	prURL         = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99})/pull/([1-9][0-9]{0,9})/?$`)
)

// digestOf is a digest of kind read as data, at most maxDigestFile+1
// bytes, of which the first maxDigestFile are shown.
func digestOf(kind string, data []byte) Digest {
	over := len(data) > maxDigestFile
	if over {
		data = data[:maxDigestFile]
	}
	d := Digest{Kind: kind, Text: text(strings.TrimSpace(string(data)), digestCap), Changes: []Change{}, Dropped: []Dropped{}}
	p := digestParser{lines: strings.Split(text(string(data), math.MaxInt), "\n")}
	if over {
		p.drop(0, "digest", fmt.Sprintf("over the %d KiB the export reads; the rest is not shown", maxDigestFile>>10))
	}
	if p.parse() {
		d.Summary, d.Changes, d.Dropped = p.summary, p.changes, p.dropped
	}
	return d
}

// digestParser parses a digest's lines, stripped and redacted.
type digestParser struct {
	lines   []string
	summary string
	changes []Change
	dropped []Dropped
}

// drop records that what starts on line, 1-based, is left out for why.
func (p *digestParser) drop(line int, what, why string) {
	if len(p.dropped) < maxDropped {
		p.dropped = append(p.dropped, Dropped{Line: line, What: what, Why: why})
	}
}

// parse fills p from its lines, and reports whether any change has a PR or
// PLAN property, without which the digest is free text.
func (p *digestParser) parse() bool {
	if p.dropped == nil {
		p.dropped = []Dropped{}
	}
	p.changes = []Change{}
	structured := false
	starts := []int{}
	for i, l := range p.lines {
		if changeHeading.MatchString(l) {
			starts = append(starts, i)
		}
	}
	end := len(p.lines)
	if len(starts) > 0 {
		end = starts[0]
	}
	var intro body
	p.body(0, end, &intro, nil)
	p.summary = cut(intro.summary(), long)
	for k, start := range starts {
		end := len(p.lines)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		c, marked := p.change(start, end)
		structured = structured || marked
		if len(p.changes) == maxChanges {
			p.drop(start+1, "change", fmt.Sprintf("over the %d changes a digest shows", maxChanges))
			continue
		}
		p.changes = append(p.changes, c)
	}
	return structured
}

// change is the change of lines [start, end), the first its heading, and
// whether it has a PR or PLAN property.
func (p *digestParser) change(start, end int) (Change, bool) {
	m := changeHeading.FindStringSubmatch(p.lines[start])
	c := Change{Title: cut(strings.TrimSpace(headingTags.ReplaceAllString(m[1], "")), short), Steps: []DemoStep{},
		Diagrams: []DigestDiagram{}, Links: []DigestLink{}}
	i := start + 1
	for i < end && planning.MatchString(p.lines[i]) {
		i++
	}
	marked := false
	if i < end && drawerStart.MatchString(p.lines[i]) {
		i, marked = p.changeProperties(i, end, &c)
	}
	var b body
	p.body(i, end, &b, &c)
	c.Summary = cut(b.summary(), long)
	return c, marked
}

func (p *digestParser) changeProperties(i, end int, c *Change) (int, bool) {
	marked := false
	for j := i + 1; j < end; j++ {
		if drawerEnd.MatchString(p.lines[j]) {
			return j + 1, marked
		}
		pm := property.FindStringSubmatch(p.lines[j])
		if pm == nil {
			continue
		}
		switch strings.ToUpper(pm[1]) {
		case "PLAN":
			marked = true
		case "PR":
			marked = true
			c.Repo, c.Number = pullRequest(strings.TrimSpace(pm[2]))
			if c.Repo == "" {
				p.drop(j+1, "pr", "not OWNER/NAME#N or a pull request's GitHub URL")
			}
		}
	}
	p.drop(i+1, "block", "its property drawer is never closed")
	return end, marked
}

// pullRequest is the repository and number s names, as OWNER/NAME#N or
// the pull request's GitHub URL, or "" and 0.
func pullRequest(s string) (string, int) {
	m := prShort.FindStringSubmatch(s)
	if m == nil {
		m = prURL.FindStringSubmatch(s)
	}
	if m == nil {
		return "", 0
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0
	}
	return m[1], n
}

// body is the text of a change, or of what comes before the first, as it
// is read: the paragraphs kept as its summary, and those not yet placed.
type body struct {
	kept    []string
	pending []string
}

// paragraphs are b's pending lines as paragraphs, each its lines joined,
// blank lines between them.
func (b *body) paragraphs() []string {
	var out []string
	var cur []string
	for _, l := range append(b.pending, "") {
		if strings.TrimSpace(l) == "" {
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, "\n"))
				cur = nil
			}
			continue
		}
		cur = append(cur, strings.TrimRight(l, " \t"))
	}
	b.pending = nil
	return out
}

// flush keeps every pending paragraph as summary.
func (b *body) flush() { b.kept = append(b.kept, b.paragraphs()...) }

// last keeps the pending paragraphs but the last as summary, and is that
// last one, "" where none is pending.
func (b *body) last() string {
	ps := b.paragraphs()
	if len(ps) == 0 {
		return ""
	}
	b.kept = append(b.kept, ps[:len(ps)-1]...)
	return ps[len(ps)-1]
}

func (b *body) summary() string {
	b.flush()
	return strings.Join(b.kept, "\n\n")
}

// body reads lines [i, end) into b and, for a change, c's steps, diagrams
// and links; outside a change, c is nil and links are dropped.
func (p *digestParser) body(i, end int, b *body, c *Change) {
	caption := ""
	for i < end {
		line := p.lines[i]
		if strings.TrimSpace(line) == "" {
			b.pending = append(b.pending, "")
			i++
			continue
		}
		if m := captionLine.FindStringSubmatch(line); m != nil {
			caption = m[1]
			i++
			continue
		}
		m := blockStart.FindStringSubmatch(line)
		if m == nil {
			caption = ""
			if !keywordLine.MatchString(line) && !commentLine.MatchString(line) {
				b.pending = append(b.pending, p.links(i, line, c))
			}
			i++
			continue
		}
		next, ok := p.bodyBlock(i, end, b, c, caption, m)
		if !ok {
			return
		}
		caption = ""
		i = next
	}
}

func (p *digestParser) bodyBlock(i, end int, b *body, c *Change, caption string, m []string) (int, bool) {
	kind, lang := strings.ToLower(m[2]), strings.ToLower(m[3])
	what := "block"
	switch {
	case kind == "src" && lang == "sh":
		what = "step"
	case kind == "src" && lang == "mermaid":
		what = "diagram"
	}
	content, next, ok := p.block(i, end, m[1], kind)
	if !ok {
		p.drop(i+1, what, "its block is never closed")
		b.flush()
		return next, false
	}
	switch {
	case c != nil && what == "step":
		result, after, ok := p.result(next, end)
		if !ok {
			p.drop(i+1, "step", "its result's block is never closed")
			b.flush()
			return after, false
		}
		text := b.last()
		if len(c.Steps) == maxDemoSteps {
			p.drop(i+1, "step", fmt.Sprintf("over the %d steps a change shows", maxDemoSteps))
		} else {
			c.Steps = append(c.Steps, DemoStep{Text: cut(text, long), Command: cut(content, long), Result: cut(result, resultCap)})
		}
		next = after
	case c != nil && what == "diagram":
		b.flush()
		source := content + "\n"
		switch {
		case len(source) > maxMermaid:
			p.drop(i+1, "diagram", fmt.Sprintf("over the %d bytes the page draws", maxMermaid))
		case !diagram.ValidSource(source):
			p.drop(i+1, "diagram", "not in the one grammar the page draws")
		case len(c.Diagrams) == maxDigestDiagrams:
			p.drop(i+1, "diagram", fmt.Sprintf("over the %d diagrams a change shows", maxDigestDiagrams))
		default:
			c.Diagrams = append(c.Diagrams, DigestDiagram{Caption: cut(caption, short), Source: source})
		}
	case kind == "example":
		b.flush()
		p.drop(i+1, "block", "a result with no step before it")
	default:
		b.flush()
		p.drop(i+1, "block", "not a block the digest's format has")
	}
	return next, true
}

// block is the content of the block of kind that starts on line i and is
// indented by indent, and the line after its end; or false where it does
// not end before end. A line's leading comma that Org escapes a heading or
// a keyword with is taken off, as is the block's own indentation.
func (p *digestParser) block(i, end int, indent, kind string) (string, int, bool) {
	closing := regexp.MustCompile(`(?i)^[ \t]*#\+end_` + regexp.QuoteMeta(kind) + `[ \t]*$`)
	var out []string
	for j := i + 1; j < end; j++ {
		if closing.MatchString(p.lines[j]) {
			return strings.TrimRight(strings.Join(out, "\n"), "\n"), j + 1, true
		}
		l := strings.TrimPrefix(p.lines[j], indent)
		if t := strings.TrimLeft(l, " \t"); strings.HasPrefix(t, ",*") || strings.HasPrefix(t, ",#+") {
			l = l[:len(l)-len(t)] + t[1:]
		}
		out = append(out, l)
	}
	return "", end, false
}

// result is the content of the example block that follows a step at line
// i, past blank lines, and the line after it: "" and i where none follows,
// or false where it never ends.
func (p *digestParser) result(i, end int) (string, int, bool) {
	j := i
	for j < end && strings.TrimSpace(p.lines[j]) == "" {
		j++
	}
	if j == end {
		return "", i, true
	}
	m := blockStart.FindStringSubmatch(p.lines[j])
	if m == nil || strings.ToLower(m[2]) != "example" {
		return "", i, true
	}
	content, next, ok := p.block(j, end, m[1], "example")
	return content, next, ok
}

// links is line with each Org link put as its caption, or its target where
// it has none. Each link to an https URL goes into c's links; others, and
// every link outside a change, are dropped.
func (p *digestParser) links(i int, line string, c *Change) string {
	return orgLink.ReplaceAllStringFunc(line, func(s string) string {
		m := orgLink.FindStringSubmatch(s)
		target, caption := m[1], m[2]
		if caption == "" {
			caption = target
		}
		switch {
		case !httpsURL(target):
			p.drop(i+1, "link", "not an https URL")
		case c == nil:
			p.drop(i+1, "link", "outside a change, where the page shows no links")
		case len(c.Links) == maxDigestLinks:
			p.drop(i+1, "link", fmt.Sprintf("over the %d links a change shows", maxDigestLinks))
		default:
			c.Links = append(c.Links, DigestLink{URL: target, Caption: cut(caption, short)})
		}
		return caption
	})
}

// httpsURL reports whether s is an absolute https URL with a host and no
// user, of at most maxURL bytes, holding no space, quote or angle bracket.
func httpsURL(s string) bool {
	if len(s) > maxURL || !strings.HasPrefix(s, "https://") || strings.ContainsAny(s, " \t\"'<>\\") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Host != "" && u.User == nil
}

// cut is s cut to max runes.
func cut(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}
