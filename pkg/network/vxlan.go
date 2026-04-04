//go:build linux

package network

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/vishvananda/netlink"
)

// VXLANManager handles VXLAN overlay network creation and management.
type VXLANManager struct {
	BridgeName string
	VXLANID    int
	OverlayIP  string
	Peers      []string
	vxlanPort  int
}

// NewVXLANManager creates a new VXLAN manager.
func NewVXLANManager(bridgeName string, vxlanID int, overlayIP string, peers []string) *VXLANManager {
	return &VXLANManager{
		BridgeName: bridgeName,
		VXLANID:    vxlanID,
		OverlayIP:  overlayIP,
		Peers:      peers,
		vxlanPort:  4789,
	}
}

// SetupVXLAN creates and configures the VXLAN overlay network.
func (v *VXLANManager) SetupVXLAN(physInterface, localIP string) error {
	if err := v.ensureVXLANModule(); err != nil {
		return fmt.Errorf("failed to load VXLAN module: %w", err)
	}

	vxlanName := v.BridgeName + "-vxlan"

	if err := v.createVXLANInterface(vxlanName, physInterface, localIP); err != nil {
		return fmt.Errorf("failed to create VXLAN interface: %w", err)
	}

	if err := v.attachVXLANToBridge(vxlanName); err != nil {
		return fmt.Errorf("failed to attach VXLAN to bridge: %w", err)
	}

	if err := v.addOverlayIP(); err != nil {
		return fmt.Errorf("failed to add overlay IP: %w", err)
	}

	for _, peer := range v.Peers {
		if err := v.addPeerForwarding(vxlanName, peer); err != nil {
			return fmt.Errorf("failed to add peer %s: %w", peer, err)
		}
	}

	if err := v.enableProxySettings(); err != nil {
		return fmt.Errorf("failed to enable proxy settings: %w", err)
	}

	return nil
}

// createVXLANInterface creates the VXLAN network interface.
func (v *VXLANManager) createVXLANInterface(name, physInterface, localIP string) error {
	// Delete existing VXLAN if present
	if link, err := netlink.LinkByName(name); err == nil {
		netlink.LinkDel(link)
	}

	ip := net.ParseIP(localIP)
	if ip == nil {
		return fmt.Errorf("invalid local IP: %s", localIP)
	}

	physLink, err := netlink.LinkByName(physInterface)
	if err != nil {
		return fmt.Errorf("physical interface %s not found: %w", physInterface, err)
	}

	vxlan := &netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{
			Name: name,
			MTU:  1450,
		},
		VxlanId:      v.VXLANID,
		VtepDevIndex: physLink.Attrs().Index,
		SrcAddr:      ip,
		Port:         v.vxlanPort,
	}

	if err := netlink.LinkAdd(vxlan); err != nil {
		return fmt.Errorf("failed to add VXLAN link: %w", err)
	}

	if err := netlink.LinkSetUp(vxlan); err != nil {
		return fmt.Errorf("failed to bring VXLAN link up: %w", err)
	}

	return nil
}

// attachVXLANToBridge attaches the VXLAN interface to the bridge.
func (v *VXLANManager) attachVXLANToBridge(vxlanName string) error {
	vxlanLink, err := netlink.LinkByName(vxlanName)
	if err != nil {
		return fmt.Errorf("VXLAN interface not found: %w", err)
	}

	bridgeLink, err := netlink.LinkByName(v.BridgeName)
	if err != nil {
		return fmt.Errorf("bridge %s not found: %w", v.BridgeName, err)
	}

	if err := netlink.LinkSetMaster(vxlanLink, bridgeLink); err != nil {
		return fmt.Errorf("failed to attach VXLAN to bridge: %w", err)
	}

	return nil
}

// addOverlayIP adds the overlay network IP to the bridge.
func (v *VXLANManager) addOverlayIP() error {
	bridgeLink, err := netlink.LinkByName(v.BridgeName)
	if err != nil {
		return fmt.Errorf("bridge not found: %w", err)
	}

	ip, ipNet, err := net.ParseCIDR(v.OverlayIP)
	if err != nil {
		return fmt.Errorf("invalid overlay CIDR: %w", err)
	}

	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   ip,
			Mask: ipNet.Mask,
		},
	}

	if err := netlink.AddrAdd(bridgeLink, addr); err != nil {
		// Ignore "file exists" error (address already assigned)
		if !strings.Contains(err.Error(), "file exists") {
			return fmt.Errorf("failed to add overlay IP: %w", err)
		}
	}

	return nil
}

// addPeerForwarding adds forwarding database entries for peer nodes.
func (v *VXLANManager) addPeerForwarding(vxlanName, peerIP string) error {
	ip := net.ParseIP(peerIP)
	if ip == nil {
		return fmt.Errorf("invalid peer IP: %s", peerIP)
	}

	vxlanLink, err := netlink.LinkByName(vxlanName)
	if err != nil {
		return fmt.Errorf("VXLAN interface not found: %w", err)
	}

	// Add an FDB entry: any MAC → dst peerIP
	neigh := &netlink.Neigh{
		LinkIndex:    vxlanLink.Attrs().Index,
		State:        netlink.NUD_PERMANENT,
		HardwareAddr: []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		Family:       netlink.FAMILY_V4,
		IP:           ip,
	}

	if err := netlink.NeighAdd(neigh); err != nil {
		if !strings.Contains(err.Error(), "file exists") {
			return fmt.Errorf("failed to add FDB entry: %w", err)
		}
	}

	return nil
}

// AddRouteToSubnet adds a route to reach a remote worker's VM subnet.
func (v *VXLANManager) AddRouteToSubnet(remoteSubnet, remoteOverlayIP string) error {
	_, dstNet, err := net.ParseCIDR(remoteSubnet)
	if err != nil {
		return fmt.Errorf("invalid remote subnet: %w", err)
	}

	gw := net.ParseIP(remoteOverlayIP)
	if gw == nil {
		return fmt.Errorf("invalid gateway IP: %s", remoteOverlayIP)
	}

	bridgeLink, err := netlink.LinkByName(v.BridgeName)
	if err != nil {
		return fmt.Errorf("bridge not found: %w", err)
	}

	route := &netlink.Route{
		Dst:       dstNet,
		Gw:        gw,
		LinkIndex: bridgeLink.Attrs().Index,
		Scope:     netlink.SCOPE_UNIVERSE,
	}

	if err := netlink.RouteAdd(route); err != nil {
		if !strings.Contains(err.Error(), "file exists") {
			return fmt.Errorf("failed to add route: %w", err)
		}
	}

	return nil
}

// enableProxySettings enables proxy ARP and IP forwarding.
func (v *VXLANManager) enableProxySettings() error {
	writeSysctl := func(key string, value string) error {
		path := fmt.Sprintf("/proc/sys/%s", key)
		return os.WriteFile(path, []byte(value), 0644)
	}

	if err := writeSysctl(fmt.Sprintf("net/ipv4/conf/%s/proxy_arp", v.BridgeName), "1"); err != nil {
		return fmt.Errorf("failed to enable proxy ARP: %w", err)
	}

	if err := writeSysctl(fmt.Sprintf("net/ipv4/conf/%s/forwarding", v.BridgeName), "1"); err != nil {
		return fmt.Errorf("failed to enable forwarding: %w", err)
	}

	if err := writeSysctl("net/ipv4/ip_forward", "1"); err != nil {
		return fmt.Errorf("failed to enable global IP forwarding: %w", err)
	}

	return nil
}

// ensureVXLANModule ensures the VXLAN kernel module is loaded.
func (v *VXLANManager) ensureVXLANModule() error {
	// Kernel auto-loads VXLAN module when creating a VXLAN interface.
	// No explicit modprobe needed when using netlink.
	return nil
}
