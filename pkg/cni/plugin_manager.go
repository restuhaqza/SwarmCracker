package cni

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PluginManager handles CNI plugin execution
type PluginManager struct {
	pluginDir string
	configDir string
	env       []string
	executor  CommandExecutor
}

// NewPluginManager creates a new CNI plugin manager
func NewPluginManager(pluginDir, configDir string) *PluginManager {
	return &PluginManager{
		pluginDir: pluginDir,
		configDir: configDir,
		env:       []string{},
		executor:  NewDefaultCommandExecutor(),
	}
}

// NewPluginManagerWithExecutor creates a plugin manager with custom executor (for testing)
func NewPluginManagerWithExecutor(pluginDir, configDir string, executor CommandExecutor) *PluginManager {
	if executor == nil {
		executor = NewDefaultCommandExecutor()
	}
	return &PluginManager{
		pluginDir: pluginDir,
		configDir: configDir,
		env:       []string{},
		executor:  executor,
	}
}

// WithEnv adds environment variables for CNI execution
func (m *PluginManager) WithEnv(env []string) *PluginManager {
	m.env = append(m.env, env...)
	return m
}

// Add executes CNI ADD command for a network attachment
func (m *PluginManager) Add(ctx context.Context, netName, containerID, ifName string, args map[string]string) (*CNIExecResult, error) {
	// Load network configuration
	config, err := m.loadNetworkConfig(netName)
	if err != nil {
		return nil, fmt.Errorf("failed to load network config: %w", err)
	}

	// Build CNI arguments
	cniArgs := m.buildCNIArgs(containerID, ifName, args)

	// Find the plugin binary
	pluginType := config.Type
	pluginPath := filepath.Join(m.pluginDir, pluginType)

	if _, err := os.Stat(pluginPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("plugin %s not found in %s", pluginType, m.pluginDir)
	}

	// Execute CNI ADD
	resultBytes, err := m.executePlugin(ctx, pluginPath, "ADD", config, cniArgs)
	if err != nil {
		return nil, fmt.Errorf("CNI ADD failed: %w", err)
	}

	// Parse result
	result := &CNIExecResult{}
	if err := json.Unmarshal(resultBytes, result); err != nil {
		return nil, fmt.Errorf("failed to parse CNI result: %w", err)
	}

	return result, nil
}

// Del executes CNI DEL command to remove a network attachment
func (m *PluginManager) Del(ctx context.Context, netName, containerID, ifName string, args map[string]string) error {
	// Load network configuration
	config, err := m.loadNetworkConfig(netName)
	if err != nil {
		return fmt.Errorf("failed to load network config: %w", err)
	}

	// Build CNI arguments
	cniArgs := m.buildCNIArgs(containerID, ifName, args)

	// Find the plugin binary
	pluginType := config.Type
	pluginPath := filepath.Join(m.pluginDir, pluginType)

	if _, err := os.Stat(pluginPath); os.IsNotExist(err) {
		return fmt.Errorf("plugin %s not found in %s", pluginType, m.pluginDir)
	}

	// Execute CNI DEL (no result expected)
	_, err = m.executePlugin(ctx, pluginPath, "DEL", config, cniArgs)
	return err
}

// Check executes CNI CHECK command to verify an attachment
func (m *PluginManager) Check(ctx context.Context, netName, containerID, ifName string, args map[string]string) error {
	// Load network configuration
	config, err := m.loadNetworkConfig(netName)
	if err != nil {
		return fmt.Errorf("failed to load network config: %w", err)
	}

	// Build CNI arguments
	cniArgs := m.buildCNIArgs(containerID, ifName, args)

	// Find the plugin binary
	pluginType := config.Type
	pluginPath := filepath.Join(m.pluginDir, pluginType)

	if _, err := os.Stat(pluginPath); os.IsNotExist(err) {
		return fmt.Errorf("plugin %s not found in %s", pluginType, m.pluginDir)
	}

	// Execute CNI CHECK
	_, err = m.executePlugin(ctx, pluginPath, "CHECK", config, cniArgs)
	return err
}

// loadNetworkConfig loads a CNI network configuration file
func (m *PluginManager) loadNetworkConfig(netName string) (*CNINetworkConfig, error) {
	// Try .conflist first (configuration list)
	conflistPath := filepath.Join(m.configDir, netName+".conflist")
	if _, err := os.Stat(conflistPath); err == nil {
		data, err := os.ReadFile(conflistPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read conflist: %w", err)
		}

		// Parse as conflist and return first plugin
		var conflist map[string]interface{}
		if err := json.Unmarshal(data, &conflist); err != nil {
			return nil, fmt.Errorf("failed to parse conflist: %w", err)
		}

		plugins, ok := conflist["plugins"].([]interface{})
		if !ok || len(plugins) == 0 {
			return nil, fmt.Errorf("no plugins in conflist")
		}

		// Return first plugin config
		pluginConfig := plugins[0].(map[string]interface{})
		return m.parsePluginConfig(pluginConfig), nil
	}

	// Try .conf file (single configuration)
	confPath := filepath.Join(m.configDir, netName+".conf")
	if _, err := os.Stat(confPath); err == nil {
		data, err := os.ReadFile(confPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read conf: %w", err)
		}

		var pluginConfig map[string]interface{}
		if err := json.Unmarshal(data, &pluginConfig); err != nil {
			return nil, fmt.Errorf("failed to parse conf: %w", err)
		}

		return m.parsePluginConfig(pluginConfig), nil
	}

	// Try numbered config files (01-netname.conf, etc)
	files, err := os.ReadDir(m.configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read config dir: %w", err)
	}

	for _, file := range files {
		name := file.Name()
		// Check if file contains network name
		if strings.Contains(name, netName) && strings.HasSuffix(name, ".conf") || strings.HasSuffix(name, ".conflist") {
			data, err := os.ReadFile(filepath.Join(m.configDir, name))
			if err != nil {
				continue
			}

			var pluginConfig map[string]interface{}
			if err := json.Unmarshal(data, &pluginConfig); err != nil {
				continue
			}

			// Check if this is the right network
			if nameField, ok := pluginConfig["name"].(string); ok && nameField == netName {
				return m.parsePluginConfig(pluginConfig), nil
			}
		}
	}

	return nil, fmt.Errorf("network config for %s not found in %s", netName, m.configDir)
}

// parsePluginConfig parses a plugin configuration into CNINetworkConfig
func (m *PluginManager) parsePluginConfig(config map[string]interface{}) *CNINetworkConfig {
	cniConfig := &CNINetworkConfig{
		RawConfig: config,
	}

	if version, ok := config["cniVersion"].(string); ok {
		cniConfig.CNIVersion = version
	} else {
		cniConfig.CNIVersion = DefaultCNIVersion
	}

	if name, ok := config["name"].(string); ok {
		cniConfig.Name = name
	}

	if typ, ok := config["type"].(string); ok {
		cniConfig.Type = typ
	}

	return cniConfig
}

// buildCNIArgs builds the CNI_ARGS environment variable
func (m *PluginManager) buildCNIArgs(containerID, ifName string, args map[string]string) []string {
	cniArgs := []string{
		fmt.Sprintf("CNI_COMMAND=%s", "ADD"), // Will be overridden by command
		fmt.Sprintf("CNI_CONTAINERID=%s", containerID),
		fmt.Sprintf("CNI_IFNAME=%s", ifName),
		fmt.Sprintf("CNI_PATH=%s", m.pluginDir),
	}

	// Build additional args as CNI_ARGS
	if len(args) > 0 {
		argsStr := ""
		for k, v := range args {
			if argsStr != "" {
				argsStr += ";"
			}
			argsStr += fmt.Sprintf("%s=%s", k, v)
		}
		cniArgs = append(cniArgs, fmt.Sprintf("CNI_ARGS=%s", argsStr))
	}

	return cniArgs
}

// executePlugin executes a CNI plugin binary
func (m *PluginManager) executePlugin(ctx context.Context, pluginPath, command string, config *CNINetworkConfig, cniArgs []string) ([]byte, error) {
	// Serialize config to JSON
	configBytes, err := json.Marshal(config.RawConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	// Update CNI_COMMAND in args
	for i, arg := range cniArgs {
		if strings.HasPrefix(arg, "CNI_COMMAND=") {
			cniArgs[i] = fmt.Sprintf("CNI_COMMAND=%s", command)
		}
	}

	// Add environment variables
	env := append(cniArgs, m.env...)

	// Execute using executor interface
	stdout, stderr, err := m.executor.Execute(ctx, pluginPath, configBytes, env)
	if err != nil {
		return nil, fmt.Errorf("plugin execution failed: %w, stderr: %s", err, string(stderr))
	}

	// DEL command doesn't return results
	if command == "DEL" {
		return nil, nil
	}

	return stdout, nil
}

// ListAvailablePlugins returns a list of available CNI plugins
func (m *PluginManager) ListAvailablePlugins() ([]string, error) {
	files, err := os.ReadDir(m.pluginDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read plugin dir: %w", err)
	}

	plugins := []string{}
	for _, file := range files {
		if !file.IsDir() {
			plugins = append(plugins, file.Name())
		}
	}

	return plugins, nil
}

// PluginExists checks if a specific plugin exists
func (m *PluginManager) PluginExists(pluginName string) bool {
	pluginPath := filepath.Join(m.pluginDir, pluginName)
	_, err := os.Stat(pluginPath)
	return err == nil
}

// ValidatePlugins checks that required plugins are available
func (m *PluginManager) ValidatePlugins() error {
	requiredPlugins := []string{"bridge", "host-local", "loopback"}

	for _, plugin := range requiredPlugins {
		if !m.PluginExists(plugin) {
			return fmt.Errorf("required plugin %s not found in %s", plugin, m.pluginDir)
		}
	}

	return nil
}

// ListNetworkConfigs returns available network configurations
func (m *PluginManager) ListNetworkConfigs() ([]string, error) {
	files, err := os.ReadDir(m.configDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read config dir: %w", err)
	}

	networks := []string{}
	for _, file := range files {
		name := file.Name()
		// Extract network name from filename
		if strings.HasSuffix(name, ".conf") || strings.HasSuffix(name, ".conflist") {
			// Remove prefix number and extension
			base := strings.TrimSuffix(name, ".conf")
			base = strings.TrimSuffix(base, ".conflist")
			// Remove numeric prefix (e.g., "01-")
			if idx := strings.Index(base, "-"); idx > 0 && idx < 4 {
				base = base[idx+1:]
			}
			networks = append(networks, base)
		}
	}

	return networks, nil
}
