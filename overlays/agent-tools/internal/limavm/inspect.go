package limavm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
)

// State is the managed VM's state as attestation decides it.
type State string

const (
	StateAbsent     State = "absent"
	StateStopped    State = "stopped"
	StateUnattested State = "running-unattested"
	StateAttested   State = "attested"
	StateMismatch   State = "mismatch"
)

// Listed is what limactl list --json says of the instance.
type Listed struct {
	Status string `json:"status"`
	Config struct {
		Mounts       []json.RawMessage `json:"mounts"`
		PortForwards []json.RawMessage `json:"portForwards"`
		User         struct {
			Home string `json:"home"`
		} `json:"user"`
	} `json:"config"`
}

// Running reports whether Lima says the VM runs.
func (l Listed) Running() bool { return l.Status == "Running" }

// Attestation is attestation's decision: the State, why when it is
// mismatch or running-unattested, and what Lima listed.
type Attestation struct {
	State  State
	Reason string
	Listed Listed
}

// Inspector attests the managed VM. Its functions are what it reads:
// limactl list, the process table and a process's arguments.
type Inspector struct {
	Config      *Config
	List        func(context.Context) (Listed, error)
	Processes   func() ([]Proc, error)
	ProcessArgs func(pid int) (string, []string, error)
}

// Inspect decides the VM's state. Its error is a failure to read what the
// decision needs (limactl list, the process table, a QEMU's arguments, an
// override's or lima.yaml's metadata), which decides nothing: a caller
// fails on it, and neither stops the VM nor advises deleting it.
func (in Inspector) Inspect(ctx context.Context) (Attestation, error) {
	c := in.Config
	if _, err := os.Stat(c.InstanceDir()); errors.Is(err, fs.ErrNotExist) { //nolint:gosec // in the Lima home of the store configuration
		return Attestation{State: StateAbsent}, nil
	} else if err != nil {
		return Attestation{}, err
	}
	l, err := in.List(ctx)
	if err != nil {
		return Attestation{}, err
	}
	a := Attestation{Listed: l}
	mismatch := func(reason string) (Attestation, error) {
		a.State, a.Reason = StateMismatch, reason
		return a, nil
	}
	if reason, err := CheckOverrides(&c.GuardConfig); err != nil || reason != "" {
		if err != nil {
			return Attestation{}, err
		}
		return mismatch(reason)
	}
	if reason, err := CheckTemplate(c); err != nil || reason != "" {
		if err != nil {
			return Attestation{}, err
		}
		return mismatch(reason)
	}
	if len(l.Config.Mounts) != 0 || len(l.Config.PortForwards) != 0 {
		return mismatch("Lima reports mounts or port forwards for the managed VM")
	}
	if !l.Running() {
		a.State = StateStopped
		return a, nil
	}
	procs, err := in.Processes()
	if err != nil {
		return Attestation{}, err
	}
	found, err := FindQEMU(&c.GuardConfig, procs, in.ProcessArgs)
	if err != nil {
		return Attestation{}, err
	}
	switch {
	case len(found) == 0:
		a.State, a.Reason = StateUnattested, "Lima reports the VM running, and no store QEMU of its instance runs"
		return a, nil
	case len(found) > 1:
		return mismatch(fmt.Sprintf("%d store QEMUs name the instance directory", len(found)))
	}
	if err := CheckLive(&c.GuardConfig, found[0].Exe, found[0].Argv); err != nil {
		return mismatch(err.Error())
	}
	a.State = StateAttested
	return a, nil
}

// CheckOverrides returns why the VM may not run when any of Lima's
// per-home defaults and overrides exists, which would change the instance
// whatever its template says, and an error when it cannot tell.
func CheckOverrides(g *GuardConfig) (string, error) {
	for _, name := range []string{"default.yaml", "override.yaml", "base.yaml"} {
		p := path.Join(g.LimaHome, "_config", name)
		if _, err := os.Lstat(p); err == nil { //nolint:gosec // in the Lima home of the store configuration
			return fmt.Sprintf("%s exists, which changes the managed VM: remove it", p), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return "", nil
}

// CheckTemplate returns why the VM may not run when its lima.yaml is
// missing or is not the store template, which limactl create copies there
// byte for byte, and an error when it cannot read either.
func CheckTemplate(c *Config) (string, error) {
	want, err := os.ReadFile(c.Template) //nolint:gosec // the store template the store configuration names
	if err != nil {
		return "", err
	}
	have, err := os.ReadFile(path.Join(c.InstanceDir(), "lima.yaml")) //nolint:gosec // in the Lima home of the store configuration
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err != nil || !bytes.Equal(have, want) {
		return fmt.Sprintf("instance %s was not made from the current template %s: the operator deletes it with `lima-dev delete %s`, and the next start makes it again", c.Instance, c.Template, c.Instance), nil
	}
	return "", nil
}
