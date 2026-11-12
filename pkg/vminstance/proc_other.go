//go:build !linux

package vminstance

import (
	"os"
	"syscall"
)

// ProcessAlive reports whether pid still names a running process, by the rule the
// registry uses to decide whether a record describes a live VM (see the Linux
// implementation's comment for why it is exported).
func ProcessAlive(pid int) bool { return processAlive(pid) }

// processAlive reports whether pid still names a running process. Off Linux the
// signal-0 probe is the portable answer: it asks the kernel whether the process
// exists without sending it anything. A zombie counts as alive until its parent
// reaps it, so a VM that just died may stay resolvable for a moment; the record
// then fails to dial and the next lookup reports it stale, which is the same end
// state the Linux answer reaches at once.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || os.IsPermission(err)
}
