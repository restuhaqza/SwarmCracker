package lifecycle

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRealProcessExecutor_FindProcess tests RealProcessExecutor.FindProcess
func TestRealProcessExecutor_FindProcess(t *testing.T) {
	executor := &RealProcessExecutor{}

	// Find current process (always succeeds on Unix)
	proc, err := executor.FindProcess(os.Getpid())
	require.NoError(t, err)
	require.NotNil(t, proc)
	assert.Equal(t, os.Getpid(), proc.Pid())

	// Find non-existent process (os.FindProcess always succeeds on Unix,
	// error only occurs when signaling)
	proc, err = executor.FindProcess(999999)
	require.NoError(t, err) // os.FindProcess doesn't error on Unix
	require.NotNil(t, proc)
}

// TestRealProcessExecutor_LookPath tests RealProcessExecutor.LookPath
func TestRealProcessExecutor_LookPath(t *testing.T) {
	executor := &RealProcessExecutor{}

	// Find a common binary
	path, err := executor.LookPath("ls")
	require.NoError(t, err)
	assert.NotEmpty(t, path)

	// Find another common binary
	path, err = executor.LookPath("cat")
	require.NoError(t, err)
	assert.NotEmpty(t, path)

	// Non-existent binary
	path, err = executor.LookPath("nonexistent-binary-xyz")
	require.Error(t, err)
	assert.Empty(t, path)
}

// TestRealProcessExecutor_Command tests RealProcessExecutor.Command
func TestRealProcessExecutor_Command(t *testing.T) {
	executor := &RealProcessExecutor{}

	cmd := executor.Command("echo", "hello")
	require.NotNil(t, cmd)

	// Test that it's a realCmd
	assert.Implements(t, (*Cmd)(nil), cmd)
}

// TestRealProcessExecutor_CommandContext tests RealProcessExecutor.CommandContext
func TestRealProcessExecutor_CommandContext(t *testing.T) {
	executor := &RealProcessExecutor{}
	ctx := context.Background()

	cmd := executor.CommandContext(ctx, "echo", "hello")
	require.NotNil(t, cmd)

	assert.Implements(t, (*Cmd)(nil), cmd)
}

// TestRealProcessExecutor_StartProcess tests RealProcessExecutor.StartProcess
func TestRealProcessExecutor_StartProcess(t *testing.T) {
	executor := &RealProcessExecutor{}

	// Create a command that exits quickly
	cmd := executor.Command("sleep", "0.1")
	require.NotNil(t, cmd)

	proc, err := executor.StartProcess(cmd)
	require.NoError(t, err)
	require.NotNil(t, proc)
	assert.Greater(t, proc.Pid(), 0)

	// Wait for process to finish
	_, err = proc.Wait()
	require.NoError(t, err)
}

// TestRealProcessExecutor_StartProcess_Error tests StartProcess with error
func TestRealProcessExecutor_StartProcess_Error(t *testing.T) {
	executor := &RealProcessExecutor{}

	// Create a command with non-existent binary
	cmd := executor.Command("nonexistent-binary-xyz")
	require.NotNil(t, cmd)

	proc, err := executor.StartProcess(cmd)
	require.Error(t, err)
	assert.Nil(t, proc)
}

// TestRealCmd_AllMethods tests all realCmd methods
func TestRealCmd_AllMethods(t *testing.T) {
	executor := &RealProcessExecutor{}

	// Test Output
	cmd := executor.Command("echo", "test-output")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "test-output")

	// Test CombinedOutput
	cmd = executor.Command("echo", "test-combined")
	output, err = cmd.CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(output), "test-combined")

	// Test Run
	cmd = executor.Command("true")
	err = cmd.Run()
	require.NoError(t, err)

	// Test Start + Wait
	cmd = executor.Command("sleep", "0.1")
	err = cmd.Start()
	require.NoError(t, err)
	err = cmd.Wait()
	require.NoError(t, err)
}

// TestRealCmd_SetStdio tests SetStdin, SetStdout, SetStderr
func TestRealCmd_SetStdio(t *testing.T) {
	executor := &RealProcessExecutor{}

	// Test SetStdin
	var stdin bytes.Buffer
	stdin.Write([]byte("test input"))
	cmd := executor.Command("cat")
	cmd.SetStdin(&stdin)

	var stdout bytes.Buffer
	cmd.SetStdout(&stdout)

	err := cmd.Run()
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), "test input")

	// Test SetStderr
	cmd = executor.Command("ls", "/nonexistent")
	var stderr bytes.Buffer
	cmd.SetStderr(&stderr)

	_ = cmd.Run() // Will error, but stderr should be captured
	// stderr may or may not have content depending on ls behavior
}

// TestRealCmd_Process tests Process method
func TestRealCmd_Process(t *testing.T) {
	executor := &RealProcessExecutor{}

	// Before start, Process is nil
	cmd := executor.Command("sleep", "0.1")
	proc := cmd.Process()
	assert.Nil(t, proc)

	// After start, Process is non-nil
	err := cmd.Start()
	require.NoError(t, err)

	proc = cmd.Process()
	require.NotNil(t, proc)
	assert.Greater(t, proc.Pid(), 0)

	// Wait for completion
	_ = cmd.Wait()
}

// TestRealProcess_Pid tests realProcess.Pid
func TestRealProcess_Pid(t *testing.T) {
	executor := &RealProcessExecutor{}

	proc, err := executor.FindProcess(os.Getpid())
	require.NoError(t, err)

	pid := proc.Pid()
	assert.Equal(t, os.Getpid(), pid)
}

// TestRealProcess_Signal tests realProcess.Signal
func TestRealProcess_Signal(t *testing.T) {
	executor := &RealProcessExecutor{}

	// Start a sleep process that will run long enough
	cmd := executor.Command("sleep", "10")
	err := cmd.Start()
	require.NoError(t, err)

	proc := cmd.Process()
	require.NotNil(t, proc)

	// Send SIGCONT (a harmless signal that won't terminate)
	err = proc.Signal(syscall.SIGCONT)
	require.NoError(t, err)

	// Now terminate the process
	err = proc.Signal(syscall.SIGTERM)
	require.NoError(t, err)

	// Wait for process to exit
	_, err = proc.Wait()
	// Process should have exited due to signal
	_ = err // Accept either success or error
}

// TestRealProcess_Wait tests realProcess.Wait
func TestRealProcess_Wait(t *testing.T) {
	executor := &RealProcessExecutor{}

	cmd := executor.Command("true")
	err := cmd.Start()
	require.NoError(t, err)

	proc := cmd.Process()
	require.NotNil(t, proc)

	state, err := proc.Wait()
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.Success())
}

// TestRealHTTPClient_Do tests RealHTTPClient.Do
func TestRealHTTPClient_Do(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	}))
	defer server.Close()

	client := NewRealHTTPClient(5 * time.Second)
	require.NotNil(t, client)

	req, err := http.NewRequest("GET", server.URL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Test with error (invalid URL)
	req, err = http.NewRequest("GET", "http://invalid.host.xyz", nil)
	require.NoError(t, err)

	_, err = client.Do(req)
	assert.Error(t, err)
}

// TestRealHTTPClient_Get tests RealHTTPClient.Get
func TestRealHTTPClient_Get(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test get response"))
	}))
	defer server.Close()

	client := NewRealHTTPClient(5 * time.Second)
	require.NotNil(t, client)

	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Test with error (invalid URL)
	_, err = client.Get("http://invalid.host.xyz")
	assert.Error(t, err)
}

// TestMockCmd_SetStdio_Coverage tests mockCmd SetStdin/SetStdout/SetStderr for coverage
func TestMockCmd_SetStdio_Coverage(t *testing.T) {
	mock := NewMockProcessExecutor()
	mock.Commands["test"] = struct {
		Output []byte
		Err    error
	}{Output: []byte("output"), Err: nil}

	cmd := mock.Command("test")

	// Call SetStdin, SetStdout, SetStderr to cover them
	var stdin, stdout, stderr bytes.Buffer
	cmd.SetStdin(&stdin)
	cmd.SetStdout(&stdout)
	cmd.SetStderr(&stderr)

	// Verify the command still works
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, []byte("output"), output)
}

// TestVMMManagerInternal_Start_Error tests VMMManagerInternal.Start error paths
func TestVMMManagerInternal_Start_Error(t *testing.T) {
	mockExec := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	config := &ManagerConfig{
		SocketDir: t.TempDir(),
	}

	vm := NewVMMManagerWithExecutors(config, mockExec, mockHTTP)
	require.NotNil(t, vm)

	ctx := context.Background()

	// Test with nil task
	err := vm.Start(ctx, nil, "config")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task cannot be nil")

	// Test with empty config
	task := &types.Task{ID: "test-vm"}
	err = vm.Start(ctx, task, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid config")

	// Test with binary not found
	task = &types.Task{ID: "test-vm-2"}
	err = vm.Start(ctx, task, "{\"test\": true}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "firecracker binary not found")
}

// TestMockHTTPClient_Do_Error tests MockHTTPClient.Do error cases
func TestMockHTTPClient_Do_Error(t *testing.T) {
	mock := NewMockHTTPClient()

	// Test unexpected request
	req, err := http.NewRequest("GET", "http://unexpected.url", nil)
	require.NoError(t, err)
	_, err = mock.Do(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected request")

	// Test configured error
	mock.SetError("GET", "http://error.url", assert.AnError)
	req, err = http.NewRequest("GET", "http://error.url", nil)
	require.NoError(t, err)
	_, err = mock.Do(req)
	require.Error(t, err)

	// Test response with error in response struct
	mock.Responses["POST:http://post.url"] = MockHTTPResponse{
		StatusCode: 500,
		Body:       []byte("internal error"),
		Err:        assert.AnError,
	}
	req, err = http.NewRequest("POST", "http://post.url", nil)
	require.NoError(t, err)
	_, err = mock.Do(req)
	require.Error(t, err)
}

// TestMockHTTPClient_Get_Error tests MockHTTPClient.Get error cases
func TestMockHTTPClient_Get_Error(t *testing.T) {
	mock := NewMockHTTPClient()

	// Test unexpected GET request
	_, err := mock.Get("http://unexpected.url")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected GET request")

	// Test configured error
	mock.SetError("GET", "http://error.url", assert.AnError)
	_, err = mock.Get("http://error.url")
	require.Error(t, err)

	// Test response with error in response struct
	mock.Responses["GET:http://response-error.url"] = MockHTTPResponse{
		StatusCode: 500,
		Body:       []byte("error body"),
		Err:        assert.AnError,
	}
	_, err = mock.Get("http://response-error.url")
	require.Error(t, err)
}

// TestMockHTTPClient_Success tests MockHTTPClient success cases
func TestMockHTTPClient_Success(t *testing.T) {
	mock := NewMockHTTPClient()

	// Test Do with successful response
	mock.SetResponse("GET", "http://success.url", 200, []byte("success body"))
	req, err := http.NewRequest("GET", "http://success.url", nil)
	require.NoError(t, err)
	resp, err := mock.Do(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()

	// Verify call was tracked
	assert.Len(t, mock.Calls, 1)
	assert.Equal(t, "GET", mock.Calls[0].Method)
	assert.Equal(t, "http://success.url", mock.Calls[0].URL)

	// Test Get with successful response
	mock.SetResponse("GET", "http://get-success.url", 200, []byte("get body"))
	resp, err = mock.Get("http://get-success.url")
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()

	assert.Len(t, mock.Calls, 2)
}

// TestVMMManagerInternal_Start_VMExists tests VM already exists case
func TestVMMManagerInternal_Start_VMExists(t *testing.T) {
	mockExec := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	config := &ManagerConfig{
		SocketDir: t.TempDir(),
	}

	vm := NewVMMManagerWithExecutors(config, mockExec, mockHTTP)
	require.NotNil(t, vm)

	// Manually add a VM to the map to simulate existing VM
	vm.mu.Lock()
	vm.vms["existing-vm"] = &VMInstance{ID: "existing-vm"}
	vm.mu.Unlock()

	ctx := context.Background()
	task := &types.Task{ID: "existing-vm"}

	err := vm.Start(ctx, task, "{\"test\": true}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VM already exists")
}

// TestVMMManagerInternal_Start_ConfigMarshal tests config marshaling case
func TestVMMManagerInternal_Start_ConfigMarshal(t *testing.T) {
	mockExec := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	config := &ManagerConfig{
		SocketDir: t.TempDir(),
	}

	vm := NewVMMManagerWithExecutors(config, mockExec, mockHTTP)
	require.NotNil(t, vm)

	ctx := context.Background()
	task := &types.Task{ID: "marshal-test"}

	// Pass non-string config (will be marshaled)
	configObj := map[string]string{"key": "value"}
	err := vm.Start(ctx, task, configObj)
	// Will fail because firecracker binary not found
	require.Error(t, err)
	assert.Contains(t, err.Error(), "firecracker binary not found")
}

// TestVMMManagerInternal_Start_BinaryFound tests binary found case
func TestVMMManagerInternal_Start_BinaryFound(t *testing.T) {
	mockExec := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	socketDir := t.TempDir()
	config := &ManagerConfig{
		SocketDir: socketDir,
	}

	// Setup mock to find firecracker binary
	mockExec.Binaries["firecracker"] = "/usr/bin/firecracker"
	mockExec.Commands["/usr/bin/firecracker"] = struct {
		Output []byte
		Err    error
	}{Output: nil, Err: nil}

	// Setup mock process
	mockProc := &mockProcess{pid: 12345}
	mockExec.Processes[12345] = mockProc

	vm := NewVMMManagerWithExecutors(config, mockExec, mockHTTP)
	require.NotNil(t, vm)

	ctx := context.Background()
	task := &types.Task{ID: "binary-test", Annotations: map[string]string{"init_system": "systemd"}}

	// Create socket file so waitForAPIServer can find it
	socketPath := filepath.Join(socketDir, "binary-test.sock")
	err := os.WriteFile(socketPath, []byte{}, 0644)
	require.NoError(t, err)

	// Setup mock HTTP responses for API server wait and instance start
	mockHTTP.SetResponse("GET", "http://unix"+socketPath+"/", 200, nil)
	mockHTTP.SetResponse("PUT", "http://unix"+socketPath+"/actions", 204, nil)

	err = vm.Start(ctx, task, "{\"test\": true}")
	require.NoError(t, err)

	// Verify VM was added
	vm.mu.Lock()
	vmInst, exists := vm.vms["binary-test"]
	vm.mu.Unlock()
	assert.True(t, exists)
	assert.NotNil(t, vmInst)
	assert.Equal(t, "binary-test", vmInst.ID)
	assert.Equal(t, "systemd", vmInst.InitSystem)
}

// TestVMMManagerInternal_Start_ProcessError tests process start error
func TestVMMManagerInternal_Start_ProcessError(t *testing.T) {
	mockExec := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	config := &ManagerConfig{
		SocketDir: t.TempDir(),
	}

	// Setup mock to find firecracker binary but fail to start process
	mockExec.Binaries["firecracker"] = "/usr/bin/firecracker"
	mockExec.Commands["/usr/bin/firecracker"] = struct {
		Output []byte
		Err    error
	}{Output: nil, Err: assert.AnError}

	vm := NewVMMManagerWithExecutors(config, mockExec, mockHTTP)
	require.NotNil(t, vm)

	ctx := context.Background()
	task := &types.Task{ID: "process-error-test"}

	err := vm.Start(ctx, task, "{\"test\": true}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start firecracker")
}

// TestVMMManagerInternal_Start_APIServerTimeout tests API server timeout case
func TestVMMManagerInternal_Start_APIServerTimeout(t *testing.T) {
	mockExec := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	config := &ManagerConfig{
		SocketDir: t.TempDir(),
	}

	// Setup mock to find firecracker binary and start process
	mockExec.Binaries["firecracker"] = "/usr/bin/firecracker"
	mockExec.Commands["/usr/bin/firecracker"] = struct {
		Output []byte
		Err    error
	}{Output: nil, Err: nil}

	mockProc := &mockProcess{pid: 12345}
	mockExec.Processes[12345] = mockProc

	vm := NewVMMManagerWithExecutors(config, mockExec, mockHTTP)
	require.NotNil(t, vm)

	ctx := context.Background()
	task := &types.Task{ID: "api-timeout-test"}

	// Don't setup HTTP responses - API server will timeout
	err := vm.Start(ctx, task, "{\"test\": true}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "firecracker API server not ready")
}

// TestVMMManagerInternal_Start_InstanceStartError tests instance start error
func TestVMMManagerInternal_Start_InstanceStartError(t *testing.T) {
	mockExec := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	socketDir := t.TempDir()
	config := &ManagerConfig{
		SocketDir: socketDir,
	}

	// Setup mock to find firecracker binary and start process
	mockExec.Binaries["firecracker"] = "/usr/bin/firecracker"
	mockExec.Commands["/usr/bin/firecracker"] = struct {
		Output []byte
		Err    error
	}{Output: nil, Err: nil}

	mockProc := &mockProcess{pid: 12345}
	mockExec.Processes[12345] = mockProc

	vm := NewVMMManagerWithExecutors(config, mockExec, mockHTTP)
	require.NotNil(t, vm)

	ctx := context.Background()
	task := &types.Task{ID: "instance-error-test"}

	// Create socket file
	socketPath := filepath.Join(socketDir, "instance-error-test.sock")
	err := os.WriteFile(socketPath, []byte{}, 0644)
	require.NoError(t, err)

	// Setup HTTP response for API server but fail instance start
	mockHTTP.SetResponse("GET", "http://unix"+socketPath+"/", 200, nil)
	mockHTTP.SetError("PUT", "http://unix"+socketPath+"/actions", assert.AnError)

	err = vm.Start(ctx, task, "{\"test\": true}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start instance")
}

// TestVMMManagerInternal_Start_InstanceStartBadStatus tests instance start bad status
func TestVMMManagerInternal_Start_InstanceStartBadStatus(t *testing.T) {
	mockExec := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	socketDir := t.TempDir()
	config := &ManagerConfig{
		SocketDir: socketDir,
	}

	// Setup mock to find firecracker binary and start process
	mockExec.Binaries["firecracker"] = "/usr/bin/firecracker"
	mockExec.Commands["/usr/bin/firecracker"] = struct {
		Output []byte
		Err    error
	}{Output: nil, Err: nil}

	mockProc := &mockProcess{pid: 12345}
	mockExec.Processes[12345] = mockProc

	vm := NewVMMManagerWithExecutors(config, mockExec, mockHTTP)
	require.NotNil(t, vm)

	ctx := context.Background()
	task := &types.Task{ID: "bad-status-test"}

	// Create socket file
	socketPath := filepath.Join(socketDir, "bad-status-test.sock")
	err := os.WriteFile(socketPath, []byte{}, 0644)
	require.NoError(t, err)

	// Setup HTTP response for API server but bad status for instance start
	mockHTTP.SetResponse("GET", "http://unix"+socketPath+"/", 200, nil)
	mockHTTP.SetResponse("PUT", "http://unix"+socketPath+"/actions", 500, []byte("error"))

	err = vm.Start(ctx, task, "{\"test\": true}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status code")
}

// TestVMMManagerInternal_SendCtrlAltDel_Coverage tests sendCtrlAltDelWithClient
func TestVMMManagerInternal_SendCtrlAltDel_Coverage(t *testing.T) {
	mockExec := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	socketDir := t.TempDir()
	config := &ManagerConfig{
		SocketDir: socketDir,
	}

	vm := NewVMMManagerWithExecutors(config, mockExec, mockHTTP)
	require.NotNil(t, vm)

	ctx := context.Background()
	socketPath := filepath.Join(socketDir, "test.sock")

	// Test success case
	mockHTTP.SetResponse("PUT", "http://unix"+socketPath+"/actions", 204, nil)
	err := vm.sendCtrlAltDelWithClient(ctx, socketPath)
	require.NoError(t, err)

	// Test error case - need to clear the previous response and set an error
	mockHTTP = NewMockHTTPClient()
	vm.httpClient = mockHTTP
	mockHTTP.SetError("PUT", "http://unix"+socketPath+"/actions", assert.AnError)
	err = vm.sendCtrlAltDelWithClient(ctx, socketPath)
	require.Error(t, err)
}

// TestVMMManagerInternal_findProcess tests findProcess method
func TestVMMManagerInternal_findProcess(t *testing.T) {
	mockExec := NewMockProcessExecutor()
	mockHTTP := NewMockHTTPClient()

	config := &ManagerConfig{
		SocketDir: t.TempDir(),
	}

	mockProc := &mockProcess{pid: 12345}
	mockExec.Processes[12345] = mockProc

	vm := NewVMMManagerWithExecutors(config, mockExec, mockHTTP)
	require.NotNil(t, vm)

	proc, err := vm.findProcess(12345)
	require.NoError(t, err)
	assert.Equal(t, 12345, proc.Pid())

	// Test error case
	proc, err = vm.findProcess(99999)
	require.Error(t, err)
	assert.Nil(t, proc)
}
