//go:build !linux

package network

import (
	"fmt"
	"net"
)

// VXLANManager manages VXLAN overlay networks (stub for non-Linux platforms).
type VXLANManager struct {
	BridgeName string
	VXLANID    int
	LocalIP    string
}

// SetupVXLAN creates a VXLAN overlay network.
func (v *VXLANManager) SetupVXLAN(physInterface string, overlayIP string, peerIPs []string) error {
	return fmt.Errorf("VXLAN is only supported on Linux")
}

// AddPeer adds a peer to the VXLAN FDB.
func (v *VXLANManager) AddPeer(peerIP string) error {
	return fmt.Errorf("VXLAN is only supported on Linux")
}

// RemovePeer removes a peer from the VXLAN FDB.
func (v *VXLANManager) RemovePeer(peerIP string) error {
	return fmt.Errorf("VXLAN is only supported on Linux")
}

// AddRouteToSubnet adds a route to reach a remote worker's VM subnet.
func (v *VXLANManager) AddRouteToSubnet(remoteSubnet, remoteOverlayIP string) error {
	return fmt.Errorf("VXLAN is only supported on Linux")
}

// EnableProxySettings enables proxy ARP and IP forwarding on the bridge.
func (v *VXLANManager) EnableProxySettings() error {
	return fmt.Errorf("VXLAN is only supported on Linux")
}

// Teardown removes the VXLAN interface and cleans up.
func (v *VXLANManager) Teardown() error {
	return fmt.Errorf("VXLAN is only supported on Linux")
}

// DiscoverPeers finds other worker IPs via various methods.
func DiscoverPeers(swarmAddr, nodeName string) ([]string, error) {
	return nil, fmt.Errorf("peer discovery is only supported on Linux")
}

// ParseOverlayIP extracts the IP without CIDR notation.
func ParseOverlayIP(cidr string) (net.IP, error) {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	return ip, nil
}

// GetPhysicalInterface finds the default network interface.
func GetPhysicalInterface() (string, error) {
	return "", fmt.Errorf("physical interface discovery is only supported on Linux")
}
