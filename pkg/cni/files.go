package cni

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/moby/swarmkit/v2/api"
)

// WriteConfigFile writes a CNI configuration file to disk
func WriteConfigFile(configDir, name string, config []byte) error {
	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	filename := filepath.Join(configDir, name+".conf")
	return os.WriteFile(filename, config, 0644)
}

// WriteConfigListFile writes a CNI configuration list to disk
func WriteConfigListFile(configDir, name string, configs []map[string]interface{}) error {
	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	list := map[string]interface{}{
		"cniVersion": DefaultCNIVersion,
		"name":       name,
		"plugins":    configs,
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config list: %w", err)
	}

	filename := filepath.Join(configDir, name+".conflist")
	return os.WriteFile(filename, data, 0644)
}

// RemoveConfigFile removes a CNI configuration file
func RemoveConfigFile(configDir, name string) error {
	// Try .conf file first
	confPath := filepath.Join(configDir, name+".conf")
	if _, err := os.Stat(confPath); err == nil {
		if err := os.Remove(confPath); err != nil {
			return fmt.Errorf("failed to remove conf file: %w", err)
		}
		return nil
	}

	// Try .conflist file
	conflistPath := filepath.Join(configDir, name+".conflist")
	if _, err := os.Stat(conflistPath); err == nil {
		if err := os.Remove(conflistPath); err != nil {
			return fmt.Errorf("failed to remove conflist file: %w", err)
		}
		return nil
	}

	// Try numbered config files
	files, err := os.ReadDir(configDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read config directory: %w", err)
	}

	for _, file := range files {
		filename := file.Name()
		// Check if file contains network name
		if containsNetworkName(filename, name) {
			if err := os.Remove(filepath.Join(configDir, filename)); err != nil {
				return fmt.Errorf("failed to remove %s: %w", filename, err)
			}
		}
	}

	return nil
}

// containsNetworkName checks if a filename contains a network name
func containsNetworkName(filename, networkName string) bool {
	// Remove extension
	base := filename
	if idx := len(filename) - 5; idx > 0 && filename[idx:] == ".conf" {
		base = filename[:idx]
	} else if idx := len(filename) - 10; idx > 0 && filename[idx:] == ".conflist" {
		base = filename[:idx]
	}

	// Remove numeric prefix (e.g., "01-")
	if idx := findPrefixEnd(base); idx > 0 && idx < 4 {
		base = base[idx+1:]
	}

	return base == networkName
}

// findPrefixEnd finds the end of a numeric prefix
func findPrefixEnd(s string) int {
	for i, c := range s {
		if c < '0' || c > '9' {
			return i
		}
	}
	return 0
}

// EnsurePluginDir ensures the CNI plugin directory exists and has required plugins
func EnsurePluginDir(pluginDir string) error {
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		return fmt.Errorf("failed to create plugin directory: %w", err)
	}

	return nil
}

// EnsureConfigDir ensures the CNI config directory exists
func EnsureConfigDir(configDir string) error {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	return nil
}

// InitializeCNI initializes CNI directories and default configurations
func InitializeCNI(ctx context.Context, config *CNIConfig) error {
	// Ensure directories exist
	if err := EnsurePluginDir(config.PluginDir); err != nil {
		return err
	}

	if err := EnsureConfigDir(config.ConfigDir); err != nil {
		return err
	}

	// Create loopback config (required by CNI spec)
	loopbackConfig, err := NewConfigGenerator().GenerateLoopbackConfig()
	if err != nil {
		return fmt.Errorf("failed to generate loopback config: %w", err)
	}

	if err := WriteConfigFile(config.ConfigDir, "lo", loopbackConfig); err != nil {
		return fmt.Errorf("failed to write loopback config: %w", err)
	}

	return nil
}

// CreateNetworkFromSpec creates a CNI network from SwarmKit network spec
func CreateNetworkFromSpec(ctx context.Context, provider *CNIProvider, spec *api.NetworkSpec) (*CNINetworkConfig, error) {
	// Get driver
	driver := "bridge"
	if spec.DriverConfig != nil && spec.DriverConfig.Name != "" {
		driver = spec.DriverConfig.Name
	}

	// Validate driver
	if err := provider.ValidateNetworkDriver(spec.DriverConfig); err != nil {
		return nil, err
	}

	// Get name
	name := spec.Annotations.Name
	if name == "" {
		return nil, fmt.Errorf("network name required")
	}

	// Generate subnet if not specified
	_ = "" // subnet placeholder for future use
	if spec.IPAM != nil && len(spec.IPAM.Configs) > 0 {
		_ = spec.IPAM.Configs[0].Subnet // subnet extracted for future use
	}

	// Use provider's allocation logic
	_, err := provider.AllocateNetwork(name, driver)
	if err != nil {
		return nil, err
	}

	// Load the generated config
	return provider.pluginMgr.loadNetworkConfig(name)
}

// GetNetworkAttachmentInfo returns CNI attachment information for a task
func GetNetworkAttachmentInfo(ctx context.Context, pluginMgr *PluginManager, networkName, containerID string) (*CNIExecResult, error) {
	// Execute CNI ADD to get attachment details
	return pluginMgr.Add(ctx, networkName, containerID, "eth0", nil)
}

// CleanupNetworkAttachment removes a network attachment for a task
func CleanupNetworkAttachment(ctx context.Context, pluginMgr *PluginManager, networkName, containerID string) error {
	return pluginMgr.Del(ctx, networkName, containerID, "eth0", nil)
}