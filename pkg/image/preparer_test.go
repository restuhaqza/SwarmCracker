package image

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewImagePreparer tests creating a new ImagePreparer
func TestNewImagePreparer(t *testing.T) {
	tests := []struct {
		name     string
		config   interface{}
		wantDir  string
		setupDir bool
	}{
		{
			name: "with PreparerConfig",
			config: &PreparerConfig{
				RootfsDir: "/tmp/test-rootfs",
			},
			wantDir:  "/tmp/test-rootfs",
			setupDir: false,
		},
		{
			name:     "with nil config",
			config:   nil,
			wantDir:  "/var/lib/firecracker/rootfs",
			setupDir: false,
		},
		{
			name: "with invalid config type",
			config: &struct {
				Field string
			}{Field: "invalid"},
			wantDir:  "/var/lib/firecracker/rootfs",
			setupDir: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preparer := NewImagePreparer(tt.config)
			assert.NotNil(t, preparer)

			ip, ok := preparer.(*ImagePreparer)
			require.True(t, ok, "Should return *ImagePreparer")
			assert.Equal(t, tt.wantDir, ip.rootfsDir)

			// Check that directory was created
			if tt.setupDir || tt.wantDir != "" {
				info, err := os.Stat(tt.wantDir)
				if err == nil {
					assert.True(t, info.IsDir(), "Should be a directory")
					// Cleanup
					os.RemoveAll(tt.wantDir)
				}
			}
		})
	}
}

// TestGenerateImageID tests image ID generation
func TestGenerateImageID(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		want     string
	}{
		{
			name:     "nginx latest",
			imageRef: "nginx:latest",
			want:     "nginx-latest",
		},
		{
			name:     "nginx with tag",
			imageRef: "nginx:alpine",
			want:     "nginx-alpine",
		},
		{
			name:     "no tag defaults to latest",
			imageRef: "nginx",
			want:     "nginx-latest",
		},
		{
			name:     "full path image",
			imageRef: "docker.io/library/nginx:latest",
			want:     "docker.io-library-nginx-latest",
		},
		{
			name:     "private registry",
			imageRef: "registry.example.com/myapp:v1.0.0",
			want:     "registry.example.com-myapp-v1.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateImageID(tt.imageRef)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestGetDirSize tests directory size calculation
func TestGetDirSize(t *testing.T) {
	t.Run("existing directory with files", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create test files
		testFiles := map[string]int64{
			"file1.txt": 1024,
			"file2.txt": 2048,
			"file3.txt": 512,
		}

		for name, size := range testFiles {
			path := filepath.Join(tmpDir, name)
			err := os.WriteFile(path, make([]byte, size), 0644)
			require.NoError(t, err)
		}

		// Create subdirectory with files
		subDir := filepath.Join(tmpDir, "subdir")
		os.Mkdir(subDir, 0755)
		err := os.WriteFile(filepath.Join(subDir, "file4.txt"), make([]byte, 1024), 0644)
		require.NoError(t, err)

		size, err := getDirSize(tmpDir)
		require.NoError(t, err)
		assert.Equal(t, int64(4608), size) // 1024 + 2048 + 512 + 1024
	})

	t.Run("empty directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		size, err := getDirSize(tmpDir)
		require.NoError(t, err)
		assert.Equal(t, int64(0), size)
	})

	t.Run("non-existent path", func(t *testing.T) {
		_, err := getDirSize("/non/existent/path")
		assert.Error(t, err)
	})
}

// TestImagePreparer_Prepare_Cached tests that cached images are reused
func TestImagePreparer_Prepare_Cached(t *testing.T) {
	tmpDir := t.TempDir()
	rootfsDir := filepath.Join(tmpDir, "rootfs")
	imageID := "nginx-latest"
	rootfsPath := filepath.Join(rootfsDir, imageID+".ext4")

	config := &PreparerConfig{
		RootfsDir: rootfsDir,
	}

	preparer := NewImagePreparer(config).(*ImagePreparer)

	// Create existing rootfs
	err := os.MkdirAll(rootfsDir, 0755)
	require.NoError(t, err)
	err = os.WriteFile(rootfsPath, []byte("existing image"), 0644)
	require.NoError(t, err)

	task := &types.Task{
		ID: "test-task",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:latest",
			},
		},
		Annotations: make(map[string]string),
	}

	ctx := context.Background()
	err = preparer.Prepare(ctx, task)
	require.NoError(t, err)
	assert.Equal(t, rootfsPath, task.Annotations["rootfs"])
}

// TestImagePreparer_Prepare_InvalidRuntime tests error handling for non-container runtime
func TestImagePreparer_Prepare_InvalidRuntime(t *testing.T) {
	preparer := NewImagePreparer(&PreparerConfig{}).(*ImagePreparer)

	task := &types.Task{
		ID: "test-task",
		Spec: types.TaskSpec{
			Runtime: "not-a-container",
		},
	}

	ctx := context.Background()
	err := preparer.Prepare(ctx, task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a container")
}

// TestImagePreparer_Prepare_NoContainerRuntime tests error when no container runtime available
func TestImagePreparer_Prepare_NoContainerRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	rootfsDir := filepath.Join(tmpDir, "rootfs")

	config := &PreparerConfig{
		RootfsDir: rootfsDir,
	}

	preparer := NewImagePreparer(config).(*ImagePreparer)

	task := &types.Task{
		ID: "test-task",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:latest",
			},
		},
		Annotations: make(map[string]string),
	}

	ctx := context.Background()
	err := preparer.Prepare(ctx, task)

	// This will fail if no container runtime is available
	// That's expected behavior
	if err != nil {
		assert.Contains(t, err.Error(), "no container runtime found")
	}
}

// TestCreateExt4Image tests ext4 image creation
func TestCreateExt4Image(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test requiring mkfs.ext4")
	}

	// Check if mkfs.ext4 is available
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not available")
	}

	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	outputPath := filepath.Join(tmpDir, "output.ext4")

	// Create test source directory with some files
	err := os.MkdirAll(sourceDir, 0755)
	require.NoError(t, err)

	// Create some test files
	testFiles := []string{"test1.txt", "test2.txt", "subdir/test3.txt"}
	for _, file := range testFiles {
		fullPath := filepath.Join(sourceDir, file)
		dir := filepath.Dir(fullPath)
		os.MkdirAll(dir, 0755)
		err = os.WriteFile(fullPath, []byte("test content"), 0644)
		require.NoError(t, err)
	}

	preparer := &ImagePreparer{}

	err = preparer.createExt4Image(sourceDir, outputPath)
	require.NoError(t, err)

	// Verify output file exists
	info, err := os.Stat(outputPath)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))

	// Verify it's at least 100MB (minimum size)
	assert.Greater(t, info.Size(), int64(100*1024*1024))
}

// TestCreateExt4Image_NoMkfs tests error handling when mkfs.ext4 is not available
func TestCreateExt4Image_NoMkfs(t *testing.T) {
	// This test verifies error handling without requiring mkfs.ext4
	// We'll test by using a temporary directory that's not actually a valid source

	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "nonexistent")
	outputPath := filepath.Join(tmpDir, "output.ext4")

	preparer := &ImagePreparer{}
	err := preparer.createExt4Image(sourceDir, outputPath)

	// Should fail either because source doesn't exist or mkfs.ext4 fails
	assert.Error(t, err)
}

// TestImagePreparer_Prepare_Concurrent tests concurrent image preparation
func TestImagePreparer_Prepare_Concurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	tmpDir := t.TempDir()
	rootfsDir := filepath.Join(tmpDir, "rootfs")

	config := &PreparerConfig{
		RootfsDir: rootfsDir,
	}

	preparer := NewImagePreparer(config).(*ImagePreparer)

	numGoroutines := 10
	numTasks := 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errors := make(chan error, numTasks)
	start := make(chan struct{})

	// Launch multiple goroutines
	for i := 0; i < numGoroutines; i++ {
		go func(workerID int) {
			defer wg.Done()

			<-start // Wait for signal

			for j := 0; j < numTasks/numGoroutines; j++ {
				taskID := fmt.Sprintf("task-%d-%d", workerID, j)
				task := &types.Task{
					ID: taskID,
					Spec: types.TaskSpec{
						Runtime: &types.Container{
							Image: fmt.Sprintf("nginx:%d", j),
						},
					},
					Annotations: make(map[string]string),
				}

				ctx := context.Background()
				if err := preparer.Prepare(ctx, task); err != nil {
					// Expected to fail without actual container runtime
					// Just verify no panic or race condition
					errors <- err
				}
			}
		}(i)
	}

	// Start all goroutines at once
	close(start)
	wg.Wait()
	close(errors)

	// Collect errors (expected without container runtime)
	errorCount := 0
	for err := range errors {
		errorCount++
		_ = err // Expected errors without container runtime
	}

	// Should have attempted all tasks
	assert.Equal(t, numTasks, errorCount)
}

// TestImagePreparer_Cleanup tests cleanup functionality
func TestImagePreparer_Cleanup(t *testing.T) {
	tmpDir := t.TempDir()
	rootfsDir := filepath.Join(tmpDir, "rootfs")

	config := &PreparerConfig{
		RootfsDir: rootfsDir,
	}

	preparer := NewImagePreparer(config).(*ImagePreparer)

	// Create some old test files
	err := os.MkdirAll(rootfsDir, 0755)
	require.NoError(t, err)

	// Create recent file
	recentFile := filepath.Join(rootfsDir, "recent.ext4")
	err = os.WriteFile(recentFile, []byte("recent"), 0644)
	require.NoError(t, err)

	// Create old file
	oldFile := filepath.Join(rootfsDir, "old.ext4")
	err = os.WriteFile(oldFile, []byte("old"), 0644)
	require.NoError(t, err)

	// Make it appear old
	oldTime := time.Now().Add(-30 * 24 * time.Hour)
	err = os.Chtimes(oldFile, oldTime, oldTime)
	require.NoError(t, err)

	ctx := context.Background()
	err = preparer.Cleanup(ctx, 7) // Keep 7 days

	// Cleanup is not yet implemented, so should just return nil
	assert.NoError(t, err)

	// Files should still exist (cleanup TODO)
	_, err = os.Stat(recentFile)
	assert.NoError(t, err)
	_, err = os.Stat(oldFile)
	assert.NoError(t, err)
}

// TestImagePreparer_Prepare_ContextCancellation tests context cancellation
func TestImagePreparer_Prepare_ContextCancellation(t *testing.T) {
	t.Skip("Skipping - context cancellation behavior depends on container runtime availability")

	tmpDir := t.TempDir()
	rootfsDir := filepath.Join(tmpDir, "rootfs")

	config := &PreparerConfig{
		RootfsDir: rootfsDir,
	}

	preparer := NewImagePreparer(config).(*ImagePreparer)

	task := &types.Task{
		ID: "test-task",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "nginx:latest",
			},
		},
		Annotations: make(map[string]string),
	}

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := preparer.Prepare(ctx, task)
	// Should either succeed (cached) or fail with context error
	if err != nil {
		assert.Contains(t, strings.ToLower(err.Error()), "context")
	}
}

// TestGenerateImageID_Uniqueness tests that different images generate different IDs
func TestGenerateImageID_Uniqueness(t *testing.T) {
	images := []string{
		"nginx:latest",
		"nginx:alpine",
		"redis:latest",
		"postgres:14",
		"myapp:v1.0.0",
		"registry.example.com/myapp:v2.0.0",
	}

	ids := make(map[string]string)
	for _, img := range images {
		id := generateImageID(img)
		ids[id] = img
	}

	// All IDs should be unique
	assert.Equal(t, len(images), len(ids), "Each image should generate a unique ID")
}

// TestImagePreparer_Prepare_EmptyImage tests handling of empty image reference
func TestImagePreparer_Prepare_EmptyImage(t *testing.T) {
	preparer := NewImagePreparer(&PreparerConfig{}).(*ImagePreparer)

	task := &types.Task{
		ID: "test-task",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "",
			},
		},
		Annotations: make(map[string]string),
	}

	ctx := context.Background()
	err := preparer.Prepare(ctx, task)
	assert.Error(t, err)
}

// Benchmark tests
func BenchmarkGenerateImageID(b *testing.B) {
	imageRef := "docker.io/library/nginx:alpine"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		generateImageID(imageRef)
	}
}

func BenchmarkGetDirSize(b *testing.B) {
	// Create a test directory
	tmpDir := b.TempDir()
	for i := 0; i < 100; i++ {
		path := filepath.Join(tmpDir, fmt.Sprintf("file%d.txt", i))
		os.WriteFile(path, make([]byte, 1024), 0644)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		getDirSize(tmpDir)
	}
}
