package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCancelledJobError(t *testing.T) {
	js := newJobs(t.Context())
	j := js.start(time.Hour, func(ctx context.Context, _ string) (string, bool, *int, error) {
		<-ctx.Done()
		return "", false, nil, fmt.Errorf("%w; guest cleanup failed", ctx.Err())
	})
	j.cancel()
	<-j.done
	if j.status != "cancelled" || !strings.Contains(j.err, "guest cleanup failed") {
		t.Fatalf("cancelled status %q error %q", j.status, j.err)
	}
}

func TestJobEviction(t *testing.T) {
	js := newJobs(t.Context())
	done := func(context.Context, string) (string, bool, *int, error) { return "", false, nil, nil }
	hold := func(ctx context.Context, _ string) (string, bool, *int, error) {
		<-ctx.Done()
		return "", false, nil, ctx.Err()
	}
	running := js.start(time.Hour, hold)
	defer running.cancel()
	var finished []*job
	for range maxJobs {
		j := js.start(time.Hour, done)
		<-j.done
		finished = append(finished, j)
	}
	// maxJobs+1 records: the oldest finished one goes, the running one
	// stays, though it is older.
	if js.get(finished[0].id) != nil {
		t.Error("the oldest finished job was kept beyond maxJobs")
	}
	if js.get(running.id) == nil {
		t.Error("a running job was evicted")
	}
	if js.get(finished[1].id) == nil || js.get(finished[maxJobs-1].id) == nil {
		t.Error("a job within maxJobs was evicted")
	}
}
