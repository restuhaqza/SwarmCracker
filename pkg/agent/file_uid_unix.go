//go:build unix

package agent

import (
	"io/fs"
	"syscall"
)

// fileUID reports the owner of a file from its stat information. It exists so
// the token check can require a root-owned file in the guest without pulling
// platform-specific stat types into the shared code.
func fileUID(info fs.FileInfo) (uint32, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}
