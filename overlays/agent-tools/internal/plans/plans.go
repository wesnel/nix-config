// Package plans keeps agents' plan files and their append-only event logs:
// one directory per plan under the plans directory, <host>/<date>-<slug>/,
// holding plan.org, a <part>.org per sub-agent, and log.org, which only Log
// writes to.
package plans

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/repos"
)

// Config is where plans live, from "plans" in the user's
// agent-tools/config.json.
type Config struct {
	// Dir holds a directory per host, each holding that host's plans.
	Dir string `json:"dir"`
	// Host names this machine's directory under Dir.
	Host string `json:"host"`
}

var (
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	partPattern = regexp.MustCompile(`^P[0-9]+$`)
	partFile    = regexp.MustCompile(`^(P[0-9]+)\.org$`)
	stepPattern = regexp.MustCompile(`^(P[0-9]+\.)?S[0-9]+$`)
)

// Load reads the config, defaulting to ~/git/org/agents and this machine's
// short host name.
func Load() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	c := Config{Dir: filepath.Join(home, "git", "org", "agents"), Host: host}
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
	if file.Plans.Dir != "" {
		c.Dir = file.Plans.Dir
	}
	if file.Plans.Host != "" {
		c.Host = file.Plans.Host
	}
	if !filepath.IsAbs(c.Dir) || !idPattern.MatchString(c.Host) {
		return Config{}, fmt.Errorf("%s: plans.dir must be absolute and plans.host a plain name: %+v", repos.Path(), c)
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
}

// Created is what New made.
type Created struct {
	Plan string `json:"plan"`
	File string `json:"file"`
	ID   string `json:"id"`
	Log  string `json:"log"`
	// Excluded is the exclude file that keeps the plans directory out of
	// the repository it sits in, if it does.
	Excluded string `json:"excluded,omitempty"`
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
// its parent's. A new plan's directory is <host>/<date>-<slug>, with -2, -3
// and so on for a slug already used that day.
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
	if o.Parent != "" || o.Part != "" {
		return c.newPart(o)
	}
	if !slugPattern.MatchString(o.Slug) {
		return Created{}, refusal.New("slug %q is not lower-case words joined by hyphens. Pass one such as `--slug add-run-loop`", o.Slug)
	}
	ready, err := c.Init()
	if err != nil {
		return Created{}, err
	}
	host, excluded := ready.Dir, ready.Excluded
	base := now.Format("2006-01-02") + "-" + o.Slug
	name := ""
	for i := 1; name == "" && i < 1000; i++ {
		try := base
		if i > 1 {
			try = fmt.Sprintf("%s-%d", base, i)
		}
		switch err := os.Mkdir(filepath.Join(host, try), 0o755); {
		case err == nil:
			name = try
		case !errors.Is(err, fs.ErrExist):
			return Created{}, err
		}
	}
	if name == "" {
		return Created{}, fmt.Errorf("no free directory for %s under %s", base, host)
	}
	p := Plan{Dir: filepath.Join(host, name), Name: name}
	text := planText(o, name, map[string]string{"HOST": c.Host}, true)
	if err := writeNew(p.file(""), text); err != nil {
		return Created{}, err
	}
	if err := p.append("#+title: Log of " + name + "\n"); err != nil {
		return Created{}, err
	}
	return Created{Plan: p.Dir, File: p.file(""), ID: name, Log: p.log(), Excluded: excluded}, nil
}

func (c Config) newPart(o NewOptions) (Created, error) {
	if o.Parent == "" || !partPattern.MatchString(o.Part) {
		return Created{}, refusal.New("a part needs its parent plan and a part ID such as P1. Pass both, as `--parent PLAN --part P1`")
	}
	p, err := c.Open(o.Parent)
	if err != nil {
		return Created{}, err
	}
	items, err := p.items()
	if err != nil {
		return Created{}, err
	}
	if item, ok := items[o.Part]; !ok || item.File != p.file("") {
		return Created{}, refusal.New("%s is not in %s's Distribution. Ask whoever coordinates the plan to add it, or use a part ID `agent-plan status %s` lists", o.Part, p.Name, p.Dir)
	}
	text := planText(o, o.Part, map[string]string{"PARENT": "[[file:plan.org]]"}, false)
	file := p.file(o.Part)
	if err := writeNew(file, text); errors.Is(err, fs.ErrExist) {
		return Created{}, refusal.New("%s already has its plan file, %s. Edit that file instead; `agent-plan goal %s --part %s` prints its goal", o.Part, file, p.Dir, o.Part)
	} else if err != nil {
		return Created{}, err
	}
	return Created{Plan: p.Dir, File: file, ID: o.Part, Log: p.log()}, nil
}

// planText is a new plan file: the parent's sections, and Distribution for
// a parent alone.
func planText(o NewOptions, id string, extra map[string]string, parent bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#+title: %s\n#+TODO: TODO NEXT | DONE\n\n* Plan\n:PROPERTIES:\n:ID: %s\n", o.Title, id)
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
	property("STATUS", "planning")
	property("PULL_REQUESTS", "")
	property("PIPELINE", "full")
	b.WriteString(":END:\n")
	sections := []string{"Goal", "Decisions", "Steps", "Distribution", "Open questions", "Verification"}
	for _, s := range sections {
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

// Ready is a host's plans directory, made ready for plans.
type Ready struct {
	Dir      string `json:"dir"`
	Excluded string `json:"excluded,omitempty"`
}

// Init makes this host's plans directory and lists the plans directory in
// the exclude file of the repository around it, as New does, for a session
// whose sandbox may write only inside the host's directory.
func (c Config) Init() (Ready, error) {
	host := filepath.Join(c.Dir, c.Host)
	if err := os.MkdirAll(host, 0o755); err != nil {
		return Ready{}, err
	}
	excluded, err := c.exclude()
	return Ready{Dir: host, Excluded: excluded}, err
}

// exclude lists the plans directory in the exclude file of the repository
// whose top level holds it, the one the ring commits to, and returns that
// file. It does nothing when no repository has its top level there: git
// runs aimed at that directory alone, never at the caller's repository.
func (c Config) exclude() (string, error) {
	parent := filepath.Dir(c.Dir)
	top, err := git.Run("-C", parent, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil
	}
	realTop, err1 := filepath.EvalSymlinks(top)
	realParent, err2 := filepath.EvalSymlinks(parent)
	if err1 != nil || err2 != nil || realTop != realParent {
		return "", nil
	}
	path, err := git.Run("-C", parent, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return "", err
	}
	name := filepath.Base(c.Dir)
	entry := "/" + name + "/"
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if l := strings.TrimSpace(line); l == entry || l == name || l == name+"/" || l == "/"+name {
			return path, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		entry = "\n" + entry
	}
	if _, err := f.WriteString(entry + "\n"); err != nil {
		return "", err
	}
	return path, f.Close()
}

// Plan is one plan's directory.
type Plan struct {
	Dir  string `json:"dir"`
	Name string `json:"name"`
}

func (p Plan) file(part string) string {
	if part == "" {
		return filepath.Join(p.Dir, "plan.org")
	}
	return filepath.Join(p.Dir, part+".org")
}

func (p Plan) log() string { return filepath.Join(p.Dir, "log.org") }

// Open finds the plan arg names: its directory, or a file in it. Only a
// plan directory under the plans directory is one.
func (c Config) Open(arg string) (Plan, error) {
	notPlan := func() error {
		return refusal.New("%s is not a plan under %s. Pass the directory `agent-plan new` printed, or find it with `agent-plan find`", arg, c.Dir)
	}
	dir, err := filepath.Abs(arg)
	if err != nil {
		return Plan{}, err
	}
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return Plan{}, notPlan()
	}
	root, err := filepath.EvalSymlinks(c.Dir)
	if err != nil {
		return Plan{}, notPlan()
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || strings.HasPrefix(rel, "..") || len(strings.Split(rel, string(filepath.Separator))) != 2 {
		return Plan{}, notPlan()
	}
	p := Plan{Dir: real, Name: filepath.Base(real)}
	for _, f := range []string{p.file(""), p.log()} {
		if info, err := os.Lstat(f); err != nil || !info.Mode().IsRegular() {
			return Plan{}, notPlan()
		}
	}
	return p, nil
}

// item is a heading with an ID in one of a plan's files.
type item struct {
	ID, Title, File, Part string
	Props                 map[string]string
}

// files are the plan's own files: plan.org, then each part's in order.
func (p Plan) files() ([]string, error) {
	entries, err := os.ReadDir(p.Dir)
	if err != nil {
		return nil, err
	}
	var parts []int
	for _, e := range entries {
		if m := partFile.FindStringSubmatch(e.Name()); m != nil && e.Type().IsRegular() {
			n, _ := strconv.Atoi(m[1][1:])
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
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		part := ""
		if m := partFile.FindStringSubmatch(filepath.Base(f)); m != nil {
			part = m[1]
		}
		for _, h := range parseOrg(string(data)) {
			id := h.Props["ID"]
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, item{ID: id, Title: h.Title, File: f, Part: part, Props: h.Props})
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
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return "", refusal.New("%s has no plan file for part %s. The part's sub-agent creates it with `agent-plan new --parent %s --part %s`", p.Name, part, p.Dir, part)
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
	Plan    string   `json:"plan"`
	File    string   `json:"file"`
	Part    string   `json:"part,omitempty"`
	Title   string   `json:"title"`
	State   string   `json:"status"`
	Matched []string `json:"matched"`
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
	dirs, err := os.ReadDir(filepath.Join(c.Dir, c.Host))
	if errors.Is(err, fs.ErrNotExist) {
		return []Found{}, nil
	}
	if err != nil {
		return nil, err
	}
	found := []Found{}
	for i := len(dirs) - 1; i >= 0; i-- {
		p, err := c.Open(filepath.Join(c.Dir, c.Host, dirs[i].Name()))
		if err != nil {
			continue
		}
		files, err := p.files()
		if err != nil {
			continue
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
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
			part := ""
			if m := partFile.FindStringSubmatch(filepath.Base(f)); m != nil {
				part = m[1]
			}
			found = append(found, Found{Plan: p.Dir, File: f, Part: part, Title: orgTitle(string(data)),
				State: top.Props["STATUS"], Matched: matched})
		}
	}
	return found, nil
}
