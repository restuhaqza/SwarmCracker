package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/moby/swarmkit/v2/api"
)

func createNetwork(ctx context.Context, client api.ControlClient, args []string) {
	// Parse name (first arg)
	name := args[0]

	// Parse flags
	var subnet = "10.0.9.0/24"
	var driver = "overlay"

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--subnet":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--subnet requires a value\n")
				os.Exit(1)
			}
			subnet = args[i+1]
			i++
		case "--driver":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--driver requires a value\n")
				os.Exit(1)
			}
			driver = args[i+1]
			i++
		}
	}

	req := &api.CreateNetworkRequest{
		Spec: &api.NetworkSpec{
			Annotations: api.Annotations{
				Name: name,
			},
			DriverConfig: &api.Driver{
				Name: driver,
			},
			IPAM: &api.IPAMOptions{
				Configs: []*api.IPAMConfig{
					{
						Subnet: subnet,
					},
				},
			},
		},
	}

	resp, err := client.CreateNetwork(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create network: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Network created: %s\n", resp.Network.ID)
	fmt.Printf("Name: %s\n", name)
	fmt.Printf("Driver: %s\n", driver)
	fmt.Printf("Subnet: %s\n", subnet)
}

func listNetworks(ctx context.Context, client api.ControlClient) {
	resp, err := client.ListNetworks(ctx, &api.ListNetworksRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list networks: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Networks) == 0 {
		fmt.Println("No networks found")
		return
	}

	fmt.Printf("%-20s %-20s %-15s %s\n", "ID", "NAME", "DRIVER", "SUBNET")
	fmt.Println(strings.Repeat("-", 80))
	for _, net := range resp.Networks {
		name := net.Spec.Annotations.Name
		driver := ""
		if net.Spec.DriverConfig != nil {
			driver = net.Spec.DriverConfig.Name
		}
		subnet := ""
		if net.Spec.IPAM != nil && len(net.Spec.IPAM.Configs) > 0 {
			subnet = net.Spec.IPAM.Configs[0].Subnet
		}
		fmt.Printf("%-20s %-20s %-15s %s\n", net.ID[:12], name, driver, subnet)
	}
	fmt.Printf("\nTotal: %d network(s)\n", len(resp.Networks))
}
