package plans

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// The notes files are the files agent-plan writes in the notes repository
// outside the plans directory: each week's demo digest, DigestFile, and
// each year's brag file, BragFile. Only the host Config.NotesHost names
// writes them, and its sync alone sends them.

var (
	// digestName is a demo digest's file name in its year's directory
	// under digests/: MM-DD-demo.org, for the day it is named for.
	digestName = regexp.MustCompile(`^([0-9]{2})-([0-9]{2})-demo\.org$`)
	yearName   = regexp.MustCompile(`^[0-9]{4}$`)
	bragName   = regexp.MustCompile(`^[0-9]{4}\.org$`)
)

// notesDirs are the directories, relative to the notes repository, that
// hold the notes files.
var notesDirs = []string{"digests", "brag"}

// DigestFile is the demo digest of day, as a slash path relative to the
// notes repository: digests/YYYY/MM-DD-demo.org.
func DigestFile(day time.Time) string {
	return path.Join("digests", fmt.Sprintf("%04d", day.Year()), day.Format("01-02")+"-demo.org")
}

// DigestDay is the day, in loc, that the file named name in the directory
// digests/YEAR is the demo digest of; ok is false for any name DigestFile
// does not give, a day that does not exist among them.
func DigestDay(year int, name string, loc *time.Location) (day time.Time, ok bool) {
	m := digestName.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation("2006-01-02", fmt.Sprintf("%04d-%s-%s", year, m[1], m[2]), loc)
	return day, err == nil
}

// BragFile is year's brag file, as a slash path relative to the notes
// repository: brag/YYYY.org.
func BragFile(year int) string { return fmt.Sprintf("brag/%04d.org", year) }

// NotesFile reports whether rel, a slash path relative to the notes
// repository, is a notes file: a digest DigestFile names, or a brag file
// BragFile names.
func NotesFile(rel string) bool {
	parts := strings.Split(rel, "/")
	switch {
	case len(parts) == 3 && parts[0] == "digests" && yearName.MatchString(parts[1]):
		year, _ := strconv.Atoi(parts[1])
		_, ok := DigestDay(year, parts[2], time.UTC)
		return ok
	case len(parts) == 2 && parts[0] == "brag":
		return bragName.MatchString(parts[1])
	}
	return false
}

// ownsNotes reports whether this host is the one that writes the notes
// files.
func (c Config) ownsNotes() bool { return c.NotesHost != "" && c.NotesHost == c.Host }

// notesFiles are the notes files in the repository's working tree, as
// slash paths relative to it: regular files in directories that are not
// links.
func (c Config) notesFiles() ([]string, error) {
	var out []string
	for _, dir := range notesDirs {
		if info, err := os.Lstat(filepath.Join(c.repo(), dir)); err != nil || !info.IsDir() {
			continue
		}
		// A digest is a level deeper than a brag file, in its year's
		// directory.
		if err := c.walkNotes(dir, 1, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c Config) walkNotes(rel string, depth int, out *[]string) error {
	entries, err := os.ReadDir(filepath.Join(c.repo(), filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := rel + "/" + e.Name()
		switch {
		case e.Type().IsRegular() && NotesFile(name):
			*out = append(*out, name)
		case e.IsDir() && depth > 0 && yearName.MatchString(e.Name()):
			if err := c.walkNotes(name, depth-1, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// MaxDigest is the most of a digest file, in bytes, that the export reads,
// and so the most WriteDigest writes.
const MaxDigest = 64 << 10

// NotesWrite is what a write of a notes file did: the file, and whether it
// changed.
type NotesWrite struct {
	File    string `json:"file"`
	Changed bool   `json:"changed"`
}

var (
	// prShort and prURL are a pull request as a digest or brag entry
	// names it: OWNER/NAME#N, or its GitHub URL.
	prShort = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99})#([1-9][0-9]{0,9})$`)
	prURL   = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99})/pull/([1-9][0-9]{0,9})/?$`)
	// planIDLink is a digest's PLAN: an id: link to the plan's file-level
	// ID, bare or in brackets with a description.
	planIDLink = regexp.MustCompile(`^(?:id:[A-Za-z0-9-]+|\[\[id:[A-Za-z0-9-]+\](?:\[[^\[\]]*\])?\])$`)
)

// pullRequest is a pull request, read from either form prShort and prURL
// take.
type pullRequest struct {
	repo   string
	number string
}

func parsePR(s string) (pullRequest, bool) {
	m := prShort.FindStringSubmatch(s)
	if m == nil {
		m = prURL.FindStringSubmatch(s)
	}
	if m == nil {
		return pullRequest{}, false
	}
	return pullRequest{m[1], m[2]}, true
}

func (pr pullRequest) url() string {
	return "https://github.com/" + pr.repo + "/pull/" + pr.number
}

// checkNotesHost refuses a write of the notes files on any host but theirs,
// whose sync alone sends them.
func (c Config) checkNotesHost() error {
	if c.NotesHost == "" {
		return refusal.New("no host writes digests and brag files: plans.notesHost in the agent-tools config names none. Leave the write to the host that should, or report it to the operator")
	}
	if !c.ownsNotes() {
		return refusal.New("digests and brag files are written on %s alone, which plans.notesHost names, so that no two hosts write the same file; this is %s. Run the command on %s, or report it to the operator",
			c.NotesHost, c.Host, c.NotesHost)
	}
	return nil
}

// checkDigest refuses text that is not a demo digest as the export parses
// one: Org, with at least one level-1 heading, each a change whose property
// drawer, right below it, holds PLAN, an id: link, and PR, OWNER/NAME#N or
// the pull request's GitHub URL. A line inside a block, by blockLines, is
// the block's text, never a heading.
func checkDigest(text string) error {
	if !utf8.ValidString(text) || strings.ContainsAny(text, "\x00\r") {
		return badDigest("the digest holds a NUL, a carriage return or text that is not UTF-8; write plain Org lines")
	}
	lines := strings.Split(text, "\n")
	blocks := blockLines(lines)
	changes := 0
	for i := 0; i < len(lines); i++ {
		if m := headingLine.FindStringSubmatch(lines[i]); m == nil || len(m[1]) != 1 || blocks[i] {
			continue
		}
		changes++
		heading := lines[i]
		if i+1 >= len(lines) || strings.TrimSpace(lines[i+1]) != ":PROPERTIES:" {
			return badDigest("line %d, %q, is a change with no property drawer right below it, holding :PLAN: and :PR:", i+1, heading)
		}
		props, end, err := digestDrawer(lines, i, heading)
		if err != nil {
			return err
		}
		i = end
		if _, ok := parsePR(props["PR"]); !ok {
			return badDigest("%q has PR %q, which names no pull request; give :PR: OWNER/NAME#N or its https://github.com URL", heading, props["PR"])
		}
		if !planIDLink.MatchString(props["PLAN"]) {
			return badDigest("%q has PLAN %q, which is no id: link; give :PLAN: [[id:ID][title]], ID the plan file's own", heading, props["PLAN"])
		}
	}
	if changes == 0 {
		return badDigest("the digest has no change, a level-1 heading with :PLAN: and :PR: in its property drawer")
	}
	return nil
}

func digestDrawer(lines []string, i int, heading string) (map[string]string, int, error) {
	props := map[string]string{}
	closed := false
	for i += 2; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if l == ":END:" {
			closed = true
			break
		}
		m := propertyLine.FindStringSubmatch(l)
		if m == nil {
			return nil, i, badDigest("line %d, %q, in the drawer of %q, is not a property, and the drawer has no :END: before it", i+1, lines[i], heading)
		}
		props[strings.ToUpper(m[1])] = strings.TrimSpace(m[2])
	}
	if !closed {
		return nil, i, badDigest("the drawer of %q has no :END:", heading)
	}
	return props, i, nil
}

// badDigest refuses a digest for why, and says how to write it.
func badDigest(why string, args ...any) error {
	return refusal.New(why+". Write it as a level-1 heading per change, its drawer holding :PLAN: and :PR:, and run "+
		"`agent-plan digest --date FRIDAY --text-file FILE` again", args...)
}

// WriteDigest writes text as the demo digest of day, a Friday, replacing
// the one there, and changes nothing when it holds text already. It
// refuses any other day, text over MaxDigest or not in the format
// checkDigest reads, and any host but the notes host.
func (c Config) WriteDigest(day time.Time, text string) (NotesWrite, error) {
	if err := c.checkNotesHost(); err != nil {
		return NotesWrite{}, err
	}
	if day.Weekday() != time.Friday {
		return NotesWrite{}, refusal.New("%s is a %s, and a digest is named for its week's Friday. Run `agent-plan digest --date YYYY-MM-DD --text-file FILE` with that Friday's date",
			day.Format("2006-01-02"), day.Weekday())
	}
	if len(text) > MaxDigest {
		return NotesWrite{}, refusal.New("the digest is %d bytes, over the %d KiB the export reads. Shorten it, linking long output rather than holding it, and run `agent-plan digest --date FRIDAY --text-file FILE` again",
			len(text), MaxDigest>>10)
	}
	if err := checkDigest(text); err != nil {
		return NotesWrite{}, err
	}
	return c.writeNotesFile(DigestFile(day), func([]byte) (string, error) { return text, nil })
}

// BragOptions are a brag entry: the change merged on Day, whose year names
// the brag file; its pull request PR, OWNER/NAME#N or its GitHub URL; its
// plan, as Open reads it; and its title and summary, the operator's.
type BragOptions struct {
	Day            time.Time
	PR, Plan       string
	Title, Summary string
}

// WriteBrag appends o's entry to the brag file of o.Day's year: a level-1
// heading, the title, whose drawer holds PR, as its GitHub URL,
// REPOSITORY and PLAN, an id: link to the plan, followed by the summary.
// An entry for the same pull request already there, the same, changes
// nothing, and refuses o where it differs. It refuses any host but the
// notes host.
func (c Config) WriteBrag(o BragOptions) (NotesWrite, error) {
	if err := c.checkNotesHost(); err != nil {
		return NotesWrite{}, err
	}
	pr, ok := parsePR(o.PR)
	if !ok {
		return NotesWrite{}, refusal.New("PR %q names no pull request. Pass `--pr OWNER/NAME#N` or its https://github.com URL", o.PR)
	}
	title := strings.TrimSpace(o.Title)
	if title == "" {
		return NotesWrite{}, refusal.New("a brag entry needs a title. Pass `--title 'What changed'`")
	}
	if err := checkLine("title", title); err != nil {
		return NotesWrite{}, err
	}
	summary, err := textLines(o.Summary)
	if err != nil {
		return NotesWrite{}, err
	}
	if len(summary) == 0 {
		return NotesWrite{}, refusal.New("a brag entry needs the summary the operator approved. Pass it with `--text-file FILE`")
	}
	p, err := c.Open(o.Plan)
	if err != nil {
		return NotesWrite{}, err
	}
	data, err := readPlan(p.File())
	if err != nil {
		return NotesWrite{}, err
	}
	id := fileID(string(data))
	if id == "" {
		return NotesWrite{}, refusal.New("%s is in the old form, with no file-level ID for the entry's PLAN to name. Leave PLAN to the operator, and report it", p.Name)
	}
	planTitle := strings.NewReplacer("[", "(", "]", ")").Replace(orgTitle(string(data)))
	entry := "* " + title + "\n:PROPERTIES:\n:PR: " + pr.url() + "\n:REPOSITORY: " + pr.repo +
		"\n:PLAN: [[id:" + id + "][" + planTitle + "]]\n:END:\n" + strings.Join(summary, "\n") + "\n"
	file := BragFile(o.Day.Year())
	return c.writeNotesFile(file, func(old []byte) (string, error) {
		text := string(old)
		if strings.Contains("\n"+text, "\n"+entry) {
			return text, nil
		}
		if strings.Contains(text, "\n:PR: "+pr.url()+"\n") {
			return "", refusal.New("%s already holds an entry for %s, which differs from this one. Leave it as it is; a change to it is the operator's", file, pr.url())
		}
		if text == "" {
			text = fmt.Sprintf("#+title: Brag sheet %04d\n", o.Day.Year())
		}
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return text + entry, nil
	})
}

// writeNotesFile replaces the notes file rel, a slash path, with what next
// makes of its content, "" for none, holding the notes lock, so that writes
// on this host never interleave, and changes nothing when next returns the
// content as it was. It writes only through directories that are not
// links, making those missing, and replaces only a regular file, by
// renaming a new one over it; then it touches the plans directory, which
// the sync's watcher watches.
func (c Config) writeNotesFile(rel string, next func(old []byte) (string, error)) (NotesWrite, error) {
	file := filepath.Join(c.repo(), filepath.FromSlash(rel))
	w := NotesWrite{File: file}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil { //nolint:gosec // the plans directory, as New makes it
		return w, err
	}
	lock, err := lockFile(filepath.Join(c.Dir, ".notes.lock"), false)
	if err != nil {
		return w, err
	}
	defer lock.Close()
	dir := c.repo()
	for _, part := range strings.Split(path.Dir(rel), "/") {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(dir, 0o755); err != nil { //nolint:gosec // a directory of the notes repository, which git tracks
				return w, err
			}
			continue
		}
		if err != nil {
			return w, err
		}
		if !info.IsDir() {
			return w, refusal.New("%s is a link or a file, not a directory, so %s is not written through it. Report it to the operator", dir, rel)
		}
	}
	old, err := readRegularFile(file)
	if err != nil {
		return w, err
	}
	text, err := next(old)
	if err != nil || text == string(old) {
		return w, err
	}
	if err := replaceFile(file, text, func() bool { return true }); err != nil {
		return w, err
	}
	touch(c.Dir)
	w.Changed = true
	return w, nil
}

// readRegularFile reads file, nil where it is missing, refusing anything but
// a regular file, a link included.
func readRegularFile(file string) ([]byte, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, refusal.New("%s cannot be read as a regular file (%v), so it is not replaced. Report it to the operator", file, err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, refusal.New("%s is not a regular file, so it is not replaced. Report it to the operator", file)
	}
	return io.ReadAll(f)
}
