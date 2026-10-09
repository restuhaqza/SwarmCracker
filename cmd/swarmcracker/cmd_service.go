package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// newServiceCommand creates the service command group
func newServiceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage SwarmCracker services",
		Long: `Manage services in the SwarmCracker cluster.

Services are replicated sets of VMs that can be scaled and updated.`,
	}

	// Add subcommands
	cmd.AddCommand(newServiceListCommand())
	cmd.AddCommand(newServiceInspectCommand())
	cmd.AddCommand(newServicePSCommand())
	cmd.AddCommand(newServiceCreateCommand())
	cmd.AddCommand(newServiceUpdateCommand())
	cmd.AddCommand(newServiceScaleCommand())
	cmd.AddCommand(newServiceRemoveCommand())

	return cmd
}

// newServiceListCommand lists services
func newServiceListCommand() *cobra.Command {
	var (
		format string
		filter string
		quiet  bool
	)

	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List services",
		Aliases: []string{"list"},
		Example: `  swarmcracker service ls
  swarmcracker service ls --format json
  swarmcracker service ls --filter "name=my-service"`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listServices(format, filter, quiet)
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")
	cmd.Flags().StringVar(&filter, "filter", "", "Filter output (e.g., 'name=my-service')")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only display IDs")

	return cmd
}

// newServiceInspectCommand inspects a service
func newServiceInspectCommand() *cobra.Command {
	var (
		format string
		pretty bool
	)

	cmd := &cobra.Command{
		Use:   "inspect <service-id>",
		Short: "Inspect a service",
		Long:  `Display detailed information about a service.`,
		Example: `  swarmcracker service inspect my-service
  swarmcracker service inspect --format json my-service`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return inspectService(args[0], format, pretty)
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Output format (json)")
	cmd.Flags().BoolVar(&pretty, "pretty", true, "Pretty print output")

	return cmd
}

// newServicePSCommand shows service tasks
func newServicePSCommand() *cobra.Command {
	var (
		format  string
		filter  string
		quiet   bool
		noTrunc bool
	)

	cmd := &cobra.Command{
		Use:   "ps <service-id>",
		Short: "List tasks of a service",
		Long:  `List all tasks belonging to a service.`,
		Example: `  swarmcracker service ps my-service
  swarmcracker service ps --format json my-service`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listServiceTasks(args[0], format, filter, quiet, noTrunc)
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")
	cmd.Flags().StringVar(&filter, "filter", "", "Filter output (e.g., 'state=running')")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only display IDs")
	cmd.Flags().BoolVar(&noTrunc, "no-trunc", false, "Don't truncate output")

	return cmd
}

// newServiceCreateCommand creates a service
func newServiceCreateCommand() *cobra.Command {
	var (
		name        string
		image       string
		replicas    uint64
		cpu         float64
		memory      string
		env         []string
		command     []string
		args        []string
		labels      []string
		disk        string
		golden      string
		publish     []string
		publishMode string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new service",
		Long: `Create a new service in the SwarmCracker cluster.

The service will be scheduled on available nodes and can be scaled
and updated as needed.

With --golden, the service boots a prebuilt golden image (see
'swarmcracker image build') instead of building a rootfs from an OCI image.`,
		Example: `  swarmcracker service create --name myapp --image nginx:latest --replicas 3
  swarmcracker service create --name api --image myimage:v1 --cpu 2 --memory 512M
  swarmcracker service create --name worker --image busybox --command /bin/sh --args "-c,echo hello"
  swarmcracker service create --name web --golden ubuntu-24.04-docker@1.0.0 --replicas 2`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if image != "" && golden != "" {
				return fmt.Errorf("--image and --golden are mutually exclusive")
			}
			if golden != "" {
				labels = append(labels, types.GoldenLabel+"="+golden)
			}
			if image == "" {
				// A golden service needs no OCI image: the label carries the
				// golden reference. SwarmKit still validates the container image
				// reference, so substitute a syntactically valid placeholder
				// repo:tag (it is never pulled - image preparation is skipped).
				ref := parseLabels(labels)[types.GoldenLabel]
				if ref == "" {
					return fmt.Errorf("provide --image <ref> or --golden <name[@version]>")
				}
				image = goldenPlaceholderImage(ref)
			}
			return createService(name, image, replicas, cpu, memory, disk, env, command, args, labels, publish, publishMode)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Service name (required)")
	cmd.Flags().StringVar(&image, "image", "", "Container image (or use --golden)")
	cmd.Flags().StringVar(&golden, "golden", "", "Boot a prebuilt golden image (e.g., ubuntu-24.04-docker@1.0.0)")
	cmd.Flags().Uint64Var(&replicas, "replicas", 1, "Number of replicas")
	cmd.Flags().Float64Var(&cpu, "cpu", 0, "CPU limit (cores, e.g., 1.5)")
	cmd.Flags().StringVar(&memory, "memory", "", "Memory limit (e.g., 512M, 1G)")
	cmd.Flags().StringVar(&disk, "disk", "", "Minimum rootfs disk size for the VM (e.g., 10G)")
	cmd.Flags().StringArrayVarP(&env, "env", "e", nil, "Environment variables (e.g., KEY=value)")
	cmd.Flags().StringArrayVar(&command, "command", nil, "Override default container command")
	cmd.Flags().StringArrayVar(&args, "args", nil, "Container arguments")
	cmd.Flags().StringArrayVarP(&labels, "label", "l", nil, "Service labels (e.g., key=value)")
	cmd.Flags().StringArrayVarP(&publish, "publish", "p", nil, "Publish a host port to the guest ([host:]container[/tcp|udp], e.g. 8080:80)")
	cmd.Flags().StringVar(&publishMode, "publish-mode", types.PublishModeIngress, "Port publish mode: ingress (cluster load-balanced) or host (per-replica host port)")

	cobra.CheckErr(cmd.MarkFlagRequired("name"))

	return cmd
}

// newServiceUpdateCommand updates a service
func newServiceUpdateCommand() *cobra.Command {
	var (
		replicas    uint64
		cpuLimit    float64
		memoryLimit string
		image       string
		env         []string
		envRemove   []string
		publishMode string
		force       bool
	)

	cmd := &cobra.Command{
		Use:   "update <service-id>",
		Short: "Update a service",
		Long: `Update an existing service's configuration.

Supports updating replicas, resource limits, image, and environment variables.`,
		Example: `  swarmcracker service update my-service --replicas 5
  swarmcracker service update my-service --cpu-limit 2 --memory-limit 1G
  swarmcracker service update my-service --image myimage:v2
  swarmcracker service update my-service --env-add KEY=value`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateService(args[0], replicas, cmd.Flags().Changed("replicas"), cpuLimit, memoryLimit, image, env, envRemove, publishMode, cmd.Flags().Changed("publish-mode"), force)
		},
	}

	cmd.Flags().Uint64Var(&replicas, "replicas", 0, "Number of replicas (0 = no change)")
	cmd.Flags().Float64Var(&cpuLimit, "cpu-limit", 0, "CPU limit in cores (0 = no change)")
	cmd.Flags().StringVar(&memoryLimit, "memory-limit", "", "Memory limit (e.g., 512M, 1G)")
	cmd.Flags().StringVar(&image, "image", "", "New container image")
	cmd.Flags().StringArrayVar(&env, "env-add", nil, "Add environment variable (e.g., KEY=value)")
	cmd.Flags().StringArrayVar(&envRemove, "env-rm", nil, "Remove environment variable")
	cmd.Flags().StringVar(&publishMode, "publish-mode", "", "Change the publish mode of existing published ports (ingress|host)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force update even if no changes detected")

	return cmd
}

// newServiceScaleCommand scales a service
func newServiceScaleCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scale <service-id> <replicas>",
		Short: "Scale a service",
		Long: `Scale a service to the specified number of replicas.

This is a convenience command that updates the replica count.`,
		Example: `  swarmcracker service scale my-service 5
  swarmcracker service scale my-service 0`,
		Args: cobra.ExactArgs(2),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return scaleService(args[0], args[1])
		},
	}

	// Treat "-1" as a positional argument rather than an unknown flag so the
	// user gets an "invalid replica count" error instead of a flag-parse error.
	cmd.Flags().SetInterspersed(false)

	return cmd
}

// newServiceRemoveCommand removes a service
func newServiceRemoveCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "rm <service-id>",
		Short:   "Remove a service",
		Long:    `Remove a service from the cluster.`,
		Aliases: []string{"remove"},
		Example: `  swarmcracker service rm my-service
  swarmcracker service rm --force my-service`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return removeService(args[0], force)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force removal without confirmation")

	return cmd
}

// SwarmKit client helper for service operations
func getSwarmClientForService() (api.ControlClient, *grpc.ClientConn, error) {
	socketPath := "/var/run/swarmkit/swarm.sock"
	if envSocket := os.Getenv("SWARM_SOCKET"); envSocket != "" {
		socketPath = envSocket
	}

	stateDir := "/var/lib/swarmkit"
	if envState := os.Getenv("SWARM_STATE_DIR"); envState != "" {
		stateDir = envState
	}

	certDir := filepath.Join(stateDir, "certificates")
	certFile := filepath.Join(certDir, "swarm-node.crt")
	keyFile := filepath.Join(certDir, "swarm-node.key")
	caFile := filepath.Join(certDir, "swarm-root-ca.crt")

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load TLS certificate: %w", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read CA certificate: %w", err)
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: true,
	}

	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to swarm: %w", err)
	}

	client := api.NewControlClient(conn)
	return client, conn, nil
}

// parseMemory parses memory string (e.g., "512M", "1G") to bytes
func parseMemory(mem string) (int64, error) {
	if mem == "" {
		return 0, nil
	}

	mem = strings.TrimSpace(strings.ToUpper(mem))
	var multiplier int64 = 1

	switch {
	case strings.HasSuffix(mem, "G"):
		multiplier = 1024 * 1024 * 1024
		mem = strings.TrimSuffix(mem, "G")
	case strings.HasSuffix(mem, "M"):
		multiplier = 1024 * 1024
		mem = strings.TrimSuffix(mem, "M")
	case strings.HasSuffix(mem, "K"):
		multiplier = 1024
		mem = strings.TrimSuffix(mem, "K")
	case strings.HasSuffix(mem, "GB"):
		multiplier = 1024 * 1024 * 1024
		mem = strings.TrimSuffix(mem, "GB")
	case strings.HasSuffix(mem, "MB"):
		multiplier = 1024 * 1024
		mem = strings.TrimSuffix(mem, "MB")
	case strings.HasSuffix(mem, "KB"):
		multiplier = 1024
		mem = strings.TrimSuffix(mem, "KB")
	}

	var value int64
	if _, err := fmt.Sscanf(mem, "%d", &value); err != nil {
		return 0, fmt.Errorf("invalid memory format: %s", mem)
	}

	return value * multiplier, nil
}

// parseLabels parses label strings (e.g., "key=value") to map
// goldenPlaceholderImage returns a syntactically valid container image
// reference for a golden service. SwarmKit validates the reference in the
// service spec, but the image is never pulled: image preparation is skipped for
// golden tasks, and the real reference travels in the swarmcracker.golden label.
func goldenPlaceholderImage(ref string) string {
	return "swarmcracker/golden:" + strings.NewReplacer("@", "-", "/", "-").Replace(ref)
}

func parseLabels(labelStrs []string) map[string]string {
	labels := make(map[string]string)
	for _, l := range labelStrs {
		parts := strings.SplitN(l, "=", 2)
		if len(parts) == 2 {
			labels[parts[0]] = parts[1]
		}
	}
	return labels
}

// Service operations implementation

func listServices(format, filter string, quiet bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}

	if len(resp.Services) == 0 {
		fmt.Println("No services found")
		return nil
	}

	// Apply filter if specified
	services := resp.Services
	if filter != "" {
		services = filterServices(services, filter)
	}

	// Output based on format
	if format == "json" {
		data, _ := json.MarshalIndent(services, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	// Table format
	if quiet {
		for _, svc := range services {
			fmt.Println(svc.ID)
		}
		return nil
	}

	fmt.Printf("%-20s %-20s %-10s %-24s %s\n", "ID", "NAME", "REPLICAS", "PORTS", "IMAGE")
	fmt.Println(strings.Repeat("-", 100))
	for _, svc := range services {
		id := svc.ID
		if len(id) > 12 {
			id = id[:12]
		}
		name := svc.Spec.Annotations.Name
		replicas := "global"
		if r := svc.Spec.GetReplicated(); r != nil {
			replicas = fmt.Sprintf("%d", r.Replicas)
		}
		image := ""
		if svc.Spec.Task.GetContainer() != nil {
			image = svc.Spec.Task.GetContainer().Image
		}
		fmt.Printf("%-20s %-20s %-10s %-24s %s\n", id, name, replicas, formatServicePorts(svc), image)
	}
	fmt.Printf("\nTotal: %d service(s)\n", len(services))

	return nil
}

// buildEndpointPorts converts internal published ports into SwarmKit port
// configs. The publish mode determines how the port is exposed: ingress
// (cluster load-balanced, the default) or host (per-replica host port).
func buildEndpointPorts(published []types.PublishedPort, mode string) []*api.PortConfig {
	publishMode := api.PublishModeIngress
	if mode == types.PublishModeHost {
		publishMode = api.PublishModeHost
	}
	ports := make([]*api.PortConfig, 0, len(published))
	for _, p := range published {
		proto := api.ProtocolTCP
		if p.Protocol == "udp" {
			proto = api.ProtocolUDP
		}
		ports = append(ports, &api.PortConfig{
			Protocol:      proto,
			TargetPort:    p.TargetPort,
			PublishedPort: p.PublishedPort,
			PublishMode:   publishMode,
		})
	}
	return ports
}

// formatServicePorts renders a service's published ports as "host:target/proto".
func formatServicePorts(svc *api.Service) string {
	if svc.Spec.Endpoint == nil || len(svc.Spec.Endpoint.Ports) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(svc.Spec.Endpoint.Ports))
	for _, p := range svc.Spec.Endpoint.Ports {
		parts = append(parts, fmt.Sprintf("%d:%d/%s", p.PublishedPort, p.TargetPort, strings.ToLower(p.Protocol.String())))
	}
	return strings.Join(parts, ",")
}

func filterServices(services []*api.Service, filter string) []*api.Service {
	var result []*api.Service
	for _, svc := range services {
		match := true

		// Filter by name
		if strings.HasPrefix(filter, "name=") {
			name := strings.TrimPrefix(filter, "name=")
			if svc.Spec.Annotations.Name != name {
				match = false
			}
		}
		// Filter by ID prefix
		if strings.HasPrefix(filter, "id=") {
			idPrefix := strings.TrimPrefix(filter, "id=")
			if !strings.HasPrefix(svc.ID, idPrefix) {
				match = false
			}
		}

		if match {
			result = append(result, svc)
		}
	}
	return result
}

func inspectService(serviceID, format string, pretty bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	// Resolve by full ID, unique ID prefix, or name.
	serviceID, err = resolveServiceRef(ctx, client, serviceID)
	if err != nil {
		return err
	}

	resp, err := client.GetService(ctx, &api.GetServiceRequest{ServiceID: serviceID})
	if err != nil {
		return fmt.Errorf("failed to get service: %w", err)
	}

	if format == "json" {
		var data []byte
		if pretty {
			data, _ = json.MarshalIndent(resp.Service, "", "  ")
		} else {
			data, _ = json.Marshal(resp.Service)
		}
		fmt.Println(string(data))
	} else {
		svc := resp.Service
		fmt.Printf("ID: %s\n", svc.ID)
		fmt.Printf("Name: %s\n", svc.Spec.Annotations.Name)
		fmt.Printf("Version: %d\n", svc.Meta.Version.Index)
		if r := svc.Spec.GetReplicated(); r != nil {
			fmt.Printf("Replicas: %d\n", r.Replicas)
		} else if g := svc.Spec.GetGlobal(); g != nil {
			fmt.Printf("Mode: global\n")
		}
		if svc.Spec.Task.GetContainer() != nil {
			container := svc.Spec.Task.GetContainer()
			fmt.Printf("Image: %s\n", container.Image)
			if len(container.Command) > 0 {
				fmt.Printf("Command: %v\n", container.Command)
			}
			if len(container.Args) > 0 {
				fmt.Printf("Args: %v\n", container.Args)
			}
			if len(container.Env) > 0 {
				fmt.Printf("Environment:\n")
				for _, env := range container.Env {
					fmt.Printf("  %s\n", env)
				}
			}
		}
		if svc.Spec.Endpoint != nil && len(svc.Spec.Endpoint.Ports) > 0 {
			fmt.Printf("Published Ports:\n")
			for _, p := range svc.Spec.Endpoint.Ports {
				fmt.Printf("  %d: -> %d/%s (%s)\n", p.PublishedPort, p.TargetPort, strings.ToLower(p.Protocol.String()), strings.ToLower(p.PublishMode.String()))
			}
		}
		if svc.Endpoint != nil && len(svc.Endpoint.VirtualIPs) > 0 {
			fmt.Printf("Virtual IPs:\n")
			for _, v := range svc.Endpoint.VirtualIPs {
				fmt.Printf("  %s (%s)\n", v.Addr, v.NetworkID)
			}
		}
		if svc.Spec.Task.Resources != nil && svc.Spec.Task.Resources.Limits != nil {
			limits := svc.Spec.Task.Resources.Limits
			if limits.NanoCPUs > 0 {
				fmt.Printf("CPU Limit: %.2f cores\n", float64(limits.NanoCPUs)/1e9)
			}
			if limits.MemoryBytes > 0 {
				fmt.Printf("Memory Limit: %d bytes\n", limits.MemoryBytes)
			}
		}
		if len(svc.Spec.Annotations.Labels) > 0 {
			fmt.Printf("Labels:\n")
			for k, v := range svc.Spec.Annotations.Labels {
				fmt.Printf("  %s=%s\n", k, v)
			}
		}
	}

	return nil
}

func listServiceTasks(serviceID, format, filter string, quiet, noTrunc bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	// Resolve the reference (full ID, unique ID prefix, or name) to a full ID.
	actualServiceID, err := resolveServiceRef(ctx, client, serviceID)
	if err != nil {
		return err
	}

	// List tasks
	taskResp, err := client.ListTasks(ctx, &api.ListTasksRequest{})
	if err != nil {
		return fmt.Errorf("failed to list tasks: %w", err)
	}

	// Filter by service ID
	var tasks []*api.Task
	for _, task := range taskResp.Tasks {
		if task.ServiceID == actualServiceID {
			tasks = append(tasks, task)
		}
	}

	if len(tasks) == 0 {
		fmt.Printf("No tasks found for service %s\n", serviceID)
		return nil
	}

	// Apply additional filters
	if filter != "" {
		tasks = filterTasks(tasks, filter, "", "", true)
	}

	// Output based on format
	if format == "json" {
		data, _ := json.MarshalIndent(tasks, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	// Table format
	if quiet {
		for _, task := range tasks {
			fmt.Println(task.ID)
		}
		return nil
	}

	fmt.Printf("%-20s %-12s %-20s %s\n", "ID", "STATUS", "NODE", "IMAGE")
	fmt.Println(strings.Repeat("-", 70))
	for _, task := range tasks {
		taskID := task.ID
		if !noTrunc && len(taskID) > 12 {
			taskID = taskID[:12]
		}
		status := task.Status.State.String()
		nodeID := task.NodeID
		if !noTrunc && len(nodeID) > 12 {
			nodeID = nodeID[:12]
		}
		image := ""
		if task.Spec.GetContainer() != nil {
			image = task.Spec.GetContainer().Image
		}
		fmt.Printf("%-20s %-12s %-20s %s\n", taskID, status, nodeID, image)
	}
	fmt.Printf("\nTotal: %d task(s)\n", len(tasks))

	return nil
}

func createService(name, image string, replicas uint64, cpu float64, memory, disk string, env, command, args, labels, publish []string, publishMode string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	// Parse memory
	memoryBytes, err := parseMemory(memory)
	if err != nil {
		return fmt.Errorf("invalid memory value: %w", err)
	}

	mode, err := types.NormalizePublishMode(publishMode)
	if err != nil {
		return err
	}

	// The requested VM disk size is carried as a service label so the executor
	// can size the rootfs. A user-supplied label of the same name wins.
	svcLabels := parseLabels(labels)
	if disk != "" {
		if svcLabels == nil {
			svcLabels = make(map[string]string)
		}
		if _, ok := svcLabels[types.DiskSizeLabel]; !ok {
			svcLabels[types.DiskSizeLabel] = disk
		}
	}

	// Parse published ports. The mapping travels to the executor as a service
	// label (SwarmKit copies service labels onto each task) and is also
	// recorded on the spec's Endpoint so it surfaces in service ls/inspect.
	var endpointPorts []*api.PortConfig
	if len(publish) > 0 {
		published, err := types.ParsePublishSpec(publish)
		if err != nil {
			return fmt.Errorf("invalid --publish: %w", err)
		}
		if svcLabels == nil {
			svcLabels = make(map[string]string)
		}
		svcLabels[types.PublishLabel] = types.FormatPublishLabel(published)
		svcLabels[types.PublishModeLabel] = mode
		endpointPorts = buildEndpointPorts(published, mode)
	}

	// Build service spec
	spec := &api.ServiceSpec{
		Annotations: api.Annotations{
			Name:   name,
			Labels: svcLabels,
		},
		Task: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image:   image,
					Env:     env,
					Command: command,
					Args:    args,
				},
			},
		},
		Mode: &api.ServiceSpec_Replicated{
			Replicated: &api.ReplicatedService{
				Replicas: replicas,
			},
		},
	}

	if len(endpointPorts) > 0 {
		spec.Endpoint = &api.EndpointSpec{Ports: endpointPorts}
	}

	// Set resource limits if specified
	if cpu > 0 || memoryBytes > 0 {
		spec.Task.Resources = &api.ResourceRequirements{
			Limits: &api.Resources{},
		}
		if cpu > 0 {
			spec.Task.Resources.Limits.NanoCPUs = int64(cpu * 1e9)
		}
		if memoryBytes > 0 {
			spec.Task.Resources.Limits.MemoryBytes = memoryBytes
		}
	}

	// Create service
	resp, err := client.CreateService(ctx, &api.CreateServiceRequest{
		Spec: spec,
	})
	if err != nil {
		return fmt.Errorf("failed to create service: %w", err)
	}

	fmt.Printf("Service %s created with ID: %s\n", name, resp.Service.ID)
	fmt.Printf("Replicas: %d\n", replicas)
	fmt.Printf("Image: %s\n", image)

	return nil
}

func updateService(serviceID string, replicas uint64, replicasSet bool, cpuLimit float64, memoryLimit string, image string, env, envRemove []string, publishMode string, publishModeSet bool, force bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	// Resolve the reference (full ID, unique ID prefix, or name) and fetch it.
	serviceID, err = resolveServiceRef(ctx, client, serviceID)
	if err != nil {
		return err
	}
	getResp, err := client.GetService(ctx, &api.GetServiceRequest{ServiceID: serviceID})
	if err != nil {
		return fmt.Errorf("failed to get service: %w", err)
	}
	svc := getResp.Service

	// Parse memory
	memoryBytes, err := parseMemory(memoryLimit)
	if err != nil {
		return fmt.Errorf("invalid memory value: %w", err)
	}

	// Update spec
	spec := svc.Spec.Copy()

	// Update replicas when the caller explicitly provided a count. This keeps
	// `service update` without --replicas a no-op while allowing an explicit
	// `--replicas 0` (scale to zero).
	if replicasSet {
		if r := spec.GetReplicated(); r != nil {
			r.Replicas = replicas
		} else {
			spec.Mode = &api.ServiceSpec_Replicated{
				Replicated: &api.ReplicatedService{
					Replicas: replicas,
				},
			}
		}
	}

	// Update container spec
	container := spec.Task.GetContainer()
	if container != nil {
		// Update image if specified
		if image != "" {
			container.Image = image
		}

		// Update environment variables
		if len(env) > 0 || len(envRemove) > 0 {
			// Remove specified env vars
			if len(envRemove) > 0 {
				removeSet := make(map[string]bool)
				for _, key := range envRemove {
					removeSet[key] = true
				}
				var newEnv []string
				for _, e := range container.Env {
					parts := strings.SplitN(e, "=", 2)
					if !removeSet[parts[0]] {
						newEnv = append(newEnv, e)
					}
				}
				container.Env = newEnv
			}

			// Add/update env vars
			if len(env) > 0 {
				envMap := make(map[string]string)
				for _, e := range container.Env {
					parts := strings.SplitN(e, "=", 2)
					if len(parts) == 2 {
						envMap[parts[0]] = parts[1]
					}
				}
				for _, e := range env {
					parts := strings.SplitN(e, "=", 2)
					if len(parts) == 2 {
						envMap[parts[0]] = parts[1]
					}
				}
				container.Env = make([]string, 0, len(envMap))
				for k, v := range envMap {
					container.Env = append(container.Env, k+"="+v)
				}
			}
		}
	}

	// Update resource limits
	if cpuLimit > 0 || memoryBytes > 0 {
		if spec.Task.Resources == nil {
			spec.Task.Resources = &api.ResourceRequirements{}
		}
		if spec.Task.Resources.Limits == nil {
			spec.Task.Resources.Limits = &api.Resources{}
		}
		if cpuLimit > 0 {
			spec.Task.Resources.Limits.NanoCPUs = int64(cpuLimit * 1e9)
		}
		if memoryBytes > 0 {
			spec.Task.Resources.Limits.MemoryBytes = memoryBytes
		}
	}

	// Change the publish mode of the service's existing published ports. The
	// tasks are recreated on the spec change, so host-mode rules are released
	// and ingress rules reconcile accordingly.
	if publishModeSet {
		mode, err := types.NormalizePublishMode(publishMode)
		if err != nil {
			return err
		}
		if spec.Endpoint == nil || len(spec.Endpoint.Ports) == 0 {
			return fmt.Errorf("service %q has no published ports to change", svc.Spec.Annotations.Name)
		}
		apiMode := api.PublishModeIngress
		if mode == types.PublishModeHost {
			apiMode = api.PublishModeHost
		}
		for _, p := range spec.Endpoint.Ports {
			p.PublishMode = apiMode
		}
		if spec.Annotations.Labels == nil {
			spec.Annotations.Labels = make(map[string]string)
		}
		spec.Annotations.Labels[types.PublishModeLabel] = mode
	}

	// Force update if requested
	if force {
		spec.Task.ForceUpdate++
	}

	// Update service
	_, err = client.UpdateService(ctx, &api.UpdateServiceRequest{
		ServiceID:      svc.ID,
		ServiceVersion: &svc.Meta.Version,
		Spec:           spec,
	})
	if err != nil {
		return fmt.Errorf("failed to update service: %w", err)
	}

	fmt.Printf("Service %s updated\n", svc.Spec.Annotations.Name)
	return nil
}

func scaleService(serviceID, replicasStr string) error {
	replicas, err := strconv.ParseUint(strings.TrimSpace(replicasStr), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid replica count %q: must be a non-negative integer", replicasStr)
	}

	// Scale always applies the value, including 0 (scale to zero).
	return updateService(serviceID, replicas, true, 0, "", "", nil, nil, "", false, false)
}

func removeService(serviceID string, force bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	// Resolve the reference (full ID, unique ID prefix, or name).
	actualID, err := resolveServiceRef(ctx, client, serviceID)
	if err != nil {
		return err
	}

	// Best-effort friendly name for the success message.
	displayName := serviceID
	if getResp, gerr := client.GetService(ctx, &api.GetServiceRequest{ServiceID: actualID}); gerr == nil {
		displayName = getResp.Service.Spec.Annotations.Name
	}

	// Remove service
	_, err = client.RemoveService(ctx, &api.RemoveServiceRequest{
		ServiceID: actualID,
	})
	if err != nil {
		return fmt.Errorf("failed to remove service: %w", err)
	}

	fmt.Printf("Service %s removed\n", displayName)
	return nil
}

// filterTasks is a helper to filter tasks (shared with cmd_task.go)
func filterTasks(tasks []*api.Task, filter string, node, serviceID string, all bool) []*api.Task {
	var result []*api.Task
	for _, task := range tasks {
		match := true

		// Filter by state
		if !all && task.Status.State == api.TaskStateRunning {
			// Show all tasks when --all is specified
			match = true
		}

		// Filter by node
		if node != "" && task.NodeID != node {
			match = false
		}

		// Filter by service
		if serviceID != "" && task.ServiceID != serviceID {
			match = false
		}

		// Filter by custom filter string
		if filter != "" {
			if strings.HasPrefix(filter, "state=") {
				stateStr := strings.TrimPrefix(filter, "state=")
				if task.Status.State.String() != stateStr {
					match = false
				}
			}
			if strings.HasPrefix(filter, "node=") {
				nodeStr := strings.TrimPrefix(filter, "node=")
				if !strings.HasPrefix(task.NodeID, nodeStr) {
					match = false
				}
			}
		}

		if match {
			result = append(result, task)
		}
	}
	return result
}
