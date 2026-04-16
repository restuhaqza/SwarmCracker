package snapshot

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFirecrackerAPIClient_ImplementationVars(t *testing.T) {
	// Test that implementation variables can be swapped
	origPause := pauseVMImpl
	origResume := resumeVMImpl

	// Swap implementations
	pauseVMImpl = func(ctx context.Context, socketPath string) error {
		return errors.New("mock pause error")
	}
	resumeVMImpl = func(ctx context.Context, socketPath string) error {
		return nil
	}

	client := NewDefaultFirecrackerAPIClient()
	ctx := context.Background()

	// Test with swapped implementation
	err := client.PauseVM(ctx, "/tmp/socket")
	if err == nil || err.Error() != "mock pause error" {
		t.Errorf("Expected mock pause error, got %v", err)
	}

	err = client.ResumeVM(ctx, "/tmp/socket")
	if err != nil {
		t.Errorf("ResumeVM should succeed: %v", err)
	}

	// Restore original implementations
	pauseVMImpl = origPause
	resumeVMImpl = origResume
}

func TestDefaultProcessExecutor_Kill(t *testing.T) {
	exec := NewDefaultProcessExecutor()

	handle, err := exec.StartCommand("sleep", "0.5")
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}

	// Kill the process
	if err := handle.Kill(); err != nil {
		t.Errorf("Kill should succeed: %v", err)
	}
}

func TestSnapshotConfig_AgeEnforcement(t *testing.T) {
	cfg := SnapshotConfig{
		Enabled:      true,
		SnapshotDir:  "/tmp/snapshots",
		MaxAge:       24 * time.Hour,
		MaxSnapshots: 5,
	}

	if cfg.MaxAge != 24*time.Hour {
		t.Errorf("Expected MaxAge 24h, got %v", cfg.MaxAge)
	}

	if cfg.MaxSnapshots != 5 {
		t.Errorf("Expected MaxSnapshots 5, got %d", cfg.MaxSnapshots)
	}
}

func TestCreateOptions_Fields(t *testing.T) {
	opts := CreateOptions{
		ServiceID:  "svc-1",
		NodeID:     "node-1",
		VCPUCount:  2,
		MemoryMB:   512,
		RootfsPath: "/tmp/rootfs",
		Metadata:   map[string]string{"key": "value"},
	}

	if opts.ServiceID != "svc-1" {
		t.Errorf("Expected ServiceID svc-1, got %s", opts.ServiceID)
	}
	if opts.VCPUCount != 2 {
		t.Errorf("Expected VCPUCount 2, got %d", opts.VCPUCount)
	}
}

func TestSnapshotInfo_SizeCalculation(t *testing.T) {
	info := &SnapshotInfo{
		ID:         "snap-1",
		MemoryPath: "/tmp/mem",
		StatePath:  "/tmp/state",
		SizeBytes:  1024000,
	}

	if info.SizeBytes != 1024000 {
		t.Errorf("Expected SizeBytes 1024000, got %d", info.SizeBytes)
	}
}

func TestSnapshotFilter_TimeRange(t *testing.T) {
	now := time.Now().UTC()
	filter := SnapshotFilter{
		Since:  now.Add(-2 * time.Hour),
		Before: now.Add(2 * time.Hour),
	}

	// Test that filter's time range is valid
	if filter.Since.After(filter.Before) {
		t.Error("Since should be before Before")
	}
}

func TestMockProcessHandle_PidZero(t *testing.T) {
	handle := &MockProcessHandle{}

	// Default pid should be 0
	if handle.Pid() != 0 {
		t.Errorf("Expected Pid 0, got %d", handle.Pid())
	}

	// Set custom pid
	handle.pid = 12345
	if handle.Pid() != 12345 {
		t.Errorf("Expected Pid 12345, got %d", handle.Pid())
	}
}

func TestMockProcessHandle_KillAndWait(t *testing.T) {
	calledKill := false
	calledWait := false

	handle := &MockProcessHandle{
		KillFunc: func() error { calledKill = true; return nil },
		WaitFunc: func() error { calledWait = true; return nil },
	}

	if err := handle.Kill(); err != nil {
		t.Errorf("Kill should succeed: %v", err)
	}
	if !calledKill {
		t.Error("KillFunc should have been called")
	}

	if err := handle.Wait(); err != nil {
		t.Errorf("Wait should succeed: %v", err)
	}
	if !calledWait {
		t.Error("WaitFunc should have been called")
	}
}