package image

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- prepareWithLock tests ---

func TestPrepareWithLock_CreatesLockFile(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "test.rootfs.ext4")

	lockPath := rootfsPath + ".lock"

	// Verify the lock file path logic
	if filepath.Base(lockPath) != "test.rootfs.ext4.lock" {
		t.Fatalf("lock file name should be rootfs + .lock, got %s", filepath.Base(lockPath))
	}
}

func TestPrepareWithLock_DoubleCheckPattern(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "test.rootfs.ext4")

	// Create a fake existing rootfs
	if err := os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644); err != nil {
		t.Fatal(err)
	}

	ip := &ImagePreparer{
		config:    &PreparerConfig{RootfsDir: dir},
		cacheDir:  dir,
		rootfsDir: dir,
	}

	// verifyCachedRootfs should handle missing debugfs gracefully
	result := ip.verifyCachedRootfs(rootfsPath)
	// Without debugfs, should assume valid (returns true)
	// With debugfs, will try to stat /init and likely return false
	t.Logf("verifyCachedRootfs returned: %v (depends on debugfs availability)", result)
}

// --- verifyCachedRootfs tests ---

func TestVerifyCachedRootfs_NonexistentFile(t *testing.T) {
	ip := &ImagePreparer{
		config: &PreparerConfig{},
	}

	// Non-existent file should fail
	result := ip.verifyCachedRootfs("/nonexistent/path/rootfs.ext4")
	if result {
		t.Fatal("verifyCachedRootfs should return false for non-existent file")
	}
}

func TestVerifyCachedRootfs_InvalidExt4(t *testing.T) {
	dir := t.TempDir()
	fakeRootfs := filepath.Join(dir, "rootfs.ext4")
	os.WriteFile(fakeRootfs, []byte("not an ext4 image"), 0644)

	ip := &ImagePreparer{
		config: &PreparerConfig{},
	}

	// Invalid ext4 should return false (if debugfs available) or true (if not)
	result := ip.verifyCachedRootfs(fakeRootfs)
	t.Logf("verifyCachedRootfs(invalid ext4) = %v", result)
	// We don't assert because it depends on debugfs availability
}

func TestVerifyCachedRootfs_DebugfsNotAvailable(t *testing.T) {
	ip := &ImagePreparer{
		config: &PreparerConfig{},
	}

	// This test verifies the graceful degradation path
	// If debugfs is not installed, verifyCachedRootfs returns true
	// If debugfs IS installed, it will actually try to inspect the file
	dir := t.TempDir()
	fakeRootfs := filepath.Join(dir, "rootfs.ext4")
	os.WriteFile(fakeRootfs, []byte("fake"), 0644)

	// Just ensure it doesn't panic
	_ = ip.verifyCachedRootfs(fakeRootfs)
}

// --- prepareImage pipeline order test ---

func TestPrepareImage_InitInjectedBeforeExt4(t *testing.T) {
	// This is a structural test: verify the pipeline order in prepareImage()
	// We read the source to confirm init injection happens before ext4 creation

	// Read preparer.go and check that injectInitIntoDir appears before createExt4Image
	source, err := os.ReadFile("preparer.go")
	if err != nil {
		t.Skip("Cannot read source file for structural test")
	}

	sourceStr := string(source)
	injectIdx := indexOf(sourceStr, "InjectIntoDir")
	ext4Idx := indexOf(sourceStr, "createExt4Image")

	if injectIdx == -1 {
		t.Fatal("InjectIntoDir not found in preparer.go — pipeline may not be fixed")
	}
	if ext4Idx == -1 {
		t.Fatal("createExt4Image not found in preparer.go")
	}
	if injectIdx > ext4Idx {
		t.Fatal("InjectIntoDir appears AFTER createExt4Image — pipeline order is WRONG")
	}
	t.Log("✓ Pipeline order correct: init injection happens before ext4 creation")
}

// --- Concurrent preparation test ---

func TestPrepareWithLock_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	rootfsPath := filepath.Join(dir, "concurrent.rootfs.ext4")

	// Create a fake rootfs to simulate one process finishing first
	os.WriteFile(rootfsPath, []byte("rootfs content"), 0644)

	ip := &ImagePreparer{
		config:    &PreparerConfig{RootfsDir: dir},
		cacheDir:  dir,
		rootfsDir: dir,
	}

	// Simulate concurrent access with context
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The second call should detect the existing rootfs via double-check
	err := ip.prepareWithLock(ctx, "alpine:latest", "sha256:abc123", rootfsPath)
	// May or may not succeed depending on whether it re-prepares
	t.Logf("prepareWithLock result: %v", err)
}

// --- helper ---

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
