package routes

import (
	"bufio"
	"bytes"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Rule routes a commit to a focused review: one that changes a path
// matching one of Paths, or, with Lines, adds or removes a line matching
// one of Lines, in a file matching Paths where it has any. Paths are globs
// over the path from the top of the repository, in which `**/` is any run
// of directories, `/**` at the end anything below, `*` any run of other
// characters than `/`, and `?` one of them; Lines are RE2 expressions.
// With Removed, only a removed line matches Lines, as a line changed in
// place is removed and added again.
type Rule struct {
	Route   string   `json:"route"`
	Paths   []string `json:"paths,omitempty"`
	Lines   []string `json:"lines,omitempty"`
	Removed bool     `json:"removed,omitempty"`
	Why     string   `json:"why"`
}

// Check refuses a rule that names no known route, matches nothing, or
// does not compile, or gives no reason.
func (r Rule) Check() error {
	fix := "ask the operator to fix the rule in their routes settings"
	switch {
	case !IsKnown(r.Route):
		return refusal.New("the rule's route %q is not one a focused review exists for (%s); %s", r.Route,
			strings.Join(Known, ", "), fix)
	case len(r.Paths) == 0 && len(r.Lines) == 0:
		return refusal.New("a rule of route %s names no paths and no lines, so it routes nothing; %s", r.Route, fix)
	case r.Removed && len(r.Lines) == 0:
		return refusal.New("a rule of route %s takes only removed lines but names none to match; %s", r.Route, fix)
	case strings.TrimSpace(r.Why) == "":
		return refusal.New("a rule of route %s gives no reason, which the routing reports; %s", r.Route, fix)
	}
	for _, g := range r.Paths {
		if _, err := globRegexp(g); err != nil {
			return refusal.New("the rule's path %q is not a glob this takes (%v); %s", g, err, fix)
		}
	}
	for _, l := range r.Lines {
		if _, err := regexp.Compile(l); err != nil {
			return refusal.New("the rule's line %q is not an RE2 expression (%v); %s", l, err, fix)
		}
	}
	return nil
}

// Builtin are the rules every repository's changes are routed by; the
// operator's settings may add to them, never take from them.
var Builtin = []Rule{
	{Route: "security", Paths: []string{"**/*.go"},
		Lines: []string{`"os/exec"`, `\bexec\.Command`, `"syscall"`, `\bsyscall\.`, `"unsafe"`},
		Why:   "Go code that runs programs or reaches the system directly"},
	{Route: "security", Paths: []string{"**/*.go"},
		Lines: []string{`"net/http"`, `\bnet\.(Listen|Dial)`, `InsecureSkipVerify`},
		Why:   "Go code that listens, dials or serves over the network"},
	{Route: "security", Paths: []string{"**/*.go"}, Lines: []string{`"crypto/`},
		Why: "Go code that uses cryptography"},
	{Route: "security", Paths: []string{"**/*.go"},
		Lines: []string{`\bos\.(WriteFile|OpenFile|Mkdir|MkdirAll|Chmod|Chown)\(`, `\b0o?[0-7]{3}\b`},
		Why:   "Go code that sets file permissions"},
	{Route: "security", Paths: []string{"**/go.mod", "**/go.sum", "**/flake.lock", "**/package-lock.json", "**/yarn.lock",
		"**/pnpm-lock.yaml", "**/Cargo.lock", "**/Gemfile.lock", "**/poetry.lock", "**/uv.lock"},
		Why: "a change of dependencies"},
	{Route: "security", Paths: []string{".github/workflows/**"}, Why: "a change of CI workflows"},
	{Route: "security", Paths: []string{"**/AGENTS.md", "**/CLAUDE.md", "**/.claude/**", "**/.codex/**", "**/.mcp.json",
		"**/policy.json", "**/mcp.json"},
		Why: "a change of agent or tooling configuration"},
	{Route: "compat", Paths: []string{"**/*.go"},
		Lines: []string{`\b(flag|fs|\w*[Ff]lags?|\w*[Ff]lagSet)\.(Bool|BoolFunc|BoolVar|Duration|DurationVar|Float64|Float64Var|` +
			`Func|Int|Int64|Int64Var|IntVar|String|StringVar|TextVar|Uint|Uint64|Uint64Var|UintVar|Var)\(`},
		Why: "Go command-line flags defined"},
	// A field added beside the others is read by nothing yet, so only a
	// tag removed, or renamed, routes.
	{Route: "compat", Paths: []string{"**/*.go"}, Lines: []string{"\\bjson:\""}, Removed: true,
		Why: "Go JSON field names removed or renamed, which stored data and other programs read"},
	{Route: "compat", Paths: []string{"**/*.nix"},
		Lines: []string{`\bmk(Option|EnableOption|PackageOption|RenamedOptionModule|RemovedOptionModule|AliasOptionModule)\b`},
		Why:   "Nix options declared"},
	{Route: "compat", Paths: []string{"**/*.el"},
		Lines: []string{`\((defcustom|define-obsolete-variable-alias|make-obsolete-variable)\s`},
		Why:   "Emacs customisations declared"},
	// Plan files outlive agent-tools' versions, and Emacs and other hosts
	// read them: their kinds, keywords and property names are defined as
	// quoted words in these files.
	{Route: "compat", Paths: []string{"**/agent-tools/internal/plans/*.go"},
		Lines: []string{`\b\w*(Kinds?|Properties)\s*:?=[^=]`, `":[A-Z][A-Z0-9_]*:`, `"[A-Z][A-Z0-9_]{2,}"`,
			`^\s*"[a-z][a-z0-9-]*"(,\s*"[a-z][a-z0-9-]*")*,?\s*\}?,?\s*$`},
		Why: "agent-tools' plan files: event kinds, keywords and property names"},
}

// Route is a route a change needs, why, and the commits that need it,
// oldest first.
type Route struct {
	Route   string   `json:"route"`
	Why     []string `json:"why"`
	Commits []string `json:"commits"`
}

// Scan routes each commit of base..tip, merges left out, in the repository
// at top, by rules: by the paths it changes and the lines it adds or
// removes. Routes come in name order.
func Scan(top, base, tip string, rules []Rule) ([]Route, error) {
	cs, err := compile(rules)
	if err != nil {
		return nil, err
	}
	out, err := git.Run("-C", top, "rev-list", "--reverse", "--no-merges", base+".."+tip)
	if err != nil {
		return nil, err
	}
	found := map[string]*Route{}
	for _, commit := range strings.Fields(out) {
		files, err := changes(top, commit)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			if !matches(files, c) {
				continue
			}
			r := found[c.Route]
			if r == nil {
				r = &Route{Route: c.Route}
				found[c.Route] = r
			}
			if !slices.Contains(r.Commits, commit) {
				r.Commits = append(r.Commits, commit)
			}
			if !slices.Contains(r.Why, c.Why) {
				r.Why = append(r.Why, c.Why)
			}
		}
	}
	routes := []Route{}
	for _, name := range slices.Sorted(maps.Keys(found)) {
		routes = append(routes, *found[name])
	}
	return routes, nil
}

// compiled is a rule with its globs and expressions compiled.
type compiled struct {
	Rule
	paths, lines []*regexp.Regexp
}

// compile checks and compiles rules.
func compile(rules []Rule) ([]compiled, error) {
	var cs []compiled
	for _, r := range rules {
		if err := r.Check(); err != nil {
			return nil, err
		}
		c := compiled{Rule: r}
		for _, g := range r.Paths {
			re, _ := globRegexp(g)
			c.paths = append(c.paths, re)
		}
		for _, l := range r.Lines {
			c.lines = append(c.lines, regexp.MustCompile(l))
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// matches reports whether a commit changing files, each with the lines it
// adds and removes as changes gives them, matches rule c.
func matches(files map[string][]string, c compiled) bool {
	for file, changed := range files {
		if len(c.paths) > 0 && !slices.ContainsFunc(c.paths, func(re *regexp.Regexp) bool { return re.MatchString(file) }) {
			continue
		}
		if len(c.lines) == 0 {
			return true
		}
		for _, line := range changed {
			if c.Removed && line[0] != '-' {
				continue
			}
			if slices.ContainsFunc(c.lines, func(re *regexp.Regexp) bool { return re.MatchString(line[1:]) }) {
				return true
			}
		}
	}
	return false
}

// changes maps each path commit changes to the lines it adds or removes
// there, each still marked + or -, against its first parent, or against nothing for a root commit.
// The patch's headers quote some paths, so its sections are matched to
// the NUL-separated raw list by order instead: one section for each
// change, two for a change of type.
func changes(top, commit string) (map[string][]string, error) {
	raw, err := git.Bytes(nil, "-C", top, "diff-tree", "-r", "-z", "--raw", "--no-commit-id", "--no-renames", "--root", commit)
	if err != nil {
		return nil, err
	}
	files := map[string][]string{}
	var sections []string
	fields := strings.Split(string(raw), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		meta, path := fields[i], fields[i+1]
		files[path] = nil
		sections = append(sections, path)
		if strings.HasSuffix(meta, " T") {
			sections = append(sections, path)
		}
	}
	patch, err := git.Bytes(nil, "-C", top, "diff-tree", "-r", "-p", "-U0", "--no-commit-id",
		"--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--root", commit)
	if err != nil {
		return nil, err
	}
	file, header, n := "", false, 0
	scan := bufio.NewScanner(bytes.NewReader(patch))
	scan.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scan.Scan() {
		line := scan.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file, header = "", true
			if n < len(sections) {
				file = sections[n]
			}
			n++
		case strings.HasPrefix(line, "@@"):
			header = false
		case !header && file != "" && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")):
			files[file] = append(files[file], line)
		}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	if n != len(sections) {
		return nil, fmt.Errorf("the patch of %s has %d sections for its %d changes", commit, n, len(sections))
	}
	return files, nil
}

// Match reports whether path matches glob, as Rule's Paths are matched.
func Match(glob, path string) bool {
	re, err := globRegexp(glob)
	return err == nil && re.MatchString(path)
}

// globRegexp is glob as an anchored expression.
func globRegexp(glob string) (*regexp.Regexp, error) {
	if glob == "" || strings.ContainsAny(glob, "[]\\") {
		return nil, fmt.Errorf("a glob is not empty and holds no brackets or backslashes")
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); {
		switch rest := glob[i:]; {
		case strings.HasPrefix(rest, "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case rest == "/**":
			b.WriteString("/.*")
			i += 3
		case strings.HasPrefix(rest, "**"):
			b.WriteString(".*")
			i += 2
		case rest[0] == '*':
			b.WriteString("[^/]*")
			i++
		case rest[0] == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(rest[:1]))
			i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// sharedMarkers mark a file that holds tests beside the code they test, by
// its extension. Go keeps its tests in files of their own.
var sharedMarkers = map[string]*regexp.Regexp{
	".rs": regexp.MustCompile(`#\[cfg\(test\)\]`),
	".py": regexp.MustCompile(`(?m)^\s*>>> `),
}

// SharedFiles are the files base..tip changes in the repository at top
// that hold tests and code together at tip, in path order: red-check runs
// a commit's test files without the rest of it, which it cannot do for
// these, so their tests are the reviewer's to judge.
func SharedFiles(top, base, tip string) ([]string, error) {
	names, err := git.Bytes(nil, "-C", top, "diff", "--name-only", "-z", "--no-renames", "--diff-filter=d", base, tip)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, f := range strings.Split(string(names), "\x00") {
		marker := sharedMarkers[path.Ext(f)]
		if f == "" || marker == nil {
			continue
		}
		data, err := git.Bytes(nil, "-C", top, "cat-file", "blob", tip+":"+f)
		if err == nil && marker.Match(data) {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out, nil
}
