package review

import (
	"strings"
	"testing"
)

// A heading's section runs to the next heading of its level or above,
// outside code fences, in any case and with CRLF lines.
func TestSectionNamed(t *testing.T) {
	text := "# Title\n\n### reverting\n\nRevert it.\n\n```\n# not a heading\n```\n\n#### A subheading\n\nmore\n\n## Limits\n\nnone\n"
	got, ok := SectionNamed(text, "Reverting")
	if !ok || !strings.Contains(got, "Revert it.") || !strings.Contains(got, "# not a heading") ||
		!strings.Contains(got, "more") || strings.Contains(got, "none") {
		t.Errorf("section %v:\n%s", ok, got)
	}
	if _, ok := SectionNamed(text, "Verification beyond CI"); ok {
		t.Error("a heading the text lacks has a section")
	}
	if got, ok := SectionNamed("## Reverting ##\r\n\r\n- one\r\n\r\n## Next\r\n\r\n- no\r\n", "Reverting"); !ok ||
		strings.TrimSpace(got) != "- one" {
		t.Errorf("a closed heading with CRLF lines: %v %q", ok, got)
	}
	if got, ok := SectionNamed("## Reverting\n\n## Next\n", "Reverting"); !ok || strings.TrimSpace(got) != "" {
		t.Errorf("an empty section: %v %q", ok, got)
	}
	if got, ok := SectionNamed("intro\n\n## Reverting\n\nlast\n", "Reverting"); !ok || strings.TrimSpace(got) != "last" {
		t.Errorf("a section that ends the text: %v %q", ok, got)
	}
}

// A fence closes only on a line of its own character, at least as long as
// the one that opened it, so a heading inside it is never read.
func TestSectionFences(t *testing.T) {
	for name, text := range map[string]string{
		"a shorter fence inside": "````md\n```\n## Reverting\n\nexample\n```\n````\n\n## Reverting\n\nreal\n",
		"the other character":    "~~~\n```\n## Reverting\n\nexample\n~~~\n\n## Reverting\n\nreal\n",
		"text after the closer":  "```\n``` not a closer\n## Reverting\n\nexample\n```\n\n## Reverting\n\nreal\n",
	} {
		if got, ok := SectionNamed(text, "Reverting"); !ok || strings.TrimSpace(got) != "real" {
			t.Errorf("%s: %v %q", name, ok, got)
		}
	}
}

// A Reverting section of only a template's comment is no section, and a
// long one is cut short.
func TestRevertingSection(t *testing.T) {
	for name, text := range map[string]string{
		"only a comment":              "## Reverting\n\n<!-- say what\nreverting leaves behind -->\n\n## Next\n",
		"a comment holding a heading": "## Reverting\n<!--\n## Template\n\nfill this in\n-->\n",
		"the heading in a comment":    "intro\n<!--\n## Reverting\n\nexample\n-->\n",
		"an unterminated comment":     "## Reverting\n\n<!-- placeholder\n\nmore of it\n",
	} {
		if got, err := revertingSection(text, "it"); err == nil {
			t.Errorf("%s was taken: %q", name, got)
		}
	}
	got, err := revertingSection("## Reverting\n\n<!-- hint -->\nRevert it.\n", "it")
	if err != nil || strings.Contains(got, "hint") || !strings.Contains(got, "Revert it.") {
		t.Errorf("%v %q", err, got)
	}
	// A `<!--` in code is text, not a comment, so the section after it
	// stands; a comment after the code still goes.
	for name, text := range map[string]string{
		"in inline code":       "Templates open comments with `<!--`.\n\n## Reverting\n\nRevert it. <!-- hint -->\n",
		"in a fenced block":    "```html\n<!-- an example\n```\n\n## Reverting\n\nRevert it. <!-- hint -->\n",
		"in a tilde fence":     "~~~\n<!--\n~~~\n\n## Reverting\n\nRevert it. <!-- hint -->\n",
		"in a double span":     "Write ``a `<!--` b`` there.\n\n## Reverting\n\nRevert it. <!-- hint -->\n",
		"after a code comment": "`<!-- x -->` <!-- real\ncomment --> text\n\n## Reverting\n\nRevert it. <!-- hint -->\n",
	} {
		got, err := revertingSection(text, "it")
		if err != nil || !strings.Contains(got, "Revert it.") || strings.Contains(got, "hint") {
			t.Errorf("%s: %v %q", name, err, got)
		}
	}
	long := strings.Repeat("Reverting leaves nothing behind.\n", 2000)
	if got, err := revertingSection("## Reverting\n\n"+long, "it"); err != nil || len(got) > revertingCap+1024 {
		t.Errorf("%v: %d bytes", err, len(got))
	}
}
