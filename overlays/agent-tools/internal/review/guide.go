package review

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/record"
)

// guideContract is the guide's instructions, in place of a reviewer's
// contract, and guideSchema the JSON Schema of its reply.
//
//go:embed guide.md
var guideContract string

//go:embed guide.schema.json
var guideSchema []byte

// Chunk is one part of the guide's walkthrough, as a review prints it.
type Chunk struct {
	Title   string     `json:"title"`
	Summary string     `json:"summary"`
	Hunks   []ShowHunk `json:"hunks"`
}

// diffHunks are the hunks of `git diff BASE TIP` for r, as a reader of it
// sees them, by the lines of each side.
func diffHunks(r *span) ([]record.Hunk, error) {
	out, err := git.Bytes(nil, "-C", r.top, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
		r.base, r.tip)
	if err != nil {
		return nil, err
	}
	var hunks []record.Hunk
	var path, oldPath string
	// A file's --- and +++ lines come between its diff --git line and its
	// first hunk; after that, a line starting so is a removed or added one.
	header := false
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			path, oldPath, header = "", "", true
		case header && strings.HasPrefix(line, "--- "):
			oldPath = diffPath(strings.TrimPrefix(line, "--- "), "a/")
		case header && strings.HasPrefix(line, "+++ "):
			path = diffPath(strings.TrimPrefix(line, "+++ "), "b/")
			if path == "/dev/null" {
				path = oldPath
			}
		case strings.HasPrefix(line, "@@ "):
			header = false
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil || path == "" {
				continue
			}
			h := record.Hunk{Path: path, OldStart: atoi(m[1]), OldLines: count(m[2]), NewStart: atoi(m[3]), NewLines: count(m[4])}
			hunks = append(hunks, h)
		}
	}
	return hunks, nil
}

// diffPath is a --- or +++ line's path without its prefix: git ends the
// line with a tab where the path holds a space, and writes a path holding
// a quote, a backslash, a control or a non-ASCII byte in C's quoting,
// which Go's reads.
func diffPath(text, prefix string) string {
	text = strings.TrimSuffix(text, "\t")
	if strings.HasPrefix(text, `"`) {
		if unquoted, err := strconv.Unquote(text); err == nil {
			text = unquoted
		}
	}
	return strings.TrimPrefix(text, prefix)
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// count is a hunk header's line count, which git leaves out for one line.
func count(s string) int {
	if s == "" {
		return 1
	}
	return atoi(s)
}

var citation = regexp.MustCompile(`^file:(.+)::([0-9]+)@([0-9a-f]{7,40})$`)

// cited is the hunk of hunks a citation names: by its path and a line of
// it at TIP, or at BASE for one that only removes lines; -1 for none.
func cited(cite string, hunks []record.Hunk, r *span) int {
	m := citation.FindStringSubmatch(strings.TrimSpace(cite))
	if m == nil {
		return -1
	}
	path, line, sha := m[1], atoi(m[2]), m[3]
	atTip, atBase := strings.HasPrefix(r.tip, sha), strings.HasPrefix(r.base, sha)
	for i, h := range hunks {
		if h.Path != path {
			continue
		}
		switch {
		case atTip && h.NewLines > 0 && line >= h.NewStart && line < h.NewStart+h.NewLines:
			return i
		case atBase && line >= h.OldStart && line < h.OldStart+max(h.OldLines, 1):
			return i
		}
	}
	return -1
}

// validateGuide reads the guide's reply on r: each chunk with the hunks it
// cites, in order. A citation of no hunk of the diff, a hunk cited again,
// a chunk left with no hunk and a hunk no chunk cites go to Dropped.
func validateGuide(reply string, r *span) (*Output, error) {
	if _, err := checkReply(guideSchema, []byte(reply)); err != nil {
		return nil, fmt.Errorf("the reply does not fit its schema: %v", err)
	}
	var parsed struct {
		Chunks []struct {
			Title   string   `json:"title"`
			Summary string   `json:"summary"`
			Hunks   []string `json:"hunks"`
		} `json:"chunks"`
		Unverified []string `json:"unverified"`
	}
	if err := json.Unmarshal([]byte(reply), &parsed); err != nil {
		return nil, fmt.Errorf("the reply is not the JSON the contract describes (%v); ask the guide again", err)
	}
	hunks, err := diffHunks(r)
	if err != nil {
		return nil, err
	}
	out := &Output{Range: r.given, Tip: r.tip, Findings: []*Finding{}, Dropped: []Dropped{}, Unverified: parsed.Unverified,
		Walkthrough: []Chunk{}}
	if out.Unverified == nil {
		out.Unverified = []string{}
	}
	drop := func(what any, why string) {
		raw, _ := json.Marshal(what)
		out.Dropped = append(out.Dropped, Dropped{Finding: raw, Reason: why})
	}
	taken := make([]bool, len(hunks))
	for _, c := range parsed.Chunks {
		chunk := Chunk{Title: c.Title, Summary: c.Summary, Hunks: []ShowHunk{}}
		for _, cite := range c.Hunks {
			switch i := cited(cite, hunks, r); {
			case i < 0:
				drop(cite, "cites no hunk of `git diff BASE TIP`")
			case taken[i]:
				drop(cite, "cites a hunk an earlier chunk cites")
			default:
				taken[i] = true
				h := hunks[i]
				chunk.Hunks = append(chunk.Hunks, ShowHunk{Path: h.Path, NewStart: h.NewStart, NewLines: h.NewLines,
					OldStart: h.OldStart, OldLines: h.OldLines})
			}
		}
		if len(chunk.Hunks) == 0 {
			drop(c, "the chunk cites no hunk of the diff")
			continue
		}
		out.Walkthrough = append(out.Walkthrough, chunk)
	}
	for i, h := range hunks {
		if !taken[i] {
			drop(ShowHunk{Path: h.Path, NewStart: h.NewStart, NewLines: h.NewLines, OldStart: h.OldStart, OldLines: h.OldLines},
				"no chunk cites this hunk")
		}
	}
	return out, nil
}

// walkthroughOf is a walkthrough as the record keeps it, its chunks
// numbered C1 on.
func walkthroughOf(chunks []Chunk) []record.Chunk {
	var out []record.Chunk
	for i, c := range chunks {
		rc := record.Chunk{ID: "C" + strconv.Itoa(i+1), Title: c.Title, Summary: c.Summary}
		for _, h := range c.Hunks {
			rc.Hunks = append(rc.Hunks, record.Hunk{Path: h.Path, NewStart: h.NewStart, NewLines: h.NewLines,
				OldStart: h.OldStart, OldLines: h.OldLines})
		}
		out = append(out, rc)
	}
	return out
}
