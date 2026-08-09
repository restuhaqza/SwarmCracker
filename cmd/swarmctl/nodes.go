package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/moby/swarmkit/v2/api"
)

func listNodes(ctx context.Context, client api.ControlClient) {
	resp, err := client.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list nodes: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Nodes) == 0 {
		fmt.Println("No nodes found")
		return
	}

	fmt.Printf("%-20s %-12s %-20s %s\n", "ID", "STATUS", "HOSTNAME", "AVAILABILITY")
	fmt.Println(strings.Repeat("-", 70))
	for _, node := range resp.Nodes {
		status := node.Status.State.String()
		avail := node.Spec.Availability.String()
		hostname := node.Description.Hostname
		fmt.Printf("%-20s %-12s %-20s %s\n", node.ID[:12], status, hostname, avail)
	}
	fmt.Printf("\nTotal: %d node(s)\n", len(resp.Nodes))
}

func setNodeAvailability(ctx context.Context, client api.ControlClient, nodeID string, availability api.NodeSpec_Availability) {
	// Get current node
	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get node: %v\n", err)
		os.Exit(1)
	}

	node := resp.Node
	node.Spec.Availability = availability

	_, err = client.UpdateNode(ctx, &api.UpdateNodeRequest{
		NodeID:      nodeID,
		NodeVersion: &node.Meta.Version,
		Spec:        &node.Spec,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to update node: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Node %s availability set to %s\n", nodeID, availability.String())
}

func promoteNode(ctx context.Context, client api.ControlClient, nodeID string) {
	// Get current node
	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get node: %v\n", err)
		os.Exit(1)
	}

	node := resp.Node
	node.Spec.DesiredRole = api.NodeRoleManager

	_, err = client.UpdateNode(ctx, &api.UpdateNodeRequest{
		NodeID:      nodeID,
		NodeVersion: &node.Meta.Version,
		Spec:        &node.Spec,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to promote node: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Node %s promoted to manager\n", nodeID)
}

func demoteNode(ctx context.Context, client api.ControlClient, nodeID string) {
	// Get current node
	resp, err := client.GetNode(ctx, &api.GetNodeRequest{NodeID: nodeID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get node: %v\n", err)
		os.Exit(1)
	}

	node := resp.Node
	node.Spec.DesiredRole = api.NodeRoleWorker

	_, err = client.UpdateNode(ctx, &api.UpdateNodeRequest{
		NodeID:      nodeID,
		NodeVersion: &node.Meta.Version,
		Spec:        &node.Spec,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to demote node: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Node %s demoted to worker\n", nodeID)
}
