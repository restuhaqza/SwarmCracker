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
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// balloonTestVMM returns a VMMManager wired to a caller-supplied socket dir.
func balloonTestVMM(socketDir string) *VMMManager {
	return &VMMManager{
		config:    &ManagerConfig{SocketDir: socketDir},
		vms:       make(map[string]*VMInstance),
		socketDir: socketDir,
	}
}

// balloonTestConfig returns a minimal config that configureVM accepts, with an
// optional "balloon" entry whose amount_mib is typed by the caller.
func balloonTestConfig(balloon interface{}) map[string]interface{} {
	config := map[string]interface{}{
		"boot-source":    map[string]interface{}{"kernel_image_path": "/vmlinux"},
		"machine-config": map[string]interface{}{"vcpu_count": 2, "mem_size_mib": 512},
		"drives": []interface{}{
			map[string]interface{}{
				"drive_id":       "rootfs",
				"path_on_host":   "/rootfs.ext4",
				"is_root_device": true,
			},
		},
	}
	if balloon != nil {
		config["balloon"] = balloon
	}
	return config
}

// TestConfigureVM_ConfiguresBalloonDevice verifies the CLI VM path PUTs
// /balloon with the configured target.
func TestConfigureVM_ConfiguresBalloonDevice(t *testing.T) {
	tmpDir := shortTempDir(t)
	socketPath := filepath.Join(tmpDir, "task-balloon.sock")
	capture := startVsockCaptureServer(t, socketPath)

	config := balloonTestConfig(map[string]interface{}{
		"amount_mib":               int64(256),
		"deflate_on_oom":           true,
		"stats_polling_interval_s": 0,
	})

	err := balloonTestVMM(tmpDir).configureVM(context.Background(), socketPath, config)
	require.NoError(t, err)

	body, ok := capture.get("/balloon")
	require.True(t, ok, "configureVM must PUT /balloon")
	assert.JSONEq(t, `{"amount_mib":256,"deflate_on_oom":true,"stats_polling_interval_s":0}`, string(body))
}

// TestConfigureVM_BalloonAcceptsJSONNumber verifies an amount that arrives as
// float64 — as it does after a JSON round-trip — is accepted rather than
// dropped.
func TestConfigureVM_BalloonAcceptsJSONNumber(t *testing.T) {
	tmpDir := shortTempDir(t)
	socketPath := filepath.Join(tmpDir, "task-balloon.sock")
	capture := startVsockCaptureServer(t, socketPath)

	config := balloonTestConfig(map[string]interface{}{
		"amount_mib":               float64(128),
		"deflate_on_oom":           true,
		"stats_polling_interval_s": 0,
	})

	err := balloonTestVMM(tmpDir).configureVM(context.Background(), socketPath, config)
	require.NoError(t, err)

	body, ok := capture.get("/balloon")
	require.True(t, ok, "configureVM must PUT /balloon")
	assert.JSONEq(t, `{"amount_mib":128,"deflate_on_oom":true,"stats_polling_interval_s":0}`, string(body))
}

// TestConfigureVM_BalloonUnexpectedTypeIsWrapped verifies a malformed balloon
// entry fails loudly instead of being sent to Firecracker or silently ignored.
func TestConfigureVM_BalloonUnexpectedTypeIsWrapped(t *testing.T) {
	tmpDir := shortTempDir(t)
	socketPath := filepath.Join(tmpDir, "task-balloon.sock")
	capture := startVsockCaptureServer(t, socketPath)

	config := balloonTestConfig(map[string]interface{}{
		"amount_mib":               "256",
		"deflate_on_oom":           true,
		"stats_polling_interval_s": 0,
	})

	err := balloonTestVMM(tmpDir).configureVM(context.Background(), socketPath, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "balloon")

	_, sent := capture.get("/balloon")
	assert.False(t, sent, "a malformed balloon entry must not reach Firecracker")
}

// TestConfigureVM_NoBalloonWhenUnconfigured verifies an unconfigured VM sends
// no /balloon request at all, so its behaviour is byte-identical to before.
func TestConfigureVM_NoBalloonWhenUnconfigured(t *testing.T) {
	tmpDir := shortTempDir(t)
	socketPath := filepath.Join(tmpDir, "task-balloon.sock")
	capture := startVsockCaptureServer(t, socketPath)

	err := balloonTestVMM(tmpDir).configureVM(context.Background(), socketPath, balloonTestConfig(nil))
	require.NoError(t, err)

	_, ok := capture.get("/balloon")
	assert.False(t, ok, "an unconfigured VM must not PUT /balloon")
}

// The balloon device cannot be added to a running VM, so it must be PUT before
// the InstanceStart action. Unlike the swarmkit path — where configureVM owns
// the /actions PUT — the lifecycle path issues /actions from Start after
// configureVM returns, so this exercises the whole Start sequence: the test
// spawns itself as a fake "firecracker" API server over the VM's Unix socket.

const fakeFirecrackerEnvVar = "SWARMCRACKER_FAKE_FIRECRACKER"
const fakeFirecrackerRecordEnvVar = "SWARMCRACKER_FAKE_FIRECRACKER_RECORD"

// balloonRequest is one captured request: the path and raw body.
type balloonRequest struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

// TestFakeFirecrackerProcess is not a real test. The balloon ordering test
// re-execs this test binary as its "firecracker" binary; this function serves
// the Firecracker API on the socket named by --api-sock and appends every
// request (path and body) to the file named by SWARMCRACKER_FAKE_FIRECRACKER_RECORD.
func TestFakeFirecrackerProcess(t *testing.T) {
	if os.Getenv(fakeFirecrackerEnvVar) != "1" {
		t.Skip("fake firecracker helper is only run as a child process")
	}

	var socketPath string
	for i, arg := range os.Args {
		if arg == "--api-sock" && i+1 < len(os.Args) {
			socketPath = os.Args[i+1]
		}
	}
	recordPath := os.Getenv(fakeFirecrackerRecordEnvVar)
	if socketPath == "" || recordPath == "" {
		t.Fatalf("fake firecracker: --api-sock=%q record=%q", socketPath, recordPath)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("fake firecracker: listen: %v", err)
	}

	var mu sync.Mutex
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		line, _ := json.Marshal(balloonRequest{Path: r.URL.Path, Body: string(body)})

		mu.Lock()
		f, ferr := os.OpenFile(recordPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if ferr == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
		mu.Unlock()

		w.WriteHeader(http.StatusNoContent)
	})}

	_ = server.Serve(listener)
}

// TestConfigureVM_BalloonBeforeInstanceStart drives the full lifecycle Start
// against the fake API server and asserts /balloon precedes /actions.
func TestConfigureVM_BalloonBeforeInstanceStart(t *testing.T) {
	dir := shortTempDir(t)
	recordPath := filepath.Join(dir, "requests.log")

	// A fake "firecracker" that re-execs this test binary as the API server.
	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	script := "#!/bin/sh\nexec " + strconv.Quote(os.Args[0]) +
		" -test.run=TestFakeFirecrackerProcess -- \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "firecracker"), []byte(script), 0o755))

	oldPath := os.Getenv("PATH")
	t.Setenv(fakeFirecrackerEnvVar, "1")
	t.Setenv(fakeFirecrackerRecordEnvVar, recordPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	vm := balloonTestVMM(dir)
	task := &types.Task{ID: "task-balloon"}
	config := balloonTestConfig(map[string]interface{}{
		"amount_mib":               int64(256),
		"deflate_on_oom":           true,
		"stats_polling_interval_s": 0,
	})

	require.NoError(t, vm.Start(context.Background(), task, config))

	// Stop the fake firecracker process we spawned.
	vm.mu.RLock()
	instance := vm.vms[task.ID]
	vm.mu.RUnlock()
	require.NotNil(t, instance)
	if proc, err := os.FindProcess(instance.PID); err == nil {
		_ = proc.Kill()
	}

	requests := readBalloonRequests(t, recordPath)

	balloonIdx, actionsIdx := -1, -1
	for i, req := range requests {
		switch req.Path {
		case "/balloon":
			if balloonIdx == -1 {
				balloonIdx = i
				assert.JSONEq(t,
					`{"amount_mib":256,"deflate_on_oom":true,"stats_polling_interval_s":0}`,
					req.Body)
			}
		case "/actions":
			if actionsIdx == -1 {
				actionsIdx = i
			}
		}
	}
	require.NotEqual(t, -1, balloonIdx, "/balloon request not observed")
	require.NotEqual(t, -1, actionsIdx, "/actions request not observed")
	assert.Less(t, balloonIdx, actionsIdx, "/balloon must be configured before InstanceStart")
}

// readBalloonRequests parses the newline-delimited capture written by
// TestFakeFirecrackerProcess.
func readBalloonRequests(t *testing.T, path string) []balloonRequest {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var requests []balloonRequest
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var req balloonRequest
		require.NoError(t, json.Unmarshal([]byte(line), &req))
		requests = append(requests, req)
	}
	return requests
}
