//go:build !linux && !darwin

package snapshot

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid still names a running process. On a platform
// with neither procfs nor the BSD process-table sysctl, the signal-0 probe is the
// only portable answer. A zombie counts as alive until its parent reaps it, so a
// stop on such a platform may use the whole grace window before the SIGKILL; the
// result is the same.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
