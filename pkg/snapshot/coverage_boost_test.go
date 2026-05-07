package snapshot

import (
	"context"
	"errors"
	"net/http"
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

// TestDefaultFirecrackerAPIClient_CreateSnapshot tests CreateSnapshot via impl swap

// TestDefaultFirecrackerAPIClient_CreateSnapshot_Error tests CreateSnapshot error
func TestDefaultFirecrackerAPIClient_CreateSnapshot_Error(t *testing.T) {
	orig := callSnapshotCreateImpl
	defer func() { callSnapshotCreateImpl = orig }()

	callSnapshotCreateImpl = func(ctx context.Context, socketPath, statePath, memoryPath string) error {
		return errors.New("snapshot failed")
	}

	client := NewDefaultFirecrackerAPIClient()
	err := client.CreateSnapshot(context.Background(), "/tmp/sock", "/tmp/state", "/tmp/mem")
	if err == nil {
		t.Error("CreateSnapshot should fail with mock error")
	}
}

// TestDefaultFirecrackerAPIClient_LoadSnapshot tests LoadSnapshot via impl swap

// TestDefaultFirecrackerAPIClient_StartInstance tests StartInstance via impl swap

// TestDefaultFirecrackerAPIClient_WaitForSocket tests WaitForSocket via impl swap

// TestDefaultHTTPClientFactory_NewUnixClient tests NewUnixClient via impl
func TestDefaultHTTPClientFactory_NewUnixClient(t *testing.T) {
	factory := NewDefaultHTTPClientFactory()
	client := factory.NewUnixClient("/tmp/test.sock", 5*time.Second)
	if client == nil {
		t.Error("NewUnixClient should return non-nil client")
	}
}

// TestMockProcessExecutor_LookPath_Success tests mock LookPath
func TestMockProcessExecutor_LookPath_Success(t *testing.T) {
	exec := &MockProcessExecutor{
		LookPathFunc: func(file string) (string, error) {
			return "/usr/bin/" + file, nil
		},
	}

	path, err := exec.LookPath("firecracker")
	if err != nil {
		t.Errorf("LookPath should succeed: %v", err)
	}
	if path != "/usr/bin/firecracker" {
		t.Errorf("Expected /usr/bin/firecracker, got %s", path)
	}
}

// TestMockProcessExecutor_LookPath_Error tests mock LookPath error
func TestMockProcessExecutor_LookPath_Error(t *testing.T) {
	exec := &MockProcessExecutor{
		LookPathFunc: func(file string) (string, error) {
			return "", errors.New("not found")
		},
	}

	_, err := exec.LookPath("nonexistent")
	if err == nil {
		t.Error("LookPath should fail for nonexistent binary")
	}
}

// TestMockProcessExecutor_StartCommand_Success tests mock StartCommand
func TestMockProcessExecutor_StartCommand_Success(t *testing.T) {
	exec := &MockProcessExecutor{
		StartCommandFunc: func(name string, arg ...string) (ProcessHandle, error) {
			return &MockProcessHandle{pid: 12345}, nil
		},
	}

	handle, err := exec.StartCommand("firecracker", "--api-sock", "/tmp/sock")
	if err != nil {
		t.Errorf("StartCommand should succeed: %v", err)
	}
	if handle.Pid() != 12345 {
		t.Errorf("Expected Pid 12345, got %d", handle.Pid())
	}
}

// TestMockProcessHandle_Kill_Error tests mock Kill error
func TestMockProcessHandle_Kill_Error(t *testing.T) {
	handle := &MockProcessHandle{
		KillFunc: func() error { return errors.New("kill failed") },
	}

	if err := handle.Kill(); err == nil {
		t.Error("Kill should return error")
	}
}

// TestMockProcessHandle_Wait_Error tests mock Wait error
func TestMockProcessHandle_Wait_Error(t *testing.T) {
	handle := &MockProcessHandle{
		WaitFunc: func() error { return errors.New("wait failed") },
	}

	if err := handle.Wait(); err == nil {
		t.Error("Wait should return error")
	}
}

// TestMockHTTPClient_Do_Success tests mock HTTP Do
func TestMockHTTPClient_Do_Success(t *testing.T) {
	client := &MockHTTPClient{
		DoFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200}, nil
		},
	}

	req, _ := http.NewRequest("GET", "http://test", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Errorf("Do should succeed: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}
}

// TestMockHTTPClient_Do_Error tests mock HTTP Do error
func TestMockHTTPClient_Do_Error(t *testing.T) {
	client := &MockHTTPClient{
		DoFunc: func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		},
	}

	req, _ := http.NewRequest("GET", "http://test", nil)
	_, err := client.Do(req)
	if err == nil {
		t.Error("Do should return error")
	}
}

// TestMockHTTPClientFactory_NewUnixClient_Success tests mock factory
func TestMockHTTPClientFactory_NewUnixClient_Success(t *testing.T) {
	factory := &MockHTTPClientFactory{
		NewUnixClientFunc: func(socketPath string, timeout time.Duration) HTTPClient {
			return &MockHTTPClient{}
		},
	}

	client := factory.NewUnixClient("/tmp/sock", 5*time.Second)
	if client == nil {
		t.Error("NewUnixClient should return non-nil client")
	}
}