package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/limavm"
)

const (
	// startTimeout bounds a start job: creating the instance downloads
	// its image, and a first boot provisions it.
	startTimeout = 30 * time.Minute
	// maxExecSeconds bounds a guest command, which runs under the guest's
	// timeout; the host gives it a minute more to report.
	maxExecSeconds     = 7200
	defaultExecSeconds = 600
	// maxWaitSeconds keeps lima_dev_job_result under the proxy's and
	// Codex's call deadlines.
	maxWaitSeconds = 240
	// watchInterval is how often a running VM is attested again.
	watchInterval = 60 * time.Second
	guestShim     = "/tmp/lima-udp-shim"
	// startAttestTries is how often a start tries to attest the VM it
	// started while attestation cannot read what it needs, attestRetry
	// apart, before it stops the VM.
	startAttestTries = 3
	attestRetry      = 2 * time.Second
)

// managed is the one managed VM: its configuration, and the jobs that
// start it and run commands in it.
type managed struct {
	cfg        *limavm.Config
	configFile string
	// processes lists the user's processes, and processArgs reads one's
	// executable and argv; limavm's outside tests.
	processes   func() ([]limavm.Proc, error)
	processArgs func(pid int) (string, []string, error)
	// attestRetry is how long a start waits between tries to attest: the
	// constant attestRetry outside tests.
	attestRetry time.Duration
	jobs        *jobs
	close       context.CancelFunc
	// lifecycle, a semaphore of one, keeps one start or stop at a time;
	// starting is the start job that holds it, which a stop cancels.
	lifecycle chan struct{}
	mu        sync.Mutex
	starting  *job
}

func newManaged(cfg *limavm.Config, configFile string, processes func() ([]limavm.Proc, error), processArgs func(int) (string, []string, error)) *managed {
	base, cancel := context.WithCancel(context.Background())
	return &managed{cfg: cfg, configFile: configFile, processes: processes, processArgs: processArgs,
		attestRetry: attestRetry, jobs: newJobs(base), close: cancel, lifecycle: make(chan struct{}, 1)}
}

// lock takes the lifecycle, or fails when ctx ends first.
func (m *managed) lock(ctx context.Context) error {
	select {
	case m.lifecycle <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("waiting for another start or stop: %w", ctx.Err())
	}
}

func (m *managed) unlock() { <-m.lifecycle }

// limactlEnv is the whole environment the pinned limactl runs with: HOME
// and TMPDIR of lima-mcp's, a system PATH, the managed home and the guard
// as QEMU.
func limactlEnv(cfg *limavm.Config) []string {
	return []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"TMPDIR=" + os.TempDir(),
		"LIMA_HOME=" + cfg.LimaHome,
		"QEMU_SYSTEM_AARCH64=" + cfg.Guard,
	}
}

// limactl runs the pinned limactl against the managed home, with
// limactlEnv. Its output goes to a file, not a pipe, since limactl start
// leaves the host agent running, and is returned as its last maxOutput
// bytes, with whether more was cut.
func (m *managed) limactl(ctx context.Context, args ...string) (string, bool, error) {
	out, err := os.CreateTemp("", "lima-mcp-*")
	if err != nil {
		return "", false, err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	cmd := exec.CommandContext(ctx, m.cfg.Limactl, args...) //nolint:gosec // the pinned limactl the store configuration names; each argument is a constant or a value the tool checked
	cmd.Env = limactlEnv(m.cfg)
	cmd.Stdout, cmd.Stderr = out, out
	// A cancelled command takes its children with it, such as the ssh
	// client of limactl shell.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	output, truncated, err := tail(out)
	if err != nil {
		return "", false, err
	}
	return output, truncated, runErr
}

// tail returns f's last maxOutput bytes, and whether there were more.
func tail(f *os.File) (string, bool, error) {
	info, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	offset := max(info.Size()-maxOutput, 0)
	b := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(b, offset); err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	return string(b), offset > 0, nil
}

func (m *managed) list(ctx context.Context) (limavm.Listed, error) {
	var l limavm.Listed
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, _, err := m.limactl(ctx, "list", "--json", m.cfg.Instance)
	if err != nil {
		return l, fmt.Errorf("limactl list: %v: %s", err, out)
	}
	// limactl writes its warnings to the same file; the instance is the
	// line that is a JSON object.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "{") {
			return l, json.Unmarshal([]byte(line), &l)
		}
	}
	return l, fmt.Errorf("limactl list printed no instance: %s", out)
}

// attest decides the VM's state with limavm.Inspector, and stops a running
// VM it decides is mismatch or running-unattested: a VM it cannot attest
// does not keep running. An error decides nothing: the VM is left as it is,
// and the caller fails, or, for the watcher, tries again.
func (m *managed) attest(ctx context.Context) (limavm.Attestation, error) {
	a, err := limavm.Inspector{Config: m.cfg, List: m.list, Processes: m.processes, ProcessArgs: m.processArgs}.Inspect(ctx)
	if err != nil {
		return a, fmt.Errorf("attestation could not read what it needs, and left the VM as it is: %w", err)
	}
	if a.Listed.Running() && (a.State == limavm.StateMismatch || a.State == limavm.StateUnattested) {
		if out, _, serr := m.limactl(ctx, "stop", "-f", m.cfg.Instance); serr != nil {
			a.Reason = fmt.Sprintf("%s; stopping it failed: %v: %s", a.Reason, serr, out)
		}
	}
	return a, nil
}

// refusal is why the VM may not start, from limavm.CheckOverrides or
// CheckTemplate, as an error, or their error.
func refusal(reason string, err error) error {
	if err != nil {
		return err
	}
	if reason != "" {
		return errors.New(reason)
	}
	return nil
}

// watch attests a running VM every interval until ctx ends, or n times
// when n is positive, so that one that stops attesting is stopped. An
// attestation that fails to read is tried again at the next tick.
func (m *managed) watch(ctx context.Context, interval time.Duration, n int) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for i := 0; n <= 0 || i < n; i++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.attest(ctx)
		}
	}
}

// refreshLinks points the stable links at this lima-mcp and its
// configuration, which the relays read through them.
func (m *managed) refreshLinks() error {
	return limavm.RefreshLinks(m.cfg, m.configFile)
}

// start creates the instance from the template if it is absent, starts
// it with the guard, attests it and installs the guest's UDP shim.
func (m *managed) start(ctx context.Context, _ string) (string, bool, *int, error) {
	if err := m.lock(ctx); err != nil {
		return "", false, nil, err
	}
	defer m.unlock()
	if err := refusal(limavm.CheckOverrides(&m.cfg.GuardConfig)); err != nil {
		return "", false, nil, err
	}
	if err := os.MkdirAll(m.cfg.LimaHome, 0o700); err != nil {
		return "", false, nil, err
	}
	if err := m.refreshLinks(); err != nil {
		return "", false, nil, err
	}
	// limactl create reads the CA the template's caCerts names.
	if err := limavm.EnsureCA(ctx, m.cfg.CACert(), m.cfg.CAKey(), m.cfg.QUICServers); err != nil {
		return "", false, nil, err
	}
	var log strings.Builder
	step := func(args ...string) error {
		out, _, err := m.limactl(ctx, args...)
		log.WriteString(out)
		if err != nil {
			return fmt.Errorf("limactl %s: %w", args[0], err)
		}
		return nil
	}
	if _, err := os.Stat(m.cfg.InstanceDir()); errors.Is(err, fs.ErrNotExist) {
		if err := step("create", "--tty=false", "--name", m.cfg.Instance, m.cfg.Template); err != nil {
			return log.String(), false, nil, err
		}
	} else if err := refusal(limavm.CheckTemplate(m.cfg)); err != nil {
		return "", false, nil, err
	}
	if err := step("start", "--tty=false", m.cfg.Instance); err != nil {
		return log.String(), false, nil, err
	}
	a, err := m.attest(ctx)
	for try := 1; err != nil && try < startAttestTries && ctx.Err() == nil; try++ {
		select {
		case <-ctx.Done():
		case <-time.After(m.attestRetry):
			a, err = m.attest(ctx)
		}
	}
	if err != nil {
		// This start may have left the VM running, unattested.
		m.limactl(ctx, "stop", "-f", m.cfg.Instance)
		return log.String(), false, nil, fmt.Errorf("the started VM was stopped: %w", err)
	}
	if a.State != limavm.StateAttested {
		return log.String(), false, nil, fmt.Errorf("the started VM is %s: %s", a.State, a.Reason)
	}
	if err := step("copy", m.cfg.Shim, m.cfg.Instance+":"+guestShim); err != nil {
		return log.String(), false, nil, err
	}
	install := "install -m 0755 " + guestShim + " /usr/local/bin/lima-udp-shim && systemctl restart lima-udp-shim"
	if err := step("shell", m.cfg.Instance, "--", "sudo", "sh", "-c", install); err != nil {
		return log.String(), false, nil, err
	}
	return log.String(), false, nil, nil
}

// guestJob is the guest command for job id: a session of its own, whose
// ID it records in the guest's /tmp/lima-mcp-jobs/ID, so that cancelling
// the job can kill all of it, and the script under the guest's timeout.
// (timeout puts itself in a process group of its own, so the session, not
// the group, is what holds the whole command.)
func guestJob(id string, seconds int, script string) []string {
	return []string{"setsid", "-w", "sh", "-c",
		`d=/tmp/lima-mcp-jobs; mkdir -p "$d"; echo $$ > "$d/` + id + `"; ` +
			`timeout -k 10 ` + strconv.Itoa(seconds) + ` bash -lc "$1"; rc=$?; rm -f "$d/` + id + `"; exit $rc`,
		"lima-mcp-job", script}
}

// guestKill is the guest command that kills job id's session.
func guestKill(id string) []string {
	return []string{"sh", "-c", `f=/tmp/lima-mcp-jobs/` + id + `; [ -s "$f" ] && pkill -KILL -s "$(cat "$f")"; rm -f "$f"`}
}

// exec runs script in the guest's workdir under the guest's timeout, once
// the VM attests. When ctx is cancelled it kills the guest's command too,
// since killing limactl ends only the host's side of it.
func (m *managed) exec(ctx context.Context, id, script string, seconds int) (string, bool, *int, error) {
	a, err := m.attest(ctx)
	if err != nil {
		return "", false, nil, err
	}
	if a.State != limavm.StateAttested {
		return "", false, nil, fmt.Errorf("the managed VM is %s: %s", a.State, a.Reason)
	}
	if err := m.refreshLinks(); err != nil {
		return "", false, nil, err
	}
	home := a.Listed.Config.User.Home
	if home == "" {
		return "", false, nil, errors.New("Lima reports no guest home")
	}
	workdir := home + "/" + m.cfg.Workdir
	out, truncated, err := m.limactl(ctx, append([]string{"shell", "--workdir", workdir, m.cfg.Instance, "--"},
		guestJob(id, seconds, script)...)...)
	if ctx.Err() != nil {
		kill, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if kout, _, kerr := m.limactl(kill, append([]string{"shell", m.cfg.Instance, "--"}, guestKill(id)...)...); kerr != nil {
			return out, truncated, nil, fmt.Errorf("%w; killing the guest's command failed: %v: %s", ctx.Err(), kerr, kout)
		}
		return out, truncated, nil, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		if code >= 0 {
			return out, truncated, &code, fmt.Errorf("exit status %d", code)
		}
	}
	if err != nil {
		return out, truncated, nil, err
	}
	zero := 0
	return out, truncated, &zero, nil
}

// The managed VM's tools.

func (s *server) managedVM() (*managed, map[string]any) {
	if s.managed == nil {
		return nil, failure("no managed VM is configured: lima-mcp runs without --config")
	}
	return s.managed, nil
}

func jsonText(v any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		return failure(err.Error())
	}
	return text(string(data))
}

func callStatus(ctx context.Context, s *server, raw json.RawMessage) map[string]any {
	m, fail := s.managedVM()
	if fail != nil {
		return fail
	}
	var input struct{}
	if err := strict(raw, &input); err != nil {
		return failure("invalid arguments")
	}
	a, err := m.attest(ctx)
	if err != nil {
		return failure(err.Error())
	}
	r := map[string]any{"state": a.State}
	if a.Reason != "" {
		r["detail"] = a.Reason
	}
	return jsonText(r)
}

func callStart(ctx context.Context, s *server, raw json.RawMessage) map[string]any {
	m, fail := s.managedVM()
	if fail != nil {
		return fail
	}
	var input struct{}
	if err := strict(raw, &input); err != nil {
		return failure("invalid arguments")
	}
	// One start at a time, so that m.starting is the start a stop must
	// cancel.
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.starting != nil {
		select {
		case <-m.starting.done:
		default:
			return failure("a start is already running: job " + m.starting.id)
		}
	}
	j := m.jobs.start(startTimeout, m.start)
	m.starting = j
	return jsonText(map[string]any{"job": j.id})
}

func callDevStop(ctx context.Context, s *server, raw json.RawMessage) map[string]any {
	m, fail := s.managedVM()
	if fail != nil {
		return fail
	}
	var input struct{}
	if err := strict(raw, &input); err != nil {
		return failure("invalid arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	// A stop wins over a start still running: it cancels the start, then
	// waits for it to let go, within the call's own deadline.
	m.mu.Lock()
	if m.starting != nil {
		m.starting.cancel()
	}
	m.mu.Unlock()
	if err := m.lock(ctx); err != nil {
		return failure(err.Error())
	}
	defer m.unlock()
	out, _, err := m.limactl(ctx, "stop", m.cfg.Instance)
	if err != nil {
		return failure(fmt.Sprintf("limactl stop: %v\n%s", err, out))
	}
	return text(out)
}

func callExecStart(ctx context.Context, s *server, raw json.RawMessage) map[string]any {
	m, fail := s.managedVM()
	if fail != nil {
		return fail
	}
	var input struct {
		Script         string `json:"script"`
		TimeoutSeconds *int   `json:"timeout_seconds"`
	}
	if err := strict(raw, &input); err != nil || input.Script == "" || len(input.Script) > 64<<10 {
		return failure("invalid arguments: script is required, at most 64 KiB")
	}
	seconds := defaultExecSeconds
	if input.TimeoutSeconds != nil {
		seconds = *input.TimeoutSeconds
	}
	if seconds < 1 || seconds > maxExecSeconds {
		return failure(fmt.Sprintf("invalid arguments: timeout_seconds is 1 to %d", maxExecSeconds))
	}
	j := m.jobs.start(time.Duration(seconds)*time.Second+time.Minute, func(ctx context.Context, id string) (string, bool, *int, error) {
		return m.exec(ctx, id, input.Script, seconds)
	})
	return jsonText(map[string]any{"job": j.id})
}

func callJobResult(ctx context.Context, s *server, raw json.RawMessage) map[string]any {
	m, fail := s.managedVM()
	if fail != nil {
		return fail
	}
	var input struct {
		Job         string `json:"job"`
		WaitSeconds int    `json:"wait_seconds"`
	}
	if err := strict(raw, &input); err != nil || input.WaitSeconds < 0 || input.WaitSeconds > maxWaitSeconds {
		return failure(fmt.Sprintf("invalid arguments: wait_seconds is 0 to %d", maxWaitSeconds))
	}
	j := m.jobs.get(input.Job)
	if j == nil {
		return failure("no such job")
	}
	return jsonText(j.result(ctx, time.Duration(input.WaitSeconds)*time.Second))
}

func callJobCancel(ctx context.Context, s *server, raw json.RawMessage) map[string]any {
	m, fail := s.managedVM()
	if fail != nil {
		return fail
	}
	var input struct {
		Job string `json:"job"`
	}
	if err := strict(raw, &input); err != nil {
		return failure("invalid arguments")
	}
	j := m.jobs.get(input.Job)
	if j == nil {
		return failure("no such job")
	}
	j.cancel()
	return jsonText(map[string]any{"job": j.id, "cancelled": true})
}
