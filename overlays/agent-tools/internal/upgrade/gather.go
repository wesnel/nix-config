package upgrade

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/goproxy"
	"github.com/wesnel/agent-tools/internal/repos"
)

// Limits bound what one review gathers: Pins moved pins, Text bytes of
// each text, Total bytes in all, and Calls GitHub API calls for each pin.
type Limits struct {
	Pins, Text, Total, Calls int
}

// DefaultLimits are a review's limits.
var DefaultLimits = Limits{Pins: 30, Text: 16 << 10, Total: 120 << 10, Calls: 2}

// Options are how Gather gathers.
type Options struct {
	// Cache is the directory results are kept in, by source and versions,
	// so that gathering the same pin again fetches nothing. It is a fetch
	// cache alone: nothing reads it to check a reply.
	Cache string
	// Clones is the directory local clones are found in, as
	// Clones/OWNER/REPO.
	Clones string
	// Private are comma-separated module path patterns, as GOPRIVATE's: a
	// module one matches is never requested from the module proxy, and is
	// gathered only from a local clone or through the GitHub login.
	Private string
	// Own is whether the change is the operator's own. Only then do the
	// Private patterns name private GitHub repositories that may be read;
	// for anyone else's every repository is read only where GitHub says
	// it is public.
	Own    bool
	Limits Limits
}

// DefaultPrivateModules are the private module patterns where the
// operator's config names none: ngrok's, which the host's Go settings
// leave out so that go never fetches them over SSH.
var DefaultPrivateModules = []string{"github.com/ngrok/*", "github.com/ngrok-private/*", "go.ngrok.com/*"}

// privateVars are the Go settings that name private modules.
var privateVars = []string{"GOPRIVATE", "GONOPROXY", "GONOSUMDB"}

// DefaultOptions are a review's options on this host: the cache under the
// XDG cache directory, clones under ~/git/github.com, and as private
// modules those GOPRIVATE, GONOPROXY and GONOSUMDB name, in the
// environment and as `go env` gives them, with review.privateModules of
// the operator's config, DefaultPrivateModules where it names none.
func DefaultOptions(ctx context.Context) Options {
	o := Options{Limits: DefaultLimits}
	home, _ := os.UserHomeDir()
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" && home != "" {
		cache = filepath.Join(home, ".cache")
	}
	if cache != "" {
		o.Cache = filepath.Join(cache, "agent-tools", "upgrade")
	}
	if home != "" {
		o.Clones = filepath.Join(home, "git", "github.com")
	}
	patterns := privateModules(repos.Path())
	for _, v := range privateVars {
		patterns = append(patterns, os.Getenv(v))
	}
	cmd := exec.CommandContext(ctx, "go", append([]string{"env"}, privateVars...)...)
	cmd.Dir = os.TempDir()
	if out, err := cmd.Output(); err == nil {
		patterns = append(patterns, strings.Fields(string(out))...)
	}
	o.Private = strings.Join(slices.DeleteFunc(patterns, func(p string) bool { return strings.TrimSpace(p) == "" }), ",")
	return o
}

// privateModules are review.privateModules of the config file at path, or
// DefaultPrivateModules where it names none; an empty list is none.
func privateModules(path string) []string {
	var c struct {
		Review struct {
			PrivateModules *[]string `json:"privateModules"`
		} `json:"review"`
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &c) != nil || c.Review.PrivateModules == nil {
		return slices.Clone(DefaultPrivateModules)
	}
	return slices.Clone(*c.Review.PrivateModules)
}

// Text is upstream text gathered for a pin: what it is and where it came
// from, and the directories it was read from, which a reviewer may read
// further.
type Text struct {
	Source string   `json:"source"`
	Body   string   `json:"body"`
	Paths  []string `json:"paths,omitempty"`
}

// Gathered is a moved pin with what was gathered for it, and, where
// nothing was, why.
type Gathered struct {
	Pin   Pin
	Texts []Text
	Why   string
}

// Read reports whether any upstream text was gathered for the pin.
func (g Gathered) Read() bool { return len(g.Texts) > 0 }

// Gather gathers what changed upstream for each moved pin of pins, within
// o's limits: from a local clone that has both commits, the changelog
// files of both versions' source trees (GitHub tarballs nix fetches over
// HTTPS and checks against the lock's NAR hash, or Go modules from the
// public proxy), and GitHub's releases and comparison of the two, over
// HTTPS with the user's gh login. Nothing goes over SSH, a flake node of
// another type than github is never fetched, and a module o.Private
// matches is never asked of the module proxy. A GitHub repository is read
// only where GitHub says it is public or, with o.Own, o.Private names it.
func Gather(ctx context.Context, pins []Pin, o Options) []Gathered {
	var out []Gathered
	left := o.Limits.Total
	n := 0
	for _, p := range pins {
		if !p.Moved() {
			continue
		}
		n++
		g := Gathered{Pin: p}
		switch {
		case n > o.Limits.Pins:
			g.Why = fmt.Sprintf("the review gathers for the first %d moved pins only", o.Limits.Pins)
		case left <= 0:
			g.Why = fmt.Sprintf("the %d KiB of upstream text a review takes went to the pins before it", o.Limits.Total>>10)
		default:
			g = gatherPin(ctx, p, o, &left)
		}
		out = append(out, g)
	}
	return out
}

// gatherPin gathers p's sources in order, taking each text from left.
func gatherPin(ctx context.Context, p Pin, o Options, left *int) Gathered {
	g := Gathered{Pin: p}
	var notes []string
	add := func(t Text, err error, what string) {
		switch {
		case err != nil:
			notes = append(notes, what+": "+oneLine(err.Error()))
		case strings.TrimSpace(t.Body) == "":
			notes = append(notes, what+": nothing")
		case *left <= 0:
			notes = append(notes, what+": no room left in the review")
		default:
			t.Body = cut(t.Body, min(o.Limits.Text, *left))
			*left -= len(t.Body)
			g.Texts = append(g.Texts, t)
		}
	}
	private := p.Module != "" && matchPrefixPatterns(o.Private, p.Module) ||
		p.OldModule != "" && matchPrefixPatterns(o.Private, p.OldModule)
	switch {
	case p.Kind == "flake-nix":
		g.Why = p.File + " changed, and its inputs are what nix evaluates them to, which this review never does on the host; " +
			"read the diff of its inputs, and whether flake.lock locks what they now name (" + p.Note + ", by a text scan " +
			"that may miss some); that diff is all there is to read, and reading it settles the pin"
		return g
	case p.Kind == "nix-unread" || p.Kind == "actions-unread":
		g.Why = p.File + "'s changed lines " + p.Note + " name what may be a pin this review's scanner did not read; those " +
			"lines are all there is to read, and reading them settles the pin"
		return g
	case p.Note != "":
		g.Why = p.File + " cannot be read as its tool reads it (" + p.Note + "), so what it pins is not known; read its diff"
		return g
	case p.HashOnly():
		g.Why = "only its hash or its directory moved, from " + p.Old + " to " + p.New + ", at the same " + p.NewRef +
			", which has no upstream changes between the two to read; find what changed it"
		return g
	case p.Kind == "lockfile":
		g.Why = "agent-tools reads no versions from " + path.Base(p.File) + "; read its diff"
		return g
	case p.Kind == "go" || p.Kind == "toolchain":
		g.Why = "the Go release notes between " + p.Old + " and " + p.New + " are not gathered"
		return g
	case p.Kind == "flake" && p.Owner == "":
		g.Why = fmt.Sprintf("a %s node, which this review never fetches", cmp.Or(p.Type, "non-github"))
		return g
	}
	if why := unreadable(p, o, private); why != "" {
		g.Why = why
		return g
	}
	logged := false
	if p.Owner != "" && validName(p.Owner) && validName(p.Repo) && validRef(p.OldRef) && validRef(p.NewRef) {
		t, err := cached(o, "clone", []string{o.Clones, p.Owner, p.Repo, p.OldRef, p.NewRef}, func() (Text, error) {
			return cloneLog(ctx, o.Clones, p)
		})
		logged = err == nil && t.Body != ""
		add(t, err, "local clone")
	}
	if private {
		notes = append(notes, "source trees: "+p.Module+" is a private module, which the module proxy is never asked for")
	} else if t, err, ok := sourceTrees(ctx, p, o); ok {
		add(t, err, "source trees")
	}
	if p.Owner != "" && validName(p.Owner) && validName(p.Repo) {
		for _, s := range githubSources(ctx, p, o, logged) {
			add(s.text, s.err, s.what)
		}
	}
	if !g.Read() {
		g.Why = strings.Join(notes, "; ")
		if g.Why == "" {
			g.Why = "no source knows where it comes from"
		}
	}
	return g
}

// repoPrivate reports whether GitHub, asked with the user's login, says
// the repository owner/repo is private.
func repoPrivate(owner, repo string) (bool, error) {
	var r struct {
		Private *bool `json:"private"`
	}
	if err := gh.REST("GET", fmt.Sprintf("repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo)), nil, &r); err != nil {
		return false, err
	}
	if r.Private == nil {
		return false, errors.New("GitHub's reply names no visibility")
	}
	return *r.Private, nil
}

// unreadable is why p's repository is not read, or "": the pins a change
// moves are the author's to name, and the user's login, local clones and
// nix's tokens reach private repositories, so a repository is read only
// where GitHub says it is public, or, for the operator's own change
// (o.Own), where it is a private module, or its github.com path, that
// o.Private names. Anyone else's change could name the operator's private
// repositories to have them read into a review it sees.
func unreadable(p Pin, o Options, privateModule bool) string {
	if p.Owner == "" || !validName(p.Owner) || !validName(p.Repo) ||
		o.Own && (privateModule || matchPrefixPatterns(o.Private, "github.com/"+p.Owner+"/"+p.Repo)) {
		return ""
	}
	private, err := repoPrivate(p.Owner, p.Repo)
	switch {
	case err != nil:
		return fmt.Sprintf("github.com/%s/%s's visibility could not be read (%s), and only a public repository, or for the "+
			"operator's own change a private one review.privateModules names, is read", p.Owner, p.Repo, oneLine(err.Error()))
	case private && !o.Own:
		return fmt.Sprintf("github.com/%s/%s is private, and the change is not the operator's own, so nothing of it is "+
			"read: for anyone else's change only a public repository is read", p.Owner, p.Repo)
	case private:
		return fmt.Sprintf("github.com/%s/%s is private, and review.privateModules does not name it, so nothing of it is "+
			"read: a private repository is read only where the operator's config names it", p.Owner, p.Repo)
	}
	return ""
}

var (
	// repoName is a GitHub owner or repository name, which goes into a
	// path and a URL.
	repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	// refName is a commit or tag, which goes on a command line and in a
	// URL.
	refName = regexp.MustCompile(`^[A-Za-z0-9_+][A-Za-z0-9_./+-]*$`)
)

func validName(s string) bool { return repoName.MatchString(s) && s != "." && s != ".." }

func validRef(s string) bool { return refName.MatchString(s) && !strings.Contains(s, "..") }

// oneLine is s on one line, cut to 300 bytes.
func oneLine(s string) string {
	return cut(strings.Join(strings.Fields(s), " "), 300)
}

// cut is s cut to at most n bytes, at a rune, saying so.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const mark = "\n… (cut here)"
	end := max(n-len(mark), 0)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + mark
}

// cacheKept is how long a cached result is kept.
const cacheKept = 30 * 24 * time.Hour

// cached is fetch's result for source and key, kept in o.Cache: read from
// there where it is, fetched and kept where not.
func cached(o Options, source string, key []string, fetch func() (Text, error)) (Text, error) {
	sum := sha256.Sum256([]byte(source + "\x00" + strings.Join(key, "\x00")))
	file := ""
	if o.Cache != "" && CacheProblem(o) == nil {
		file = filepath.Join(o.Cache, source+"-"+hex.EncodeToString(sum[:16])+".json")
		var t Text
		if info, err := os.Lstat(file); err == nil && info.Mode().IsRegular() {
			if data, err := os.ReadFile(file); err == nil && json.Unmarshal(data, &t) == nil {
				return t, nil
			}
		}
	}
	t, err := fetch()
	if err != nil || file == "" {
		return t, err
	}
	if data, err := json.Marshal(t); err == nil {
		tmp, err := os.CreateTemp(o.Cache, ".write-*")
		if err == nil {
			_, werr := tmp.Write(data)
			if tmp.Close() == nil && werr == nil {
				os.Rename(tmp.Name(), file)
			}
			os.Remove(tmp.Name())
		}
		prune(o.Cache, time.Now().Add(-cacheKept))
	}
	return t, nil
}

// CacheProblem is why o's cache directory is refused, or nil: a review
// whose cache is refused neither reads nor keeps anything there, and
// fetches every pin again.
func CacheProblem(o Options) error {
	if o.Cache == "" {
		return nil
	}
	if err := privateDir(o.Cache); err != nil {
		return fmt.Errorf("the cache directory is refused: %v", err)
	}
	return nil
}

// privateDir makes dir, mode 0700, where it does not exist, and refuses a
// cache directory anyone but this user could have written: one that is
// not a directory of their own, mode 0700, as what is read from it goes
// into a reviewer's prompt.
func privateDir(dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("the cache %s is not a directory of this user's with mode 0700", dir)
	}
	return nil
}

// prune removes the results in dir last written before cutoff.
func prune(dir string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && strings.HasSuffix(e.Name(), ".json") &&
			info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// localGit is the environment git runs in against a local clone: it may
// fetch nothing, a missing object of a partial clone included, and never
// over SSH.
var localGit = []string{"GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=file", "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=false",
	// Replace refs and grafts would show other commits than the clone
	// has under the revisions named.
	"GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=/dev/null"}

// cloneConfig is the configuration every command in a local clone runs
// with, beside the git package's: no signature checked (which runs
// gpg.program), no pager, no mailmap and no other program the user's
// config names.
var cloneConfig = []string{"-c", "log.showSignature=false", "-c", "gpg.program=false", "-c", "gpg.ssh.program=false",
	"-c", "gpg.x509.program=false", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "core.pager=cat",
	"-c", "pager.log=false", "-c", "log.mailmap=false", "-c", "core.sshCommand=false"}

// cloneTimeout bounds each git command in a local clone, whose files, an
// object store's alternates among them, may never end.
var cloneTimeout = 30 * time.Second

// cloneGit runs git with args in the local clone at dir, with cloneConfig
// and localGit's environment, for at most cloneTimeout. The configuration
// the environment hands git (GIT_CONFIG_PARAMETERS, GIT_CONFIG_COUNT and
// its keys and values) never reaches it.
func cloneGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append(append([]string{"--no-replace-objects", "-C", dir}, cloneConfig...), args...)...)
	cmd.Dir = os.TempDir()
	env := slices.DeleteFunc(gitenv.Without(os.Environ()), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return name == "GIT_CONFIG_PARAMETERS" || name == "GIT_CONFIG_COUNT" ||
			strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") ||
			name == "GIT_REPLACE_REF_BASE" || name == "GIT_GRAFT_FILE" || name == "GIT_NO_REPLACE_OBJECTS"
	})
	cmd.Env = append(env, localGit...)
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("git %s in %s stopped after %s", args[0], dir, cloneTimeout)
	case err != nil:
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, oneLine(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// countsOnly are the repositories whose history between two pins is too
// long to list, which only their counts stand for.
var countsOnly = map[string]bool{"nixos/nixpkgs": true}

// cloneLog is the history between p's two refs in the local clone of its
// repository under clones, where that has both.
func cloneLog(ctx context.Context, clones string, p Pin) (Text, error) {
	if clones == "" {
		return Text{}, errors.New("no directory of clones")
	}
	dir := filepath.Join(clones, p.Owner, p.Repo)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return Text{}, fmt.Errorf("none at %s", dir)
	}
	// The clone is the operator's, but anything may have written its
	// config: one that names a program is never run.
	if err := git.CheckConfig(dir); err != nil {
		return Text{}, err
	}
	run := func(args ...string) (string, error) { return cloneGit(ctx, dir, args...) }
	// A shallow clone's history stops where its boundary says, which may
	// hide what lies between the two revisions.
	if shallow, err := run("rev-parse", "--path-format=absolute", "--git-path", "shallow"); err != nil {
		return Text{}, err
	} else if _, err := os.Lstat(shallow); err == nil {
		return Text{}, fmt.Errorf("%s is a shallow clone, whose history is cut short", dir)
	}
	for _, r := range []string{p.OldRef, p.NewRef} {
		if _, err := run("cat-file", "-e", "--end-of-options", r+"^{commit}"); err != nil {
			return Text{}, fmt.Errorf("%s does not have %s", dir, r)
		}
	}
	span := p.OldRef + ".." + p.NewRef
	if countsOnly[strings.ToLower(p.Owner+"/"+p.Repo)] {
		n, err := run("rev-list", "--count", "--end-of-options", span)
		if err != nil {
			return Text{}, err
		}
		return Text{Source: fmt.Sprintf("the count of commits %s in the local clone %s", span, dir),
			Body: n + " commits"}, nil
	}
	out, err := run("log", "--no-merges", "--no-show-signature", "--no-decorate", "--no-color", "--format=%h %s", "--end-of-options", span)
	if err != nil {
		return Text{}, err
	}
	if back, _ := run("rev-list", "--count", "--end-of-options", p.NewRef+".."+p.OldRef); back != "" && back != "0" {
		out = fmt.Sprintf("(a downgrade: %s commits of the old version are not in the new one)\n%s", back, out)
	}
	return Text{Source: fmt.Sprintf("git log --no-merges %s in the local clone %s", span, dir), Body: out}, nil
}

// notesNames start the names of the changelog files at the top of a
// source tree, in capitals.
var notesNames = []string{"CHANGELOG", "NEWS", "RELEASE"}

// knownNotes are where repositories keep their release notes besides,
// as their trees hold them.
var knownNotes = map[string][]string{
	"nixos/nixpkgs":              {"nixos/doc/manual/release-notes", "doc/release-notes"},
	"nix-community/home-manager": {"modules/misc/news"},
}

// sourceTrees is the changelog files' diff between p's two source trees,
// where p has trees this fetches: GitHub tarballs for a flake's github
// node or a fetchFromGitHub pin with both NAR hashes, and a Go module's
// two versions. ok is false where it has none.
func sourceTrees(ctx context.Context, p Pin, o Options) (Text, error, bool) {
	switch {
	case p.Kind == "go-module":
		t, err := cached(o, "go", []string{p.OldModule, p.Module, p.Old, p.New}, func() (Text, error) {
			return moduleTrees(ctx, p, o)
		})
		return t, err, true
	case (p.Kind == "flake" || p.Kind == "nix") && p.Owner != "" && validName(p.Owner) && validName(p.Repo) &&
		validRef(p.OldRef) && validRef(p.NewRef) && strings.HasPrefix(p.OldHash, "sha256-") && strings.HasPrefix(p.NewHash, "sha256-"):
		t, err := cached(o, "tree", []string{p.Owner, p.Repo, p.OldRef, p.OldHash, p.NewRef, p.NewHash}, func() (Text, error) {
			old, err := prefetch(ctx, p.Owner, p.Repo, p.OldRef, p.OldHash)
			if err != nil {
				return Text{}, err
			}
			next, err := prefetch(ctx, p.Owner, p.Repo, p.NewRef, p.NewHash)
			if err != nil {
				return Text{}, err
			}
			body, err := notesDiff(ctx, old, next, knownNotes[strings.ToLower(p.Owner+"/"+p.Repo)], o.Limits.Text)
			return Text{Source: fmt.Sprintf("the changelog files' diff between the source trees of github:%s/%s at %s and %s, "+
				"fetched by nix flake prefetch and checked against their NAR hashes", p.Owner, p.Repo, p.OldRef, p.NewRef),
				Body: body, Paths: []string{old, next}}, err
		})
		return t, err, true
	}
	return Text{}, nil, false
}

// storeDir holds every source tree prefetch gives; tests serve their own.
var storeDir = "/nix/store/"

// prefetch is the store path of github:owner/repo at ref, which `nix flake
// prefetch` fetches over HTTPS, refused unless its NAR hash is narHash:
// nix does not check a hash given with a source it already has.
func prefetch(ctx context.Context, owner, repo, ref, narHash string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nix", "flake", "prefetch", "--json", "--", "github:"+owner+"/"+repo+"/"+ref)
	cmd.Dir = os.TempDir()
	cmd.Env = gitenv.Without(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("nix flake prefetch github:%s/%s/%s: %v: %s", owner, repo, ref, err, oneLine(stderr.String()))
	}
	var got struct {
		Hash      string `json:"hash"`
		StorePath string `json:"storePath"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		return "", fmt.Errorf("nix flake prefetch github:%s/%s/%s: %v", owner, repo, ref, err)
	}
	if got.Hash != narHash || !strings.HasPrefix(got.StorePath, storeDir) {
		return "", fmt.Errorf("github:%s/%s/%s has the NAR hash %s, not the %s the pin names", owner, repo, ref, got.Hash, narHash)
	}
	return got.StorePath, nil
}

// moduleTrees is the changelog files' diff between p's two module
// versions, which `go mod download` fetches from the public proxy alone,
// into a module cache of its own that it removes after. A pin moved to
// another module holds each side as PATH@VERSION, of which the version is
// taken.
func moduleTrees(ctx context.Context, p Pin, o Options) (Text, error) {
	oldModule := cmp.Or(p.OldModule, p.Module)
	if strings.HasPrefix(p.Module, "-") || strings.HasPrefix(oldModule, "-") {
		return Text{}, fmt.Errorf("%q is no module path", cmp.Or(p.Module, oldModule))
	}
	p.Old, p.New = strings.TrimPrefix(p.Old, oldModule+"@"), strings.TrimPrefix(p.New, p.Module+"@")
	tmp, err := os.MkdirTemp("", "upgrade-go-")
	if err != nil {
		return Text{}, err
	}
	env := goproxy.DownloadEnv(filepath.Join(tmp, "mod"), tmp, "local")
	defer func() {
		// go makes its module cache read-only; this makes it removable.
		clean := exec.Command("go", "clean", "-modcache")
		clean.Dir, clean.Env = tmp, env
		clean.Run()
		os.RemoveAll(tmp)
	}()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "mod", "download", "-json", "--", oldModule+"@"+p.Old, p.Module+"@"+p.New)
	cmd.Dir, cmd.Env = tmp, env
	out, _ := cmd.Output()
	dirs := map[string]string{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var m struct{ Path, Version, Dir, Error string }
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			return Text{}, fmt.Errorf("go mod download: %v", err)
		}
		if m.Error != "" {
			return Text{}, fmt.Errorf("go mod download %s@%s: %s", m.Path, m.Version, oneLine(m.Error))
		}
		dirs[m.Path+"@"+m.Version] = m.Dir
	}
	before, after := dirs[oldModule+"@"+p.Old], dirs[p.Module+"@"+p.New]
	if before == "" || after == "" {
		return Text{}, fmt.Errorf("go mod download gave no directory for %s@%s and %s@%s", oldModule, p.Old, p.Module, p.New)
	}
	body, err := notesDiff(ctx, before, after, nil, o.Limits.Text)
	return Text{Source: fmt.Sprintf("the changelog files' diff between %s@%s and %s@%s, downloaded from the public module "+
		"proxy and checked against the checksum database", oldModule, p.Old, p.Module, p.New), Body: body}, err
}

// diffTimeout bounds each diff of a changelog path between two trees.
const diffTimeout = 2 * time.Minute

// inTree reports whether path, within the tree at root, is there with no
// symbolic link on the way, root's own excepted: a tree's link could
// point anywhere on this host.
func inTree(root, path string) bool {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(filepath.Join(root, path))
	return err == nil && real == filepath.Join(realRoot, path)
}

// notesDiff is the diff, between the trees old and next, of the changelog
// files at the top of either and of extra, paths within them, at most
// limit bytes of it. A path reached through a symbolic link counts as not
// there.
func notesDiff(ctx context.Context, old, next string, extra []string, limit int) (string, error) {
	paths := map[string]bool{}
	for _, dir := range []string{old, next} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if slices.ContainsFunc(notesNames, func(n string) bool { return strings.HasPrefix(strings.ToUpper(e.Name()), n) }) {
				paths[e.Name()] = true
			}
		}
	}
	for _, p := range extra {
		paths[filepath.Clean(p)] = true
	}
	empty, err := os.MkdirTemp("", "upgrade-empty-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(empty)
	var b strings.Builder
	for _, p := range slices.Sorted(maps.Keys(paths)) {
		if b.Len() > limit {
			break
		}
		if !filepath.IsLocal(p) {
			continue
		}
		a, z := filepath.Join(old, p), filepath.Join(next, p)
		var ia, iz fs.FileInfo
		errA, errZ := fs.ErrNotExist, fs.ErrNotExist
		if inTree(old, p) {
			ia, errA = os.Stat(a)
		}
		if inTree(next, p) {
			iz, errZ = os.Stat(z)
		}
		switch {
		case errA != nil && errZ != nil:
			continue
		case errA != nil && iz.IsDir():
			a = empty
		case errA != nil:
			a = os.DevNull
		case errZ != nil && ia.IsDir():
			z = empty
		case errZ != nil:
			z = os.DevNull
		}
		out, err := diffPaths(ctx, empty, a, z, limit-b.Len()+1)
		if err != nil {
			return "", fmt.Errorf("git diff --no-index %s: %v", p, err)
		}
		b.Write(out)
	}
	return b.String(), nil
}

// diffPaths is `git diff --no-index a z`, run in dir with no configuration,
// for at most diffTimeout, and at most limit bytes of it: what it would
// print beyond that is never read.
func diffPaths(ctx context.Context, dir, a, z string, limit int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "diff", "--no-index", "--no-color",
		"--no-ext-diff", "--no-textconv", "-U1", "--", a, z)
	cmd.Dir = dir
	cmd.Env = append(gitenv.Without(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out, readErr := io.ReadAll(io.LimitReader(pipe, int64(max(limit, 0))))
	full := len(out) >= limit
	if full {
		cancel()
	}
	err = cmd.Wait()
	var exit *exec.ExitError
	switch {
	case full:
		return out, nil
	case readErr != nil:
		return nil, readErr
	case ctx.Err() != nil:
		return nil, fmt.Errorf("stopped after %s", diffTimeout)
	case err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1):
		return nil, err
	}
	return out, nil
}

// source is one GitHub source's result.
type source struct {
	what string
	text Text
	err  error
}

// githubSources are p's releases between its two tags, or its two times,
// and GitHub's comparison of its two refs where no local clone gave its
// history, within o's calls for each pin.
func githubSources(ctx context.Context, p Pin, o Options, logged bool) []source {
	var out []source
	calls := 0
	slug := strings.ToLower(p.Owner + "/" + p.Repo)
	if !countsOnly[slug] && (p.OldTag != "" && p.NewTag != "" || !p.OldTime.IsZero() && !p.NewTime.IsZero()) && calls < o.Limits.Calls {
		calls++
		t, err := cached(o, "releases", []string{p.Owner, p.Repo, p.OldTag, p.NewTag, p.OldTime.String(), p.NewTime.String()},
			func() (Text, error) { return releases(p) })
		out = append(out, source{"GitHub releases", t, err})
	}
	if !logged && validRef(p.OldRef) && validRef(p.NewRef) && calls < o.Limits.Calls {
		t, err := cached(o, "compare", []string{p.Owner, p.Repo, p.OldRef, p.NewRef}, func() (Text, error) {
			return compare(p, countsOnly[slug])
		})
		out = append(out, source{"GitHub's comparison", t, err})
	}
	return out
}

// releases are p's repository's releases between its two versions: by
// tag, where its newest hundred hold both, else by when they were
// published, between the times its revisions were made.
func releases(p Pin) (Text, error) {
	var all []struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		Draft       bool      `json:"draft"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := gh.REST("GET", fmt.Sprintf("repos/%s/%s/releases?per_page=100", url.PathEscape(p.Owner), url.PathEscape(p.Repo)),
		nil, &all); err != nil {
		return Text{}, err
	}
	from, to := -1, -1
	for i, r := range all {
		switch r.TagName {
		case p.NewTag:
			to = i
		case p.OldTag:
			from = i
		}
	}
	var b strings.Builder
	for i, r := range all {
		var in bool
		switch {
		// Newest first: an upgrade takes the new release and those down
		// to the old one, a downgrade the old one and those it undoes.
		case p.OldTag != "" && from >= 0 && to >= 0:
			in = i >= min(from, to) && i < max(from, to)
		case !p.OldTime.IsZero() && !p.NewTime.IsZero():
			lo, hi := p.OldTime, p.NewTime
			if hi.Before(lo) {
				lo, hi = hi, lo
			}
			in = r.PublishedAt.After(lo) && !r.PublishedAt.After(hi)
		}
		if !in || r.Draft {
			continue
		}
		fmt.Fprintf(&b, "## %s %s (%s)\n\n%s\n\n", r.TagName, r.Name, r.PublishedAt.Format(time.DateOnly), strings.TrimSpace(r.Body))
	}
	return Text{Source: fmt.Sprintf("the GitHub releases of %s/%s between the two versions", p.Owner, p.Repo), Body: b.String()}, nil
}

// compare is GitHub's comparison of p's two refs: its counts, and for a
// repository not in countsOnly the first commits' subjects.
func compare(p Pin, counts bool) (Text, error) {
	var c struct {
		Status       string `json:"status"`
		AheadBy      int    `json:"ahead_by"`
		BehindBy     int    `json:"behind_by"`
		TotalCommits int    `json:"total_commits"`
		Commits      []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
		} `json:"commits"`
	}
	if err := gh.REST("GET", fmt.Sprintf("repos/%s/%s/compare/%s...%s?per_page=100", url.PathEscape(p.Owner),
		url.PathEscape(p.Repo), url.PathEscape(p.OldRef), url.PathEscape(p.NewRef)), nil, &c); err != nil {
		return Text{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d commits ahead, %d behind\n", c.Status, c.AheadBy, c.BehindBy)
	if !counts {
		for _, commit := range c.Commits {
			subject, _, _ := strings.Cut(commit.Commit.Message, "\n")
			fmt.Fprintf(&b, "%s %s\n", commit.SHA[:min(12, len(commit.SHA))], subject)
		}
		if len(c.Commits) < c.TotalCommits {
			fmt.Fprintf(&b, "(the first %d of %d)\n", len(c.Commits), c.TotalCommits)
		}
	}
	return Text{Source: fmt.Sprintf("GitHub's comparison of %s/%s %s...%s", p.Owner, p.Repo, p.OldRef, p.NewRef),
		Body: b.String()}, nil
}

// matchPrefixPatterns reports whether a module path matches one of the
// comma-separated glob patterns, as GOPRIVATE's are matched: a pattern
// matches a path whose leading elements match it. Case is ignored, as a
// host's and a GitHub owner's are, so that a module that differs from a
// private one only by case counts as private too.
func matchPrefixPatterns(patterns, module string) bool {
	module = strings.ToLower(module)
	for _, pattern := range strings.Split(strings.ToLower(patterns), ",") {
		pattern = strings.Trim(strings.TrimSpace(pattern), "/")
		if pattern == "" {
			continue
		}
		n := strings.Count(pattern, "/") + 1
		parts := strings.SplitN(module, "/", n+1)
		if len(parts) < n {
			continue
		}
		if ok, _ := path.Match(pattern, strings.Join(parts[:n], "/")); ok {
			return true
		}
	}
	return false
}
