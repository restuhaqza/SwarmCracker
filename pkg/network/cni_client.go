// Package network provides CNI integration for SwarmCracker
package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
)

// CNIConfig holds CNI plugin configuration
type CNIConfig struct {
	BinDir       string // Path to CNI binaries (default: /opt/cni/bin)
	ConfDir      string // Path to CNI configs (default: /etc/cni/net.d)
	CacheDir     string // Path to CNI cache (default: /var/lib/cni)
	NetworkName  string // Default network name (default: swarmcracker)
}

// CNIClient handles CNI plugin calls for network setup
type CNIClient struct {
	config CNIConfig
}

// NewCNIClient creates a new CNI client
func NewCNIClient(config CNIConfig) *CNIClient {
	// Set defaults
	if config.BinDir == "" {
		config.BinDir = "/opt/cni/bin"
	}
	if config.ConfDir == "" {
		config.ConfDir = "/etc/cni/net.d"
	}
	if config.CacheDir == "" {
		config.CacheDir = "/var/lib/cni"
	}
	if config.NetworkName == "" {
		config.NetworkName = "swarmcracker"
	}

	return &CNIClient{config: config}
}

// CNIResult contains the result from CNI plugin ADD call
type CNIResult struct {
	CNIVersion string
	Interfaces []CNIInterface
	IPs        []CNIIP
	Routes     []CNIRoute
}

// CNIInterface represents an interface created by CNI
type CNIInterface struct {
	Name    string
	Mac     string
	Sandbox string
}

// CNIIP represents an IP allocation
type CNIIP struct {
	Address   net.IPNet
	Interface int
}

// CNIRoute represents a route
type CNIRoute struct {
	Dest net.IPNet
	GW   net.IP
}

// AddNetwork calls CNI ADD to create network interface
func (c *CNIClient) AddNetwork(ctx context.Context, containerID, netns string, ipCIDR string, networkName string) (*CNIResult, error) {
	log.Info().
		Str("containerID", containerID).
		Str("ip", ipCIDR).
		Str("network", networkName).
		Msg("CNI ADD called")

	// Set environment variables
	env := []string{
		fmt.Sprintf("CNI_COMMAND=%s", "ADD"),
		fmt.Sprintf("CNI_CONTAINERID=%s", containerID),
		fmt.Sprintf("CNI_NETNS=%s", netns),
		fmt.Sprintf("CNI_IFNAME=%s", "eth0"),
		fmt.Sprintf("CNI_PATH=%s", c.config.BinDir),
		fmt.Sprintf("CNI_ARGS=%s", fmt.Sprintf("IP=%s", ipCIDR)),
	}

	// Get network config
	netConfig, err := c.getNetworkConfig(networkName)
	if err != nil {
		return nil, fmt.Errorf("failed to get network config: %w", err)
	}

	// Call CNI plugin
	cmd := exec.CommandContext(ctx, filepath.Join(c.config.BinDir, "swarmcracker-cni"))
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(netConfig)

	output, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			log.Error().Str("stderr", string(ee.Stderr)).Msg("CNI ADD failed")
			return nil, fmt.Errorf("CNI ADD failed: %w (stderr: %s)", err, string(ee.Stderr))
		}
		return nil, fmt.Errorf("CNI ADD failed: %w", err)
	}

	// Parse result
	result := &CNIResult{}
	if err := json.Unmarshal(output, result); err != nil {
		return nil, fmt.Errorf("failed to parse CNI result: %w", err)
	}

	log.Info().
		Str("tap", result.Interfaces[0].Name).
		Str("mac", result.Interfaces[0].Mac).
		Msg("CNI ADD succeeded")

	return result, nil
}

// DelNetwork calls CNI DEL to remove network interface
func (c *CNIClient) DelNetwork(ctx context.Context, containerID, netns string, networkName string) error {
	log.Info().
		Str("containerID", containerID).
		Str("network", networkName).
		Msg("CNI DEL called")

	// Set environment variables
	env := []string{
		fmt.Sprintf("CNI_COMMAND=%s", "DEL"),
		fmt.Sprintf("CNI_CONTAINERID=%s", containerID),
		fmt.Sprintf("CNI_NETNS=%s", netns),
		fmt.Sprintf("CNI_IFNAME=%s", "eth0"),
		fmt.Sprintf("CNI_PATH=%s", c.config.BinDir),
	}

	// Get network config
	netConfig, err := c.getNetworkConfig(networkName)
	if err != nil {
		// Config might be deleted - that's OK for DEL
		netConfig = fmt.Sprintf(`{"cniVersion":"1.0.0","name":"%s","type":"swarmcracker-cni"}`, networkName)
	}

	// Call CNI plugin
	cmd := exec.CommandContext(ctx, filepath.Join(c.config.BinDir, "swarmcracker-cni"))
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(netConfig)

	if _, err := cmd.Output(); err != nil {
		// DEL should be tolerant of errors
		log.Warn().Err(err).Msg("CNI DEL warning")
	}

	return nil
}

// getNetworkConfig reads the network config from CNI config directory
func (c *CNIClient) getNetworkConfig(networkName string) (string, error) {
	confDir := c.config.ConfDir

	// Look for .conflist or .conf files
	files, err := os.ReadDir(confDir)
	if err != nil {
		return "", fmt.Errorf("failed to read CNI config dir: %w", err)
	}

	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".conflist") || strings.HasSuffix(file.Name(), ".conf") {
			data, err := os.ReadFile(filepath.Join(confDir, file.Name()))
			if err != nil {
				continue
			}

			// Check if this config matches our network
			var conf struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(data, &conf); err != nil {
				continue
			}

			if conf.Name == networkName {
				return string(data), nil
			}
		}
	}

	// Return default config if not found
	return fmt.Sprintf(`{"cniVersion":"1.0.0","name":"%s","type":"swarmcracker-cni","bridge":"swarm-br0"}`, networkName), nil
}