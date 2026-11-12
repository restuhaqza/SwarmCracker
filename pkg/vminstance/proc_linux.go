//go:build linux

package vminstance

import (
	"fmt"
	"os"
	"strings"
)

// ProcessAlive reports whether pid still names a running process, by the rule the
// registry uses to decide whether a record describes a live VM: procfs on Linux, so a
// killed process that is still a zombie is not alive, and a signal-0 probe elsewhere.
//
// It is exported because a teardown outside this package has to ask the same
// question the registry does. A sandbox's rm stops a VMM and then hands the rest of
// the teardown to the registry, and the two must agree on what "the VMM is gone"
// means — a teardown that trusted a different answer would free a live VM's
// namespace and disk, or refuse to free a dead one's forever.
func ProcessAlive(pid int) bool { return processAlive(pid) }

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
