//go:build darwin

package snapshot

import (
	"golang.org/x/sys/unix"
)

// szomb is the BSD process state of a zombie: exited but not yet reaped by its
// parent. kill(pid, 0) succeeds for a zombie, so the signal probe alone cannot
// tell an exited VM from a running one.
const szomb = 5

// processAlive reports whether pid still names a running process. Darwin has no
// procfs, so the process state comes from the kernel's process table: a zombie
// counts as gone, exactly like the Linux implementation.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false // ESRCH: no such process
	}
	return info.Proc.P_stat != szomb
}
