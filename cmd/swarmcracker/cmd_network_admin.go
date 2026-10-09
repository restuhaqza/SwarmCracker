package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/spf13/cobra"
)

// newNetworkCreateCommand creates a user-defined network.
func newNetworkCreateCommand() *cobra.Command {
	var (
		name   string
		subnet string
		driver string
		labels []string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a user-defined network",
		Long: `Create a user-defined network with its own subnet (and overlay VXLAN).

Services attached with 'service create --network <name>' are placed on this
network and are isolated from services on other networks.`,
		Example: `  swarmcracker network create --name backend --subnet 10.10.0.0/24
  swarmcracker network create --name frontend --driver overlay`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return createNetwork(name, subnet, driver, labels)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Network name (required)")
	cmd.Flags().StringVar(&subnet, "subnet", "", "Subnet CIDR (e.g. 10.10.0.0/24); allocated automatically if omitted")
	cmd.Flags().StringVar(&driver, "driver", "overlay", "Network driver: overlay or bridge")
	cmd.Flags().StringArrayVarP(&labels, "label", "l", nil, "Network labels (key=value)")

	cobra.CheckErr(cmd.MarkFlagRequired("name"))

	return cmd
}

func createNetwork(name, subnet, driver string, labels []string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("--name is required")
	}
	// The SwarmKit/CNI driver name for an overlay is "vxlan"; accept the
	// Docker-style "overlay" as an alias.
	switch driver {
	case "overlay", "vxlan":
		driver = "vxlan"
	case "bridge":
	default:
		return fmt.Errorf("invalid --driver %q (want overlay or bridge)", driver)
	}
	if subnet != "" {
		if _, _, err := net.ParseCIDR(subnet); err != nil {
			return fmt.Errorf("invalid --subnet %q: %w", subnet, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	spec := &api.NetworkSpec{
		Annotations:  api.Annotations{Name: name, Labels: parseLabels(labels)},
		DriverConfig: &api.Driver{Name: driver},
	}
	if subnet != "" {
		spec.IPAM = &api.IPAMOptions{Configs: []*api.IPAMConfig{{Subnet: subnet}}}
	}

	resp, err := client.CreateNetwork(ctx, &api.CreateNetworkRequest{Spec: spec})
	if err != nil {
		return fmt.Errorf("failed to create network: %w", err)
	}

	fmt.Printf("Network %s created with ID: %s\n", name, resp.Network.ID)
	if subnet != "" {
		fmt.Printf("Subnet: %s\n", subnet)
	}
	fmt.Printf("Driver: %s\n", driver)
	return nil
}

// newNetworkListCommand lists user-defined networks.
func newNetworkListCommand() *cobra.Command {
	var (
		format string
		quiet  bool
	)

	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List networks",
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listUserNetworks(format, quiet)
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only display IDs")
	return cmd
}

func listUserNetworks(format string, quiet bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.ListNetworks(ctx, &api.ListNetworksRequest{})
	if err != nil {
		return fmt.Errorf("failed to list networks: %w", err)
	}

	if format == "json" {
		data, _ := json.MarshalIndent(resp.Networks, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	// Exclude the built-in ingress/gateway networks from the table.
	var nets []*api.Network
	for _, n := range resp.Networks {
		if n.Spec.Ingress {
			continue
		}
		if n.Spec.Annotations.Name == "ingress" || n.Spec.Annotations.Name == "docker_gwbridge" {
			continue
		}
		nets = append(nets, n)
	}

	if quiet {
		for _, n := range nets {
			fmt.Println(n.ID)
		}
		return nil
	}

	if len(nets) == 0 {
		fmt.Println("No user-defined networks found")
		return nil
	}

	fmt.Printf("%-20s %-20s %-10s %-18s %s\n", "ID", "NAME", "DRIVER", "SUBNET", "VNI")
	fmt.Println(strings.Repeat("-", 90))
	for _, n := range nets {
		id := n.ID
		if len(id) > 12 {
			id = id[:12]
		}
		driver := ""
		if n.Spec.DriverConfig != nil {
			driver = n.Spec.DriverConfig.Name
		}
		subnet, vni := networkAllocation(n)
		fmt.Printf("%-20s %-20s %-10s %-18s %s\n", id, n.Spec.Annotations.Name, driver, subnet, vni)
	}
	fmt.Printf("\nTotal: %d network(s)\n", len(nets))
	return nil
}

// newNetworkInspectCommand inspects a network.
func newNetworkInspectCommand() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "inspect <name|id>",
		Short: "Inspect a network",
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return inspectUserNetwork(args[0], format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Output format (json)")
	return cmd
}

func inspectUserNetwork(ref, format string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	id, err := resolveNetworkRef(ctx, client, ref)
	if err != nil {
		return err
	}
	resp, err := client.GetNetwork(ctx, &api.GetNetworkRequest{NetworkID: id})
	if err != nil {
		return fmt.Errorf("failed to get network: %w", err)
	}

	data, _ := json.MarshalIndent(resp.Network, "", "  ")
	fmt.Println(string(data))
	return nil
}

// newNetworkRemoveCommand removes a network.
func newNetworkRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rm <name|id>",
		Aliases: []string{"remove"},
		Short:   "Remove a network",
		Args:    cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return removeUserNetwork(args[0])
		},
	}
	return cmd
}

func removeUserNetwork(ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	id, err := resolveNetworkRef(ctx, client, ref)
	if err != nil {
		return err
	}

	// Refuse while any service is still attached, with a clear message.
	svcResp, err := client.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}
	var attached []string
	for _, svc := range svcResp.Services {
		for _, na := range svc.Spec.Task.Networks {
			if na.Target == id {
				attached = append(attached, svc.Spec.Annotations.Name)
				break
			}
		}
	}
	if len(attached) > 0 {
		return fmt.Errorf("network is in use by service(s) %s; remove or update them first", strings.Join(attached, ", "))
	}

	if _, err := client.RemoveNetwork(ctx, &api.RemoveNetworkRequest{NetworkID: id}); err != nil {
		return fmt.Errorf("failed to remove network: %w", err)
	}
	fmt.Printf("Network %s removed\n", ref)
	return nil
}

// resolveNetworkRef maps a full ID, unique ID prefix, or name to a network ID.
func resolveNetworkRef(ctx context.Context, client api.ControlClient, ref string) (string, error) {
	resp, err := client.ListNetworks(ctx, &api.ListNetworksRequest{})
	if err != nil {
		return "", fmt.Errorf("failed to list networks: %w", err)
	}
	ref = strings.TrimSpace(ref)
	var matches []string
	for _, n := range resp.Networks {
		if n.ID == ref || n.Spec.Annotations.Name == ref || strings.HasPrefix(n.ID, ref) {
			matches = append(matches, n.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("network %q not found", ref)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("network reference %q is ambiguous", ref)
	}
}

// networkAllocation returns the allocated subnet and VNI for display.
func networkAllocation(n *api.Network) (subnet, vni string) {
	if n.IPAM != nil && len(n.IPAM.Configs) > 0 {
		subnet = n.IPAM.Configs[0].Subnet
	}
	if n.DriverState != nil {
		if v := n.DriverState.Options["vxlan_vni"]; v != "" {
			vni = v
		}
	}
	if subnet == "" {
		subnet = "-"
	}
	if vni == "" {
		vni = "-"
	}
	return subnet, vni
}
