package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/limavm"
)

// guardMain runs `lima-mcp qemu-guard --config FILE -- ARGS`, which Lima
// starts in place of QEMU for the managed home: it executes what
// limavm.Guard decides for ARGS, through execute, or refuses before
// executing anything. FILE is the guard's own configuration, which must
// resolve to a file under store (/nix/store/), so that nothing which can
// write the Lima home, QEMU included, chooses what the guard executes.
func guardMain(args []string, execute func(path string, argv, env []string) error, store string) error {
	if len(args) < 3 || args[0] != "--config" || args[2] != "--" {
		return errors.New("usage: lima-mcp qemu-guard --config FILE -- QEMU-ARGS")
	}
	file, err := filepath.EvalSymlinks(args[1])
	if err != nil {
		return err
	}
	if store, err = filepath.EvalSymlinks(store); err != nil {
		return err
	}
	if !strings.HasPrefix(file, strings.TrimSuffix(store, "/")+"/") {
		return fmt.Errorf("the guard's configuration %s is not in %s", file, store)
	}
	c, err := limavm.LoadGuardConfig(file)
	if err != nil {
		return err
	}
	launch, err := limavm.Guard(c, args[3:])
	if err != nil {
		return err
	}
	return execute(launch.Path, launch.Argv, os.Environ())
}
