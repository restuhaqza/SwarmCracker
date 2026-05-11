package image

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TestExtractTarStream tests the extractTarStream function with various tar contents.
func TestExtractTarStream(t *testing.T) {
	t.Run("empty tar", func(t *testing.T) {
		dest := t.TempDir()
		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		tw.Close()

		err := extractTarStream(buf, dest)
		if err != nil {
			t.Fatalf("extractTarStream failed: %v", err)
		}
	})

	t.Run("directory entry", func(t *testing.T) {
		dest := t.TempDir()
		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeDir,
			Name:     "subdir/",
			Mode:     0755,
		})
		tw.Close()

		err := extractTarStream(buf, dest)
		if err != nil {
			t.Fatalf("extractTarStream failed: %v", err)
		}

		info, err := os.Stat(filepath.Join(dest, "subdir"))
		if err != nil {
			t.Fatalf("subdir not created: %v", err)
		}
		if !info.IsDir() {
			t.Error("subdir is not a directory")
		}
	})

	t.Run("regular file", func(t *testing.T) {
		dest := t.TempDir()
		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		content := []byte("hello world")
		tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "test.txt",
			Mode:     0644,
			Size:     int64(len(content)),
		})
		tw.Write(content)
		tw.Close()

		err := extractTarStream(buf, dest)
		if err != nil {
			t.Fatalf("extractTarStream failed: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(dest, "test.txt"))
		if err != nil {
			t.Fatalf("test.txt not created: %v", err)
		}
		if string(data) != "hello world" {
			t.Errorf("expected 'hello world', got %q", string(data))
		}
	})

	t.Run("nested file with parent dir", func(t *testing.T) {
		dest := t.TempDir()
		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		content := []byte("nested content")
		tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "a/b/c/file.txt",
			Mode:     0644,
			Size:     int64(len(content)),
		})
		tw.Write(content)
		tw.Close()

		err := extractTarStream(buf, dest)
		if err != nil {
			t.Fatalf("extractTarStream failed: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(dest, "a/b/c/file.txt"))
		if err != nil {
			t.Fatalf("nested file not created: %v", err)
		}
		if string(data) != "nested content" {
			t.Errorf("expected 'nested content', got %q", string(data))
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dest := t.TempDir()
		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		// Create target file first
		tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "target.txt",
			Mode:     0644,
			Size:     5,
		})
		tw.Write([]byte("hello"))
		// Create symlink
		tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeSymlink,
			Name:     "link.txt",
			Linkname: "target.txt",
		})
		tw.Close()

		err := extractTarStream(buf, dest)
		if err != nil {
			t.Fatalf("extractTarStream failed: %v", err)
		}

		link := filepath.Join(dest, "link.txt")
		fi, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("symlink not created: %v", err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Error("link.txt is not a symlink")
		}
	})

	t.Run("path traversal prevention", func(t *testing.T) {
		dest := t.TempDir()
		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "../../../etc/passwd",
			Mode:     0644,
			Size:     5,
		})
		tw.Write([]byte("hello"))
		tw.Close()

		err := extractTarStream(buf, dest)
		if err != nil {
			t.Fatalf("extractTarStream failed: %v", err)
		}

		// Path traversal was logged as skipped
		// Verify no file exists inside dest for the traversal path
		traversedPath := filepath.Join(dest, "etc/passwd")
		if _, err := os.Stat(traversedPath); err == nil {
			t.Error("path traversal file should not exist inside dest")
		}
	})

	t.Run("regular file with TypeRegA", func(t *testing.T) {
		dest := t.TempDir()
		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		content := []byte("regA content")
		tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeRegA,
			Name:     "regA.txt",
			Mode:     0644,
			Size:     int64(len(content)),
		})
		tw.Write(content)
		tw.Close()

		err := extractTarStream(buf, dest)
		if err != nil {
			t.Fatalf("extractTarStream failed: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(dest, "regA.txt"))
		if err != nil {
			t.Fatalf("regA.txt not created: %v", err)
		}
		if string(data) != "regA content" {
			t.Errorf("expected 'regA content', got %q", string(data))
		}
	})
}

// TestGenerateImageID tests image ID generation.
func TestGenerateImageIDUnit(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple image", "nginx", "nginx-latest"},
		{"with tag", "nginx:1.25", "nginx-1.25"},
		{"with registry", "docker.io/library/nginx:latest", "docker.io-library-nginx-latest"},
		{"with port", "registry.example.com:5000/myapp:v1", "registry.example.com:5000-myapp-v1"},
		{"no tag defaults to latest", "alpine", "alpine-latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateImageID(tt.input)
			if result != tt.expected {
				t.Errorf("generateImageID(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestGetDirSize tests directory size calculation.
func TestGetDirSizeUnit(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		dir := t.TempDir()
		size, err := getDirSize(dir)
		if err != nil {
			t.Fatalf("getDirSize failed: %v", err)
		}
		if size != 0 {
			t.Errorf("expected 0 for empty dir, got %d", size)
		}
	})

	t.Run("directory with files", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("hello world")
		if err := os.WriteFile(filepath.Join(dir, "test.txt"), content, 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		size, err := getDirSize(dir)
		if err != nil {
			t.Fatalf("getDirSize failed: %v", err)
		}
		if size != int64(len(content)) {
			t.Errorf("expected %d, got %d", len(content), size)
		}
	})

	t.Run("nested directories", func(t *testing.T) {
		dir := t.TempDir()
		subdir := filepath.Join(dir, "sub", "deep")
		if err := os.MkdirAll(subdir, 0755); err != nil {
			t.Fatalf("failed to create subdirectory: %v", err)
		}
		content := []byte("nested content")
		if err := os.WriteFile(filepath.Join(subdir, "file.txt"), content, 0644); err != nil {
			t.Fatalf("failed to write nested file: %v", err)
		}

		size, err := getDirSize(dir)
		if err != nil {
			t.Fatalf("getDirSize failed: %v", err)
		}
		if size != int64(len(content)) {
			t.Errorf("expected %d, got %d", len(content), size)
		}
	})

	t.Run("non-existent directory", func(t *testing.T) {
		_, err := getDirSize("/nonexistent/path")
		if err == nil {
			t.Error("expected error for non-existent directory")
		}
	})
}

// TestFormatBytes tests byte formatting.
func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1536, "1.5 KB"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.input), func(t *testing.T) {
			result := formatBytes(tt.input)
			if result != tt.expected {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestCopyDirectory tests the copyDirectory function.
func TestCopyDirectory(t *testing.T) {
	t.Run("copy directory with files", func(t *testing.T) {
		srcDir := t.TempDir()
		dstDir := filepath.Join(t.TempDir(), "dst")

		// Create source structure
		if err := os.MkdirAll(filepath.Join(srcDir, "subdir"), 0755); err != nil {
			t.Fatalf("failed to create subdirectory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("content1"), 0644); err != nil {
			t.Fatalf("failed to write file1: %v", err)
		}
		if err := os.WriteFile(filepath.Join(srcDir, "subdir", "file2.txt"), []byte("content2"), 0644); err != nil {
			t.Fatalf("failed to write file2: %v", err)
		}

		if err := copyDirectory(srcDir, dstDir); err != nil {
			t.Fatalf("copyDirectory failed: %v", err)
		}

		// Verify files were copied
		data1, err := os.ReadFile(filepath.Join(dstDir, "file1.txt"))
		if err != nil {
			t.Fatalf("file1.txt not found: %v", err)
		}
		if string(data1) != "content1" {
			t.Errorf("file1 content mismatch: got %q", string(data1))
		}

		data2, err := os.ReadFile(filepath.Join(dstDir, "subdir", "file2.txt"))
		if err != nil {
			t.Fatalf("file2.txt not found: %v", err)
		}
		if string(data2) != "content2" {
			t.Errorf("file2 content mismatch: got %q", string(data2))
		}
	})

	t.Run("copy non-existent source", func(t *testing.T) {
		err := copyDirectory("/nonexistent/path", t.TempDir())
		if err == nil {
			t.Error("expected error for non-existent source")
		}
	})
}

// TestImagePreparerConfigDefaults tests PreparerConfig defaults.
func TestImagePreparerConfigDefaults(t *testing.T) {
	t.Run("default values are set", func(t *testing.T) {
		ip := NewImagePreparer(&PreparerConfig{
			RootfsDir: t.TempDir(),
		})
		prep := ip.(*ImagePreparer)

		if prep.config.InitSystem != "tini" {
			t.Errorf("expected InitSystem 'tini', got %q", prep.config.InitSystem)
		}
		if prep.config.InitGracePeriod != 10 {
			t.Errorf("expected InitGracePeriod 10, got %d", prep.config.InitGracePeriod)
		}
		if prep.config.MaxImageAgeDays != 7 {
			t.Errorf("expected MaxImageAgeDays 7, got %d", prep.config.MaxImageAgeDays)
		}
	})

	t.Run("nil config uses defaults", func(t *testing.T) {
		ip := NewImagePreparer(nil)
		prep := ip.(*ImagePreparer)

		if prep.config.RootfsDir == "" {
			t.Error("expected non-empty RootfsDir")
		}
	})
}

// TestPrepareValidation tests the Prepare method validation.
func TestPrepareValidation(t *testing.T) {
	ip := NewImagePreparer(&PreparerConfig{
		RootfsDir: t.TempDir(),
	})

	t.Run("nil task returns error", func(t *testing.T) {
		err := ip.Prepare(context.Background(), nil)
		if err == nil {
			t.Error("expected error for nil task")
		}
		if !strings.Contains(err.Error(), "cannot be nil") {
			t.Errorf("expected nil task error, got: %v", err)
		}
	})

	t.Run("task without runtime returns error", func(t *testing.T) {
		task := &types.Task{
			ID: "test-task",
		}
		err := ip.Prepare(context.Background(), task)
		if err == nil {
			t.Error("expected error for task without runtime")
		}
		if !strings.Contains(err.Error(), "cannot be nil") {
			t.Errorf("expected runtime nil error, got: %v", err)
		}
	})

	t.Run("task with wrong runtime type returns error", func(t *testing.T) {
		task := &types.Task{
			ID:   "test-task",
			Spec: types.TaskSpec{Runtime: "not-a-container"},
		}
		err := ip.Prepare(context.Background(), task)
		if err == nil {
			t.Error("expected error for wrong runtime type")
		}
		if !strings.Contains(err.Error(), "not a container") {
			t.Errorf("expected container type error, got: %v", err)
		}
	})
}

// TestCleanup tests the Cleanup method.
func TestCleanup(t *testing.T) {
	t.Run("cleanup with no old files", func(t *testing.T) {
		ip := NewImagePreparer(&PreparerConfig{
			RootfsDir:       t.TempDir(),
			MaxImageAgeDays: 7,
		})

		filesRemoved, bytesFreed, err := ip.Cleanup(context.Background(), 7)
		if err != nil {
			t.Fatalf("Cleanup failed: %v", err)
		}
		if filesRemoved != 0 {
			t.Errorf("expected 0 files removed, got %d", filesRemoved)
		}
		if bytesFreed != 0 {
			t.Errorf("expected 0 bytes freed, got %d", bytesFreed)
		}
	})

	t.Run("cleanup removes old ext4 files", func(t *testing.T) {
		dir := t.TempDir()
		ip := NewImagePreparer(&PreparerConfig{
			RootfsDir:       dir,
			MaxImageAgeDays: 7,
		})
		prep := ip.(*ImagePreparer)

		// Create an old file
		oldFile := filepath.Join(dir, "nginx-latest.ext4")
		if err := os.WriteFile(oldFile, []byte("old image data"), 0644); err != nil {
			t.Fatalf("failed to create old file: %v", err)
		}
		// Set modification time to 10 days ago
		oldTime := time.Now().AddDate(0, 0, -10)
		if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
			t.Fatalf("failed to set file time: %v", err)
		}

		filesRemoved, bytesFreed, err := prep.Cleanup(context.Background(), 7)
		if err != nil {
			t.Fatalf("Cleanup failed: %v", err)
		}
		if filesRemoved != 1 {
			t.Errorf("expected 1 file removed, got %d", filesRemoved)
		}
		if bytesFreed == 0 {
			t.Error("expected bytes freed > 0")
		}

		// Verify file was removed
		if _, err := os.Stat(oldFile); err == nil {
			t.Error("old file should have been removed")
		}
	})

	t.Run("cleanup keeps recent files", func(t *testing.T) {
		dir := t.TempDir()
		ip := NewImagePreparer(&PreparerConfig{
			RootfsDir:       dir,
			MaxImageAgeDays: 7,
		})

		// Create a recent file
		recentFile := filepath.Join(dir, "alpine-latest.ext4")
		if err := os.WriteFile(recentFile, []byte("recent image data"), 0644); err != nil {
			t.Fatalf("failed to create recent file: %v", err)
		}

		filesRemoved, _, err := ip.Cleanup(context.Background(), 7)
		if err != nil {
			t.Fatalf("Cleanup failed: %v", err)
		}
		if filesRemoved != 0 {
			t.Errorf("expected 0 files removed for recent file, got %d", filesRemoved)
		}

		// Verify file still exists
		if _, err := os.Stat(recentFile); err != nil {
			t.Error("recent file should still exist")
		}
	})

	t.Run("cleanup non-existent directory", func(t *testing.T) {
		ip := NewImagePreparer(&PreparerConfig{
			RootfsDir:       "/nonexistent/path/for/test",
			MaxImageAgeDays: 7,
		})

		_, _, err := ip.Cleanup(context.Background(), 7)
		if err != nil {
			t.Errorf("Cleanup should not error on non-existent directory: %v", err)
		}
	})

	t.Run("cleanup with invalid keepDays", func(t *testing.T) {
		ip := NewImagePreparer(&PreparerConfig{
			RootfsDir:       t.TempDir(),
			MaxImageAgeDays: 7,
		})

		_, _, err := ip.Cleanup(context.Background(), 0)
		if err == nil {
			t.Error("expected error for keepDays=0")
		}
		_, _, err = ip.Cleanup(context.Background(), -1)
		if err == nil {
			t.Error("expected error for negative keepDays")
		}
	})

	t.Run("cleanup ignores non-ext4 files", func(t *testing.T) {
		dir := t.TempDir()
		ip := NewImagePreparer(&PreparerConfig{
			RootfsDir:       dir,
			MaxImageAgeDays: 7,
		})

		// Create a non-ext4 file that's old
		txtFile := filepath.Join(dir, "notes.txt")
		if err := os.WriteFile(txtFile, []byte("not an image"), 0644); err != nil {
			t.Fatalf("failed to create txt file: %v", err)
		}
		oldTime := time.Now().AddDate(0, 0, -10)
		if err := os.Chtimes(txtFile, oldTime, oldTime); err != nil {
			t.Fatalf("failed to set file time: %v", err)
		}

		filesRemoved, _, err := ip.Cleanup(context.Background(), 7)
		if err != nil {
			t.Fatalf("Cleanup failed: %v", err)
		}
		if filesRemoved != 0 {
			t.Errorf("expected 0 files removed for non-ext4 file, got %d", filesRemoved)
		}
	})
}

// TestValidateArchitecture tests architecture validation.
func TestValidateArchitecture(t *testing.T) {
	ip := NewImagePreparer(&PreparerConfig{
		RootfsDir: t.TempDir(),
	})
	prep := ip.(*ImagePreparer)

	err := prep.validateArchitecture()
	if err != nil {
		t.Errorf("validateArchitecture should pass on amd64/arm64, got: %v", err)
	}
}
