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

// fileOwner reports the owner and group of a file from its stat information, for
// the file operations that describe a tree a caller is about to copy. A platform
// that does not record ownership answers "unknown" and the caller reports zeros.
func fileOwner(info fs.FileInfo) (uid, gid int, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
