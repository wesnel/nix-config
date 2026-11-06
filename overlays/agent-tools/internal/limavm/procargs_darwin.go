package limavm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// UserProcesses returns the user's processes: those of the kern.proc.all
// sysctl whose effective user is this process's. (kern.proc.uid would
// select them, but a Seatbelt sandbox, such as an agent session's,
// refuses it.)
func UserProcesses() ([]Proc, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("kern.proc.all: %w", err)
	}
	uid := os.Geteuid()
	var out []Proc
	for _, p := range procs {
		if int(p.Eproc.Ucred.Uid) == uid {
			comm := p.Proc.P_comm[:]
			if i := bytes.IndexByte(comm, 0); i >= 0 {
				comm = comm[:i]
			}
			out = append(out, Proc{PID: int(p.Proc.P_pid), Comm: string(comm)})
		}
	}
	return out, nil
}

// ProcessArgs returns the executable path and argv of process pid, from
// the kern.procargs2 sysctl: the argument count, the path the process was
// executed from, NUL padding, then each argument NUL-terminated (and the
// environment, which it ignores). Where the read fails because pid has
// exited, reaped or not, its error is ErrExited; the kernel then gives
// EPERM or EINVAL as well as ESRCH, so a fresh kern.proc.pid lookup says
// whether it has.
func ProcessArgs(pid int) (string, []string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		if exited(pid) {
			err = ErrExited
		}
		return "", nil, fmt.Errorf("kern.procargs2 of %d: %w", pid, err)
	}
	return parseProcargs(b)
}

// szomb is p_stat's SZOMB of sys/proc.h: exited, not yet reaped.
const szomb = 5

// exited reports whether kern.proc.pid finds pid gone or a zombie.
func exited(pid int) bool {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return false
	}
	return len(procs) == 0 || procs[0].Proc.P_stat == szomb
}

func parseProcargs(b []byte) (string, []string, error) {
	if len(b) < 4 {
		return "", nil, errors.New("kern.procargs2: too short")
	}
	argc := int(binary.LittleEndian.Uint32(b[:4]))
	b = b[4:]
	end := bytes.IndexByte(b, 0)
	if end < 0 {
		return "", nil, errors.New("kern.procargs2: no executable path")
	}
	exe := string(b[:end])
	b = bytes.TrimLeft(b[end:], "\x00")
	argv := make([]string, 0, argc)
	for range argc {
		end := bytes.IndexByte(b, 0)
		if end < 0 {
			return "", nil, errors.New("kern.procargs2: argv ends early")
		}
		argv = append(argv, string(b[:end]))
		b = b[end+1:]
	}
	return exe, argv, nil
}
