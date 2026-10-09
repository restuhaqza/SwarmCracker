package cni

import (
	"encoding/binary"
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
func ParseCIDR(cidr string) (*net.IPNet, net.IP, error) {
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid CIDR %s: %w", cidr, err)
	}
	return ipNet, ip, nil
}

// GenerateSubnet creates a new subnet from a pool.
//
// The subnet is the pool base plus networkIndex whole subnets, so consecutive
// indices yield consecutive subnets. For example, from 10.0.0.0/8 with subnet
// size 24:
//
//   - Network 1: 10.0.1.0/24
//   - Network 2: 10.0.2.0/24
//   - Network 255: 10.0.255.0/24
//
// When the pool is exactly the subnet size (e.g. 192.168.127.0/24 with /24),
// the pool itself is returned. The index wraps within the pool.
func GenerateSubnet(poolCIDR string, subnetSize int, networkIndex uint32) (string, error) {
	poolNet, poolIP, err := ParseCIDR(poolCIDR)
	if err != nil {
		return "", err
	}

	poolBase := poolIP.To4()
	if poolBase == nil {
		return "", fmt.Errorf("pool must be IPv4")
	}

	poolBits, _ := poolNet.Mask.Size()
	if subnetSize < poolBits || subnetSize > 32 {
		return "", fmt.Errorf("invalid pool/subnet combination: pool /%d subnet /%d", poolBits, subnetSize)
	}

	base := uint64(binary.BigEndian.Uint32(poolBase))
	step := uint64(1) << uint(32-subnetSize)
	poolSize := uint64(1) << uint(32-poolBits)
	count := poolSize / step // number of subnets that fit in the pool
	if count == 0 {
		return "", fmt.Errorf("invalid pool/subnet combination: pool /%d subnet /%d", poolBits, subnetSize)
	}

	addr := base + uint64(networkIndex)%count*step
	if addr > 0xffffffff {
		return "", fmt.Errorf("subnet index %d exceeds pool %s", networkIndex, poolCIDR)
	}

	subnetIP := make(net.IP, 4)
	binary.BigEndian.PutUint32(subnetIP, uint32(addr))

	return fmt.Sprintf("%s/%d", subnetIP.String(), subnetSize), nil
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
