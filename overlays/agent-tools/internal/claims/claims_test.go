package claims

import (
	"slices"
	"testing"
)

const body = `Title line

## Approach

- not a claim: the approach

## Verification beyond CI

- ` + "`go test ./...`" + ` passes in overlays/agent-tools,
  with the module's tests.
- The package builds:

      nix build .#agent-tools

1. a numbered claim

A paragraph claim
that wraps.

` + "```" + `
## not a heading, inside a fence
` + "```" + `

### A subsection stays in

- a claim under it

## Reverting

- not a claim: reverting
`

// The section runs from its heading to the next heading of its level or
// above, outside code fences; each top-level item, with what continues it,
// and each other paragraph or block is a claim, and a subheading none.
func TestOf(t *testing.T) {
	section, ok := Section(body)
	if !ok || slices.Contains(Parse(section), "not a claim: reverting") {
		t.Fatalf("section %v:\n%s", ok, section)
	}
	want := []string{
		"`go test ./...` passes in overlays/agent-tools,\nwith the module's tests.",
		"The package builds:\n\n    nix build .#agent-tools",
		"a numbered claim",
		"A paragraph claim\nthat wraps.",
		"```\n## not a heading, inside a fence\n```",
		"a claim under it",
	}
	if got := Of(body); !slices.Equal(got, want) {
		t.Errorf("Of:\n%q\nwant\n%q", got, want)
	}
	if _, ok := Section("# Title\n\nno claims\n"); ok {
		t.Error("a text without the heading has a section")
	}
	if got := Of("- one\n- two\n"); !slices.Equal(got, []string{"one", "two"}) {
		t.Errorf("a report without the heading: %q", got)
	}
	if got := Of("## verification beyond ci ##\n\n- lower case\n"); !slices.Equal(got, []string{"lower case"}) {
		t.Errorf("the heading in another case: %q", got)
	}
	if got := Of("## Verification beyond CI\n\n## Next\n"); len(got) != 0 {
		t.Errorf("an empty section: %q", got)
	}
	if got := Of("## Verification beyond CI\r\n\r\n- one\r\n- two\r\n\r\n## Next\r\n\r\n- no\r\n"); !slices.Equal(got, []string{"one", "two"}) {
		t.Errorf("CRLF lines: %q", got)
	}
}

// A placeholder, or a template's comment, under the heading is no claim.
func TestPlaceholders(t *testing.T) {
	for _, section := range []string{"None.", "_none_", "- None", "*None.*", "N/A", "`none`", "<!-- what you ran -->",
		"<!--\nList what you ran\nbeyond CI.\n-->", "None.\n\n<!-- template -->\n"} {
		if got := Of("## Verification beyond CI\n\n" + section + "\n\n## Next\n\nmore\n"); len(got) != 0 {
			t.Errorf("%q gives claims %q", section, got)
		}
	}
	if got := Of("## Verification beyond CI\n\n<!-- template -->\n- None of the tests fail\n"); !slices.Equal(got, []string{"None of the tests fail"}) {
		t.Errorf("a claim beside a comment: %q", got)
	}
}

// The hash is the claims', whatever their wrapping, and changes with what
// any of them says or with their order.
func TestHash(t *testing.T) {
	a := Hash(Of("## Verification beyond CI\n\n- one two\n- three\n"))
	if a == "" || len(a) != 64 {
		t.Fatalf("hash %q", a)
	}
	if b := Hash(Of("intro\n\n## Verification beyond CI\n\n- one\n  two\n- three\n\n## Limits\n\nmore\n")); b != a {
		t.Errorf("rewrapping changed the hash")
	}
	for _, other := range []string{"- one two\n- four\n", "- three\n- one two\n", "- one two\n"} {
		if Hash(Of(other)) == a {
			t.Errorf("%q has the same hash", other)
		}
	}
}
