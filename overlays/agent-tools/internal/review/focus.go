package review

import (
	_ "embed"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// securityChecks are the security review's checks, checklist and output,
// which replace the general contract's. Its categories, and its rules to
// report only what the change adds and to follow input to where it is
// used, are adapted from the security review prompt Claude Code bundles,
// github.com/anthropics/claude-code-security-review, under its MIT
// License, Copyright (c) 2025 Anthropic.
//
//go:embed security.md
var securityChecks string

// focus is a focused review: the checks that replace the general
// contract's, the categories of its checklist a reply must name as
// checked, and the kinds of finding it adds to the general ones.
type focus struct {
	checks     string
	categories []string
	kinds      []string
}

// focuses are the focused reviews, by route; each route of routes.Known
// has one.
var focuses = map[string]focus{
	"security": newFocus(securityChecks, "vulnerability"),
}

// Categories are the security checklist's categories, which a security
// reply names as checked.
var Categories = focuses["security"].categories

// category is a checklist entry: "- `name`: what to check".
var category = regexp.MustCompile("(?m)^- `([a-z-]+)`:")

// newFocus is the focus of checks, whose categories are the entries under
// its Checklist heading.
func newFocus(checks string, kinds ...string) focus {
	f := focus{checks: checks, kinds: kinds}
	_, list, _ := strings.Cut(checks, "## Checklist\n")
	list, _, _ = strings.Cut(list, "\n## ")
	for _, m := range category.FindAllStringSubmatch(list, -1) {
		f.categories = append(f.categories, m[1])
	}
	return f
}

// generalRules is the general contract up to its Checks: who the reviewer
// is, what it is given, and its rules, which every focus keeps.
func generalRules() string {
	return Contract[:strings.Index(Contract, "## Checks")]
}

// contract is the focused reviewer's instructions.
func (f focus) contract() string { return generalRules() + f.checks }

// schema is the JSON schema of a focused reply: the general one's
// findings, of its kinds too, and unverified, with the categories it
// checked in place of routes.
func (f focus) schema() []byte {
	var s map[string]any
	if err := json.Unmarshal(Schema, &s); err != nil {
		panic(err)
	}
	props := s["properties"].(map[string]any)
	delete(props, "routes")
	kind := props["findings"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)
	kind["enum"] = append(kind["enum"].([]any), anys(f.kinds)...)
	props["checked"] = map[string]any{"type": "array", "description": "Each category of the Checklist gone through.",
		"items": map[string]any{"type": "string", "enum": f.categories}}
	s["required"] = []string{"findings", "unverified", "checked"}
	out, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return out
}

func anys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// unchecked are the categories a reply's checked leaves out.
func (f focus) unchecked(checked []string) []string {
	var out []string
	for _, c := range f.categories {
		if !slices.Contains(checked, c) {
			out = append(out, c)
		}
	}
	return out
}

// focusNamed is the focus name names, or a refusal naming those there are.
func focusNamed(name string) (focus, error) {
	f, ok := focuses[name]
	if !ok {
		names := slices.Sorted(maps.Keys(focuses))
		return focus{}, refusal.New("%q is not a focus of review. Pass `--focus %s`", name, strings.Join(names, "` or `--focus "))
	}
	return f, nil
}
