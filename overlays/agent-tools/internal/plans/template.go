package plans

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/refusal"
)

var templateName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// planSections are the level-2 sections a plan's file has, which a
// template may fill.
var planSections = []string{"Goal", "Steps", "Distribution", "Questions", "Follow-ups", "Verification"}

// template reads <plans repository>/templates/<name>.org: the lines under
// each of its level-2 headings, by heading. It refuses a section a plan
// does not have, and a heading keyword that is not a step's, since only
// log writes the others.
func (c Config) template(name string) (map[string][]string, error) {
	if !templateName.MatchString(name) {
		return nil, refusal.New("%q is not a template name. Pass the base name of a file in %s, as `--template release`", name, filepath.Join(c.repo(), "templates"))
	}
	file := filepath.Join(c.repo(), "templates", name+".org")
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refusal.New("there is no template %s. Ask Wesley to add it, or pass the base name of a file in %s", file, filepath.Dir(file))
	}
	if err != nil {
		return nil, err
	}
	text := strings.TrimRight(string(data), "\n")
	steps := fileKeywords("#+TODO: " + stepSequence)
	for _, h := range parseOrg(text) {
		if h.Keyword != "" && !steps.all[h.Keyword] {
			return nil, refusal.New("%s has a %s heading, a keyword outside a step's `%s`. Ask Wesley to change the template, or pass another with `--template`", file, h.Keyword, stepSequence)
		}
	}
	sections := map[string][]string{}
	current := ""
	for _, line := range strings.Split(text, "\n") {
		switch n := level(line); {
		case n == 1:
			return nil, refusal.New("%s has a level-1 heading, %q; a template holds only `**` sections of a plan. Ask Wesley to change the template, or pass another with `--template`", file, line)
		case n == 2:
			current = strings.TrimSpace(strings.TrimLeft(line, "*"))
			if !slices.Contains(planSections, current) {
				return nil, refusal.New("%s has a section %q, which a plan does not have (%s). Ask Wesley to change the template, or pass another with `--template`", file, current, strings.Join(planSections, ", "))
			}
		case current != "":
			sections[current] = append(sections[current], line)
		}
	}
	return sections, nil
}

// withTemplate is a plan's text with each section's template lines added at
// its end.
func withTemplate(text string, sections map[string][]string) string {
	lines := strings.Split(text, "\n")
	for _, name := range planSections {
		add := sections[name]
		_, end, ok := section(lines, name)
		if len(add) == 0 || !ok {
			continue
		}
		// A section ending the file ends at its final empty line.
		if end == len(lines) && lines[end-1] == "" {
			end--
		}
		lines = slices.Insert(lines, end, add...)
	}
	return strings.Join(lines, "\n")
}
