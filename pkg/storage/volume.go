// Package storage provides persistent volume management for SwarmCracker.
// It supports directory-based volumes that persist data across VM restarts.
package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

// VolumeManager manages persistent volumes for Firecracker VMs.
type VolumeManager struct {
	volumesDir string // e.g., /var/lib/swarmcracker/volumes
	mu         sync.Mutex
}

// Volume represents a persistent volume.
type Volume struct {
	ID     string // Unique volume ID (name-based)
	Name   string // Volume name
	Path   string // Host path to the volume directory
	TaskID string // Task that owns this volume
}

// NewVolumeManager creates a new VolumeManager.
func NewVolumeManager(volumesDir string) (*VolumeManager, error) {
	if volumesDir == "" {
		volumesDir = "/var/lib/swarmcracker/volumes"
	}

	// Ensure volumes directory exists
	if err := os.MkdirAll(volumesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create volumes directory: %w", err)
	}

	return &VolumeManager{
		volumesDir: volumesDir,
	}, nil
}

// CreateVolume creates a new persistent volume directory.
// name: Volume name
// taskID: Task ID that owns this volume
// sizeMB: Size in MB (for future use with ext4 images, currently unused for dir-based volumes)
func (vm *VolumeManager) CreateVolume(ctx context.Context, name, taskID string, sizeMB int) (*Volume, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	if name == "" {
		return nil, fmt.Errorf("volume name cannot be empty")
	}

	// Sanitize volume name (remove path separators and special chars)
	sanitizedName := sanitizeVolumeName(name)
	volumePath := filepath.Join(vm.volumesDir, sanitizedName)

	log.Info().
		Str("volume", name).
		Str("task_id", taskID).
		Str("path", volumePath).
		Msg("Creating volume")

	// Create volume directory
	if err := os.MkdirAll(volumePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create volume directory: %w", err)
	}

	// Create metadata file to track ownership
	metadataPath := filepath.Join(volumePath, ".swarmcracker_volume_meta")
	metadata := fmt.Sprintf("name=%s\ntask_id=%s\n", name, taskID)
	if err := os.WriteFile(metadataPath, []byte(metadata), 0644); err != nil {
		log.Warn().Err(err).Msg("Failed to write volume metadata (non-critical)")
	}

	volume := &Volume{
		ID:     sanitizedName,
		Name:   name,
		Path:   volumePath,
		TaskID: taskID,
	}

	log.Info().
		Str("volume", name).
		Str("path", volumePath).
		Msg("Volume created successfully")

	return volume, nil
}

// GetVolume retrieves an existing volume by name.
func (vm *VolumeManager) GetVolume(name string) (*Volume, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	sanitizedName := sanitizeVolumeName(name)
	volumePath := filepath.Join(vm.volumesDir, sanitizedName)

	// Check if volume exists
	if _, err := os.Stat(volumePath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("volume not found: %s", name)
		}
		return nil, fmt.Errorf("failed to access volume: %w", err)
	}

	return &Volume{
		ID:   sanitizedName,
		Name: name,
		Path: volumePath,
	}, nil
}

// MountVolume copies volume contents into the rootfs at the target path.
// This is called during rootfs preparation before the VM starts.
//
// vol: The volume to mount
// rootfsPath: Path to the mounted rootfs directory
// target: Target path inside the container (e.g., "/data")
func (vm *VolumeManager) MountVolume(ctx context.Context, vol *Volume, rootfsPath, target string) error {
	if vol == nil {
		return fmt.Errorf("volume cannot be nil")
	}

	if target == "" {
		return fmt.Errorf("target path cannot be empty")
	}

	// Ensure target path is absolute
	if !strings.HasPrefix(target, "/") {
		target = "/" + target
	}

	log.Info().
		Str("volume", vol.Name).
		Str("rootfs", rootfsPath).
		Str("target", target).
		Msg("Mounting volume")

	// Create target directory in rootfs
	targetPath := filepath.Join(rootfsPath, target)
	if err := os.MkdirAll(targetPath, 0755); err != nil {
		return fmt.Errorf("failed to create target directory: %w", err)
	}

	// Copy volume contents to rootfs
	if err := copyDirectory(vol.Path, targetPath); err != nil {
		return fmt.Errorf("failed to copy volume contents: %w", err)
	}

	log.Info().
		Str("volume", vol.Name).
		Str("target", target).
		Msg("Volume mounted successfully")

	return nil
}

// UnmountVolume copies changes from rootfs back to the volume directory.
// This preserves data between VM restarts for ReadWrite mounts.
//
// vol: The volume to unmount
// rootfsPath: Path to the mounted rootfs directory
// target: Target path inside the container (e.g., "/data")
// readOnly: If true, skip syncing data back (ReadOnly mounts)
func (vm *VolumeManager) UnmountVolume(ctx context.Context, vol *Volume, rootfsPath, target string, readOnly bool) error {
	if vol == nil {
		return fmt.Errorf("volume cannot be nil")
	}

	if readOnly {
		log.Info().
			Str("volume", vol.Name).
			Msg("Skipping unmount for read-only volume")
		return nil
	}

	if target == "" {
		return fmt.Errorf("target path cannot be empty")
	}

	// Ensure target path is absolute
	if !strings.HasPrefix(target, "/") {
		target = "/" + target
	}

	log.Info().
		Str("volume", vol.Name).
		Str("rootfs", rootfsPath).
		Str("target", target).
		Msg("Unmounting volume (syncing data back)")

	// Source path in rootfs
	sourcePath := filepath.Join(rootfsPath, target)

	// Check if source exists
	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			log.Warn().
				Str("source", sourcePath).
				Msg("Source path does not exist, skipping sync")
			return nil
		}
		return fmt.Errorf("failed to access source path: %w", err)
	}

	// Sync changes back to volume directory
	if err := copyDirectory(sourcePath, vol.Path); err != nil {
		return fmt.Errorf("failed to sync volume data back: %w", err)
	}

	log.Info().
		Str("volume", vol.Name).
		Str("target", target).
		Msg("Volume unmounted successfully (data synced)")

	return nil
}

// DeleteVolume removes a volume and all its data.
func (vm *VolumeManager) DeleteVolume(ctx context.Context, name string) error {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	sanitizedName := sanitizeVolumeName(name)
	volumePath := filepath.Join(vm.volumesDir, sanitizedName)

	log.Info().
		Str("volume", name).
		Str("path", volumePath).
		Msg("Deleting volume")

	// Remove volume directory and all contents
	if err := os.RemoveAll(volumePath); err != nil {
		return fmt.Errorf("failed to delete volume: %w", err)
	}

	log.Info().
		Str("volume", name).
		Msg("Volume deleted successfully")

	return nil
}

// ListVolumes returns all volumes in the volumes directory.
func (vm *VolumeManager) ListVolumes(ctx context.Context) ([]*Volume, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	entries, err := os.ReadDir(vm.volumesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Volume{}, nil
		}
		return nil, fmt.Errorf("failed to read volumes directory: %w", err)
	}

	var volumes []*Volume
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		volumePath := filepath.Join(vm.volumesDir, entry.Name())
		volumes = append(volumes, &Volume{
			ID:   entry.Name(),
			Name: entry.Name(),
			Path: volumePath,
		})
	}

	return volumes, nil
}

// IsVolumeReference checks if a mount source is a volume reference.
// Volume references start with "volume://" or are simple names.
func IsVolumeReference(source string) bool {
	if strings.HasPrefix(source, "volume://") {
		return true
	}

	// If source doesn't look like a path (no /), it might be a volume name
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
	// Replace path separators and special chars with underscores
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "..", "_")
	name = strings.ReplaceAll(name, ":", "_")

	// Remove leading/trailing dots and spaces
	name = strings.Trim(name, ". ")

	// Ensure non-empty
	if name == "" {
		name = "unnamed"
	}

	return name
}

// copyDirectory copies a directory recursively from src to dst.
func copyDirectory(src, dst string) error {
	// Ensure source exists
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	// Create destination directory
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	// Read source directory
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	// Copy each entry
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			// Recursively copy subdirectory
			if err := copyDirectory(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			// Copy file
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// copyFile copies a single file from src to dst.
func copyFile(src, dst string) error {
	// Read source file
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	// Get file mode
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	// Write destination file
	return os.WriteFile(dst, data, info.Mode())
}
