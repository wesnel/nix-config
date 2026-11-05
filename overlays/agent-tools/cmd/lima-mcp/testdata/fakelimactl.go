// Command fakelimactl stands in for Lima's limactl in lima-mcp's tests of
// the managed VM, which cannot boot a VM. It keeps an instance as a
// directory under LIMA_HOME, as Lima does, and appends each call, with
// the QEMU_SYSTEM_AARCH64 it saw, to LIMA_HOME/calls.log. With
// LIMA_HOME/fake-guest.json, {"home": DIR, "path": PATH}, a directory on
// this host stands in for the guest user's home: copy writes there, and
// shell runs a job's script there, with that PATH and no git
// configuration.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type guest struct {
	Home string `json:"home"`
	Path string `json:"path"`
}

// run runs script with bash in the stand-in guest, in dir, and exits as
// it does. bash is looked up in the guest's PATH, not this process's,
// which lima-mcp fixes and which has no bash in the Linux build sandbox.
func (g guest) run(dir, script string) {
	cmd := exec.Command(g.bash(), "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + g.Home, "PATH=" + g.Path, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// bash is the first executable bash in the guest's PATH, or "bash" for
// exec to report as missing.
func (g guest) bash() string {
	for _, dir := range filepath.SplitList(g.Path) {
		p := filepath.Join(dir, "bash")
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return "bash"
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func main() {
	home := os.Getenv("LIMA_HOME")
	args := os.Args[1:]
	log, err := os.OpenFile(filepath.Join(home, "calls.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Fprintf(log, "%s qemu=%s proxy=%s\n", strings.Join(args, " "), os.Getenv("QEMU_SYSTEM_AARCH64"), os.Getenv("HTTPS_PROXY"))
	log.Close()
	fail := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", a...)
		os.Exit(1)
	}
	var g guest
	if data, err := os.ReadFile(filepath.Join(home, "fake-guest.json")); err == nil {
		if err := json.Unmarshal(data, &g); err != nil {
			fail("fake-guest.json: %v", err)
		}
	}
	status := func(name string) string {
		if _, err := os.Stat(filepath.Join(home, name, "qemu.pid")); err == nil {
			return "Running"
		}
		return "Stopped"
	}
	switch args[0] {
	case "create":
		// create --tty=false --name NAME TEMPLATE
		name, template := args[3], args[4]
		// Lima reads the files caCerts names when it creates the instance.
		if _, err := os.Stat(filepath.Join(home, "_mcp", "ca.pem")); err != nil {
			fail("open ca.pem: %v", err)
		}
		data, err := os.ReadFile(template)
		if err != nil {
			fail("%v", err)
		}
		if err := os.MkdirAll(filepath.Join(home, name), 0o700); err != nil {
			fail("%v", err)
		}
		os.WriteFile(filepath.Join(home, name, "lima.yaml"), data, 0o644)
		// The image Lima downloads becomes the disk: a qcow2 image.
		disk := make([]byte, 104)
		copy(disk, "QFI\xfb\x00\x00\x00\x03")
		os.WriteFile(filepath.Join(home, name, "disk"), disk, 0o644)
	case "start":
		if _, err := os.Stat(filepath.Join(home, "hold-start")); err == nil {
			time.Sleep(time.Hour)
		}
		name := args[len(args)-1]
		// The fake's QEMU "runs" while qemu.pid is there.
		os.WriteFile(filepath.Join(home, name, "qemu.pid"), []byte("4242\n"), 0o644)
		fmt.Println("READY")
	case "stop":
		os.Remove(filepath.Join(home, args[len(args)-1], "qemu.pid"))
	case "list":
		name := args[len(args)-1]
		if _, err := os.Stat(filepath.Join(home, name)); err != nil {
			fail("instance %q does not exist", name)
		}
		if _, err := os.Stat(filepath.Join(home, "fail-list")); err == nil {
			fail("the host agent does not answer")
		}
		guestHome := "/home/u.guest"
		if g.Home != "" {
			guestHome = g.Home
		}
		config := map[string]any{"user": map[string]any{"home": guestHome}}
		if _, err := os.Stat(filepath.Join(home, "fake-mounts")); err == nil {
			config["mounts"] = []any{map[string]any{"location": "~"}}
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"name": name, "status": status(name), "config": config})
	case "copy":
		// copy SRC NAME:DST; only a copy into the stand-in guest's home
		// happens.
		_, dst, _ := strings.Cut(args[len(args)-1], ":")
		if g.Home != "" && strings.HasPrefix(dst, g.Home+"/") {
			if err := copyFile(args[len(args)-2], dst); err != nil {
				fail("%v", err)
			}
		}
	case "shell":
		// shell [--workdir W] NAME -- COMMAND...
		i := 1
		workdir := g.Home
		for i < len(args) && args[i] != "--" {
			if args[i] == "--workdir" {
				workdir = args[i+1]
			}
			i++
		}
		command := args[i+1:]
		// A job's guest command, setsid -w sh -c WRAPPER lima-mcp-job
		// SCRIPT, runs its script in the stand-in guest, without the
		// session and timeout around it.
		if g.Home != "" && command[0] == "setsid" {
			g.run(workdir, command[len(command)-1])
		}
		for _, a := range command {
			fmt.Println(a)
		}
		script := command[len(command)-1]
		switch script {
		case "hold":
			time.Sleep(time.Hour)
		case "exit 3":
			fmt.Fprintln(os.Stderr, "three")
			os.Exit(3)
		case "large":
			fmt.Print(strings.Repeat("x", 3<<20))
		}
	default:
		fail("unknown command %q", args[0])
	}
}
