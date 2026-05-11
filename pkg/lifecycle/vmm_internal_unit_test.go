package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMMManagerInternal_Start_Success tests successful VM start
func TestVMMManagerInternal_Start_Success(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test-vm.sock")

	procExec := NewMockProcessExecutor()
	procExec.Binaries["firecracker"] = "/usr/bin/firecracker"

	httpClient := NewMockHTTPClient()
	httpClient.SetResponse("GET", "http://unix"+socketPath+"/", http.StatusOK, []byte("{}"))
	httpClient.SetResponse("PUT", "http://unix"+socketPath+"/actions", http.StatusNoContent, []byte{})

	// Pre-create socket for waitForAPIServer
	file, _ := os.Create(socketPath)
	file.Close()

	config := &ManagerConfig{SocketDir: tmpDir}
	vm := NewVMMManagerWithExecutors(config, procExec, httpClient)

	task := &types.Task{ID: "test-vm"}
	vmConfig := `{"kernel-image-path": "/vmlinux", "drives": []}`

	err := vm.Start(context.Background(), task, vmConfig)
	require.NoError(t, err)

	assert.Contains(t, procExec.Calls, "LookPath:firecracker")
	assert.GreaterOrEqual(t, len(httpClient.Calls), 1)
}

// TestVMMManagerInternal_Start_NilTask tests nil task error
func TestVMMManagerInternal_Start_NilTask(t *testing.T) {
	procExec := NewMockProcessExecutor()
	httpClient := NewMockHTTPClient()

	vm := NewVMMManagerWithExecutors(nil, procExec, httpClient)

	err := vm.Start(context.Background(), nil, "{}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task cannot be nil")
}

// TestVMMManagerInternal_Start_BinaryNotFound tests missing firecracker binary
func TestVMMManagerInternal_Start_BinaryNotFound(t *testing.T) {
	procExec := NewMockProcessExecutor()
	httpClient := NewMockHTTPClient()

	config := &ManagerConfig{SocketDir: t.TempDir()}
	vm := NewVMMManagerWithExecutors(config, procExec, httpClient)

	task := &types.Task{ID: "test-vm"}
	err := vm.Start(context.Background(), task, "{}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "firecracker binary not found")
}

// TestVMMManagerInternal_Start_DuplicateVM tests duplicate VM error
func TestVMMManagerInternal_Start_DuplicateVM(t *testing.T) {
	tmpDir := t.TempDir()

	procExec := NewMockProcessExecutor()
	procExec.Binaries["firecracker"] = "/usr/bin/firecracker"

	httpClient := NewMockHTTPClient()

	config := &ManagerConfig{SocketDir: tmpDir}
	vm := NewVMMManagerWithExecutors(config, procExec, httpClient)

	// Pre-existing VM
	vm.vms["existing-vm"] = &VMInstance{ID: "existing-vm", state: VMStateRunning}

	task := &types.Task{ID: "existing-vm"}
	err := vm.Start(context.Background(), task, "{}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VM already exists")
}

// TestVMMManagerInternal_Start_EmptyConfig tests empty config error
func TestVMMManagerInternal_Start_EmptyConfig(t *testing.T) {
	procExec := NewMockProcessExecutor()
	procExec.Binaries["firecracker"] = "/usr/bin/firecracker"

	httpClient := NewMockHTTPClient()

	config := &ManagerConfig{SocketDir: t.TempDir()}
	vm := NewVMMManagerWithExecutors(config, procExec, httpClient)

	task := &types.Task{ID: "test-vm"}
	err := vm.Start(context.Background(), task, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty configuration")
}

// TestVMMManagerInternal_Start_APITimeout tests API server timeout
func TestVMMManagerInternal_Start_APITimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: timeout test takes time")
	}

	tmpDir := t.TempDir()

	procExec := NewMockProcessExecutor()
	procExec.Binaries["firecracker"] = "/usr/bin/firecracker"

	httpClient := NewMockHTTPClient()
	// No responses set — will return errors

	config := &ManagerConfig{SocketDir: tmpDir}
	vm := NewVMMManagerWithExecutors(config, procExec, httpClient)

	task := &types.Task{ID: "test-vm"}
	err := vm.Start(context.Background(), task, "{}")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API server not ready")
}

// TestVMMManagerInternal_waitForAPIServerWithClient tests API server wait
func TestVMMManagerInternal_waitForAPIServerWithClient(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		// Create socket file
		file, _ := os.Create(socketPath)
		file.Close()

		httpClient := NewMockHTTPClient()
		httpClient.SetResponse("GET", "http://unix"+socketPath+"/", http.StatusOK, []byte("{}"))

		vm := NewVMMManagerWithExecutors(nil, nil, httpClient)

		err := vm.waitForAPIServerWithClient(socketPath, 1*time.Second)
		require.NoError(t, err)
	})

	t.Run("timeout", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping: timeout test")
		}

		vm := NewVMMManagerWithExecutors(nil, nil, NewMockHTTPClient())
		err := vm.waitForAPIServerWithClient("/nonexistent/path.sock", 100*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not ready")
	})
}

// TestVMMManagerInternal_startInstanceWithClient tests instance start
func TestVMMManagerInternal_startInstanceWithClient(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		socketPath := "/var/run/test.sock"

		httpClient := NewMockHTTPClient()
		httpClient.SetResponse("PUT", "http://unix"+socketPath+"/actions", http.StatusNoContent, []byte{})

		vm := NewVMMManagerWithExecutors(nil, nil, httpClient)

		err := vm.startInstanceWithClient(context.Background(), socketPath)
		require.NoError(t, err)
		assert.Equal(t, 1, len(httpClient.Calls))
	})

	t.Run("error", func(t *testing.T) {
		socketPath := "/var/run/test.sock"

		httpClient := NewMockHTTPClient()
		httpClient.SetError("PUT", "http://unix"+socketPath+"/actions", errors.New("connection refused"))

		vm := NewVMMManagerWithExecutors(nil, nil, httpClient)

		err := vm.startInstanceWithClient(context.Background(), socketPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to start instance")
	})

	t.Run("unexpected_status", func(t *testing.T) {
		socketPath := "/var/run/test.sock"

		httpClient := NewMockHTTPClient()
		httpClient.SetResponse("PUT", "http://unix"+socketPath+"/actions", http.StatusInternalServerError, []byte("error"))

		vm := NewVMMManagerWithExecutors(nil, nil, httpClient)

		err := vm.startInstanceWithClient(context.Background(), socketPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status code")
	})
}

// TestVMMManagerInternal_sendCtrlAltDelWithClient tests CtrlAltDel
func TestVMMManagerInternal_sendCtrlAltDelWithClient(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		socketPath := "/var/run/test.sock"

		httpClient := NewMockHTTPClient()
		httpClient.SetResponse("PUT", "http://unix"+socketPath+"/actions", http.StatusNoContent, []byte{})

		vm := NewVMMManagerWithExecutors(nil, nil, httpClient)

		err := vm.sendCtrlAltDelWithClient(context.Background(), socketPath)
		require.NoError(t, err)
	})

	t.Run("error", func(t *testing.T) {
		socketPath := "/var/run/test.sock"

		httpClient := NewMockHTTPClient()
		httpClient.SetError("PUT", "http://unix"+socketPath+"/actions", errors.New("connection refused"))

		vm := NewVMMManagerWithExecutors(nil, nil, httpClient)

		err := vm.sendCtrlAltDelWithClient(context.Background(), socketPath)
		require.Error(t, err)
	})
}

// TestVMMManagerInternal_findProcess tests process finding

// TestMockHTTPClient_AllMethods tests all mock HTTP client methods
func TestMockHTTPClient_AllMethods(t *testing.T) {
	t.Run("Do_with_response", func(t *testing.T) {
		client := NewMockHTTPClient()
		client.SetResponse("PUT", "http://test/api", http.StatusNoContent, []byte{})

		req, _ := http.NewRequest("PUT", "http://test/api", nil)
		resp, err := client.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("Do_with_error", func(t *testing.T) {
		client := NewMockHTTPClient()
		client.SetError("PUT", "http://test/api", errors.New("timeout"))

		req, _ := http.NewRequest("PUT", "http://test/api", nil)
		resp, err := client.Do(req)
		require.Error(t, err)
		assert.Nil(t, resp)
	})

	t.Run("Do_unexpected_request", func(t *testing.T) {
		client := NewMockHTTPClient()

		req, _ := http.NewRequest("GET", "http://unexpected/url", nil)
		resp, err := client.Do(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected request")
		assert.Nil(t, resp)
	})

	t.Run("Get_success", func(t *testing.T) {
		client := NewMockHTTPClient()
		client.SetResponse("GET", "http://test/api", http.StatusOK, []byte("data"))

		resp, err := client.Get("http://test/api")
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("Get_error", func(t *testing.T) {
		client := NewMockHTTPClient()
		client.SetError("GET", "http://test/api", errors.New("timeout"))

		resp, err := client.Get("http://test/api")
		require.Error(t, err)
		assert.Nil(t, resp)
	})

	t.Run("Get_unexpected", func(t *testing.T) {
		client := NewMockHTTPClient()

		resp, err := client.Get("http://unexpected/url")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected GET request")
		assert.Nil(t, resp)
	})
}

// TestMockProcess_AllMethods_Extended tests mockProcess methods
func TestMockProcess_AllMethods_Extended(t *testing.T) {
	proc := &mockProcess{
		pid:     54321,
		signals: make([]syscall.Signal, 0),
	}

	// Test Pid
	assert.Equal(t, 54321, proc.Pid())

	// Test Kill
	err := proc.Kill()
	require.NoError(t, err)

	// Test Signal (non-zero)
	err = proc.Signal(syscall.SIGTERM)
	require.NoError(t, err)
	assert.Contains(t, proc.signals, syscall.SIGTERM)

	// Test Wait
	state, err := proc.Wait()
	require.NoError(t, err)
	assert.Nil(t, state)

	// Test Release
	err = proc.Release()
	require.NoError(t, err)
}

// TestMockCmd_Process_ReturnsDefault tests mockCmd Process method
func TestMockCmd_Process_ReturnsDefault(t *testing.T) {
	t.Run("with_process", func(t *testing.T) {
		mockProc := &mockProcess{pid: 11111}
		cmd := &mockCmd{process: mockProc}

		proc := cmd.Process()
		require.NotNil(t, proc)
		assert.Equal(t, 11111, proc.Pid())
	})

	t.Run("without_process", func(t *testing.T) {
		cmd := &mockCmd{executor: NewMockProcessExecutor()}

		proc := cmd.Process()
		require.NotNil(t, proc) // Returns default mockProcess with pid 12345
	})
}
