package signpass

import (
	"context"
	"io"
	"os"
	"time"
)

// Options are what one pass takes from its caller beside its arguments: the
// terminal it talks to the operator on, how much it says there, the
// --notify ID the commands it prints carry, and how it retries a missed
// touch with no one to ask. WithOptions carries them in the context every
// entry point (Run, Resume, Plan) takes; a context without them gets
// DefaultOptions.
type Options struct {
	// In is where the operator answers whether to retry a missed touch,
	// which the pass asks only when In is an *os.File that is a terminal.
	// The git commands that ask for a touch read it too.
	In io.Reader
	// Err is where the pass talks to the operator, and where those git
	// commands write their standard error.
	Err io.Writer
	// Brief makes a pass print only what someone reading its output later
	// needs: what it will sign, one line for the touches, a progress line
	// every progressEvery signatures, one line per push, and anything git
	// reports as an error, in full. Git's own account of a rebase or a push
	// is left out.
	Brief bool
	// NotifyID is the --notify ID the pass runs with, which every command it
	// prints for resuming carries.
	NotifyID string
	// With no terminal to ask, a step the key refused is retried Retries
	// times, each after RetryWait, and the retried signature, with
	// everything git started for it, is killed after RetryTimeout. One
	// missed touch locks the key, and the next request then waits on
	// pinentry without end, so a retry must never run unbounded, and one is
	// all a locked key allows.
	Retries      int
	RetryWait    time.Duration
	RetryTimeout time.Duration
	// retryContext, when set, bounds one automatic retry in place of
	// RetryTimeout from now: tests end it on an event, as a deadline ends,
	// so that none depends on how long a signature takes.
	retryContext func(parent context.Context) (context.Context, context.CancelFunc)
}

// DefaultOptions are a pass's options when its caller gives none: this
// process's standard input and error, the full output, no --notify ID, and
// one automatic retry after 10s, killed after a minute.
func DefaultOptions() Options {
	return Options{In: os.Stdin, Err: os.Stderr, Retries: 1, RetryWait: 10 * time.Second, RetryTimeout: 60 * time.Second}
}

type optionsKey struct{}

// WithOptions is ctx carrying o for every pass run under it.
func WithOptions(ctx context.Context, o Options) context.Context {
	return context.WithValue(ctx, optionsKey{}, o)
}

// options are the Options ctx carries, or DefaultOptions; a missing In or
// Err is this process's.
func options(ctx context.Context) Options {
	o, ok := ctx.Value(optionsKey{}).(Options)
	if !ok {
		return DefaultOptions()
	}
	if o.In == nil {
		o.In = os.Stdin
	}
	if o.Err == nil {
		o.Err = os.Stderr
	}
	return o
}

// interactive reports whether the operator can answer on o.In: whether it
// is a terminal.
func (o Options) interactive() bool {
	f, ok := o.In.(*os.File)
	return ok && isTerminal(f)
}

// retry is the context that bounds one automatic retry: RetryTimeout from
// now, unless retryContext says otherwise.
func (o Options) retry(parent context.Context) (context.Context, context.CancelFunc) {
	if o.retryContext != nil {
		return o.retryContext(parent)
	}
	return context.WithTimeout(parent, o.RetryTimeout)
}
