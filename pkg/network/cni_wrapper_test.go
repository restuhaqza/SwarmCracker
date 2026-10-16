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

// TestCreateTAPDevice_Wrapper_Fails covers the CreateTAPDevice wrapper's error
// path. It used to rely on "no root → tuntap add fails", but as root the device
// was really created (and left behind) and the *bridge attach* failed, so the
// expected message never matched (#84). An over-long name is rejected by the
// kernel for every user, so the creation call is the one that fails and nothing
// is left behind.
func TestCreateTAPDevice_Wrapper_Fails(t *testing.T) {
	// Longer than IFNAMSIZ (16, so 15 usable bytes): `ip tuntap add` cannot
	// accept it, with or without privileges.
	const tooLong = "tap-this-name-is-too-long-for-a-nic"
	require.Greater(t, len(tooLong), 15)

	_, err := CreateTAPDevice(tooLong, "br-test-xyz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create TAP device")
}

func TestDeleteTAPDevice_Wrapper_NotExist(t *testing.T) {
	// DeleteTAPDevice on non-existent device → will fail on ip link delete
	err := DeleteTAPDevice("tap-nonexist-xyz")
	require.Error(t, err) // ip link delete fails on non-existent
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
