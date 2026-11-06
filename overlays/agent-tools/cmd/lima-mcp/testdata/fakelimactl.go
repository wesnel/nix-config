// Command fakelimactl stands in for Lima's limactl in lima-mcp's tests of
// the managed VM, which cannot boot a VM. It keeps an instance as a
// directory under LIMA_HOME, as Lima does, and appends each call, with
// the QEMU_SYSTEM_AARCH64 it saw, to LIMA_HOME/calls.log.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
		config := map[string]any{"user": map[string]any{"home": "/home/u.guest"}}
		if _, err := os.Stat(filepath.Join(home, "fake-mounts")); err == nil {
			config["mounts"] = []any{map[string]any{"location": "~"}}
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"name": name, "status": status(name), "config": config})
	case "copy":
	case "shell":
		// shell [--workdir W] NAME -- COMMAND...
		i := 1
		for i < len(args) && args[i] != "--" {
			i++
		}
		command := args[i+1:]
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
