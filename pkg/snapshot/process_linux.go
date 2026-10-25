//go:build linux

package snapshot

import (
	"fmt"
	"os"
	"strings"
)

// processAlive reports whether pid still names a running process. On Linux the
// state comes from procfs rather than a signal-0 probe: a killed child stays a
// zombie until its parent reaps it, and kill(pid, 0) succeeds for a zombie. A
// restored Firecracker is the daemon's child and this package never waits on it,
// so treating a zombie as alive would always burn the whole stop window before
// the SIGKILL.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false // gone, or not readable by this process
	}

	// The state is the field after the parenthesised command name. The name can
	// contain spaces and parentheses (a kernel thread, a renamed binary), so the
	// last ')' is the reliable delimiter.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 || end+2 >= len(data) {
		return true // unparseable: assume alive and let the SIGKILL decide
	}
	switch data[end+2] {
	case 'Z', 'X':
		return false
	default:
		return true
	}
}
