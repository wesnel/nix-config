// Package sandboxrun runs a shell script in a VM with no network, through
// the sandbox backend of emacs-config's ECA sandbox: eca-gondolin on macOS,
// eca-bwrap on Linux. The guest sees a workspace and other directories,
// all read-only, and its standard output, capped, is the only result: the
// host reads no file the guest wrote.
package sandboxrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// GuestWorkspace is where the guest sees Spec.Workspace.
const GuestWorkspace = "/src"

// Spec is one run.
type Spec struct {
	// Backend is the eca-gondolin or eca-bwrap executable.
	Backend string
	// Workspace is mounted read-only at GuestWorkspace, and Mounts, guest
	// path to host directory, read-only where they say.
	Workspace string
	Mounts    map[string]string
	// Script runs with sh in the guest, in GuestWorkspace.
	Script string
	// Memory, such as 4G, and CPUs size the VM; how each backend enforces
	// them is stated in eca-gondolin and eca-bwrap.
	Memory string
	CPUs   int
	// Timeout stops the run; Grace is how long the backend then has to shut
	// the VM down before it and everything it started are killed.
	Timeout, Grace time.Duration
	// Cap is how many bytes of the guest's standard output are kept.
	Cap int
}

// Result is what came back from the guest.
type Result struct {
	Exit   int    `json:"exit"`
	Output string `json:"output"`
	// Cut is whether Output stops at Spec.Cap.
	Cut      bool `json:"cut"`
	TimedOut bool `json:"timedOut"`
	// Log is the end of the backend's standard error, which carries the
	// guest's: as untrusted as Output.
	Log string `json:"log"`
}

// logCap is how much of the backend's standard error Result keeps.
const logCap = 4 << 10

// capped keeps the first n bytes written to it and drops the rest, so a
// guest that floods its output neither fills memory nor blocks.
type capped struct {
	buf bytes.Buffer
	n   int
	cut bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.n - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(room, len(p))])
	}
	if c.buf.Len()+len(p) > c.n {
		c.cut = true
	}
	return len(p), nil
}

// tail keeps the last n bytes written to it.
type tail struct {
	buf []byte
	n   int
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.n {
		t.buf = t.buf[len(t.buf)-t.n:]
	}
	return len(p), nil
}

// args are the backend's arguments: offline, so the guest has no network
// device and the workspace is read-only; no state or ECA config of the
// host's mounted; every other mount read-only.
func args(s Spec) ([]string, error) {
	a := []string{"--offline", "--state", "", "--config", "", "--workspace", s.Workspace, "--guest-path", GuestWorkspace}
	if s.Memory != "" {
		a = append(a, "--memory", s.Memory)
	}
	if s.CPUs > 0 {
		a = append(a, "--cpus", strconv.Itoa(s.CPUs))
	}
	guests := make([]string, 0, len(s.Mounts))
	for guest := range s.Mounts {
		guests = append(guests, guest)
	}
	sort.Strings(guests)
	for _, guest := range guests {
		host := s.Mounts[guest]
		if !path.IsAbs(guest) || path.Clean(guest) == "/" || strings.Contains(guest, ":") {
			return nil, fmt.Errorf("guest path %q is not an absolute path below /", guest)
		}
		if info, err := os.Stat(host); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("%s is not a directory to mount", host)
		}
		a = append(a, "--ro-mount", host+":"+guest)
	}
	return append(a, "--", "sh", "-c", s.Script), nil
}

// environment is this process's without the ECA_SANDBOX_* settings, which
// would otherwise give the backend hosts, state or a relay.
func environment() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ECA_SANDBOX_") {
			env = append(env, kv)
		}
	}
	return env
}

// Run runs s.
func Run(ctx context.Context, s Spec) (*Result, error) {
	if s.Backend == "" {
		return nil, errors.New("no sandbox backend is configured")
	}
	if info, err := os.Stat(s.Workspace); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("the workspace %s is not a directory", s.Workspace)
	}
	argv, err := args(s)
	if err != nil {
		return nil, err
	}
	if s.Grace == 0 {
		s.Grace = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Backend, argv...)
	cmd.Env = environment()
	cmd.Stdin = nil
	out := &capped{n: s.Cap}
	log := &tail{n: logCap}
	cmd.Stdout, cmd.Stderr = out, log
	// The backend and the VM it starts are one process group, which a
	// timeout asks to stop and then kills.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = s.Grace
	err = cmd.Start()
	if err != nil {
		return nil, err
	}
	err = cmd.Wait()
	// Whatever of the group outlived the backend.
	stopGroup(cmd.Process.Pid, 5*time.Second)
	r := &Result{Output: out.buf.String(), Cut: out.cut, Log: string(log.buf)}
	switch {
	case ctx.Err() != nil:
		// A run the caller stopped did not time out.
		r.Exit, r.TimedOut = -1, errors.Is(ctx.Err(), context.DeadlineExceeded)
	case err != nil:
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return nil, err
		}
		r.Exit = exit.ExitCode()
	}
	return r, nil
}

// stopGroup kills the process group pgid until it is empty, or for at most
// bound. One kill is not enough: a process that forks as the kill lands can
// leave a child in the group that the kill missed.
func stopGroup(pgid int, bound time.Duration) {
	deadline := time.Now().Add(bound)
	for syscall.Kill(-pgid, syscall.SIGKILL) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}
