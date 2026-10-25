//go:build !linux

package snapshot

import "os"

// copyFileData copies the file byte by byte. Reflinks and extent reporting are
// Linux-only in this package (see copyfile_linux.go), so every other platform
// takes this path: the package still builds and its tests run natively on a
// Darwin host, where the copy is exercised end to end.
func copyFileData(dst, src *os.File) error {
	return copyAll(dst, src)
}
