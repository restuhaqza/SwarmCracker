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
	container, ok := task.Spec.Runtime.(*types.Container)
	if !ok {
		return fmt.Errorf("task runtime is not a container")
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
	// Try docker first, then podman
	cmds := []struct {
		name string
		args []string
	}{
		{
			name: "docker",
			args: []string{"create", "--root", destPath, imageRef, "/bin/true"},
		},
		{
			name: "podman",
			args: []string{"create", "--root", destPath, imageRef, "/bin/true"},
		},
	}

	for _, cmd := range cmds {
		if _, err := exec.LookPath(cmd.name); err != nil {
			continue
		}

		log.Debug().Str("using", cmd.name).Msg("Extracting image")

		// Create container
		output, err := exec.CommandContext(ctx, cmd.name, cmd.args...).CombinedOutput()
		if err != nil {
			log.Debug().Str("output", string(output)).Err(err).Msg("Command failed, trying next method")
			continue
		}

		containerID := strings.TrimSpace(string(output))

		// Export container filesystem
		exportCmd := exec.CommandContext(ctx, cmd.name, "export", containerID, "-o", filepath.Join(destPath, "fs.tar"))
		if err := exportCmd.Run(); err != nil {
			// Cleanup container
			exec.Command(cmd.name, "rm", "-f", containerID).Run()
			return fmt.Errorf("failed to export container: %w", err)
		}

		// Cleanup container
		exec.Command(cmd.name, "rm", "-f", containerID).Run()

		// Extract tar
		tarCmd := exec.CommandContext(ctx, "tar", "xf", filepath.Join(destPath, "fs.tar"), "-C", destPath)
		if err := tarCmd.Run(); err != nil {
			return fmt.Errorf("failed to extract tar: %w", err)
		}

		// Remove tar file
		os.Remove(filepath.Join(destPath, "fs.tar"))

		return nil
	}

	return fmt.Errorf("no container runtime found (docker or podman required)")
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
