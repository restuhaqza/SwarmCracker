package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
)

func listServices(ctx context.Context, client api.ControlClient) {
	resp, err := client.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list services: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Services) == 0 {
		fmt.Println("No services found")
		return
	}

	fmt.Printf("%-20s %-30s %-8s %s\n", "ID", "NAME", "MODE", "REPLICAS")
	fmt.Println(strings.Repeat("-", 70))
	for _, svc := range resp.Services {
		name := svc.Spec.Annotations.Name
		if name == "" {
			name = "<unnamed>"
		}
		mode := "replicated"
		replicas := ""
		if replicated := svc.Spec.GetReplicated(); replicated != nil {
			replicas = fmt.Sprintf("%d", replicated.Replicas)
		}
		fmt.Printf("%-24s %-30s %-8s %s\n", svc.ID, name, mode, replicas)
	}
	fmt.Printf("\nTotal: %d service(s)\n", len(resp.Services))
}

func createService(ctx context.Context, client api.ControlClient, args []string) {
	// Parse image (first arg)
	image := args[0]

	// Parse flags
	var networkID string
	var svcName string
	var replicas uint64 = 1

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--network":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--network requires a value\n")
				os.Exit(1)
			}
			networkID = args[i+1]
			i++
		case "--name":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--name requires a value\n")
				os.Exit(1)
			}
			svcName = args[i+1]
			i++
		case "--replicas":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--replicas requires a value\n")
				os.Exit(1)
			}
			replicas, _ = parseInt(args[i+1])
			i++
		}
	}

	// Generate service name if not provided
	if svcName == "" {
		svcName = fmt.Sprintf("svc-%s", image[strings.LastIndex(image, "/")+1:])
		if strings.Contains(svcName, ":") {
			svcName = svcName[:strings.Index(svcName, ":")]
		}
		svcName = svcName + "-" + time.Now().Format("150405")
	}

	// Build task spec with optional network
	taskSpec := api.TaskSpec{
		Runtime: &api.TaskSpec_Container{
			Container: &api.ContainerSpec{
				Image: image,
			},
		},
	}

	if networkID != "" {
		taskSpec.Networks = []*api.NetworkAttachmentConfig{
			{
				Target: networkID,
			},
		}
	}

	req := &api.CreateServiceRequest{
		Spec: &api.ServiceSpec{
			Annotations: api.Annotations{
				Name: svcName,
			},
			Task: taskSpec,
			Mode: &api.ServiceSpec_Replicated{
				Replicated: &api.ReplicatedService{
					Replicas: replicas,
				},
			},
		},
	}

	resp, err := client.CreateService(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create service: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Service created: %s\n", resp.Service.ID)
	fmt.Printf("Name: %s\n", svcName)
	fmt.Printf("Image: %s\n", image)
	if networkID != "" {
		fmt.Printf("Network: %s\n", networkID)
	}
	fmt.Printf("Replicas: %d\n", replicas)
}

func removeService(ctx context.Context, client api.ControlClient, serviceID string) {
	req := &api.RemoveServiceRequest{
		ServiceID: serviceID,
	}

	_, err := client.RemoveService(ctx, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to remove service: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Service %s removed\n", serviceID)
}

func inspectService(ctx context.Context, client api.ControlClient, id string) {
	resp, err := client.GetService(ctx, &api.GetServiceRequest{
		ServiceID: id,
	})
	if err != nil {
		// Try as task
		taskResp, taskErr := client.GetTask(ctx, &api.GetTaskRequest{
			TaskID: id,
		})
		if taskErr != nil {
			fmt.Fprintf(os.Stderr, "Failed to find service or task: %v\n", err)
			os.Exit(1)
		}
		printJSON(taskResp.Task)
		return
	}
	printJSON(resp.Service)
}

func scaleService(ctx context.Context, client api.ControlClient, serviceID string, replicas uint64) {
	// Get current service
	resp, err := client.GetService(ctx, &api.GetServiceRequest{ServiceID: serviceID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get service: %v\n", err)
		os.Exit(1)
	}

	// Update replicas
	svc := resp.Service
	if replicated := svc.Spec.GetReplicated(); replicated != nil {
		replicated.Replicas = replicas
	} else {
		fmt.Fprintf(os.Stderr, "Service is not replicated\n")
		os.Exit(1)
	}

	_, err = client.UpdateService(ctx, &api.UpdateServiceRequest{
		ServiceID:      serviceID,
		ServiceVersion: &svc.Meta.Version,
		Spec:           &svc.Spec,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to scale service: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Service %s scaled to %d replicas\n", serviceID, replicas)
}

func updateService(ctx context.Context, client api.ControlClient, args []string, serviceID string) {
	// Get current service
	resp, err := client.GetService(ctx, &api.GetServiceRequest{ServiceID: serviceID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get service: %v\n", err)
		os.Exit(1)
	}

	svc := resp.Service

	// Parse flags
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--image":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--image requires a value\n")
				os.Exit(1)
			}
			if container := svc.Spec.Task.GetContainer(); container != nil {
				container.Image = args[i+1]
			}
			i++
		case "--replicas":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--replicas requires a value\n")
				os.Exit(1)
			}
			replicas, err := parseInt(args[i+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Invalid replicas: %v\n", err)
				os.Exit(1)
			}
			if replicated := svc.Spec.GetReplicated(); replicated != nil {
				replicated.Replicas = replicas
			}
			i++
		case "--env":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "--env requires a value\n")
				os.Exit(1)
			}
			if container := svc.Spec.Task.GetContainer(); container != nil {
				parts := strings.SplitN(args[i+1], "=", 2)
				if len(parts) == 2 {
					// Remove existing env with same key
					for j, env := range container.Env {
						if strings.HasPrefix(env, parts[0]+"=") {
							container.Env = append(container.Env[:j], container.Env[j+1:]...)
							break
						}
					}
					container.Env = append(container.Env, args[i+1])
				}
			}
			i++
		default:
			// Unknown flag, skip
		}
	}

	_, err = client.UpdateService(ctx, &api.UpdateServiceRequest{
		ServiceID:      serviceID,
		ServiceVersion: &svc.Meta.Version,
		Spec:           &svc.Spec,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to update service: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Service %s updated\n", serviceID)
}
