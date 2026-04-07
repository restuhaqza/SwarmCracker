package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Volume represents a handle to a storage volume.
type Volume struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	TaskID string `json:"task_id"`
}

// VolumeManager dispatches volume operations to the appropriate driver.
type VolumeManager struct {
	drivers     map[VolumeType]VolumeDriver
	defaultType VolumeType
	mu          sync.RWMutex
}

// NewVolumeManager creates a VolumeManager with dir and block drivers.
func NewVolumeManager(volumesDir string) (*VolumeManager, error) {
	if volumesDir == "" {
		volumesDir = "/var/lib/swarmcracker/volumes"
	}

	dirDriver, err := NewDirectoryDriver(volumesDir)
	if err != nil {
		return nil, fmt.Errorf("create directory driver: %w", err)
	}

	vmm := &VolumeManager{
		drivers:     make(map[VolumeType]VolumeDriver),
		defaultType: VolumeTypeDir,
	}
	vmm.drivers[VolumeTypeDir] = dirDriver

	blockDriver, err := NewBlockDriver(volumesDir)
	if err != nil {
		log.Warn().Err(err).Msg("Block driver unavailable (may require root)")
	} else {
		vmm.drivers[VolumeTypeBlock] = blockDriver
	}

	return vmm, nil
}

func (vm *VolumeManager) getDriver(t VolumeType) (VolumeDriver, error) {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	if t == "" {
		t = vm.defaultType
	}
	d, ok := vm.drivers[t]
	if !ok {
		return nil, fmt.Errorf("no driver registered for type %q", t)
	}
	return d, nil
}

func (vm *VolumeManager) getDriverMeta(t VolumeType) (*MetaStore, error) {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	if t == "" {
		t = vm.defaultType
	}
	d, ok := vm.drivers[t]
	if !ok {
		return nil, fmt.Errorf("no driver for type %q", t)
	}
	switch dd := d.(type) {
	case *DirectoryDriver:
		return dd.meta, nil
	case *BlockDriver:
		return dd.meta, nil
	default:
		return nil, fmt.Errorf("unknown driver type")
	}
}

// CreateVolume creates a volume (legacy API — defaults to dir type).
func (vm *VolumeManager) CreateVolume(ctx context.Context, name, taskID string, sizeMB int) (*Volume, error) {
	return vm.CreateVolumeWithOptions(ctx, name, taskID, CreateOptions{
		Type:   vm.defaultType,
		SizeMB: sizeMB,
	})
}

// CreateVolumeWithOptions creates a volume with full options.
func (vm *VolumeManager) CreateVolumeWithOptions(ctx context.Context, name, taskID string, opts CreateOptions) (*Volume, error) {
	if name == "" {
		return nil, fmt.Errorf("volume name cannot be empty")
	}

	d, err := vm.getDriver(opts.Type)
	if err != nil {
		return nil, err
	}

	path, err := d.Create(ctx, name, opts)
	if err != nil {
		return nil, err
	}

	// Update task association
	if meta, err := vm.getDriverMeta(opts.Type); err == nil {
		_ = meta.UpdateTaskID(ctx, name, taskID)
	}

	vol := &Volume{
		ID:     sanitizeVolumeName(name),
		Name:   name,
		Path:   path,
		TaskID: taskID,
	}

	log.Info().
		Str("volume", name).
		Str("type", string(opts.Type)).
		Str("path", path).
		Int("size_mb", opts.SizeMB).
		Str("task_id", taskID).
		Msg("Volume created")

	return vol, nil
}

// GetVolume retrieves a volume handle by name.
func (vm *VolumeManager) GetVolume(name string) (*Volume, error) {
	for _, t := range []VolumeType{VolumeTypeDir, VolumeTypeBlock} {
		d, err := vm.getDriver(t)
		if err != nil {
			continue
		}
		info, err := d.Stat(context.Background(), name)
		if err == nil {
			return &Volume{
				ID:   sanitizeVolumeName(name),
				Name: name,
				Path: info.Path,
			}, nil
		}
	}
	return nil, fmt.Errorf("volume not found: %s", name)
}

// GetVolumeInfo returns detailed info about a volume.
func (vm *VolumeManager) GetVolumeInfo(ctx context.Context, name string, volType VolumeType) (*VolumeInfo, error) {
	d, err := vm.getDriver(volType)
	if err != nil {
		return nil, err
	}
	return d.Stat(ctx, name)
}

// MountVolume copies volume data into rootfs at target (legacy API).
func (vm *VolumeManager) MountVolume(ctx context.Context, vol *Volume, rootfsPath, target string) error {
	if vol == nil {
		return fmt.Errorf("volume cannot be nil")
	}
	for _, t := range []VolumeType{VolumeTypeDir, VolumeTypeBlock} {
		d, err := vm.getDriver(t)
		if err != nil {
			continue
		}
		if _, statErr := d.Stat(ctx, vol.Name); statErr == nil {
			return d.Mount(ctx, vol.Name, rootfsPath, target)
		}
	}
	return fmt.Errorf("volume not found in any driver: %s", vol.Name)
}

// UnmountVolume syncs data back from rootfs to volume (legacy API).
func (vm *VolumeManager) UnmountVolume(ctx context.Context, vol *Volume, rootfsPath, target string, readOnly bool) error {
	if vol == nil {
		return fmt.Errorf("volume cannot be nil")
	}
	for _, t := range []VolumeType{VolumeTypeDir, VolumeTypeBlock} {
		d, err := vm.getDriver(t)
		if err != nil {
			continue
		}
		if _, statErr := d.Stat(ctx, vol.Name); statErr == nil {
			return d.Unmount(ctx, vol.Name, rootfsPath, target, readOnly)
		}
	}
	return fmt.Errorf("volume not found in any driver: %s", vol.Name)
}

// DeleteVolume removes a volume.
func (vm *VolumeManager) DeleteVolume(ctx context.Context, name string) error {
	for _, t := range []VolumeType{VolumeTypeDir, VolumeTypeBlock} {
		d, err := vm.getDriver(t)
		if err != nil {
			continue
		}
		if _, statErr := d.Stat(ctx, name); statErr == nil {
			return d.Delete(ctx, name)
		}
	}
	return fmt.Errorf("volume not found: %s", name)
}

// ListVolumes returns all volumes (legacy API).
func (vm *VolumeManager) ListVolumes(ctx context.Context) ([]*Volume, error) {
	var volumes []*Volume
	for _, t := range []VolumeType{VolumeTypeDir, VolumeTypeBlock} {
		d, err := vm.getDriver(t)
		if err != nil {
			continue
		}
		dd, ok := d.(*DirectoryDriver)
		if !ok {
			continue
		}
		metas, err := dd.meta.List(ctx)
		if err != nil {
			continue
		}
		for _, m := range metas {
			volumes = append(volumes, &Volume{
				ID:     sanitizeVolumeName(m.Name),
				Name:   m.Name,
				TaskID: m.TaskID,
			})
		}
	}
	return volumes, nil
}

// ListVolumeInfos returns detailed info for all volumes.
func (vm *VolumeManager) ListVolumeInfos(ctx context.Context) ([]*VolumeInfo, error) {
	var infos []*VolumeInfo
	for _, t := range []VolumeType{VolumeTypeDir, VolumeTypeBlock} {
		d, err := vm.getDriver(t)
		if err != nil {
			continue
		}
		dd, ok := d.(*DirectoryDriver)
		if !ok {
			continue
		}
		metas, err := dd.meta.List(ctx)
		if err != nil {
			continue
		}
		for _, m := range metas {
			usedMB, _ := dirSizeMB(dd.dataPath(m.Name))
			infos = append(infos, &VolumeInfo{
				Name:       m.Name,
				Type:       m.Type,
				SizeMB:     m.SizeMB,
				CreatedAt:  m.CreatedAt,
				LastUsedAt: m.LastUsedAt,
				TaskID:     m.TaskID,
				UsedMB:     usedMB,
			})
		}
	}
	return infos, nil
}

// SnapshotVolume creates a point-in-time snapshot.
func (vm *VolumeManager) SnapshotVolume(ctx context.Context, name string) (*Snapshot, error) {
	for _, t := range []VolumeType{VolumeTypeDir, VolumeTypeBlock} {
		d, err := vm.getDriver(t)
		if err != nil {
			continue
		}
		if _, statErr := d.Stat(ctx, name); statErr == nil {
			return d.Snapshot(ctx, name)
		}
	}
	return nil, fmt.Errorf("volume not found: %s", name)
}

// RestoreVolume restores a volume from a snapshot.
func (vm *VolumeManager) RestoreVolume(ctx context.Context, name string, snap *Snapshot) error {
	for _, t := range []VolumeType{VolumeTypeDir, VolumeTypeBlock} {
		d, err := vm.getDriver(t)
		if err != nil {
			continue
		}
		if _, statErr := d.Stat(ctx, name); statErr == nil {
			return d.Restore(ctx, name, snap)
		}
	}
	return fmt.Errorf("volume not found: %s", name)
}

// GetDriver returns a raw driver for direct access.
func (vm *VolumeManager) GetDriver(t VolumeType) (VolumeDriver, error) {
	return vm.getDriver(t)
}

// --- Volume reference helpers ---

// IsVolumeReference checks if a mount source is a volume reference.
func IsVolumeReference(source string) bool {
	if strings.HasPrefix(source, "volume://") {
		return true
	}
	return !strings.Contains(source, "/") && !strings.HasPrefix(source, ".")
}

// ExtractVolumeName extracts the volume name from a volume reference.
func ExtractVolumeName(source string) string {
	if strings.HasPrefix(source, "volume://") {
		return strings.TrimPrefix(source, "volume://")
	}
	return source
}

// sanitizeVolumeName sanitizes a volume name for use as a directory name.
func sanitizeVolumeName(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "..", "_")
	name = strings.ReplaceAll(name, ":", "_")
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "unnamed"
	}
	return name
}

// nowUTC returns the current time in UTC.
func nowUTC() time.Time {
	return time.Now().UTC()
}

// --- File operation helpers ---

// dirSizeBytes returns the total size of all files in a directory.
func dirSizeBytes(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// dirSizeMB returns directory size in MB.
func dirSizeMB(path string) (int64, error) {
	b, err := dirSizeBytes(path)
	if err != nil {
		return 0, err
	}
	return b / (1024 * 1024), nil
}

// copyDirectory copies a directory recursively from src to dst.
func copyDirectory(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDirectory(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile copies a single file from src to dst.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode())
}
