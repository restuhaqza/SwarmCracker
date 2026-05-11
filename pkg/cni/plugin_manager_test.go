package cni

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ===== Mock CommandExecutor =====

// MockCommandExecutor is a mock implementation of CommandExecutor for testing
type MockCommandExecutor struct {
	ExecuteFunc func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error)
}

func (m *MockCommandExecutor) Execute(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
	if m.ExecuteFunc != nil {
		return m.ExecuteFunc(ctx, name, stdin, env)
	}
	return nil, nil, nil
}

// ===== PluginManager Tests =====

func TestPluginManager_Add_Success(t *testing.T) {
	// Setup mock executor that returns valid CNI result
	mockExecutor := &MockCommandExecutor{
		ExecuteFunc: func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
			// Verify command is ADD
			for _, e := range env {
				if e == "CNI_COMMAND=ADD" {
					// Return valid CNI result
					result := CNIExecResult{
						Interfaces: []CNIInterface{
							{Name: "eth0", MAC: "00:11:22:33:44:55", Sandbox: "/var/run/netns/test"},
						},
						IPs: []CNIIPConfig{
							{Interface: 0, Address: "10.0.1.2/24", Gateway: "10.0.1.1"},
						},
						Routes: []CNIRoute{
							{Destination: "0.0.0.0/0", Gateway: "10.0.1.1"},
						},
					}
					data, _ := json.Marshal(result)
					return data, nil, nil
				}
			}
			return nil, nil, errors.New("CNI_COMMAND not found in env")
		},
	}

	// Create temp directories
	tmpPluginDir := t.TempDir()
	tmpConfigDir := t.TempDir()

	// Create mock bridge plugin
	pluginPath := filepath.Join(tmpPluginDir, "bridge")
	os.WriteFile(pluginPath, []byte("#!/bin/sh\necho '{}'"), 0755)

	// Create network config file
	config := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       "test-net",
		"type":       "bridge",
		"bridge":     "br-test",
	}
	configData, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(tmpConfigDir, "test-net.conf"), configData, 0644)

	pm := NewPluginManagerWithExecutor(tmpPluginDir, tmpConfigDir, mockExecutor)

	ctx := context.Background()
	result, err := pm.Add(ctx, "test-net", "container-123", "eth0", map[string]string{"K8S_POD_NAME": "test-pod"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Len(t, result.Interfaces, 1)
	assert.Equal(t, "eth0", result.Interfaces[0].Name)
	assert.Len(t, result.IPs, 1)
	assert.Equal(t, "10.0.1.2/24", result.IPs[0].Address)
}

func TestPluginManager_Add_NetworkNotFound(t *testing.T) {
	mockExecutor := &MockCommandExecutor{}

	tmpPluginDir := t.TempDir()
	tmpConfigDir := t.TempDir()

	pm := NewPluginManagerWithExecutor(tmpPluginDir, tmpConfigDir, mockExecutor)

	ctx := context.Background()
	_, err := pm.Add(ctx, "nonexistent", "container-123", "eth0", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load network config")
}

func TestPluginManager_Add_PluginNotFound(t *testing.T) {
	mockExecutor := &MockCommandExecutor{}

	tmpPluginDir := t.TempDir()
	tmpConfigDir := t.TempDir()

	// Create config but no plugin
	config := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       "test-net",
		"type":       "bridge",
	}
	configData, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(tmpConfigDir, "test-net.conf"), configData, 0644)

	pm := NewPluginManagerWithExecutor(tmpPluginDir, tmpConfigDir, mockExecutor)

	ctx := context.Background()
	_, err := pm.Add(ctx, "test-net", "container-123", "eth0", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugin bridge not found")
}

func TestPluginManager_Add_ExecutionError(t *testing.T) {
	mockExecutor := &MockCommandExecutor{
		ExecuteFunc: func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
			return nil, []byte("execution failed"), errors.New("exit status 1")
		},
	}

	tmpPluginDir := t.TempDir()
	tmpConfigDir := t.TempDir()

	// Create plugin and config
	pluginPath := filepath.Join(tmpPluginDir, "bridge")
	os.WriteFile(pluginPath, []byte{}, 0755)

	config := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       "test-net",
		"type":       "bridge",
	}
	configData, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(tmpConfigDir, "test-net.conf"), configData, 0644)

	pm := NewPluginManagerWithExecutor(tmpPluginDir, tmpConfigDir, mockExecutor)

	ctx := context.Background()
	_, err := pm.Add(ctx, "test-net", "container-123", "eth0", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "CNI ADD failed")
}

func TestPluginManager_Del_Success(t *testing.T) {
	mockExecutor := &MockCommandExecutor{
		ExecuteFunc: func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
			// Verify command is DEL
			for _, e := range env {
				if e == "CNI_COMMAND=DEL" {
					return nil, nil, nil // DEL returns no output
				}
			}
			return nil, nil, errors.New("CNI_COMMAND not found")
		},
	}

	tmpPluginDir := t.TempDir()
	tmpConfigDir := t.TempDir()

	pluginPath := filepath.Join(tmpPluginDir, "bridge")
	os.WriteFile(pluginPath, []byte{}, 0755)

	config := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       "test-net",
		"type":       "bridge",
	}
	configData, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(tmpConfigDir, "test-net.conf"), configData, 0644)

	pm := NewPluginManagerWithExecutor(tmpPluginDir, tmpConfigDir, mockExecutor)

	ctx := context.Background()
	err := pm.Del(ctx, "test-net", "container-123", "eth0", nil)

	require.NoError(t, err)
}

func TestPluginManager_Del_NetworkNotFound(t *testing.T) {
	mockExecutor := &MockCommandExecutor{}

	tmpPluginDir := t.TempDir()
	tmpConfigDir := t.TempDir()

	pm := NewPluginManagerWithExecutor(tmpPluginDir, tmpConfigDir, mockExecutor)

	ctx := context.Background()
	err := pm.Del(ctx, "nonexistent", "container-123", "eth0", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load network config")
}

func TestPluginManager_Check_Success(t *testing.T) {
	mockExecutor := &MockCommandExecutor{
		ExecuteFunc: func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
			// Verify command is CHECK
			for _, e := range env {
				if e == "CNI_COMMAND=CHECK" {
					return nil, nil, nil // CHECK returns no output on success
				}
			}
			return nil, nil, errors.New("CNI_COMMAND not found")
		},
	}

	tmpPluginDir := t.TempDir()
	tmpConfigDir := t.TempDir()

	pluginPath := filepath.Join(tmpPluginDir, "bridge")
	os.WriteFile(pluginPath, []byte{}, 0755)

	config := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       "test-net",
		"type":       "bridge",
	}
	configData, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(tmpConfigDir, "test-net.conf"), configData, 0644)

	pm := NewPluginManagerWithExecutor(tmpPluginDir, tmpConfigDir, mockExecutor)

	ctx := context.Background()
	err := pm.Check(ctx, "test-net", "container-123", "eth0", nil)

	require.NoError(t, err)
}

func TestPluginManager_Check_NetworkNotFound(t *testing.T) {
	mockExecutor := &MockCommandExecutor{}

	tmpPluginDir := t.TempDir()
	tmpConfigDir := t.TempDir()

	pm := NewPluginManagerWithExecutor(tmpPluginDir, tmpConfigDir, mockExecutor)

	ctx := context.Background()
	err := pm.Check(ctx, "nonexistent", "container-123", "eth0", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load network config")
}

// ===== loadNetworkConfig Tests =====

func TestPluginManager_LoadNetworkConfig_ConfFile(t *testing.T) {
	tmpConfigDir := t.TempDir()

	config := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       "test-network",
		"type":       "bridge",
		"bridge":     "br-test",
	}
	configData, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(tmpConfigDir, "test-network.conf"), configData, 0644)

	pm := NewPluginManager("/tmp/plugins", tmpConfigDir)

	cniConfig, err := pm.loadNetworkConfig("test-network")

	require.NoError(t, err)
	require.NotNil(t, cniConfig)
	assert.Equal(t, "1.0.0", cniConfig.CNIVersion)
	assert.Equal(t, "test-network", cniConfig.Name)
	assert.Equal(t, "bridge", cniConfig.Type)
}

func TestPluginManager_LoadNetworkConfig_ConflistFile(t *testing.T) {
	tmpConfigDir := t.TempDir()

	conflist := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       "test-network",
		"plugins": []interface{}{
			map[string]interface{}{
				"type":   "bridge",
				"bridge": "br-test",
			},
		},
	}
	configData, _ := json.Marshal(conflist)
	os.WriteFile(filepath.Join(tmpConfigDir, "test-network.conflist"), configData, 0644)

	pm := NewPluginManager("/tmp/plugins", tmpConfigDir)

	cniConfig, err := pm.loadNetworkConfig("test-network")

	require.NoError(t, err)
	require.NotNil(t, cniConfig)
	assert.Equal(t, "bridge", cniConfig.Type)
}

func TestPluginManager_LoadNetworkConfig_NotFound(t *testing.T) {
	tmpConfigDir := t.TempDir()

	pm := NewPluginManager("/tmp/plugins", tmpConfigDir)

	_, err := pm.loadNetworkConfig("nonexistent")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPluginManager_LoadNetworkConfig_EmptyConflist(t *testing.T) {
	tmpConfigDir := t.TempDir()

	conflist := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       "empty-network",
		"plugins":    []interface{}{},
	}
	configData, _ := json.Marshal(conflist)
	os.WriteFile(filepath.Join(tmpConfigDir, "empty-network.conflist"), configData, 0644)

	pm := NewPluginManager("/tmp/plugins", tmpConfigDir)

	_, err := pm.loadNetworkConfig("empty-network")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no plugins")
}

func TestPluginManager_LoadNetworkConfig_NumberedFile(t *testing.T) {
	tmpConfigDir := t.TempDir()

	config := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       "numbered-net",
		"type":       "bridge",
	}
	configData, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(tmpConfigDir, "10-numbered-net.conf"), configData, 0644)

	pm := NewPluginManager("/tmp/plugins", tmpConfigDir)

	cniConfig, err := pm.loadNetworkConfig("numbered-net")

	require.NoError(t, err)
	require.NotNil(t, cniConfig)
	assert.Equal(t, "numbered-net", cniConfig.Name)
}

func TestPluginManager_LoadNetworkConfig_InvalidJSON(t *testing.T) {
	tmpConfigDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpConfigDir, "invalid.conf"), []byte("not valid json"), 0644)

	pm := NewPluginManager("/tmp/plugins", tmpConfigDir)

	_, err := pm.loadNetworkConfig("invalid")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

// ===== parsePluginConfig Tests =====

func TestPluginManager_ParsePluginConfig(t *testing.T) {
	pm := &PluginManager{}

	tests := []struct {
		name     string
		config   map[string]interface{}
		expected *CNINetworkConfig
	}{
		{
			name: "full config",
			config: map[string]interface{}{
				"cniVersion": "1.0.0",
				"name":       "test-net",
				"type":       "bridge",
				"bridge":     "br-test",
			},
			expected: &CNINetworkConfig{
				CNIVersion: "1.0.0",
				Name:       "test-net",
				Type:       "bridge",
			},
		},
		{
			name: "minimal config",
			config: map[string]interface{}{
				"type": "vxlan",
			},
			expected: &CNINetworkConfig{
				CNIVersion: DefaultCNIVersion,
				Type:       "vxlan",
			},
		},
		{
			name:   "empty config",
			config: map[string]interface{}{},
			expected: &CNINetworkConfig{
				CNIVersion: DefaultCNIVersion,
			},
		},
		{
			name: "config with extra fields",
			config: map[string]interface{}{
				"cniVersion": "0.4.0",
				"name":       "extra-net",
				"type":       "bridge",
				"ipam": map[string]interface{}{
					"type":   "host-local",
					"subnet": "10.0.0.0/24",
				},
			},
			expected: &CNINetworkConfig{
				CNIVersion: "0.4.0",
				Name:       "extra-net",
				Type:       "bridge",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := pm.parsePluginConfig(tt.config)

			assert.Equal(t, tt.expected.CNIVersion, result.CNIVersion)
			assert.Equal(t, tt.expected.Name, result.Name)
			assert.Equal(t, tt.expected.Type, result.Type)
			assert.NotNil(t, result.RawConfig)
		})
	}
}

// ===== buildCNIArgs Tests =====

func TestPluginManager_BuildCNIArgs(t *testing.T) {
	pm := &PluginManager{pluginDir: "/opt/cni/bin"}

	tests := []struct {
		name        string
		containerID string
		ifName      string
		args        map[string]string
		checkFunc   func(args []string)
	}{
		{
			name:        "basic args",
			containerID: "container-123",
			ifName:      "eth0",
			args:        nil,
			checkFunc: func(args []string) {
				assertContains(t, args, "CNI_CONTAINERID=container-123")
				assertContains(t, args, "CNI_IFNAME=eth0")
				assertContains(t, args, "CNI_PATH=/opt/cni/bin")
			},
		},
		{
			name:        "with extra args",
			containerID: "container-456",
			ifName:      "eth1",
			args: map[string]string{
				"K8S_POD_NAME":      "my-pod",
				"K8S_POD_NAMESPACE": "default",
			},
			checkFunc: func(args []string) {
				assertContains(t, args, "CNI_CONTAINERID=container-456")
				assertContains(t, args, "CNI_IFNAME=eth1")
				assertContains(t, args, "CNI_ARGS=K8S_POD_NAME=my-pod;K8S_POD_NAMESPACE=default")
			},
		},
		{
			name:        "single extra arg",
			containerID: "container-789",
			ifName:      "eth0",
			args: map[string]string{
				"IGNORE": "true",
			},
			checkFunc: func(args []string) {
				assertContains(t, args, "CNI_ARGS=IGNORE=true")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := pm.buildCNIArgs(tt.containerID, tt.ifName, tt.args)

			require.NotNil(t, args)
			assert.GreaterOrEqual(t, len(args), 4)

			// Check that CNI_COMMAND starts as ADD (will be overridden by executePlugin)
			assertContains(t, args, "CNI_COMMAND=ADD")

			if tt.checkFunc != nil {
				tt.checkFunc(args)
			}
		})
	}
}

func TestPluginManager_BuildCNIArgs_Format(t *testing.T) {
	pm := &PluginManager{pluginDir: "/opt/cni/bin"}

	args := pm.buildCNIArgs("test-container", "eth0", map[string]string{
		"KEY1": "value1",
		"KEY2": "value2",
	})

	// Verify format
	for _, arg := range args {
		assert.Contains(t, arg, "=")
		parts := splitArg(arg)
		assert.Len(t, parts, 2)
	}
}

// ===== Helper functions =====

func assertContains(t *testing.T, args []string, expected string) {
	t.Helper()
	for _, arg := range args {
		if arg == expected {
			return
		}
	}
	t.Errorf("expected args to contain %q, got %v", expected, args)
}

func splitArg(arg string) []string {
	for i := 0; i < len(arg); i++ {
		if arg[i] == '=' {
			return []string{arg[:i], arg[i+1:]}
		}
	}
	return []string{arg}
}

// ===== NewPluginManagerWithExecutor Tests =====

func TestNewPluginManagerWithExecutor_NilExecutor(t *testing.T) {
	pm := NewPluginManagerWithExecutor("/tmp/plugins", "/tmp/config", nil)

	require.NotNil(t, pm)
	assert.NotNil(t, pm.executor)
	_, ok := pm.executor.(*DefaultCommandExecutor)
	assert.True(t, ok)
}

func TestNewPluginManagerWithExecutor_CustomExecutor(t *testing.T) {
	mockExecutor := &MockCommandExecutor{}
	pm := NewPluginManagerWithExecutor("/tmp/plugins", "/tmp/config", mockExecutor)

	require.NotNil(t, pm)
	assert.Equal(t, mockExecutor, pm.executor)
}

// ===== PluginExists Tests =====

func TestPluginManager_PluginExists(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "bridge"), []byte{}, 0755)
	os.WriteFile(filepath.Join(tmpDir, "vxlan"), []byte{}, 0755)

	pm := NewPluginManager(tmpDir, "/tmp/config")

	assert.True(t, pm.PluginExists("bridge"))
	assert.True(t, pm.PluginExists("vxlan"))
	assert.False(t, pm.PluginExists("nonexistent"))
}

// ===== executePlugin Tests =====

func TestPluginManager_ExecutePlugin(t *testing.T) {
	mockExecutor := &MockCommandExecutor{
		ExecuteFunc: func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
			// Verify stdin is valid JSON
			var config map[string]interface{}
			if err := json.Unmarshal(stdin, &config); err != nil {
				return nil, nil, err
			}
			return []byte(`{"interfaces": [], "ips": []}`), nil, nil
		},
	}

	pm := &PluginManager{
		pluginDir: "/opt/cni/bin",
		executor:  mockExecutor,
	}

	config := &CNINetworkConfig{
		CNIVersion: "1.0.0",
		Name:       "test",
		Type:       "bridge",
		RawConfig: map[string]interface{}{
			"cniVersion": "1.0.0",
			"name":       "test",
			"type":       "bridge",
		},
	}

	cniArgs := []string{
		"CNI_COMMAND=ADD",
		"CNI_CONTAINERID=test-container",
		"CNI_IFNAME=eth0",
		"CNI_PATH=/opt/cni/bin",
	}

	ctx := context.Background()
	result, err := pm.executePlugin(ctx, "/opt/cni/bin/bridge", "ADD", config, cniArgs)

	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestPluginManager_ExecutePlugin_DEL(t *testing.T) {
	mockExecutor := &MockCommandExecutor{
		ExecuteFunc: func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
			return nil, nil, nil
		},
	}

	pm := &PluginManager{
		pluginDir: "/opt/cni/bin",
		executor:  mockExecutor,
	}

	config := &CNINetworkConfig{
		RawConfig: map[string]interface{}{"type": "bridge"},
	}

	cniArgs := []string{
		"CNI_COMMAND=ADD",
		"CNI_CONTAINERID=test-container",
		"CNI_IFNAME=eth0",
	}

	ctx := context.Background()
	result, err := pm.executePlugin(ctx, "/opt/cni/bin/bridge", "DEL", config, cniArgs)

	require.NoError(t, err)
	assert.Nil(t, result) // DEL returns no result
}

// ===== Integration-style tests with mock filesystem =====

func TestPluginManager_FullAddWorkflow(t *testing.T) {
	// This test simulates the full ADD workflow with mocks

	mockResult := &CNIExecResult{
		Interfaces: []CNIInterface{
			{Name: "eth0", MAC: "02:42:ac:11:00:02", Sandbox: "/var/run/netns/ns-123"},
		},
		IPs: []CNIIPConfig{
			{
				Interface: 0,
				Address:   "172.17.0.2/16",
				Gateway:   "172.17.0.1",
			},
		},
		Routes: []CNIRoute{
			{Destination: "0.0.0.0/0"},
		},
		DNS: CNIDNS{
			Nameservers: []string{"8.8.8.8"},
		},
	}

	mockExecutor := &MockCommandExecutor{
		ExecuteFunc: func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
			data, _ := json.Marshal(mockResult)
			return data, nil, nil
		},
	}

	tmpPluginDir := t.TempDir()
	tmpConfigDir := t.TempDir()

	// Create bridge plugin
	os.WriteFile(filepath.Join(tmpPluginDir, "bridge"), []byte{}, 0755)

	// Create network config
	netConfig := map[string]interface{}{
		"cniVersion": "1.0.0",
		"name":       "full-test",
		"type":       "bridge",
		"bridge":     "cni0",
		"ipam": map[string]interface{}{
			"type":   "host-local",
			"subnet": "172.17.0.0/16",
		},
	}
	configData, _ := json.Marshal(netConfig)
	os.WriteFile(filepath.Join(tmpConfigDir, "full-test.conf"), configData, 0644)

	pm := NewPluginManagerWithExecutor(tmpPluginDir, tmpConfigDir, mockExecutor)

	ctx := context.Background()
	result, err := pm.Add(ctx, "full-test", "container-full-test", "eth0", map[string]string{
		"K8S_POD_NAME":      "test-pod",
		"K8S_POD_NAMESPACE": "kube-system",
	})

	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify interface
	assert.Len(t, result.Interfaces, 1)
	assert.Equal(t, "eth0", result.Interfaces[0].Name)
	assert.NotEmpty(t, result.Interfaces[0].MAC)

	// Verify IP
	assert.Len(t, result.IPs, 1)
	assert.Equal(t, "172.17.0.2/16", result.IPs[0].Address)
	assert.Equal(t, "172.17.0.1", result.IPs[0].Gateway)

	// Verify routes
	assert.Len(t, result.Routes, 1)

	// Verify DNS
	assert.Len(t, result.DNS.Nameservers, 1)
	assert.Equal(t, "8.8.8.8", result.DNS.Nameservers[0])
}

// ===== Edge case tests =====

func TestPluginManager_Add_InvalidResultJSON(t *testing.T) {
	mockExecutor := &MockCommandExecutor{
		ExecuteFunc: func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
			return []byte("invalid json"), nil, nil
		},
	}

	tmpPluginDir := t.TempDir()
	tmpConfigDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpPluginDir, "bridge"), []byte{}, 0755)

	config := map[string]interface{}{"name": "test", "type": "bridge"}
	configData, _ := json.Marshal(config)
	os.WriteFile(filepath.Join(tmpConfigDir, "test.conf"), configData, 0644)

	pm := NewPluginManagerWithExecutor(tmpPluginDir, tmpConfigDir, mockExecutor)

	ctx := context.Background()
	_, err := pm.Add(ctx, "test", "container-123", "eth0", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse CNI result")
}

func TestPluginManager_LoadNetworkConfig_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, permission test not applicable")
	}

	tmpConfigDir := t.TempDir()

	config := map[string]interface{}{"name": "test", "type": "bridge"}
	configData, _ := json.Marshal(config)
	configPath := filepath.Join(tmpConfigDir, "test.conf")
	os.WriteFile(configPath, configData, 0644)

	// Remove read permission
	os.Chmod(configPath, 0000)
	defer os.Chmod(configPath, 0644)

	pm := NewPluginManager("/tmp/plugins", tmpConfigDir)

	_, err := pm.loadNetworkConfig("test")

	// Should return error (permission denied or open error)
	require.Error(t, err)
}

// ===== Test with IP parsing =====

func TestCNIExecResult_IPParsing(t *testing.T) {
	resultJSON := `{
		"interfaces": [{"name": "eth0", "sandbox": "/var/run/netns/test"}],
		"ips": [{"address": "10.0.1.2/24", "gateway": "10.0.1.1"}],
		"routes": [{"dst": "0.0.0.0/0", "gw": "10.0.1.1"}]
	}`

	var result CNIExecResult
	err := json.Unmarshal([]byte(resultJSON), &result)

	require.NoError(t, err)
	assert.Len(t, result.Interfaces, 1)
	assert.Len(t, result.IPs, 1)
	assert.Len(t, result.Routes, 1)

	// Parse the IP address
	ip, subnet, err := net.ParseCIDR(result.IPs[0].Address)
	require.NoError(t, err)
	assert.NotNil(t, ip)
	assert.NotNil(t, subnet)
}
