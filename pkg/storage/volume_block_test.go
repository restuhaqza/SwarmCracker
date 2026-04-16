package storage

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewBlockDriver(t *testing.T) {
	tests := []struct {
		name    string
		baseDir string
		wantErr bool
	}{
		{
			name:    "valid directory",
			baseDir: t.TempDir(),
			wantErr: false,
		},
		{
			name:    "create new directory",
			baseDir: filepath.Join(t.TempDir(), "newdir"),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := NewBlockDriver(tt.baseDir)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewBlockDriver() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if d == nil {
					t.Fatal("expected non-nil driver")
				}
				if d.Type() != VolumeTypeBlock {
					t.Errorf("Type() = %v, want %v", d.Type(), VolumeTypeBlock)
				}
				if d.meta == nil {
					t.Error("meta store should not be nil")
				}
				if d.quota == nil {
					t.Error("quota enforcer should not be nil")
				}
				if d.mountDir == "" {
					t.Error("mountDir should not be empty")
				}
			}
		})
	}
}

func TestBlockDriver_Type(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	if d.Type() != VolumeTypeBlock {
		t.Errorf("Type() = %v, want %v", d.Type(), VolumeTypeBlock)
	}
}

func TestBlockDriver_imagePath(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	tests := []struct {
		name string
		want string
	}{
		{
			name: "test-vol",
			want: filepath.Join(tmpDir, "test-vol", blockImageFile),
		},
		{
			name: "another",
			want: filepath.Join(tmpDir, "another", blockImageFile),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.imagePath(tt.name)
			if got != tt.want {
				t.Errorf("imagePath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBlockDriver_mountPoint(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	tests := []struct {
		name string
		want string
	}{
		{
			name: "test-vol",
			want: filepath.Join(tmpDir, ".mounts", "test-vol"),
		},
		{
			name: "another",
			want: filepath.Join(tmpDir, ".mounts", "another"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.mountPoint(tt.name)
			if got != tt.want {
				t.Errorf("mountPoint() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBlockDriver_Create(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root privileges for mkfs.ext4")
	}

	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		name    string
		volName string
		opts    CreateOptions
		wantErr bool
	}{
		{
			name:    "valid creation",
			volName: "test-vol-1",
			opts: CreateOptions{
				Type:   VolumeTypeBlock,
				SizeMB: 100,
			},
			wantErr: false,
		},
		{
			name:    "default size",
			volName: "test-vol-2",
			opts: CreateOptions{
				Type:   VolumeTypeBlock,
				SizeMB: 0,
			},
			wantErr: false,
		},
		{
			name:    "empty name",
			volName: "",
			opts: CreateOptions{
				Type:   VolumeTypeBlock,
				SizeMB: 100,
			},
			wantErr: true,
		},
		{
			name:    "negative size",
			volName: "test-vol-3",
			opts: CreateOptions{
				Type:   VolumeTypeBlock,
				SizeMB: -10,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := d.Create(ctx, tt.volName, tt.opts)
			if (err != nil) != tt.wantErr {
				t.Errorf("Create() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got == "" {
					t.Error("Create() returned empty path")
				}
				// Check that image file exists
				imgPath := d.imagePath(tt.volName)
				if _, err := os.Stat(imgPath); err != nil {
					t.Errorf("image file not created: %v", err)
				}
			}
		})
	}
}

func TestBlockDriver_Create_QuotaEnforcement(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()

	// Test with size too large (should fail quota check)
	// Negative sizes are defaulted to 1024 in Create(), so they won't fail quota
	_, err = d.Create(ctx, "test-vol", CreateOptions{Type: VolumeTypeBlock, SizeMB: 1024*1024 + 1})
	if err == nil {
		t.Error("expected quota error for size too large, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected size too large error, got: %v", err)
	}

	// Test with extremely large size (way over 1TB)
	_, err = d.Create(ctx, "test-vol2", CreateOptions{Type: VolumeTypeBlock, SizeMB: 10 * 1024 * 1024})
	if err == nil {
		t.Error("expected quota error for extremely large size, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected size too large error, got: %v", err)
	}
}

func TestBlockDriver_Delete(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()

	// Create a volume manually (without mkfs)
	volName := "test-vol"
	volDir := d.meta.volumeDir(volName)
	if err := os.MkdirAll(volDir, 0755); err != nil {
		t.Fatalf("failed to create volume dir: %v", err)
	}

	// Create metadata file
	m := &volumeMeta{
		Name:      volName,
		Type:      VolumeTypeBlock,
		SizeMB:    100,
		CreatedAt: time.Now().UTC(),
	}
	if err := d.meta.Write(ctx, m); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	// Test delete
	err = d.Delete(ctx, volName)
	if err != nil {
		t.Errorf("Delete() error = %v", err)
	}

	// Verify directory is removed
	if _, err := os.Stat(volDir); !os.IsNotExist(err) {
		t.Error("volume directory still exists after delete")
	}
}

func TestBlockDriver_Stat(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()
	volName := "test-vol"
	sizeMB := 100

	// Create volume metadata
	volDir := d.meta.volumeDir(volName)
	if err := os.MkdirAll(volDir, 0755); err != nil {
		t.Fatalf("failed to create volume dir: %v", err)
	}

	m := &volumeMeta{
		Name:      volName,
		Type:      VolumeTypeBlock,
		SizeMB:    sizeMB,
		CreatedAt: time.Now().UTC(),
	}
	if err := d.meta.Write(ctx, m); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	// Create image file
	imgPath := d.imagePath(volName)
	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatalf("failed to create image: %v", err)
	}
	f.Truncate(int64(sizeMB) * 1024 * 1024)
	f.Close()

	// Test Stat
	info, err := d.Stat(ctx, volName)
	if err != nil {
		t.Errorf("Stat() error = %v", err)
	}

	if info == nil {
		t.Fatal("Stat() returned nil info")
	}

	if info.Name != volName {
		t.Errorf("Name = %v, want %v", info.Name, volName)
	}
	if info.Type != VolumeTypeBlock {
		t.Errorf("Type = %v, want %v", info.Type, VolumeTypeBlock)
	}
	if info.SizeMB != sizeMB {
		t.Errorf("SizeMB = %v, want %v", info.SizeMB, sizeMB)
	}
	if info.Path != imgPath {
		t.Errorf("Path = %v, want %v", info.Path, imgPath)
	}
}

func TestBlockDriver_Stat_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()

	_, err = d.Stat(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent volume")
	}
}

func TestBlockDriver_Capacity(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()
	volName := "test-vol"
	sizeMB := 100

	// Create volume metadata
	volDir := d.meta.volumeDir(volName)
	if err := os.MkdirAll(volDir, 0755); err != nil {
		t.Fatalf("failed to create volume dir: %v", err)
	}

	m := &volumeMeta{
		Name:      volName,
		Type:      VolumeTypeBlock,
		SizeMB:    sizeMB,
		CreatedAt: time.Now().UTC(),
	}
	if err := d.meta.Write(ctx, m); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	// Create image file
	imgPath := d.imagePath(volName)
	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatalf("failed to create image: %v", err)
	}
	expectedSize := int64(sizeMB) * 1024 * 1024
	f.Truncate(expectedSize)
	f.Close()

	// Test Capacity
	used, limit, err := d.Capacity(ctx, volName)
	if err != nil {
		t.Errorf("Capacity() error = %v", err)
	}

	if used != expectedSize {
		t.Errorf("used = %v, want %v", used, expectedSize)
	}
	if limit != expectedSize {
		t.Errorf("limit = %v, want %v", limit, expectedSize)
	}
}

func TestBlockDriver_Snapshot(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()
	volName := "test-vol"

	// Create volume directory and image
	volDir := d.meta.volumeDir(volName)
	if err := os.MkdirAll(volDir, 0755); err != nil {
		t.Fatalf("failed to create volume dir: %v", err)
	}

	imgPath := d.imagePath(volName)
	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatalf("failed to create image: %v", err)
	}
	f.Truncate(1024 * 1024 * 100) // 100 MB
	f.Close()

	// Test Snapshot
	snap, err := d.Snapshot(ctx, volName)
	if err != nil {
		t.Errorf("Snapshot() error = %v", err)
	}

	if snap == nil {
		t.Fatal("Snapshot() returned nil")
	}

	if snap.Volume != volName {
		t.Errorf("Volume = %v, want %v", snap.Volume, volName)
	}
	if snap.ID == "" {
		t.Error("ID should not be empty")
	}
	if snap.Path == "" {
		t.Error("Path should not be empty")
	}
	if snap.SizeMB <= 0 {
		t.Errorf("SizeMB = %v, want > 0", snap.SizeMB)
	}

	// Verify snapshot file exists
	if _, err := os.Stat(snap.Path); err != nil {
		t.Errorf("snapshot file not created: %v", err)
	}
}

func TestBlockDriver_Snapshot_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()

	_, err = d.Snapshot(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent volume")
	}
}

func TestBlockDriver_Restore(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()
	volName := "test-vol"

	// Create volume directory and image
	volDir := d.meta.volumeDir(volName)
	if err := os.MkdirAll(volDir, 0755); err != nil {
		t.Fatalf("failed to create volume dir: %v", err)
	}

	// Create original image with some content
	imgPath := d.imagePath(volName)
	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatalf("failed to create image: %v", err)
	}
	originalContent := []byte("original data")
	if _, err := f.Write(originalContent); err != nil {
		t.Fatalf("failed to write: %v", err)
	}
	f.Truncate(1024 * 1024 * 100)
	f.Close()

	// Create snapshot
	snap, err := d.Snapshot(ctx, volName)
	if err != nil {
		t.Fatalf("Snapshot() = %v", err)
	}

	// Modify the original image
	f, err = os.Create(imgPath)
	if err != nil {
		t.Fatalf("failed to reopen image: %v", err)
	}
	newContent := []byte("modified data")
	if _, err := f.Write(newContent); err != nil {
		t.Fatalf("failed to write: %v", err)
	}
	f.Close()

	// Restore from snapshot
	err = d.Restore(ctx, volName, snap)
	if err != nil {
		t.Errorf("Restore() error = %v", err)
	}

	// Verify file was restored (size should match snapshot)
	info, err := os.Stat(imgPath)
	if err != nil {
		t.Errorf("failed to stat restored image: %v", err)
	}
	expectedSize := int64(snap.SizeMB * 1024 * 1024)
	if info.Size() != expectedSize {
		t.Errorf("restored size = %v, want %v", info.Size(), expectedSize)
	}
}

func TestBlockDriver_Restore_NilSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()

	err = d.Restore(ctx, "test-vol", nil)
	if err == nil {
		t.Error("expected error for nil snapshot")
	}
}

func TestBlockDriver_Export(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()
	volName := "test-vol"

	// Create volume directory and image
	volDir := d.meta.volumeDir(volName)
	if err := os.MkdirAll(volDir, 0755); err != nil {
		t.Fatalf("failed to create volume dir: %v", err)
	}

	imgPath := d.imagePath(volName)
	testData := []byte("test export data")
	if err := os.WriteFile(imgPath, testData, 0644); err != nil {
		t.Fatalf("failed to create image: %v", err)
	}

	// Test Export
	var buf bytes.Buffer
	err = d.Export(ctx, volName, &buf)
	if err != nil {
		t.Errorf("Export() error = %v", err)
	}

	if !bytes.Equal(buf.Bytes(), testData) {
		t.Errorf("Export() data = %v, want %v", buf.Bytes(), testData)
	}
}

func TestBlockDriver_Import(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root privileges for e2fsck")
	}

	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()
	volName := "test-vol"
	sizeMB := 50

	// Prepare test data (small ext4 image would be ideal, but we'll test with any data)
	testData := make([]byte, 1024*1024) // 1 MB

	// Test Import
	reader := bytes.NewReader(testData)
	err = d.Import(ctx, volName, reader, sizeMB)
	if err != nil {
		t.Errorf("Import() error = %v", err)
	}

	// Verify image file exists
	imgPath := d.imagePath(volName)
	if _, err := os.Stat(imgPath); err != nil {
		t.Errorf("image file not created: %v", err)
	}
}

func TestBlockDriver_ensureUnmounted(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	volName := "test-vol"

	// Test when not mounted (should succeed)
	err = d.ensureUnmounted(volName)
	if err != nil {
		t.Errorf("ensureUnmounted() error = %v", err)
	}
}

func TestCreateSparseFile(t *testing.T) {
	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "sparse.img")
	testSize := int64(1024 * 1024 * 100) // 100 MB

	err := createSparseFile(testPath, testSize)
	if err != nil {
		t.Errorf("createSparseFile() error = %v", err)
	}

	// Verify file exists
	info, err := os.Stat(testPath)
	if err != nil {
		t.Errorf("failed to stat created file: %v", err)
	}

	if info.Size() != testSize {
		t.Errorf("file size = %v, want %v", info.Size(), testSize)
	}

	// Verify it's actually sparse (file size should match requested size)
	if info.Size() != testSize {
		t.Errorf("file size = %v, want %v", info.Size(), testSize)
	}
}

func TestCreateSparseFile_Error(t *testing.T) {
	// Test with invalid path
	err := createSparseFile("/root/invalid/path/img", 1024)
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestClearDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	testDir := filepath.Join(tmpDir, "testdir")

	// Create directory with contents
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}

	// Add some files and subdirectories
	files := []string{"file1.txt", "file2.txt", "subdir/file3.txt"}
	for _, f := range files {
		path := filepath.Join(testDir, f)
		if filepath.Dir(path) != testDir {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatalf("failed to create subdir: %v", err)
			}
		}
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}
	}

	// Clear directory
	err := clearDirectory(testDir)
	if err != nil {
		t.Errorf("clearDirectory() error = %v", err)
	}

	// Verify directory is empty
	entries, err := os.ReadDir(testDir)
	if err != nil {
		t.Errorf("failed to read directory: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("directory has %d entries, want 0", len(entries))
	}

	// Verify directory still exists
	if _, err := os.Stat(testDir); err != nil {
		t.Error("directory should still exist")
	}
}

func TestClearDirectory_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	testDir := filepath.Join(tmpDir, "testdir")

	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}

	// Clear empty directory (should succeed)
	err := clearDirectory(testDir)
	if err != nil {
		t.Errorf("clearDirectory() error = %v", err)
	}
}

func TestClearDirectory_NonExistent(t *testing.T) {
	err := clearDirectory("/nonexistent/path")
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

func TestBlockDriver_Mount_ErrorCases(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		name      string
		volName   string
		rootfs    string
		target    string
		setup     func() func()
		wantErr   bool
		errSubstr string
	}{
		{
			name:      "image not found",
			volName:   "nonexistent",
			rootfs:    t.TempDir(),
			target:    "/data",
			setup:     func() func() { return func() {} },
			wantErr:   true,
			errSubstr: "not found",
		},
		{
			name:    "target is a file blocking mkdir",
			volName: "test-vol-file",
			rootfs:  t.TempDir(),
			target:  "/data",
			setup: func() func() {
				// Create volume and image
				volDir := d.meta.volumeDir("test-vol-file")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("test-vol-file")
				f, _ := os.Create(imgPath)
				f.Close()
				// Create a file at the target path to block mkdir
				targetParent := filepath.Join(t.TempDir(), "data")
				os.MkdirAll(targetParent, 0755)
				return func() {}
			},
			wantErr:   true,
			errSubstr: "mount image",
		},
		{
			name:    "invalid target path",
			volName: "test-vol-invalid",
			rootfs:  t.TempDir(),
			target:  "invalid path with spaces",
			setup: func() func() {
				volDir := d.meta.volumeDir("test-vol-invalid")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("test-vol-invalid")
				f, _ := os.Create(imgPath)
				f.Close()
				return func() {}
			},
			wantErr: true,
		},
		{
			name:    "empty target path",
			volName: "test-vol-empty",
			rootfs:  t.TempDir(),
			target:  "",
			setup: func() func() {
				volDir := d.meta.volumeDir("test-vol-empty")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("test-vol-empty")
				f, _ := os.Create(imgPath)
				f.Close()
				return func() {}
			},
			wantErr: true,
		},
		{
			name:    "mount point creation failure",
			volName: "test-vol-mntfail",
			rootfs:  t.TempDir(),
			target:  "/data",
			setup: func() func() {
				volDir := d.meta.volumeDir("test-vol-mntfail")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("test-vol-mntfail")
				f, _ := os.Create(imgPath)
				f.Close()
				// Create mount point as a file to block mkdir
				mnt := d.mountPoint("test-vol-mntfail")
				os.WriteFile(mnt, []byte("block"), 0644)
				return func() {
					os.Remove(mnt)
				}
			},
			wantErr:   true,
			errSubstr: "create mount point",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := tt.setup()
			defer cleanup()

			err := d.Mount(ctx, tt.volName, tt.rootfs, tt.target)
			if (err != nil) != tt.wantErr {
				t.Errorf("Mount() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errSubstr != "" && err != nil {
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error = %v, should contain %q", err, tt.errSubstr)
				}
			}
		})
	}
}

func TestBlockDriver_Unmount_ReadOnly(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()
	volName := "test-vol"
	rootfs := t.TempDir()

	// Create volume
	volDir := d.meta.volumeDir(volName)
	if err := os.MkdirAll(volDir, 0755); err != nil {
		t.Fatalf("failed to create volume dir: %v", err)
	}

	m := &volumeMeta{
		Name:      volName,
		Type:      VolumeTypeBlock,
		SizeMB:    100,
		CreatedAt: time.Now().UTC(),
	}
	if err := d.meta.Write(ctx, m); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	imgPath := d.imagePath(volName)
	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatalf("failed to create image: %v", err)
	}
	f.Close()

	// Test read-only unmount (should skip sync)
	err = d.Unmount(ctx, volName, rootfs, "/data", true)
	if err != nil {
		t.Errorf("Unmount() read-only error = %v", err)
	}
}

func TestBlockDriver_Unmount_ErrorCases(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()
	rootfs := t.TempDir()

	tests := []struct {
		name      string
		volName   string
		target    string
		readOnly  bool
		setup     func() func()
		wantErr   bool
		errSubstr string
	}{
		{
			name:      "image not found",
			volName:   "nonexistent",
			target:    "/data",
			readOnly:  false,
			setup:     func() func() { return func() {} },
			wantErr:   true,
			errSubstr: "not found",
		},
		{
			name:     "metadata not found",
			volName:  "no-meta",
			target:   "/data",
			readOnly: false,
			setup: func() func() {
				// Create image but no metadata
				volDir := d.meta.volumeDir("no-meta")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("no-meta")
				f, _ := os.Create(imgPath)
				f.Close()
				return func() {}
			},
			wantErr:   true,
			errSubstr: "read volume metadata",
		},
		{
			name:     "quota check fails",
			volName:  "quota-fail",
			target:   "/quota-data",
			readOnly: false,
			setup: func() func() {
				volDir := d.meta.volumeDir("quota-fail")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("quota-fail")
				f, _ := os.Create(imgPath)
				f.Close()
				m := &volumeMeta{
					Name:      "quota-fail",
					Type:      VolumeTypeBlock,
					SizeMB:    100,
					CreatedAt: time.Now().UTC(),
				}
				d.meta.Write(ctx, m)
				// Create oversized source to fail quota
				targetPath := filepath.Join(rootfs, "quota-data")
				os.MkdirAll(targetPath, 0755)
				largeFile := filepath.Join(targetPath, "large")
				lf, _ := os.Create(largeFile)
				lf.Truncate(200 * 1024 * 1024) // 200 MB, over 100 MB quota
				lf.Close()
				return func() {
					// Clean up the large file
					os.RemoveAll(targetPath)
				}
			},
			wantErr:   true,
			errSubstr: "would exceed quota",
		},
		{
			name:     "mount point creation failure",
			volName:  "mnt-fail",
			target:   "/data",
			readOnly: false,
			setup: func() func() {
				volDir := d.meta.volumeDir("mnt-fail")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("mnt-fail")
				f, _ := os.Create(imgPath)
				f.Close()
				m := &volumeMeta{
					Name:      "mnt-fail",
					Type:      VolumeTypeBlock,
					SizeMB:    100,
					CreatedAt: time.Now().UTC(),
				}
				d.meta.Write(ctx, m)
				// Create mount point as a file to block mkdir
				mnt := d.mountPoint("mnt-fail")
				os.WriteFile(mnt, []byte("block"), 0644)
				return func() {
					os.Remove(mnt)
				}
			},
			wantErr:   true,
			errSubstr: "create mount point",
		},
		{
			name:     "source does not exist",
			volName:  "no-src",
			target:   "/nonexistent/path",
			readOnly: false,
			setup: func() func() {
				volDir := d.meta.volumeDir("no-src")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("no-src")
				f, _ := os.Create(imgPath)
				f.Close()
				m := &volumeMeta{
					Name:      "no-src",
					Type:      VolumeTypeBlock,
					SizeMB:    100,
					CreatedAt: time.Now().UTC(),
				}
				d.meta.Write(ctx, m)
				return func() {}
			},
			wantErr:   true,
			errSubstr: "mount image",
		},
		{
			name:     "invalid target path",
			volName:  "invalid-target",
			target:   "invalid path with\x00null",
			readOnly: false,
			setup: func() func() {
				volDir := d.meta.volumeDir("invalid-target")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("invalid-target")
				f, _ := os.Create(imgPath)
				f.Close()
				m := &volumeMeta{
					Name:      "invalid-target",
					Type:      VolumeTypeBlock,
					SizeMB:    100,
					CreatedAt: time.Now().UTC(),
				}
				d.meta.Write(ctx, m)
				return func() {}
			},
			wantErr: true,
		},
		{
			name:     "empty target path",
			volName:  "empty-target",
			target:   "",
			readOnly: false,
			setup: func() func() {
				volDir := d.meta.volumeDir("empty-target")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("empty-target")
				f, _ := os.Create(imgPath)
				f.Close()
				m := &volumeMeta{
					Name:      "empty-target",
					Type:      VolumeTypeBlock,
					SizeMB:    100,
					CreatedAt: time.Now().UTC(),
				}
				d.meta.Write(ctx, m)
				return func() {}
			},
			wantErr: true,
		},
		{
			name:     "rootfs does not exist",
			volName:  "no-rootfs",
			target:   "/data",
			readOnly: false,
			setup: func() func() {
				volDir := d.meta.volumeDir("no-rootfs")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("no-rootfs")
				f, _ := os.Create(imgPath)
				f.Close()
				m := &volumeMeta{
					Name:      "no-rootfs",
					Type:      VolumeTypeBlock,
					SizeMB:    100,
					CreatedAt: time.Now().UTC(),
				}
				d.meta.Write(ctx, m)
				return func() {}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := tt.setup()
			defer cleanup()

			err := d.Unmount(ctx, tt.volName, rootfs, tt.target, tt.readOnly)
			if (err != nil) != tt.wantErr {
				t.Errorf("Unmount() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errSubstr != "" && err != nil {
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error = %v, should contain %q", err, tt.errSubstr)
				}
			}
		})
	}
}

func TestBlockDriver_Mount_AdditionalErrorCases(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()

	tests := []struct {
		name      string
		volName   string
		rootfs    string
		target    string
		setup     func() func()
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "relative target path",
			volName: "rel-target",
			rootfs:  t.TempDir(),
			target:  "data/relative",
			setup: func() func() {
				volDir := d.meta.volumeDir("rel-target")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("rel-target")
				f, _ := os.Create(imgPath)
				f.Close()
				return func() {}
			},
			wantErr: true,
		},
		{
			name:    "rootfs is file not directory",
			volName: "rootfs-file",
			rootfs:  filepath.Join(t.TempDir(), "notadir"),
			target:  "/data",
			setup: func() func() {
				volDir := d.meta.volumeDir("rootfs-file")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("rootfs-file")
				f, _ := os.Create(imgPath)
				f.Close()
				// Create a file instead of directory for rootfs
				r := filepath.Join(t.TempDir(), "notadir")
				os.WriteFile(r, []byte("not a dir"), 0644)
				return func() {}
			},
			wantErr: true,
		},
		{
			name:    "dot in target path",
			volName: "dot-target",
			rootfs:  t.TempDir(),
			target:  "/data/../etc",
			setup: func() func() {
				volDir := d.meta.volumeDir("dot-target")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("dot-target")
				f, _ := os.Create(imgPath)
				f.Close()
				return func() {}
			},
			wantErr: true,
		},
		{
			name:    "trailing slash in target",
			volName: "trail-slash",
			rootfs:  t.TempDir(),
			target:  "/data/",
			setup: func() func() {
				volDir := d.meta.volumeDir("trail-slash")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("trail-slash")
				f, _ := os.Create(imgPath)
				f.Close()
				return func() {}
			},
			wantErr: true,
		},
		{
			name:    "multiple slashes in target",
			volName: "multi-slash",
			rootfs:  t.TempDir(),
			target:  "/data//test",
			setup: func() func() {
				volDir := d.meta.volumeDir("multi-slash")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("multi-slash")
				f, _ := os.Create(imgPath)
				f.Close()
				return func() {}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := tt.setup()
			defer cleanup()

			err := d.Mount(ctx, tt.volName, tt.rootfs, tt.target)
			if (err != nil) != tt.wantErr {
				t.Errorf("Mount() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errSubstr != "" && err != nil {
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error = %v, should contain %q", err, tt.errSubstr)
				}
			}
		})
	}
}

func TestBlockDriver_Unmount_AdditionalErrorCases(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()
	rootfs := t.TempDir()

	tests := []struct {
		name      string
		volName   string
		target    string
		readOnly  bool
		setup     func() func()
		wantErr   bool
		errSubstr string
	}{
		{
			name:     "relative target path",
			volName:  "rel-target",
			target:   "data/relative",
			readOnly: false,
			setup: func() func() {
				volDir := d.meta.volumeDir("rel-target")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("rel-target")
				f, _ := os.Create(imgPath)
				f.Close()
				m := &volumeMeta{
					Name:      "rel-target",
					Type:      VolumeTypeBlock,
					SizeMB:    100,
					CreatedAt: time.Now().UTC(),
				}
				d.meta.Write(ctx, m)
				return func() {}
			},
			wantErr: true,
		},
		{
			name:     "dot in target path",
			volName:  "dot-target",
			target:   "/data/../etc",
			readOnly: false,
			setup: func() func() {
				volDir := d.meta.volumeDir("dot-target")
				os.MkdirAll(volDir, 0755)
				imgPath := d.imagePath("dot-target")
				f, _ := os.Create(imgPath)
				f.Close()
				m := &volumeMeta{
					Name:      "dot-target",
					Type:      VolumeTypeBlock,
					SizeMB:    100,
					CreatedAt: time.Now().UTC(),
				}
				d.meta.Write(ctx, m)
				return func() {}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := tt.setup()
			defer cleanup()

			err := d.Unmount(ctx, tt.volName, rootfs, tt.target, tt.readOnly)
			if (err != nil) != tt.wantErr {
				t.Errorf("Unmount() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errSubstr != "" && err != nil {
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error = %v, should contain %q", err, tt.errSubstr)
				}
			}
		})
	}
}

func TestBlockDriver_Mount_Unmount_Integration(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root privileges for mount/umount")
	}

	tmpDir := t.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		t.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()
	volName := "test-vol"
	rootfs := t.TempDir()

	// Create volume
	_, err = d.Create(ctx, volName, CreateOptions{Type: VolumeTypeBlock, SizeMB: 100})
	if err != nil {
		t.Skipf("cannot create volume (needs mkfs.ext4): %v", err)
	}

	// Mount
	err = d.Mount(ctx, volName, rootfs, "/data")
	if err != nil {
		t.Logf("Mount() = %v", err)
		// Clean up
		d.ensureUnmounted(volName)
		t.SkipNow()
	}

	targetPath := filepath.Join(rootfs, "data")
	if _, err := os.Stat(targetPath); err != nil {
		t.Errorf("target directory not created: %v", err)
	}

	// Unmount
	err = d.Unmount(ctx, volName, rootfs, "/data", false)
	if err != nil {
		t.Errorf("Unmount() error = %v", err)
	}

	// Ensure cleanup
	d.ensureUnmounted(volName)
}

// Benchmark tests
func BenchmarkBlockDriver_Create(b *testing.B) {
	if os.Getuid() != 0 {
		b.Skip("requires root privileges")
	}

	tmpDir := b.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		b.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		volName := fmt.Sprintf("bench-vol-%d", i)
		_, _ = d.Create(ctx, volName, CreateOptions{Type: VolumeTypeBlock, SizeMB: 100})
	}
}

func BenchmarkBlockDriver_Stat(b *testing.B) {
	tmpDir := b.TempDir()
	d, err := NewBlockDriver(tmpDir)
	if err != nil {
		b.Fatalf("NewBlockDriver() = %v", err)
	}

	ctx := context.Background()
	volName := "test-vol"

	// Setup volume
	volDir := d.meta.volumeDir(volName)
	os.MkdirAll(volDir, 0755)
	m := &volumeMeta{
		Name:      volName,
		Type:      VolumeTypeBlock,
		SizeMB:    100,
		CreatedAt: time.Now().UTC(),
	}
	d.meta.Write(ctx, m)
	imgPath := d.imagePath(volName)
	f, _ := os.Create(imgPath)
	f.Truncate(1024 * 1024 * 100)
	f.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = d.Stat(ctx, volName)
	}
}
