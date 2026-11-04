//go:build !linux

package snapshot

import (
	"context"
	"os"
)

// copyFileData copies the file byte by byte. Reflinks and extent reporting are
// Linux-only in this package (see copyfile_linux.go), so every other platform
// takes this path: the package still builds and its tests run natively on a
// Darwin host, where the copy is exercised end to end. A copy there is always a
// full one, and it says so.
func copyFileData(ctx context.Context, dst, src *os.File) (CopyMode, error) {
	if err := copyAll(ctx, dst, src); err != nil {
		return CopyNone, err
	}
	return CopyFull, nil
}
