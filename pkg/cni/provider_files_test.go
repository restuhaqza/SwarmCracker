package cni

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCNIProvider_PredefinedNetworks_Unit verifies the static predefined
// network list without needing any CNI plugins or provider state, so it runs
// under -short. (The pre-existing TestCNIProvider_PredefinedNetworks in
// cni_test.go is short-skipped because it builds a real provider.)
func TestCNIProvider_PredefinedNetworks_Unit(t *testing.T) {
	provider := &CNIProvider{}

	networks := provider.PredefinedNetworks()
	require.Len(t, networks, 2, "there should be exactly two predefined networks")

	assert.Equal(t, IngressNetworkName, networks[0].Name)
	assert.Equal(t, "bridge", networks[0].Driver, "ingress must use the bridge driver")
	assert.Equal(t, GWBridgeNetworkName, networks[1].Name)
	assert.Equal(t, "bridge", networks[1].Driver, "gateway bridge must use the bridge driver")
}

func TestCNIProvider_AllocateNetwork_Bridge(t *testing.T) {
	provider := setupCNIProvider(t)

	allocated, err := provider.AllocateNetwork("test-net", "bridge", "", "")
	require.NoError(t, err, "AllocateNetwork should succeed for the bridge driver")
	require.NotNil(t, allocated)

	// The generated network ID is keyed by the internal allocation index.
	assert.Equal(t, "net-1", allocated.ID)
	assert.Equal(t, "test-net", allocated.Name)
	assert.Equal(t, "bridge", allocated.Driver)
	require.NotNil(t, allocated.Subnet, "a subnet should be assigned")
	assert.NotEmpty(t, allocated.Subnet.String(), "subnet should be non-empty")
	require.NotNil(t, allocated.Gateway, "a gateway should be assigned")
	assert.Equal(t, "br-test_net", allocated.BridgeName, "bridge name derives from the network name")
	assert.Zero(t, allocated.VXLANID, "bridge networks must not get a VXLAN ID")
	require.NotNil(t, allocated.Attachments)
	require.NotNil(t, allocated.Services)

	// A CNI config file must have been written for the network.
	configPath := filepath.Join(provider.config.ConfigDir, "test-net.conf")
	_, statErr := os.Stat(configPath)
	require.NoError(t, statErr, "AllocateNetwork should write a CNI config file")
}

func TestCNIProvider_AllocateNetwork_VXLAN(t *testing.T) {
	provider := setupCNIProvider(t)

	allocated, err := provider.AllocateNetwork("vxlan-net", "vxlan", "", "")
	require.NoError(t, err, "AllocateNetwork should succeed for the vxlan driver")
	require.NotNil(t, allocated)

	assert.Equal(t, "net-1", allocated.ID)
	assert.Equal(t, "vxlan", allocated.Driver)
	assert.NotZero(t, allocated.VXLANID, "vxlan networks should be assigned a VXLAN ID")
	require.NotNil(t, allocated.Subnet)

	configPath := filepath.Join(provider.config.ConfigDir, "vxlan-net.conf")
	_, statErr := os.Stat(configPath)
	require.NoError(t, statErr, "AllocateNetwork should write a CNI config file for vxlan")
}

func TestCNIProvider_AllocateNetwork_RequestedSubnet(t *testing.T) {
	provider := setupCNIProvider(t)

	// A user-requested subnet must be honored verbatim.
	allocated, err := provider.AllocateNetwork("custom", "vxlan", "10.10.0.0/24", "")
	require.NoError(t, err)
	require.NotNil(t, allocated.Subnet)
	assert.Equal(t, "10.10.0.0/24", allocated.Subnet.String())
	assert.Equal(t, "10.10.0.1", allocated.Gateway.String(), "gateway defaults to the first usable host")

	// An explicit gateway overrides the default.
	allocated2, err := provider.AllocateNetwork("custom2", "vxlan", "10.20.0.0/24", "10.20.0.254")
	require.NoError(t, err)
	assert.Equal(t, "10.20.0.0/24", allocated2.Subnet.String())
	assert.Equal(t, "10.20.0.254", allocated2.Gateway.String())
}

func TestCNINetworkAllocator_Allocate_HonorsRequestedSubnet(t *testing.T) {
	a := newTestAllocator(t)
	network := &api.Network{
		ID: "n1",
		Spec: api.NetworkSpec{
			Annotations:  api.Annotations{Name: "backend"},
			DriverConfig: &api.Driver{Name: "vxlan"},
			IPAM:         &api.IPAMOptions{Configs: []*api.IPAMConfig{{Subnet: "10.99.0.0/24"}}},
		},
	}

	require.NoError(t, a.Allocate(network))
	require.NotNil(t, network.IPAM)
	require.Len(t, network.IPAM.Configs, 1)
	assert.Equal(t, "10.99.0.0/24", network.IPAM.Configs[0].Subnet)
	assert.Equal(t, "10.99.0.1", network.IPAM.Configs[0].Gateway)
	require.NotNil(t, network.DriverState)
	assert.Equal(t, "br-backend", network.DriverState.Options["bridge"])
	assert.NotEmpty(t, network.DriverState.Options["vxlan_vni"], "overlay networks get a VNI")
}

func TestCNIProvider_GetNetwork(t *testing.T) {
	provider := setupCNIProvider(t)

	allocated, err := provider.AllocateNetwork("lookup-net", "bridge", "", "")
	require.NoError(t, err)

	// GetNetwork is keyed by the generated "net-N" ID, not the caller's name.
	fetched, err := provider.GetNetwork(allocated.ID)
	require.NoError(t, err, "GetNetwork should find the allocated network by ID")
	assert.Same(t, allocated, fetched, "GetNetwork should return the stored network instance")

	_, err = provider.GetNetwork("net-999")
	assert.Error(t, err, "GetNetwork should error for an unknown ID")

	_, err = provider.GetNetwork("")
	assert.Error(t, err, "GetNetwork should error for an empty ID")
}

func TestValidateCNINetworkName(t *testing.T) {
	longName := strings.Repeat("a", 300)

	tests := []struct {
		name    string
		wantErr bool
	}{
		// The real contract rejects only empty, "." / "..", and path separators.
		{name: "", wantErr: true},
		{name: ".", wantErr: true},
		{name: "..", wantErr: true},
		{name: "a/b", wantErr: true},
		{name: `a\b`, wantErr: true},
		// Anything else is accepted, including colons and very long names.
		{name: "valid-net", wantErr: false},
		{name: "net:name", wantErr: false},
		{name: longName, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCNINetworkName(tt.name)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRemoveCNIConfig(t *testing.T) {
	configDir := t.TempDir()

	// Write a config file, then remove it.
	require.NoError(t, WriteConfig(configDir, "test-net", []byte(`{"name":"test-net"}`)))
	configPath := filepath.Join(configDir, "test-net.conf")
	require.FileExists(t, configPath)

	require.NoError(t, RemoveCNIConfig(configDir, "test-net"))
	_, err := os.Stat(configPath)
	assert.True(t, os.IsNotExist(err), "config file should be removed")

	// Removing a name with no config file present is not an error.
	assert.NoError(t, RemoveCNIConfig(configDir, "absent"), "absent config should not be an error")

	// Invalid names are rejected before touching the filesystem.
	assert.Error(t, RemoveCNIConfig(configDir, ""), "empty network name should be rejected")
	assert.Error(t, RemoveCNIConfig(configDir, "../escape"), "path separators should be rejected")
}
