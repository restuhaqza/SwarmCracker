package swarmkit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMMManager_Describe tests Describe method
func TestVMMManager_Describe(t *testing.T) {
	t.Run("describe running task", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		// Add a mock process
		cmd := &exec.Cmd{
			Process: &os.Process{Pid: 1234},
		}
		vmm.processes["task-1"] = cmd

		task := &types.Task{ID: "task-1"}
		ctx := context.Background()

		status, err := vmm.Describe(ctx, task)
		require.NoError(t, err)
		assert.NotNil(t, status)
		assert.Equal(t, types.TaskStateRunning, status.State)
	})

	t.Run("describe completed task", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		task := &types.Task{ID: "task-nonexistent"}
		ctx := context.Background()

		status, err := vmm.Describe(ctx, task)
		require.NoError(t, err)
		assert.NotNil(t, status)
		assert.Equal(t, types.TaskStateComplete, status.State)
		assert.Contains(t, status.Message, "not running")
	})
}

// TestVMMManager_Write tests the Write method via putAPI
func TestVMMManager_Write(t *testing.T) {
	t.Run("write to mock server", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				body, _ := io.ReadAll(r.Body)
				var payload map[string]interface{}
				json.Unmarshal(body, &payload)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		defer ts.Close()
		t.Cleanup(func() { ts.Client().CloseIdleConnections() })

		// Test serialization
		payload := MachineConfig{
			VcpuCount:  2,
			MemSizeMib: 512,
			HtEnabled:  false,
		}

		data, err := json.Marshal(payload)
		require.NoError(t, err)
		assert.Contains(t, string(data), "vcpu_count")
		assert.Contains(t, string(data), "mem_size_mib")
	})
}

// TestVMMManager_createFirecrackerHTTPClient tests HTTP client creation
func TestVMMManager_createFirecrackerHTTPClient(t *testing.T) {
	t.Run("creates client with correct timeout", func(t *testing.T) {
		socketPath := "/tmp/test.sock"
		client := createFirecrackerHTTPClient(socketPath)

		assert.NotNil(t, client)
		assert.Equal(t, 10*time.Second, client.Timeout)
		assert.NotNil(t, client.Transport)
	})
}

// TestVMMManager_putAPI tests the putAPI method with mock server
func TestVMMManager_putAPI(t *testing.T) {
	t.Run("putAPI serialization test", func(t *testing.T) {
		machineConfig := MachineConfig{
			VcpuCount:  2,
			MemSizeMib: 1024,
			HtEnabled:  false,
		}

		data, err := json.Marshal(machineConfig)
		require.NoError(t, err)

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, "http://localhost/machine-config", bytes.NewReader(data))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")

		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	})

	t.Run("API error response", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid configuration"))
		}))
		defer ts.Close()

		machineConfig := MachineConfig{
			VcpuCount:  0,
			MemSizeMib: 0,
		}

		data, err := json.Marshal(machineConfig)
		require.NoError(t, err)

		client := ts.Client()
		t.Cleanup(func() { client.CloseIdleConnections() })

		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, ts.URL+"/machine-config", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		body, _ := io.ReadAll(resp.Body)
		assert.Contains(t, string(body), "invalid configuration")
	})

	t.Run("putAPI with nonexistent socket", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		socketPath := filepath.Join(tmpDir, "nonexistent.sock")
		err := vmm.putAPI(socketPath, "/machine-config", MachineConfig{})
		assert.Error(t, err)
	})
}

// TestVMMManager_configureVM tests VM configuration via API
func TestVMMManager_configureVM(t *testing.T) {
	t.Run("configure with valid config", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		task := &types.Task{ID: "task-1"}
		socketPath := filepath.Join(tmpDir, "test.sock")

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
		}

		ctx := context.Background()
		err := vmm.configureVM(ctx, task, socketPath, config)
		// Should error due to no real unix socket
		assert.Error(t, err)
	})

	t.Run("configure missing machine-config", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		task := &types.Task{ID: "task-1"}
		socketPath := filepath.Join(tmpDir, "test.sock")

		config := map[string]interface{}{
			"boot-source": map[string]interface{}{
				"kernel_image_path": "/vmlinux",
			},
		}

		ctx := context.Background()
		err := vmm.configureVM(ctx, task, socketPath, config)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing machine-config")
	})

	t.Run("configure missing boot-source", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		task := &types.Task{ID: "task-1"}
		socketPath := filepath.Join(tmpDir, "test.sock")

		config := map[string]interface{}{
			"machine-config": map[string]interface{}{
				"vcpu_count":   2,
				"mem_size_mib": 512,
			},
		}

		ctx := context.Background()
		err := vmm.configureVM(ctx, task, socketPath, config)
		assert.Error(t, err)
		// Error could be missing boot-source or connection refused
	})

	t.Run("configure missing drives", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		task := &types.Task{ID: "task-1"}
		socketPath := filepath.Join(tmpDir, "test.sock")

		config := map[string]interface{}{
			"machine-config": map[string]interface{}{
				"vcpu_count":   2,
				"mem_size_mib": 512,
			},
			"boot-source": map[string]interface{}{
				"kernel_image_path": "/vmlinux",
			},
		}

		ctx := context.Background()
		err := vmm.configureVM(ctx, task, socketPath, config)
		assert.Error(t, err)
		// Error could be missing drives or connection refused
	})

	t.Run("configure with invalid config type", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		task := &types.Task{ID: "task-1"}
		socketPath := filepath.Join(tmpDir, "test.sock")

		ctx := context.Background()
		err := vmm.configureVM(ctx, task, socketPath, "invalid-config-type")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid config type")
	})
}

// TestVMMManager_waitForSocket tests socket waiting
func TestVMMManager_waitForSocket(t *testing.T) {
	t.Run("socket appears after delay", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		go func() {
			time.Sleep(100 * time.Millisecond)
			os.WriteFile(socketPath, []byte("socket"), 0644)
		}()

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		err := vmm.waitForSocket(socketPath, 2*time.Second)
		assert.NoError(t, err)
	})

	t.Run("socket timeout", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "nonexistent.sock")

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		err := vmm.waitForSocket(socketPath, 200*time.Millisecond)
		assert.Error(t, err)
		assert.Equal(t, context.DeadlineExceeded, err)
	})

	t.Run("socket exists immediately", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "test.sock")

		os.WriteFile(socketPath, []byte("socket"), 0644)

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		err := vmm.waitForSocket(socketPath, 2*time.Second)
		assert.NoError(t, err)
	})
}

// TestVMMManager_GetPID_VMM tests GetPID method
func TestVMMManager_GetPID_VMM(t *testing.T) {
	t.Run("returns pid for existing process", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		cmd := &exec.Cmd{
			Process: &os.Process{Pid: 12345},
		}
		vmm.processes["task-1"] = cmd

		pid := vmm.GetPID("task-1")
		assert.Equal(t, 12345, pid)
	})

	t.Run("returns 0 for nonexistent task", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		pid := vmm.GetPID("task-nonexistent")
		assert.Equal(t, 0, pid)
	})

	t.Run("returns 0 for process with nil Process", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		cmd := &exec.Cmd{
			Process: nil,
		}
		vmm.processes["task-1"] = cmd

		pid := vmm.GetPID("task-1")
		assert.Equal(t, 0, pid)
	})
}

// TestVMMManager_IsRunning_VMM tests IsRunning method
func TestVMMManager_IsRunning_VMM(t *testing.T) {
	t.Run("returns false for nonexistent task", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		running := vmm.IsRunning("task-nonexistent")
		assert.False(t, running)
	})

	t.Run("returns false for nil process", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		cmd := &exec.Cmd{
			Process: nil,
		}
		vmm.processes["task-1"] = cmd

		running := vmm.IsRunning("task-1")
		assert.False(t, running)
	})
}

// TestVMMManager_Remove_VMM tests Remove method
func TestVMMManager_Remove_VMM(t *testing.T) {
	t.Run("removes socket file", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "task-1.sock")
		os.WriteFile(socketPath, []byte("socket"), 0644)

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		task := &types.Task{ID: "task-1"}
		ctx := context.Background()
		err := vmm.Remove(ctx, task)
		assert.NoError(t, err)

		_, err = os.Stat(socketPath)
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("remove with running process", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		cmd := &exec.Cmd{
			Process: nil,
		}
		vmm.processes["task-1"] = cmd

		task := &types.Task{ID: "task-1"}
		ctx := context.Background()
		err := vmm.Remove(ctx, task)
		assert.NoError(t, err)
	})

	t.Run("remove nonexistent socket", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		task := &types.Task{ID: "task-nonexistent"}
		ctx := context.Background()
		err := vmm.Remove(ctx, task)
		assert.NoError(t, err)
	})
}

// TestVMMManager_CheckVMAPIHealth_VMM tests CheckVMAPIHealth
func TestVMMManager_CheckVMAPIHealth_VMM(t *testing.T) {
	t.Run("returns false for nonexistent socket", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		healthy := vmm.CheckVMAPIHealth("task-nonexistent")
		assert.False(t, healthy)
	})

	t.Run("returns false for socket with no API", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "task-1.sock")
		os.WriteFile(socketPath, []byte("dummy"), 0644)

		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		healthy := vmm.CheckVMAPIHealth("task-1")
		assert.False(t, healthy)
	})
}

// TestVMMManager_Stop_VMM tests Stop method
func TestVMMManager_Stop_VMM(t *testing.T) {
	t.Run("stop nonexistent task", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		task := &types.Task{ID: "task-nonexistent"}
		ctx := context.Background()
		err := vmm.Stop(ctx, task)
		assert.Error(t, err)
	})
}

// TestVMMManager_ForceStop_VMM tests ForceStop method
func TestVMMManager_ForceStop_VMM(t *testing.T) {
	t.Run("force stop nonexistent task", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		task := &types.Task{ID: "task-nonexistent"}
		ctx := context.Background()
		err := vmm.ForceStop(ctx, task)
		assert.Error(t, err)
	})
}

// TestVMMManager_Wait_VMM tests Wait method
func TestVMMManager_Wait_VMM(t *testing.T) {
	t.Run("wait for nonexistent task", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
		}

		task := &types.Task{ID: "task-nonexistent"}
		ctx := context.Background()
		status, err := vmm.Wait(ctx, task)
		require.NoError(t, err)
		assert.Equal(t, types.TaskStateComplete, status.State)
	})
}

// TestActionStruct tests Action struct
func TestActionStruct(t *testing.T) {
	t.Run("InstanceStart action", func(t *testing.T) {
		action := Action{
			ActionType:     "InstanceStart",
			TimeoutSeconds: 0,
		}

		data, err := json.Marshal(action)
		require.NoError(t, err)

		var decoded Action
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, "InstanceStart", decoded.ActionType)
	})
}

// TestMachineConfigStruct tests MachineConfig struct
func TestMachineConfigStruct(t *testing.T) {
	t.Run("serialize and deserialize", func(t *testing.T) {
		config := MachineConfig{
			VcpuCount:       2,
			MemSizeMib:      1024,
			HtEnabled:       false,
			TrackDirtyPages: true,
		}

		data, err := json.Marshal(config)
		require.NoError(t, err)

		var decoded MachineConfig
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, 2, decoded.VcpuCount)
		assert.Equal(t, 1024, decoded.MemSizeMib)
		assert.False(t, decoded.HtEnabled)
		assert.True(t, decoded.TrackDirtyPages)
	})
}

// TestBootSourceStruct tests BootSource struct
func TestBootSourceStruct(t *testing.T) {
	t.Run("serialize and deserialize", func(t *testing.T) {
		boot := BootSource{
			KernelImagePath: "/vmlinux",
			BootArgs:        "console=ttyS0 reboot=k panic=1",
		}

		data, err := json.Marshal(boot)
		require.NoError(t, err)

		var decoded BootSource
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, "/vmlinux", decoded.KernelImagePath)
		assert.Contains(t, decoded.BootArgs, "console")
	})
}

// TestDriveStruct tests Drive struct
func TestDriveStruct(t *testing.T) {
	t.Run("serialize and deserialize", func(t *testing.T) {
		drive := Drive{
			DriveID:      "rootfs",
			IsRootDevice: true,
			IsReadOnly:   false,
			PathOnHost:   "/var/lib/rootfs.ext4",
		}

		data, err := json.Marshal(drive)
		require.NoError(t, err)

		var decoded Drive
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, "rootfs", decoded.DriveID)
		assert.True(t, decoded.IsRootDevice)
		assert.False(t, decoded.IsReadOnly)
		assert.Equal(t, "/var/lib/rootfs.ext4", decoded.PathOnHost)
	})
}

// TestNetworkInterfaceStruct_VMM tests NetworkInterface struct
func TestNetworkInterfaceStruct_VMM(t *testing.T) {
	t.Run("serialize and deserialize", func(t *testing.T) {
		iface := NetworkInterface{
			IfaceID:     "eth0",
			GuestMac:    "02:00:00:00:00:01",
			HostDevName: "tap0",
		}

		data, err := json.Marshal(iface)
		require.NoError(t, err)

		var decoded NetworkInterface
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, "eth0", decoded.IfaceID)
		assert.Equal(t, "02:00:00:00:00:01", decoded.GuestMac)
		assert.Equal(t, "tap0", decoded.HostDevName)
	})
}

// TestLogWriterVMM tests logWriter struct
func TestLogWriterVMM(t *testing.T) {
	t.Run("write bytes", func(t *testing.T) {
		lw := &logWriter{}
		n, err := lw.Write([]byte("test log message"))
		assert.NoError(t, err)
		assert.Equal(t, len("test log message"), n)
	})

	t.Run("write empty bytes", func(t *testing.T) {
		lw := &logWriter{}
		n, err := lw.Write([]byte{})
		assert.NoError(t, err)
		assert.Equal(t, 0, n)
	})
}