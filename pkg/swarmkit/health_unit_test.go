package swarmkit

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMMManager_CheckVMAPIHealth_Extended tests CheckVMAPIHealth with various scenarios
func TestVMMManager_CheckVMAPIHealth_Extended(t *testing.T) {
	t.Run("socket file does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		result := vmm.CheckVMAPIHealth(context.Background(), "nonexistent-task")
		assert.False(t, result)
	})

	t.Run("socket file exists but connection fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "task-1.sock")

		// Create a regular file (not a socket) - connection will fail
		err := os.WriteFile(socketPath, []byte("dummy"), 0644)
		require.NoError(t, err)

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		result := vmm.CheckVMAPIHealth(context.Background(), "task-1")
		assert.False(t, result)
	})

	t.Run("mock firecracker API server returns 200", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "task-healthy.sock")

		// Start a mock Unix socket server
		server := startMockFirecrackerServer(t, socketPath, http.StatusOK)
		defer server.Close()

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		// Give server time to start
		time.Sleep(100 * time.Millisecond)

		result := vmm.CheckVMAPIHealth(context.Background(), "task-healthy")
		assert.True(t, result)
	})

	t.Run("mock firecracker API server returns 500", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "task-unhealthy.sock")

		// Start a mock Unix socket server that returns 500
		server := startMockFirecrackerServer(t, socketPath, http.StatusInternalServerError)
		defer server.Close()

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		// Give server time to start
		time.Sleep(100 * time.Millisecond)

		result := vmm.CheckVMAPIHealth(context.Background(), "task-unhealthy")
		assert.False(t, result)
	})

	t.Run("mock firecracker API server times out", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "task-timeout.sock")

		// Start a mock Unix socket server that delays response
		server := startMockFirecrackerServerDelayed(t, socketPath, 5*time.Second)
		defer server.Close()

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		// Give server time to start
		time.Sleep(100 * time.Millisecond)

		result := vmm.CheckVMAPIHealth(context.Background(), "task-timeout")
		assert.False(t, result) // Should timeout and return false
	})

	t.Run("request creation error", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		// Create invalid socket path that will cause request creation to fail
		// (this is handled by the HTTP client, not the request creation)
		result := vmm.CheckVMAPIHealth(context.Background(), "task-invalid")
		assert.False(t, result)
	})
}

// TestVMMManager_putAPI_Extended tests putAPI method
func TestVMMManager_putAPI_Extended(t *testing.T) {
	t.Run("putAPI with mock server success", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		server := startMockFirecrackerServer(t, socketPath, http.StatusNoContent)
		defer server.Close()

		time.Sleep(100 * time.Millisecond)

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		config := MachineConfig{
			VcpuCount:  2,
			MemSizeMib: 512,
		}

		err := vmm.putAPI(context.Background(), socketPath, "/machine-config", config)
		assert.NoError(t, err)
	})

	t.Run("putAPI with mock server error response", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		server := startMockFirecrackerServerError(t, socketPath)
		defer server.Close()

		time.Sleep(100 * time.Millisecond)

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		config := MachineConfig{
			VcpuCount:  2,
			MemSizeMib: 512,
		}

		err := vmm.putAPI(context.Background(), socketPath, "/machine-config", config)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "API returned status")
	})

	t.Run("putAPI marshal error", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		// Pass a type that can't be marshaled
		err := vmm.putAPI(context.Background(), socketPath, "/machine-config", func() {})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to marshal JSON")
	})

	t.Run("putAPI with connection refused", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "nonexistent.sock")

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		config := MachineConfig{VcpuCount: 2}

		err := vmm.putAPI(context.Background(), socketPath, "/machine-config", config)
		assert.Error(t, err)
	})
}

// TestCreateFirecrackerHTTPClient tests HTTP client creation
func TestCreateFirecrackerHTTPClient_Extended(t *testing.T) {
	t.Run("client has correct timeout", func(t *testing.T) {
		socketPath := "/tmp/test.sock"
		client := createFirecrackerHTTPClient(socketPath)

		assert.NotNil(t, client)
		assert.Equal(t, 10*time.Second, client.Timeout)
	})

	t.Run("client transport is dialer", func(t *testing.T) {
		socketPath := "/tmp/test.sock"
		client := createFirecrackerHTTPClient(socketPath)

		transport := client.Transport
		assert.NotNil(t, transport)

		// Verify it's an HTTP transport with custom dialer
		httpTransport, ok := transport.(*http.Transport)
		assert.True(t, ok)
		assert.NotNil(t, httpTransport.DialContext)
	})
}

// TestVMMManager_configureVM_EdgeCases tests configureVM edge cases
func TestVMMManager_configureVM_EdgeCases(t *testing.T) {
	t.Run("configure with network interfaces", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		server := startMockFirecrackerServerConfig(t, socketPath)
		defer server.Close()

		time.Sleep(100 * time.Millisecond)

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		config := map[string]interface{}{
			"machine-config": map[string]interface{}{
				"vcpu_count":   2,
				"mem_size_mib": 512,
			},
			"boot-source": map[string]interface{}{
				"kernel_image_path": "/vmlinux",
				"boot_args":         "console=ttyS0",
			},
			"drives": []interface{}{
				map[string]interface{}{
					"drive_id":       "rootfs",
					"path_on_host":   "/rootfs.ext4",
					"is_root_device": true,
					"is_read_only":   false,
				},
			},
			"network-interfaces": []interface{}{
				map[string]interface{}{
					"iface_id":     "eth0",
					"guest_mac":    "02:00:00:00:00:01",
					"host_dev_name": "tap0",
				},
			},
		}

		task := &types.Task{ID: "task-1"}
		ctx := context.Background()

		err := vmm.configureVM(ctx, task, socketPath, config)
		assert.NoError(t, err)
	})

	t.Run("configure with empty network interfaces", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		server := startMockFirecrackerServerConfig(t, socketPath)
		defer server.Close()

		time.Sleep(100 * time.Millisecond)

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		config := map[string]interface{}{
			"machine-config": map[string]interface{}{
				"vcpu_count":   2,
				"mem_size_mib": 512,
			},
			"boot-source": map[string]interface{}{
				"kernel_image_path": "/vmlinux",
				"boot_args":         "console=ttyS0",
			},
			"drives": []interface{}{
				map[string]interface{}{
					"drive_id":       "rootfs",
					"path_on_host":   "/rootfs.ext4",
					"is_root_device": true,
					"is_read_only":   false,
				},
			},
			"network-interfaces": []interface{}{}, // Empty
		}

		task := &types.Task{ID: "task-1"}
		ctx := context.Background()

		err := vmm.configureVM(ctx, task, socketPath, config)
		assert.NoError(t, err)
	})

	t.Run("configure with map[string]interface{} drives", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		server := startMockFirecrackerServerConfig(t, socketPath)
		defer server.Close()

		time.Sleep(100 * time.Millisecond)

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		config := map[string]interface{}{
			"machine-config": map[string]interface{}{
				"vcpu_count":   2,
				"mem_size_mib": 512,
			},
			"boot-source": map[string]interface{}{
				"kernel_image_path": "/vmlinux",
				"boot_args":         "console=ttyS0",
			},
			"drives": []map[string]interface{}{
				{
					"drive_id":       "rootfs",
					"path_on_host":   "/rootfs.ext4",
					"is_root_device": true,
					"is_read_only":   false,
				},
			},
		}

		task := &types.Task{ID: "task-1"}
		ctx := context.Background()

		err := vmm.configureVM(ctx, task, socketPath, config)
		assert.NoError(t, err)
	})

	t.Run("configure with invalid drive type", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		server := startMockFirecrackerServerConfig(t, socketPath)
		defer server.Close()

		time.Sleep(100 * time.Millisecond)

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		config := map[string]interface{}{
			"machine-config": map[string]interface{}{
				"vcpu_count":   2,
				"mem_size_mib": 512,
			},
			"boot-source": map[string]interface{}{
				"kernel_image_path": "/vmlinux",
				"boot_args":         "console=ttyS0",
			},
			"drives": []interface{}{
				"invalid-drive", // Not a map
			},
		}

		task := &types.Task{ID: "task-1"}
		ctx := context.Background()

		err := vmm.configureVM(ctx, task, socketPath, config)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid drive type")
	})

	t.Run("configure with missing drive_id", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		server := startMockFirecrackerServerConfig(t, socketPath)
		defer server.Close()

		time.Sleep(100 * time.Millisecond)

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		config := map[string]interface{}{
			"machine-config": map[string]interface{}{
				"vcpu_count":   2,
				"mem_size_mib": 512,
			},
			"boot-source": map[string]interface{}{
				"kernel_image_path": "/vmlinux",
				"boot_args":         "console=ttyS0",
			},
			"drives": []interface{}{
				map[string]interface{}{
					"path_on_host":   "/rootfs.ext4",
					"is_root_device": true,
				},
			},
		}

		task := &types.Task{ID: "task-1"}
		ctx := context.Background()

		err := vmm.configureVM(ctx, task, socketPath, config)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing drive_id")
	})
}

// Mock server helpers

func startMockFirecrackerServer(t *testing.T, socketPath string, statusCode int) *http.Server {
 listener, err := net.Listen("unix", socketPath)
 require.NoError(t, err)

 server := &http.Server{
	 Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		 w.WriteHeader(statusCode)
		 if statusCode == http.StatusOK {
			 // Return mock machine config for GET requests
			 if r.Method == http.MethodGet {
				 config := MachineConfig{VcpuCount: 2, MemSizeMib: 512}
				 data, _ := json.Marshal(config)
				 w.Write(data)
			 }
		 }
	 }),
 }

 go server.Serve(listener)

 return server
}

func startMockFirecrackerServerDelayed(t *testing.T, socketPath string, delay time.Duration) *http.Server {
 listener, err := net.Listen("unix", socketPath)
 require.NoError(t, err)

 server := &http.Server{
	 Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		 time.Sleep(delay) // Delay beyond the 2s timeout
		 w.WriteHeader(http.StatusOK)
	 }),
 }

 go server.Serve(listener)

 return server
}

func startMockFirecrackerServerError(t *testing.T, socketPath string) *http.Server {
 listener, err := net.Listen("unix", socketPath)
 require.NoError(t, err)

 server := &http.Server{
	 Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		 w.WriteHeader(http.StatusBadRequest)
		 w.Write([]byte("invalid configuration"))
	 }),
 }

 go server.Serve(listener)

 return server
}

func startMockFirecrackerServerConfig(t *testing.T, socketPath string) *http.Server {
 listener, err := net.Listen("unix", socketPath)
 require.NoError(t, err)

 server := &http.Server{
	 Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		 // Handle PUT requests for machine-config, boot-source, drives, network-interfaces
		 if r.Method == http.MethodPut {
			 w.WriteHeader(http.StatusNoContent)
			 return
		 }
		 // Handle actions endpoint for InstanceStart
		 if r.URL.Path == "/actions" {
			 w.WriteHeader(http.StatusNoContent)
			 return
		 }
		 w.WriteHeader(http.StatusOK)
	 }),
 }

 go server.Serve(listener)

 return server
}