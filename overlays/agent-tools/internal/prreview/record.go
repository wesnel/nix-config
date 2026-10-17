package prreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/review"
)

// TextCap is the most of any text from a reviewer or the VM a record keeps.
const TextCap = 8 << 10

// clean strips control characters but newline and tab, which a terminal or
// an editor could act on, and cuts text to at most limit bytes.
func clean(text string, limit int) string {
	text = strings.ToValidUTF8(text, "�")
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0)) {
			return r
		}
		return -1
	}, text)
	if len(text) <= limit {
		return text
	}
	const cut = "\n[cut]"
	end := limit - len(cut)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + cut
}

// Outcomes a finding is settled with. A finding on someone else's pull
// request is only ever proposed as a comment, never fixed; needs-operator
// leaves it for the operator to decide. Records keep these values, so
// renaming one means rewriting every record that holds it.
var outcomes = map[string]bool{"propose": true, "rebut": true, "duplicate-of": true, "needs-operator": true}

// Finding is a reviewer's finding as the record keeps it, for the operator
// to edit and post: severity from its kind, body its claim then its
// evidence, and the outcome triage settles it with.
type Finding struct {
	review.Finding
	// Round is the review round that found it; settle names it as
	// ROUND.ID, such as 2.F1.
	Round       int     `json:"round"`
	Severity    string  `json:"severity"`
	Body        string  `json:"body"`
	Outcome     *string `json:"outcome"`
	DuplicateOf *string `json:"duplicateOf,omitempty"`
}

var severities = map[string]string{
	"bug": "issue", "regression": "issue", "goal-unmet": "issue", "convention": "issue",
	"unresolved": "suggestion", "untested": "suggestion", "untrue-doc": "suggestion",
	"out-of-scope": "question",
}

// Dropped is a reviewer's finding left out, cut to TextCap, and why.
type Dropped struct {
	Finding string `json:"finding"`
	Reason  string `json:"reason"`
}

// Round is one review round: who reviewed, and what they found.
type Round struct {
	Round      int        `json:"round"`
	Reviewer   string     `json:"reviewer"`
	Model      *string    `json:"model"`
	Findings   []*Finding `json:"findings"`
	Dropped    []Dropped  `json:"dropped"`
	Unverified []string   `json:"unverified"`
}

// Record is a review of one head of a pull request: what review-guide in
// Emacs imports. Only pr-loop writes it. Its round fields are the latest
// round's, Round 0 before the first; Prior holds the earlier ones, oldest
// first, as a reviewer's --prior takes them.
type Record struct {
	Version  int     `json:"version"`
	Repo     string  `json:"repo"`
	PR       int     `json:"pr"`
	URL      string  `json:"url"`
	Clone    string  `json:"clone"`
	Base     string  `json:"base"`
	Head     string  `json:"head"`
	Title    string  `json:"title"`
	Round    int     `json:"round"`
	Reviewer string  `json:"reviewer"`
	Model    *string `json:"model"`
	Vet      any     `json:"vet"`
	Ran      bool    `json:"ran"`
	// NotRunBecause says why the pull request's code did not run.
	NotRunBecause string     `json:"notRunBecause,omitempty"`
	RedCheck      any        `json:"redCheck"`
	Tests         any        `json:"tests"`
	Findings      []*Finding `json:"findings"`
	Dropped       []Dropped  `json:"dropped"`
	Unverified    []string   `json:"unverified"`
	Prior         []Round    `json:"prior"`
}

// Exit is the exit status for the record: 3 with findings in the latest
// round, else 0.
func (r *Record) Exit() int {
	if len(r.Findings) > 0 {
		return 3
	}
	return 0
}

// retire moves the latest round, if there is one, into Prior.
func (r *Record) retire() {
	if r.Round > 0 {
		r.Prior = append(r.Prior, Round{Round: r.Round, Reviewer: r.Reviewer, Model: r.Model, Findings: r.Findings,
			Dropped: r.Dropped, Unverified: r.Unverified})
	}
	r.Reviewer, r.Model = "", nil
	r.Findings, r.Dropped, r.Unverified = []*Finding{}, []Dropped{}, []string{}
}

// recordPath is where the record pr-loop trusts is kept, beside the clone
// and outside the review directory, which a review session may write.
func recordPath(pr *PR) string {
	return filepath.Join(filepath.Dir(pr.Clone), pr.Head[:12]+".json")
}

// RecordFile is recordPath for head of slug's pull request number under
// root, the directory Root names.
func RecordFile(root, slug string, number int, head string) (string, error) {
	owner, repo, err := checkSlug(slug)
	if err != nil {
		return "", err
	}
	if number <= 0 || !fullHead.MatchString(head) {
		return "", fmt.Errorf("%s#%d at %q is not a pull request's head", slug, number, head)
	}
	return filepath.Join(root, owner, repo, strconv.Itoa(number), head[:12]+".json"), nil
}

// Fetched is the head slug's pull request number was last fetched at, and
// that head's review directory, where a session reviewing it starts.
func Fetched(ctx context.Context, slug string, number int) (string, string, error) {
	owner, repo, err := checkSlug(slug)
	if err != nil {
		return "", "", err
	}
	root, err := Root()
	if err != nil {
		return "", "", err
	}
	top := filepath.Join(root, owner, repo, strconv.Itoa(number))
	head, err := gitRun(ctx, filepath.Join(top, "clone"), "rev-parse", "--verify", "refs/pr-review/head^{commit}")
	if err != nil || !fullHead.MatchString(head) {
		return "", "", fmt.Errorf("%s#%d has not been fetched: %v", slug, number, err)
	}
	return head, filepath.Join(top, head[:12]), nil
}

var fullHead = regexp.MustCompile(`^[0-9a-f]{40}$`)

// newRecord starts the record of a new run on pr's head, keeping the rounds
// an earlier run on the same head recorded, the latest among them, with
// their outcomes.
func newRecord(pr *PR) *Record {
	r := &Record{Version: 1, Repo: pr.Repo, PR: pr.Number, URL: pr.URL, Clone: pr.Clone, Base: pr.Base,
		Head: pr.Head, Title: clean(pr.Title, TextCap), Findings: []*Finding{}, Dropped: []Dropped{},
		Unverified: []string{}, Prior: []Round{}}
	if earlier, err := loadRecord(pr); err == nil {
		earlier.retire()
		r.Prior = earlier.Prior
	}
	return r
}

// save writes the record where pr-loop keeps it, which review-guide imports,
// and a copy as findings.json in the review directory for a session to read.
func (r *Record) save(dir string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	pr := &PR{Clone: r.Clone, Head: r.Head}
	for _, path := range []string{recordPath(pr), filepath.Join(dir, "findings.json")} {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return nil
}

func loadRecord(pr *PR) (*Record, error) {
	data, err := os.ReadFile(recordPath(pr))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refusal.New("%s#%d has no review at %s; run `pr-loop review %d` first", pr.Repo, pr.Number, pr.Head[:12], pr.Number)
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", recordPath(pr), err)
	}
	return &r, nil
}

// addFindings puts a validated review into the record as its next round,
// cleaned and capped, with ids F1, F2 and so on.
func (r *Record) addFindings(out *review.Output) {
	r.retire()
	r.Round = len(r.Prior) + 1
	r.Reviewer = out.Reviewer
	r.Model = out.Model
	ids := map[string]string{}
	for i, f := range out.Findings {
		ids[f.ID] = fmt.Sprintf("F%d", i+1)
	}
	for _, f := range out.Findings {
		f.ID = ids[f.ID]
		f.Claim, f.Evidence = clean(f.Claim, TextCap), clean(f.Evidence, TextCap)
		for _, p := range []**string{&f.About, &f.Reopens} {
			if *p != nil {
				s := clean(**p, 200)
				*p = &s
			}
		}
		r.Findings = append(r.Findings, &Finding{Finding: *f, Round: r.Round, Severity: severities[f.Kind],
			Body: clean(f.Claim+"\n\n"+f.Evidence, TextCap)})
	}
	r.Dropped = []Dropped{}
	for _, d := range out.Dropped {
		r.Dropped = append(r.Dropped, Dropped{Finding: clean(string(d.Finding), TextCap), Reason: clean(d.Reason, 1024)})
	}
	r.Unverified = []string{}
	for _, u := range out.Unverified {
		r.Unverified = append(r.Unverified, clean(u, TextCap))
	}
}

// Settlement is how triage settled one finding.
type Settlement struct {
	ID      string  `json:"id"`
	Outcome string  `json:"outcome"`
	Of      string  `json:"of,omitempty"`
	Body    *string `json:"body,omitempty"`
}

// settle applies settlements to the record's findings, all of them or none.
func (r *Record) settle(input []byte) error {
	var parsed struct {
		Outcomes []Settlement `json:"outcomes"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(input)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil || parsed.Outcomes == nil {
		return refusal.New(`settle reads {"outcomes": [{"id": "1.F1", "outcome": "propose", "body": "..."}]} on stdin (%v); `+
			"pass the outcomes in that form", err)
	}
	// Every round's findings, by ROUND.ID.
	byID := map[string]*Finding{}
	for _, round := range append(append([]Round{}, r.Prior...), Round{Findings: r.Findings}) {
		for _, f := range round.Findings {
			byID[fmt.Sprintf("%d.%s", f.Round, f.ID)] = f
		}
	}
	seen := map[string]bool{}
	for _, s := range parsed.Outcomes {
		switch {
		case byID[s.ID] == nil:
			return refusal.New("%q is not a finding of this review; name one as ROUND.ID, such as 1.F1, from findings.json", s.ID)
		case seen[s.ID]:
			return refusal.New("%s is settled twice; give each finding one outcome", s.ID)
		case !outcomes[s.Outcome]:
			return refusal.New("%q is not an outcome; use propose, rebut, duplicate-of or needs-operator", s.Outcome)
		case s.Outcome == "duplicate-of" && (byID[s.Of] == nil || s.Of == s.ID):
			return refusal.New("%s is a duplicate of %q, which is not another finding; name the finding it repeats in `of`", s.ID, s.Of)
		case s.Outcome != "duplicate-of" && s.Of != "":
			return refusal.New("%s has `of` but is not a duplicate; give `of` only with duplicate-of", s.ID)
		}
		seen[s.ID] = true
	}
	for _, s := range parsed.Outcomes {
		f := byID[s.ID]
		outcome := s.Outcome
		f.Outcome = &outcome
		f.DuplicateOf = nil
		if s.Of != "" {
			of := s.Of
			f.DuplicateOf = &of
		}
		if s.Body != nil {
			f.Body = clean(*s.Body, TextCap)
		}
	}
	return nil
}
