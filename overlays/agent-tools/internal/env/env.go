// Package env carries the environment variables and working directory a
// piece of code runs under, in a context.Context.
//
// Code reads them from From(ctx) and starts programs with Environ and Dir, so
// a test gives the code under test an Env of its own with With and changes
// nothing process-wide. A context with no Env gives Process, which is what
// production code runs under.
package env

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Env is a set of environment variables and a working directory. The zero
// value is empty. Methods return a changed copy and never modify the receiver.
type Env struct {
	vars []string
	dir  string
}

type key struct{}

// Process is a snapshot of this process's environment and working directory.
func Process() Env {
	dir, _ := os.Getwd()
	return Env{vars: os.Environ(), dir: dir}
}

// New is an Env with the working directory dir and the variables vars, each
// "NAME=value".
func New(dir string, vars ...string) Env {
	return Env{vars: slices.Clone(vars), dir: dir}
}

// With returns a context whose From is e.
func With(ctx context.Context, e Env) context.Context {
	return context.WithValue(ctx, key{}, e)
}

// From is the Env of ctx, or Process() where no caller set one.
func From(ctx context.Context) Env {
	if e, ok := ctx.Value(key{}).(Env); ok {
		return e
	}
	return Process()
}

// Dir is the working directory.
func (e Env) Dir() string { return e.dir }

// Environ is the variables as "NAME=value", in the form os.Environ and
// exec.Cmd's Env use. The caller may modify the result.
func (e Env) Environ() []string { return slices.Clone(e.vars) }

// Lookup is the value of name and whether it is set. Of a name set twice, the
// last wins, as for exec.Cmd.
func (e Env) Lookup(name string) (string, bool) {
	for _, kv := range slices.Backward(e.vars) {
		if n, v, _ := strings.Cut(kv, "="); n == name {
			return v, true
		}
	}
	return "", false
}

// Get is the value of name, or "" where it is unset.
func (e Env) Get(name string) string {
	v, _ := e.Lookup(name)
	return v
}

// Set is e with each "NAME=value" of vars replacing any variable of that name.
func (e Env) Set(vars ...string) Env {
	names := make([]string, len(vars))
	for i, kv := range vars {
		names[i], _, _ = strings.Cut(kv, "=")
	}
	out := e.Without(names...)
	out.vars = append(out.vars, vars...)
	return out
}

// Without is e minus the variables named.
func (e Env) Without(names ...string) Env {
	out := Env{dir: e.dir}
	for _, kv := range e.vars {
		if n, _, _ := strings.Cut(kv, "="); !slices.Contains(names, n) {
			out.vars = append(out.vars, kv)
		}
	}
	return out
}

// Command is name with args, killed when ctx is done, run in the directory
// and with the variables of ctx's Env, and found on its $PATH where name
// has no slash. The caller may change Dir and Env before starting it.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	e := From(ctx)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = e.Dir()
	// Never nil, which would hand the program this process's environment.
	cmd.Env = append([]string{}, e.vars...)
	if !strings.Contains(name, "/") {
		cmd.Path, cmd.Err = e.LookPath(name)
	}
	return cmd
}

// LookPath is the executable name in a directory of e's $PATH, as
// exec.LookPath finds it in this process's. The first program found in a
// relative directory, an empty entry included, is refused with an error that
// wraps exec.ErrDot, as exec.LookPath refuses it (without its GODEBUG
// escape); it does not go on to a later directory. The error of a name not
// found wraps exec.ErrNotFound.
func (e Env) LookPath(name string) (string, error) {
	for _, dir := range filepath.SplitList(e.Get("PATH")) {
		if dir == "" {
			dir = "."
		}
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			if !filepath.IsAbs(path) {
				return path, &exec.Error{Name: name, Err: exec.ErrDot}
			}
			return path, nil
		}
	}
	return name, &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// In is e with the working directory dir.
func (e Env) In(dir string) Env {
	e.vars = slices.Clone(e.vars)
	e.dir = dir
	return e
}
