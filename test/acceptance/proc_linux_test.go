//go:build acceptance && linux

package acceptance

import (
	"os/exec"
	"syscall"
)

// sysProcAttr puts the daemon in its own process group, so the run can signal the
// daemon and everything it started (a VMM, a relay) as one.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup asks the daemon's whole process group to stop.
func signalGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}
