//go:build !integration

package lifecycle

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortTempDir creates a temporary directory with a short path. Unix socket
// paths are limited to ~104 bytes, which t.TempDir() can exceed on some hosts.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "scls-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// vsockRequestCapture records the requests a mock Firecracker API server
// receives over its Unix socket.
type vsockRequestCapture struct {
	mu     sync.Mutex
	byPath map[string][]byte
}

func (c *vsockRequestCapture) record(path string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byPath[path] = body
}

func (c *vsockRequestCapture) get(path string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, ok := c.byPath[path]
	return body, ok
}

// startVsockCaptureServer serves an HTTP API over a Unix socket, answering 204
// to every request and recording each one.
func startVsockCaptureServer(t *testing.T, socketPath string) *vsockRequestCapture {
	t.Helper()

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	capture := &vsockRequestCapture{byPath: make(map[string][]byte)}
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

// TestConfigureVM_ConfiguresVsockDevice verifies the CLI VM path PUTs /vsock
// with a uds_path derived from the API socket.
func TestConfigureVM_ConfiguresVsockDevice(t *testing.T) {
	tmpDir := shortTempDir(t)
	socketPath := filepath.Join(tmpDir, "task-vsock.sock")
	capture := startVsockCaptureServer(t, socketPath)

	vmm := &VMMManager{
		config:    &ManagerConfig{SocketDir: tmpDir},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	config := map[string]interface{}{
		"boot-source": map[string]interface{}{
			"kernel_image_path": "/vmlinux",
			"boot_args":         "console=ttyS0",
		},
		"machine-config": map[string]interface{}{
			"vcpu_count":   2,
			"mem_size_mib": 512,
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

	err := vmm.configureVM(context.Background(), socketPath, config)
	require.NoError(t, err)

	body, ok := capture.get("/vsock")
	require.True(t, ok, "configureVM must PUT /vsock")

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &payload))
	assert.Equal(t, float64(agent.GuestCID), payload["guest_cid"])
	assert.Equal(t, "vsock0", payload["vsock_id"])

	udsPath, ok := payload["uds_path"].(string)
	require.True(t, ok, "PUT /vsock must carry a uds_path")
	assert.Equal(t, filepath.Join(tmpDir, "task-vsock.vsock"), udsPath,
		"the muxer sits beside the API socket with the vsock extension")
	assert.True(t, filepath.IsAbs(udsPath), "uds_path must be absolute")
}

// TestConfigureVM_VsockErrorIsWrapped verifies a failed vsock PUT is surfaced
// with context rather than silently ignored.
func TestConfigureVM_VsockErrorIsWrapped(t *testing.T) {
	tmpDir := shortTempDir(t)
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
		config:    &ManagerConfig{SocketDir: tmpDir},
		vms:       make(map[string]*VMInstance),
		socketDir: tmpDir,
	}

	config := map[string]interface{}{
		"boot-source":    map[string]interface{}{"kernel_image_path": "/vmlinux"},
		"machine-config": map[string]interface{}{"vcpu_count": 2, "mem_size_mib": 512},
		"drives": []interface{}{
			map[string]interface{}{"drive_id": "rootfs", "path_on_host": "/rootfs.ext4", "is_root_device": true},
		},
		"vsock": map[string]interface{}{"guest_cid": agent.GuestCID, "vsock_id": "vsock0"},
	}

	err = vmm.configureVM(context.Background(), socketPath, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vsock")
	assert.Contains(t, err.Error(), "status")
}
