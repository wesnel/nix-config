//go:build !darwin

package limavm

import "errors"

// ProcessArgs reads a process's argv only on macOS, where the managed VM
// runs.
func ProcessArgs(pid int) (string, []string, error) {
	return "", nil, errors.New("reading a process's argv needs macOS")
}

// UserProcesses lists processes only on macOS.
func UserProcesses() ([]Proc, error) {
	return nil, errors.New("listing processes needs macOS")
}
