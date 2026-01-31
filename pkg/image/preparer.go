// Package image prepares OCI images as root filesystems for Firecracker VMs.
package image

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// ImagePreparer prepares OCI images as root filesystems.
type ImagePreparer struct {
	config     *PreparerConfig
	cacheDir   string
	rootfsDir  string
}

// PreparerConfig holds image preparer configuration.
type PreparerConfig struct {
	KernelPath     string
	RootfsDir      string
	SocketDir      string
	DefaultVCPUs   int
	DefaultMemoryMB int
}

// NewImagePreparer creates a new ImagePreparer.
func NewImagePreparer(config interface{}) types.ImagePreparer {
	var cfg *PreparerConfig
	if c, ok := config.(*PreparerConfig); ok {
		cfg = c
	} else {
		cfg = &PreparerConfig{
			RootfsDir: "/var/lib/firecracker/rootfs",
		}
	}

	// Ensure rootfs directory exists
	os.MkdirAll(cfg.RootfsDir, 0755)

	return &ImagePreparer{
		config:    cfg,
		cacheDir:  "/var/cache/swarmcracker",
		rootfsDir: cfg.RootfsDir,
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

// extractWithPodman extracts an image using Podman (with --root flag)
func (ip *ImagePreparer) extractWithPodman(ctx context.Context, runtime, imageRef, destPath string) (string, error) {
	// Create container with --root flag
	output, err := exec.CommandContext(ctx, runtime, "create", "--root", destPath, imageRef, "/bin/true").CombinedOutput()
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

	// Add 20% buffer
	size = size + (size / 5)

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
	// Simple hash-based ID generation
	parts := strings.Split(imageRef, ":")
	tag := "latest"
	if len(parts) > 1 {
		tag = parts[1]
	}
	
	// Use tag + first part of name
	name := strings.ReplaceAll(parts[0], "/", "-")
	
	return fmt.Sprintf("%s-%s", name, tag)
}

// Cleanup removes old unused rootfs images.
func (ip *ImagePreparer) Cleanup(ctx context.Context, keepDays int) error {
	log.Info().Int("keep_days", keepDays).Msg("Cleaning up old images")
	
	// TODO: Implement cleanup logic
	// 1. Scan rootfs directory
	// 2. Check file ages
	// 3. Remove files older than keepDays
	
	return nil
}
