package swarmkit

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// balloonTestConfig returns a minimal config that configureVM accepts, with an
// optional "balloon" entry whose amount_mib is typed by the caller.
func balloonTestConfig(balloon interface{}) map[string]interface{} {
	config := map[string]interface{}{
		"machine-config": map[string]interface{}{
			"vcpu_count":   2,
			"mem_size_mib": 512,
			"smt":          false,
		},
		"boot-source": map[string]interface{}{"kernel_image_path": "/vmlinux"},
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

// balloonTestVMM returns a VMMManager wired to talk to the capture server.
func balloonTestVMM(socketDir string) *VMMManager {
	return &VMMManager{
		firecrackerPath: "firecracker",
		socketDir:       socketDir,
		processes:       make(map[string]*exec.Cmd),
	}
}

// TestConfigureVM_ConfiguresBalloonDevice verifies configureVM PUTs /balloon
// with the translator's entry, before InstanceStart, so the device exists on a
// VM that has never booted.
func TestConfigureVM_ConfiguresBalloonDevice(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "task-balloon.sock")
	capture := startVsockCaptureServer(t, socketPath)

	config := balloonTestConfig(map[string]interface{}{
		"amount_mib":               int64(256),
		"deflate_on_oom":           true,
		"stats_polling_interval_s": 0,
	})

	err := balloonTestVMM(tmpDir).configureVM(
		context.Background(), &types.Task{ID: "task-balloon"}, socketPath, config)
	require.NoError(t, err)

	body, ok := capture.get("/balloon")
	require.True(t, ok, "configureVM must PUT /balloon")
	assert.JSONEq(t, `{"amount_mib":256,"deflate_on_oom":true,"stats_polling_interval_s":0}`, string(body))

	sequence := capture.sequence()
	balloonIdx := indexOf(sequence, "/balloon")
	actionsIdx := indexOf(sequence, "/actions")
	require.NotEqual(t, -1, balloonIdx, "balloon request not observed")
	require.NotEqual(t, -1, actionsIdx, "InstanceStart action not observed")
	assert.Less(t, balloonIdx, actionsIdx, "/balloon must be configured before InstanceStart")
}

// TestConfigureVM_BalloonAcceptsJSONNumber verifies an amount that arrives as
// float64 — as it does after pkg/translator's JSON round-trip — is accepted
// rather than dropped.
func TestConfigureVM_BalloonAcceptsJSONNumber(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "task-balloon.sock")
	capture := startVsockCaptureServer(t, socketPath)

	config := balloonTestConfig(map[string]interface{}{
		"amount_mib":               float64(128),
		"deflate_on_oom":           true,
		"stats_polling_interval_s": 0,
	})

	err := balloonTestVMM(tmpDir).configureVM(
		context.Background(), &types.Task{ID: "task-balloon"}, socketPath, config)
	require.NoError(t, err)

	body, ok := capture.get("/balloon")
	require.True(t, ok, "configureVM must PUT /balloon")
	assert.JSONEq(t, `{"amount_mib":128,"deflate_on_oom":true,"stats_polling_interval_s":0}`, string(body))
}

// TestConfigureVM_BalloonUnexpectedTypeIsWrapped verifies a malformed balloon
// entry fails loudly instead of being sent to Firecracker or silently ignored.
func TestConfigureVM_BalloonUnexpectedTypeIsWrapped(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "task-balloon.sock")
	capture := startVsockCaptureServer(t, socketPath)

	config := balloonTestConfig(map[string]interface{}{
		"amount_mib":               "256",
		"deflate_on_oom":           true,
		"stats_polling_interval_s": 0,
	})

	err := balloonTestVMM(tmpDir).configureVM(
		context.Background(), &types.Task{ID: "task-balloon"}, socketPath, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "balloon")

	_, sent := capture.get("/balloon")
	assert.False(t, sent, "a malformed balloon entry must not reach Firecracker")
}

// TestConfigureVM_NoBalloonWhenUnconfigured verifies an unconfigured VM sends
// no /balloon request at all, so its behaviour is byte-identical to before.
func TestConfigureVM_NoBalloonWhenUnconfigured(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "task-balloon.sock")
	capture := startVsockCaptureServer(t, socketPath)

	err := balloonTestVMM(tmpDir).configureVM(
		context.Background(), &types.Task{ID: "task-balloon"}, socketPath, balloonTestConfig(nil))
	require.NoError(t, err)

	_, ok := capture.get("/balloon")
	assert.False(t, ok, "an unconfigured VM must not PUT /balloon")
	assert.NotContains(t, capture.sequence(), "/balloon")
}
