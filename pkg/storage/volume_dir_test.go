package storage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryDriver_CreateAndStat(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	path, err := d.Create(ctx, "test-vol", CreateOptions{SizeMB: 100})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if path == "" {
		t.Fatal("path should not be empty")
	}

	// Verify data subdirectory was created
	dataDir := filepath.Join(path, dirDataSubdir)
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("data dir not created: %v", err)
	}

	// Verify metadata
	info, err := d.Stat(ctx, "test-vol")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Name != "test-vol" {
		t.Errorf("Name = %q", info.Name)
	}
	if info.Type != VolumeTypeDir {
		t.Errorf("Type = %q", info.Type)
	}
	if info.SizeMB != 100 {
		t.Errorf("SizeMB = %d, want 100", info.SizeMB)
	}
}

func TestDirectoryDriver_CreateEmptyName(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = d.Create(context.Background(), "", CreateOptions{})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestDirectoryDriver_Delete(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "del-me", CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := d.Delete(ctx, "del-me"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = d.Stat(ctx, "del-me")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestDirectoryDriver_MountAndUnmount(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "mount-vol", CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	// Write some data into the volume
	dataDir := filepath.Join(dir, sanitizeVolumeName("mount-vol"), dirDataSubdir)
	if err := os.WriteFile(filepath.Join(dataDir, "test.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a fake rootfs
	rootfsDir := t.TempDir()
	targetDir := filepath.Join(rootfsDir, "data")

	// Mount
	if err := d.Mount(ctx, "mount-vol", rootfsDir, "/data"); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// Verify data was copied
	content, err := os.ReadFile(filepath.Join(targetDir, "test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello" {
		t.Errorf("content = %q, want %q", string(content), "hello")
	}

	// Modify data in rootfs
	if err := os.WriteFile(filepath.Join(targetDir, "new.txt"), []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}

	// Unmount (sync back)
	if err := d.Unmount(ctx, "mount-vol", rootfsDir, "/data", false); err != nil {
		t.Fatalf("Unmount: %v", err)
	}

	// Verify new data was synced back
	newContent, err := os.ReadFile(filepath.Join(dataDir, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(newContent) != "world" {
		t.Errorf("synced content = %q, want %q", string(newContent), "world")
	}
}

func TestDirectoryDriver_MountReadOnly(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "ro-vol", CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	rootfsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootfsDir, "data"), 0755); err != nil {
		t.Fatal(err)
	}

	// Mount
	if err := d.Mount(ctx, "ro-vol", rootfsDir, "/data"); err != nil {
		t.Fatal(err)
	}

	// Unmount read-only — should skip sync
	if err := d.Unmount(ctx, "ro-vol", rootfsDir, "/data", true); err != nil {
		t.Fatalf("Unmount read-only: %v", err)
	}
}

func TestDirectoryDriver_Capacity(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "cap-vol", CreateOptions{SizeMB: 100}); err != nil {
		t.Fatal(err)
	}

	used, limit, err := d.Capacity(ctx, "cap-vol")
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if limit != 100*1024*1024 {
		t.Errorf("limit = %d, want %d", limit, 100*1024*1024)
	}
	if used < 0 {
		t.Errorf("used = %d, should be >= 0", used)
	}
}

func TestDirectoryDriver_SnapshotAndRestore(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "snap-vol", CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	// Write data
	dataDir := filepath.Join(dir, sanitizeVolumeName("snap-vol"), dirDataSubdir)
	if err := os.WriteFile(filepath.Join(dataDir, "original.txt"), []byte("original data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Snapshot
	snap, err := d.Snapshot(ctx, "snap-vol")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Volume != "snap-vol" {
		t.Errorf("snap.Volume = %q", snap.Volume)
	}
	if _, err := os.Stat(snap.Path); err != nil {
		t.Fatalf("snapshot file not found: %v", err)
	}

	// Modify volume data
	if err := os.WriteFile(filepath.Join(dataDir, "modified.txt"), []byte("new data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Restore
	if err := d.Restore(ctx, "snap-vol", snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Original data should be back
	content, err := os.ReadFile(filepath.Join(dataDir, "original.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original data" {
		t.Errorf("after restore: content = %q", string(content))
	}

	// Modified file should be gone
	if _, err := os.Stat(filepath.Join(dataDir, "modified.txt")); !os.IsNotExist(err) {
		t.Error("modified.txt should not exist after restore")
	}
}

func TestDirectoryDriver_ExportImport(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "export-vol", CreateOptions{SizeMB: 50}); err != nil {
		t.Fatal(err)
	}

	// Write data
	dataDir := filepath.Join(dir, sanitizeVolumeName("export-vol"), dirDataSubdir)
	if err := os.WriteFile(filepath.Join(dataDir, "export.txt"), []byte("export me"), 0644); err != nil {
		t.Fatal(err)
	}

	// Export
	var buf bytes.Buffer
	if err := d.Export(ctx, "export-vol", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("export buffer should not be empty")
	}

	// Import into new volume
	if err := d.Import(ctx, "import-vol", &buf, 50); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// Verify imported data
	importDir := filepath.Join(dir, sanitizeVolumeName("import-vol"), dirDataSubdir)
	content, err := os.ReadFile(filepath.Join(importDir, "export.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "export me" {
		t.Errorf("imported content = %q", string(content))
	}
}

func TestDirectoryDriver_MountNonexistentVolume(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	rootfsDir := t.TempDir()
	err = d.Mount(context.Background(), "nope", rootfsDir, "/data")
	if err == nil {
		t.Fatal("expected error for nonexistent volume")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, should mention not found", err)
	}
}

// --- validateTarPath tests ---

func TestValidateTarPath(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		entry   string
		wantErr bool
	}{
		{name: "simple relative path", entry: "a/b", wantErr: false},
		{name: "nested file", entry: "dir/sub/file.txt", wantErr: false},
		{name: "dot path", entry: ".", wantErr: false},
		{name: "empty path", entry: "", wantErr: false},
		{name: "parent traversal", entry: "../escape", wantErr: true},
		{name: "embedded traversal", entry: "a/../../b", wantErr: true},
		{name: "null byte", entry: "a\x00b", wantErr: true},
		// Absolute paths are not rejected, but filepath.Join keeps the
		// cleaned result inside dest, so they are safe.
		{name: "absolute path contained by dest", entry: "/etc/passwd", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTarPath(dest, tt.entry)
			if tt.wantErr && err == nil {
				t.Fatalf("validateTarPath(%q) = nil, want error", tt.entry)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateTarPath(%q) = %v, want nil", tt.entry, err)
			}
		})
	}
}

// tarArchiveEntry describes one entry used to build a tar.gz test stream.
type tarArchiveEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
	linkname string
}

// buildTarGz builds an in-memory gzip-compressed tar stream from entries.
func buildTarGz(t *testing.T, entries ...tarArchiveEntry) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     e.mode,
			Linkname: e.linkname,
			Size:     int64(len(e.body)),
		}
		if e.typeflag != 0 {
			hdr.Typeflag = e.typeflag
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %q: %v", e.name, err)
		}
		if len(e.body) > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write tar body %q: %v", e.name, err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return &buf
}

// --- directory driver error-path tests ---

func TestDirectoryDriver_StatAndCapacity_MissingDataDir(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "gone-vol", CreateOptions{SizeMB: 42}); err != nil {
		t.Fatal(err)
	}

	// Remove the data directory but keep the metadata.
	if err := os.RemoveAll(d.dataPath("gone-vol")); err != nil {
		t.Fatal(err)
	}

	// Stat tolerates a missing data dir (logs a warning) and still reports.
	info, err := d.Stat(ctx, "gone-vol")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Name != "gone-vol" {
		t.Errorf("Name = %q, want %q", info.Name, "gone-vol")
	}
	if info.UsedMB != 0 {
		t.Errorf("UsedMB = %d, want 0 for missing data dir", info.UsedMB)
	}

	// Capacity surfaces the measurement error.
	if _, _, err := d.Capacity(ctx, "gone-vol"); err == nil {
		t.Fatal("expected Capacity error when data dir is missing")
	}
}

func TestDirectoryDriver_SnapshotSnapshotsDirBlocked(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "snap-blk", CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	// A regular file where the .snapshots directory should be makes MkdirAll fail.
	if err := os.WriteFile(filepath.Join(dir, ".snapshots"), []byte("not a dir"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Snapshot(ctx, "snap-blk"); err == nil {
		t.Fatal("expected Snapshot error when .snapshots path is a file")
	}
}

func TestDirectoryDriver_RestoreCorruptSnapshot(t *testing.T) {
	dir := testTempDir(t)
	d, err := NewDirectoryDriver(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := d.Create(ctx, "corrupt-vol", CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	badPath := filepath.Join(t.TempDir(), "bad.tar.gz")
	if err := os.WriteFile(badPath, []byte("this is not a gzip stream"), 0644); err != nil {
		t.Fatal(err)
	}

	err = d.Restore(ctx, "corrupt-vol", &Snapshot{ID: "bad", Volume: "corrupt-vol", Path: badPath})
	if err == nil {
		t.Fatal("expected error for corrupt snapshot")
	}
	if !strings.Contains(err.Error(), "decompress snapshot") {
		t.Errorf("error = %v, want it to mention decompress snapshot", err)
	}
}

func TestDirectoryDriver_RestoreRejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name    string
		entry   tarArchiveEntry
		wantSub string
	}{
		{
			name:    "path traversal",
			entry:   tarArchiveEntry{name: "../escape", body: "x", mode: 0644},
			wantSub: "invalid path",
		},
		{
			name:    "symlink linkname traversal",
			entry:   tarArchiveEntry{name: "link", typeflag: tar.TypeSymlink, linkname: "../escape"},
			wantSub: "invalid linkname",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := testTempDir(t)
			d, err := NewDirectoryDriver(dir)
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			if _, err := d.Create(ctx, "vol", CreateOptions{}); err != nil {
				t.Fatal(err)
			}

			snapPath := filepath.Join(t.TempDir(), "snap.tar.gz")
			if err := os.WriteFile(snapPath, buildTarGz(t, tt.entry).Bytes(), 0644); err != nil {
				t.Fatal(err)
			}

			err = d.Restore(ctx, "vol", &Snapshot{ID: "s", Volume: "vol", Path: snapPath})
			if err == nil {
				t.Fatal("expected Restore to reject unsafe entry")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %v, want substring %q", err, tt.wantSub)
			}
		})
	}
}

func TestDirectoryDriver_ImportRejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name    string
		entry   tarArchiveEntry
		wantSub string
	}{
		{
			name:    "path traversal",
			entry:   tarArchiveEntry{name: "../escape", body: "x", mode: 0644},
			wantSub: "invalid path",
		},
		{
			name:    "symlink linkname traversal",
			entry:   tarArchiveEntry{name: "link", typeflag: tar.TypeSymlink, linkname: "../escape"},
			wantSub: "invalid linkname",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := testTempDir(t)
			d, err := NewDirectoryDriver(dir)
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			buf := buildTarGz(t, tt.entry)

			err = d.Import(ctx, "vol", buf, 10)
			if err == nil {
				t.Fatal("expected Import to reject unsafe entry")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error = %v, want substring %q", err, tt.wantSub)
			}
		})
	}
}
