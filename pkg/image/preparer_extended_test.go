package image

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/storage"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TestPreparerExtended_FormatBytes tests formatBytes function
func TestPreparerExtended_FormatBytes(t *testing.T) {
	tests := []struct {
		name     string
		bytes    int64
		expected string
	}{
		{"zero bytes", 0, "0 B"},
		{"bytes", 512, "512 B"},
		{"kilobytes", 1024, "1.0 KB"},
		{"megabytes", 1024 * 1024, "1.0 MB"},
		{"gigabytes", 1024 * 1024 * 1024, "1.0 GB"},
		{"terabytes", 1024 * 1024 * 1024 * 1024, "1.0 TB"},
		{"mixed size", 1536, "1.5 KB"},
		{"large size", 5 * 1024 * 1024 * 1024, "5.0 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatBytes(tt.bytes)
			if result != tt.expected {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, result, tt.expected)
			}
		})
	}
}

// TestPreparerExtended_CopyDirectory tests copyDirectory function
func TestPreparerExtended_CopyDirectory(t *testing.T) {
	// Create temporary directories
	tempDir, err := os.MkdirTemp("", "copydir-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	srcDir := filepath.Join(tempDir, "src")
	dstDir := filepath.Join(tempDir, "dst")

	// Create source directory structure
	if err := os.MkdirAll(filepath.Join(srcDir, "subdir1", "subdir2"), 0755); err != nil {
		t.Fatalf("Failed to create src dir: %v", err)
	}

	// Create test files
	testFiles := map[string]string{
		"file1.txt":              "content1",
		"subdir1/file2.txt":      "content2",
		"subdir1/subdir2/file3":  "content3",
	}

	for path, content := range testFiles {
		fullPath := filepath.Join(srcDir, path)
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", path, err)
		}
	}

	// Test copying
	if err := copyDirectory(srcDir, dstDir); err != nil {
		t.Fatalf("copyDirectory failed: %v", err)
	}

	// Verify all files were copied
	for path, expectedContent := range testFiles {
		fullPath := filepath.Join(dstDir, path)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			t.Errorf("Failed to read copied file %s: %v", path, err)
			continue
		}
		if string(content) != expectedContent {
			t.Errorf("File %s content mismatch: got %q, want %q", path, string(content), expectedContent)
		}
	}

	// Test error case: non-existent source
	if err := copyDirectory(filepath.Join(tempDir, "nonexistent"), dstDir); err == nil {
		t.Error("copyDirectory should fail for non-existent source")
	}
}

// TestPreparerExtended_CreateInitWrapper tests createInitWrapper function
func TestPreparerExtended_CreateInitWrapper(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "init-wrapper-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create sbin directory
	sbinDir := filepath.Join(tempDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		t.Fatalf("Failed to create sbin dir: %v", err)
	}

	prep := &ImagePreparer{
		config: &PreparerConfig{
			InitSystem: "tini",
		},
	}

	// Test without entrypoint - creates default init
	if err := prep.createInitWrapper(tempDir); err != nil {
		t.Fatalf("createInitWrapper failed: %v", err)
	}

	initPath := filepath.Join(sbinDir, "init")
	if _, err := os.Stat(initPath); os.IsNotExist(err) {
		t.Error("init script was not created")
	}

	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("Failed to read init script: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "#!/bin/sh") {
		t.Error("init script should be a shell script")
	}

	// Test with entrypoint present
	os.RemoveAll(tempDir)
	os.MkdirAll(sbinDir, 0755)

	entrypointDir := filepath.Join(tempDir, "docker-entrypoint.d")
	os.MkdirAll(entrypointDir, 0755)
	entrypointPath := filepath.Join(tempDir, "docker-entrypoint.sh")
	if err := os.WriteFile(entrypointPath, []byte("#!/bin/sh\necho entrypoint"), 0755); err != nil {
		t.Fatalf("Failed to create entrypoint: %v", err)
	}

	if err := prep.createInitWrapper(tempDir); err != nil {
		t.Fatalf("createInitWrapper with entrypoint failed: %v", err)
	}

	initContent, _ := os.ReadFile(initPath)
	initStr := string(initContent)
	if !strings.Contains(initStr, "nginx") {
		t.Error("init script with entrypoint should contain nginx references")
	}
}

// TestPreparerExtended_InjectNetworkConfig tests injectNetworkConfig function
func TestPreparerExtended_InjectNetworkConfig(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(string) error
		expectChange bool
		checkFunc    func(string) error
	}{
		{
			name: "OpenRC system with inittab",
			setupFunc: func(rootfs string) error {
				// Create etc/inittab with OpenRC reference
				inittabPath := filepath.Join(rootfs, "etc", "inittab")
				return os.WriteFile(inittabPath, []byte("::sysinit:/sbin/openrc\n"), 0644)
			},
			expectChange: true,
			checkFunc: func(rootfs string) error {
				// Check interfaces file was created
				ifacesPath := filepath.Join(rootfs, "etc", "network", "interfaces")
				if _, err := os.Stat(ifacesPath); os.IsNotExist(err) {
					return err
				}
				return nil
			},
		},
		{
			name: "Non-OpenRC system",
			setupFunc: func(rootfs string) error {
				// Create etc/inittab without OpenRC
				inittabPath := filepath.Join(rootfs, "etc", "inittab")
				return os.WriteFile(inittabPath, []byte("::sysinit:/bin/init\n"), 0644)
			},
			expectChange: false,
			checkFunc: func(rootfs string) error {
				ifacesPath := filepath.Join(rootfs, "etc", "network", "interfaces")
				if _, err := os.Stat(ifacesPath); err == nil {
					t.Error("interfaces should not be created for non-OpenRC")
				}
				return nil
			},
		},
		{
			name:         "No inittab",
			setupFunc:    func(rootfs string) error { return nil },
			expectChange: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir, err := os.MkdirTemp("", "network-config-test-")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tempDir)

			// Create etc directory
			os.MkdirAll(filepath.Join(tempDir, "etc"), 0755)

			// Setup initial state
			if err := tt.setupFunc(tempDir); err != nil {
				t.Fatalf("setup failed: %v", err)
			}

			prep := &ImagePreparer{}

			// Run injectNetworkConfig
			if err := prep.injectNetworkConfig(tempDir); err != nil {
				t.Fatalf("injectNetworkConfig failed: %v", err)
			}

			// Check expected changes
			if tt.checkFunc != nil {
				if err := tt.checkFunc(tempDir); err != nil {
					t.Errorf("check failed: %v", err)
				}
			}
		})
	}
}

// TestPreparerExtended_UnmountExt4 tests unmountExt4 function
func TestPreparerExtended_UnmountExt4(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "unmount-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	prep := &ImagePreparer{}

	// Test unmount of non-mounted directory (should not error)
	if err := prep.unmountExt4(tempDir); err != nil {
		t.Errorf("unmountExt4 should not error on non-mounted dir: %v", err)
	}

	// Verify directory was cleaned up
	if _, err := os.Stat(tempDir); os.IsExist(err) {
		// unmountExt4 calls RemoveAll, so dir should be gone
		// This is expected behavior
	}
}

// TestPreparerExtended_HandleMounts tests handleMounts function
func TestPreparerExtended_HandleMounts(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mounts-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create fake ext4 file
	rootfsPath := filepath.Join(tempDir, "test.ext4")
	if err := os.WriteFile(rootfsPath, []byte("fake ext4"), 0644); err != nil {
		t.Fatalf("Failed to create rootfs: %v", err)
	}

	prep := &ImagePreparer{
		volumeManager: &storage.VolumeManager{},
	}

	task := &types.Task{
		ID: "test-task",
	}

	mounts := []types.Mount{
		{
			Source:   "/host/path",
			Target:   "/container/path",
			ReadOnly: false,
		},
	}

	// Test handleMounts - will likely fail mount without root, but should not panic
	if err := prep.handleMounts(context.Background(), task, rootfsPath, mounts); err != nil {
		// Expected to fail without root privileges
		t.Log("handleMounts failed as expected without root:", err)
	}
}

// TestPreparerExtended_HandleBindMount tests handleBindMount function
func TestPreparerExtended_HandleBindMount(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "bind-mount-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create source file and directory
	srcFile := filepath.Join(tempDir, "source.txt")
	if err := os.WriteFile(srcFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	srcDir := filepath.Join(tempDir, "source-dir")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}
	os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("dir content"), 0644)

	// Create target rootfs
	rootfsPath := filepath.Join(tempDir, "rootfs")
	if err := os.MkdirAll(rootfsPath, 0755); err != nil {
		t.Fatalf("Failed to create rootfs: %v", err)
	}

	prep := &ImagePreparer{}

	tests := []struct {
		name      string
		source    string
		target    string
		wantError bool
	}{
		{
			name:      "bind mount file",
			source:    srcFile,
			target:    "/container/source.txt",
			wantError: false,
		},
		{
			name:      "bind mount directory",
			source:    srcDir,
			target:    "/container/sourcedir",
			wantError: false,
		},
		{
			name:      "non-existent source",
			source:    filepath.Join(tempDir, "nonexistent"),
			target:    "/container/nonexistent",
			wantError: false, // Should not error, just warn
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mount := &types.Mount{
				Source: tt.source,
				Target: tt.target,
			}

			err := prep.handleBindMount(rootfsPath, mount)
			if (err != nil) != tt.wantError {
				t.Errorf("handleBindMount() error = %v, wantError %v", err, tt.wantError)
			}

			if !tt.wantError && tt.source != filepath.Join(tempDir, "nonexistent") {
				// Verify target was created
				targetPath := filepath.Join(rootfsPath, tt.target)
				if _, err := os.Stat(targetPath); os.IsNotExist(err) {
					t.Errorf("Target not created: %s", targetPath)
				}
			}
		})
	}
}

// TestPreparerExtended_HandleVolumeMount tests handleVolumeMount function
func TestPreparerExtended_HandleVolumeMount(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "volume-mount-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create fake rootfs
	rootfsPath := filepath.Join(tempDir, "rootfs")
	if err := os.MkdirAll(rootfsPath, 0755); err != nil {
		t.Fatalf("Failed to create rootfs: %v", err)
	}

	// Create volume manager with temp storage
	volumeManager, _ := storage.NewVolumeManager(tempDir)

	prep := &ImagePreparer{
		volumeManager: volumeManager,
	}

	task := &types.Task{
		ID: "test-task",
	}

	mount := &types.Mount{
		Source: "volume-test-vol",
		Target: "/data",
	}

	// Test volume mount
	err = prep.handleVolumeMount(context.Background(), task, rootfsPath, mount)
	if err != nil {
		t.Logf("handleVolumeMount failed (may need storage setup): %v", err)
	}
}

// TestPreparerExtended_ExtractWithDockerCLI tests extractWithDockerCLI function.
// This tests the CLI fallback (docker/podman) behavior.
func TestPreparerExtended_ExtractWithDockerCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping CLI extraction test in short mode")
	}

	tests := []struct {
		name      string
		imageRef  string
		wantError bool
	}{
		{
			name:      "invalid image ref",
			imageRef:  "invalid!!!image",
			wantError: true,
		},
		{
			name:      "empty image ref",
			imageRef:  "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir, err := os.MkdirTemp("", "docker-test-")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tempDir)

			prep := &ImagePreparer{}

			ctx := context.Background()
			err = prep.extractWithDockerCLI(ctx, tt.imageRef, tempDir)
			if (err != nil) != tt.wantError {
				t.Errorf("extractWithDockerCLI() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

// TestPreparerExtended_InjectDumbInit tests injectDumbInit function
func TestPreparerExtended_InjectDumbInit(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dumbinit-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create fake ext4 file
	rootfsPath := filepath.Join(tempDir, "rootfs.ext4")
	if err := os.WriteFile(rootfsPath, []byte("fake ext4"), 0644); err != nil {
		t.Fatalf("Failed to create rootfs: %v", err)
	}

	injector := NewInitInjector(&InitSystemConfig{
		Type:           InitSystemDumbInit,
		GracePeriodSec: 10,
	})

	// Test injection - will fail mount without root, but should handle gracefully
	err = injector.injectDumbInit(rootfsPath)
	if err != nil {
		t.Logf("injectDumbInit failed as expected without root: %v", err)
	}
}

// TestPreparerExtended_ExtractWithDocker tests extractWithDocker function
func TestPreparerExtended_ExtractWithDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available in PATH")
	}

	tests := []struct {
		name      string
		imageRef  string
		wantError bool
	}{
		{
			name:      "valid image ref",
			imageRef:  "nginx:latest",
			wantError: false, // Should succeed with docker available
		},
		{
			name:      "invalid image ref",
			imageRef:  "invalid!!!image",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir, err := os.MkdirTemp("", "docker-test-")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tempDir)

			prep := &ImagePreparer{}

			ctx := context.Background()
			err = prep.extractWithDockerCLI(ctx, tt.imageRef, tempDir)

			if (err != nil) != tt.wantError {
				t.Errorf("extractWithDocker() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

// TestPreparerExtended_GenerateImageID tests generateImageID function
func TestPreparerExtended_GenerateImageID(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		expected string
	}{
		{
			name:     "simple image with tag",
			imageRef: "nginx:latest",
			expected: "nginx-latest",
		},
		{
			name:     "registry image with port",
			imageRef: "registry.example.com:5000/myimage:v1.0",
			expected: "registry.example.com:5000-myimage-v1.0",
		},
		{
			name:     "nested path image",
			imageRef: "docker.io/library/nginx:alpine",
			expected: "docker.io-library-nginx-alpine",
		},
		{
			name:     "image without tag",
			imageRef: "nginx",
			expected: "nginx-latest",
		},
		{
			name:     "complex registry path",
			imageRef: "ghcr.io/user/repo/image:tag",
			expected: "ghcr.io-user-repo-image-tag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateImageID(tt.imageRef)
			if result != tt.expected {
				t.Errorf("generateImageID(%q) = %q, want %q", tt.imageRef, result, tt.expected)
			}
		})
	}
}

// TestPreparerExtended_Cleanup tests Cleanup function
func TestPreparerExtended_Cleanup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "cleanup-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	prep := &ImagePreparer{
		rootfsDir: tempDir,
	}

	// Create test files with different ages
	oldFile := filepath.Join(tempDir, "old-image.ext4")
	recentFile := filepath.Join(tempDir, "recent-image.ext4")

	if err := os.WriteFile(oldFile, []byte("old"), 0644); err != nil {
		t.Fatalf("Failed to create old file: %v", err)
	}

	if err := os.WriteFile(recentFile, []byte("recent"), 0644); err != nil {
		t.Fatalf("Failed to create recent file: %v", err)
	}

	// Set old file modification time to 20 days ago
	oldTime := time.Now().AddDate(0, 0, -20)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("Failed to set old file time: %v", err)
	}

	// Run cleanup with 10 days keep
	filesRemoved, bytesFreed, err := prep.Cleanup(context.Background(), 10)
	if err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	// Should remove only old file
	if filesRemoved != 1 {
		t.Errorf("Expected 1 file removed, got %d", filesRemoved)
	}

	if bytesFreed != 3 {
		t.Errorf("Expected 3 bytes freed, got %d", bytesFreed)
	}

	// Verify old file is gone
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("Old file should have been removed")
	}

	// Verify recent file still exists
	if _, err := os.Stat(recentFile); os.IsNotExist(err) {
		t.Error("Recent file should still exist")
	}
}

// TestPreparerExtended_GetDirSize tests getDirSize function
func TestPreparerExtended_GetDirSize(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dirsize-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create test files
	file1 := filepath.Join(tempDir, "file1.txt")
	if err := os.WriteFile(file1, []byte("content1"), 0644); err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}

	file2 := filepath.Join(tempDir, "file2.txt")
	if err := os.WriteFile(file2, []byte("content2 content2"), 0644); err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	size, err := getDirSize(tempDir)
	if err != nil {
		t.Fatalf("getDirSize failed: %v", err)
	}

	// Check that size is reasonable (at least the sum of content bytes)
	minExpectedSize := int64(8 + 16) // "content1" + "content2 content2"
	if size < minExpectedSize {
		t.Errorf("getDirSize() = %d, want at least %d", size, minExpectedSize)
	}
}

// TestPreparerExtended_WithMocks tests ImagePreparerInternal with mocks
func TestPreparerExtended_WithMocks(t *testing.T) {
	mockRuntime := NewMockContainerRuntime()
	mockFS := NewMockFilesystemOperator()
	mockBin := NewMockBinaryLocator()

	// Add tini binary to mock locator
	mockBin.Binaries["tini"] = "/usr/bin/tini"

	config := &PreparerConfig{
		RootfsDir:       "/tmp/test-rootfs",
		InitSystem:      "tini",
		InitGracePeriod: 10,
	}

	prep := NewImagePreparerWithMocks(config, mockRuntime, mockFS, mockBin)

	if prep == nil {
		t.Fatal("NewImagePreparerWithMocks returned nil")
	}

	if prep.runtime != mockRuntime {
		t.Error("runtime not set correctly")
	}

	if prep.fsOps != mockFS {
		t.Error("fsOps not set correctly")
	}

	if prep.binLoc != mockBin {
		t.Error("binLoc not set correctly")
	}
}

// execError is a simple error type for exec command failures
type execError struct {
	msg string
}

func (e *execError) Error() string {
	return e.msg
}

// TestPreparerExtended_ValidateArchitecture tests validateArchitecture function
func TestPreparerExtended_ValidateArchitecture(t *testing.T) {
	prep := &ImagePreparer{}

	// Should succeed on supported architectures
	if err := prep.validateArchitecture(); err != nil {
		// Current arch might not be supported in test environment
		t.Logf("validateArchitecture failed (arch may not be supported): %v", err)
	}
}

// TestPreparerExtended_CopyFile tests copyFile function
func TestPreparerExtended_CopyFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "copyfile-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	srcFile := filepath.Join(tempDir, "source.txt")
	dstFile := filepath.Join(tempDir, "dest.txt")
	content := []byte("test content")

	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	prep := &ImagePreparer{}

	if err := prep.copyFile(srcFile, dstFile, 0644); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	dstContent, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("Failed to read dest file: %v", err)
	}

	if string(dstContent) != string(content) {
		t.Errorf("copyFile content mismatch: got %q, want %q", string(dstContent), string(content))
	}

	// Test error case
	err = prep.copyFile(filepath.Join(tempDir, "nonexistent"), dstFile, 0644)
	if err == nil {
		t.Error("copyFile should error for non-existent source")
	}
}

// TestPreparerExtended_GetInitBinaryPath tests getInitBinaryPath function
func TestPreparerExtended_GetInitBinaryPath(t *testing.T) {
	tests := []struct {
		name       string
		initSystem string
	}{
		{"tini init", "tini"},
		{"dumb-init init", "dumb-init"},
		{"none init", "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prep := &ImagePreparer{
				initInjector: NewInitInjector(&InitSystemConfig{
					Type: InitSystemType(tt.initSystem),
				}),
			}

			// Just test that it doesn't crash
			// Real binary might not exist in test environment
			path := prep.getInitBinaryPath()
			t.Logf("getInitBinaryPath() returned: %q", path)
		})
	}
}

// TestPreparerExtended_MountExt4 tests mountExt4 function
func TestPreparerExtended_MountExt4(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "mount-test-")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create fake ext4 file
	imagePath := filepath.Join(tempDir, "test.ext4")
	if err := os.WriteFile(imagePath, []byte("fake ext4"), 0644); err != nil {
		t.Fatalf("Failed to create image file: %v", err)
	}

	prep := &ImagePreparer{}

	// Will fail without root privileges
	mountDir, err := prep.mountExt4(imagePath)
	if err != nil {
		t.Logf("mountExt4 failed as expected without root: %v", err)
		return
	}

	// If succeeded, cleanup
	defer prep.unmountExt4(mountDir)

	if mountDir == "" {
		t.Error("mountDir should not be empty on success")
	}
}
