package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// diskFileName is the snapshot-owned copy of the task disk, kept next to
// vm.state and vm.mem so one snapshot directory is one complete restore point.
const diskFileName = "rootfs.ext4"

// copyChunkSize bounds one read/write pair. A copy of a multi-GB disk is
// interruptible because the context is checked between chunks, so this is the
// unit of cancellation latency, not a buffer-size tuning knob.
const copyChunkSize = 1 << 20

// CopyMode is how a copy produced its destination. It is reported rather than
// assumed because the difference is disk: a filesystem that can reflink shares the
// source's blocks, and one that cannot writes every byte again. A node planning
// its capacity, or an operator whose instances suddenly cost their images' size,
// has to be able to see which one happened.
type CopyMode string

const (
	// CopyNone is the zero value: no copy was made, because it failed or was never
	// asked for.
	CopyNone CopyMode = ""
	// CopyReflink means the destination shares the source's blocks (FICLONE) and
	// diverges only as one side is written, so the copy costs no data.
	CopyReflink CopyMode = "reflink"
	// CopySparse means the data extents were written out and the holes kept, which
	// is what a filesystem without reflink gives a disk image.
	CopySparse CopyMode = "sparse"
	// CopyFull means every byte, holes included, was written.
	CopyFull CopyMode = "full"
)

// copyDiskFile is the seam a test uses to observe when the task disk is copied
// relative to the VM's pause/resume pair. Production never replaces it.
var copyDiskFile = copyFile

// copyFile copies src to dst, creating dst's parent directories and keeping the
// source's permission bits. The copy is flushed before it returns, and the mode it
// took is reported: a reflink shares the source's blocks, a sparse copy keeps the
// holes, and a full copy writes every byte.
//
// The copy is chunked and holds no lock, but it does stop when ctx is cancelled:
// a request that gave up must not keep a node busy copying for minutes. A
// destination left half-written by a failure or a cancellation is removed, so a
// caller that sees an error can assume no destination file exists. The platform
// files choose the mechanism (see copyFileData).
//
// A destination that is the source file itself is refused before anything is
// opened: the copy exists so that two instances, or a snapshot and its source, each
// own their own file, and opening the source as the destination would truncate the
// very disk it was asked to copy.
func copyFile(ctx context.Context, src, dst string) (mode CopyMode, retErr error) {
	if err := ctx.Err(); err != nil {
		return CopyNone, fmt.Errorf("copy %s: %w", src, err)
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return CopyNone, fmt.Errorf("open source: %w", err)
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return CopyNone, fmt.Errorf("stat source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return CopyNone, fmt.Errorf("source %s is not a regular file", src)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return CopyNone, fmt.Errorf("create destination directory: %w", err)
	}

	if err := refuseSharedDestination(info, src, dst); err != nil {
		return CopyNone, err
	}

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return CopyNone, fmt.Errorf("create destination: %w", err)
	}

	defer func() {
		if closeErr := dstFile.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close destination: %w", closeErr)
		}
		if retErr != nil {
			// A partially copied disk must never be mistaken for a snapshot file
			// or for a task disk.
			_ = os.Remove(dst)
			mode = CopyNone
		}
	}()

	if mode, retErr = copyFileData(ctx, dstFile, srcFile); retErr != nil {
		return CopyNone, fmt.Errorf("copy data: %w", retErr)
	}
	if retErr = dstFile.Sync(); retErr != nil {
		return CopyNone, fmt.Errorf("fsync destination: %w", retErr)
	}

	// The copy wrote a file of its own. A destination that still shares the
	// source's inode after the copy would be one disk under two names, and the
	// guest's writes to one would corrupt the other, so nothing is published under
	// a name that turns out to be the source.
	if err := refuseSharedDestination(info, src, dst); err != nil {
		return CopyNone, err
	}

	return mode, nil
}

// refuseSharedDestination reports ErrSharedDisk when dst already exists and is the
// file info describes, following symlinks. A destination that does not exist is
// not the source: creating one is what a copy does.
func refuseSharedDestination(info os.FileInfo, src, dst string) error {
	dstInfo, err := os.Stat(dst)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat destination %s: %w", dst, err)
	}
	if os.SameFile(info, dstInfo) {
		return fmt.Errorf("%w: %s and %s are one file, and a copy must never overwrite the disk it copies",
			ErrSharedDisk, src, dst)
	}
	return nil
}

// replaceFile materialises src at dst in one visible step: the bytes go into a
// temporary file in dst's directory, are flushed by copyFile, and are renamed
// over dst. A restore therefore never runs a VM against a half-written disk, and
// a failed copy leaves whatever was at dst untouched.
//
// dst is replaced even when it already exists. A task disk left behind by an
// earlier restored VM is not the disk this snapshot's memory image describes, and
// reusing it would let the guest flush one VM's page cache over the other's data.
func replaceFile(ctx context.Context, src, dst string) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(dst)+".restore-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	// copyFile writes its own destination file, so this handle only reserves a
	// unique path; close it before the copy reopens the same name.
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temporary file: %w", closeErr)
	}

	// The mode a restore's copy took is the launcher's to report when it stages an
	// instance's disk; this path only needs the bytes.
	if _, err := copyFile(ctx, src, tmpPath); err != nil {
		_ = os.Remove(tmpPath) // copyFile already removes it; this is belt and braces
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("publish restored disk: %w", err)
	}
	return nil
}

// copyAll copies every byte including holes. It is what a platform without
// reflink or extent reporting uses, and the last fallback on Linux.
func copyAll(ctx context.Context, dst, src *os.File) error {
	if err := resetForCopy(dst, src); err != nil {
		return err
	}
	_, err := copyRange(ctx, dst, src, -1)
	return err
}

// copyRange copies exactly n bytes, or everything until EOF when n is negative,
// in copyChunkSize pieces. ctx is checked before every chunk, so a cancelled
// request stops within one chunk instead of after the whole image.
func copyRange(ctx context.Context, dst io.Writer, src io.Reader, n int64) (written int64, err error) {
	buf := make([]byte, copyChunkSize)

	for n != 0 {
		if err := ctx.Err(); err != nil {
			return written, err
		}

		want := int64(len(buf))
		if n > 0 && n < want {
			want = n
		}

		read, readErr := src.Read(buf[:want])
		if read > 0 {
			wrote, writeErr := dst.Write(buf[:read])
			written += int64(wrote)
			if n > 0 {
				n -= int64(wrote)
			}
			if writeErr != nil {
				return written, writeErr
			}
			if wrote != read {
				return written, io.ErrShortWrite
			}
		}

		switch {
		case readErr == io.EOF:
			if n > 0 {
				return written, io.ErrUnexpectedEOF
			}
			return written, nil
		case readErr != nil:
			return written, readErr
		}
	}

	return written, nil
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
