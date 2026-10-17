// Package relay lets a sandbox without credentials run chosen programs on
// the host: the server holds the login and runs them, and the client inside
// the sandbox forwards its arguments, working directory and standard input.
// The relay confines the working directory to the workspace and its
// repository, and keeps the session that started it from relayed programs;
// each program decides which of
// its own commands a relayed caller may run (see RelayEnv).
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
)

// URLEnv names the relay a client inside a sandbox sends commands to.
const URLEnv = "SANDBOX_RELAY_URL"

// RelayEnv is set to 1 for every program the relay runs, so the program can
// refuse what a sandbox must not ask for. The sandbox cannot set it: the
// relay builds the program's environment on the host.
const RelayEnv = "SANDBOX_RELAYED"

// Request is one command sent by a client.
type Request struct {
	Program string   `json:"program"`
	Argv    []string `json:"argv"`
	Cwd     string   `json:"cwd"`
	Stdin   string   `json:"stdin"`
}

// Reply is the command's result.
type Reply struct {
	Code   int    `json:"code"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

type programs []string

func (p *programs) String() string     { return strings.Join(*p, ",") }
func (p *programs) Set(v string) error { *p = append(*p, v); return nil }

// resolve finds each program beside this executable first, so a relay
// installed with its programs runs those, and on PATH otherwise.
func resolve(names []string) (map[string]string, error) {
	if len(names) == 0 {
		names = []string{"pr-loop"}
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for _, name := range names {
		if strings.ContainsRune(name, filepath.Separator) {
			return nil, fmt.Errorf("--program takes a name, not a path: %s", name)
		}
		if beside := filepath.Join(filepath.Dir(self), name); isExecutable(beside) {
			paths[name] = beside
			continue
		}
		path, err := exec.LookPath(name)
		if err != nil {
			return nil, err
		}
		if paths[name], err = filepath.Abs(path); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// parseFailure is the exit code after flag.Parse has printed the flags: 0 when
// they were asked for with -h or --help.
func parseFailure(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

// Serve parses serve's flags and serves until killed.
func Serve(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "the only directory tree commands may run in")
	guestPath := flags.String("guest-path", "", "where the sandbox sees the workspace, if elsewhere")
	listen := flags.String("listen", "", "HOST:PORT; port 0 picks one")
	socket := flags.String("socket", "", "Unix socket path")
	var names programs
	flags.Var(&names, "program", "a program clients may run (repeatable; default pr-loop)")
	if err := flags.Parse(args); err != nil {
		return parseFailure(err)
	}
	if *workspace == "" || (*listen == "") == (*socket == "") {
		fmt.Fprintln(stderr, "sandbox-relay serve: needs --workspace and exactly one of --listen and --socket")
		return 2
	}
	network, address := "tcp", *listen
	if *socket != "" {
		network, address = "unix", *socket
	}
	paths, err := resolve(names)
	if err != nil {
		fmt.Fprintf(stderr, "sandbox-relay serve: %s\n", err)
		return 1
	}
	listener, root, err := start(*workspace, *guestPath, network, address, paths)
	if err != nil {
		fmt.Fprintf(stderr, "sandbox-relay serve: %s\n", err)
		return 1
	}
	where := "unix:" + address
	if network == "tcp" {
		where = "http://" + listener.Addr().String()
	}
	// The sandbox wrappers read the port from this line.
	fmt.Printf("sandbox-relay: serving %s on %s\n", root, where)
	os.Stdout.Sync()
	select {}
}

// start listens on address and serves the programs for workspace in the
// background.
func start(workspace, guestPath, network, address string, paths map[string]string) (net.Listener, string, error) {
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, "", err
	}
	s := space{root: root, guest: filepath.Clean(guestPath)}
	if guestPath == "" {
		s.guest = root
	}
	s.top, s.common, _ = repositoryAt(root)
	if network == "unix" {
		os.Remove(address)
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, "", err
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(handle(req, s, paths))
	})
	go http.Serve(listener, handler)
	return listener, root, nil
}

// Session runs a command beside a relay for the workspace containing the
// current directory: the relay listens on a Unix socket in a private
// directory, the command gets SANDBOX_RELAY_URL and SANDBOX_RELAY_SOCKET, and
// "{socket}", "{workspace}" and "{gitdir}" in its arguments become the real
// paths of the socket, the workspace and its repository's git common
// directory (the workspace where it is none), for a sandbox's settings. The
// relay stops when the command exits.
func Session(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("session", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "default: the repository containing the current directory")
	var names programs
	flags.Var(&names, "program", "a program clients may run (repeatable; default pr-loop)")
	if err := flags.Parse(args); err != nil {
		return parseFailure(err)
	}
	command := flags.Args()
	if len(command) == 0 {
		fmt.Fprintln(stderr, "sandbox-relay session: needs a command after --")
		return 2
	}
	if *workspace == "" {
		*workspace = repositoryRoot()
	}
	paths, err := resolve(names)
	if err != nil {
		fmt.Fprintf(stderr, "sandbox-relay session: %s\n", err)
		return 1
	}
	dir, err := os.MkdirTemp("", "sandbox-relay-")
	if err != nil {
		fmt.Fprintf(stderr, "sandbox-relay session: %s\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	// A sandbox compares the real path: macOS's /var/folders is a link to
	// /private/var/folders, and a socket allowed under the first is refused.
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	socket := filepath.Join(dir, "s")
	listener, root, err := start(*workspace, "", "unix", socket, paths)
	if err != nil {
		fmt.Fprintf(stderr, "sandbox-relay session: %s\n", err)
		return 1
	}
	defer listener.Close()

	placeholders := strings.NewReplacer("{socket}", socket, "{workspace}", root, "{gitdir}", gitDir(root))
	for i, a := range command {
		command[i] = placeholders.Replace(a)
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(childEnv(), URLEnv+"=unix:"+socket, "SANDBOX_RELAY_SOCKET="+socket)
	// The terminal's interrupt reaches the command directly; this process
	// only has to outlive it.
	signal.Ignore(os.Interrupt)
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintf(stderr, "sandbox-relay session: %s\n", err)
		return 1
	}
	return 0
}

// repositoryRoot is the main worktree of the repository containing the
// current directory, so worktrees under it are inside the workspace; outside
// a repository it is the current directory.
func repositoryRoot() string {
	cwd, _ := os.Getwd()
	out, err := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return cwd
	}
	common := strings.TrimSpace(string(out))
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return cwd
}

// gitDir is the real path of the git common directory of the repository at
// root, which lies outside a linked worktree; root itself where it is no
// repository.
func gitDir(root string) string {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Env = gitenv.Without(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return root
	}
	dir := strings.TrimSpace(string(out))
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

// space is where a relay's programs may run.
type space struct {
	// root is the workspace's real path, and guest where the sandbox sees
	// it.
	root, guest string
	// top and common are the real paths of the worktree containing root
	// and of its repository's git common directory, or "" where root is in
	// no worktree.
	top, common string
}

// repositoryAt is the real paths of the top of the worktree containing dir
// and of its repository's git common directory, as git finds them from dir
// alone, and false where dir is in no worktree. A sandbox can write a .git
// file or directory anywhere in its workspace, so this runs no program the
// repository's config names.
func repositoryAt(dir string) (top, common string, ok bool) {
	top, _, common, ok = gitDirsAt(dir)
	return top, common, ok
}

// gitDirsAt is repositoryAt with the worktree's own git directory, which
// for a linked worktree lies in the common one.
func gitDirsAt(dir string) (top, gitDir, common string, ok bool) {
	cmd := exec.Command("git", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-C", dir,
		"rev-parse", "--path-format=absolute", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	cmd.Env = childEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", "", "", false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		return "", "", "", false
	}
	return realPath(lines[0]), realPath(lines[1]), realPath(lines[2]), true
}

func realPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// within reports whether path is root or below it.
func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// check is why a program may not run in cwd, a real path, or "", and else
// the environment that pins its git commands to the repository checked.
// Every git command a relayed program runs there would otherwise find its
// repository from cwd, so a .git file the sandbox wrote, naming another
// repository's git directory, or a repository it made, whose remotes it
// chose, would have the host act on that repository with the host's
// credentials. cwd must therefore be in the workspace's own repository, and
// in a worktree inside the workspace; in a workspace that is no repository,
// in none or in one held inside it. GIT_DIR, GIT_WORK_TREE and
// GIT_COMMON_DIR name the directories checked, so a .git swapped once the
// check is done moves no git command that finds its repository from the
// environment. A command given `-C DIR`, which internal/git runs without
// them, finds it from DIR again.
func (s space) check(cwd string) (pin []string, reason string) {
	if !within(cwd, s.root) {
		return nil, fmt.Sprintf("%s is outside %s; run the command from inside the workspace", cwd, s.root)
	}
	top, gitDir, common, ok := gitDirsAt(cwd)
	elsewhere := "; run the command from a worktree of the workspace's repository, and remove the .git file or " +
		"directory between it and the workspace that sends git elsewhere"
	switch {
	case !ok && s.common == "":
		return nil, ""
	case !ok:
		return nil, fmt.Sprintf("git finds no worktree at %s, so not %s's repository%s", cwd, s.root, elsewhere)
	case s.common != "" && common != s.common:
		return nil, fmt.Sprintf("%s is in the repository at %s, not the workspace's at %s%s", cwd, common, s.common, elsewhere)
	case s.common == "" && !within(common, s.root):
		return nil, fmt.Sprintf("%s is in the repository at %s, outside the workspace %s%s", cwd, common, s.root, elsewhere)
	case !within(top, s.root) && top != s.top:
		return nil, fmt.Sprintf("%s is in the worktree at %s, outside the workspace %s%s", cwd, top, s.root, elsewhere)
	}
	return []string{"GIT_DIR=" + gitDir, "GIT_WORK_TREE=" + top, "GIT_COMMON_DIR=" + common}, ""
}

// checked runs between a call's check and its program's start, where a
// sandbox could swap a .git file; tests set it to do that.
var checked = func() {}

// hostPath maps the sandbox's working directory onto the host's workspace.
func hostPath(cwd, root, guest string) string {
	cwd = filepath.Clean(cwd)
	if cwd == guest || strings.HasPrefix(cwd, guest+string(filepath.Separator)) {
		cwd = root + strings.TrimPrefix(cwd, guest)
	}
	return realPath(cwd)
}

func handle(req Request, s space, paths map[string]string) Reply {
	path, ok := paths[req.Program]
	if !ok {
		return Reply{Code: 2, Stderr: fmt.Sprintf("sandbox-relay: refused: %q is not served; hand the operator the command to run on the host\n", req.Program)}
	}
	cwd := hostPath(req.Cwd, s.root, s.guest)
	pin, reason := s.check(cwd)
	if reason != "" {
		return Reply{Code: 2, Stderr: "sandbox-relay: refused: " + reason + "\n"}
	}
	checked()
	cmd := exec.CommandContext(context.Background(), path, req.Argv...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(req.Stdin)
	cmd.Env = append(relayedEnv(), RelayEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(cmd.Env, pin...)
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			code, stderr = 1, *bytes.NewBufferString(err.Error() + "\n")
		}
	}
	return Reply{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

// childEnv is the environment of a program the relay starts, in a directory
// of its own: without the repository this process's environment names.
func childEnv() []string {
	return withoutURL(gitenv.Without(os.Environ()))
}

// relayedEnv is childEnv without the harness session that started the
// relay, which is not the sandbox's: a relayed program that finds a plan
// by session would otherwise find that session's.
func relayedEnv() []string {
	var out []string
	for _, kv := range childEnv() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(plans.SessionEnv, name) {
			out = append(out, kv)
		}
	}
	return out
}

// withoutURL keeps a relayed program from relaying again, and drops a
// caller's RelayEnv so only the relay sets it.
func withoutURL(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, URLEnv+"=") && !strings.HasPrefix(kv, RelayEnv+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// Call sends program's argv, the working directory and stdin to the relay at
// url (http://HOST[:PORT] or unix:PATH) and returns the command's result.
func Call(url, program string, argv []string, stdin []byte) (Reply, error) {
	cwd, _ := os.Getwd()
	body, _ := json.Marshal(Request{Program: program, Argv: argv, Cwd: cwd, Stdin: string(stdin)})

	client := &http.Client{}
	target := strings.TrimSuffix(url, "/") + "/"
	if path, ok := strings.CutPrefix(url, "unix:"); ok {
		client.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}
		target = "http://sandbox-relay/"
	}
	resp, err := client.Post(target, "application/json", bytes.NewReader(body))
	if err != nil {
		return Reply{}, fmt.Errorf("cannot reach the relay at %s: %w", url, err)
	}
	defer resp.Body.Close()
	var r Reply
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Reply{}, fmt.Errorf("bad reply from %s: %w", url, err)
	}
	return r, nil
}

// Client runs program through the relay at url as Call does, relays the
// output, and returns the exit code.
func Client(url, program string, argv []string, stdin []byte) int {
	r, err := Call(url, program, argv, stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", program, err)
		return 1
	}
	os.Stdout.WriteString(r.Stdout)
	os.Stderr.WriteString(r.Stderr)
	return r.Code
}
