package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// newNetworkCommand creates the network command group
func newNetworkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Manage network configuration",
		Long: `Manage network configuration for SwarmCracker.

Provides commands for VXLAN overlay and bridge management.`,
	}

	// Add subcommands
	cmd.AddCommand(newNetworkVXLANCommand())
	cmd.AddCommand(newNetworkBridgeCommand())
	cmd.AddCommand(newNetworkCreateCommand())
	cmd.AddCommand(newNetworkListCommand())
	cmd.AddCommand(newNetworkInspectCommand())
	cmd.AddCommand(newNetworkRemoveCommand())

	return cmd
}

// newNetworkVXLANCommand creates the VXLAN subcommand
func newNetworkVXLANCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vxlan",
		Short: "Manage VXLAN overlay",
		Long:  `Manage VXLAN overlay networking for cross-node VM communication.`,
	}

	cmd.AddCommand(newVXLANListCommand())
	cmd.AddCommand(newVXLANStatusCommand())

	return cmd
}

// newVXLANListCommand lists VXLAN peers
func newVXLANListCommand() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"peers", "list"},
		Short:   "List VXLAN interfaces and peers",
		Long: `List VXLAN overlay interfaces present on this node.

The overlay is only configured when VXLAN is enabled
('swarmcracker cluster init --vxlan-enabled'); otherwise no interfaces are shown.
This command is read-only and reports the host's current state.

Examples:
  swarmcracker network vxlan ls
  swarmcracker network vxlan ls --format json`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listVXLANPeers(format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")

	return cmd
}

// listVXLANPeers lists the VXLAN interfaces on this node.
func listVXLANPeers(format string) error {
	peers, err := queryVXLANPeers()
	if err != nil {
		return err
	}

	if strings.EqualFold(format, "json") {
		if peers == nil {
			peers = []vxlanPeer{}
		}
		data, _ := json.MarshalIndent(peers, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	if len(peers) == 0 {
		fmt.Println("No VXLAN interfaces found (VXLAN overlay is not enabled on this node).")
		return nil
	}

	fmt.Printf("%-16s %-8s %-16s %-16s %s\n", "INTERFACE", "VNI", "LOCAL", "REMOTE", "STATE")
	fmt.Println(strings.Repeat("-", 72))
	for _, p := range peers {
		fmt.Printf("%-16s %-8s %-16s %-16s %s\n", p.Interface, p.VNI, p.Local, p.Remote, p.State)
	}
	fmt.Printf("\nTotal: %d VXLAN interface(s)\n", len(peers))
	return nil
}

// newVXLANStatusCommand shows VXLAN status
func newVXLANStatusCommand() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show VXLAN status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listVXLANPeers(format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")
	return cmd
}

// newNetworkBridgeCommand creates the bridge subcommand
func newNetworkBridgeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bridge",
		Short: "Manage bridge network",
		Long:  `Manage the bridge network for local VM communication.`,
	}

	cmd.AddCommand(newBridgeStatusCommand())

	return cmd
}

// newBridgeStatusCommand shows bridge status
func newBridgeStatusCommand() *cobra.Command {
	var (
		format string
		bridge string
	)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show bridge status",
		Long: `Show the VM bridge's state, addresses, and attached interfaces.

Examples:
  swarmcracker network bridge status
  swarmcracker network bridge status --format json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return showBridgeStatus(bridge, format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")
	cmd.Flags().StringVar(&bridge, "bridge", "swarm-br0", "Bridge device name")
	return cmd
}

// showBridgeStatus prints the status of a bridge device.
func showBridgeStatus(name, format string) error {
	bs, err := queryBridgeStatus(name)
	if err != nil {
		return err
	}

	if strings.EqualFold(format, "json") {
		data, _ := json.MarshalIndent(bs, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	fmt.Printf("Bridge:  %s\n", bs.Name)
	fmt.Printf("State:   %s\n", bs.State)
	if len(bs.Addresses) > 0 {
		fmt.Printf("Address: %s\n", strings.Join(bs.Addresses, ", "))
	} else {
		fmt.Printf("Address: (none)\n")
	}
	if len(bs.Members) > 0 {
		fmt.Printf("Members: %s\n", strings.Join(bs.Members, ", "))
	} else {
		fmt.Printf("Members: (none)\n")
	}
	return nil
}

// --- read-only introspection ---

type bridgeStatus struct {
	Name      string   `json:"name"`
	State     string   `json:"state"`
	Addresses []string `json:"addresses"`
	Members   []string `json:"members"`
}

type vxlanPeer struct {
	Interface string `json:"interface"`
	VNI       string `json:"vni,omitempty"`
	Local     string `json:"local,omitempty"`
	Remote    string `json:"remote,omitempty"`
	State     string `json:"state"`
}

// ipJSONAddr is the subset of `ip -j addr show` that we consume.
type ipJSONAddr struct {
	IfName    string `json:"ifname"`
	OperState string `json:"operstate"`
	AddrInfo  []struct {
		Family    string `json:"family"`
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
	} `json:"addr_info"`
}

// ipJSONLink is the subset of `ip -j link show` that we consume.
type ipJSONLink struct {
	IfName string `json:"ifname"`
}

var (
	reVXLANID     = regexp.MustCompile(`vxlan id (\d+)`)
	reVXLANLocal  = regexp.MustCompile(`\blocal ([0-9a-fA-F:.]+)`)
	reVXLANRemote = regexp.MustCompile(`\bremote ([0-9a-fA-F:.]+)`)
	reIfName      = regexp.MustCompile(`^\d+:\s+([^:@]+)[:@]`)
	reLinkState   = regexp.MustCompile(`\bstate (\w+)`)
)

// queryBridgeStatus reports a bridge device's state, addresses, and members.
func queryBridgeStatus(name string) (*bridgeStatus, error) {
	addrOut, err := exec.Command("ip", "-j", "addr", "show", name).Output()
	if err != nil {
		return nil, fmt.Errorf("bridge %s not found", name)
	}

	bs := &bridgeStatus{Name: name, Addresses: []string{}, Members: []string{}}

	var addrs []ipJSONAddr
	if jerr := json.Unmarshal(addrOut, &addrs); jerr == nil && len(addrs) > 0 {
		bs.State = addrs[0].OperState
		for _, ai := range addrs[0].AddrInfo {
			if ai.Family == "inet" || ai.Family == "inet6" {
				bs.Addresses = append(bs.Addresses, fmt.Sprintf("%s/%d", ai.Local, ai.PrefixLen))
			}
		}
	}

	if linkOut, lerr := exec.Command("ip", "-j", "link", "show", "master", name).Output(); lerr == nil {
		var links []ipJSONLink
		if jerr := json.Unmarshal(linkOut, &links); jerr == nil {
			for _, l := range links {
				bs.Members = append(bs.Members, l.IfName)
			}
		}
	}

	return bs, nil
}

// queryVXLANPeers reports VXLAN interfaces on the host. No interfaces is not
// an error.
func queryVXLANPeers() ([]vxlanPeer, error) {
	out, err := exec.Command("ip", "-d", "link", "show", "type", "vxlan").CombinedOutput()
	if err != nil && strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	return parseVXLANPeers(string(out)), nil
}

// parseVXLANPeers extracts VXLAN interfaces from `ip -d link show type vxlan`.
func parseVXLANPeers(out string) []vxlanPeer {
	var peers []vxlanPeer
	var current *vxlanPeer

	for _, line := range strings.Split(out, "\n") {
		if m := reIfName.FindStringSubmatch(line); m != nil {
			if current != nil {
				peers = append(peers, *current)
			}
			state := "UNKNOWN"
			if sm := reLinkState.FindStringSubmatch(line); sm != nil {
				state = sm[1]
			} else if strings.Contains(line, ",UP") {
				state = "UP"
			}
			current = &vxlanPeer{Interface: m[1], State: state}
		}
		if current == nil {
			continue
		}
		if m := reVXLANID.FindStringSubmatch(line); m != nil {
			current.VNI = m[1]
		}
		if m := reVXLANLocal.FindStringSubmatch(line); m != nil {
			current.Local = m[1]
		}
		if m := reVXLANRemote.FindStringSubmatch(line); m != nil {
			current.Remote = m[1]
		}
	}

	if current != nil {
		peers = append(peers, *current)
	}
	return peers
}
