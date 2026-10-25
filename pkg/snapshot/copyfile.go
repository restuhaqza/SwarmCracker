package snapshot

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// diskFileName is the snapshot-owned copy of the task disk, kept next to
// vm.state and vm.mem so one snapshot directory is one complete restore point.
const diskFileName = "rootfs.ext4"

// copyFile copies src to dst, creating dst's parent directories and keeping the
// source's permission bits. The copy is flushed before it returns.
//
// A destination left half-written by a failure is removed, so a caller that sees
// an error can assume no destination file exists. The platform files choose the
// mechanism (see copyFileData): a reflink where the filesystem supports one, a
// sparse copy on Linux, and a plain byte copy everywhere else.
func copyFile(src, dst string) (retErr error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source %s is not a regular file", src)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}

	defer func() {
		if closeErr := dstFile.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close destination: %w", closeErr)
		}
		if retErr != nil {
			// A partially copied disk must never be mistaken for a snapshot file.
			_ = os.Remove(dst)
		}
	}()

	if err := copyFileData(dstFile, srcFile); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}
	if err := dstFile.Sync(); err != nil {
		return fmt.Errorf("fsync destination: %w", err)
	}

	return nil
}

// copyAll copies every byte including holes. It is what a platform without
// reflink or extent reporting uses, and the last fallback on Linux.
func copyAll(dst, src *os.File) error {
	if err := resetForCopy(dst, src); err != nil {
		return err
	}
	_, err := io.Copy(dst, src)
	return err
}

// resetForCopy empties dst and rewinds both files, so a fallback attempt starts
// from a clean slate after a failed one left data or an offset behind.
func resetForCopy(dst, src *os.File) error {
	if err := dst.Truncate(0); err != nil {
		return err
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return nil
}
