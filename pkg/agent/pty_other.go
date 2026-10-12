//go:build !linux

package agent

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// openPTY is unsupported off Linux; the package still compiles so the host-side
// client can be built and tested on other platforms.
func openPTY() (*os.File, *os.File, error) {
	return nil, nil, fmt.Errorf("open pty: unsupported on %s", runtime.GOOS)
}

// ptySysProcAttr is unused off Linux (openPTY always fails).
func ptySysProcAttr() *syscall.SysProcAttr {
	return nil
}
