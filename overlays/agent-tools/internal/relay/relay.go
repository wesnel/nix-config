// Package relay lets a sandbox without credentials run chosen programs on
// the host: the server holds the login and runs them, and the client inside
// the sandbox forwards its arguments, working directory and standard input.
// The relay confines the working directory; each program decides which of
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
	"strings"
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
	guest := filepath.Clean(guestPath)
	if guestPath == "" {
		guest = root
	}
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
		json.NewEncoder(w).Encode(handle(req, root, guest, paths))
	})
	go http.Serve(listener, handler)
	return listener, root, nil
}

// Session runs a command beside a relay for the workspace containing the
// current directory: the relay listens on a Unix socket in a private
// directory, the command gets SANDBOX_RELAY_URL and SANDBOX_RELAY_SOCKET, and
// "{socket}" and "{workspace}" in its arguments become the socket's and the
// workspace's real paths, for a sandbox's settings. The relay stops when the
// command exits.
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

	for i, a := range command {
		command[i] = strings.NewReplacer("{socket}", socket, "{workspace}", root).Replace(a)
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(withoutURL(os.Environ()), URLEnv+"=unix:"+socket, "SANDBOX_RELAY_SOCKET="+socket)
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

// hostPath maps the sandbox's working directory onto the host's workspace.
func hostPath(cwd, root, guest string) string {
	cwd = filepath.Clean(cwd)
	if cwd == guest || strings.HasPrefix(cwd, guest+string(filepath.Separator)) {
		cwd = root + strings.TrimPrefix(cwd, guest)
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return cwd
}

func handle(req Request, root, guest string, paths map[string]string) Reply {
	path, ok := paths[req.Program]
	if !ok {
		return Reply{Code: 2, Stderr: fmt.Sprintf("sandbox-relay: refused: %q is not served\n", req.Program)}
	}
	cwd := hostPath(req.Cwd, root, guest)
	if cwd != root && !strings.HasPrefix(cwd, root+string(filepath.Separator)) {
		return Reply{Code: 2, Stderr: fmt.Sprintf("sandbox-relay: refused: %s is outside %s\n", cwd, root)}
	}
	cmd := exec.CommandContext(context.Background(), path, req.Argv...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(req.Stdin)
	cmd.Env = append(withoutURL(os.Environ()), RelayEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
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

// Client sends program's argv, the working directory and stdin to the relay
// at url (http://HOST[:PORT] or unix:PATH), relays the output, and returns the
// exit code.
func Client(url, program string, argv []string, stdin []byte) int {
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
		fmt.Fprintf(os.Stderr, "%s: cannot reach the relay at %s: %s\n", program, url, err)
		return 1
	}
	defer resp.Body.Close()
	var r Reply
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		fmt.Fprintf(os.Stderr, "%s: bad reply from %s: %s\n", program, url, err)
		return 1
	}
	os.Stdout.WriteString(r.Stdout)
	os.Stderr.WriteString(r.Stderr)
	return r.Code
}
