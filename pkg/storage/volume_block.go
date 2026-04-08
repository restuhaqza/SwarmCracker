package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/rs/zerolog/log"
)

const blockImageFile = "image.ext4"

// BlockDriver stores volume data in an ext4 loopback image file.
//
// Advantages over DirectoryDriver:
//   - Native filesystem quotas via ext4 reserved blocks
//   - Full POSIX filesystem semantics inside the VM (symlinks, permissions, xattrs)
//   - Efficient copy via block-level operations
//
// Requires root for mkfs, mount/umount.
type BlockDriver struct {
	meta     *MetaStore
	quota    *QuotaEnforcer
	mountDir string // base dir for temporary mount points
}

// NewBlockDriver creates a driver that stores volume data in ext4 images.
func NewBlockDriver(baseDir string) (*BlockDriver, error) {
	meta, err := NewMetaStore(baseDir)
	if err != nil {
		return nil, err
	}
	mountDir := filepath.Join(baseDir, ".mounts")
	if err := os.MkdirAll(mountDir, 0750); err != nil {
		return nil, fmt.Errorf("create mount base dir: %w", err)
	}
	return &BlockDriver{
		meta:     meta,
		quota:    NewQuotaEnforcer(),
		mountDir: mountDir,
	}, nil
}

// Type returns VolumeTypeBlock.
func (d *BlockDriver) Type() VolumeType { return VolumeTypeBlock }

// imagePath returns the ext4 image path for a volume.
func (d *BlockDriver) imagePath(name string) string {
	return filepath.Join(d.meta.volumeDir(name), blockImageFile)
}

// mountPoint returns the mount path for a volume.
func (d *BlockDriver) mountPoint(name string) string {
	return filepath.Join(d.mountDir, sanitizeVolumeName(name))
}

// Create creates an ext4 loopback image for the volume.
// If opts.SizeMB is 0, defaults to 1024 MB.
func (d *BlockDriver) Create(ctx context.Context, name string, opts CreateOptions) (string, error) {
	if name == "" {
		return "", fmt.Errorf("volume name cannot be empty")
	}

	sizeMB := opts.SizeMB
	if sizeMB <= 0 {
		sizeMB = 1024 // default 1 GB
	}

	if err := d.quota.CheckCreate(sizeMB); err != nil {
		return "", err
	}

	dir := d.meta.volumeDir(name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create volume dir: %w", err)
	}

	imgPath := d.imagePath(name)
	sizeBytes := int64(sizeMB) * 1024 * 1024

	// Create sparse image file
	if err := createSparseFile(imgPath, sizeBytes); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("create image file: %w", err)
	}

	// Format as ext4
	if output, err := exec.Command("mkfs.ext4", "-F", "-q", imgPath).CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("mkfs.ext4: %s: %w", string(output), err)
	}

	m := &volumeMeta{
		Name:      name,
		Type:      VolumeTypeBlock,
		SizeMB:    sizeMB,
		CreatedAt: nowUTC(),
	}

	if err := d.meta.Write(ctx, m); err != nil {
		os.RemoveAll(dir)
		return "", err
	}

	log.Info().Str("volume", name).Str("image", imgPath).Int("size_mb", sizeMB).Msg("Block volume created")
	return dir, nil
}

// Delete removes the image file and volume directory.
func (d *BlockDriver) Delete(ctx context.Context, name string) error {
	// Ensure unmounted first
	_ = d.ensureUnmounted(name)

	if err := d.meta.RemoveVolumeDir(name); err != nil {
		return err
	}
	log.Info().Str("volume", name).Msg("Block volume deleted")
	return nil
}

// Mount mounts the ext4 image and copies data into rootfs at target.
func (d *BlockDriver) Mount(ctx context.Context, name, rootfsPath, target string) error {
	imgPath := d.imagePath(name)
	if _, err := os.Stat(imgPath); err != nil {
		return fmt.Errorf("volume image not found: %s", name)
	}

	mnt := d.mountPoint(name)
	if err := os.MkdirAll(mnt, 0750); err != nil {
		return fmt.Errorf("create mount point: %w", err)
	}

	// Mount the ext4 image
	if output, err := exec.Command("mount", "-o", "loop", imgPath, mnt).CombinedOutput(); err != nil {
		return fmt.Errorf("mount image: %s: %w", string(output), err)
	}

	targetPath := ensureAbsolutePath(target)
	dest := filepath.Join(rootfsPath, targetPath)
	if err := os.MkdirAll(dest, 0755); err != nil {
		_ = exec.Command("umount", mnt).Run()
		return fmt.Errorf("create target dir: %w", err)
	}

	if err := copyDirectory(mnt, dest); err != nil {
		_ = exec.Command("umount", mnt).Run()
		return fmt.Errorf("copy data from block volume: %w", err)
	}

	// Unmount after copy
	if output, err := exec.Command("umount", mnt).CombinedOutput(); err != nil {
		log.Warn().Str("output", string(output)).Err(err).Msg("Failed to unmount after mount copy")
	}

	_ = d.meta.TouchLastUsed(ctx, name)

	log.Info().Str("volume", name).Str("target", targetPath).Msg("Block volume mounted")
	return nil
}

// Unmount mounts the ext4 image and syncs data back from rootfs.
func (d *BlockDriver) Unmount(ctx context.Context, name, rootfsPath, target string, readOnly bool) error {
	if readOnly {
		log.Debug().Str("volume", name).Msg("Skipping unmount sync for read-only block volume")
		return nil
	}

	imgPath := d.imagePath(name)
	if _, err := os.Stat(imgPath); err != nil {
		return fmt.Errorf("volume image not found: %s", name)
	}

	// Check quota before syncing back
	m, err := d.meta.Read(ctx, name)
	if err != nil {
		return fmt.Errorf("read volume metadata: %w", err)
	}

	targetPath := ensureAbsolutePath(target)
	src := filepath.Join(rootfsPath, targetPath)

	if err := d.quota.CheckSync(name, src, m.SizeMB); err != nil {
		return err
	}

	mnt := d.mountPoint(name)
	if err := os.MkdirAll(mnt, 0750); err != nil {
		return fmt.Errorf("create mount point: %w", err)
	}

	// Mount the ext4 image for writing
	if output, err := exec.Command("mount", "-o", "loop", imgPath, mnt).CombinedOutput(); err != nil {
		return fmt.Errorf("mount image: %s: %w", string(output), err)
	}

	// Clear existing content and copy new data
	if err := clearDirectory(mnt); err != nil {
		_ = exec.Command("umount", mnt).Run()
		return fmt.Errorf("clear block volume: %w", err)
	}

	if err := copyDirectory(src, mnt); err != nil {
		_ = exec.Command("umount", mnt).Run()
		return fmt.Errorf("sync data to block volume: %w", err)
	}

	// Unmount
	if output, err := exec.Command("umount", mnt).CombinedOutput(); err != nil {
		return fmt.Errorf("unmount image: %s: %w", string(output), err)
	}

	// Run fsck to ensure filesystem integrity
	if output, err := exec.Command("e2fsck", "-p", "-f", imgPath).CombinedOutput(); err != nil {
		log.Warn().Str("output", string(output)).Err(err).Msg("fsck reported issues")
	}

	log.Info().Str("volume", name).Str("target", targetPath).Msg("Block volume unmounted (data synced)")
	return nil
}

// Stat returns metadata and disk usage.
func (d *BlockDriver) Stat(ctx context.Context, name string) (*VolumeInfo, error) {
	m, err := d.meta.Read(ctx, name)
	if err != nil {
		return nil, err
	}

	imgPath := d.imagePath(name)
	info, err := os.Stat(imgPath)
	var usedMB int64
	if err == nil {
		usedMB = info.Size() / (1024 * 1024)
	}

	return &VolumeInfo{
		Name:       m.Name,
		Type:       m.Type,
		Path:       imgPath,
		SizeMB:     m.SizeMB,
		CreatedAt:  m.CreatedAt,
		LastUsedAt: m.LastUsedAt,
		TaskID:     m.TaskID,
		UsedMB:     usedMB,
	}, nil
}

// Capacity reports image size and limit.
func (d *BlockDriver) Capacity(ctx context.Context, name string) (int64, int64, error) {
	m, err := d.meta.Read(ctx, name)
	if err != nil {
		return 0, 0, err
	}

	imgPath := d.imagePath(name)
	info, err := os.Stat(imgPath)
	if err != nil {
		return 0, 0, err
	}

	limitBytes := int64(m.SizeMB) * 1024 * 1024
	return info.Size(), limitBytes, nil
}

// Snapshot creates a raw copy of the ext4 image.
func (d *BlockDriver) Snapshot(ctx context.Context, name string) (*Snapshot, error) {
	imgPath := d.imagePath(name)
	if _, err := os.Stat(imgPath); err != nil {
		return nil, fmt.Errorf("volume image not found: %s", name)
	}

	snapDir := filepath.Join(d.meta.baseDir, ".snapshots")
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		return nil, fmt.Errorf("create snapshots dir: %w", err)
	}

	snapID := fmt.Sprintf("%s-%d", name, nowUTC().UnixMilli())
	snapPath := filepath.Join(snapDir, snapID+".ext4")

	// Copy the image file (could use cp --reflink for CoW on supported filesystems)
	if output, err := exec.Command("cp", imgPath, snapPath).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("copy image: %s: %w", string(output), err)
	}

	info, _ := os.Stat(snapPath)
	var sizeMB int64
	if info != nil {
		sizeMB = info.Size() / (1024 * 1024)
	}

	snap := &Snapshot{
		ID:        snapID,
		Volume:    name,
		Path:      snapPath,
		CreatedAt: nowUTC(),
		SizeMB:    sizeMB,
	}

	log.Info().Str("snapshot", snapID).Str("volume", name).Msg("Block snapshot created")
	return snap, nil
}

// Restore replaces volume image from a snapshot.
func (d *BlockDriver) Restore(ctx context.Context, name string, snap *Snapshot) error {
	if snap == nil {
		return fmt.Errorf("snapshot cannot be nil")
	}

	_ = d.ensureUnmounted(name)

	imgPath := d.imagePath(name)
	if output, err := exec.Command("cp", snap.Path, imgPath).CombinedOutput(); err != nil {
		return fmt.Errorf("restore image: %s: %w", string(output), err)
	}

	// Run fsck on restored image
	if output, err := exec.Command("e2fsck", "-p", "-f", imgPath).CombinedOutput(); err != nil {
		log.Warn().Str("output", string(output)).Err(err).Msg("fsck on restored image reported issues")
	}

	log.Info().Str("snapshot", snap.ID).Str("volume", name).Msg("Block volume restored from snapshot")
	return nil
}

// Export streams the raw ext4 image to w.
func (d *BlockDriver) Export(ctx context.Context, name string, w io.Writer) error {
	imgPath := d.imagePath(name)
	f, err := os.Open(imgPath)
	if err != nil {
		return fmt.Errorf("open image: %w", err)
	}
	defer f.Close()

	_, err = io.Copy(w, f)
	return err
}

// Import reads a raw ext4 image from r and writes it as the volume image.
func (d *BlockDriver) Import(ctx context.Context, name string, r io.Reader, sizeMB int) error {
	opts := CreateOptions{Type: VolumeTypeBlock, SizeMB: sizeMB}
	if _, err := d.Create(ctx, name, opts); err != nil {
		// May already exist
		_ = d.ensureUnmounted(name)
	}

	imgPath := d.imagePath(name)
	f, err := os.Create(imgPath)
	if err != nil {
		return fmt.Errorf("create image: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write image data: %w", err)
	}

	// Verify filesystem
	if output, err := exec.Command("e2fsck", "-p", "-f", imgPath).CombinedOutput(); err != nil {
		return fmt.Errorf("fsck on imported image: %s: %w", string(output), err)
	}

	log.Info().Str("volume", name).Msg("Block volume imported")
	return nil
}

// ensureUnmounted unmounts the volume image if currently mounted.
func (d *BlockDriver) ensureUnmounted(name string) error {
	mnt := d.mountPoint(name)
	// Check if mount point is active
	output, err := exec.Command("mountpoint", "-q", mnt).CombinedOutput()
	if err != nil {
		return nil // not mounted
	}
	_ = output
	if output, err := exec.Command("umount", mnt).CombinedOutput(); err != nil {
		return fmt.Errorf("umount %s: %s: %w", mnt, string(output), err)
	}
	return nil
}

// createSparseFile creates a sparse file of the given size.
func createSparseFile(path string, sizeBytes int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := f.Truncate(sizeBytes); err != nil {
		return err
	}
	return f.Sync()
}

// clearDirectory removes all contents of a directory without removing the directory itself.
func clearDirectory(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}
