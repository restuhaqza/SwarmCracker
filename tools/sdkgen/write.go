package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// fileOps is the filesystem surface the writer needs. Tests replace single
// entries to inject a failure deterministically: a read-only directory only
// fails for an unprivileged user, and root (or a container running as root)
// ignores the mode bits entirely.
type fileOps struct {
	mkdirAll  func(path string, perm os.FileMode) error
	writeFile func(path string, data []byte, perm os.FileMode) error
	rename    func(oldPath, newPath string) error
	remove    func(path string) error
}

// osFileOps is the real filesystem.
var osFileOps = fileOps{
	mkdirAll:  os.MkdirAll,
	writeFile: os.WriteFile,
	rename:    os.Rename,
	remove:    os.Remove,
}

// writeTargets writes every generated file with the real filesystem.
func writeTargets(targets []target) error {
	return writeTargetsWith(osFileOps, targets)
}

// writeTargetsWith writes every generated file atomically: the payload goes to
// <path>.tmp in the same directory and is then renamed over the target.
//
// The rename is what makes the guarantee useful. A reader (a concurrent build, a
// `go test ./...` that reads the committed models, a packaging step) either sees
// the previous complete file or the new complete file, never a half-written one,
// and a failure at any point leaves the previous bytes untouched. Writing the
// temporary file next to the target keeps both on one filesystem, so the rename
// is atomic instead of degrading to a copy.
func writeTargetsWith(ops fileOps, targets []target) error {
	for _, item := range targets {
		if err := writeAtomic(ops, item.path, item.data); err != nil {
			return err
		}
	}
	return nil
}

// writeAtomic replaces one file via a temporary sibling and a rename.
func writeAtomic(ops fileOps, path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := ops.mkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp := path + ".tmp"
	//nolint:gosec // G306: generated sources are world-readable by design.
	if err := ops.writeFile(tmp, data, 0o644); err != nil {
		discard(ops, tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := ops.rename(tmp, path); err != nil {
		discard(ops, tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// discard removes a temporary file after a failure. The removal error is
// irrelevant: the caller already reports the failure that caused it, and the
// target file was never touched.
func discard(ops fileOps, path string) {
	_ = ops.remove(path)
}
