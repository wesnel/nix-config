// Package plans keeps agents' plan files and their append-only event logs,
// side by side in one plans directory: a plan <date>-<slug>.<host>.org, a
// <date>-<slug>.<host>.<part>.org per sub-agent, and the log
// <date>-<slug>.<host>.log.org, which only Log writes to.
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
	// Remote is Repo's origin, which init clones where Repo is missing.
	Remote string `json:"remote,omitempty"`
	// SSHConfig is the ssh config file git reaches Remote with.
	SSHConfig string `json:"sshConfig,omitempty"`
	// Key is the deploy key SSHConfig names; init clones only when it
	// exists.
	Key string `json:"key,omitempty"`
	// Peers are told to pull whenever a sync pushes.
	Peers []Peer `json:"peers,omitempty"`
}

var (
	// A host is part of every file name, between dots, so it holds none.
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// planFile is a plan's file name: the plan's name, which is its date and
	// slug, then its host, and then a part's ID or "log" for those files.
	planFile = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2}-[a-z0-9]+(?:-[a-z0-9]+)*\.([A-Za-z0-9][A-Za-z0-9_-]*))(?:\.(P[0-9]+|log))?\.org$`)
)

// IsLog reports whether name is the file name of a plan's log, or of the
// lock file that records the log's state.
func IsLog(name string) bool {
	m := planFile.FindStringSubmatch(strings.TrimSuffix(name, ".lock"))
	return m != nil && m[3] == "log"
}

// Load reads the config, defaulting to ~/git/notes/agents and this
// machine's short host name.
func Load() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	c := Config{Dir: filepath.Join(home, "git", "notes", "agents"), Host: host}
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
	dir, host := c.Dir, c.Host
	c = file.Plans
	if c.Dir == "" {
		c.Dir = dir
	}
	if c.Host == "" {
		c.Host = host
	}
	if !filepath.IsAbs(c.Dir) || !hostPattern.MatchString(c.Host) || (c.Repo != "" && !filepath.IsAbs(c.Repo)) {
		return Config{}, fmt.Errorf("%s: plans.dir and plans.repo must be absolute and plans.host a plain name without dots: %+v", repos.Path(), c)
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
	// Template names <plans repository>/templates/<Template>.org, whose
	// entries the new plan starts with.
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
// its parent's. A new plan's name is <date>-<slug>, with -2, -3 and so on
// for a slug this host already used that day.
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
	base := now.Format("2006-01-02") + "-" + o.Slug
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
	if err := writeNew(file, text); errors.Is(err, fs.ErrExist) {
		return Created{}, refusal.New("%s already has its plan file, %s. Edit that file instead; `agent-plan goal %s --part %s` prints its goal", o.Part, file, p.file(""), o.Part)
	} else if err != nil {
		return Created{}, err
	}
	err = p.withLock(func(l *lockedPlan) error { return l.logNewProperties(o.Part, now) })
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

// planText is a new plan file of host's: the parent's sections, and
// Distribution for a parent alone.
func planText(o NewOptions, id, host string, extra map[string]string, parent bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#+title: %s\n%s#+FILETAGS: %s\n\n* Plan\n:PROPERTIES:\n:ID: %s\n", o.Title, todoLines(),
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

// writeNew writes a file that must not exist yet.
func writeNew(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	return f.Close()
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
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
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
			id := h.Props["ID"]
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
		under := ""
		for _, h := range parseOrg(string(data)) {
			if h.Level <= 2 {
				under = h.Title
			}
			if id := h.Props["ID"]; id != "" && !seen[id] {
				seen[id] = true
				out[id] = h.Level > 2 && under == section
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
	for _, h := range parseOrg(string(data)) {
		if h.Level != 2 || h.Title != "Goal" {
			continue
		}
		body := strings.Trim(strings.Join(h.Body, "\n"), "\n")
		if strings.TrimSpace(body) == "" {
			break
		}
		return body + "\n", nil
	}
	return "", refusal.New("%s has no goal statement under its Goal heading. Write the goal and what is not this change's job there, as the `plan-work` skill says", file)
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

// OwnPlans are this host's plans, newest first.
func (c Config) OwnPlans() ([]Plan, error) { return c.ownPlans() }

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
	for i := len(entries) - 1; i >= 0; i-- {
		m := planFile.FindStringSubmatch(entries[i].Name())
		if m == nil || host != "" && m[2] != host || m[3] != "" || !entries[i].Type().IsRegular() {
			continue
		}
		if p, err := c.Open(filepath.Join(c.Dir, entries[i].Name())); err == nil {
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
				if h.Level == 1 && h.Title == "Plan" {
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
