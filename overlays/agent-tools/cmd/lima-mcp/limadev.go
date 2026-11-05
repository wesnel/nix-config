package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/limavm"
)

// devMain runs `lima-mcp lima-dev --config FILE -- ARGS`, the operator's
// lima-dev: limactl ARGS against the managed home, with the guard as QEMU,
// after refreshing the stable links to FILE and making the broker CA, as
// lima-mcp does before a start, so that a VM the operator starts gets the
// same relays and policy. Every command but those of recovers, which
// cannot start a VM, is refused, as lima-mcp's start is, while one of
// Lima's overrides exists or the instance's lima.yaml is not the store
// template. It executes limactl through execute.
func devMain(args []string, execute func(path string, argv, env []string) error) error {
	if len(args) < 3 || args[0] != "--config" || args[2] != "--" {
		return errors.New("usage: lima-mcp lima-dev --config FILE -- LIMACTL-ARGS")
	}
	c, err := limavm.LoadConfig(args[1])
	if err != nil {
		return err
	}
	if !recovers(args[3:]) {
		if err := refusal(limavm.CheckOverrides(&c.GuardConfig)); err != nil {
			return err
		}
		if _, err := os.Stat(c.InstanceDir()); err == nil { //nolint:gosec // in the Lima home of the store configuration
			if err := refusal(limavm.CheckTemplate(c)); err != nil {
				return err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := limavm.RefreshLinks(c, args[1]); err != nil {
		return err
	}
	if err := limavm.EnsureCA(context.Background(), c.CACert(), c.CAKey(), c.QUICServers); err != nil {
		return err
	}
	return execute(c.Limactl, append([]string{c.Limactl}, args[3:]...), limactlEnv(c))
}

// recovers reports whether limactl args, read without knowing limactl's
// flags, is one of the commands the operator needs to inspect or remove a
// refused VM, none of which starts one: list, stop, delete, info, help, or
// --version alone. Its command is the first argument not a flag, so a
// flag's value before it, as in --log-level debug start, is taken for an
// unknown command and checked.
func recovers(args []string) bool {
	if slices.Equal(args, []string{"--version"}) {
		return true
	}
	i := slices.IndexFunc(args, func(a string) bool { return !strings.HasPrefix(a, "-") })
	return i >= 0 && slices.Contains([]string{"list", "stop", "delete", "info", "help"}, args[i])
}
