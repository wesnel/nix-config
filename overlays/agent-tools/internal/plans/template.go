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
var planSections = []string{"Goal", "Steps", "Distribution", "Questions", "Follow-ups", "References", "Verification"}

// templateDirs are the directories a template is looked for in, first
// found first: the plans repository's, then the built-in one.
func (c Config) templateDirs() []string {
	dirs := []string{filepath.Join(c.repo(), "templates")}
	if c.Templates != "" {
		dirs = append(dirs, c.Templates)
	}
	return dirs
}

// template reads <name>.org from the first of templateDirs that has it: the
// lines under each of its level-2 headings, by heading. It refuses a
// section a plan does not have, a heading keyword that is not a step's,
// since only log writes the others, and anything else that would start the
// plan with structure drift, such as an entry without its ID.
func (c Config) template(name string) (map[string][]string, error) {
	dirs := strings.Join(c.templateDirs(), " or ")
	if !templateName.MatchString(name) {
		return nil, refusal.New("%q is not a template name. Pass the base name of a file in %s, as `--template release`", name, dirs)
	}
	var file string
	var data []byte
	found := false
	for _, dir := range c.templateDirs() {
		var err error
		file = filepath.Join(dir, name+".org")
		data, err = os.ReadFile(file)
		if err == nil {
			found = true
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	if !found {
		return nil, refusal.New("there is no template %s.org in %s. Ask the operator to add it, or pass the base name of a file there", name, dirs)
	}
	text := strings.TrimRight(string(data), "\n")
	steps := fileKeywords("#+TODO: " + stepSequence)
	for _, h := range parseOrg(text) {
		if h.Keyword != "" && !steps.all[h.Keyword] {
			return nil, refusal.New("%s has a %s heading, a keyword outside a step's `%s`. Ask the operator to change the template, or pass another with `--template`", file, h.Keyword, stepSequence)
		}
	}
	sections := map[string][]string{}
	current := ""
	keywords := fileKeywords(text)
	for _, line := range strings.Split(text, "\n") {
		switch n := level(line); {
		case n == 1:
			return nil, refusal.New("%s has a level-1 heading, %q; a template holds only `**` sections of a plan. Ask the operator to change the template, or pass another with `--template`", file, line)
		case n == 2:
			current = lineTitle(line, keywords)
			if !slices.Contains(planSections, current) {
				return nil, refusal.New("%s has a section %q, which a plan does not have (%s). Ask the operator to change the template, or pass another with `--template`", file, current, strings.Join(planSections, ", "))
			}
		case current != "":
			sections[current] = append(sections[current], line)
		}
	}
	if drift := fileDrift(withTemplate(planText(NewOptions{Title: name}, name, c.Host, nil, true), sections), false); len(drift) > 0 {
		return nil, refusal.New("%s would start a plan with structure drift: `%s`, %s. Each entry needs its ID in CUSTOM_ID, and a question is asked "+
			"with `agent-plan ask` once the plan exists. Ask the operator to change the template, or pass another with `--template`",
			file, drift[0].heading, drift[0].problem)
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
