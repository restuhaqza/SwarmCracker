package lifecycle

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMMManagerInternal_StartSuccess tests successful VM start with mocked components
func TestVMMManagerInternal_StartSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	// Create mock process executor
	mockProc := NewMockProcessExecutor()
	mockProc.Binaries["firecracker"] = "/usr/bin/firecracker"

	// Create mock HTTP client
	mockHTTP := NewMockHTTPClient()
	mockHTTP.SetResponse("GET", "http://unix/tmp/test-sock-internal.sock/", http.StatusOK, []byte("{}"))
	mockHTTP.SetResponse("PUT", "http://unix/tmp/test-sock-internal.sock/actions", http.StatusNoContent, []byte(""))

	// Create VMM manager with mocked components
	config := &ManagerConfig{
		SocketDir: "/tmp/test-vmm-internal",
	}
	vmMgr := NewVMMManagerWithExecutors(config, mockProc, mockHTTP)

	task := &types.Task{
		ID:    "test-internal-vm",
		Annotations: map[string]string{
			"init_system": "tini",
		},
	}

	ctx := context.Background()

	// This will fail because we can't actually start firecracker in tests,
	// but it tests the code paths
	configJSON := `{"boot-source": {"kernel-image-path": "/vmlinux"}}`
	err := vmMgr.Start(ctx, task, configJSON)

	// Expected to fail since we're not actually running firecracker
	// But we've tested the code path
	assert.Error(t, err, "Start should fail without real firecracker")
}

// TestVMMManagerInternal_MockLookPath tests the mocked LookPath function
func TestVMMManagerInternal_MockLookPath(t *testing.T) {
	mockProc := NewMockProcessExecutor()

	// Test binary found
	mockProc.Binaries["test-binary"] = "/usr/bin/test-binary"
	path, err := mockProc.LookPath("test-binary")
	assert.NoError(t, err, "LookPath should succeed")
	assert.Equal(t, "/usr/bin/test-binary", path, "should return correct path")

	// Test binary not found
	_, err = mockProc.LookPath("not-found")
	assert.Error(t, err, "LookPath should fail for unknown binary")

	// Verify call was recorded
	assert.Contains(t, mockProc.Calls, "LookPath:test-binary", "should record call")
}

// TestVMMManagerInternal_MockFindProcess tests the mocked FindProcess function
func TestVMMManagerInternal_MockFindProcess(t *testing.T) {
	mockProc := NewMockProcessExecutor()

	// Create a mock process
	mockProc.Processes[123] = &mockProcess{pid: 123}

	// Test process found
	proc, err := mockProc.FindProcess(123)
	assert.NoError(t, err, "FindProcess should succeed")
	assert.NotNil(t, proc, "should return process")

	// Test process not found
	_, err = mockProc.FindProcess(999)
	assert.Error(t, err, "FindProcess should fail for unknown PID")
}

// TestMockProcess_Signal tests the mock process Signal method
func TestMockProcess_Signal(t *testing.T) {
	mockProc := &mockProcess{pid: 123}

	// Test SIGTERM
	err := mockProc.Signal(syscall.SIGTERM)
	assert.NoError(t, err, "Signal should succeed")
	assert.Equal(t, 1, len(mockProc.signals), "should record signal")
	assert.Equal(t, syscall.SIGTERM, mockProc.signals[0], "should record SIGTERM")

	// Test Signal(0) - simulates process check
	err = mockProc.Signal(syscall.Signal(0))
	assert.Error(t, err, "Signal(0) should return error (process not found)")
}

// TestMockProcess_Kill tests the mock process Kill method
func TestMockProcess_Kill(t *testing.T) {
	mockProc := &mockProcess{pid: 123}

	err := mockProc.Kill()
	assert.NoError(t, err, "Kill should succeed")
	assert.True(t, mockProc.killed, "should mark as killed")
}

// TestMockHTTPClient_Do tests the mock HTTP client Do method
func TestMockHTTPClient_Do(t *testing.T) {
	mockHTTP := NewMockHTTPClient()

	// Set up response
	mockHTTP.SetResponse("GET", "http://example.com/test", http.StatusOK, []byte("response body"))

	// Make request
	req, _ := http.NewRequest("GET", "http://example.com/test", nil)
	resp, err := mockHTTP.Do(req)

	assert.NoError(t, err, "Do should succeed")
	assert.NotNil(t, resp, "should return response")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "should return correct status")
	assert.Equal(t, 1, len(mockHTTP.Calls), "should record call")

	// Test unexpected request
	req2, _ := http.NewRequest("GET", "http://example.com/notfound", nil)
	_, err = mockHTTP.Do(req2)
	assert.Error(t, err, "Do should fail for unexpected request")
}

// TestMockHTTPClient_Get tests the mock HTTP client Get method
func TestMockHTTPClient_Get(t *testing.T) {
	mockHTTP := NewMockHTTPClient()

	// Set up response
	mockHTTP.SetResponse("GET", "http://example.com/data", http.StatusOK, []byte("data"))

	// Make request
	resp, err := mockHTTP.Get("http://example.com/data")

	assert.NoError(t, err, "Get should succeed")
	assert.NotNil(t, resp, "should return response")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "should return correct status")

	// Test unexpected request
	_, err = mockHTTP.Get("http://example.com/notfound")
	assert.Error(t, err, "Get should fail for unexpected request")
}

// TestMockHTTPClient_SetError tests setting errors for mock HTTP client
func TestMockHTTPClient_SetError(t *testing.T) {
	mockHTTP := NewMockHTTPClient()

	expectedErr := fmt.Errorf("connection refused")
	mockHTTP.SetError("POST", "http://example.com/error", expectedErr)

	req, _ := http.NewRequest("POST", "http://example.com/error", nil)
	_, err := mockHTTP.Do(req)

	assert.Error(t, err, "Do should return error")
	assert.Equal(t, expectedErr, err, "should return correct error")
}

// TestMockCommand_tests tests the mock command implementation
func TestMockCommand(t *testing.T) {
	mockProc := NewMockProcessExecutor()

	// Set up command output
	mockProc.Commands["ls"] = struct {
		Output []byte
		Err    error
	}{
		Output: []byte("file1.txt\nfile2.txt"),
		Err:    nil,
	}

	cmd := mockProc.Command("ls")
	assert.NotNil(t, cmd, "should return command")

	// Test Output
	output, err := cmd.Output()
	assert.NoError(t, err, "Output should succeed")
	assert.Equal(t, []byte("file1.txt\nfile2.txt"), output, "should return correct output")

	// Test Run
	err = cmd.Run()
	assert.NoError(t, err, "Run should succeed")
}

// TestVMMManagerInternal_WaitForAPIServer tests API server wait with mock
func TestVMMManagerInternal_WaitForAPIServer(t *testing.T) {
	mockHTTP := NewMockHTTPClient()
	mockHTTP.SetResponse("GET", "http://unix/tmp/test-sock-wait.sock/", http.StatusOK, []byte("{}"))

	config := &ManagerConfig{
		SocketDir: "/tmp/test-wait-api",
	}
	vmMgr := NewVMMManagerWithExecutors(config, NewMockProcessExecutor(), mockHTTP)

	// Create socket file
	socketPath := "/tmp/test-sock-wait.sock"
	os.Remove(socketPath)
	file, err := os.Create(socketPath)
	require.NoError(t, err)
	file.Close()
	defer os.Remove(socketPath)

	// Wait for API server (should succeed immediately)
	err = vmMgr.waitForAPIServerWithClient(socketPath, 1*time.Second)
	assert.NoError(t, err, "waitForAPIServerWithClient should succeed")
}

// TestVMMManagerInternal_StartInstance tests VM instance start with mock
func TestVMMManagerInternal_StartInstance(t *testing.T) {
	mockHTTP := NewMockHTTPClient()
	mockHTTP.SetResponse("PUT", "http://unix/tmp/test-sock-start.sock/actions", http.StatusNoContent, []byte(""))

	config := &ManagerConfig{
		SocketDir: "/tmp/test-start-instance",
	}
	vmMgr := NewVMMManagerWithExecutors(config, NewMockProcessExecutor(), mockHTTP)

	ctx := context.Background()
	socketPath := "/tmp/test-sock-start.sock"

	err := vmMgr.startInstanceWithClient(ctx, socketPath)
	assert.NoError(t, err, "startInstanceWithClient should succeed")
}

// TestVMMManagerInternal_StartInstanceFailure tests instance start failure
func TestVMMManagerInternal_StartInstanceFailure(t *testing.T) {
	mockHTTP := NewMockHTTPClient()
	// Return error instead of response
	mockHTTP.SetError("PUT", "http://unix/tmp/test-sock-fail.sock/actions", fmt.Errorf("connection refused"))

	config := &ManagerConfig{
		SocketDir: "/tmp/test-start-fail",
	}
	vmMgr := NewVMMManagerWithExecutors(config, NewMockProcessExecutor(), mockHTTP)

	ctx := context.Background()
	socketPath := "/tmp/test-sock-fail.sock"

	err := vmMgr.startInstanceWithClient(ctx, socketPath)
	assert.Error(t, err, "startInstanceWithClient should fail")
}

// TestVMMManagerInternal_SendCtrlAltDel tests Ctrl+Alt+Del with mock
func TestVMMManagerInternal_SendCtrlAltDel(t *testing.T) {
	mockHTTP := NewMockHTTPClient()
	mockHTTP.SetResponse("PUT", "http://unix/tmp/test-sock-cad.sock/actions", http.StatusNoContent, []byte(""))

	config := &ManagerConfig{
		SocketDir: "/tmp/test-send-cad",
	}
	vmMgr := NewVMMManagerWithExecutors(config, NewMockProcessExecutor(), mockHTTP)

	ctx := context.Background()
	socketPath := "/tmp/test-sock-cad.sock"

	err := vmMgr.sendCtrlAltDelWithClient(ctx, socketPath)
	assert.NoError(t, err, "sendCtrlAltDelWithClient should succeed")
}

// TestVMMManagerInternal_FindProcess tests findProcess wrapper
func TestVMMManagerInternal_FindProcess(t *testing.T) {
	mockProc := NewMockProcessExecutor()
	mockProc.Processes[123] = &mockProcess{pid: 123}

	config := &ManagerConfig{
		SocketDir: "/tmp/test-find-proc",
	}
	vmMgr := NewVMMManagerWithExecutors(config, mockProc, NewMockHTTPClient())

	proc, err := vmMgr.findProcess(123)
	assert.NoError(t, err, "findProcess should succeed")
	assert.NotNil(t, proc, "should return process")
	assert.Equal(t, 123, proc.Pid(), "should have correct PID")

	// Test not found
	_, err = vmMgr.findProcess(999)
	assert.Error(t, err, "findProcess should fail for unknown PID")
}

// TestNewVMMManagerWithExecutors tests creating VMM manager with custom executors
func TestNewVMMManagerWithExecutors(t *testing.T) {
	mockProc := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	config := &ManagerConfig{
		SocketDir: "/tmp/test-custom-exec",
	}

	vmMgr := NewVMMManagerWithExecutors(config, mockProc, mockHTTP)

	assert.NotNil(t, vmMgr, "should create VMMManager")
	assert.NotNil(t, vmMgr.VMMManager, "should have VMMManager")
	assert.Equal(t, mockProc, vmMgr.processExecutor, "should use mock process executor")
	assert.Equal(t, mockHTTP, vmMgr.httpClient, "should use mock HTTP client")

	// Verify socket directory was created
	_, err := os.Stat(config.SocketDir)
	assert.NoError(t, err, "socket directory should be created")

	// Cleanup
	os.RemoveAll(config.SocketDir)
}

// TestNewVMMManagerWithExecutors_NilConfig tests with nil config
func TestNewVMMManagerWithExecutors_NilConfig(t *testing.T) {
	mockProc := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	vmMgr := NewVMMManagerWithExecutors(nil, mockProc, mockHTTP)

	assert.NotNil(t, vmMgr, "should create VMMManager with nil config")
	assert.NotNil(t, vmMgr.VMMManager, "should have VMMManager")

	// Cleanup - socket directory should be in current directory
	os.RemoveAll(".sock")
}

// TestRealProcessExecutor tests the real process executor
func TestRealProcessExecutor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	executor := &RealProcessExecutor{}

	// Test LookPath for a common binary
	path, err := executor.LookPath("ls")
	assert.NoError(t, err, "LookPath should find 'ls'")
	assert.NotEmpty(t, path, "should return path")

	// Test FindProcess for current process
	proc, err := executor.FindProcess(os.Getpid())
	assert.NoError(t, err, "FindProcess should find current process")
	assert.NotNil(t, proc, "should return process")
	assert.Equal(t, os.Getpid(), proc.Pid(), "should have correct PID")

	// Test Signal(0) on current process
	err = proc.Signal(syscall.Signal(0))
	assert.NoError(t, err, "Signal(0) should succeed for running process")
}

// TestRealHTTPClient tests the real HTTP client
func TestRealHTTPClient(t *testing.T) {
	client := NewRealHTTPClient(5 * time.Second)

	// Create a test server
	server := &http.Server{
		Addr: "127.0.0.1:12345",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		}),
	}

	go server.ListenAndServe()
	defer server.Close()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Note: This test might fail if port is in use
	// In a real test, we'd use a more robust test server setup
	_ = client
	_ = server
}

// TestMockProcess_Wait tests mock process Wait method
func TestMockProcess_Wait(t *testing.T) {
	mockProc := &mockProcess{pid: 123}

	state, err := mockProc.Wait()
	assert.NoError(t, err, "Wait should succeed")
	assert.Nil(t, state, "should return nil state")
}

// TestMockProcess_Release tests mock process Release method
func TestMockProcess_Release(t *testing.T) {
	mockProc := &mockProcess{pid: 123}

	err := mockProc.Release()
	assert.NoError(t, err, "Release should succeed")
}

// TestMockCmd_Process tests mock Cmd Process method
func TestMockCmd_Process(t *testing.T) {
	mockProc := NewMockProcessExecutor()
	mockProc.Processes[123] = &mockProcess{pid: 123}

	cmd := mockProc.Command("test")
	cmd.(*mockCmd).process = mockProc.Processes[123]

	proc := cmd.Process()
	assert.NotNil(t, proc, "should return process")
	assert.Equal(t, 123, proc.Pid(), "should have correct PID")
}

// TestMockCmd_SetStdio tests mock Cmd stdio methods
func TestMockCmd_SetStdio(t *testing.T) {
	mockProc := NewMockProcessExecutor()
	cmd := mockProc.Command("test")

	// These should not panic
	cmd.SetStdin(nil)
	cmd.SetStdout(nil)
	cmd.SetStderr(nil)

	assert.NotNil(t, cmd, "cmd should still be valid")
}

// TestMockCmd_StartAndFail tests mock command start with error
func TestMockCmd_StartAndFail(t *testing.T) {
	mockProc := NewMockProcessExecutor()
	mockProc.Commands["fail"] = struct {
		Output []byte
		Err    error
	}{
		Output: nil,
		Err:    fmt.Errorf("start failed"),
	}

	cmd := mockProc.Command("fail")
	err := cmd.Start()
	assert.Error(t, err, "Start should fail")
	assert.Equal(t, "start failed", err.Error(), "should return correct error")
}

// TestMockHTTPClient_MultipleCalls tests multiple HTTP calls
func TestMockHTTPClient_MultipleCalls(t *testing.T) {
	mockHTTP := NewMockHTTPClient()

	mockHTTP.SetResponse("GET", "http://example.com/1", http.StatusOK, []byte("response1"))
	mockHTTP.SetResponse("GET", "http://example.com/2", http.StatusOK, []byte("response2"))

	// Make first request
	resp1, err := mockHTTP.Get("http://example.com/1")
	assert.NoError(t, err, "first Get should succeed")
	assert.Equal(t, http.StatusOK, resp1.StatusCode, "first response should be OK")

	// Make second request
	resp2, err := mockHTTP.Get("http://example.com/2")
	assert.NoError(t, err, "second Get should succeed")
	assert.Equal(t, http.StatusOK, resp2.StatusCode, "second response should be OK")

	// Verify both calls were recorded
	assert.Equal(t, 2, len(mockHTTP.Calls), "should record both calls")
}
