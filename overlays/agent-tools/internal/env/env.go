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

// In is e with the working directory dir.
func (e Env) In(dir string) Env {
	e.vars = slices.Clone(e.vars)
	e.dir = dir
	return e
}
