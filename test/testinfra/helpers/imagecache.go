// Package helpers provides shared test infrastructure utilities.
package helpers

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ImageCache provides pre-built ext4 rootfs images for integration/E2E tests,
// avoiding repeated Docker Hub pulls.
type ImageCache struct {
	Dir string // e.g. ~/.cache/swarmcracker/images
}

// NewImageCache creates a cache with the default directory.
func NewImageCache() *ImageCache {
	home, _ := os.UserHomeDir()
	return &ImageCache{
		Dir: filepath.Join(home, ".cache", "swarmcracker", "images"),
	}
}

// NewImageCacheWithDir creates a cache with a custom directory.
func NewImageCacheWithDir(dir string) *ImageCache {
	return &ImageCache{Dir: dir}
}

// Has checks whether a cached rootfs exists for the given image reference.
func (c *ImageCache) Has(imageRef string) bool {
	path := c.Path(imageRef)
	_, err := os.Stat(path)
	return err == nil
}

// Path returns the cache file path for an image.
func (c *ImageCache) Path(imageRef string) string {
	return filepath.Join(c.Dir, cacheName(imageRef))
}

// Ensure creates the cache directory if it doesn't exist.
func (c *ImageCache) Ensure() error {
	return os.MkdirAll(c.Dir, 0755)
}

// Size returns the number of cached images and total bytes.
func (c *ImageCache) Size() (count int, bytes int64) {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ext4") {
			info, err := e.Info()
			if err != nil {
				continue
			}
			count++
			bytes += info.Size()
		}
	}
	return
}

// List returns all cached image references.
func (c *ImageCache) List() ([]string, error) {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ext4") {
			ref := imageRefFromCacheName(e.Name())
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

// Warm pulls an image via docker and builds a minimal ext4 for test usage.
// This is a best-effort cache warm — failures are non-fatal.
func (c *ImageCache) Warm(imageRef string) error {
	if err := c.Ensure(); err != nil {
		return err
	}
	if c.Has(imageRef) {
		return nil // already cached
	}

	// Use docker to pull and create
	docker, _ := exec.LookPath("docker")
	if docker == "" {
		podman, _ := exec.LookPath("podman")
		if podman == "" {
			return fmt.Errorf("no container runtime available")
		}
		docker = podman
	}

	// Create a minimal ext4 from the container image
	tmpDir, err := os.MkdirTemp("", "swarmcracker-cache-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	containerName := fmt.Sprintf("swarmcracker-cache-%s", cacheName(imageRef))
	containerName = strings.TrimSuffix(containerName, ".ext4")
	containerName = containerName[:min(len(containerName), 64)]

	// Create container
	create := exec.Command(docker, "create", "--name", containerName, imageRef)
	if out, err := create.CombinedOutput(); err != nil {
		return fmt.Errorf("create container: %s: %w", string(out), err)
	}

	// Export filesystem
	tarPath := filepath.Join(tmpDir, "rootfs.tar")
	export := exec.Command(docker, "export", "-o", tarPath, containerName)
	if out, err := export.CombinedOutput(); err != nil {
		exec.Command(docker, "rm", containerName).Run()
		return fmt.Errorf("export container: %s: %w", string(out), err)
	}

	// Extract to directory
	extractDir := filepath.Join(tmpDir, "extract")
	os.MkdirAll(extractDir, 0755)
	extract := exec.Command("tar", "-xf", tarPath, "-C", extractDir)
	if out, err := extract.CombinedOutput(); err != nil {
		exec.Command(docker, "rm", containerName).Run()
		return fmt.Errorf("extract tar: %s: %w", string(out), err)
	}

	// Create ext4
	cachePath := c.Path(imageRef)
	makeExt4 := exec.Command("mkfs.ext4", "-d", extractDir, cachePath, "100M")
	if out, err := makeExt4.CombinedOutput(); err != nil {
		exec.Command(docker, "rm", containerName).Run()
		return fmt.Errorf("mkfs.ext4: %s: %w", string(out), err)
	}

	// Cleanup
	exec.Command(docker, "rm", containerName).Run()
	return nil
}

// Clean removes old cached images older than the given duration.
// If maxAge is 0, the default retention is used.
func (c *ImageCache) Clean(keep int) error {
	if keep <= 0 {
		keep = 10 // keep last 10 by default
	}
	refs, err := c.List()
	if err != nil {
		return err
	}
	if len(refs) <= keep {
		return nil
	}
	// Remove oldest (first in list — simple FIFO)
	for _, ref := range refs[:len(refs)-keep] {
		os.Remove(c.Path(ref))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func cacheName(imageRef string) string {
	h := sha256.Sum256([]byte(imageRef))
	return fmt.Sprintf("%x.ext4", h[:16])
}

func imageRefFromCacheName(name string) string {
	// We can't reverse the hash, but we store it for listing purposes.
	// The actual mapping is maintained by the cache directory structure.
	return strings.TrimSuffix(name, ".ext4")
}
