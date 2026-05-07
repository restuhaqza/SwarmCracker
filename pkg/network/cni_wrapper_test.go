//go:build !integration

package network

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// CNI wrapper function tests — call real ip command
// These functions call NewDefaultTAPExecutor() which uses real exec.Command.
// Since `ip` binary exists on the system, some paths are testable.
// =============================================================================

func TestCreateTAPDevice_Wrapper_Fails(t *testing.T) {
	// CreateTAPDevice calls real ip command → will fail without root
	_, err := CreateTAPDevice("tap-test-xyz", "br-test-xyz")
	require.Error(t, err) // No root → tuntap add fails
	assert.Contains(t, err.Error(), "failed to create TAP device")
}

func TestDeleteTAPDevice_Wrapper_NotExist(t *testing.T) {
	// DeleteTAPDevice on non-existent device → will fail on ip link delete
	err := DeleteTAPDevice("tap-nonexist-xyz")
	require.Error(t, err) // ip link delete fails on non-existent
}

func TestTAPDeviceExists_Wrapper_NotExist(t *testing.T) {
	exists, err := TAPDeviceExists("tap-nonexist-xyz")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestGetTAPMAC_Wrapper_NotExist(t *testing.T) {
	_, err := getTAPMAC("tap-nonexist-xyz")
	require.Error(t, err) // ip -br link show fails
}

func TestConfigureTAPIP_Wrapper_Fails(t *testing.T) {
	err := ConfigureTAPIP("tap-nonexist-xyz", "10.0.0.2/24")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to set IP on TAP")
}

func TestCreateBridge_Wrapper_Fails(t *testing.T) {
	err := CreateBridge("br-test-fail-xyz", "10.0.0.0/24")
	require.Error(t, err) // No root → ip link add fails
}

func TestSetupVXLANFDB_WithPeers_Direct(t *testing.T) {
	// This uses exec.Command("bridge", "fdb", ...) directly
	// bridge fdb add will fail without proper setup, but function handles errors gracefully
	err := SetupVXLANFDB("tap-test", []string{"10.0.0.2", "  ", ""})
	// Should not return error (logs warnings only)
	require.NoError(t, err)
}

func TestSetupVXLANFDB_SinglePeer_Direct(t *testing.T) {
	err := SetupVXLANFDB("tap-test", []string{"192.168.1.100"})
	require.NoError(t, err)
}
