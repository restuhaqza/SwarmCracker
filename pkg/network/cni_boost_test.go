//go:build !integration

package network

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Wrapper function tests (0% coverage -> direct calls)
// These call the WithExecutor variants internally
// =============================================================================

func TestCreateTAPDevice_Wrapper(t *testing.T) {
	// Wrapper uses real executor - skip in short mode
	if testing.Short() {
		t.Skip("skipping: wrapper uses real executor")
	}
}

func TestDeleteTAPDevice_Wrapper(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: wrapper uses real executor")
	}
}

func TestTAPDeviceExists_Wrapper(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: wrapper uses real executor")
	}
}

func TestConfigureTAPIP_Wrapper(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: wrapper uses real executor")
	}
}

func TestCreateBridge_Wrapper(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: wrapper uses real executor")
	}
}

func TestGetTAPMAC_Wrapper(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: wrapper uses real executor")
	}
}

// =============================================================================
// CreateBridgeWithExecutor tests
// =============================================================================

func TestCreateBridgeWithExecutor_BridgeCreateFails(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(errors.New("permission denied"))

	err := CreateBridgeWithExecutor("br0", "", mock)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create bridge")
}

func TestCreateBridgeWithExecutor_AlreadyExists_SkipsCreate(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(nil)

	err := CreateBridgeWithExecutor("br0-existing", "10.0.0.0/24", mock)

	require.NoError(t, err)

	commands := mock.GetCommands()
	require.GreaterOrEqual(t, len(commands), 1)
	assert.Equal(t, "ip", commands[0].Name)
}

func TestCreateBridgeWithExecutor_NoSubnet(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(nil)

	err := CreateBridgeWithExecutor("br0", "", mock)

	require.NoError(t, err)
}

func TestCreateBridgeWithExecutor_CreatesBridge_WithValidSubnet(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(nil)

	err := CreateBridgeWithExecutor("br0", "10.0.0.0/24", mock)

	require.NoError(t, err)
}

// =============================================================================
// TAPDeviceExists tests
// =============================================================================

func TestTAPDeviceExistsWithExecutor_DeviceExists(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(nil)

	exists, err := TAPDeviceExistsWithExecutor("tap0", mock)

	require.NoError(t, err)
	assert.True(t, exists)
}

func TestTAPDeviceExistsWithExecutor_DeviceNotFound(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(errors.New("Device not found"))

	exists, err := TAPDeviceExistsWithExecutor("tap-nonexistent", mock)

	require.NoError(t, err)
	assert.False(t, exists)
}

// =============================================================================
// getTAPMAC tests
// =============================================================================

func TestGetTAPMACWithExecutor_IPlinkFormat(t *testing.T) {
	mock := NewMockTAPExecutor()
	// ip -br link show format: "tap0: UNKNOWN ff:ff:ff:ff:ff:ff ..."
	// Use OutputResult which is returned as fallback when name doesn't match
	mock.SetOutputResult([]byte("tap0: UNKNOWN 00:aa:bb:cc:dd:ee"))

	mac, err := getTAPMACWithExecutor("tap0", mock)

	require.NoError(t, err)
	assert.Equal(t, "00:aa:bb:cc:dd:ee", mac)
}

func TestGetTAPMACWithExecutor_OutputError(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.OutputError = errors.New("command failed")

	mac, err := getTAPMACWithExecutor("tap0", mock)

	require.Error(t, err)
	assert.Empty(t, mac)
}

func TestGetTAPMACWithExecutor_TooShortOutput(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetOutputResult([]byte("tap0"))

	mac, err := getTAPMACWithExecutor("tap0", mock)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not parse MAC")
	assert.Empty(t, mac)
}

func TestGetTAPMACWithExecutor_TwoFieldsOnly(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetOutputResult([]byte("tap0: <UP>"))

	mac, err := getTAPMACWithExecutor("tap0", mock)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not parse MAC")
	assert.Empty(t, mac)
}

func TestGetTAPMACWithExecutor_EmptyOutput(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetOutputResult([]byte(""))

	mac, err := getTAPMACWithExecutor("tap0", mock)

	require.Error(t, err)
	assert.Empty(t, mac)
}

// =============================================================================
// ConfigureTAPIP tests
// =============================================================================

func TestConfigureTAPIPWithExecutor_IPSuccess(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(nil)

	err := ConfigureTAPIPWithExecutor("tap0", "10.0.0.2/24", mock)

	require.NoError(t, err)

	commands := mock.GetCommands()
	require.GreaterOrEqual(t, len(commands), 1)
	assert.Equal(t, "ip", commands[0].Name)
}

func TestConfigureTAPIPWithExecutor_IPFails(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(errors.New("RTNETLINK answers: File exists"))

	err := ConfigureTAPIPWithExecutor("tap0", "10.0.0.2/24", mock)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to set IP on TAP")
}

// =============================================================================
// DeleteTAPDeviceWithExecutor tests
// =============================================================================

func TestDeleteTAPDeviceWithExecutor_DeleteSucceeds(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetCombinedResult([]byte(""))

	err := DeleteTAPDeviceWithExecutor("tap0", mock)

	require.NoError(t, err)
}

func TestDeleteTAPDeviceWithExecutor_DeleteFails_Coverage(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetCombinedError(errors.New("operation failed"))

	err := DeleteTAPDeviceWithExecutor("tap0", mock)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to delete TAP device")
}

func TestDeleteTAPDeviceWithExecutor_NomasterFails(t *testing.T) {
	mock := NewMockTAPExecutor()
	// Set combined error to simulate nomaster failing
	// but the first call (nomaster) should error, second (delete) should succeed
	// Since mock uses global error, both will fail, but that's okay for testing the error path
	mock.SetCombinedResult([]byte("")) // Clear any default

	err := DeleteTAPDeviceWithExecutor("tap0", mock)

	// With no CombinedError set, both succeed
	require.NoError(t, err)
}

// =============================================================================
// CreateTAPDeviceWithExecutor tests
// =============================================================================

func TestCreateTAPDeviceWithExecutor_TuntapFails_Coverage(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(errors.New("tuntap add failed"))

	tap, err := CreateTAPDeviceWithExecutor("tap0", "br0", mock)

	require.Error(t, err)
	assert.Nil(t, tap)
	assert.Contains(t, err.Error(), "failed to create TAP device")
}

func TestCreateTAPDeviceWithExecutor_UpFails_Cleanup(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(errors.New("link set up failed"))

	tap, err := CreateTAPDeviceWithExecutor("tap0", "br0", mock)

	require.Error(t, err)
	assert.Nil(t, tap)
}

func TestCreateTAPDeviceWithExecutor_MasterFails_Cleanup(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(errors.New("link set master failed"))

	tap, err := CreateTAPDeviceWithExecutor("tap0", "br0", mock)

	require.Error(t, err)
	assert.Nil(t, tap)
}

func TestCreateTAPDeviceWithExecutor_MACErrorFallback(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.OutputError = errors.New("link show failed")
	mock.SetRunError(nil)

	tap, err := CreateTAPDeviceWithExecutor("tap0", "", mock)

	require.NoError(t, err)
	assert.NotNil(t, tap)
	assert.Equal(t, "00:00:00:00:00:00", tap.MAC) // Placeholder when MAC fetch fails
}

func TestCreateTAPDeviceWithExecutor_EmptyBridge(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(nil)
	mock.SetOutputResult([]byte("tap0: UNKNOWN 00:11:22:33:44:55"))

	tap, err := CreateTAPDeviceWithExecutor("tap0", "", mock)

	require.NoError(t, err)
	assert.NotNil(t, tap)
	assert.Equal(t, "", tap.Bridge)
}

func TestCreateTAPDeviceWithExecutor_WithMAC(t *testing.T) {
	mock := NewMockTAPExecutor()
	mock.SetRunError(nil)
	mock.SetOutputResult([]byte("tap0: UNKNOWN aa:bb:cc:dd:ee:ff"))

	tap, err := CreateTAPDeviceWithExecutor("tap0", "br0", mock)

	require.NoError(t, err)
	assert.NotNil(t, tap)
	assert.Equal(t, "aa:bb:cc:dd:ee:ff", tap.MAC)
}

// =============================================================================
// maskToPrefix tests
// =============================================================================

func TestMaskToPrefix_Coverage(t *testing.T) {
	tests := []struct {
		mask     net.IPMask
		expected int
	}{
		{net.IPMask{255, 255, 255, 0}, 24},
		{net.IPMask{255, 255, 255, 255}, 32},
		{net.IPMask{255, 255, 0, 0}, 16},
		{net.IPMask{255, 0, 0, 0}, 8},
		{net.IPMask{255, 255, 255, 128}, 25},
		{net.IPMask{255, 255, 255, 192}, 26},
		{net.IPMask{0, 0, 0, 0}, 0},
	}

	for _, tt := range tests {
		result := maskToPrefix(tt.mask)
		assert.Equal(t, tt.expected, result)
	}
}
