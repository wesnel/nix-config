package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// maxJobs bounds the job records kept; the oldest finished go first.
const maxJobs = 64

// job is a long operation running in lima-mcp's memory, so a restart of
// lima-mcp loses it, and the guest's timeout then ends its command.
type job struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	status    string // running, succeeded, failed or cancelled
	exitCode  *int
	output    string
	truncated bool
	err       string
}

// jobRun is a job's work. It returns the command's output (at most
// maxOutput bytes, its tail), whether more was cut, the exit code where a
// command ran, and why it failed. id is the job's.
type jobRun func(ctx context.Context, id string) (output string, truncated bool, exitCode *int, err error)

type jobs struct {
	mu    sync.Mutex
	base  context.Context
	byID  map[string]*job
	order []string
}

func newJobs(base context.Context) *jobs {
	return &jobs{base: base, byID: map[string]*job{}}
}

// start runs work in a goroutine, under timeout, and returns its job.
func (js *jobs) start(timeout time.Duration, work jobRun) *job {
	var b [8]byte
	rand.Read(b[:])
	ctx, cancel := context.WithTimeout(js.base, timeout)
	j := &job{id: hex.EncodeToString(b[:]), cancel: cancel, done: make(chan struct{}), status: "running"}
	js.mu.Lock()
	js.byID[j.id] = j
	js.order = append(js.order, j.id)
	js.evict()
	js.mu.Unlock()
	go func() {
		defer close(j.done)
		defer cancel()
		output, truncated, code, err := work(ctx, j.id)
		j.mu.Lock()
		defer j.mu.Unlock()
		j.output, j.truncated, j.exitCode = output, truncated, code
		switch {
		case cancelled(ctx):
			j.status = "cancelled"
		case err != nil:
			j.status, j.err = "failed", err.Error()
		default:
			j.status = "succeeded"
		}
	}()
	return j
}

// cancelled reports whether the job was cancelled, rather than timing out
// or failing.
func cancelled(ctx context.Context) bool {
	return context.Cause(ctx) == context.Canceled
}

// evict drops the oldest finished records beyond maxJobs.
func (js *jobs) evict() {
	for i := 0; len(js.order) > maxJobs && i < len(js.order); {
		j := js.byID[js.order[i]]
		select {
		case <-j.done:
			delete(js.byID, j.id)
			js.order = append(js.order[:i], js.order[i+1:]...)
		default:
			i++
		}
	}
}

func (js *jobs) get(id string) *job {
	js.mu.Lock()
	defer js.mu.Unlock()
	return js.byID[id]
}

// result waits up to wait for j to finish and returns its record.
func (j *job) result(ctx context.Context, wait time.Duration) map[string]any {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-j.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	r := map[string]any{"job": j.id, "status": j.status, "output": j.output, "truncated": j.truncated}
	if j.exitCode != nil {
		r["exit_code"] = *j.exitCode
	}
	if j.err != "" {
		r["error"] = j.err
	}
	return r
}
