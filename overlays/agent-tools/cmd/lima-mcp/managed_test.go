package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/limavm"
)

// fakes is the directory, made and removed by TestMain, of the fake
// limactl that every test shares and none writes. It is built once per
// run: macOS assesses a freshly built binary at its first exec, and with
// one build per VM, dozens queued, first execs stalled for tens of seconds
// and the tests' deadlines passed before the fake's main ran.
var fakes string

var buildFakeLimactl = sync.OnceValues(func() (string, error) {
	out := filepath.Join(fakes, "limactl")
	cmd := exec.Command("go", "build", "-o", out, filepath.Join("testdata", "fakelimactl.go"))
	cmd.Env = append(os.Environ(), "GO111MODULE=off", "GOWORK=off", "GOPROXY=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build fake limactl: %v\n%s", err, output)
	}
	return out, nil
})

// fakeLimactl is testdata/fakelimactl.go, built: the test cannot boot a
// VM.
func fakeLimactl(t *testing.T) string {
	t.Helper()
	out, err := buildFakeLimactl()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// vm is a managed VM on the fake limactl, in a Lima home of its own.
type vm struct {
	cfg        *limavm.Config
	configFile string
	s          *server
	// live says what the process table and kern.procargs2 show for QEMU:
	// the guard's launch ("guard"), Lima's argv unchanged, as if QEMU had
	// been started around the guard ("lima"), no QEMU ("none"), two
	// ("two"), a table that cannot be read ("unreadable"), or one that
	// cannot be read once and is then the guard's ("unreadable-once").
	live string
}

func newVM(t *testing.T) *vm {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "lima")
	template := filepath.Join(dir, "ngrok-agent.yaml")
	if err := os.WriteFile(template, []byte("vmType: qemu\nplain: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	outbox := filepath.Join(dir, "outbox")
	if err := os.Mkdir(outbox, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"home": "/Users/x", "limaHome": home, "instance": "ngrok-agent", "workdir": "src/ngrok-agent",
		"limactl": fakeLimactl(t), "qemu": testQEMU,
		"guard": "/nix/store/g/bin/qemu-system-aarch64", "limaMcp": "/nix/store/m/bin/lima-mcp",
		"template": template, "shim": "/nix/store/s/bin/lima-udp-shim", "deniedReads": []string{},
		"git": git, "outbox": outbox,
		"connect": "192.168.5.100:3128", "quic": "192.168.5.101:443",
		"egress":               map[string]any{"allow": []string{}, "deny": []string{}, "quicServers": []string{"us.quic.connect-endpoint.ngrok.com"}},
		"privateDialEndpoints": []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := limavm.ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	v := &vm{cfg: cfg, configFile: configFile, live: "guard"}
	// Fakes of the process table and kern.procargs2: the test's QEMU is no
	// process. 4242 is the VM's QEMU, as v.live says, while the fake
	// limactl has it running.
	processes := func() ([]limavm.Proc, error) {
		if v.live == "unreadable-once" {
			v.live = "guard"
			return nil, syscall.EPERM
		}
		if v.live == "unreadable" {
			return nil, syscall.EPERM
		}
		procs := []limavm.Proc{{PID: 1, Comm: "launchd"}}
		if _, err := os.Stat(filepath.Join(cfg.InstanceDir(), "qemu.pid")); err == nil && v.live != "none" {
			procs = append(procs, limavm.Proc{PID: 4242, Comm: "qemu-system-aarc"})
			if v.live == "two" {
				procs = append(procs, limavm.Proc{PID: 4243, Comm: "qemu-system-aarc"})
			}
		}
		return procs, nil
	}
	processArgs := func(pid int) (string, []string, error) {
		if pid != 4242 && pid != 4243 {
			return "", nil, fmt.Errorf("read the arguments of %d, which is no QEMU", pid)
		}
		boot := []string{"-m", "2048", "-netdev", "user,id=net0,net=192.168.5.0/24,dhcpstart=192.168.5.15,hostfwd=tcp:127.0.0.1:2222-:22",
			"-pidfile", filepath.Join(cfg.InstanceDir(), "qemu.pid")}
		if v.live == "lima" {
			return cfg.QEMU, append([]string{cfg.QEMU}, boot...), nil
		}
		launch, err := limavm.Guard(&cfg.GuardConfig, boot)
		if err != nil {
			return "", nil, err
		}
		return cfg.QEMU, launch.Argv[3:], nil
	}
	v.s = &server{limactl: "unused", timeout: time.Minute, managed: newManaged(cfg, configFile, processes, processArgs)}
	// A start's tries to attest need not wait seconds apart here.
	v.s.managed.attestRetry = time.Millisecond
	t.Cleanup(v.s.managed.close)
	return v
}

// calls is every limactl call so far, one per line.
func (v *vm) calls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(v.cfg.LimaHome, "calls.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// tool calls a tool and returns its text and whether it failed.
func (v *vm) tool(t *testing.T, name, arguments string) (string, bool) {
	t.Helper()
	result := v.s.call(t.Context(), json.RawMessage(`{"name":"`+name+`","arguments":`+arguments+`}`))
	content := result["content"].([]any)
	return content[0].(map[string]any)["text"].(string), result["isError"] == true
}

type jobResult struct {
	Job       string `json:"job"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error"`
}

// run starts a job with the tool and waits for its result.
func (v *vm) run(t *testing.T, name, arguments string) jobResult {
	t.Helper()
	text, failed := v.tool(t, name, arguments)
	if failed {
		t.Fatalf("%s: %s", name, text)
	}
	var started struct {
		Job string `json:"job"`
	}
	if err := json.Unmarshal([]byte(text), &started); err != nil || started.Job == "" {
		t.Fatalf("%s returned %q, not a job", name, text)
	}
	return v.result(t, started.Job, 30)
}

func (v *vm) result(t *testing.T, job string, wait int) jobResult {
	t.Helper()
	text, failed := v.tool(t, "lima_dev_job_result", fmt.Sprintf(`{"job":%q,"wait_seconds":%d}`, job, wait))
	if failed {
		t.Fatalf("job result: %s", text)
	}
	var r jobResult
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatalf("job result %q: %v", text, err)
	}
	return r
}

func (v *vm) status(t *testing.T) string {
	t.Helper()
	text, failed := v.tool(t, "lima_dev_status", `{}`)
	if failed {
		t.Fatalf("status: %s", text)
	}
	var s struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(text), &s); err != nil {
		t.Fatalf("status %q: %v", text, err)
	}
	return s.State
}

func TestManagedStart(t *testing.T) {
	t.Run("creates starts attests and installs the shim", func(t *testing.T) {
		v := newVM(t)
		if state := v.status(t); state != "absent" {
			t.Fatalf("state before start %q", state)
		}
		r := v.run(t, "lima_dev_start", `{}`)
		if r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		calls := v.calls(t)
		for _, want := range []string{
			"create --tty=false --name ngrok-agent " + v.cfg.Template + " qemu=" + v.cfg.Guard + " proxy=\n",
			"start --tty=false ngrok-agent qemu=" + v.cfg.Guard,
			"copy /nix/store/s/bin/lima-udp-shim ngrok-agent:/tmp/lima-udp-shim",
			"systemctl restart lima-udp-shim",
		} {
			if !strings.Contains(calls, want) {
				t.Errorf("limactl calls lack %q:\n%s", want, calls)
			}
		}
		if strings.Index(calls, "copy ") < strings.Index(calls, "start ") {
			t.Errorf("the shim was copied before the start:\n%s", calls)
		}
		for link, want := range map[string]string{v.cfg.ConfigLink(): v.configFile, v.cfg.BinLink(): v.cfg.LimaMCP} {
			if got, err := os.Readlink(link); err != nil || got != want {
				t.Errorf("link %s -> %q, %v; want %s", link, got, err, want)
			}
		}
		if _, err := limavm.LoadCA(v.cfg.CACert(), v.cfg.CAKey()); err != nil {
			t.Error(err)
		}
		if state := v.status(t); state != "attested" {
			t.Fatalf("state after start %q", state)
		}
	})

	t.Run("refuses while a Lima override is configured", func(t *testing.T) {
		v := newVM(t)
		override := filepath.Join(v.cfg.LimaHome, "_config", "override.yaml")
		if err := os.MkdirAll(filepath.Dir(override), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(override, []byte("mounts: [{location: '~'}]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := v.run(t, "lima_dev_start", `{}`)
		if r.Status != "failed" || !strings.Contains(r.Error, "override.yaml") {
			t.Fatalf("start: %+v", r)
		}
		if calls := v.calls(t); strings.Contains(calls, "create") || strings.Contains(calls, "start") {
			t.Fatalf("limactl ran:\n%s", calls)
		}
	})

	t.Run("refuses an instance not made from the template", func(t *testing.T) {
		v := newVM(t)
		if err := os.MkdirAll(v.cfg.InstanceDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(v.cfg.InstanceDir(), "lima.yaml"), []byte("vmType: vz\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := v.run(t, "lima_dev_start", `{}`)
		if r.Status != "failed" || !strings.Contains(r.Error, "lima-dev delete ngrok-agent") {
			t.Fatalf("start: %+v", r)
		}
		if calls := v.calls(t); strings.Contains(calls, "start --tty") {
			t.Fatalf("limactl started it:\n%s", calls)
		}
	})

	t.Run("stops a QEMU the guard did not launch", func(t *testing.T) {
		v := newVM(t)
		v.live = "lima"
		r := v.run(t, "lima_dev_start", `{}`)
		if r.Status != "failed" || !strings.Contains(r.Error, "restricted") {
			t.Fatalf("start: %+v", r)
		}
		if calls := v.calls(t); !strings.Contains(calls, "stop -f ngrok-agent") || strings.Contains(calls, "copy ") {
			t.Fatalf("limactl calls:\n%s", calls)
		}
	})

	t.Run("stops a started VM whose QEMU it cannot find", func(t *testing.T) {
		v := newVM(t)
		v.live = "none"
		r := v.run(t, "lima_dev_start", `{}`)
		if r.Status != "failed" || !strings.Contains(r.Error, "running-unattested") {
			t.Fatalf("start: %+v", r)
		}
		if calls := v.calls(t); !strings.Contains(calls, "stop -f ngrok-agent") || strings.Contains(calls, "copy ") {
			t.Fatalf("limactl calls:\n%s", calls)
		}
	})

	t.Run("stops a VM it started and then could not list", func(t *testing.T) {
		v := newVM(t)
		if err := os.MkdirAll(v.cfg.LimaHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(v.cfg.LimaHome, "fail-list"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		r := v.run(t, "lima_dev_start", `{}`)
		if r.Status != "failed" || !strings.Contains(r.Error, "does not answer") {
			t.Fatalf("start: %+v", r)
		}
		if calls := v.calls(t); !strings.Contains(calls, "stop -f ngrok-agent") || strings.Contains(calls, "copy ") {
			t.Fatalf("limactl calls:\n%s", calls)
		}
	})

	t.Run("attests again after one read error and keeps the VM", func(t *testing.T) {
		v := newVM(t)
		v.live = "unreadable-once"
		r := v.run(t, "lima_dev_start", `{}`)
		if r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		if calls := v.calls(t); strings.Contains(calls, "stop -f ngrok-agent") || !strings.Contains(calls, "copy ") {
			t.Fatalf("limactl calls:\n%s", calls)
		}
	})

	t.Run("a stop cancels a start that hangs", func(t *testing.T) {
		v := newVM(t)
		if err := os.MkdirAll(v.cfg.LimaHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(v.cfg.LimaHome, "hold-start"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		text, failed := v.tool(t, "lima_dev_start", `{}`)
		if failed {
			t.Fatal(text)
		}
		var started struct{ Job string }
		json.Unmarshal([]byte(text), &started)
		if r := v.result(t, started.Job, 2); r.Status != "running" {
			t.Fatalf("start %+v; want it still running", r)
		}
		if text, failed := v.tool(t, "lima_dev_stop", `{}`); failed {
			t.Fatalf("stop: %s", text)
		}
		if r := v.result(t, started.Job, 10); r.Status != "cancelled" {
			t.Fatalf("start %+v; want it cancelled", r)
		}
	})

	t.Run("a second start is refused while one runs and a stop still cancels the first", func(t *testing.T) {
		v := newVM(t)
		if err := os.MkdirAll(v.cfg.LimaHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(v.cfg.LimaHome, "hold-start"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		text, failed := v.tool(t, "lima_dev_start", `{}`)
		if failed {
			t.Fatal(text)
		}
		var first struct{ Job string }
		json.Unmarshal([]byte(text), &first)
		if r := v.result(t, first.Job, 2); r.Status != "running" {
			t.Fatalf("start %+v; want it still running", r)
		}
		if text, failed := v.tool(t, "lima_dev_start", `{}`); !failed {
			t.Fatalf("a second start began while the first runs: %s", text)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		if r := v.s.call(ctx, json.RawMessage(`{"name":"lima_dev_stop","arguments":{}}`)); r["isError"] == true {
			t.Fatalf("stop: %v", r)
		}
		if r := v.result(t, first.Job, 10); r.Status != "cancelled" {
			t.Fatalf("first start %+v; want it cancelled", r)
		}
	})

	t.Run("a stop gives up at its deadline", func(t *testing.T) {
		// The reviewer's case: the lifecycle held by something else.
		v := newVM(t)
		if err := v.s.managed.lock(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer v.s.managed.unlock()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		done := make(chan map[string]any, 1)
		go func() { done <- v.s.call(ctx, json.RawMessage(`{"name":"lima_dev_stop","arguments":{}}`)) }()
		select {
		case r := <-done:
			if r["isError"] != true {
				t.Fatalf("stop succeeded holding no lock: %v", r)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("lima_dev_stop still blocked 4 s after its context ended")
		}
	})
}

func TestManagedStatus(t *testing.T) {
	v := newVM(t)
	if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
		t.Fatalf("start: %+v", r)
	}
	t.Run("mismatch stops the VM", func(t *testing.T) {
		v.live = "lima"
		if state := v.status(t); state != "mismatch" {
			t.Fatalf("state %q", state)
		}
		if !strings.Contains(v.calls(t), "stop -f ngrok-agent") {
			t.Fatal("the VM was not stopped")
		}
		if state := v.status(t); state != "stopped" {
			t.Fatalf("state after the stop %q", state)
		}
	})
	t.Run("a mount reported by Lima is a mismatch", func(t *testing.T) {
		v.live = "guard"
		if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		mounts := filepath.Join(v.cfg.LimaHome, "fake-mounts")
		if err := os.WriteFile(mounts, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(mounts)
		before := strings.Count(v.calls(t), "stop -f")
		if state := v.status(t); state != "mismatch" {
			t.Fatalf("state %q", state)
		}
		if strings.Count(v.calls(t), "stop -f") != before+1 {
			t.Fatalf("the running VM was not stopped:\n%s", v.calls(t))
		}
	})
	t.Run("a Lima override is a mismatch", func(t *testing.T) {
		v.live = "guard"
		if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		override := filepath.Join(v.cfg.LimaHome, "_config", "override.yaml")
		if err := os.MkdirAll(filepath.Dir(override), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(override, []byte("mounts: [{location: '~'}]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(override)
		if state := v.status(t); state != "mismatch" {
			t.Fatalf("state %q", state)
		}
	})
	t.Run("two QEMUs of the instance are a mismatch", func(t *testing.T) {
		v.live = "guard"
		if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		v.live = "two"
		defer func() { v.live = "guard" }()
		if state := v.status(t); state != "mismatch" {
			t.Fatalf("state %q", state)
		}
	})
	// A read that fails decides nothing: the status call fails, and the VM
	// keeps running, with no advice to delete it.
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"the process table cannot be read", func(t *testing.T) {
			v.live = "unreadable"
			t.Cleanup(func() { v.live = "guard" })
		}},
		{"the overrides cannot be looked for", func(t *testing.T) {
			dir := filepath.Join(v.cfg.LimaHome, "_config")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			os.Chmod(dir, 0)
			t.Cleanup(func() { os.Chmod(dir, 0o700) })
		}},
		{"lima.yaml cannot be read", func(t *testing.T) {
			f := filepath.Join(v.cfg.InstanceDir(), "lima.yaml")
			os.Chmod(f, 0)
			t.Cleanup(func() { os.Chmod(f, 0o644) })
		}},
	} {
		t.Run("fails and leaves the VM running when "+tc.name, func(t *testing.T) {
			v.live = "guard"
			if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
				t.Fatalf("start: %+v", r)
			}
			tc.setup(t)
			before := strings.Count(v.calls(t), "stop -f")
			text, failed := v.tool(t, "lima_dev_status", `{}`)
			if !failed || strings.Contains(text, "lima-dev delete") {
				t.Fatalf("status %q, failed %v; want a failure without advice to delete", text, failed)
			}
			v.s.managed.watch(t.Context(), 10*time.Millisecond, 1)
			if strings.Count(v.calls(t), "stop -f") != before {
				t.Fatalf("the VM was stopped:\n%s", v.calls(t))
			}
		})
	}
	t.Run("concurrent link refreshes all succeed", func(t *testing.T) {
		// The reviewer's case.
		var wg sync.WaitGroup
		errs := make(chan error, 4000)
		for range 8 {
			wg.Go(func() {
				for range 500 {
					if err := v.s.managed.refreshLinks(); err != nil {
						errs <- err
					}
				}
			})
		}
		wg.Wait()
		close(errs)
		if err, ok := <-errs; ok {
			t.Fatalf("%d of 4000 concurrent refreshes failed; first: %v", len(errs)+1, err)
		}
	})
	t.Run("the watcher stops a running VM whose QEMU it cannot find", func(t *testing.T) {
		v.live = "guard"
		if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		v.live = "none"
		defer func() { v.live = "guard" }()
		before := strings.Count(v.calls(t), "stop -f")
		v.s.managed.watch(t.Context(), 10*time.Millisecond, 1)
		if strings.Count(v.calls(t), "stop -f") != before+1 {
			t.Fatalf("the watcher left it running:\n%s", v.calls(t))
		}
	})
	t.Run("the watcher stops a VM that stops attesting", func(t *testing.T) {
		v.live = "guard"
		if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
			t.Fatalf("start: %+v", r)
		}
		v.live = "lima"
		before := strings.Count(v.calls(t), "stop -f")
		v.s.managed.watch(t.Context(), 10*time.Millisecond, 1)
		if strings.Count(v.calls(t), "stop -f") != before+1 {
			t.Fatalf("the watcher did not stop it:\n%s", v.calls(t))
		}
	})
}

func TestManagedExec(t *testing.T) {
	v := newVM(t)
	if r := v.run(t, "lima_dev_start", `{}`); r.Status != "succeeded" {
		t.Fatalf("start: %+v", r)
	}

	t.Run("runs the script in the workdir in a session of its own under a guest timeout", func(t *testing.T) {
		r := v.run(t, "lima_dev_exec_start", `{"script":"make test","timeout_seconds":90}`)
		// The fake prints the guest command's arguments, one per line.
		want := "setsid\n-w\nsh\n-c\n" +
			`d=/tmp/lima-mcp-jobs; mkdir -p "$d"; echo $$ > "$d/` + r.Job + `"; timeout -k 10 90 bash -lc "$1"; rc=$?; rm -f "$d/` + r.Job + `"; exit $rc` +
			"\nlima-mcp-job\nmake test\n"
		if r.Status != "succeeded" || *r.ExitCode != 0 || r.Output != want {
			t.Fatalf("exec: %+v; want output %q", r, want)
		}
		if calls := v.calls(t); !strings.Contains(calls, "shell --workdir /home/u.guest/src/ngrok-agent ngrok-agent -- setsid") {
			t.Fatalf("limactl calls:\n%s", calls)
		}
	})

	t.Run("reports the exit code and stderr", func(t *testing.T) {
		r := v.run(t, "lima_dev_exec_start", `{"script":"exit 3"}`)
		if r.Status != "failed" || r.ExitCode == nil || *r.ExitCode != 3 || !strings.Contains(r.Output, "three") ||
			!strings.Contains(r.Output, "timeout -k 10 600 bash") {
			t.Fatalf("exec: %+v", r)
		}
	})

	t.Run("keeps the last MiB of a larger output", func(t *testing.T) {
		r := v.run(t, "lima_dev_exec_start", `{"script":"large"}`)
		if r.Status != "succeeded" || !r.Truncated || len(r.Output) != 1<<20 || strings.Trim(r.Output, "x") != "" {
			t.Fatalf("exec: status %s truncated %v output %d bytes", r.Status, r.Truncated, len(r.Output))
		}
	})

	t.Run("cancel kills a running job", func(t *testing.T) {
		text, failed := v.tool(t, "lima_dev_exec_start", `{"script":"hold"}`)
		if failed {
			t.Fatal(text)
		}
		var started struct{ Job string }
		json.Unmarshal([]byte(text), &started)
		if r := v.result(t, started.Job, 1); r.Status != "running" {
			t.Fatalf("job %+v; want it running", r)
		}
		if text, failed := v.tool(t, "lima_dev_job_cancel", `{"job":"`+started.Job+`"}`); failed {
			t.Fatal(text)
		}
		if r := v.result(t, started.Job, 10); r.Status != "cancelled" {
			t.Fatalf("job %+v; want it cancelled", r)
		}
		// Killing limactl ends only the host's side: the guest's session
		// is killed by its own command.
		want := `shell ngrok-agent -- sh -c f=/tmp/lima-mcp-jobs/` + started.Job + `; [ -s "$f" ] && pkill -KILL -s "$(cat "$f")"; rm -f "$f"`
		if calls := v.calls(t); !strings.Contains(calls, want) {
			t.Fatalf("limactl calls lack %q:\n%s", want, calls)
		}
	})

	t.Run("refuses to run in a VM that does not attest", func(t *testing.T) {
		v.live = "lima"
		defer func() { v.live = "guard" }()
		before := strings.Count(v.calls(t), "shell --workdir")
		r := v.run(t, "lima_dev_exec_start", `{"script":"make test"}`)
		if r.Status != "failed" || strings.Count(v.calls(t), "shell --workdir") != before {
			t.Fatalf("exec: %+v\n%s", r, v.calls(t))
		}
	})

	for _, tc := range []struct{ name, tool, arguments string }{
		{"a timeout over two hours", "lima_dev_exec_start", `{"script":"x","timeout_seconds":7201}`},
		{"no script", "lima_dev_exec_start", `{"script":""}`},
		{"an unknown argument", "lima_dev_exec_start", `{"script":"x","sudo":true}`},
		{"a wait over 240 seconds", "lima_dev_job_result", `{"job":"x","wait_seconds":241}`},
		{"an unknown job", "lima_dev_job_result", `{"job":"nope"}`},
		{"cancelling an unknown job", "lima_dev_job_cancel", `{"job":"nope"}`},
		{"arguments to start", "lima_dev_start", `{"recreate":true}`},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			if text, failed := v.tool(t, tc.tool, tc.arguments); !failed {
				t.Fatalf("accepted: %s", text)
			}
		})
	}
}

func TestManagedNotConfigured(t *testing.T) {
	for _, name := range []string{"lima_dev_status", "lima_dev_start", "lima_dev_stop", "lima_dev_exec_start"} {
		result := unused.call(t.Context(), json.RawMessage(`{"name":"`+name+`","arguments":{}}`))
		if result["isError"] != true {
			t.Errorf("%s succeeded with no managed VM configured: %v", name, result)
		}
	}
}
