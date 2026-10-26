//go:build linux

package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// copyFileData copies the bytes of src into dst. It first asks the filesystem to
// share the source's blocks (a reflink costs no data copy), then copies only the
// data extents so a sparse disk keeps its holes, and finally falls back to a
// plain byte copy when the filesystem supports neither. Every attempt is subject
// to ctx, so a cancelled request stops between chunks.
func copyFileData(ctx context.Context, dst, src *os.File) error {
	if err := cloneExtents(ctx, dst, src); err == nil {
		return nil
	}
	if err := copyDataExtents(ctx, dst, src); err == nil {
		return nil
	}
	return copyAll(ctx, dst, src)
}

// cloneExtents asks the filesystem to share the source's extents (FICLONE).
// EOPNOTSUPP, EXDEV, EINVAL and similar are expected on a filesystem without
// reflink support; the caller falls back, so the error is only a signal.
func cloneExtents(ctx context.Context, dst, src *os.File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := resetForCopy(dst, src); err != nil {
		return err
	}
	return unix.IoctlSetInt(int(dst.Fd()), unix.FICLONE, int(src.Fd()))
}

// copyDataExtents copies only what SEEK_DATA reports, so a sparse disk image
// keeps its holes instead of turning into gigabytes of zero blocks.
func copyDataExtents(ctx context.Context, dst, src *os.File) error {
	if err := resetForCopy(dst, src); err != nil {
		return err
	}

	info, err := src.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	// A trailing hole is never copied, so the destination length is set up front.
	if err := dst.Truncate(size); err != nil {
		return err
	}

	var offset int64
	for offset < size {
		dataOffset, err := unix.Seek(int(src.Fd()), offset, unix.SEEK_DATA)
		if err != nil {
			if errors.Is(err, unix.ENXIO) {
				// The rest of the file is a hole.
				return nil
			}
			return err
		}

		holeOffset, err := unix.Seek(int(src.Fd()), dataOffset, unix.SEEK_HOLE)
		if err != nil {
			if !errors.Is(err, unix.ENXIO) {
				return err
			}
			holeOffset = size
		}
		if holeOffset > size {
			holeOffset = size
		}
		if holeOffset <= dataOffset {
			return fmt.Errorf("extent search made no progress at offset %d", dataOffset)
		}

		// SEEK_DATA/SEEK_HOLE moved the read offset; rewind to the data run.
		if _, err := src.Seek(dataOffset, io.SeekStart); err != nil {
			return err
		}
		if _, err := dst.Seek(dataOffset, io.SeekStart); err != nil {
			return err
		}
		// One extent can be gigabytes long, so it is copied in chunks that check
		// ctx between them.
		if _, err := copyRange(ctx, dst, src, holeOffset-dataOffset); err != nil {
			return err
		}

		offset = holeOffset
	}

	return nil
}
