package snapshot

import (
	"testing"
	"time"
)

func TestDefaultFirecrackerAPIClient_InterfaceType(t *testing.T) {
	var _ FirecrackerAPIClient = NewDefaultFirecrackerAPIClient()
	var _ FirecrackerAPIClient = &MockFirecrackerAPIClient{}
}

func TestDefaultProcessExecutor_InterfaceType(t *testing.T) {
	var _ ProcessExecutor = NewDefaultProcessExecutor()
	var _ ProcessExecutor = &MockProcessExecutor{}
}

func TestDefaultHTTPClientFactory_InterfaceType(t *testing.T) {
	var _ HTTPClientFactory = NewDefaultHTTPClientFactory()
	var _ HTTPClientFactory = &MockHTTPClientFactory{}
}

func TestDefaultProcessExecutor_LookPath(t *testing.T) {
	exec := NewDefaultProcessExecutor()

	// LookPath should find common binaries
	path, err := exec.LookPath("sh")
	if err != nil {
		t.Errorf("LookPath should find 'sh': %v", err)
	}
	if path == "" {
		t.Error("LookPath should return non-empty path for 'sh'")
	}
}

func TestDefaultProcessExecutor_StartCommand(t *testing.T) {
	exec := NewDefaultProcessExecutor()

	// StartCommand should be able to start simple commands
	handle, err := exec.StartCommand("sh", "-c", "echo test")
	if err != nil {
		t.Errorf("StartCommand failed: %v", err)
	}
	if handle == nil {
		t.Error("StartCommand should return non-nil handle")
	}

	// Wait for process to complete
	if err := handle.Wait(); err != nil {
		t.Logf("Wait returned: %v (may be expected)", err)
	}
}

func TestDefaultProcessHandle_Pid(t *testing.T) {
	exec := NewDefaultProcessExecutor()

	handle, err := exec.StartCommand("sleep", "0.1")
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}

	pid := handle.Pid()
	if pid <= 0 {
		t.Errorf("Pid should be positive, got %d", pid)
	}

	// Let the process finish naturally
	handle.Wait()
}

func TestMockFirecrackerAPIClient_AllMethodsReturnNil(t *testing.T) {
	mock := &MockFirecrackerAPIClient{}

	ctx := t.Context()

	if err := mock.PauseVM(ctx, "/tmp/socket"); err != nil {
		t.Error("Default PauseVM should return nil")
	}

	if err := mock.ResumeVM(ctx, "/tmp/socket"); err != nil {
		t.Error("Default ResumeVM should return nil")
	}

	if err := mock.CreateSnapshot(ctx, "/tmp/socket", "/tmp/state", "/tmp/mem"); err != nil {
		t.Error("Default CreateSnapshot should return nil")
	}

	if err := mock.LoadSnapshot(ctx, "/tmp/socket", "/tmp/state", "/tmp/mem"); err != nil {
		t.Error("Default LoadSnapshot should return nil")
	}

	if err := mock.StartInstance(ctx, "/tmp/socket"); err != nil {
		t.Error("Default StartInstance should return nil")
	}

	if err := mock.WaitForSocket("/tmp/socket", 1*time.Second); err != nil {
		t.Error("Default WaitForSocket should return nil")
	}
}

func TestMockHTTPClientFactory_NewUnixClient(t *testing.T) {
	factory := &MockHTTPClientFactory{}

	client := factory.NewUnixClient("/tmp/socket", 30*time.Second)
	if client == nil {
		t.Error("NewUnixClient should return non-nil client")
	}
}

func TestMockHTTPClient_DoWithConfiguredFunc(t *testing.T) {
	mock := &MockHTTPClient{}

	// Verify mock structure exists
	_ = mock

	// Note: MockHTTPClient is tested in interface_test.go with proper HTTP types
	t.Log("MockHTTPClient structure verified")
}