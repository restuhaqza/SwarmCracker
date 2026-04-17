// swarmcracker-cni - CNI plugin for SwarmCracker Firecracker VMs
//
// Implements the CNI spec (https://github.com/containernetworking/cni/spec)
// to create TAP devices for Firecracker microVMs attached to SwarmKit networks.
//
// Commands:
//   ADD  - Create TAP device and connect to bridge
//   DEL  - Remove TAP device
//   CHECK - Verify TAP device exists
//   VERSION - Return plugin version
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	cnitypes "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/vishvananda/netlink"

	"github.com/restuhaqza/swarmcracker/pkg/network"
)

const (
	PluginVersion = "0.1.0"
)

// NetConf is the CNI plugin configuration
type NetConf struct {
	types.NetConf

	// SwarmCracker-specific config
	BridgeName   string `json:"bridge"`
	VXLANEnabled bool   `json:"vxlanEnabled"`
	VXLANVNI     int    `json:"vxlanVNI,omitempty"`
	VXLANPeers   string `json:"vxlanPeers,omitempty"` // comma-separated IPs

	// TAP device config
	TAPPrefix string `json:"tapPrefix,omitempty"` // default: "tap-"
}

func main() {
	// PluginMain signature: cmdAdd, cmdCheck, cmdDel, versionInfo, about
	// Note: cmdCheck comes BEFORE cmdDel in the argument order!
	skel.PluginMain(cmdAdd, cmdCheck, cmdDel, version.All, "swarmcracker-cni")
}

// cmdAdd creates a TAP device and connects it to the bridge
func cmdAdd(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}

	// Get container ID (VM ID for Firecracker)
	containerID := args.ContainerID
	if containerID == "" {
		return fmt.Errorf("CNI_CONTAINERID is required")
	}

	// Create TAP device name from container ID
	// Interface names must be <= 15 chars, so use prefix + up to 8 chars of ID
	idLen := 8
	if len(containerID) < idLen {
		idLen = len(containerID)
	}
	tapName := conf.TAPPrefix + containerID[:idLen]
	if conf.TAPPrefix == "" {
		tapName = "tap-" + containerID[:idLen]
	}

	// Determine IP address from CNI_ARGS (SwarmKit provides this)
	var ipAddr string
	if args.Args != "" {
		ipAddr = parseIPFromArgs(args.Args)
	}

	if ipAddr == "" {
		return fmt.Errorf("IP address required via CNI_ARGS")
	}

	// Create TAP device
	tap, err := network.CreateTAPDevice(tapName, conf.BridgeName)
	if err != nil {
		return fmt.Errorf("failed to create TAP device %s: %w", tapName, err)
	}

	// Configure VXLAN if enabled
	if conf.VXLANEnabled && conf.VXLANPeers != "" {
		peers := parseVXLANPeers(conf.VXLANPeers)
		if len(peers) > 0 {
			if err := network.SetupVXLANFDB(tapName, peers); err != nil {
				// Log warning, don't fail
				fmt.Fprintf(os.Stderr, "VXLAN FDB setup warning: %v\n", err)
			}
		}
	}

	// Build result
	result := &cnitypes.Result{
		CNIVersion: "1.0.0",
		Interfaces: []*cnitypes.Interface{
			{
				Name:    tapName,
				Sandbox: args.Netns,
				Mac:     tap.MAC,
			},
		},
		IPs: []*cnitypes.IPConfig{
			{
				Address:   *parseCIDR(ipAddr),
				Interface: cnitypes.Int(0),
			},
		},
	}

	return types.PrintResult(result, conf.CNIVersion)
}

// cmdDel removes the TAP device
func cmdDel(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}

	containerID := args.ContainerID
	if containerID == "" {
		return fmt.Errorf("CNI_CONTAINERID is required")
	}

	// Create TAP device name (same logic as cmdAdd)
	idLen := 8
	if len(containerID) < idLen {
		idLen = len(containerID)
	}
	tapName := conf.TAPPrefix + containerID[:idLen]
	if conf.TAPPrefix == "" {
		tapName = "tap-" + containerID[:idLen]
	}

	fmt.Fprintf(os.Stderr, "CNI DEL: attempting to delete TAP %s\n", tapName)

	// Delete TAP device
	if err := network.DeleteTAPDevice(tapName); err != nil {
		fmt.Fprintf(os.Stderr, "CNI DEL error: %v\n", err)
		// Return nil anyway - CNI spec says DEL should be tolerant of missing resources
	} else {
		fmt.Fprintf(os.Stderr, "CNI DEL: TAP %s deleted successfully\n", tapName)
	}

	return nil
}

// cmdCheck verifies the TAP device exists
func cmdCheck(args *skel.CmdArgs) error {
	conf, err := parseConfig(args.StdinData)
	if err != nil {
		return err
	}

	containerID := args.ContainerID
	if containerID == "" {
		return fmt.Errorf("CNI_CONTAINERID is required")
	}

	// Create TAP device name (same logic as cmdAdd)
	idLen := 8
	if len(containerID) < idLen {
		idLen = len(containerID)
	}
	tapName := conf.TAPPrefix + containerID[:idLen]
	if conf.TAPPrefix == "" {
		tapName = "tap-" + containerID[:idLen]
	}

	// Check if TAP exists using netlink
	link, err := netlink.LinkByName(tapName)
	if err != nil {
		return fmt.Errorf("TAP device %s does not exist: %w", tapName, err)
	}

	if link.Type() != "tuntap" {
		return fmt.Errorf("%s is not a TAP device", tapName)
	}

	return nil
}

// parseConfig parses the CNI configuration JSON
func parseConfig(data []byte) (*NetConf, error) {
	conf := &NetConf{}
	if err := json.Unmarshal(data, conf); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Set defaults
	if conf.BridgeName == "" {
		conf.BridgeName = "swarm-br0"
	}

	return conf, nil
}

// parseIPFromArgs extracts IP from CNI_ARGS (format: "IP=10.0.0.2/24;...")
func parseIPFromArgs(args string) string {
	pairs := strings.Split(args, ";")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 && strings.TrimSpace(kv[0]) == "IP" {
			return strings.TrimSpace(kv[1])
		}
	}
	return ""
}

// parseVXLANPeers parses comma-separated peer IPs
func parseVXLANPeers(peers string) []string {
	if peers == "" {
		return nil
	}
	result := []string{}
	for _, p := range strings.Split(peers, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// parseCIDR parses CIDR string into net.IPNet
func parseCIDR(cidr string) *net.IPNet {
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		// Fallback: assume /24
		ip = net.ParseIP(cidr)
		if ip == nil {
			ip = net.ParseIP("10.0.0.2")
		}
		ipNet = &net.IPNet{
			IP:   ip,
			Mask: net.IPv4Mask(255, 255, 255, 0),
		}
	}
	return ipNet
}