// Package image prepares OCI images as root filesystems for Firecracker VMs.
package image

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/storage"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// ImagePreparer prepares OCI images as root filesystems.
type ImagePreparer struct {
	config        *PreparerConfig
	cacheDir      string
	rootfsDir     string
	initInjector  *InitInjector
	volumeManager *storage.VolumeManager
	secretManager *storage.SecretManager
}

// PreparerConfig holds image preparer configuration.
type PreparerConfig struct {
	KernelPath      string
	RootfsDir       string
	SocketDir       string
	DefaultVCPUs    int
	DefaultMemoryMB int
	InitSystem      string `yaml:"init_system"`       // "none", "tini", "dumb-init"
	InitGracePeriod int    `yaml:"init_grace_period"` // Grace period in seconds
	MaxImageAgeDays int    `yaml:"max_image_age_days"` // Maximum age of rootfs images before cleanup (default 7)
}

// NewImagePreparer creates a new ImagePreparer.
func NewImagePreparer(config interface{}) types.ImagePreparer {
	var cfg *PreparerConfig
	if c, ok := config.(*PreparerConfig); ok {
		cfg = c
	} else {
		cfg = &PreparerConfig{
			RootfsDir:       "/var/lib/firecracker/rootfs",
			InitSystem:      "tini",
			InitGracePeriod: 10,
		}
	}

	// Set defaults
	if cfg.InitSystem == "" {
		cfg.InitSystem = "tini"
	}
	if cfg.InitGracePeriod == 0 {
		cfg.InitGracePeriod = 10
	}
	if cfg.MaxImageAgeDays == 0 {
		cfg.MaxImageAgeDays = 7
	}

	// Ensure rootfs directory exists
	os.MkdirAll(cfg.RootfsDir, 0755)

	// Create init injector
	initConfig := &InitSystemConfig{
		Type:           InitSystemType(cfg.InitSystem),
		GracePeriodSec: cfg.InitGracePeriod,
	}
	initInjector := NewInitInjector(initConfig)

	// Create volume manager
	volumeMgr, err := storage.NewVolumeManager("")
	if err != nil {
		log.Warn().Err(err).Msg("Failed to create volume manager, volume support disabled")
	}

	return &ImagePreparer{
		config:        cfg,
		cacheDir:      "/var/cache/swarmcracker",
		rootfsDir:     cfg.RootfsDir,
		initInjector:  initInjector,
		volumeManager: volumeMgr,
	}
}

// Prepare prepares an OCI image for the given task.
func (ip *ImagePreparer) Prepare(ctx context.Context, task *types.Task) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}

	if task.Spec.Runtime == nil {
		return fmt.Errorf("task runtime cannot be nil")
	}

	container, ok := task.Spec.Runtime.(*types.Container)
	if !ok {
		return fmt.Errorf("task runtime is not a container")
	}

	// Initialize annotations map if nil
	if task.Annotations == nil {
		task.Annotations = make(map[string]string)
	}

	log.Info().
		Str("task_id", task.ID).
		Str("image", container.Image).
		Msg("Preparing container image")

	// Validate architecture support
	if err := ip.validateArchitecture(); err != nil {
		return fmt.Errorf("architecture validation failed: %w", err)
	}

	// Generate image ID
	imageID := generateImageID(container.Image)
	rootfsPath := filepath.Join(ip.rootfsDir, imageID+".ext4")

	// Check if rootfs already exists
	if _, err := os.Stat(rootfsPath); err == nil {
		log.Info().
			Str("path", rootfsPath).
			Msg("Rootfs already exists, skipping")
		task.Annotations["rootfs"] = rootfsPath
		return nil
	}

	// Prepare the image
	if err := ip.prepareImage(ctx, container.Image, imageID, rootfsPath); err != nil {
		return fmt.Errorf("failed to prepare image: %w", err)
	}

	// Inject init system if enabled
	if ip.initInjector.IsEnabled() {
		log.Info().
			Str("task_id", task.ID).
			Str("init_system", string(ip.initInjector.config.Type)).
			Msg("Injecting init system")

		if err := ip.injectInitSystem(rootfsPath); err != nil {
			return fmt.Errorf("failed to inject init system: %w", err)
		}

		// Store init system type in annotations
		task.Annotations["init_system"] = string(ip.initInjector.config.Type)
		task.Annotations["init_path"] = ip.initInjector.GetInitPath()
	}

	// Handle mounts if volume manager is available
	if ip.volumeManager != nil && len(container.Mounts) > 0 {
		log.Info().
			Str("task_id", task.ID).
			Int("mount_count", len(container.Mounts)).
			Msg("Processing mounts")

		if err := ip.handleMounts(ctx, task, rootfsPath, container.Mounts); err != nil {
			return fmt.Errorf("failed to handle mounts: %w", err)
		}
	}

	// Store rootfs path in task annotations
	task.Annotations["rootfs"] = rootfsPath

	log.Info().
		Str("task_id", task.ID).
		Str("rootfs", rootfsPath).
		Msg("Image preparation completed")

	return nil
}

// prepareImage prepares an OCI image and converts to ext4 filesystem.
func (ip *ImagePreparer) prepareImage(ctx context.Context, imageRef, imageID, outputPath string) error {
	// Create temporary directory for extraction
	tmpDir, err := os.MkdirTemp("", "swarmcracker-extract-")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Step 1: Pull and extract OCI image
	log.Debug().Str("image", imageRef).Msg("Pulling OCI image")
	if err := ip.extractOCIImage(ctx, imageRef, tmpDir); err != nil {
		return fmt.Errorf("failed to extract OCI image: %w", err)
	}

	// Step 1.5: Inject network configuration for Alpine/OpenRC systems
	if err := ip.injectNetworkConfig(tmpDir); err != nil {
		log.Warn().Err(err).Msg("Failed to inject network config (may still work if image has it)")
	}

	// Step 2: Create ext4 filesystem image
	log.Debug().Str("output", outputPath).Msg("Creating ext4 filesystem")
	if err := ip.createExt4Image(tmpDir, outputPath); err != nil {
		return fmt.Errorf("failed to create ext4 image: %w", err)
	}

	return nil
}

// extractOCIImage extracts an OCI image using containerd/podman/docker.
func (ip *ImagePreparer) extractOCIImage(ctx context.Context, imageRef, destPath string) error {
	// Try different methods in order
	methods := []struct {
		name string
		fn   func(context.Context, string, string, string) (string, error)
	}{
		{
			name: "docker",
			fn:   ip.extractWithDocker,
		},
		{
			name: "podman",
			fn:   ip.extractWithPodman,
		},
	}

	for _, method := range methods {
		if _, err := exec.LookPath(method.name); err != nil {
			continue
		}

		log.Debug().Str("using", method.name).Msg("Extracting image")

		containerID, err := method.fn(ctx, method.name, imageRef, destPath)
		if err != nil {
			log.Debug().Str("method", method.name).Err(err).Msg("Extraction failed, trying next method")
			continue
		}

		// Cleanup container
		exec.Command(method.name, "rm", "-f", containerID).Run()

		// Extract tar
		tarPath := filepath.Join(destPath, "fs.tar")
		tarCmd := exec.CommandContext(ctx, "tar", "xf", tarPath, "-C", destPath)
		if err := tarCmd.Run(); err != nil {
			return fmt.Errorf("failed to extract tar: %w", err)
		}

		// Remove tar file
		os.Remove(tarPath)

		return nil
	}

	return fmt.Errorf("no container runtime found (docker or podman required)")
}

// extractWithDocker extracts an image using Docker (without --root flag)
func (ip *ImagePreparer) extractWithDocker(ctx context.Context, runtime, imageRef, destPath string) (string, error) {
	// Create container (Docker doesn't support --root)
	output, err := exec.CommandContext(ctx, runtime, "create", imageRef, "/bin/true").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker create failed: %s: %w", string(output), err)
	}

	containerID := strings.TrimSpace(string(output))

	// Export container filesystem to tar
	tarPath := filepath.Join(destPath, "fs.tar")
	exportCmd := exec.CommandContext(ctx, runtime, "export", containerID, "-o", tarPath)
	if output, err := exportCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker export failed: %s: %w", string(output), err)
	}

	return containerID, nil
}

// extractWithPodman extracts an image using Podman
// Note: We use podman's default storage for pulling images, only extracting the container filesystem to destPath
func (ip *ImagePreparer) extractWithPodman(ctx context.Context, runtime, imageRef, destPath string) (string, error) {
	// Create container (using podman's default storage)
	output, err := exec.CommandContext(ctx, runtime, "create", imageRef, "/bin/true").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("podman create failed: %s: %w", string(output), err)
	}

	containerID := strings.TrimSpace(string(output))

	// Export container filesystem to tar
	tarPath := filepath.Join(destPath, "fs.tar")
	exportCmd := exec.CommandContext(ctx, runtime, "export", containerID, "-o", tarPath)
	if output, err := exportCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("podman export failed: %s: %w", string(output), err)
	}

	return containerID, nil
}

// createExt4Image creates an ext4 filesystem from a directory.
func (ip *ImagePreparer) createExt4Image(sourceDir, outputPath string) error {
	// Check if mkfs.ext4 is available
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		return fmt.Errorf("mkfs.ext4 not found: %w", err)
	}

	// Calculate size (estimate based on directory size)
	size, err := getDirSize(sourceDir)
	if err != nil {
		size = 512 * 1024 * 1024 // Default 512MB
	}

	// Add 50% buffer to account for filesystem overhead
	size = size + (size / 2)

	// Create sparse file
	sizeMB := size / (1024 * 1024)
	if sizeMB < 100 {
		sizeMB = 100 // Minimum 100MB
	}

	// Create empty file
	if err := exec.Command("truncate", "-s", fmt.Sprintf("%dM", sizeMB), outputPath).Run(); err != nil {
		return fmt.Errorf("failed to create image file: %w", err)
	}

	// Format as ext4
	mkfsCmd := exec.Command("mkfs.ext4", "-d", sourceDir, outputPath)
	if output, err := mkfsCmd.CombinedOutput(); err != nil {
		os.Remove(outputPath)
		return fmt.Errorf("mkfs.ext4 failed: %s: %w", string(output), err)
	}

	return nil
}

// getDirSize calculates the total size of a directory.
func getDirSize(path string) (int64, error) {
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

// generateImageID generates a unique ID for an image.
func generateImageID(imageRef string) string {
	// Split on last colon to separate tag from image name
	// This handles registry:port/image:tag correctly
	lastColon := strings.LastIndex(imageRef, ":")
	var name, tag string
	if lastColon > 0 {
		name = imageRef[:lastColon]
		tag = imageRef[lastColon+1:]
	} else {
		name = imageRef
		tag = "latest"
	}

	// Replace slashes with dashes for filesystem-safe names
	name = strings.ReplaceAll(name, "/", "-")

	return fmt.Sprintf("%s-%s", name, tag)
}

// injectInitSystem injects the init system into the rootfs.
func (ip *ImagePreparer) injectInitSystem(rootfsPath string) error {
	// Use the init injector to add init binary to rootfs
	if err := ip.initInjector.Inject(rootfsPath); err != nil {
		return fmt.Errorf("init injection failed: %w", err)
	}

	// For ext4 images, we need to mount, copy, unmount
	// This is a simplified implementation
	mountDir, err := ip.mountExt4(rootfsPath)
	if err != nil {
		log.Debug().Err(err).Msg("Could not mount rootfs for init injection (may require privileges)")
		// Continue anyway - init might already be present
		return nil
	}
	defer ip.unmountExt4(mountDir)

	// Copy init binary
	initBinaryPath := ip.getInitBinaryPath()
	if initBinaryPath == "" {
		// No init binary to copy
		return nil
	}

	targetPath := filepath.Join(mountDir, ip.initInjector.GetInitPath())
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("failed to create init directory: %w", err)
	}

	// Copy binary
	if err := ip.copyFile(initBinaryPath, targetPath, 0755); err != nil {
		return fmt.Errorf("failed to copy init binary: %w", err)
	}

	log.Info().
		Str("from", initBinaryPath).
		Str("to", targetPath).
		Msg("Init binary copied")

	return nil
}

// mountExt4 mounts an ext4 image temporarily.
func (ip *ImagePreparer) mountExt4(imagePath string) (string, error) {
	// Create temp mount point
	mountDir, err := os.MkdirTemp("", "swarmcracker-mount-")
	if err != nil {
		return "", err
	}

	// Try to mount the image
	// This requires root privileges or user namespace setup
	// For non-root, we'll skip this step
	cmd := exec.Command("mount", "-o", "loop", imagePath, mountDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(mountDir)
		return "", fmt.Errorf("mount failed: %s: %w", string(output), err)
	}

	return mountDir, nil
}

// unmountExt4 unmounts a temporary mount point.
func (ip *ImagePreparer) unmountExt4(mountDir string) error {
	// Unmount
	cmd := exec.Command("umount", mountDir)
	_ = cmd.Run() // Ignore errors

	// Cleanup temp dir
	os.RemoveAll(mountDir)
	return nil
}

// getInitBinaryPath returns the path to the init binary on the host.
func (ip *ImagePreparer) getInitBinaryPath() string {
	// Search for init binaries in common locations
	paths := []string{
		"/usr/bin/tini",
		"/usr/sbin/tini",
		"/sbin/tini",
		"/usr/bin/dumb-init",
		"/usr/sbin/dumb-init",
		"/sbin/dumb-init",
	}

	switch ip.initInjector.config.Type {
	case InitSystemTini:
		paths = []string{"/usr/bin/tini", "/usr/sbin/tini", "/sbin/tini"}
	case InitSystemDumbInit:
		paths = []string{"/usr/bin/dumb-init", "/usr/sbin/dumb-init", "/sbin/dumb-init"}
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// Check if binary is in PATH
	cmd := exec.Command("which", string(ip.initInjector.config.Type))
	if output, err := cmd.CombinedOutput(); err == nil {
		return strings.TrimSpace(string(output))
	}

	return ""
}

// copyFile copies a file from src to dst.
func (ip *ImagePreparer) copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	return os.WriteFile(dst, data, mode)
}

// handleMounts processes mount specifications and applies them to the rootfs.
func (ip *ImagePreparer) handleMounts(ctx context.Context, task *types.Task, rootfsPath string, mounts []types.Mount) error {
	// Temporarily mount the rootfs to apply mounts
	mountDir, err := ip.mountExt4(rootfsPath)
	if err != nil {
		log.Warn().Err(err).Msg("Could not mount rootfs for mount handling (may require privileges)")
		// Continue without mounts - non-critical
		return nil
	}
	defer ip.unmountExt4(mountDir)

	for _, mount := range mounts {
		if mount.Target == "" {
			log.Warn().Msg("Skipping mount with empty target")
			continue
		}

		log.Debug().
			Str("source", mount.Source).
			Str("target", mount.Target).
			Bool("readonly", mount.ReadOnly).
			Msg("Processing mount")

		// Check if this is a volume reference
		if storage.IsVolumeReference(mount.Source) {
			// Handle volume mount
			if err := ip.handleVolumeMount(ctx, task, mountDir, &mount); err != nil {
				log.Error().Err(err).
					Str("source", mount.Source).
					Str("target", mount.Target).
					Msg("Failed to handle volume mount")
				// Continue with other mounts
				continue
			}
		} else {
			// Handle host path bind mount
			if err := ip.handleBindMount(mountDir, &mount); err != nil {
				log.Error().Err(err).
					Str("source", mount.Source).
					Str("target", mount.Target).
					Msg("Failed to handle bind mount")
				// Continue with other mounts
				continue
			}
		}
	}

	return nil
}

// handleVolumeMount handles a volume mount.
func (ip *ImagePreparer) handleVolumeMount(ctx context.Context, task *types.Task, rootfsPath string, mount *types.Mount) error {
	// Extract volume name
	volumeName := storage.ExtractVolumeName(mount.Source)

	log.Info().
		Str("volume", volumeName).
		Str("target", mount.Target).
		Msg("Handling volume mount")

	// Get or create volume
	vol, err := ip.volumeManager.GetVolume(volumeName)
	if err != nil {
		// Volume doesn't exist, create it
		log.Info().
			Str("volume", volumeName).
			Msg("Volume does not exist, creating new volume")

		vol, err = ip.volumeManager.CreateVolume(ctx, volumeName, task.ID, 0)
		if err != nil {
			return fmt.Errorf("failed to create volume: %w", err)
		}
	}

	// Mount volume into rootfs
	if err := ip.volumeManager.MountVolume(ctx, vol, rootfsPath, mount.Target); err != nil {
		return fmt.Errorf("failed to mount volume: %w", err)
	}

	log.Info().
		Str("volume", volumeName).
		Str("target", mount.Target).
		Msg("Volume mounted successfully")

	return nil
}

// handleBindMount handles a host path bind mount.
func (ip *ImagePreparer) handleBindMount(rootfsPath string, mount *types.Mount) error {
	// Validate source path exists
	if _, err := os.Stat(mount.Source); err != nil {
		if os.IsNotExist(err) {
			log.Warn().
				Str("source", mount.Source).
				Msg("Bind mount source does not exist, skipping")
			return nil
		}
		return fmt.Errorf("failed to access source: %w", err)
	}

	// Create target directory in rootfs
	targetPath := filepath.Join(rootfsPath, mount.Target)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("failed to create target directory: %w", err)
	}

	// For directories, copy contents; for files, copy the file
	sourceInfo, err := os.Stat(mount.Source)
	if err != nil {
		return fmt.Errorf("failed to stat source: %w", err)
	}

	if sourceInfo.IsDir() {
		// Copy directory contents
		if err := copyDirectory(mount.Source, targetPath); err != nil {
			return fmt.Errorf("failed to copy directory: %w", err)
		}
	} else {
		// Copy file
		if err := ip.copyFile(mount.Source, targetPath, sourceInfo.Mode()); err != nil {
			return fmt.Errorf("failed to copy file: %w", err)
		}
	}

	log.Debug().
		Str("source", mount.Source).
		Str("target", mount.Target).
		Msg("Bind mount applied")

	return nil
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
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0644); err != nil {
				return err
			}
		}
	}

	return nil
}

// Cleanup removes old unused rootfs images.
// Returns the number of files removed and bytes freed.
func (ip *ImagePreparer) Cleanup(ctx context.Context, keepDays int) (filesRemoved int, bytesFreed int64, err error) {
	log.Info().Int("keep_days", keepDays).Msg("Cleaning up old images")

	if keepDays <= 0 {
		return 0, 0, fmt.Errorf("keepDays must be positive")
	}

	// Check if rootfs directory exists
	if _, statErr := os.Stat(ip.rootfsDir); os.IsNotExist(statErr) {
		log.Debug().Str("dir", ip.rootfsDir).Msg("Rootfs directory does not exist, nothing to clean")
		return 0, 0, nil
	}

	// Calculate cutoff time
	cutoffTime := time.Now().AddDate(0, 0, -keepDays)
	// Safe heuristic: skip files accessed in last 24h (likely in use)
	recentAccessThreshold := time.Now().Add(-24 * time.Hour)

	// Scan directory
	entries, readErr := os.ReadDir(ip.rootfsDir)
	if readErr != nil {
		return 0, 0, fmt.Errorf("failed to read rootfs directory: %w", readErr)
	}

	log.Debug().Int("total_files", len(entries)).Msg("Scanning rootfs directory")

	for _, entry := range entries {
		// Skip directories
		if entry.IsDir() {
			continue
		}

		// Only process .ext4 files
		if !strings.HasSuffix(entry.Name(), ".ext4") {
			continue
		}

		filePath := filepath.Join(ip.rootfsDir, entry.Name())

		// Get file info
		fileInfo, statErr := os.Stat(filePath)
		if statErr != nil {
			log.Warn().Str("file", filePath).Err(statErr).Msg("Failed to stat file, skipping")
			continue
		}

		// Check if file was recently accessed (safe heuristic for in-use files)
		if fileInfo.ModTime().After(recentAccessThreshold) {
			log.Debug().Str("file", filePath).Time("mod_time", fileInfo.ModTime()).Msg("Skipping recently accessed file")
			continue
		}

		// Check if file is old enough to delete
		if fileInfo.ModTime().Before(cutoffTime) {
			log.Info().Str("file", filePath).
				Time("mod_time", fileInfo.ModTime()).
				Int64("size_bytes", fileInfo.Size()).
				Msg("Removing old rootfs image")

			// Remove the file
			if removeErr := os.Remove(filePath); removeErr != nil {
				log.Error().Str("file", filePath).Err(removeErr).Msg("Failed to remove file")
				// Continue with other files instead of failing completely
				continue
			}

			filesRemoved++
			bytesFreed += fileInfo.Size()
		}
	}

	if filesRemoved > 0 {
		log.Info().
			Int("files_removed", filesRemoved).
			Int64("bytes_freed", bytesFreed).
			Str("space_freed", formatBytes(bytesFreed)).
			Msg("Cleanup completed")
	} else {
		log.Info().Msg("No old images to remove")
	}

	return filesRemoved, bytesFreed, nil
}

// formatBytes formats a byte count into a human-readable string.
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// injectNetworkConfig adds network initialization scripts to the rootfs.
// This is needed for Alpine/OpenRC systems that don't have network init by default.
func (ip *ImagePreparer) injectNetworkConfig(rootfsDir string) error {
	// Check if this is an OpenRC-based system (Alpine)
	initTabPath := filepath.Join(rootfsDir, "etc/inittab")
	if _, err := os.Stat(initTabPath); err != nil {
		// No inittab, probably not OpenRC
		return nil
	}

	initTab, err := os.ReadFile(initTabPath)
	if err != nil {
		return nil
	}

	// Check if it uses OpenRC
	if !strings.Contains(string(initTab), "openrc") {
		return nil
	}

	log.Info().Msg("Detected OpenRC-based system, injecting network config")

	// Create /etc/network/interfaces for DHCP
	networkDir := filepath.Join(rootfsDir, "etc/network")
	if err := os.MkdirAll(networkDir, 0755); err != nil {
		return fmt.Errorf("failed to create network dir: %w", err)
	}

	interfacesContent := `# Network interfaces for Firecracker VM
auto lo
iface lo inet loopback

auto eth0
iface eth0 inet dhcp
`
	interfacesPath := filepath.Join(networkDir, "interfaces")
	if err := os.WriteFile(interfacesPath, []byte(interfacesContent), 0644); err != nil {
		return fmt.Errorf("failed to write interfaces file: %w", err)
	}

	log.Info().Str("path", interfacesPath).Msg("Created /etc/network/interfaces")

	// Modify inittab to run networking directly (OpenRC not installed in Docker images)
	initTabContent := `# /etc/inittab - Modified for Firecracker VM

::sysinit:/bin/busybox mount -t proc proc /proc
::sysinit:/bin/busybox mount -t sysfs sysfs /sys
::sysinit:/bin/busybox mount -t devtmpfs devtmpfs /dev
::sysinit:/bin/busybox hostname firecracker-vm
::sysinit:/sbin/ifconfig eth0 up
::sysinit:/usr/sbin/udhcpc -i eth0 -s /usr/share/udhcpc/default.script -q -n

# Serial console
ttyS0::respawn:/sbin/getty -L 115200 ttyS0 vt100

# Shutdown
::ctrlaltdel:/sbin/reboot
::shutdown:/bin/busybox umount -a -r
`
	if err := os.WriteFile(initTabPath, []byte(initTabContent), 0644); err != nil {
		return fmt.Errorf("failed to write inittab: %w", err)
	}
	log.Info().Str("path", initTabPath).Msg("Modified inittab for network setup")

	// Ensure init.d directory exists for our script
	initDir := filepath.Join(rootfsDir, "etc/init.d")
	if err := os.MkdirAll(initDir, 0755); err != nil {
		return fmt.Errorf("failed to create init.d dir: %w", err)
	}

	networkingScript := filepath.Join(initDir, "networking")

	if _, err := os.Stat(networkingScript); err != nil {
		// Create minimal networking init script
		scriptContent := `#!/sbin/openrc-run

name="networking"
description="Configure network interfaces"

depend() {
    need localmount
    after bootmisc
}

start() {
    ebegin "Configuring network interfaces"
    if [ -f /etc/network/interfaces ]; then
        ifconfig eth0 up
        udhcpc -i eth0 -s /usr/share/udhcpc/default.script -q -n
    fi
    eend $?
}

stop() {
    ebegin "Stopping network interfaces"
    ifconfig eth0 down
    eend $?
}
`
		if err := os.WriteFile(networkingScript, []byte(scriptContent), 0755); err != nil {
			return fmt.Errorf("failed to write networking script: %w", err)
		}
		log.Info().Str("path", networkingScript).Msg("Created networking init script")
	}

	// Add networking to the default runlevel
	// OpenRC stores runlevels in /etc/runlevels/
	defaultRunlevelDir := filepath.Join(rootfsDir, "etc/runlevels/default")
	if err := os.MkdirAll(defaultRunlevelDir, 0755); err != nil {
		return fmt.Errorf("failed to create runlevel dir: %w", err)
	}

	// Create symlink to networking script
	networkingSymlink := filepath.Join(defaultRunlevelDir, "networking")
	if _, err := os.Lstat(networkingSymlink); err != nil {
		// Relative path for symlink (OpenRC convention)
		relativePath := "../../init.d/networking"
		if err := os.Symlink(relativePath, networkingSymlink); err != nil {
			return fmt.Errorf("failed to create runlevel symlink: %w", err)
		}
		log.Info().Str("path", networkingSymlink).Msg("Added networking to default runlevel")
	}

	return nil
}

// validateArchitecture checks if the host architecture is supported
func (ip *ImagePreparer) validateArchitecture() error {
	// Firecracker supports x86_64 and aarch64
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return nil
	default:
		return fmt.Errorf("unsupported architecture: %s (Firecracker requires amd64 or arm64)", runtime.GOARCH)
	}
}
