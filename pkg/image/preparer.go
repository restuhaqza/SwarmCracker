// Package image prepares OCI images as root filesystems for Firecracker VMs.
package image

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/restuhaqza/swarmcracker/pkg/storage"
	localtypes "github.com/restuhaqza/swarmcracker/pkg/types"
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
	ociInfo       *OCIImageInfo // Parsed OCI image configuration
}

// PreparerConfig holds image preparer configuration.
type PreparerConfig struct {
	KernelPath      string
	RootfsDir       string
	SocketDir       string
	DefaultVCPUs    int
	DefaultMemoryMB int
	InitSystem      string        `yaml:"init_system"`        // "none", "tini", "dumb-init"
	InitGracePeriod int           `yaml:"init_grace_period"`  // Grace period in seconds
	MaxImageAgeDays int           `yaml:"max_image_age_days"` // Maximum age of rootfs images before cleanup (default 7)
	RegistryAuth    *RegistryAuth `yaml:"registry_auth"`      // Registry authentication configuration
}

// NewImagePreparer creates a new ImagePreparer.
func NewImagePreparer(config interface{}) localtypes.ImagePreparer {
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

	// Create secret manager
	secretMgr := storage.NewSecretManager(
		"/var/lib/swarmcracker/secrets",
		"/var/lib/swarmcracker/configs",
	)

	return &ImagePreparer{
		config:        cfg,
		cacheDir:      "/var/cache/swarmcracker",
		rootfsDir:     cfg.RootfsDir,
		initInjector:  initInjector,
		volumeManager: volumeMgr,
		secretManager: secretMgr,
	}
}

// Prepare prepares an OCI image for the given task.
func (ip *ImagePreparer) Prepare(ctx context.Context, task *localtypes.Task) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}

	container, err := task.Spec.GetContainer()
	if err != nil {
		return fmt.Errorf("invalid task runtime: %w", err)
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

	// Check if rootfs already exists with valid init
	if _, err := os.Stat(rootfsPath); err == nil {
		// Verify cached rootfs has valid /init
		if ip.verifyCachedRootfs(rootfsPath) {
			log.Info().
				Str("path", rootfsPath).
				Msg("Rootfs already exists and valid, skipping")
			task.Annotations["rootfs"] = rootfsPath
			return nil
		}
		log.Info().
			Str("path", rootfsPath).
			Msg("Cached rootfs invalid (missing init), re-preparing")
	}

	// Prepare the image with file locking for concurrent safety
	if err := ip.prepareWithLock(ctx, container.Image, imageID, rootfsPath); err != nil {
		return fmt.Errorf("failed to prepare image: %w", err)
	}

	// Store init system type in annotations (init was injected during prepareImage)
	if ip.initInjector.IsEnabled() {
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

	// Inject secrets if secret manager is available
	if ip.secretManager != nil && len(task.Secrets) > 0 {
		log.Info().
			Str("task_id", task.ID).
			Int("secret_count", len(task.Secrets)).
			Msg("Injecting secrets")

		if err := ip.secretManager.InjectSecrets(ctx, task.ID, task.Secrets, rootfsPath); err != nil {
			return fmt.Errorf("failed to inject secrets: %w", err)
		}
	}

	// Inject configs if secret manager is available
	if ip.secretManager != nil && len(task.Configs) > 0 {
		log.Info().
			Str("task_id", task.ID).
			Int("config_count", len(task.Configs)).
			Msg("Injecting configs")

		if err := ip.secretManager.InjectConfigs(ctx, task.ID, task.Configs, rootfsPath); err != nil {
			return fmt.Errorf("failed to inject configs: %w", err)
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
// Init injection happens BEFORE ext4 creation so files are included.
func (ip *ImagePreparer) prepareImage(ctx context.Context, imageRef, imageID, outputPath string) error {
	// Create temporary directory for extraction
	tmpDir, err := os.MkdirTemp("", "swarmcracker-extract-")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Step 1: Validate image manifest (OS/architecture compatibility)
	log.Debug().Str("image", imageRef).Msg("Validating image manifest")
	buildOpts := buildRemoteOptions(ctx, ip.config.RegistryAuth)
	if err := validateImageManifest(ctx, imageRef, buildOpts...); err != nil {
		return fmt.Errorf("image validation failed: %w", err)
	}

	// Step 2: Pull and extract OCI image
	log.Debug().Str("image", imageRef).Msg("Pulling OCI image")
	if err := ip.extractOCIImage(ctx, imageRef, tmpDir); err != nil {
		return fmt.Errorf("failed to extract OCI image: %w", err)
	}

	// Step 3: Validate critical symlinks (especially /bin/sh)
	log.Debug().Str("tmpDir", tmpDir).Msg("Validating critical symlinks")
	if err := validateCriticalSymlinks(tmpDir); err != nil {
		return fmt.Errorf("symlink validation failed: %w", err)
	}

	// Step 4: Inject init system BEFORE ext4 creation (NEW - was after)
	if ip.initInjector.IsEnabled() {
		log.Info().
			Str("init_system", string(ip.initInjector.config.Type)).
			Msg("Injecting init system into extracted directory")

		// Pass OCI config to init injector for generic wrapper
		if err := ip.initInjector.InjectIntoDir(tmpDir, ip.ociInfo); err != nil {
			return fmt.Errorf("failed to inject init system: %w", err)
		}
	}

	// Step 5: Inject essential files (DNS, hosts, nsswitch, machine-id, dirs)
	log.Debug().Str("tmpDir", tmpDir).Msg("Injecting essential files")
	if err := injectEssentialFiles(tmpDir, imageID); err != nil {
		return fmt.Errorf("failed to inject essential files: %w", err)
	}

	// Step 6: Inject network configuration for Alpine/OpenRC systems
	if err := ip.injectNetworkConfig(tmpDir); err != nil {
		log.Warn().Err(err).Msg("Failed to inject network config (may still work if image has it)")
	}

	// Step 7: Create ext4 filesystem image (now includes init files)
	log.Debug().Str("output", outputPath).Msg("Creating ext4 filesystem")
	if err := ip.createExt4Image(tmpDir, outputPath); err != nil {
		return fmt.Errorf("failed to create ext4 image: %w", err)
	}

	// Step 8: Verify rootfs is bootable (graceful warning if verification fails)
	if err := VerifyBootable(outputPath); err != nil {
		log.Warn().Err(err).Msg("Rootfs verification failed, but continuing")
		// Don't fail the whole preparation — just warn
	}

	return nil
}

// prepareWithLock prepares an image with file locking for concurrent safety.
// Acquires an exclusive lock on the rootfs path to prevent race conditions
// when multiple goroutines/processes try to prepare the same image.
func (ip *ImagePreparer) prepareWithLock(ctx context.Context, imageRef, imageID, rootfsPath string) error {
	// Create lock file path
	lockPath := rootfsPath + ".lock"

	// Ensure parent directory exists for lock file
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return fmt.Errorf("failed to create lock directory: %w", err)
	}

	// Create/open lock file
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("failed to create lock file: %w", err)
	}
	defer lockFile.Close()

	// Acquire exclusive lock
	log.Debug().Str("lock", lockPath).Msg("Acquiring lock for image preparation")
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	log.Debug().Str("lock", lockPath).Msg("Lock acquired")

	// Double-check: another process may have created rootfs while we waited
	if _, err := os.Stat(rootfsPath); err == nil {
		if ip.verifyCachedRootfs(rootfsPath) {
			log.Info().
				Str("path", rootfsPath).
				Msg("Rootfs created by another process while waiting for lock")
			return nil
		}
	}

	// Proceed with preparation
	return ip.prepareImage(ctx, imageRef, imageID, rootfsPath)
}

// verifyCachedRootfs checks if a cached rootfs has a valid /init entry.
// Uses debugfs to inspect the ext4 image without mounting.
func (ip *ImagePreparer) verifyCachedRootfs(rootfsPath string) bool {
	// Check if rootfs file exists first
	if _, err := os.Stat(rootfsPath); err != nil {
		log.Debug().Str("path", rootfsPath).Msg("Cached rootfs does not exist")
		return false
	}

	// Check if debugfs is available
	if _, err := exec.LookPath("debugfs"); err != nil {
		// debugfs not available, assume rootfs is valid
		log.Debug().Msg("debugfs not available, assuming cached rootfs is valid")
		return true
	}

	// Use debugfs to check for /init
	cmd := exec.Command("debugfs", "-R", "stat /init", rootfsPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Debug().
			Err(err).
			Str("output", string(output)).
			Msg("Cached rootfs missing /init, will re-prepare")
		return false
	}

	log.Debug().Str("path", rootfsPath).Msg("Cached rootfs has valid /init")
	return true
}

// rootfsVersion tracks the pipeline version for cache invalidation.
const rootfsVersion = "1" // Increment when pipeline changes invalidate cache

// extractOCIImage extracts an OCI image using go-containerregistry (primary) or docker/podman (fallback).
func (ip *ImagePreparer) extractOCIImage(ctx context.Context, imageRef, destPath string) error {
	// Try different methods in order of preference (daemon-free first)
	methods := []struct {
		name string
		fn   func(context.Context, string, string) error
	}{
		{
			name: "go-containerregistry",
			fn:   ip.extractWithGGCR,
		},
		{
			name: "docker",
			fn:   ip.extractWithDockerCLI,
		},
		{
			name: "podman",
			fn:   ip.extractWithDockerCLI,
		},
	}

	for _, method := range methods {
		// Skip CLI methods if tool not available
		if method.name != "go-containerregistry" {
			if _, err := exec.LookPath(method.name); err != nil {
				continue
			}
		}

		log.Debug().Str("using", method.name).Msg("Extracting image")

		err := method.fn(ctx, imageRef, destPath)
		if err != nil {
			log.Debug().Str("method", method.name).Err(err).Msg("Extraction failed, trying next method")
			continue
		}

		return nil
	}

	return fmt.Errorf("no image extraction method available (go-containerregistry, docker, or podman)")
}

// extractWithGGCR extracts an OCI image using go-containerregistry (pure Go, no daemon).
// This pulls directly from the registry and flattens all layers into a filesystem.
// Also extracts OCI image configuration (ENTRYPOINT, CMD, ENV, USER, etc).
func (ip *ImagePreparer) extractWithGGCR(ctx context.Context, imageRef, destPath string) error {
	// Validate inputs
	if imageRef == "" {
		return fmt.Errorf("image reference must not be empty")
	}
	if destPath == "" {
		return fmt.Errorf("destination path must not be empty")
	}

	// Ensure image ref has docker.io prefix for standard images
	fullRef := imageRef
	if !strings.Contains(fullRef, "/") {
		fullRef = "docker.io/library/" + fullRef
	} else if !strings.Contains(fullRef, ".") {
		fullRef = "docker.io/" + fullRef
	}

	// Parse the image reference
	ref, err := name.ParseReference(fullRef)
	if err != nil {
		return fmt.Errorf("failed to parse image reference %q: %w", fullRef, err)
	}

	// Build remote options with auth and explicit platform selection
	opts := buildRemoteOptions(ctx, ip.config.RegistryAuth)
	opts = append(opts, remote.WithPlatform(v1.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}))

	log.Info().Str("image", fullRef).Msg("Pulling image from registry")

	// Pull image from registry (no daemon required)
	img, err := remote.Image(ref, opts...)
	if err != nil {
		return fmt.Errorf("failed to pull image %q: %w", fullRef, err)
	}

	// Extract OCI image configuration (ENTRYPOINT, CMD, ENV, USER, etc)
	cfg, err := img.ConfigFile()
	if err != nil {
		log.Warn().Err(err).Str("image", fullRef).Msg("Failed to get image config, continuing without OCI info")
	} else if cfg != nil {
		ip.ociInfo = ParseOCIImageConfig(cfg, fullRef)
		log.Info().
				Str("image", fullRef).
				Str("os", ip.ociInfo.OS).
				Str("arch", ip.ociInfo.Architecture).
				Bool("has_entrypoint", ip.ociInfo.HasEntrypoint()).
				Bool("has_cmd", ip.ociInfo.HasCmd()).
				Int("env_count", len(ip.ociInfo.Env)).
				Msg("Extracted OCI image configuration")
	}

	// Get image info
	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("failed to get layers: %w", err)
	}
	log.Info().Str("image", fullRef).Int("layers", len(layers)).Msg("Image pulled successfully")

	// Extract flattened filesystem (handles whiteouts automatically)
	fs := mutate.Extract(img)
	defer fs.Close()

	// Extract tar stream to destination directory
	return extractTarStream(fs, destPath)
}

// extractTarStream extracts a tar stream to a directory.
// Handles regular files, directories, symlinks, and hard links.
func extractTarStream(r io.Reader, dest string) error {
	tr := tar.NewReader(r)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar entry: %w", err)
		}

		// Security: prevent path traversal
		target := filepath.Join(dest, header.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dest)+string(os.PathSeparator)) {
			log.Warn().Str("path", header.Name).Msg("Skipping path traversal attempt")
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)&0777); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", target, err)
			}

		case tar.TypeReg, tar.TypeRegA:
			// Ensure parent directory exists
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode)&0777)
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", target, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("failed to write file %s: %w", target, err)
			}
			f.Close()

		case tar.TypeSymlink:
			if err := os.Symlink(header.Linkname, target); err != nil {
				// Symlink may already exist, skip
				log.Debug().Str("path", target).Err(err).Msg("Failed to create symlink")
			}

		case tar.TypeLink:
			// Hard link
			linkTarget := filepath.Join(dest, header.Linkname)
			if err := os.Link(linkTarget, target); err != nil {
				log.Debug().Str("path", target).Str("link", linkTarget).Err(err).Msg("Failed to create hard link")
			}

		case tar.TypeXGlobalHeader:
			// Extended header, skip
			continue

		default:
			log.Debug().Str("type", string(header.Typeflag)).Str("path", header.Name).Msg("Skipping unsupported tar entry type")
		}
	}

	return nil
}

// extractWithDockerCLI extracts an image using docker or podman CLI as a fallback.
// Creates a container, exports the filesystem, and extracts the tar.
func (ip *ImagePreparer) extractWithDockerCLI(ctx context.Context, imageRef, destPath string) error {
	runtimeName := "docker"
	if _, err := exec.LookPath("docker"); err != nil {
		runtimeName = "podman"
	}

	// Create container
	output, err := exec.CommandContext(ctx, runtimeName, "create", "--quiet", imageRef, "/bin/true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s create failed: %s: %w", runtimeName, string(output), err)
	}

	outputStr := string(output)
	lines := strings.Split(strings.TrimSpace(outputStr), "\n")
	containerID := strings.TrimSpace(lines[len(lines)-1])

	if containerID == "" || len(containerID) < 12 {
		return fmt.Errorf("%s create returned invalid container ID: %q", runtimeName, outputStr)
	}

	// Cleanup on failure
	defer exec.Command(runtimeName, "rm", "-f", containerID).Run()

	// Export filesystem to tar
	tarPath := filepath.Join(destPath, "fs.tar")
	exportCmd := exec.CommandContext(ctx, runtimeName, "export", containerID, "-o", tarPath)
	if output, err := exportCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s export failed: %s: %w", runtimeName, string(output), err)
	}

	// Extract tar to directory
	tarCmd := exec.CommandContext(ctx, "tar", "xf", tarPath, "-C", destPath)
	if err := tarCmd.Run(); err != nil {
		return fmt.Errorf("failed to extract tar: %w", err)
	}

	// Remove tar file
	os.Remove(tarPath)

	return nil
}

// createExt4Image creates an ext4 filesystem from a directory.
// This is a wrapper that calls createExt4ImageWithOverhead with default 50% overhead.
func (ip *ImagePreparer) createExt4Image(sourceDir, outputPath string) error {
	return ip.createExt4ImageWithOverhead(sourceDir, outputPath, 50)
}

// createExt4ImageWithOverhead creates an ext4 filesystem with explicit overhead and disk space checking.
func (ip *ImagePreparer) createExt4ImageWithOverhead(sourceDir, outputPath string, overheadPercent int) error {
	if sourceDir == "" {
		return fmt.Errorf("source directory cannot be empty")
	}
	if outputPath == "" {
		return fmt.Errorf("output path cannot be empty")
	}
	if overheadPercent <= 0 {
		overheadPercent = 50
	}

	// Check if source directory exists
	if _, err := os.Stat(sourceDir); err != nil {
		return fmt.Errorf("source directory does not exist: %w", err)
	}

	// Check if mkfs.ext4 is available
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		return fmt.Errorf("mkfs.ext4 not found: %w", err)
	}

	// Calculate directory size
	var dirSize int64
	filepath.Walk(sourceDir, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() {
			dirSize += info.Size()
		}
		return nil
	})

	// Apply overhead
	totalSize := dirSize * int64(100+overheadPercent) / 100

	// Minimum 100MB
	minSize := int64(100 * 1024 * 1024)
	if totalSize < minSize {
		totalSize = minSize
	}

	// Calculate block count (4K blocks)
	blockSize := int64(4096)
	blockCount := (totalSize + blockSize - 1) / blockSize

	// Check available disk space
	var stat syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(outputPath), &stat); err == nil {
		available := int64(stat.Bavail * uint64(stat.Bsize))
		if totalSize > available {
			return fmt.Errorf("insufficient disk space: need %d MB, have %d MB",
				totalSize/1024/1024, available/1024/1024)
		}
	}

	// Calculate sufficient inodes: 1 inode per 4KB of data, minimum 2048
	inodeCount := dirSize / 4096 * 2
	if inodeCount < 2048 {
		inodeCount = 2048
	}
	// Make it a nice round number
	inodeRatio := totalSize / inodeCount
	if inodeRatio < 4096 {
		inodeRatio = 4096
	}

	// Create ext4 with explicit size using mkfs.ext4 -d
	cmd := exec.Command("mkfs.ext4",
		"-d", sourceDir,
		"-b", fmt.Sprintf("%d", blockSize),
		"-L", "rootfs",
		"-i", fmt.Sprintf("%d", inodeRatio),
		outputPath,
		fmt.Sprintf("%d", blockCount),
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(outputPath)
		return fmt.Errorf("mkfs.ext4 failed: %w\nOutput: %s", err, string(output))
	}

	log.Info().
		Int64("dir_size_mb", dirSize/1024/1024).
		Int64("rootfs_size_mb", totalSize/1024/1024).
		Int("overhead_percent", overheadPercent).
		Msg("Created ext4 image")

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
	// Init injection now happens via InjectIntoDir BEFORE ext4 creation.
	// The old Inject method (which used broken mountRootfs) has been removed.
	// If init needs to be injected post-creation (unusual), mount and copy manually.

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

	// Create /sbin/init wrapper that runs tini with entrypoint
	if err := ip.createInitWrapper(mountDir); err != nil {
		log.Warn().Err(err).Msg("Failed to create init wrapper")
	}

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

// createInitWrapper creates /sbin/init as a wrapper that calls tini with entrypoint.
// Deprecated: This method is only used by the deprecated Inject() method.
// The generic wrapper is now created by createGenericInitWrapper in injectTiniIntoDir.
func (ip *ImagePreparer) createInitWrapper(mountDir string) error {
	// This method is deprecated. The generic OCI-aware wrapper is now created
	// by createGenericInitWrapper in init.go via InjectIntoDir.
	// Just log a warning and return nil.
	log.Warn().Msg("createInitWrapper is deprecated, use InjectIntoDir instead")
	return nil
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
func (ip *ImagePreparer) handleMounts(ctx context.Context, task *localtypes.Task, rootfsPath string, mounts []localtypes.Mount) error {
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
func (ip *ImagePreparer) handleVolumeMount(ctx context.Context, task *localtypes.Task, rootfsPath string, mount *localtypes.Mount) error {
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
func (ip *ImagePreparer) handleBindMount(rootfsPath string, mount *localtypes.Mount) error {
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

// GetOCIInfo returns the parsed OCI image configuration.
// Returns nil if no OCI config was extracted (e.g., fallback to Docker CLI).
func (ip *ImagePreparer) GetOCIInfo() *OCIImageInfo {
	return ip.ociInfo
}
