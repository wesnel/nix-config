package relay

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
	"unsafe"

	"github.com/wesnel/agent-tools/internal/loopsession"
)

// groupLeader reports whether this process leads its process group.
var groupLeader = func() bool { return syscall.Getpgrp() == os.Getpid() }

// foreground reports whether standard input is a terminal whose foreground
// process group is this process's.
var foreground = func() bool {
	var pgrp int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), uintptr(syscall.TIOCGPGRP),
		uintptr(unsafe.Pointer(&pgrp))) //nolint:gosec // TIOCGPGRP writes one process group ID into pgrp
	return errno == 0 && int(pgrp) == syscall.Getpgrp()
}

// runSession runs a session's command to its end, passing on a TERM, as
// `pr-loop stop` sends. In a loop session (loopsession.Env=1) it records
// where the command runs for `pr-loop stop` while it runs, keyed by common,
// the repository's git common directory:
//   - a session that leads its own group, as a shell's job does, keeps the
//     command there and records that group;
//   - one that does not, in a terminal's foreground group, as under a
//     wrapper script, keeps the command there too, where the terminal's
//     input, interrupt and hangup reach it, and records no group, which
//     would be its caller's as well: `pr-loop stop` follows the session's
//     processes alone;
//   - any other gives the command a group of its own and records it, so
//     stopping it stops none of its caller's processes.
func runSession(cmd *exec.Cmd, common string, stderr io.Writer) error {
	record := os.Getenv(loopsession.Env) == "1"
	leads := groupLeader()
	shared := !leads && record && foreground()
	if record && !leads && !shared {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	terms := make(chan os.Signal, 1)
	signal.Notify(terms, syscall.SIGTERM)
	defer func() {
		signal.Stop(terms)
		close(terms)
	}()
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		for range terms {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()
	if record {
		r := loopsession.Record{PID: os.Getpid(), PGID: cmd.Process.Pid, GitDir: common, Started: time.Now()}
		switch {
		case leads:
			r.PGID = syscall.Getpgrp()
		case shared:
			r.PGID = 0
		}
		if err := loopsession.Write(r); err != nil {
			fmt.Fprintf(stderr, "sandbox-relay session: `pr-loop stop` will not find this session: %s\n", err)
		}
		defer loopsession.Remove(common, r.PID)
	}
	return cmd.Wait()
}
