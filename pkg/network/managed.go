//go:build linux

package network

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// isolationChain is the iptables chain used to drop traffic between distinct
// VM bridges, so VMs on different user-defined networks cannot reach each
// other even though the host routes between their subnets.
const isolationChain = "SC-ISOLATION"

// managedNetwork is a user-defined network materialized on this node: its own
// bridge, an optional overlay VXLAN, a subnet, and a per-network IP allocator.
// Distinct bridges keep distinct networks isolated at L2 on a node.
type managedNetwork struct {
	id        string
	name      string
	bridge    string
	subnet    string
	gateway   string
	vxlanID   int
	vxlanMgr  *VXLANManager
	allocator *IPAllocator
}

// networkBridgeName returns the bridge device name for a network: the name the
// allocator recorded, or a deterministic fallback derived from the network ID.
func networkBridgeName(network *types.Network) string {
	if network == nil {
		return ""
	}
	if network.Spec.DriverConfig != nil && network.Spec.DriverConfig.Bridge != nil {
		if name := strings.TrimSpace(network.Spec.DriverConfig.Bridge.Name); name != "" {
			return name
		}
	}
	if len(network.ID) >= 8 {
		return "sc-" + network.ID[:8]
	}
	return "sc-" + network.ID
}

// managed reports whether the attachment targets a user-defined network (one
// with its own allocated subnet), as opposed to the default bridge.
func managed(network *types.Network) bool {
	return network != nil && strings.TrimSpace(network.Spec.Subnet) != ""
}

// hasManagedAttachment reports whether any attachment targets a user-defined
// network.
func hasManagedAttachment(networks []types.NetworkAttachment) bool {
	for i := range networks {
		if managed(&networks[i].Network) {
			return true
		}
	}
	return false
}

// ensureBridgeNamed creates a bridge device if it does not already exist and
// brings it up.
func (nm *NetworkManager) ensureBridgeNamed(bridge string) error {
	if err := validateBridgeName(bridge); err != nil {
		return err
	}
	if err := execCommand("ip", "link", "show", bridge).Run(); err == nil {
		_ = execCommand("ip", "link", "set", bridge, "up").Run()
		return nil
	}
	if err := execCommand("ip", "link", "add", bridge, "type", "bridge").Run(); err != nil {
		return fmt.Errorf("failed to create bridge: %w", err)
	}
	if err := execCommand("ip", "link", "set", bridge, "up").Run(); err != nil {
		return fmt.Errorf("failed to bring bridge up: %w", err)
	}
	return nil
}

// setBridgeAddr assigns an address (CIDR) to a bridge, ignoring "file exists".
func (nm *NetworkManager) setBridgeAddr(bridge, cidr string) error {
	if cidr == "" {
		return nil
	}
	if err := execCommand("ip", "addr", "add", cidr, "dev", bridge).Run(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "exists") {
			return nil
		}
		return err
	}
	return nil
}

// ensureNetworkVXLAN creates the per-network overlay VXLAN interface (VNI),
// attaches it to the network's bridge, and programs an FDB entry for every peer
// node so the network's L2 domain spans the cluster. Best-effort on nodes
// without a physical uplink; same-node traffic does not need it.
func (nm *NetworkManager) ensureNetworkVXLAN(bridge string, vni int) (*VXLANManager, error) {
	if vni <= 0 {
		return nil, nil
	}
	phys, localIP, err := nm.getPhysicalInterface()
	if err != nil {
		return nil, fmt.Errorf("no physical interface for VXLAN: %w", err)
	}
	peers := nm.allVXLANPeers()
	vxm := NewVXLANManager(bridge, vni, "", NewStaticPeerStore(peers))
	vxlanName := vxm.vxlanName()
	if err := vxm.createVXLANInterface(vxlanName, phys, localIP); err != nil {
		return nil, err
	}
	if err := vxm.attachVXLANToBridge(vxlanName); err != nil {
		return nil, err
	}
	// Flood broadcast/unknown-unicast to every peer node so the network's L2
	// domain spans the cluster, exactly like the default overlay.
	for _, peer := range peers {
		if err := vxm.addPeerForwarding(vxlanName, peer); err != nil {
			log.Warn().Err(err).Str("vxlan", vxlanName).Str("peer", peer).Msg("Failed to add network VXLAN peer")
		}
	}
	return vxm, nil
}

// setupManagedNAT adds a masquerade rule for the network's subnet so VMs on it
// keep outbound connectivity.
func (nm *NetworkManager) setupManagedNAT(bridge, subnet string) {
	rule := []string{"-t", "nat", "-C", "POSTROUTING", "-s", subnet, "!", "-o", bridge, "-j", "MASQUERADE"}
	if err := execCommand("iptables", rule...).Run(); err == nil {
		return
	}
	add := []string{"-t", "nat", "-A", "POSTROUTING", "-s", subnet, "!", "-o", bridge, "-j", "MASQUERADE"}
	if err := execCommand("iptables", add...).Run(); err != nil {
		log.Warn().Err(err).Str("subnet", subnet).Msg("Failed to add NAT rule for managed network")
	}
}

// rebuildIsolationRules reconciles the isolation chain so traffic between any
// two distinct VM bridges (the default bridge and every managed network) is
// dropped. It is idempotent: the chain is flushed and rebuilt from the current
// bridge set each time.
func (nm *NetworkManager) rebuildIsolationRules() {
	nm.mu.RLock()
	bridges := []string{}
	if nm.config.BridgeName != "" {
		bridges = append(bridges, nm.config.BridgeName)
	}
	for _, m := range nm.managedNetworks {
		if m.bridge != "" {
			bridges = append(bridges, m.bridge)
		}
	}
	nm.mu.RUnlock()

	// Ensure the chain exists and is jumped from FORWARD (before any ACCEPT).
	if err := execCommand("iptables", "-N", isolationChain).Run(); err != nil {
		// Already exists is fine.
		log.Debug().Msg("Isolation chain already exists")
	}
	if err := execCommand("iptables", "-C", "FORWARD", "-j", isolationChain).Run(); err != nil {
		if err := execCommand("iptables", "-I", "FORWARD", "1", "-j", isolationChain).Run(); err != nil {
			log.Warn().Err(err).Msg("Failed to install isolation chain jump")
		}
	}
	if err := execCommand("iptables", "-F", isolationChain).Run(); err != nil {
		log.Warn().Err(err).Msg("Failed to flush isolation chain")
	}

	for _, a := range bridges {
		for _, b := range bridges {
			if a == b {
				continue
			}
			if err := execCommand("iptables", "-A", isolationChain, "-i", a, "-o", b, "-j", "DROP").Run(); err != nil {
				log.Debug().Err(err).Str("from", a).Str("to", b).Msg("Failed to add isolation rule")
			}
		}
	}
}

// ensureManagedNetwork materializes (idempotently) a user-defined network on
// this node and returns its handle.
func (nm *NetworkManager) ensureManagedNetwork(ctx context.Context, network *types.Network) (*managedNetwork, error) {
	if network == nil || network.ID == "" {
		return nil, fmt.Errorf("network is missing an ID")
	}

	nm.mu.RLock()
	existing := nm.managedNetworks[network.ID]
	nm.mu.RUnlock()
	if existing != nil {
		return existing, nil
	}

	bridge := networkBridgeName(network)
	if bridge == "" {
		return nil, fmt.Errorf("network %s has no bridge name", network.ID)
	}
	if err := nm.ensureBridgeNamed(bridge); err != nil {
		return nil, fmt.Errorf("failed to create bridge %s: %w", bridge, err)
	}

	// Gateway address on the bridge, derived from the allocated subnet.
	gateway := strings.TrimSpace(network.Spec.Gateway)
	cidr := ""
	if network.Spec.Subnet != "" {
		_, ipNet, err := net.ParseCIDR(network.Spec.Subnet)
		if err != nil {
			return nil, fmt.Errorf("invalid subnet %q: %w", network.Spec.Subnet, err)
		}
		if gateway == "" {
			gw := make(net.IP, len(ipNet.IP))
			copy(gw, ipNet.IP)
			gw[len(gw)-1]++
			gateway = gw.String()
		}
		ones, _ := ipNet.Mask.Size()
		cidr = fmt.Sprintf("%s/%d", gateway, ones)
	}
	if err := nm.setBridgeAddr(bridge, cidr); err != nil {
		log.Warn().Err(err).Str("bridge", bridge).Msg("Failed to set managed bridge address")
	}

	vxm, err := nm.ensureNetworkVXLAN(bridge, network.Spec.VXLANID)
	if err != nil {
		log.Warn().Err(err).Str("bridge", bridge).Int("vni", network.Spec.VXLANID).Msg("Failed to set up network VXLAN")
	}

	if network.Spec.Subnet != "" && nm.config.NATEnabled {
		nm.setupManagedNAT(bridge, network.Spec.Subnet)
	}

	var alloc *IPAllocator
	if network.Spec.Subnet != "" && gateway != "" {
		if a, err := NewIPAllocatorForNode(network.Spec.Subnet, gateway, nm.nodeIndex, nm.nodeSlots); err == nil {
			alloc = a
		}
	}

	mn := &managedNetwork{
		id:        network.ID,
		name:      network.Spec.Name,
		bridge:    bridge,
		subnet:    network.Spec.Subnet,
		gateway:   gateway,
		vxlanID:   network.Spec.VXLANID,
		vxlanMgr:  vxm,
		allocator: alloc,
	}

	nm.mu.Lock()
	if nm.managedNetworks == nil {
		nm.managedNetworks = make(map[string]*managedNetwork)
	}
	if other := nm.managedNetworks[network.ID]; other != nil {
		nm.mu.Unlock()
		return other, nil
	}
	nm.managedNetworks[network.ID] = mn
	nm.mu.Unlock()

	// Reconcile isolation now that the bridge set changed.
	nm.rebuildIsolationRules()

	log.Info().
		Str("network_id", network.ID).
		Str("name", network.Spec.Name).
		Str("bridge", bridge).
		Str("subnet", network.Spec.Subnet).
		Int("vni", network.Spec.VXLANID).
		Msg("Materialized user-defined network")

	return mn, nil
}

// prepareManagedNetworks attaches a task's TAPs to its user-defined networks.
// Each attachment gets its own bridge (isolation) and an IP from the network's
// subnet (SwarmKit-assigned when present, otherwise allocated locally).
func (nm *NetworkManager) prepareManagedNetworks(ctx context.Context, task *types.Task) error {
	if nm.config.NATEnabled && !nm.natSetup {
		if err := nm.setupNAT(ctx); err != nil {
			log.Warn().Err(err).Msg("Failed to setup NAT, VMs may not have internet access")
		} else {
			nm.natSetup = true
		}
	}

	for i := range task.Networks {
		attachment := &task.Networks[i]

		if !managed(&attachment.Network) {
			// Mixed attachment: fall back to the default bridge for this one.
			tap, err := nm.createTapDevice(ctx, *attachment, i, task.ID)
			if err != nil {
				return fmt.Errorf("failed to create TAP device: %w", err)
			}
			nm.mu.Lock()
			nm.tapDevices[task.ID+"-"+tap.Name] = tap
			nm.mu.Unlock()
			continue
		}

		mn, err := nm.ensureManagedNetwork(ctx, &attachment.Network)
		if err != nil {
			return fmt.Errorf("failed to prepare network %q: %w", attachment.Network.Spec.Name, err)
		}

		if len(attachment.Addresses) == 0 && mn.allocator != nil {
			ip, aerr := mn.allocator.Allocate(task.ID)
			if aerr != nil {
				return fmt.Errorf("failed to allocate IP on network %q: %w", mn.name, aerr)
			}
			_, ipNet, _ := net.ParseCIDR(mn.subnet)
			ones, _ := ipNet.Mask.Size()
			attachment.Addresses = []string{fmt.Sprintf("%s/%d", ip, ones)}
		}

		tap, err := nm.createTapDevice(ctx, *attachment, i, task.ID)
		if err != nil {
			return fmt.Errorf("failed to create TAP device on network %q: %w", mn.name, err)
		}
		nm.mu.Lock()
		nm.tapDevices[task.ID+"-"+tap.Name] = tap
		nm.mu.Unlock()

		log.Info().
			Str("task_id", task.ID).
			Str("network", mn.name).
			Str("bridge", tap.Bridge).
			Str("ip", tap.IP).
			Msg("TAP device created on user-defined network")
	}

	return nil
}

// reconcileManagedNetworks tears down the node-local resources of every
// user-defined network that no local task uses anymore (no tracked TAP sits on
// its bridge). It runs after a task is cleaned up so `network rm` does not leave
// orphaned bridges and VXLAN interfaces behind; a network that is still needed
// is re-materialized on demand.
func (nm *NetworkManager) reconcileManagedNetworks() {
	nm.mu.RLock()
	stale := make([]*managedNetwork, 0, len(nm.managedNetworks))
	for _, m := range nm.managedNetworks {
		if !nm.bridgeHasLocalTapLocked(m.bridge) {
			stale = append(stale, m)
		}
	}
	nm.mu.RUnlock()

	for _, m := range stale {
		nm.teardownManagedNetwork(m)
	}
}

// bridgeHasLocalTapLocked reports whether any tracked TAP still sits on the
// bridge. The caller must hold nm.mu (read or write).
func (nm *NetworkManager) bridgeHasLocalTapLocked(bridge string) bool {
	for _, tap := range nm.tapDevices {
		if tap.Bridge == bridge {
			return true
		}
	}
	return false
}

// teardownManagedNetwork deletes a user-defined network's VXLAN and bridge on
// this node and reconciles the isolation rules. It re-checks that the network is
// still unused so a concurrent task start is not disrupted.
func (nm *NetworkManager) teardownManagedNetwork(mn *managedNetwork) {
	if mn == nil {
		return
	}

	nm.mu.Lock()
	if nm.bridgeHasLocalTapLocked(mn.bridge) {
		nm.mu.Unlock()
		return
	}
	delete(nm.managedNetworks, mn.id)
	nm.mu.Unlock()

	if mn.vxlanMgr != nil {
		name := mn.vxlanMgr.vxlanName()
		if err := execCommand("ip", "link", "delete", name).Run(); err != nil {
			log.Debug().Err(err).Str("vxlan", name).Msg("Network VXLAN interface already gone")
		}
	}
	if mn.bridge != "" {
		if err := execCommand("ip", "link", "delete", mn.bridge).Run(); err != nil {
			log.Debug().Err(err).Str("bridge", mn.bridge).Msg("Managed bridge already gone")
		}
	}

	nm.rebuildIsolationRules()

	log.Info().
		Str("network_id", mn.id).
		Str("name", mn.name).
		Str("bridge", mn.bridge).
		Msg("Tore down user-defined network")
}
