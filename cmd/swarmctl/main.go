// swarmctl - Simple SwarmKit control client for testing
package main

import (
	"fmt"
	"os"

	"github.com/moby/swarmkit/v2/api"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	client, ctx, cancel := connectControlAPI()
	defer cancel()
	defer client.conn.Close()

	switch os.Args[1] {
	case "list-nodes", "ls-nodes":
		listNodes(ctx, client.client)
	case "list-services", "ls-services", "ls":
		listServices(ctx, client.client)
	case "list-tasks", "ls-tasks":
		listTasks(ctx, client.client)
	case "create-service":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl create-service <image> [--network <network-id>] [--name <name>] [--replicas <n>]")
			os.Exit(1)
		}
		createService(ctx, client.client, os.Args[2:])
	case "create-network":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl create-network <name> [--subnet <subnet>] [--driver <driver>]")
			os.Exit(1)
		}
		createNetwork(ctx, client.client, os.Args[2:])
	case "list-networks", "ls-networks":
		listNetworks(ctx, client.client)
	case "remove-service", "rm-service":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl rm-service <service-id>")
			os.Exit(1)
		}
		removeService(ctx, client.client, os.Args[2])
	case "stop-task":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl stop-task <task-id>")
			os.Exit(1)
		}
		stopTask(ctx, client.client, os.Args[2])
	case "snapshot":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl snapshot <create|list|restore> ...")
			os.Exit(1)
		}
		handleSnapshotCommand(os.Args[2:])
	case "inspect":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl inspect <service-id|task-id>")
			os.Exit(1)
		}
		inspectService(ctx, client.client, os.Args[2])
	case "scale":
		if len(os.Args) < 4 {
			fmt.Println("Usage: swarmctl scale <service-id> <replicas>")
			os.Exit(1)
		}
		replicas, err := parseInt(os.Args[3])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid replicas: %v\n", err)
			os.Exit(1)
		}
		scaleService(ctx, client.client, os.Args[2], replicas)
	case "update":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl update <service-id> [flags]")
			os.Exit(1)
		}
		updateService(ctx, client.client, os.Args[2:], os.Args[2])
	case "drain":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl drain <node-id>")
			os.Exit(1)
		}
		setNodeAvailability(ctx, client.client, os.Args[2], api.NodeAvailabilityDrain)
	case "activate":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl activate <node-id>")
			os.Exit(1)
		}
		setNodeAvailability(ctx, client.client, os.Args[2], api.NodeAvailabilityActive)
	case "pause-node":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl pause-node <node-id>")
			os.Exit(1)
		}
		setNodeAvailability(ctx, client.client, os.Args[2], api.NodeAvailabilityPause)
	case "promote":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl promote <node-id>")
			os.Exit(1)
		}
		promoteNode(ctx, client.client, os.Args[2])
	case "demote":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl demote <node-id>")
			os.Exit(1)
		}
		demoteNode(ctx, client.client, os.Args[2])
	case "logs":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl logs <task-id> [--lines N]")
			os.Exit(1)
		}
		lines := 100
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--lines" && i+1 < len(os.Args) {
				if n, err := parseInt(os.Args[i+1]); err == nil {
					lines = int(n)
				}
			}
		}
		getTaskLogs(ctx, client.client, os.Args[2], lines)
	case "metrics":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl metrics <task-id>")
			os.Exit(1)
		}
		getTaskMetrics(ctx, client.client, os.Args[2])
	case "volume":
		if len(os.Args) < 3 {
			fmt.Println("Usage: swarmctl volume <create|list|inspect|rm> ...")
			os.Exit(1)
		}
		handleVolumeCommand(os.Args[2:])
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println("SwarmKit Control Client")
	fmt.Println()
	fmt.Println("Services:")
	fmt.Println("  ls-services, ls   List services")
	fmt.Println("  create-service    Create a service from an image")
	fmt.Println("  rm-service        Remove a service")
	fmt.Println("  inspect           Inspect a service or task")
	fmt.Println("  scale             Scale a service to N replicas")
	fmt.Println("  update            Update service (image, replicas, env)")
	fmt.Println()
	fmt.Println("Nodes:")
	fmt.Println("  ls-nodes          List nodes in the cluster")
	fmt.Println("  drain             Drain a node (reschedule tasks)")
	fmt.Println("  activate          Activate a drained/paused node")
	fmt.Println("  pause-node        Pause a node (no new tasks)")
	fmt.Println("  promote           Promote worker to manager")
	fmt.Println("  demote            Demote manager to worker")
	fmt.Println()
	fmt.Println("Tasks:")
	fmt.Println("  ls-tasks          List tasks")
	fmt.Println("  logs              Get VM logs for a task")
	fmt.Println("  metrics           Get VM resource metrics for a task")
	fmt.Println("  stop-task         Stop a running task/VM")
	fmt.Println()
	fmt.Println("Volumes:")
	fmt.Println("  volume create     Create a persistent volume")
	fmt.Println("  volume list       List all volumes")
	fmt.Println("  volume inspect    Inspect a volume")
	fmt.Println("  volume rm         Remove a volume")
	fmt.Println()
	fmt.Println("Snapshots:")
	fmt.Println("  snapshot create   Create a VM snapshot")
	fmt.Println("  snapshot list     List snapshots")
	fmt.Println("  snapshot restore  Restore from snapshot")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  SWARM_SOCKET      Path to swarm socket (default: /var/run/swarmkit/swarm.sock)")
	fmt.Println("  SWARM_STATE_DIR   State directory (default: /var/lib/swarmkit)")
}
