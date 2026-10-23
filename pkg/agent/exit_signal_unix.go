//go:build unix

package agent

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// exitSignal returns the signal that terminated the process, or 0 when it was
// not killed by one. A nil or non-exec error has no signal.
func exitSignal(err error) int {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 0
	}
	return waitSignal(ee.ProcessState)
}

// waitSignal extracts the terminating signal from a process state.
func waitSignal(state *os.ProcessState) int {
	if state == nil {
		return 0
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0
	}
	return int(status.Signal())
}
