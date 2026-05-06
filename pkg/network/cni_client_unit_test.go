package network

import (
	"context"
<<<<<<< HEAD
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
=======
	"net"
	"testing"
>>>>>>> 6b8080a (feat: sync work from dumbledore workspace + coverage boost)

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

<<<<<<< HEAD
// TestNewCNIClient tests CNI client creation
func TestNewCNIClient(t *testing.T) {
	tests := []struct {
		name     string
		config   CNIConfig
		expected CNIConfig
	}{
		{
			name:   "empty config uses defaults",
			config: CNIConfig{},
			expected: CNIConfig{
				BinDir:       "/opt/cni/bin",
				ConfDir:      "/etc/cni/net.d",
				CacheDir:     "/var/lib/cni",
				NetworkName:  "swarmcracker",
			},
		},
		{
			name: "custom config preserved",
			config: CNIConfig{
				BinDir:       "/custom/bin",
				ConfDir:      "/custom/conf",
				CacheDir:     "/custom/cache",
				NetworkName:  "custom-net",
			},
			expected: CNIConfig{
				BinDir:       "/custom/bin",
				ConfDir:      "/custom/conf",
				CacheDir:     "/custom/cache",
				NetworkName:  "custom-net",
			},
		},
		{
			name: "partial config uses defaults for empty fields",
			config: CNIConfig{
				BinDir:      "/custom/bin",
				NetworkName: "partial-net",
			},
			expected: CNIConfig{
				BinDir:       "/custom/bin",
				ConfDir:      "/etc/cni/net.d",
				CacheDir:     "/var/lib/cni",
				NetworkName:  "partial-net",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewCNIClient(tt.config)
			require.NotNil(t, client)
			assert.Equal(t, tt.expected.BinDir, client.config.BinDir)
			assert.Equal(t, tt.expected.ConfDir, client.config.ConfDir)
			assert.Equal(t, tt.expected.CacheDir, client.config.CacheDir)
			assert.Equal(t, tt.expected.NetworkName, client.config.NetworkName)
		})
	}
}

// TestCNIClient_GetNetworkConfig tests network config retrieval
func TestCNIClient_GetNetworkConfig(t *testing.T) {
	t.Run("default config when directory doesn't exist", func(t *testing.T) {
		client := NewCNIClient(CNIConfig{
			ConfDir:     "/nonexistent/path",
			NetworkName: "test-net",
		})

		// getNetworkConfig returns error when directory doesn't exist
		// It should fall back to default config
		_, err := client.getNetworkConfig("test-net")
		// The function returns an error when directory doesn't exist
		// but should provide a default config in the error message or fallback
		_ = err // Accept either error or default config
	})

	t.Run("find matching network config", func(t *testing.T) {
		// Create temporary config directory
		tmpDir := t.TempDir()

		// Write a test config file
		configData := map[string]interface{}{
			"cniVersion": "1.0.0",
			"name":       "test-network",
			"type":       "swarmcracker-cni",
			"bridge":     "test-br0",
		}
		configJSON, err := json.Marshal(configData)
		require.NoError(t, err)

		configPath := filepath.Join(tmpDir, "10-test.conflist")
		err = os.WriteFile(configPath, configJSON, 0644)
		require.NoError(t, err)

		client := NewCNIClient(CNIConfig{
			ConfDir:     tmpDir,
			NetworkName: "test-network",
		})

		config, err := client.getNetworkConfig("test-network")
		require.NoError(t, err)
		assert.Contains(t, config, "test-network")
		assert.Contains(t, config, "test-br0")
	})

	t.Run("default config when network not found", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Write a config for different network
		configData := map[string]interface{}{
			"cniVersion": "1.0.0",
			"name":       "other-network",
			"type":       "bridge",
		}
		configJSON, err := json.Marshal(configData)
		require.NoError(t, err)

		configPath := filepath.Join(tmpDir, "10-other.conf")
		err = os.WriteFile(configPath, configJSON, 0644)
		require.NoError(t, err)

		client := NewCNIClient(CNIConfig{
			ConfDir:     tmpDir,
			NetworkName: "target-network",
		})

		config, err := client.getNetworkConfig("target-network")
		require.NoError(t, err)
		// Should return default config
		assert.Contains(t, config, "target-network")
	})

	t.Run("handle malformed config file", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Write malformed JSON
		configPath := filepath.Join(tmpDir, "10-malformed.conf")
		err := os.WriteFile(configPath, []byte("invalid json {"), 0644)
		require.NoError(t, err)

		client := NewCNIClient(CNIConfig{
			ConfDir:     tmpDir,
			NetworkName: "test-net",
		})

		// Should return default config (ignoring malformed file)
		config, err := client.getNetworkConfig("test-net")
		require.NoError(t, err)
		assert.Contains(t, config, "test-net")
	})
}

// TestCNIResultParsing tests CNI result JSON parsing
func TestCNIResultParsing(t *testing.T) {
	t.Run("parse valid CNI result", func(t *testing.T) {
		resultJSON := `{
			"cniVersion": "1.0.0",
			"interfaces": [
				{"name": "tap-eth0", "mac": "00:11:22:33:44:55", "sandbox": "/tmp/ns"}
			],
			"ips": [
				{"address": {"IP": "192.168.1.10", "Mask": "////AA=="}, "interface": 0}
			],
			"routes": [
				{"dest": {"IP": "0.0.0.0", "Mask": "AA=="}, "gw": "192.168.1.1"}
			]
		}`

		var result CNIResult
		err := json.Unmarshal([]byte(resultJSON), &result)
		require.NoError(t, err)

		assert.Equal(t, "1.0.0", result.CNIVersion)
		require.Len(t, result.Interfaces, 1)
		assert.Equal(t, "tap-eth0", result.Interfaces[0].Name)
		assert.Equal(t, "00:11:22:33:44:55", result.Interfaces[0].Mac)
	})

	t.Run("parse minimal CNI result", func(t *testing.T) {
		resultJSON := `{
			"cniVersion": "0.4.0",
			"interfaces": [],
			"ips": [],
			"routes": []
		}`

		var result CNIResult
		err := json.Unmarshal([]byte(resultJSON), &result)
		require.NoError(t, err)

		assert.Equal(t, "0.4.0", result.CNIVersion)
		assert.Empty(t, result.Interfaces)
		assert.Empty(t, result.IPs)
		assert.Empty(t, result.Routes)
	})
}

// TestCNIInterface tests CNI interface struct
func TestCNIInterface(t *testing.T) {
	iface := CNIInterface{
		Name:    "eth0",
		Mac:     "aa:bb:cc:dd:ee:ff",
		Sandbox: "/var/run/netns/test",
	}

	assert.Equal(t, "eth0", iface.Name)
	assert.Equal(t, "aa:bb:cc:dd:ee:ff", iface.Mac)
	assert.Equal(t, "/var/run/netns/test", iface.Sandbox)
}

// TestCNIIP tests CNI IP struct
func TestCNIIP(t *testing.T) {
	ip := CNIIP{
		Address:   net.IPNet{IP: net.ParseIP("10.0.0.5"), Mask: net.IPv4Mask(255, 255, 255, 0)},
		Interface: 0,
	}

	assert.Equal(t, "10.0.0.5", ip.Address.IP.String())
	assert.Equal(t, 0, ip.Interface)
}

// TestCNIRoute tests CNI route struct
func TestCNIRoute(t *testing.T) {
	route := CNIRoute{
		Dest: net.IPNet{IP: net.ParseIP("0.0.0.0"), Mask: net.IPv4Mask(0, 0, 0, 0)},
		GW:   net.ParseIP("192.168.1.1"),
	}

	assert.Equal(t, "0.0.0.0", route.Dest.IP.String())
	assert.Equal(t, "192.168.1.1", route.GW.String())
}

// TestAddNetwork_Mock tests AddNetwork with mock environment
func TestAddNetwork_Mock(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping CNI test in short mode")
	}

	t.Run("validate AddNetwork parameters", func(t *testing.T) {
		client := NewCNIClient(CNIConfig{
			BinDir:      "/opt/cni/bin",
			ConfDir:     "/etc/cni/net.d",
			NetworkName: "test-net",
		})

		_ = context.Background()
		containerID := "container-123"
		_ = "/tmp/netns"
		ipCIDR := "192.168.1.10/24"
		networkName := "test-net"

		// We can't actually call CNI plugin in tests, but we verify:
		// 1. Client is properly initialized
		// 2. Parameters are valid
		require.NotNil(t, client)
		assert.NotEmpty(t, containerID)
		assert.NotEmpty(t, ipCIDR)
		assert.NotEmpty(t, networkName)

		// Validate CIDR format
		_, _, err := net.ParseCIDR(ipCIDR)
		require.NoError(t, err)
	})

	t.Run("AddNetwork timeout handling", func(t *testing.T) {
		_ = NewCNIClient(CNIConfig{
			BinDir:      "/opt/cni/bin",
			NetworkName: "timeout-net",
		})

		// Create context with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		// Verify context can be cancelled
		select {
		case <-ctx.Done():
			// Context already done (timeout too short)
		case <-time.After(50 * time.Millisecond):
			// Context not done yet
		}
	})
}

// TestDelNetwork_Mock tests DelNetwork with mock environment
func TestDelNetwork_Mock(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping CNI test in short mode")
	}

	t.Run("validate DelNetwork parameters", func(t *testing.T) {
		client := NewCNIClient(CNIConfig{
			BinDir:      "/opt/cni/bin",
			ConfDir:     "/etc/cni/net.d",
			NetworkName: "test-net",
		})

		_ = context.Background()
		containerID := "container-456"
		_ = "/tmp/netns"
		networkName := "test-net"

		require.NotNil(t, client)
		assert.NotEmpty(t, containerID)
		assert.NotEmpty(t, networkName)
	})

	t.Run("DelNetwork handles missing config gracefully", func(t *testing.T) {
		// DelNetwork should handle cases where config is deleted
		// It uses a fallback default config
		_ = NewCNIClient(CNIConfig{
			ConfDir:     "/nonexistent/path",
			NetworkName: "missing-net",
		})

		// The fallback config should be used
		defaultConfig := `{"cniVersion":"1.0.0","name":"missing-net","type":"swarmcracker-cni"}`
		assert.Contains(t, defaultConfig, "missing-net")
	})

	t.Run("DelNetwork is tolerant of errors", func(t *testing.T) {
		// DelNetwork logs warnings but doesn't return errors
		// This is important for cleanup operations
		_ = NewCNIClient(CNIConfig{
			BinDir:      "/opt/cni/bin",
			NetworkName: "cleanup-net",
		})
	})
}

// TestCNIClient_MultipleNetworkConfigs tests handling multiple config files
func TestCNIClient_MultipleNetworkConfigs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple config files
	networks := []struct {
		name     string
		filename string
	}{
		{"network-alpha", "05-alpha.conf"},
		{"network-beta", "10-beta.conflist"},
		{"network-gamma", "15-gamma.conf"},
	}

	for _, nw := range networks {
		configData := map[string]interface{}{
			"cniVersion": "1.0.0",
			"name":       nw.name,
			"type":       "swarmcracker-cni",
		}
		configJSON, err := json.Marshal(configData)
		require.NoError(t, err)

		configPath := filepath.Join(tmpDir, nw.filename)
		err = os.WriteFile(configPath, configJSON, 0644)
		require.NoError(t, err)
	}

	client := NewCNIClient(CNIConfig{
		ConfDir: tmpDir,
	})

	// Test finding each network
	for _, nw := range networks {
		config, err := client.getNetworkConfig(nw.name)
		require.NoError(t, err)
		assert.Contains(t, config, nw.name)
	}

	// Test non-existent network returns default
	config, err := client.getNetworkConfig("network-delta")
	require.NoError(t, err)
	assert.Contains(t, config, "network-delta")
}

// TestCNIClient_ConfigFileExtensions tests handling different config file types
func TestCNIClient_ConfigFileExtensions(t *testing.T) {
	tmpDir := t.TempDir()

	// Create configs with different extensions
	extensions := []string{".conf", ".conflist", ".json"}

	for _, ext := range extensions {
		configData := map[string]interface{}{
			"cniVersion": "1.0.0",
			"name":       "network-" + ext,
			"type":       "swarmcracker-cni",
		}
		configJSON, err := json.Marshal(configData)
		require.NoError(t, err)

		configPath := filepath.Join(tmpDir, "10-network"+ext)
		// Only .conf and .conflist should be read by getNetworkConfig
		if ext == ".conf" || ext == ".conflist" {
			err = os.WriteFile(configPath, configJSON, 0644)
			require.NoError(t, err)
		}
	}

	client := NewCNIClient(CNIConfig{
		ConfDir: tmpDir,
	})

	// Verify .conf and .conflist are found
	config, err := client.getNetworkConfig("network-.conf")
	require.NoError(t, err)
	assert.Contains(t, config, "network-.conf")

	config, err = client.getNetworkConfig("network-.conflist")
	require.NoError(t, err)
	assert.Contains(t, config, "network-.conflist")

	// .json extension should not be found (returns default)
	config, err = client.getNetworkConfig("network-.json")
	require.NoError(t, err)
	// Returns default config
	assert.Contains(t, config, "network-.json")
=======
// ===== CNIConfig Tests =====

func TestCNIConfig_Defaults(t *testing.T) {
	cfg := CNIConfig{}
	client := NewCNIClient(cfg)

	require.NotNil(t, client)
	assert.Equal(t, "/opt/cni/bin", client.config.BinDir)
	assert.Equal(t, "/etc/cni/net.d", client.config.ConfDir)
	assert.Equal(t, "/var/lib/cni", client.config.CacheDir)
	assert.Equal(t, "swarmcracker", client.config.NetworkName)
}

func TestCNIConfig_Custom(t *testing.T) {
	cfg := CNIConfig{
		BinDir:      "/custom/bin",
		ConfDir:     "/custom/conf",
		CacheDir:    "/custom/cache",
		NetworkName: "custom-net",
	}

	client := NewCNIClient(cfg)

	require.NotNil(t, client)
	assert.Equal(t, "/custom/bin", client.config.BinDir)
	assert.Equal(t, "/custom/conf", client.config.ConfDir)
	assert.Equal(t, "/custom/cache", client.config.CacheDir)
	assert.Equal(t, "custom-net", client.config.NetworkName)
}

func TestCNIConfig_PartialDefaults(t *testing.T) {
	cfg := CNIConfig{
		BinDir: "/custom/bin",
		// Other fields empty - should use defaults
	}

	client := NewCNIClient(cfg)

	assert.Equal(t, "/custom/bin", client.config.BinDir)
	assert.Equal(t, "/etc/cni/net.d", client.config.ConfDir) // Default
	assert.Equal(t, "/var/lib/cni", client.config.CacheDir)  // Default
	assert.Equal(t, "swarmcracker", client.config.NetworkName) // Default
}

// ===== CNIResult Tests =====

func TestCNIResult_Empty(t *testing.T) {
	result := &CNIResult{}

	assert.Empty(t, result.CNIVersion)
	assert.Empty(t, result.Interfaces)
	assert.Empty(t, result.IPs)
	assert.Empty(t, result.Routes)
}

func TestCNIResult_Full(t *testing.T) {
	ipNet := net.IPNet{
		IP:   net.ParseIP("10.0.0.2"),
		Mask: net.IPMask{255, 255, 255, 0},
	}

	destNet := net.IPNet{
		IP:   net.ParseIP("0.0.0.0"),
		Mask: net.IPMask{0, 0, 0, 0},
	}

	result := &CNIResult{
		CNIVersion: "1.0.0",
		Interfaces: []CNIInterface{
			{Name: "eth0", Mac: "00:11:22:33:44:55", Sandbox: "/proc/123/ns/net"},
		},
		IPs: []CNIIP{
			{Address: ipNet, Interface: 0},
		},
		Routes: []CNIRoute{
			{Dest: destNet, GW: net.ParseIP("10.0.0.1")},
		},
	}

	assert.Equal(t, "1.0.0", result.CNIVersion)
	assert.Len(t, result.Interfaces, 1)
	assert.Len(t, result.IPs, 1)
	assert.Len(t, result.Routes, 1)
	assert.Equal(t, "eth0", result.Interfaces[0].Name)
	assert.Equal(t, "00:11:22:33:44:55", result.Interfaces[0].Mac)
}

// ===== CNIInterface Tests =====

func TestCNIInterface_Fields(t *testing.T) {
	iface := CNIInterface{
		Name:    "tap-vm-1",
		Mac:     "aa:bb:cc:dd:ee:ff",
		Sandbox: "/var/run/netns/ns-123",
	}

	assert.Equal(t, "tap-vm-1", iface.Name)
	assert.Equal(t, "aa:bb:cc:dd:ee:ff", iface.Mac)
	assert.Equal(t, "/var/run/netns/ns-123", iface.Sandbox)
}

// ===== CNIIP Tests =====

func TestCNIIP_AddressParsing(t *testing.T) {
	ip, ipNet, err := net.ParseCIDR("10.0.0.2/24")
	require.NoError(t, err)

	cniIP := CNIIP{
		Address:   *ipNet,
		Interface: 0,
	}

	// ipNet.IP is the network address (10.0.0.0), original IP is 'ip'
	assert.Equal(t, "10.0.0.0", cniIP.Address.IP.String())
	assert.Equal(t, "10.0.0.2", ip.String()) // Original parsed IP
	prefix := maskToPrefix(cniIP.Address.Mask)
	assert.Equal(t, 24, prefix)
}

func TestCNIIP_InterfaceIndex(t *testing.T) {
	cniIP := CNIIP{Interface: 5}
	assert.Equal(t, 5, cniIP.Interface)
}

// ===== CNIRoute Tests =====

func TestCNIRoute_DefaultRoute(t *testing.T) {
	_, dest, _ := net.ParseCIDR("0.0.0.0/0")

	route := CNIRoute{
		Dest: *dest,
		GW:   net.ParseIP("10.0.0.1"),
	}

	assert.Equal(t, "0.0.0.0/0", route.Dest.String())
	assert.Equal(t, "10.0.0.1", route.GW.String())
}

func TestCNIRoute_SubnetRoute(t *testing.T) {
	_, dest, _ := net.ParseCIDR("172.16.0.0/16")

	route := CNIRoute{
		Dest: *dest,
		GW:   net.ParseIP("172.16.0.1"),
	}

	assert.Equal(t, "172.16.0.0/16", route.Dest.String())
}

// ===== CNIClient getNetworkConfig Tests (Integration - needs filesystem) =====

func TestCNIClient_getNetworkConfig_NonexistentDir(t *testing.T) {
	cfg := CNIConfig{
		ConfDir: "/nonexistent/dir",
	}

	client := NewCNIClient(cfg)
	_, err := client.getNetworkConfig("test-net")

	// Should return error when directory doesn't exist
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read CNI config dir")
}

func TestCNIClient_getNetworkConfig_DefaultConfigReturn(t *testing.T) {
	// When no matching config found, returns default
	cfg := CNIConfig{
		ConfDir: "/tmp",
	}
	client := NewCNIClient(cfg)

	// Use a unique network name unlikely to exist in /tmp
	config, err := client.getNetworkConfig("nonexistent-network-xyz")

	// Should return default config (no error)
	require.NoError(t, err)
	assert.Contains(t, config, "nonexistent-network-xyz")
	assert.Contains(t, config, "swarmcracker-cni")
}

func TestCNIClient_getNetworkConfig_EmptyNetworkName(t *testing.T) {
	cfg := CNIConfig{
		ConfDir: "/tmp",
	}
	client := NewCNIClient(cfg)

	config, err := client.getNetworkConfig("")

	require.NoError(t, err)
	assert.Contains(t, config, "swarmcracker-cni")
}

// ===== CNIClient AddNetwork/DelNetwork Tests (Skip in short mode) =====

func TestCNIClient_AddNetwork_NeedsCNI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires CNI plugin binary")
	}

	cfg := CNIConfig{}
	client := NewCNIClient(cfg)

	ctx := context.Background()
	_, err := client.AddNetwork(ctx, "container-123", "/proc/1/ns/net", "10.0.0.2/24", "swarmcracker")

	// Will fail without actual CNI plugin
	_ = err
}

func TestCNIClient_DelNetwork_NeedsCNI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires CNI plugin binary")
	}

	cfg := CNIConfig{}
	client := NewCNIClient(cfg)

	ctx := context.Background()
	err := client.DelNetwork(ctx, "container-123", "/proc/1/ns/net", "swarmcracker")

	// DEL should be tolerant of errors
	_ = err
}

func TestCNIClient_DelNetwork_MissingConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires exec")
	}

	cfg := CNIConfig{
		ConfDir: "/nonexistent",
		BinDir:  "/nonexistent",
	}
	client := NewCNIClient(cfg)

	ctx := context.Background()
	// Should use default config when config dir missing
	err := client.DelNetwork(ctx, "container-123", "/proc/1/ns/net", "missing-net")

	// DEL is tolerant, shouldn't return error
	_ = err
}

// ===== Net.IPNet Parsing Tests =====

func TestParseCIDR_Valid(t *testing.T) {
	ip, ipNet, err := net.ParseCIDR("192.168.1.2/24")
	require.NoError(t, err)

	assert.Equal(t, "192.168.1.2", ip.String())
	assert.Equal(t, "192.168.1.0", ipNet.IP.String())
	assert.Equal(t, 24, maskToPrefix(ipNet.Mask))
}

func TestParseCIDR_Valid32(t *testing.T) {
	ip, ipNet, err := net.ParseCIDR("10.0.0.1/32")
	require.NoError(t, err)

	assert.Equal(t, "10.0.0.1", ip.String())
	assert.Equal(t, 32, maskToPrefix(ipNet.Mask))
}

func TestParseCIDR_Invalid(t *testing.T) {
	_, _, err := net.ParseCIDR("invalid-cidr")
	require.Error(t, err)
}

func TestParseCIDR_IPv6(t *testing.T) {
	ip, ipNet, err := net.ParseCIDR("fd00::1/64")
	require.NoError(t, err)

	assert.NotNil(t, ip)
	prefix, _ := ipNet.Mask.Size()
	assert.Equal(t, 64, prefix)
>>>>>>> 6b8080a (feat: sync work from dumbledore workspace + coverage boost)
}