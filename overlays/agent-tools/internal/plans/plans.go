// Package plans keeps agents' plan files and their append-only event logs,
// side by side in one plans directory: a plan <name>.<host>.org, a
// <name>.<host>.<part>.org per sub-agent, and the log
// <name>.<host>.log.org, which only Log writes to. New names a plan
// <time>-<slug>, its time YYYYMMDDHHMMSS as org-roam names notes; a plan
// named YYYY-MM-DD-<slug> is read alike.
package plans

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/repos"
)

// Config is where plans live, from "plans" in the user's
// agent-tools/config.json.
type Config struct {
	// Dir holds every host's plans.
	Dir string `json:"dir"`
	// Host names this machine in its plans' file names.
	Host string `json:"host"`
	// Repo is the git repository the plans directory is in, which sync
	// commits to; the directory above Dir unless given.
	Repo string `json:"repo,omitempty"`
	// Templates is the directory of built-in plan templates, which a
	// template of the same name in Repo's templates directory overrides.
	Templates string `json:"templates,omitempty"`
	// Remote is Repo's origin, which init clones where Repo is missing.
	Remote string `json:"remote,omitempty"`
	// SSHConfig is the ssh config file git reaches Remote with.
	SSHConfig string `json:"sshConfig,omitempty"`
	// Key is the deploy key SSHConfig names; init clones only when it
	// exists.
	Key string `json:"key,omitempty"`
}

var (
	// A host is part of every file name, between dots, so it holds none.
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// planFile is a plan's file name: the plan's name, which is its time,
	// YYYYMMDDHHMMSS, or for an older plan its date, YYYY-MM-DD, and its
	// slug; then its host, and then a part's ID or "log" for those files.
	// emacs-config's wgn/agent-plans--file-regexp and the Agent plans page's
	// PLAN_ID (internal/agentplans/page.html) read the same names.
	planFile = regexp.MustCompile(`^((?:[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{14})-[a-z0-9]+(?:-[a-z0-9]+)*\.([A-Za-z0-9][A-Za-z0-9_-]*))(?:\.(P[0-9]+|log))?\.org$`)
	// oldDate is the date an older plan's name starts with.
	oldDate = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})-`)
)

// nameTimeLayout is the time a new plan's name starts with, as org-roam
// starts a note's.
const nameTimeLayout = "20060102150405"

// newerName reports whether the plan named a sorts before the plan named
// b, the newer first: by their names with an older plan's YYYY-MM-DD read
// as YYYYMMDD, as wgn/agent-plans--newer-p in emacs-config orders them.
func newerName(a, b string) bool {
	key := func(name string) string { return oldDate.ReplaceAllString(name, "$1$2$3-") }
	return key(a) > key(b)
}

// IsLog reports whether name is the file name of a plan's log, or of the
// lock file that records the log's state.
func IsLog(name string) bool {
	m := planFile.FindStringSubmatch(strings.TrimSuffix(name, ".lock"))
	return m != nil && m[3] == "log"
}

// IsPlan reports whether name is the file name of a plan's file or a
// part's, which agent-plan alone writes.
func IsPlan(name string) bool {
	m := planFile.FindStringSubmatch(name)
	return m != nil && m[3] != "log"
}

// notesRepo is the operator's notes repository under home, the plans'
// repository when the config names no plans directory.
func notesRepo(home string) string {
	return filepath.Join(home, "git", "github.com", "wesnel", "notes")
}

// DefaultDir is the plans directory under home when the config names
// none: agents/plans in the operator's notes repository.
func DefaultDir(home string) string { return filepath.Join(notesRepo(home), "agents", "plans") }

// Load reads the config, defaulting to this machine's short host name and
// to DefaultDir, in the notes repository, which is then the plans'
// repository too.
func Load() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	c := Config{Dir: DefaultDir(home), Repo: notesRepo(home), Host: host}
	data, err := os.ReadFile(repos.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return Config{}, err
	}
	var file struct {
		Plans Config `json:"plans"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return Config{}, fmt.Errorf("%s: %w", repos.Path(), err)
	}
	dir, repo, host := c.Dir, c.Repo, c.Host
	c = file.Plans
	if c.Dir == "" {
		c.Dir = dir
		if c.Repo == "" {
			c.Repo = repo
		}
	}
	if c.Host == "" {
		c.Host = host
	}
	if !filepath.IsAbs(c.Dir) || !hostPattern.MatchString(c.Host) || (c.Repo != "" && !filepath.IsAbs(c.Repo)) ||
		(c.Templates != "" && !filepath.IsAbs(c.Templates)) {
		return Config{}, fmt.Errorf("%s: plans.dir, plans.repo and plans.templates must be absolute and plans.host a plain name without dots: %+v", repos.Path(), c)
	}
	return c, nil
}

// NewOptions describe a plan to create, or with Parent and Part, a
// sub-agent's part of one. List values are Org property values, so none
// holds whitespace.
type NewOptions struct {
	Title, Slug                          string
	Repos, Branches, Worktrees, Sessions []string
	Harness                              string
	Parent, Part                         string
	// Template names <Template>.org in <plans repository>/templates or else
	// Config.Templates, whose entries the new plan starts with.
	Template string
	// Goal is a line the plan's Goal starts with, and Status its STATUS
	// in place of planning.
	Goal, Status string
}

// Created is what New made.
type Created struct {
	// Plan is the plan's file, the PLAN the other commands take.
	Plan string `json:"plan"`
	// File is the file made: the plan's, or the part's.
	File string `json:"file"`
	ID   string `json:"id"`
	Log  string `json:"log"`
}

// checkLine refuses a value that would break the Org line it goes on.
func checkLine(what, value string) error {
	if strings.ContainsAny(value, "\n\r\x00") {
		return refusal.New("%s holds a line break, which would forge Org structure. Pass it on one line, as `--%s 'one line'`", what, what)
	}
	return nil
}

// checkWords refuses a list value that is empty or holds whitespace.
func checkWords(what string, values []string) error {
	for _, v := range values {
		if v == "" || strings.ContainsAny(v, " \t\n\r\x00") {
			return refusal.New("%s %q is empty or holds whitespace, which an Org property list cannot hold. Pass each value as its own `--%s` flag, without spaces", what, v, what)
		}
	}
	return nil
}

// New creates a plan, or with Parent and Part, a part's plan file beside
// its parent's. A new plan's name is <time>-<slug>, its time now as
// YYYYMMDDHHMMSS, with -2, -3 and so on for a slug this host already used
// that second.
func (c Config) New(o NewOptions, now time.Time) (Created, error) {
	if strings.TrimSpace(o.Title) == "" {
		return Created{}, refusal.New("a plan needs a title. Pass one with `--title`")
	}
	if err := checkLine("title", o.Title); err != nil {
		return Created{}, err
	}
	for what, values := range map[string][]string{"repo": o.Repos, "branch": o.Branches, "worktree": o.Worktrees,
		"session": o.Sessions} {
		if err := checkWords(what, values); err != nil {
			return Created{}, err
		}
	}
	if o.Harness != "" {
		if err := checkWords("harness", []string{o.Harness}); err != nil {
			return Created{}, err
		}
	}
	if (o.Parent != "" || o.Part != "") && o.Template != "" {
		return Created{}, refusal.New("a template starts a plan, not a part. Make the part without `--template`, and copy what it needs into its file")
	}
	if o.Parent != "" || o.Part != "" {
		return c.newPart(o, now)
	}
	if !slugPattern.MatchString(o.Slug) {
		return Created{}, refusal.New("slug %q is not lower-case words joined by hyphens. Pass one such as `--slug add-run-loop`", o.Slug)
	}
	var sections map[string][]string
	if o.Template != "" {
		var err error
		if sections, err = c.template(o.Template); err != nil {
			return Created{}, err
		}
	}
	if o.Goal != "" {
		if err := checkLine("goal", o.Goal); err != nil {
			return Created{}, err
		}
		if sections == nil {
			sections = map[string][]string{}
		}
		sections["Goal"] = append([]string{o.Goal}, sections["Goal"]...)
	}
	if _, err := c.Init(); err != nil {
		return Created{}, err
	}
	base := now.Format(nameTimeLayout) + "-" + o.Slug
	var p Plan
	for i := 1; p.Name == "" && i < 1000; i++ {
		try := base
		if i > 1 {
			try = fmt.Sprintf("%s-%d", base, i)
		}
		candidate := Plan{Dir: c.Dir, Name: try + "." + c.Host, Host: c.Host, Own: true}
		text := withTemplate(planText(o, candidate.Name, c.Host, map[string]string{"HOST": c.Host}, true), sections)
		switch err := writeNew(candidate.file(""), text); {
		case err == nil:
			p = candidate
		case !errors.Is(err, fs.ErrExist):
			return Created{}, err
		}
	}
	if p.Name == "" {
		return Created{}, fmt.Errorf("no free name for %s.%s under %s", base, c.Host, c.Dir)
	}
	err := p.withLock(func(l *lockedPlan) error {
		if err := l.append("#+title: Log of " + p.Name + "\n"); err != nil {
			return err
		}
		return l.logNewProperties("", now)
	})
	if err != nil {
		return Created{}, err
	}
	return Created{Plan: p.file(""), File: p.file(""), ID: p.Name, Log: p.log()}, nil
}

func (c Config) newPart(o NewOptions, now time.Time) (Created, error) {
	if o.Parent == "" || !partPattern.MatchString(o.Part) {
		return Created{}, refusal.New("a part needs its parent plan and a part ID such as P1. Pass both, as `--parent PLAN --part P1`")
	}
	p, err := c.Open(o.Parent)
	if err != nil {
		return Created{}, err
	}
	if err := p.checkOwn(); err != nil {
		return Created{}, err
	}
	items, err := p.items()
	if err != nil {
		return Created{}, err
	}
	if item, ok := items[o.Part]; !ok || item.File != p.file("") {
		return Created{}, refusal.New("%s is not in %s's Distribution. Ask whoever coordinates the plan to add it, or use a part ID `agent-plan status %s` lists", o.Part, p.Name, p.file(""))
	}
	text := planText(o, o.Part, c.Host, map[string]string{"PARENT": "[[file:" + filepath.Base(p.file("")) + "]]"}, false)
	file := p.file(o.Part)
	kept := false
	err = p.withLock(func(l *lockedPlan) error {
		if err := l.checkSeal(); err != nil {
			return err
		}
		if err := l.create(file, text); errors.Is(err, fs.ErrExist) {
			return refusal.New("%s already has its plan file, %s. Change it with `agent-plan section`, `step` or `edit` and `--part %s`; `agent-plan goal %s --part %s` prints its goal", o.Part, file, o.Part, p.file(""), o.Part)
		} else if err != nil {
			return err
		}
		err := l.logNewProperties(o.Part, now)
		kept = l.appended
		return err
	})
	if err != nil && !kept {
		return Created{}, err
	}
	return Created{Plan: p.file(""), File: file, ID: o.Part, Log: p.log()}, err
}

var nonTag = regexp.MustCompile(`[^A-Za-z0-9_@#%]`)

// fileTags are the Org tags of a plan made on host for repos: each
// repository's name and the host, each character Org does not allow in a
// tag made a _, so the agenda can filter plans by either.
func fileTags(repos []string, host string) string {
	var tags []string
	for _, name := range append(slices.Clone(repos), host) {
		name = strings.TrimSuffix(name[strings.LastIndex(name, "/")+1:], ".git")
		if tag := nonTag.ReplaceAllString(name, "_"); tag != "" && !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	return ":" + strings.Join(tags, ":") + ":"
}

// planText is a new plan file of host's, with id as its `* Plan`
// heading's CUSTOM_ID: the parent's sections, and Distribution for a
// parent alone. A parent's file starts with a property drawer holding a
// fresh UUID as its ID, the one org-roam node of the plan; a part's file
// is no node and has none.
func planText(o NewOptions, id, host string, extra map[string]string, parent bool) string {
	var b strings.Builder
	if parent {
		fmt.Fprintf(&b, ":PROPERTIES:\n:ID: %s\n:END:\n", newUUID())
	}
	fmt.Fprintf(&b, "#+title: %s\n%s#+FILETAGS: %s\n\n* Plan\n:PROPERTIES:\n:CUSTOM_ID: %s\n", o.Title, todoLines(),
		fileTags(o.Repos, host), id)
	property := func(name, value string) {
		if value == "" {
			fmt.Fprintf(&b, ":%s:\n", name)
		} else {
			fmt.Fprintf(&b, ":%s: %s\n", name, value)
		}
	}
	for _, name := range []string{"PARENT", "HOST"} {
		if extra[name] != "" {
			property(name, extra[name])
		}
	}
	property("REPOSITORIES", strings.Join(o.Repos, " "))
	property("BRANCHES", strings.Join(o.Branches, " "))
	property("WORKTREES", strings.Join(o.Worktrees, " "))
	property("SESSIONS", strings.Join(o.Sessions, " "))
	property("HARNESS", o.Harness)
	status := "planning"
	if o.Status != "" {
		status = o.Status
	}
	property("STATUS", status)
	property("PULL_REQUESTS", "")
	property("PIPELINE", "full")
	b.WriteString(":END:\n")
	for _, s := range planSections {
		if s == "Distribution" && !parent {
			continue
		}
		fmt.Fprintf(&b, "** %s\n", s)
	}
	return b.String()
}

// writeNew writes a file that must not exist yet, whole or not at all: a
// part written would be a file that every retry refuses as existing. It
// writes a temporary file, and links it to path, which fails with
// fs.ErrExist rather than replace a file there.
func writeNew(path, text string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".edit-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Link(f.Name(), path)
}

// Ready is the plans directory, made ready for plans.
type Ready struct {
	Dir  string `json:"dir"`
	Repo string `json:"repo"`
	// Cloned is whether Init cloned the repository.
	Cloned bool `json:"cloned"`
	// Note says why the repository is not there to sync with.
	Note string `json:"note,omitempty"`
}

// ignore keeps the lock files, which only this machine's agent-plan reads,
// out of the repository the plans directory is in.
const ignore = "*.lock\n"

// Init clones the notes repository where it is missing and Remote is set,
// then makes the plans directory, with a .gitignore for the lock files if
// it has none, as New does, for a session whose sandbox may write only
// inside it.
func (c Config) Init() (Ready, error) {
	r := Ready{Dir: c.Dir, Repo: c.repo()}
	var err error
	if r.Cloned, r.Note, err = c.clone(); err != nil {
		return r, err
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil { //nolint:gosec // the plans: notes on the work, synced through git
		return r, err
	}
	err = writeNew(filepath.Join(c.Dir, ".gitignore"), ignore)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return r, err
	}
	return r, nil
}

// Plan is one plan's files in the plans directory.
type Plan struct {
	Dir  string `json:"dir"`
	Name string `json:"name"`
	Host string `json:"host"`
	// Own is whether the plan is this host's, which alone writes it.
	Own bool `json:"own"`
}

func (p Plan) file(part string) string {
	if part == "" {
		return filepath.Join(p.Dir, p.Name+".org")
	}
	return filepath.Join(p.Dir, p.Name+"."+part+".org")
}

func (p Plan) log() string { return filepath.Join(p.Dir, p.Name+".log.org") }

// File is the plan's own file.
func (p Plan) File() string { return p.file("") }

// checkOwn refuses a write to another host's plan, whose files that host's
// sync alone commits.
func (p Plan) checkOwn() error {
	if p.Own {
		return nil
	}
	return refusal.New("%s is %s's plan, and only agent-plan on %s writes its files. Run the command on %s, or put it in your report for the operator", p.Name, p.Host, p.Host, p.Host)
}

// Open finds the plan arg names: one of its files under the plans
// directory, or its name.
func (c Config) Open(arg string) (Plan, error) {
	notPlan := func() error {
		return refusal.New("%s is not a plan under %s. Pass the plan file `agent-plan new` printed, or find it with `agent-plan find`", arg, c.Dir)
	}
	path := arg
	if !strings.Contains(arg, "/") {
		if _, err := os.Lstat(arg); err != nil {
			path = filepath.Join(c.Dir, arg+".org")
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return Plan{}, err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Plan{}, notPlan()
	}
	root, err := filepath.EvalSymlinks(c.Dir)
	if err != nil || filepath.Dir(real) != root {
		return Plan{}, notPlan()
	}
	m := planFile.FindStringSubmatch(filepath.Base(real))
	if m == nil {
		return Plan{}, notPlan()
	}
	p := Plan{Dir: root, Name: m[1], Host: m[2], Own: m[2] == c.Host}
	for _, f := range []string{p.file(""), p.log()} {
		if info, err := os.Lstat(f); err != nil || !soleFile(info) {
			return Plan{}, notPlan()
		}
	}
	return p, nil
}

// item is a heading with an ID in one of a plan's files.
type item struct {
	heading
	ID, File, Part string
}

// files are the plan's own files: the plan's, then each part's in order.
func (p Plan) files() ([]string, error) {
	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		return nil, err
	}
	var parts []int
	for _, e := range entries {
		if m := planFile.FindStringSubmatch(e.Name()); m != nil && m[1] == p.Name && partPattern.MatchString(m[3]) &&
			e.Type().IsRegular() {
			n, _ := strconv.Atoi(m[3][1:])
			parts = append(parts, n)
		}
	}
	sort.Ints(parts)
	files := []string{p.file("")}
	for _, n := range parts {
		files = append(files, p.file(fmt.Sprintf("P%d", n)))
	}
	return files, nil
}

// partOf is the part whose file f is, or "" for the plan's own.
func partOf(f string) string {
	if m := planFile.FindStringSubmatch(filepath.Base(f)); m != nil && partPattern.MatchString(m[3]) {
		return m[3]
	}
	return ""
}

// itemList is every heading with an ID, in file order; the first of a
// repeated ID stands.
func (p Plan) itemList() ([]item, error) {
	files, err := p.files()
	if err != nil {
		return nil, err
	}
	var out []item
	seen := map[string]bool{}
	for _, f := range files {
		data, err := readPlan(f)
		if err != nil {
			return nil, err
		}
		part := partOf(f)
		for _, h := range parseOrg(string(data)) {
			id := itemID(h)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, item{heading: h, ID: id, File: f, Part: part})
		}
	}
	return out, nil
}

// InSection is the IDs of the plan's items, its parts' too, that stand
// under a level-2 heading titled section, as itemList picks them.
func (p Plan) InSection(section string) (map[string]bool, error) {
	files, err := p.files()
	if err != nil {
		return nil, err
	}
	out, seen := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		data, err := readPlan(f)
		if err != nil {
			return nil, err
		}
		hs := parseOrg(string(data))
		under := sectionsOf(hs)
		for i, h := range hs {
			if id := itemID(h); id != "" && !seen[id] {
				seen[id] = true
				out[id] = h.Level > 2 && under[i] == section
			}
		}
	}
	return out, nil
}

func (p Plan) items() (map[string]item, error) {
	list, err := p.itemList()
	if err != nil {
		return nil, err
	}
	m := map[string]item{}
	for _, it := range list {
		m[it.ID] = it
	}
	return m, nil
}

// Goal is the text under "** Goal" in the plan's file, or the part's: the
// goal statement a fresh review reads.
func (p Plan) Goal(part string) (string, error) {
	if part != "" && !partPattern.MatchString(part) {
		return "", refusal.New("%q is not a part ID. Pass one such as `--part P1`", part)
	}
	file := p.file(part)
	data, err := readPlan(file)
	if errors.Is(err, fs.ErrNotExist) {
		return "", refusal.New("%s has no plan file for part %s. The part's sub-agent creates it with `agent-plan new --parent %s --part %s`", p.Name, part, p.file(""), part)
	}
	if err != nil {
		return "", err
	}
	hs := parseOrg(string(data))
	sections := sectionsOf(hs)
	for i, h := range hs {
		if h.Level != 2 || sections[i] != "Goal" {
			continue
		}
		body := strings.Trim(strings.Join(h.Body, "\n"), "\n")
		if strings.TrimSpace(body) == "" {
			break
		}
		return body + "\n", nil
	}
	return "", refusal.New("%s has no goal statement under its Goal heading. Write the goal and what is not this change's job with `agent-plan section PLAN Goal --text TEXT [--part P1]`, as the `plan-work` skill says", file)
}

// FindOptions select plans; a plan matches any one given.
type FindOptions struct {
	// Session is a session ID, alone or as harness:id.
	Session string
	// Worktree is a worktree's top-level directory.
	Worktree string
	// Branch is a branch name.
	Branch string
}

// Found is a plan file that matched.
type Found struct {
	// Plan is the plan's own file, File the one that matched.
	Plan    string   `json:"plan"`
	File    string   `json:"file"`
	Part    string   `json:"part,omitempty"`
	Title   string   `json:"title"`
	State   string   `json:"status"`
	Matched []string `json:"matched"`
}

// Plans are every host's plans, newest first, and the names of the plan
// files Open refused, such as one whose log has not arrived yet. A link in
// the plans directory is neither, even to a plan there.
func (c Config) Plans() ([]Plan, []string, error) { return c.plans("") }

// ownPlans are this host's plans, newest first.
func (c Config) ownPlans() ([]Plan, error) {
	out, _, err := c.plans(c.Host)
	return out, err
}

// plans are host's plans, or every host's for "", newest first, each
// opened through Open, and the names of those Open refused.
func (c Config) plans(host string) (out []Plan, failed []string, err error) {
	entries, err := os.ReadDir(c.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		switch {
		case newerName(a.Name(), b.Name()):
			return -1
		case newerName(b.Name(), a.Name()):
			return 1
		}
		return 0
	})
	for _, e := range entries {
		m := planFile.FindStringSubmatch(e.Name())
		if m == nil || host != "" && m[2] != host || m[3] != "" || !e.Type().IsRegular() {
			continue
		}
		if p, err := c.Open(filepath.Join(c.Dir, e.Name())); err == nil {
			out = append(out, p)
		} else {
			failed = append(failed, m[1])
		}
	}
	return out, failed, nil
}

// soleFile reports whether info is a regular file with no other name: a
// hard link would let a file outside the plans directory pass for a plan.
func soleFile(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return info.Mode().IsRegular() && (!ok || st.Nlink == 1)
}

// readPlan reads one of a plan's files, checking the file it opened, not
// the path: one swapped for a link, a file with another name or a FIFO
// after Open checked it is refused, and a FIFO without blocking.
func readPlan(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !soleFile(info) {
		return nil, fmt.Errorf("%s is not a plan's own regular file", path)
	}
	return io.ReadAll(f)
}

// HostBranches are the branches this host's plans name in BRANCHES, their
// own and their parts', newest plan first, each once.
func HostBranches() []string {
	c, err := Load()
	if err != nil {
		return nil
	}
	own, err := c.ownPlans()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range own {
		files, err := p.files()
		if err != nil {
			continue
		}
		for _, f := range files {
			props, err := planHeading(f)
			if err != nil {
				continue
			}
			for _, v := range strings.Fields(props["BRANCHES"]) {
				if _, b, ok := strings.Cut(v, ".."); ok {
					v = b
				}
				if v != "" && !slices.Contains(out, v) {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// Find lists this host's plan files that match, newest plan first.
func (c Config) Find(o FindOptions) ([]Found, error) {
	if o.Session == "" && o.Worktree == "" && o.Branch == "" {
		return nil, refusal.New("find needs something to match. Pass `--session`, `--worktree` or `--branch`")
	}
	worktree := o.Worktree
	if worktree != "" {
		worktree = filepath.Clean(worktree)
		if real, err := filepath.EvalSymlinks(worktree); err == nil {
			worktree = real
		}
	}
	own, err := c.ownPlans()
	if err != nil {
		return nil, err
	}
	found := []Found{}
	for _, p := range own {
		files, err := p.files()
		if err != nil {
			continue
		}
		for _, f := range files {
			data, err := readPlan(f)
			if err != nil {
				continue
			}
			var top heading
			for _, h := range parseOrg(string(data)) {
				if h.Level == 1 && sectionTitle(h) == "Plan" {
					top = h
					break
				}
			}
			var matched []string
			for _, v := range strings.Fields(top.Props["SESSIONS"]) {
				if o.Session != "" && (v == o.Session || strings.HasSuffix(v, ":"+o.Session)) {
					matched = append(matched, "session")
					break
				}
			}
			for _, v := range strings.Fields(top.Props["WORKTREES"]) {
				if worktree != "" && filepath.Clean(v) == worktree {
					matched = append(matched, "worktree")
					break
				}
			}
			for _, v := range strings.Fields(top.Props["BRANCHES"]) {
				if _, branch, ok := strings.Cut(v, ".."); ok {
					v = branch
				}
				if o.Branch != "" && v == o.Branch {
					matched = append(matched, "branch")
					break
				}
			}
			if len(matched) == 0 {
				continue
			}
			found = append(found, Found{Plan: p.file(""), File: f, Part: partOf(f), Title: orgTitle(string(data)),
				State: top.Props["STATUS"], Matched: matched})
		}
	}
	return found, nil
}
