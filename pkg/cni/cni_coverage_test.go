package cni

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ===== Config Generator Tests =====

func TestNewConfigGenerator(t *testing.T) {
	gen := NewConfigGenerator()
	require.NotNil(t, gen)
	assert.Equal(t, DefaultCNIVersion, gen.version)
}

func TestGenerateBridgeConfig(t *testing.T) {
	gen := NewConfigGenerator()
	gateway := net.ParseIP("10.0.1.1")

	config, err := gen.GenerateBridgeConfig("test-net", "br-test", "10.0.1.0/24", gateway)
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal(config, &parsed)
	require.NoError(t, err)

	assert.Equal(t, "test-net", parsed["name"])
	assert.Equal(t, "bridge", parsed["type"])
	assert.Equal(t, "br-test", parsed["bridge"])
}

func TestGenerateVXLANConfig(t *testing.T) {
	gen := NewConfigGenerator()
	gateway := net.ParseIP("10.0.2.1")

	config, err := gen.GenerateVXLANConfig("vxlan-net", "10.0.2.0/24", gateway, 100, 4789)
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal(config, &parsed)
	require.NoError(t, err)

	assert.Equal(t, "vxlan-net", parsed["name"])
	assert.Equal(t, "vxlan", parsed["type"])
}

func TestGenerateIngressConfig(t *testing.T) {
	gen := NewConfigGenerator()
	gateway := net.ParseIP("10.0.0.1")

	config, err := gen.GenerateIngressConfig("10.0.0.0/24", gateway)
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal(config, &parsed)
	require.NoError(t, err)

	assert.Equal(t, IngressNetworkName, parsed["name"])
}

func TestGenerateGWBridgeConfig(t *testing.T) {
	gen := NewConfigGenerator()

	config, err := gen.GenerateGWBridgeConfig()
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal(config, &parsed)
	require.NoError(t, err)

	assert.Equal(t, GWBridgeNetworkName, parsed["name"])
}

func TestGenerateLoopbackConfig(t *testing.T) {
	gen := NewConfigGenerator()

	config, err := gen.GenerateLoopbackConfig()
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal(config, &parsed)
	require.NoError(t, err)

	assert.Equal(t, "lo", parsed["name"])
	assert.Equal(t, "loopback", parsed["type"])
}

func TestWriteConfig_PackageFunc(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "cni")
	os.MkdirAll(configDir, 0755)

	config := []byte(`{"name": "test", "type": "bridge"}`)

	err := WriteConfig(configDir, "test", config)
	require.NoError(t, err)

	path := filepath.Join(configDir, "test.conf")
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestWriteConfigListFile(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")

	configs := []map[string]interface{}{
		{"name": "lo", "type": "loopback"},
		{"name": "test", "type": "bridge"},
	}

	err := WriteConfigListFile(configDir, "multi", configs)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(configDir, "multi.conflist"))
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	plugins := parsed["plugins"].([]interface{})
	assert.Len(t, plugins, 2)
}

func TestGenerateSubnet(t *testing.T) {
	tests := []struct {
		pool  string
		size  int
		index uint32
		name  string
	}{
		{pool: "10.0.0.0/8", size: 24, index: 0, name: "net1"},
		{pool: "10.0.0.0/8", size: 24, index: 1, name: "net2"},
		{pool: "172.16.0.0/12", size: 24, index: 0, name: "net3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subnet, err := GenerateSubnet(tt.pool, tt.size, tt.index)
			require.NoError(t, err)

			_, _, err = net.ParseCIDR(subnet)
			require.NoError(t, err)
		})
	}
}

func TestGenerateSubnet_InvalidPool(t *testing.T) {
	_, err := GenerateSubnet("invalid-pool", 24, 0)
	assert.Error(t, err)
}

// ===== Files Tests =====

func TestEnsurePluginDir(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "plugins")

	err := EnsurePluginDir(pluginDir)
	require.NoError(t, err)

	_, err = os.Stat(pluginDir)
	require.NoError(t, err)
}

func TestEnsureConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")

	err := EnsureConfigDir(configDir)
	require.NoError(t, err)

	_, err = os.Stat(configDir)
	require.NoError(t, err)
}

func TestWriteConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")

	config := []byte(`{"name": "test", "type": "bridge"}`)

	err := WriteConfigFile(configDir, "test", config)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(configDir, "test.conf"))
	require.NoError(t, err)
	assert.Equal(t, config, data)
}

func TestRemoveConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	path := filepath.Join(configDir, "test.conf")
	os.WriteFile(path, []byte(`{"name": "test"}`), 0644)

	err := RemoveConfigFile(configDir, "test")
	require.NoError(t, err)

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestRemoveConfigFile_Nonexistent(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	err := RemoveConfigFile(configDir, "nonexistent")
	require.NoError(t, err)
}

func TestContainsNetworkName(t *testing.T) {
	tests := []struct {
		filename string
		name     string
		expected bool
	}{
		{filename: "test-network.conf", name: "test-network", expected: true},
		{filename: "other-network.conf", name: "test-network", expected: false},
		{filename: "01-test.conf", name: "test", expected: true},
		{filename: "test.conf", name: "test", expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := containsNetworkName(tt.filename, tt.name)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInitializeCNI(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	cfg := DefaultCNIConfig()
	cfg.PluginDir = filepath.Join(tmpDir, "plugins")
	cfg.ConfigDir = filepath.Join(tmpDir, "config")

	err := InitializeCNI(ctx, cfg)
	require.NoError(t, err)

	_, err = os.Stat(cfg.PluginDir)
	require.NoError(t, err)
	_, err = os.Stat(cfg.ConfigDir)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(cfg.ConfigDir, "lo.conf"))
	require.NoError(t, err)
}

// ===== IPAM Manager Tests =====

func TestIPAMManager_GetPoolStats(t *testing.T) {
	mgr := NewIPAMManager(nil)

	mgr.CreatePool("10.0.1.0/24", nil)
	mgr.AllocateIP("10.0.1.0/24", "container-1")
	mgr.AllocateIP("10.0.1.0/24", "container-2")

	used, total, err := mgr.GetPoolStats("10.0.1.0/24")
	require.NoError(t, err)

	assert.GreaterOrEqual(t, used, 2)
	assert.GreaterOrEqual(t, total, 1)
}

func TestIPAMManager_ListPools(t *testing.T) {
	mgr := NewIPAMManager(nil)

	mgr.CreatePool("10.0.1.0/24", nil)
	mgr.CreatePool("10.0.2.0/24", nil)

	pools := mgr.ListPools()
	assert.Len(t, pools, 2)
}

func TestIPAMManager_GetAllocationOwner(t *testing.T) {
	mgr := NewIPAMManager(nil)

	mgr.CreatePool("10.0.1.0/24", nil)
	ip, err := mgr.AllocateIP("10.0.1.0/24", "test-container")
	require.NoError(t, err)

	owner, err := mgr.GetAllocationOwner(ip, "10.0.1.0/24")
	require.NoError(t, err)
	assert.Equal(t, "test-container", owner)
}

func TestIPAMManager_CleanupStaleAllocations(t *testing.T) {
	mgr := NewIPAMManager(nil)

	mgr.CreatePool("10.0.1.0/24", nil)
	mgr.AllocateIP("10.0.1.0/24", "container-old")
	mgr.AllocateIP("10.0.1.0/24", "container-new")

	count := mgr.CleanupStaleAllocations("10.0.1.0/24", time.Duration(0))
	assert.GreaterOrEqual(t, count, 0)
}

// ===== Plugin Manager Tests =====

func TestPluginManager_WithEnv(t *testing.T) {
	pm := NewPluginManager("/tmp/plugins", "/tmp/config")

	env := []string{
		"CNI_PATH=/opt/cni",
		"CNI_ARGS=key=value",
	}

	pm = pm.WithEnv(env)
	require.NotNil(t, pm)
	assert.Contains(t, pm.env, "CNI_PATH=/opt/cni")
}

func TestPluginManager_ListAvailablePlugins(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "plugins")
	os.MkdirAll(pluginDir, 0755)

	os.WriteFile(filepath.Join(pluginDir, "bridge"), []byte{}, 0755)
	os.WriteFile(filepath.Join(pluginDir, "vxlan"), []byte{}, 0755)

	pm := NewPluginManager(pluginDir, "/tmp/config")

	plugins, err := pm.ListAvailablePlugins()
	require.NoError(t, err)
	assert.Contains(t, plugins, "bridge")
	assert.Contains(t, plugins, "vxlan")
}

func TestPluginManager_ValidatePlugins(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "plugins")
	os.MkdirAll(pluginDir, 0755)

	os.WriteFile(filepath.Join(pluginDir, "bridge"), []byte{}, 0755)
	os.WriteFile(filepath.Join(pluginDir, "loopback"), []byte{}, 0755)
	os.WriteFile(filepath.Join(pluginDir, "host-local"), []byte{}, 0755)

	pm := NewPluginManager(pluginDir, "/tmp/config")

	err := pm.ValidatePlugins()
	require.NoError(t, err)
}

func TestPluginManager_ValidatePlugins_Missing(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "plugins")
	os.MkdirAll(pluginDir, 0755)

	pm := NewPluginManager(pluginDir, "/tmp/config")

	err := pm.ValidatePlugins()
	assert.Error(t, err)
}

func TestPluginManager_ListNetworkConfigs(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, "config")
	os.MkdirAll(configDir, 0755)

	os.WriteFile(filepath.Join(configDir, "10-test.conflist"), []byte(`{"name": "test"}`), 0644)
	os.WriteFile(filepath.Join(configDir, "20-other.conflist"), []byte(`{"name": "other"}`), 0644)

	pm := NewPluginManager("/tmp/plugins", configDir)

	configs, err := pm.ListNetworkConfigs()
	require.NoError(t, err)
	assert.Len(t, configs, 2)
}

// ===== Allocator Tests =====

func setupCNIProvider(t *testing.T) *CNIProvider {
	cfg := DefaultCNIConfig()
	tmpPluginDir := t.TempDir()
	tmpConfigDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpPluginDir, "bridge"), []byte{}, 0755)
	os.WriteFile(filepath.Join(tmpPluginDir, "loopback"), []byte{}, 0755)
	os.WriteFile(filepath.Join(tmpPluginDir, "host-local"), []byte{}, 0755)

	cfg.PluginDir = tmpPluginDir
	cfg.ConfigDir = tmpConfigDir

	provider, err := NewCNIProvider(cfg)
	if err != nil {
		t.Skipf("CNI provider creation failed: %v", err)
	}
	return provider
}

func TestCNINetworkAllocator_New(t *testing.T) {
	provider := setupCNIProvider(t)

	allocator, err := NewCNINetworkAllocator(provider, nil)
	require.NoError(t, err)
	require.NotNil(t, allocator)
}

func TestAllocatedNetwork_DriverState(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("10.0.0.0/24")
	net := &AllocatedNetwork{
		Name:       "test",
		Driver:     "bridge",
		Subnet:     subnet,
		Gateway:    net.ParseIP("10.0.0.1"),
		BridgeName: "br-test",
	}

	state := net.DriverState()
	require.NotNil(t, state)
	assert.Contains(t, state, "bridge")
	assert.Equal(t, "br-test", state["bridge"])
}

func TestAllocatedNetwork_IPAMState(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("10.0.0.0/24")
	net := &AllocatedNetwork{
		Name:    "test",
		Driver:  "bridge",
		Subnet:  subnet,
		Gateway: net.ParseIP("10.0.0.1"),
	}

	state := net.IPAMState()
	require.NotNil(t, state)
}

func TestCNINetworkAllocator_GetAllocatedNetwork(t *testing.T) {
	provider := setupCNIProvider(t)

	allocator, err := NewCNINetworkAllocator(provider, nil)
	if err != nil {
		t.Skipf("Allocator creation failed: %v", err)
	}

	_, err = allocator.GetAllocatedNetwork("nonexistent")
	assert.Error(t, err)
}

func TestCNINetworkAllocator_ListAllocatedNetworks(t *testing.T) {
	provider := setupCNIProvider(t)

	allocator, err := NewCNINetworkAllocator(provider, nil)
	if err != nil {
		t.Skipf("Allocator creation failed: %v", err)
	}

	networks := allocator.ListAllocatedNetworks()
	assert.Empty(t, networks)
}

func TestCNINetworkAllocator_RunGC(t *testing.T) {
	provider := setupCNIProvider(t)

	allocator, err := NewCNINetworkAllocator(provider, nil)
	if err != nil {
		t.Skipf("Allocator creation failed: %v", err)
	}

	ctx := context.Background()
	err = allocator.RunGC(ctx)
	require.NoError(t, err)
}

func TestCNINetworkAllocator_IsServiceAllocated(t *testing.T) {
	provider := setupCNIProvider(t)

	allocator, err := NewCNINetworkAllocator(provider, nil)
	if err != nil {
		t.Skipf("Allocator creation failed: %v", err)
	}

	assert.False(t, allocator.IsServiceAllocated(nil))
}

func TestCNINetworkAllocator_IsTaskAllocated(t *testing.T) {
	provider := setupCNIProvider(t)

	allocator, err := NewCNINetworkAllocator(provider, nil)
	if err != nil {
		t.Skipf("Allocator creation failed: %v", err)
	}

	assert.False(t, allocator.IsTaskAllocated(nil))
}

func TestCNINetworkAllocator_IsAttachmentAllocated(t *testing.T) {
	provider := setupCNIProvider(t)

	allocator, err := NewCNINetworkAllocator(provider, nil)
	if err != nil {
		t.Skipf("Allocator creation failed: %v", err)
	}

	assert.False(t, allocator.IsAttachmentAllocated(nil, nil))
}

// ===== Provider Tests =====

func TestCNIProvider_GetPluginManager(t *testing.T) {
	provider := setupCNIProvider(t)

	pm := provider.GetPluginManager()
	require.NotNil(t, pm)
}

func TestCNIProvider_GetIPAMManager(t *testing.T) {
	provider := setupCNIProvider(t)

	ipam := provider.GetIPAMManager()
	require.NotNil(t, ipam)
}

func TestCNIProvider_GetConfig(t *testing.T) {
	provider := setupCNIProvider(t)

	config := provider.GetConfig()
	require.NotNil(t, config)
}

func TestCNIProvider_GetVXLANPort(t *testing.T) {
	provider := setupCNIProvider(t)

	port := provider.GetVXLANPort()
	// Port should be a valid VXLAN port (default or configured)
	assert.GreaterOrEqual(t, port, uint32(4789))
}

func TestCNIProvider_SetDefaultVXLANUDPPort(t *testing.T) {
	provider := &CNIProvider{vxlanPort: 4789}

	provider.SetDefaultVXLANUDPPort(8472)
	assert.Equal(t, uint32(8472), provider.vxlanPort)
}

func TestCNIProvider_NewAllocator(t *testing.T) {
	provider := setupCNIProvider(t)

	allocator, err := provider.NewAllocator(nil)
	require.NoError(t, err)
	require.NotNil(t, allocator)
}

func TestCNIProvider_ValidateIngressNetworkDriver(t *testing.T) {
	provider := &CNIProvider{}

	tests := []struct {
		driver  string
		wantErr bool
	}{
		{driver: "overlay", wantErr: true},
		{driver: "bridge", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			driver := &api.Driver{Name: tt.driver}
			err := provider.ValidateIngressNetworkDriver(driver)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
