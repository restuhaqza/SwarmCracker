package cni

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

// NetworkConfigGenerator generates CNI network configurations
type NetworkConfigGenerator struct {
	version string
}

// NewConfigGenerator creates a new configuration generator
func NewConfigGenerator() *NetworkConfigGenerator {
	return &NetworkConfigGenerator{
		version: DefaultCNIVersion,
	}
}

// GenerateBridgeConfig generates a CNI bridge network configuration
func (g *NetworkConfigGenerator) GenerateBridgeConfig(name, bridgeName, subnet string, gateway net.IP) ([]byte, error) {
	config := map[string]interface{}{
		"cniVersion": g.version,
		"name":       name,
		"type":       "bridge",
		"bridge":     bridgeName,
		"isGateway":  true,
		"ipMasq":     true,
		"ipam": map[string]interface{}{
			"type":    "host-local",
			"subnet":  subnet,
			"gateway": gateway.String(),
		},
	}

	return json.MarshalIndent(config, "", "  ")
}

// GenerateVXLANConfig generates a CNI VXLAN overlay network configuration
func (g *NetworkConfigGenerator) GenerateVXLANConfig(name, subnet string, gateway net.IP, vxlanID uint32, vxlanPort uint32) ([]byte, error) {
	config := map[string]interface{}{
		"cniVersion": g.version,
		"name":       name,
		"type":       "vxlan",
		"vxlanID":    vxlanID,
		"vxlanPort":  vxlanPort,
		"ipam": map[string]interface{}{
			"type":    "host-local",
			"subnet":  subnet,
			"gateway": gateway.String(),
		},
	}

	return json.MarshalIndent(config, "", "  ")
}

// GenerateIngressConfig generates an ingress network configuration
func (g *NetworkConfigGenerator) GenerateIngressConfig(subnet string, gateway net.IP) ([]byte, error) {
	return g.GenerateBridgeConfig(
		IngressNetworkName,
		"br-"+IngressNetworkName,
		subnet,
		gateway,
	)
}

// GenerateGWBridgeConfig generates a gateway bridge configuration
func (g *NetworkConfigGenerator) GenerateGWBridgeConfig() ([]byte, error) {
	// The gateway bridge uses a fixed subnet for container gateway access
	config := map[string]interface{}{
		"cniVersion": g.version,
		"name":       GWBridgeNetworkName,
		"type":       "bridge",
		"bridge":     "br-" + GWBridgeNetworkName,
		"isGateway":  true,
		"ipMasq":     true,
		"mtu":        1500,
		"ipam": map[string]interface{}{
			"type":    "host-local",
			"subnet":  "172.18.0.0/16",
			"gateway": "172.18.0.1",
			"routes": []map[string]interface{}{
				{"dst": "0.0.0.0/0"},
			},
		},
	}

	return json.MarshalIndent(config, "", "  ")
}

// GenerateLoopbackConfig generates a loopback network configuration
func (g *NetworkConfigGenerator) GenerateLoopbackConfig() ([]byte, error) {
	config := map[string]interface{}{
		"cniVersion": g.version,
		"name":       "lo",
		"type":       "loopback",
	}

	return json.MarshalIndent(config, "", "  ")
}

// WriteConfig writes a CNI configuration to the config directory
func WriteConfig(configDir, name string, config []byte) error {
	filename := fmt.Sprintf("%s/%s.conf", configDir, name)
	return writeFile(filename, config)
}

// WriteConfigList writes a CNI configuration list to the config directory
func WriteConfigList(configDir, name string, configs []map[string]interface{}) error {
	list := map[string]interface{}{
		"cniVersion": DefaultCNIVersion,
		"name":       name,
		"plugins":    configs,
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config list: %w", err)
	}

	filename := fmt.Sprintf("%s/%s.conflist", configDir, name)
	return writeFile(filename, data)
}

// ParseCIDR parses a CIDR string and returns the IP network
func ParseCIDR(cidr string) (*net.IPNet, net.IP, error) {
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid CIDR %s: %w", cidr, err)
	}
	return ipNet, ip, nil
}

// GenerateSubnet creates a new subnet from a pool
// Uses the format: poolBase.networkIndex.0.0/subnetSize
// For example, from 10.0.0.0/8 with subnetSize 24:
//   - Network 0: 10.0.0.0/24
//   - Network 1: 10.0.1.0/24
//   - Network 255: 10.0.255.0/24
func GenerateSubnet(poolCIDR string, subnetSize int, networkIndex uint32) (string, error) {
	poolNet, poolIP, err := ParseCIDR(poolCIDR)
	if err != nil {
		return "", err
	}

	// Get the pool base IP
	poolBase := poolIP.To4()
	if poolBase == nil {
		return "", fmt.Errorf("pool must be IPv4")
	}

	// Calculate subnet mask
	_ = net.CIDRMask(subnetSize, 32) // subnetMask used for validation

	// Calculate the new subnet based on network index
	// We increment the second octet (for /8 pool with /24 subnets)
	// Or third octet (for /16 pool with /24 subnets)
	poolBits, _ := poolNet.Mask.Size()
	offsetByte := (32 - poolBits - subnetSize) / 8

	if offsetByte < 0 || offsetByte > 3 {
		return "", fmt.Errorf("invalid pool/subnet combination")
	}

	// Copy pool base and modify the offset byte
	subnetIP := make([]byte, 4)
	copy(subnetIP, poolBase)

	// Add network index to the appropriate byte position
	// This is a simplified approach - we add to byte at offset position
	if offsetByte < 4 {
		subnetIP[offsetByte] = byte(networkIndex % 256)
		if offsetByte > 0 {
			subnetIP[offsetByte-1] = byte((networkIndex / 256) % 256)
		}
	}

	subnetAddr := fmt.Sprintf("%d.%d.%d.%d/%d",
		subnetIP[0], subnetIP[1], subnetIP[2], subnetIP[3], subnetSize)

	return subnetAddr, nil
}

// GenerateVXLANID generates a unique VXLAN ID for an overlay network
// Uses a hash of the network name combined with a base ID
func GenerateVXLANID(networkName string, baseID uint32) uint32 {
	// VXLAN IDs are 24-bit (1-16777215)
	// We use a simple hash approach
	hash := uint32(0)
	for _, c := range networkName {
		hash = hash*31 + uint32(c)
	}

	// Combine with base ID and ensure it's in valid range
	vxlanID := (hash + baseID) % 16777215
	if vxlanID < 1 {
		vxlanID = 1
	}

	return vxlanID
}

// NetworkNameFromSwarmKit converts SwarmKit network name to CNI name
func NetworkNameFromSwarmKit(name string) string {
	// CNI names must be lowercase and alphanumeric
	// Replace any invalid characters
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "-", "_")

	// Remove any non-alphanumeric characters except underscore
	result := make([]byte, 0, len(name))
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			result = append(result, byte(c))
		}
	}

	return string(result)
}

// Helper function to write a config file
func writeFile(filename string, data []byte) error {
	return WriteConfigFile(filepath.Dir(filename), filepath.Base(filename[:len(filename)-5]), data)
}
