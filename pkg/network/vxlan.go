//go:build linux

package network

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/vishvananda/netlink"
)

// PeerStore defines the interface for storing and retrieving VXLAN peer information.
type PeerStore interface {
	// GetPeers returns the current list of peer IPs.
	GetPeers() []string
	// AddPeer adds a peer IP to the store.
	AddPeer(ip string)
	// RemovePeer removes a peer IP from the store.
	RemovePeer(ip string)
}

// StaticPeerStore is a simple map-based peer store for manually configured peers.
type StaticPeerStore struct {
	mu    sync.RWMutex
	peers map[string]bool
}

// NewStaticPeerStore creates a new static peer store with initial peers.
func NewStaticPeerStore(initialPeers []string) *StaticPeerStore {
	ps := &StaticPeerStore{
		peers: make(map[string]bool),
	}
	for _, peer := range initialPeers {
		ps.peers[peer] = true
	}
	return ps
}

// GetPeers returns the current list of peer IPs.
func (s *StaticPeerStore) GetPeers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peers := make([]string, 0, len(s.peers))
	for peer := range s.peers {
		peers = append(peers, peer)
	}
	return peers
}

// AddPeer adds a peer IP to the store.
func (s *StaticPeerStore) AddPeer(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[ip] = true
}

// RemovePeer removes a peer IP from the store.
func (s *StaticPeerStore) RemovePeer(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.peers, ip)
}

// VXLANManager handles VXLAN overlay network creation and management.
type VXLANManager struct {
	BridgeName string
	VXLANID    int
	OverlayIP  string
	vxlanPort  int
	peerStore  PeerStore
	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	// netlinkExecutor is the interface for netlink operations
	netlinkExecutor NetlinkExecutor
}

// NewVXLANManager creates a new VXLAN manager.
func NewVXLANManager(bridgeName string, vxlanID int, overlayIP string, peerStore PeerStore) *VXLANManager {
	if peerStore == nil {
		peerStore = NewStaticPeerStore(nil)
	}
	return &VXLANManager{
		BridgeName:      bridgeName,
		VXLANID:         vxlanID,
		OverlayIP:       overlayIP,
		vxlanPort:       4789,
		peerStore:       peerStore,
		netlinkExecutor: NewDefaultNetlinkExecutor(),
	}
}

// NewVXLANManagerWithExecutor creates a new VXLAN manager with a custom executor.
// This is primarily used for testing to inject mock implementations.
func NewVXLANManagerWithExecutor(bridgeName string, vxlanID int, overlayIP string, peerStore PeerStore, executor NetlinkExecutor) *VXLANManager {
	if peerStore == nil {
		peerStore = NewStaticPeerStore(nil)
	}
	if executor == nil {
		executor = NewDefaultNetlinkExecutor()
	}
	return &VXLANManager{
		BridgeName:      bridgeName,
		VXLANID:         vxlanID,
		OverlayIP:       overlayIP,
		vxlanPort:       4789,
		peerStore:       peerStore,
		netlinkExecutor: executor,
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

	for _, peer := range v.peerStore.GetPeers() {
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
	if link, err := v.netlinkExecutor.LinkByName(name); err == nil {
		v.netlinkExecutor.LinkDel(link)
	}

	ip := net.ParseIP(localIP)
	if ip == nil {
		return fmt.Errorf("invalid local IP: %s", localIP)
	}

	physLink, err := v.netlinkExecutor.LinkByName(physInterface)
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

	if err := v.netlinkExecutor.LinkAdd(vxlan); err != nil {
		return fmt.Errorf("failed to add VXLAN link: %w", err)
	}

	if err := v.netlinkExecutor.LinkSetUp(vxlan); err != nil {
		return fmt.Errorf("failed to bring VXLAN link up: %w", err)
	}

	return nil
}

// attachVXLANToBridge attaches the VXLAN interface to the bridge.
func (v *VXLANManager) attachVXLANToBridge(vxlanName string) error {
	vxlanLink, err := v.netlinkExecutor.LinkByName(vxlanName)
	if err != nil {
		return fmt.Errorf("VXLAN interface not found: %w", err)
	}

	bridgeLink, err := v.netlinkExecutor.LinkByName(v.BridgeName)
	if err != nil {
		return fmt.Errorf("bridge %s not found: %w", v.BridgeName, err)
	}

	if err := v.netlinkExecutor.LinkSetMaster(vxlanLink, bridgeLink); err != nil {
		return fmt.Errorf("failed to attach VXLAN to bridge: %w", err)
	}

	return nil
}

// addOverlayIP adds the overlay network IP to the bridge.
func (v *VXLANManager) addOverlayIP() error {
	bridgeLink, err := v.netlinkExecutor.LinkByName(v.BridgeName)
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

	if err := v.netlinkExecutor.AddrAdd(bridgeLink, addr); err != nil {
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

	vxlanLink, err := v.netlinkExecutor.LinkByName(vxlanName)
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

	if err := v.netlinkExecutor.NeighAdd(neigh); err != nil {
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

	bridgeLink, err := v.netlinkExecutor.LinkByName(v.BridgeName)
	if err != nil {
		return fmt.Errorf("bridge not found: %w", err)
	}

	route := &netlink.Route{
		Dst:       dstNet,
		Gw:        gw,
		LinkIndex: bridgeLink.Attrs().Index,
		Scope:     netlink.SCOPE_UNIVERSE,
	}

	if err := v.netlinkExecutor.RouteAdd(route); err != nil {
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

// UpdatePeers updates the peer list and adds forwarding entries for new peers.
func (v *VXLANManager) UpdatePeers(newPeers []string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	vxlanName := v.BridgeName + "-vxlan"

	// Check if VXLAN interface exists
	if _, err := v.netlinkExecutor.LinkByName(vxlanName); err != nil {
		return fmt.Errorf("VXLAN interface not found: %w", err)
	}

	// Get current peers
	currentPeers := make(map[string]bool)
	for _, peer := range v.peerStore.GetPeers() {
		currentPeers[peer] = true
	}

	// Add new peers
	for _, peer := range newPeers {
		if !currentPeers[peer] {
			if err := v.addPeerForwarding(vxlanName, peer); err != nil {
				log.Warn().Err(err).Str("peer", peer).Msg("Failed to add peer forwarding")
				continue
			}
			v.peerStore.AddPeer(peer)
			log.Info().Str("peer", peer).Msg("Added VXLAN peer")
		}
		delete(currentPeers, peer)
	}

	// Remove old peers
	for peer := range currentPeers {
		v.peerStore.RemovePeer(peer)
		log.Info().Str("peer", peer).Msg("Removed VXLAN peer")
	}

	return nil
}

// StartPeerDiscovery starts UDP-based peer discovery.
// Workers multicast their presence on startup and listen for other workers.
func (v *VXLANManager) StartPeerDiscovery(ctx context.Context, localIP string, port int) error {
	v.mu.Lock()
	if v.cancel != nil {
		v.mu.Unlock()
		return fmt.Errorf("peer discovery already running")
	}

	v.ctx, v.cancel = context.WithCancel(ctx)
	v.mu.Unlock()

	// Start UDP listener for peer announcements
	go v.listenForPeers(localIP, port)

	// Announce our presence periodically
	go v.announcePresence(localIP, port)

	log.Info().Str("local_ip", localIP).Int("port", port).Msg("Started VXLAN peer discovery")
	return nil
}

// StopPeerDiscovery stops the peer discovery process.
func (v *VXLANManager) StopPeerDiscovery() {
	v.mu.Lock()
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
	v.mu.Unlock()

	// Wait briefly for goroutines to observe cancellation.
	// We don't nil v.ctx here because goroutines may still be
	// reading from v.ctx.Done() after the cancel signal.
}

// listenForPeers listens for UDP peer announcements.
func (v *VXLANManager) listenForPeers(localIP string, port int) {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", localIP, port))
	if err != nil {
		log.Error().Err(err).Msg("Failed to resolve UDP address for peer discovery")
		return
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Error().Err(err).Msg("Failed to start UDP listener for peer discovery")
		return
	}
	defer conn.Close()

	buf := make([]byte, 1024)

	for {
		select {
		case <-v.ctx.Done():
			return
		default:
			conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			n, peerAddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				log.Debug().Err(err).Msg("Error reading from UDP")
				continue
			}

			if n > 0 {
				message := string(buf[:n])
				// Expected format: "VXLAN_PEER:<ip>"
				if strings.HasPrefix(message, "VXLAN_PEER:") {
					peerIP := strings.TrimPrefix(message, "VXLAN_PEER:")
					// Don't add ourselves
					if peerIP != localIP {
						existingPeers := v.peerStore.GetPeers()
						found := false
						for _, p := range existingPeers {
							if p == peerIP {
								found = true
								break
							}
						}
						if !found {
							v.peerStore.AddPeer(peerIP)
							log.Info().Str("peer", peerIP).Str("from", peerAddr.String()).Msg("Discovered VXLAN peer via UDP")
							// Add FDB entry
							vxlanName := v.BridgeName + "-vxlan"
							if err := v.addPeerForwarding(vxlanName, peerIP); err != nil {
								log.Warn().Err(err).Str("peer", peerIP).Msg("Failed to add discovered peer to FDB")
							}
						}
					}
				}
			}
		}
	}
}

// announcePresence periodically announces this worker's presence.
func (v *VXLANManager) announcePresence(localIP string, port int) {
	// Get network interface for multicast
	interfaces, err := net.Interfaces()
	if err != nil {
		log.Error().Err(err).Msg("Failed to get network interfaces")
		return
	}

	var broadcastIPs []string
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					// Calculate broadcast address
					if ipnet.Mask != nil {
						broadcast := make(net.IP, len(ipnet.IP.To4()))
						for i := range broadcast {
							broadcast[i] = ipnet.IP.To4()[i] | ^ipnet.Mask[i]
						}
						broadcastIPs = append(broadcastIPs, fmt.Sprintf("%s:%d", broadcast.String(), port))
					}
				}
			}
		}
	}

	if len(broadcastIPs) == 0 {
		log.Warn().Msg("No broadcast addresses found for peer announcement")
		return
	}

	message := []byte(fmt.Sprintf("VXLAN_PEER:%s", localIP))

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Announce immediately on startup
	for _, addr := range broadcastIPs {
		v.sendAnnouncement(addr, message)
	}

	for {
		select {
		case <-v.ctx.Done():
			return
		case <-ticker.C:
			for _, addr := range broadcastIPs {
				v.sendAnnouncement(addr, message)
			}
		}
	}
}

// sendAnnouncement sends a peer announcement to the given address.
func (v *VXLANManager) sendAnnouncement(addr string, message []byte) {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		log.Debug().Err(err).Str("addr", addr).Msg("Failed to dial broadcast address")
		return
	}
	defer conn.Close()

	_, err = conn.Write(message)
	if err != nil {
		log.Debug().Err(err).Str("addr", addr).Msg("Failed to send peer announcement")
	}
}

// GetPeers returns the current list of peers.
func (v *VXLANManager) GetPeers() []string {
	return v.peerStore.GetPeers()
}
