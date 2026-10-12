//go:build linux

package agent

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// devptsSuperMagic is DEVPTS_SUPER_MAGIC from <linux/magic.h>.
const devptsSuperMagic = 0x1cd1

// ensureDevPTS makes sure a devpts instance is mounted at /dev/pts. The kernel
// refuses to open /dev/ptmx until one is available, so PTY allocation fails
// with ENOENT on images whose own init does not mount devpts.
func ensureDevPTS() error {
	var st unix.Statfs_t
	if err := unix.Statfs("/dev/pts", &st); err == nil && st.Type == devptsSuperMagic {
		return nil
	}

	if err := os.MkdirAll("/dev/pts", 0o755); err != nil {
		return fmt.Errorf("create /dev/pts: %w", err)
	}
	// EBUSY means something is already mounted there; opening /dev/ptmx below
	// is the real test of whether it is usable.
	if err := unix.Mount("devpts", "/dev/pts", "devpts", 0, ""); err != nil && !errors.Is(err, unix.EBUSY) {
		return fmt.Errorf("mount devpts at /dev/pts: %w", err)
	}
	return nil
}

// openPTY opens a pseudo-terminal master/slave pair. It unlocks the master with
// TIOCSPTLCK, reads the pts number with TIOCGPTN, and opens the matching slave.
func openPTY() (*os.File, *os.File, error) {
	if err := ensureDevPTS(); err != nil {
		return nil, nil, err
	}

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("open /dev/ptmx (no devpts mounted at /dev/pts?): %w", err)
		}
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("unlock pty master (TIOCSPTLCK): %w", err)
	}

	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("read pty number (TIOCGPTN): %w", err)
	}

	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("open /dev/pts/%d: %w", n, err)
	}
	return master, slave, nil
}

// ptySysProcAttr makes the child the session leader with the pty slave as its
// controlling terminal (Ctty 0 refers to the child's stdin).
func ptySysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
}
