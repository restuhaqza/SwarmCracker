package image

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestVerifyBootable_DebugfsNotAvailable tests that verification gracefully skips when debugfs is missing.
func TestVerifyBootable_DebugfsNotAvailable(t *testing.T) {
	// Skip if debugfs is available (we can't easily mock this)
	if _, err := exec.LookPath("debugfs"); err == nil {
		t.Skip("debugfs is available, cannot test missing debugfs scenario")
	}

	// Create a dummy file to pass as rootfs
	tmpFile, err := os.CreateTemp("", "test-rootfs-*.ext4")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	// Should return nil when debugfs is not available
	err = VerifyBootable(tmpFile.Name())
	if err != nil {
		t.Errorf("Expected nil when debugfs not available, got: %v", err)
	}
}

// TestVerifyBootable_InvalidExt4 tests verification with a non-ext4 file.
func TestVerifyBootable_InvalidExt4(t *testing.T) {
	// Skip if debugfs is not available
	if _, err := exec.LookPath("debugfs"); err != nil {
		t.Skip("debugfs not available")
	}

	// Create a non-ext4 file
	tmpFile, err := os.CreateTemp("", "test-rootfs-*.ext4")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write some random data
	_, err = tmpFile.WriteString("not an ext4 filesystem")
	if err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	tmpFile.Close()

	// debugfs should fail on non-ext4 files - it may return error or nil depending on behavior
	// We just verify the function doesn't panic
	_ = VerifyBootable(tmpFile.Name())
}

// TestVerifyBootable_CreatedRootfs tests verification with a real ext4 image.
func TestVerifyBootable_CreatedRootfs(t *testing.T) {
	// Skip if mkfs.ext4 is not available
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not available")
	}

	// Skip if debugfs is not available
	if _, err := exec.LookPath("debugfs"); err != nil {
		t.Skip("debugfs not available")
	}

	// Create a temp directory with required files
	tmpDir, err := os.MkdirTemp("", "test-rootfs-content-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create required directory structure
	dirs := []string{"init", "sbin", "bin", "etc"}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(tmpDir, dir), 0755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", dir, err)
		}
	}

	// Create required files
	requiredFiles := map[string]string{
		"init/init":       "#!/bin/sh\necho init",
		"sbin/init":       "#!/bin/sh\necho sbin-init",
		"sbin/tini":       "#!/bin/sh\necho tini",
		"bin/sh":          "#!/bin/sh\necho sh",
		"etc/resolv.conf": "nameserver 8.8.8.8",
	}

	for relPath, content := range requiredFiles {
		filePath := filepath.Join(tmpDir, relPath)
		if err := os.WriteFile(filePath, []byte(content), 0755); err != nil {
			t.Fatalf("Failed to create file %s: %v", relPath, err)
		}
	}

	// Create ext4 image
	outputPath := filepath.Join(os.TempDir(), "test-verify-rootfs.ext4")
	defer os.Remove(outputPath)

	// Create ext4 image with mkfs.ext4 -d
	// Block count for 100MB = 25600 blocks (4K each)
	blockCount := 25600
	cmd := exec.Command("mkfs.ext4", "-d", tmpDir, "-L", "rootfs", outputPath, fmt.Sprintf("%d", blockCount))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create ext4 image: %v\nOutput: %s", err, string(output))
	}

	// Verify should pass
	err = VerifyBootable(outputPath)
	if err != nil {
		t.Errorf("Expected verification to pass, got: %v", err)
	}
}

// TestCreateExt4ImageWithOverhead_MinSize tests that tiny dirs produce at least 100MB output.
func TestCreateExt4ImageWithOverhead_MinSize(t *testing.T) {
	// Skip if mkfs.ext4 is not available
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not available")
	}

	// Create a tiny temp directory
	tmpDir, err := os.MkdirTemp("", "test-tiny-rootfs-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create minimal structure
	if err := os.MkdirAll(filepath.Join(tmpDir, "bin"), 0755); err != nil {
		t.Fatalf("Failed to create bin dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "bin", "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatalf("Failed to create sh: %v", err)
	}

	// Create output path
	outputPath := filepath.Join(os.TempDir(), "test-minsize-rootfs.ext4")
	defer os.Remove(outputPath)

	// Create image preparer
	ip := &ImagePreparer{
		config:    &PreparerConfig{},
		cacheDir:  "/var/cache/swarmcracker",
		rootfsDir: "/var/lib/firecracker/rootfs",
	}

	// Create ext4 with overhead
	err = ip.createExt4ImageWithOverhead(tmpDir, outputPath, 50)
	if err != nil {
		t.Fatalf("Failed to create ext4 image: %v", err)
	}

	// Check file size - should be at least 100MB
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("Failed to stat output file: %v", err)
	}

	minSize := int64(100 * 1024 * 1024) // 100MB
	if info.Size() < minSize {
		t.Errorf("Expected at least 100MB, got %d bytes (%.2f MB)", info.Size(), float64(info.Size())/(1024*1024))
	}
}

// TestCreateExt4ImageWithOverhead_DiskSpaceCheck tests disk space checking.
func TestCreateExt4ImageWithOverhead_DiskSpaceCheck(t *testing.T) {
	// Skip if mkfs.ext4 is not available
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not available")
	}

	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "test-diskspace-rootfs-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create output path in a real temp directory
	outputPath := filepath.Join(os.TempDir(), "test-diskspace-rootfs.ext4")
	defer os.Remove(outputPath)

	// Create image preparer
	ip := &ImagePreparer{
		config:    &PreparerConfig{},
		cacheDir:  "/var/cache/swarmcracker",
		rootfsDir: "/var/lib/firecracker/rootfs",
	}

	// Create ext4 with overhead - should succeed if there's enough disk space
	err = ip.createExt4ImageWithOverhead(tmpDir, outputPath, 50)
	if err != nil {
		// Check if it's a disk space error
		t.Logf("Disk space check result: %v", err)
		// This test just verifies the disk space check runs without panic
	}
}

// TestCreateExt4ImageWithOverhead_OverheadApplied tests that large dirs get overhead applied.
func TestCreateExt4ImageWithOverhead_OverheadApplied(t *testing.T) {
	// Skip if mkfs.ext4 is not available
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not available")
	}

	// Create a temp directory with larger files
	tmpDir, err := os.MkdirTemp("", "test-overhead-rootfs-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create directory structure
	if err := os.MkdirAll(filepath.Join(tmpDir, "bin"), 0755); err != nil {
		t.Fatalf("Failed to create bin dir: %v", err)
	}

	// Create a 1MB file (well above minimum threshold)
	data := make([]byte, 1024*1024) // 1MB
	for i := range data {
		data[i] = byte(i % 256)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "bin", "largefile"), data, 0644); err != nil {
		t.Fatalf("Failed to create large file: %v", err)
	}

	// Create output path
	outputPath := filepath.Join(os.TempDir(), "test-overhead-rootfs.ext4")
	defer os.Remove(outputPath)

	// Create image preparer
	ip := &ImagePreparer{
		config:    &PreparerConfig{},
		cacheDir:  "/var/cache/swarmcracker",
		rootfsDir: "/var/lib/firecracker/rootfs",
	}

	// Create ext4 with 50% overhead
	err = ip.createExt4ImageWithOverhead(tmpDir, outputPath, 50)
	if err != nil {
		t.Fatalf("Failed to create ext4 image: %v", err)
	}

	// Check file size - should be larger than the source directory
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("Failed to stat output file: %v", err)
	}

	// Calculate source directory size
	var sourceSize int64
	filepath.Walk(tmpDir, func(_ string, fi os.FileInfo, _ error) error {
		if !fi.IsDir() {
			sourceSize += fi.Size()
		}
		return nil
	})

	// Output should be larger than source due to overhead (at least 50%)
	// Actually it will be at least 100MB minimum, so just verify it's larger than source
	if info.Size() <= sourceSize {
		t.Errorf("Expected output (%d bytes) to be larger than source (%d bytes)", info.Size(), sourceSize)
	}

	t.Logf("Source: %d bytes, Output: %d bytes", sourceSize, info.Size())
}

// TestCreateExt4ImageWithOverhead_DefaultOverhead tests default overhead value.
func TestCreateExt4ImageWithOverhead_DefaultOverhead(t *testing.T) {
	// Test that overheadPercent defaults to 50 when <= 0

	// Skip if mkfs.ext4 is not available
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not available")
	}

	tmpDir, err := os.MkdirTemp("", "test-default-overhead-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create minimal structure
	if err := os.MkdirAll(filepath.Join(tmpDir, "bin"), 0755); err != nil {
		t.Fatalf("Failed to create bin dir: %v", err)
	}

	outputPath := filepath.Join(os.TempDir(), "test-default-overhead.ext4")
	defer os.Remove(outputPath)

	ip := &ImagePreparer{
		config:    &PreparerConfig{},
		cacheDir:  "/var/cache/swarmcracker",
		rootfsDir: "/var/lib/firecracker/rootfs",
	}

	// Test with zero overhead - should default to 50%
	err = ip.createExt4ImageWithOverhead(tmpDir, outputPath, 0)
	if err != nil {
		t.Fatalf("Failed with zero overhead: %v", err)
	}

	// Test with negative overhead - should default to 50%
	outputPath2 := filepath.Join(os.TempDir(), "test-negative-overhead.ext4")
	defer os.Remove(outputPath2)

	err = ip.createExt4ImageWithOverhead(tmpDir, outputPath2, -10)
	if err != nil {
		t.Fatalf("Failed with negative overhead: %v", err)
	}
}
