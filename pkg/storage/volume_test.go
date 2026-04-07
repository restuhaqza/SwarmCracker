package storage

import (
	"context"
	"os"
	"testing"
)

func TestVolumeManager_CreateAndList(t *testing.T) {
	dir := testTempDir(t)
	vmm, err := NewVolumeManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Create volumes
	vol1, err := vmm.CreateVolume(ctx, "web-data", "task-1", 100)
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if vol1.Name != "web-data" {
		t.Errorf("Name = %q", vol1.Name)
	}
	if vol1.TaskID != "task-1" {
		t.Errorf("TaskID = %q", vol1.TaskID)
	}

	vol2, err := vmm.CreateVolumeWithOptions(ctx, "db-data", "task-2", CreateOptions{
		Type:   VolumeTypeDir,
		SizeMB: 500,
	})
	if err != nil {
		t.Fatalf("CreateVolumeWithOptions: %v", err)
	}
	if vol2.Name != "db-data" {
		t.Errorf("Name = %q", vol2.Name)
	}

	// List
	vols, err := vmm.ListVolumes(ctx)
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if len(vols) != 2 {
		t.Errorf("len(volumes) = %d, want 2", len(vols))
	}
}

func TestVolumeManager_GetVolume(t *testing.T) {
	dir := testTempDir(t)
	vmm, err := NewVolumeManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := vmm.CreateVolume(ctx, "find-me", "task-x", 50); err != nil {
		t.Fatal(err)
	}

	vol, err := vmm.GetVolume("find-me")
	if err != nil {
		t.Fatalf("GetVolume: %v", err)
	}
	if vol.Name != "find-me" {
		t.Errorf("Name = %q", vol.Name)
	}

	_, err = vmm.GetVolume("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent volume")
	}
}

func TestVolumeManager_DeleteVolume(t *testing.T) {
	dir := testTempDir(t)
	vmm, err := NewVolumeManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := vmm.CreateVolume(ctx, "delete-me", "", 0); err != nil {
		t.Fatal(err)
	}

	if err := vmm.DeleteVolume(ctx, "delete-me"); err != nil {
		t.Fatalf("DeleteVolume: %v", err)
	}

	_, err = vmm.GetVolume("delete-me")
	if err == nil {
		t.Error("volume should be gone after delete")
	}
}

func TestVolumeManager_MountAndUnmount(t *testing.T) {
	dir := testTempDir(t)
	vmm, err := NewVolumeManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	vol, err := vmm.CreateVolume(ctx, "mount-vol", "task-m", 50)
	if err != nil {
		t.Fatal(err)
	}

	rootfsDir := t.TempDir()

	// Mount via legacy API
	if err := vmm.MountVolume(ctx, vol, rootfsDir, "/app/data"); err != nil {
		t.Fatalf("MountVolume: %v", err)
	}

	// Verify data dir was created in rootfs
	targetDir := dirJoin(rootfsDir, "app/data")
	info, err := osStat(targetDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("target dir not created: %v", err)
	}

	// Unmount
	if err := vmm.UnmountVolume(ctx, vol, rootfsDir, "/app/data", true); err != nil {
		t.Fatalf("UnmountVolume: %v", err)
	}
}

func TestVolumeManager_SnapshotAndRestore(t *testing.T) {
	dir := testTempDir(t)
	vmm, err := NewVolumeManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := vmm.CreateVolume(ctx, "snap-vol", "", 50); err != nil {
		t.Fatal(err)
	}

	// Get snapshot
	snap, err := vmm.SnapshotVolume(ctx, "snap-vol")
	if err != nil {
		t.Fatalf("SnapshotVolume: %v", err)
	}
	if snap.Volume != "snap-vol" {
		t.Errorf("snap.Volume = %q", snap.Volume)
	}

	// Restore
	if err := vmm.RestoreVolume(ctx, "snap-vol", snap); err != nil {
		t.Fatalf("RestoreVolume: %v", err)
	}
}

func TestVolumeManager_CreateWithEmptyName(t *testing.T) {
	dir := testTempDir(t)
	vmm, err := NewVolumeManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = vmm.CreateVolume(context.Background(), "", "", 0)
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestVolumeManager_ListVolumeInfos(t *testing.T) {
	dir := testTempDir(t)
	vmm, err := NewVolumeManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := vmm.CreateVolume(ctx, "info-vol", "task-info", 200); err != nil {
		t.Fatal(err)
	}

	infos, err := vmm.ListVolumeInfos(ctx)
	if err != nil {
		t.Fatalf("ListVolumeInfos: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len(infos) = %d, want 1", len(infos))
	}
	if infos[0].Name != "info-vol" {
		t.Errorf("Name = %q", infos[0].Name)
	}
	if infos[0].SizeMB != 200 {
		t.Errorf("SizeMB = %d, want 200", infos[0].SizeMB)
	}
	if infos[0].TaskID != "task-info" {
		t.Errorf("TaskID = %q", infos[0].TaskID)
	}
}

// Test helpers
func osStat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func dirJoin(parts ...string) string {
	result := parts[0]
	for _, p := range parts[1:] {
		result += "/" + p
	}
	return result
}
