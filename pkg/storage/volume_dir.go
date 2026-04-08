package storage

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
)

const dirDataSubdir = "data"

// DirectoryDriver stores volume data as a host directory.
//
// It is the simplest backend: compatible with any filesystem, no special
// privileges beyond write access to the volumes directory. Data is
// projected into the microVM rootfs by recursive copy.
type DirectoryDriver struct {
	meta *MetaStore
}

// NewDirectoryDriver creates a driver that stores volume data in directories.
func NewDirectoryDriver(baseDir string) (*DirectoryDriver, error) {
	meta, err := NewMetaStore(baseDir)
	if err != nil {
		return nil, err
	}
	return &DirectoryDriver{meta: meta}, nil
}

// Type returns VolumeTypeDir.
func (d *DirectoryDriver) Type() VolumeType { return VolumeTypeDir }

// Create creates a directory-backed volume.
func (d *DirectoryDriver) Create(ctx context.Context, name string, opts CreateOptions) (string, error) {
	if name == "" {
		return "", fmt.Errorf("volume name cannot be empty")
	}

	m := &volumeMeta{
		Name:      name,
		Type:      VolumeTypeDir,
		SizeMB:    opts.SizeMB,
		CreatedAt: nowUTC(),
	}

	dir := d.meta.volumeDir(name)
	dataDir := filepath.Join(dir, dirDataSubdir)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", fmt.Errorf("create volume data dir: %w", err)
	}

	if err := d.meta.Write(ctx, m); err != nil {
		os.RemoveAll(dir) // rollback
		return "", err
	}

	log.Info().Str("volume", name).Str("path", dir).Int("size_mb", opts.SizeMB).Msg("Directory volume created")
	return dir, nil
}

// Delete removes the volume directory and all data.
func (d *DirectoryDriver) Delete(ctx context.Context, name string) error {
	if err := d.meta.RemoveVolumeDir(name); err != nil {
		return err
	}
	log.Info().Str("volume", name).Msg("Directory volume deleted")
	return nil
}

// Mount copies volume data into rootfs at target.
func (d *DirectoryDriver) Mount(ctx context.Context, name, rootfsPath, target string) error {
	src := d.dataPath(name)

	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("volume data directory not found: %s", name)
		}
		return err
	}

	targetPath := ensureAbsolutePath(target)
	dest := filepath.Join(rootfsPath, targetPath)

	if err := os.MkdirAll(dest, 0755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}

	if err := copyDirectory(src, dest); err != nil {
		return fmt.Errorf("copy volume data: %w", err)
	}

	// Record last used
	_ = d.meta.TouchLastUsed(ctx, name)

	log.Info().Str("volume", name).Str("target", targetPath).Msg("Directory volume mounted")
	return nil
}

// Unmount copies data back from rootfs to the volume store.
func (d *DirectoryDriver) Unmount(ctx context.Context, name, rootfsPath, target string, readOnly bool) error {
	if readOnly {
		log.Debug().Str("volume", name).Msg("Skipping unmount sync for read-only volume")
		return nil
	}

	targetPath := ensureAbsolutePath(target)
	src := filepath.Join(rootfsPath, targetPath)
	dest := d.dataPath(name)

	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			log.Warn().Str("source", src).Msg("Source path not found, skipping sync")
			return nil
		}
		return err
	}

	if err := os.MkdirAll(dest, 0755); err != nil {
		return fmt.Errorf("ensure volume data dir: %w", err)
	}

	if err := copyDirectory(src, dest); err != nil {
		return fmt.Errorf("sync volume data back: %w", err)
	}

	log.Info().Str("volume", name).Str("target", targetPath).Msg("Directory volume unmounted (data synced)")
	return nil
}

// Stat returns metadata and current disk usage.
func (d *DirectoryDriver) Stat(ctx context.Context, name string) (*VolumeInfo, error) {
	m, err := d.meta.Read(ctx, name)
	if err != nil {
		return nil, err
	}

	used, err := dirSizeMB(d.dataPath(name))
	if err != nil {
		log.Warn().Err(err).Str("volume", name).Msg("Failed to measure usage")
	}

	return &VolumeInfo{
		Name:       m.Name,
		Type:       m.Type,
		Path:       d.meta.volumeDir(name),
		SizeMB:     m.SizeMB,
		CreatedAt:  m.CreatedAt,
		LastUsedAt: m.LastUsedAt,
		TaskID:     m.TaskID,
		UsedMB:     used,
	}, nil
}

// Capacity reports current usage and limit.
func (d *DirectoryDriver) Capacity(ctx context.Context, name string) (int64, int64, error) {
	m, err := d.meta.Read(ctx, name)
	if err != nil {
		return 0, 0, err
	}

	usedBytes, err := dirSizeBytes(d.dataPath(name))
	if err != nil {
		return 0, 0, err
	}

	var limitBytes int64
	if m.SizeMB > 0 {
		limitBytes = int64(m.SizeMB) * 1024 * 1024
	}

	return usedBytes, limitBytes, nil
}

// Snapshot creates a tar.gz archive of the volume data.
func (d *DirectoryDriver) Snapshot(ctx context.Context, name string) (*Snapshot, error) {
	dataDir := d.dataPath(name)
	if _, err := os.Stat(dataDir); err != nil {
		return nil, fmt.Errorf("volume data not found: %s", name)
	}

	snapDir := filepath.Join(d.meta.baseDir, ".snapshots")
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		return nil, fmt.Errorf("create snapshots dir: %w", err)
	}

	snapID := fmt.Sprintf("%s-%d", name, nowUTC().UnixMilli())
	snapPath := filepath.Join(snapDir, snapID+".tar.gz")

	f, err := os.Create(snapPath)
	if err != nil {
		return nil, fmt.Errorf("create snapshot file: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	if err := filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		fh, err := os.Open(path)
		if err != nil {
			return err
		}
		defer fh.Close()

		_, err = io.Copy(tw, fh)
		return err
	}); err != nil {
		return nil, fmt.Errorf("create tar archive: %w", err)
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}

	info, _ := f.Stat()
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

	log.Info().Str("snapshot", snapID).Str("volume", name).Msg("Directory snapshot created")
	return snap, nil
}

// Restore replaces volume data from a snapshot archive.
func (d *DirectoryDriver) Restore(ctx context.Context, name string, snap *Snapshot) error {
	if snap == nil {
		return fmt.Errorf("snapshot cannot be nil")
	}

	dataDir := d.dataPath(name)

	// Clear existing data
	if err := os.RemoveAll(dataDir); err != nil {
		return fmt.Errorf("clear existing data: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("recreate data dir: %w", err)
	}

	f, err := os.Open(snap.Path)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("decompress snapshot: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target := filepath.Join(dataDir, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("create dir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("create file %s: %w", target, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("write file %s: %w", target, err)
			}
			out.Close()
		}
	}

	log.Info().Str("snapshot", snap.ID).Str("volume", name).Msg("Volume restored from snapshot")
	return nil
}

// Export writes a tar.gz stream of volume data to w.
func (d *DirectoryDriver) Export(ctx context.Context, name string, w io.Writer) error {
	dataDir := d.dataPath(name)
	if _, err := os.Stat(dataDir); err != nil {
		return fmt.Errorf("volume data not found: %s", name)
	}

	gw := gzip.NewWriter(w)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel

		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		fh, err := os.Open(path)
		if err != nil {
			return err
		}
		defer fh.Close()
		_, err = io.Copy(tw, fh)
		return err
	})
}

// Import reads a tar.gz stream and populates volume data.
func (d *DirectoryDriver) Import(ctx context.Context, name string, r io.Reader, sizeMB int) error {
	opts := CreateOptions{Type: VolumeTypeDir, SizeMB: sizeMB}
	if _, err := d.Create(ctx, name, opts); err != nil {
		// Volume may already exist, that's ok — we just restore data
		if !os.IsExist(err) {
			return err
		}
	}

	dataDir := d.dataPath(name)
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("decompress import stream: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		target := filepath.Join(dataDir, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("create dir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("create file %s: %w", target, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("write file %s: %w", target, err)
			}
			out.Close()
		}
	}

	log.Info().Str("volume", name).Msg("Volume imported from stream")
	return nil
}

// dataPath returns the path to the data subdirectory for a volume.
func (d *DirectoryDriver) dataPath(name string) string {
	return filepath.Join(d.meta.volumeDir(name), dirDataSubdir)
}

// ensureAbsolutePath prepends "/" if target is not absolute.
func ensureAbsolutePath(target string) string {
	if target == "" {
		return "/"
	}
	if !filepath.IsAbs(target) {
		return "/" + target
	}
	return target
}
