package golden

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Materialize copies the artifact's rootfs to dst so a VM gets its own writable
// root filesystem without mutating the shared golden template. Sharing one
// read/write rootfs across replicas would corrupt it, and a sandboxed daemon may
// not even be allowed to write to the golden store.
//
// It is a no-op when dst already exists, and prefers a copy-on-write clone
// (cheap on btrfs/XFS) while preserving sparseness.
func (a *Artifact) Materialize(ctx context.Context, dst string) error {
	if a == nil {
		return fmt.Errorf("golden: nil artifact")
	}
	if dst == "" {
		return fmt.Errorf("golden: empty destination")
	}
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("golden: create %s: %w", filepath.Dir(dst), err)
	}

	// Prefer coreutils: --reflink=auto clones when the filesystem supports it
	// and copies otherwise, and --sparse=always keeps the image sparse.
	if cp, err := exec.LookPath("cp"); err == nil {
		if _, cpErr := exec.CommandContext(ctx, cp, "--reflink=auto", "--sparse=always", a.Path, dst).CombinedOutput(); cpErr == nil {
			return nil
		}
		_ = os.Remove(dst)
	}

	return copyFileSparse(a.Path, dst)
}

// copyFileSparse copies src to dst without materializing holes.
func copyFileSparse(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("golden: open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("golden: create %s: %w", dst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("golden: copy %s -> %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("golden: close %s: %w", dst, err)
	}
	return nil
}
