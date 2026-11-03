//go:build linux

package vminstance

import (
	"fmt"
	"os"
	"strings"
)

// processAlive reports whether pid still names a running process. On Linux the
// answer comes from procfs rather than a signal-0 probe: a killed VMM stays a
// zombie until its parent reaps it, and kill(pid, 0) succeeds for a zombie, so
// the signal probe would keep a dead VM's record resolvable until the daemon
// happened to wait on it.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false // gone, or not readable by this process
	}

	// The state is the field after the parenthesised command name. The name can
	// contain spaces and parentheses (a renamed binary), so the last ')' is the
	// reliable delimiter.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 || end+2 >= len(data) {
		return true // unparseable: assume alive rather than forget a running VM
	}
	switch data[end+2] {
	case 'Z', 'X':
		return false
	default:
		return true
	}
}
