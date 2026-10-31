package swarmkit

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestCapture records the requests a mock Firecracker API server receives,
// along with the order in which they arrived.
type requestCapture struct {
	mu     sync.Mutex
	order  []string
	byPath map[string][]byte
}

func (c *requestCapture) record(path string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order = append(c.order, path)
	c.byPath[path] = body
}

func (c *requestCapture) get(path string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, ok := c.byPath[path]
	return body, ok
}

func (c *requestCapture) sequence() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.order...)
}

// startVsockCaptureServer serves an HTTP API over a Unix socket, answering 204
// to every request and recording each one. It is the smallest seam needed to
// exercise configureVM/putAPI without a live Firecracker.
func startVsockCaptureServer(t *testing.T, socketPath string) *requestCapture {
	t.Helper()

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	capture := &requestCapture{byPath: make(map[string][]byte)}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			capture.record(r.URL.Path, body)
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	return capture
}

// vsockTestTask returns a minimal container task accepted by Translate.
func vsockTestTask() *types.Task {
	return &types.Task{
		ID: "task-vsock",
		Spec: types.TaskSpec{
			Runtime: &types.Container{Image: "nginx:latest"},
		},
	}
}

// TestTranslate_ConfiguresVsock verifies the daemon translator emits a vsock
// section so configureVM can register the device before the VM boots.
func TestTranslate_ConfiguresVsock(t *testing.T) {
	translator, err := NewTaskTranslator("/test/kernel", "192.168.127.1/24")
	require.NoError(t, err)

	result, err := translator.Translate(vsockTestTask())
	require.NoError(t, err)

	config, ok := result.(map[string]interface{})
	require.True(t, ok, "Translate must return map[string]interface{}")

	vsock, ok := config["vsock"].(map[string]interface{})
	require.True(t, ok, "config must carry a vsock section")
	assert.Equal(t, agent.GuestCID, vsock["guest_cid"])
	assert.Equal(t, "vsock0", vsock["vsock_id"])
}

// TestConfigureVM_ConfiguresVsockDevice verifies configureVM PUTs /vsock with a
// uds_path derived from the API socket, before InstanceStart.
func TestConfigureVM_ConfiguresVsockDevice(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "task-vsock.sock")
	capture := startVsockCaptureServer(t, socketPath)

	vmm := &VMMManager{
		firecrackerPath: "firecracker",
		socketDir:       tmpDir,
		processes:       make(map[string]*exec.Cmd),
	}

	config := map[string]interface{}{
		"machine-config": map[string]interface{}{
			"vcpu_count":   2,
			"mem_size_mib": 512,
		},
		"boot-source": map[string]interface{}{
			"kernel_image_path": "/vmlinux",
		},
		"drives": []interface{}{
			map[string]interface{}{
				"drive_id":       "rootfs",
				"path_on_host":   "/rootfs.ext4",
				"is_root_device": true,
			},
		},
		"vsock": map[string]interface{}{
			"guest_cid": agent.GuestCID,
			"vsock_id":  "vsock0",
		},
	}

	err := vmm.configureVM(context.Background(), &types.Task{ID: "task-vsock"}, socketPath, config)
	require.NoError(t, err)

	req, ok := capture.get("/vsock")
	require.True(t, ok, "configureVM must PUT /vsock")

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(req, &body))
	assert.Equal(t, float64(agent.GuestCID), body["guest_cid"])
	assert.Equal(t, "vsock0", body["vsock_id"])

	udsPath, ok := body["uds_path"].(string)
	require.True(t, ok, "PUT /vsock must carry a uds_path")
	assert.Equal(t, filepath.Join(tmpDir, "task-vsock.vsock"), udsPath,
		"the muxer sits beside the API socket with the vsock extension")
	assert.True(t, filepath.IsAbs(udsPath), "uds_path must be absolute")

	// Firecracker rejects /vsock after the VM has started, so it must precede
	// the InstanceStart action.
	sequence := capture.sequence()
	vsockIdx := indexOf(sequence, "/vsock")
	actionsIdx := indexOf(sequence, "/actions")
	require.NotEqual(t, -1, vsockIdx, "vsock request not observed")
	require.NotEqual(t, -1, actionsIdx, "InstanceStart action not observed")
	assert.Less(t, vsockIdx, actionsIdx, "/vsock must be configured before InstanceStart")
}

// TestConfigureVM_VsockErrorIsWrapped verifies a failed vsock PUT is surfaced
// with context rather than silently ignored.
func TestConfigureVM_VsockErrorIsWrapped(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "task-vsock.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/vsock" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("vsock not supported"))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	vmm := &VMMManager{
		firecrackerPath: "firecracker",
		socketDir:       tmpDir,
		processes:       make(map[string]*exec.Cmd),
	}

	config := map[string]interface{}{
		"machine-config": map[string]interface{}{"vcpu_count": 2, "mem_size_mib": 512},
		"boot-source":    map[string]interface{}{"kernel_image_path": "/vmlinux"},
		"drives": []interface{}{
			map[string]interface{}{"drive_id": "rootfs", "path_on_host": "/rootfs.ext4", "is_root_device": true},
		},
		"vsock": map[string]interface{}{"guest_cid": agent.GuestCID, "vsock_id": "vsock0"},
	}

	err = vmm.configureVM(context.Background(), &types.Task{ID: "task-vsock"}, socketPath, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to set vsock")
}

func indexOf(haystack []string, needle string) int {
	for i, v := range haystack {
		if v == needle {
			return i
		}
	}
	return -1
}
