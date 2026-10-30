package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/redact"
	"github.com/wesnel/agent-tools/internal/redcheck"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/routes"
)

// mechanicalPrefix starts the ID of every finding red-check raises, and of
// no reviewer's.
const mechanicalPrefix = "RC-"

// redCheckCap is how much of red-check's JSON the prompt carries: its end,
// where it is longer. The verdicts are read from the whole of it.
const redCheckCap = 16 << 10

// adequacy is a review's test adequacy: red-check's results for the prompt,
// and the findings it raises with no reviewer's judgement.
type adequacy struct {
	// configured is whether the repository has test suites; given
	// whether red-check's JSON came with --red-check.
	configured, given bool
	text              string
	findings          []*Finding
	dropped           []Dropped
	// buildOnly are the commits whose tests only fail to build without
	// the rest of them.
	buildOnly []string
}

// usableSettings refuses agent settings for r's clone that a review
// cannot use, saying what to fix, where they would otherwise leave out the
// test suites and route rules beside them.
func usableSettings(r *span) error {
	_, err := r.settings()
	return err
}

// testAdequacy reads data, git red-check's JSON on r from file, the
// --red-check file ("" where none was given), and turns each suite of a
// commit that passes without the rest of it into an `untested` finding. It
// refuses JSON that does not parse, that ran other suites than the
// repository's, that holds a verdict red-check does not give or a suite it
// was not given, whose commits are not r's own in order, or that comes for
// a repository without test suites, whose test files the findings need.
func testAdequacy(r *span, file string, data []byte, log io.Writer) (*adequacy, error) {
	settings, err := r.settings()
	if err != nil {
		if file != "" {
			return nil, err
		}
		// Main refused unusable settings; they changed since.
		fmt.Fprintf(log, "%s: test adequacy is left out: %v\n", name, err)
		return &adequacy{configured: true}, nil
	}
	suites := settings.Suites()
	a := &adequacy{configured: len(suites) > 0, given: file != ""}
	switch {
	case !a.given:
		if a.configured {
			fmt.Fprintf(log, "%s: git red-check runs in the session, not here, so this review has no red-check results; "+
				"pass its JSON with `--red-check FILE`\n", name)
		}
		return a, nil
	case !a.configured:
		return nil, refusal.New("--red-check %s needs the clone's test suites, which name the test files its findings "+
			"are placed at, and this clone has none; review without `--red-check`, or ask the operator to set them in "+
			"the clone's agent settings, %s", file, settings.File())
	}
	var result redcheck.Result
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, refusal.New("--red-check %s is not git red-check's JSON: %v. Pass the file `git red-check %s..%s` "+
			"writes on stdout", file, err, r.base, r.tip)
	}
	// The verdicts say what this repository's tests show only where red-check
	// ran this repository's suites.
	if !slices.EqualFunc(result.Suites, suites, redcheck.Suite.Equal) {
		return nil, refusal.New("--red-check %s ran the suites %s, not this repository's, %s. Run `git red-check %s..%s` "+
			"without --test, --build or --test-files, and pass what it writes", file, suitesText(result.Suites),
			suitesText(suites), r.base, r.tip)
	}
	bySuite := map[string]redcheck.Suite{}
	for _, s := range suites {
		bySuite[s.Name] = s
	}
	// Oldest first, and without merges, as red-check checks them.
	out, err := git.Run("-C", r.top, "rev-list", "--reverse", "--no-merges", r.base+".."+r.tip)
	if err != nil {
		return nil, err
	}
	var got []string
	for _, c := range result.Commits {
		if c == nil {
			return nil, refusal.New("--red-check %s has a commit that is null. Pass the file `git red-check %s..%s` "+
				"writes on stdout", file, r.base, r.tip)
		}
		if why := badCommit(c, bySuite); why != "" {
			return nil, refusal.New("--red-check %s gives commit %q %s. Pass the file `git red-check %s..%s` writes on "+
				"stdout", file, c.Commit, why, r.base, r.tip)
		}
		got = append(got, c.Commit)
	}
	if why := mismatch(strings.Fields(out), got); why != "" {
		return nil, refusal.New("--red-check %s is not git red-check's JSON on %s..%s: %s. Run `git red-check %s..%s` "+
			"on this range and pass what it writes", file, r.base, r.tip, why, r.base, r.tip)
	}
	// Redacted before the cut, which could split a secret's shape and leave
	// its end unrecognised.
	a.text = fenced("json", cut([]byte(redact.Secrets(string(data))), redCheckCap))
	for _, c := range result.Commits {
		for _, s := range c.Suites {
			switch s.Verdict {
			case redcheck.FailsToBuildWithoutChange:
				if !slices.Contains(a.buildOnly, c.Commit) {
					a.buildOnly = append(a.buildOnly, c.Commit)
				}
			case redcheck.PassesWithoutChange:
				f, err := untested(r, bySuite[s.Suite], c.Commit)
				switch {
				case err != nil:
					fmt.Fprintf(log, "%s: red-check found that %s's %s tests pass without its change, but: %v\n", name,
						c.Commit, s.Suite, err)
				case f == nil:
				case f.Path == "":
					raw, _ := json.Marshal(f)
					a.dropped = append(a.dropped, Dropped{Finding: raw, Reason: "none of its test files exists at TIP"})
				default:
					a.findings = append(a.findings, f)
				}
			}
		}
	}
	return a, nil
}

// badCommit says what in c, a commit of red-check's JSON whose suites were
// bySuite, red-check would not have written, or is "" where nothing is.
func badCommit(c *redcheck.Check, bySuite map[string]redcheck.Suite) string {
	if !knownVerdict(c.Verdict) {
		return fmt.Sprintf("the verdict %q, which red-check does not give", c.Verdict)
	}
	for _, s := range c.Suites {
		if s == nil || !knownVerdict(s.Verdict) {
			return "a suite's verdict red-check does not give, or none"
		}
		if _, ok := bySuite[s.Suite]; !ok {
			return fmt.Sprintf("a result of suite %q, which it was not given", s.Suite)
		}
	}
	// A verdict its suites do not give would raise no finding, or one with
	// no suite's test files to place it at.
	if want := redcheck.Worst(c.Suites); c.Verdict != want {
		return fmt.Sprintf("the verdict %q where its suites give %q", c.Verdict, want)
	}
	return ""
}

// knownVerdict reports whether red-check gives verdict.
func knownVerdict(verdict string) bool {
	_, known := redcheck.Messages[verdict]
	return known || verdict == redcheck.FailsAsAsserted
}

// suitesText is suites as JSON, for a message.
func suitesText(suites []redcheck.Suite) string {
	data, _ := json.Marshal(suites)
	return string(data)
}

// mismatch says how got, the commits red-check's JSON names, differs from
// want, the range's commits oldest first, or is "" where they are the same.
func mismatch(want, got []string) string {
	in := func(list []string) map[string]bool {
		m := map[string]bool{}
		for _, c := range list {
			m[c] = true
		}
		return m
	}
	inWant, inGot := in(want), in(got)
	var missing, extra, why []string
	for _, c := range want {
		if !inGot[c] {
			missing = append(missing, c)
		}
	}
	for _, c := range got {
		if !inWant[c] {
			extra = append(extra, c)
		}
	}
	if len(missing) > 0 {
		why = append(why, "it lacks "+shortList(missing))
	}
	if len(extra) > 0 {
		why = append(why, fmt.Sprintf("it names %q, not full commits of the range", extra))
	}
	if len(why) == 0 && !slices.Equal(want, got) {
		why = append(why, "it names the range's commits out of order, or one more than once")
	}
	return strings.Join(why, ", and ")
}

// cut is the end of data, at most n bytes of it, saying how much it leaves
// out.
func cut(data []byte, n int) string {
	if len(data) <= n {
		return string(data)
	}
	return fmt.Sprintf("(the first %d bytes are cut)\n%s", len(data)-n, data[len(data)-n:])
}

// fenced is text in a code fence with info, longer than any run of
// backticks in it, and a newline.
func fenced(info, text string) string {
	longest, run := 0, 0
	for _, c := range text {
		if c == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	f := strings.Repeat("`", max(3, longest+1))
	return f + info + "\n" + strings.TrimRight(text, "\n") + "\n" + f + "\n"
}

// testDiff is the names of suite's test files commit changes, and their
// diff.
func testDiff(r *span, suite redcheck.Suite, commit string) ([]string, []byte, error) {
	pathspecs := []string{}
	for _, g := range suite.TestFiles {
		pathspecs = append(pathspecs, ":(glob)"+g)
	}
	diff := func(extra ...string) ([]byte, error) {
		args := append([]string{"-C", r.top, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames"}, extra...)
		return git.Bytes(nil, append(append(args, commit+"^", commit, "--"), pathspecs...)...)
	}
	names, err := diff("--name-only", "-z")
	if err != nil {
		return nil, nil, err
	}
	var files []string
	for _, f := range strings.Split(string(names), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return nil, nil, nil
	}
	patch, err := diff("--full-index", "--binary", "--src-prefix=a/", "--dst-prefix=b/", "--diff-algorithm=myers")
	return files, patch, err
}

// untested is the finding for commit, whose tests of suite red-check found
// pass without the rest of it: at the first of those test files that exists
// at TIP, its ID made from testKey of the suite's name and their diff, so
// that two suites taking in one test file raise two findings. Its Path is
// "" where no test file exists at TIP; it is nil where the commit changes
// no test file of the suite here.
func untested(r *span, suite redcheck.Suite, commit string) (*Finding, error) {
	files, patch, err := testDiff(r, suite, commit)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	subject, _ := git.Run("-C", r.top, "log", "-1", "--format=%s", commit)
	id := mechanicalPrefix + testKey(append([]byte("suite "+suite.Name+"\n"), patch...))
	f := &Finding{ID: id, Kind: "untested", Reviewer: "red-check",
		Claim: fmt.Sprintf("The tests %s %q changes pass without the rest of that commit, so they do not test it.",
			commit[:12], subject),
		Evidence: fmt.Sprintf("git red-check %s..%s, from --red-check, gave commit %s the verdict passes-without-change "+
			"in suite %q: its parent with only that suite's test files (%s) applied passed its test command. Fix the "+
			"tests, or say in the review reply why they are meant to pass without the change, as a characterisation "+
			"test is.", r.base, r.tip, commit, suite.Name, strings.Join(files, ", "))}
	f.place(r, commit, files)
	return f, nil
}

// shortList is commits by their first 12 characters, the first five of
// them.
func shortList(commits []string) string {
	var short []string
	for i, c := range commits {
		if i == 5 {
			short = append(short, fmt.Sprintf("and %d more", len(commits)-i))
			break
		}
		short = append(short, c[:min(12, len(c))])
	}
	return strings.Join(short, ", ")
}

// place puts f at the line commit first changes in the first of files that
// exists at TIP and is not empty there, or leaves its Path "".
func (f *Finding) place(r *span, commit string, files []string) {
	for _, file := range files {
		data, err := git.Bytes(nil, "-C", r.top, "cat-file", "blob", r.tip+":"+file)
		if err != nil {
			continue
		}
		n := bytes.Count(data, []byte("\n"))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			n++
		}
		if n == 0 {
			continue
		}
		f.Path, f.Line = file, min(max(firstChanged(r, commit, file), 1), n)
		return
	}
}

var (
	hunk = regexp.MustCompile(`(?m)^@@ -\S+ \+(\d+)`)
	// moved are the lines of a diff that a rebase changes while the
	// tests stay as they are: the blobs' IDs, and each hunk's line
	// numbers and the function heading after them.
	moved = regexp.MustCompile(`(?m)^(index [0-9a-f]+\.\.[0-9a-f]+.*|@@ [^@]* @@.*)$`)
)

// testKey is the key of a commit's test files' diff: a hash of it as
// written, whitespace included, without what a rebase moves.
func testKey(diff []byte) string {
	sum := sha256.Sum256(moved.ReplaceAll(diff, []byte("@@")))
	return hex.EncodeToString(sum[:])[:12]
}

// firstChanged is the first line commit adds or changes in file, as the
// commit has it, or 0.
func firstChanged(r *span, commit, file string) int {
	out, err := git.Bytes(nil, "-C", r.top, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
		"-U0", commit+"^", commit, "--", ":(literal)"+file)
	if err != nil {
		return 0
	}
	if m := hunk.FindSubmatch(out); m != nil {
		n, _ := strconv.Atoi(string(m[1]))
		return n
	}
	return 0
}

// prompt is what the reviewer is told of test adequacy. printed is whether
// the prompt is printed for a reviewer whose reply delegate never sees and
// so raises no finding from: that reviewer reports red-check's findings
// itself.
func (a *adequacy) prompt(printed bool) string {
	switch {
	case !a.configured:
		return "This repository has no test suites, so there is no mechanical test adequacy for it: " +
			"judge `untested` from the tests themselves.\n"
	case !a.given:
		return "This review was given no results of a run of the tests without each commit's change, so there is " +
			"no mechanical test adequacy for it: judge `untested` from the tests themselves.\n"
	}
	var b strings.Builder
	if printed {
		b.WriteString("Each test suite of a commit that passes without the rest of the commit is an `untested` " +
			"finding with an ID starting " + mechanicalPrefix + ". This prompt was printed for a reviewer whose reply git delegate never " +
			"sees, so nothing raises them for you: report each finding below yourself, as a finding of kind " +
			"`untested` with the `id`, `path` and `line` given, and its claim and evidence.\n")
	} else {
		b.WriteString("Each test suite of a commit that passes without the rest of the commit is reported as an " +
			"`untested` finding with an ID starting " + mechanicalPrefix + ", by this review itself; do not report " +
			"those again.\n")
	}
	if len(a.buildOnly) > 0 {
		fmt.Fprintf(&b, "\nThe tests of %s only fail to build without the rest of their commits "+
			"(fails-to-build-without-change), which shows they need the change, not that they test it: judge whether "+
			"they do, and report an `untested` finding where they do not.\n", shortList(a.buildOnly))
	}
	switch {
	case len(a.findings) > 0 && printed:
		b.WriteString("\nTo report:\n\n")
		for _, f := range a.findings {
			fmt.Fprintf(&b, "- `%s`: path `%s`, line %d; claim: %s Evidence: %s\n", f.ID, f.Path, f.Line, f.Claim,
				f.Evidence)
		}
	case len(a.findings) > 0:
		b.WriteString("\nReported this round:\n\n")
		for _, f := range a.findings {
			fmt.Fprintf(&b, "- %s at %s:%d\n", f.ID, f.Path, f.Line)
		}
	}
	return b.String()
}

// sharedPrompt is what the reviewer is told of the files r changes that
// hold tests beside their code, whose tests red-check cannot run without
// the rest of them: the verdict shared-file, judged by the reviewer.
func sharedPrompt(r *span) string {
	files, err := routes.SharedFiles(r.top, r.base, r.tip)
	if err != nil || len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nThese changed files hold tests beside the code they test (verdict shared-file), which red-check cannot " +
		"run apart from it: judge whether their tests test the change, and report an `untested` finding where they do not.\n\n")
	for _, f := range files {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	return b.String()
}

// add puts a's findings into out, after the reviewer's.
func (a *adequacy) add(out *Output) {
	out.Findings = append(out.Findings, a.findings...)
	out.Dropped = append(out.Dropped, a.dropped...)
}
