package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
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
		format     string
		filter     string
		quiet      bool
		noTrunc    bool
		healthPort int
		noHealth   bool
	)

	cmd := &cobra.Command{
		Use:   "ps <service-id>",
		Short: "List tasks of a service",
		Long: `List all tasks belonging to a service.

The HEALTH column shows the daemon's live healthcheck state for each task, read
from the node that owns it. A task the owning node has not published health for
falls back to the health-gated task state; a node that cannot be reached shows
"-".`,
		Example: `  swarmcracker service ps my-service
  swarmcracker service ps --format json my-service
  swarmcracker service ps --no-health my-service`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listServiceTasks(args[0], format, filter, quiet, noTrunc, clusterHealthOptions{Port: healthPort, Disabled: noHealth})
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")
	cmd.Flags().StringVar(&filter, "filter", "", "Filter output (e.g., 'state=running')")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only display IDs")
	cmd.Flags().BoolVar(&noTrunc, "no-trunc", false, "Don't truncate output")
	cmd.Flags().IntVar(&healthPort, "health-port", defaultHealthPort, "Port of each node's health server, used to fetch live health")
	cmd.Flags().BoolVar(&noHealth, "no-health", false, "Skip the live-health lookup; derive HEALTH from the task state only")

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
		balloonMib  int
		cpuset      string
		golden      string
		publish     []string
		publishMode string
		mounts      []string
		volumes     []string
		networks    []string
		secrets     []string
		configs     []string
		mode        string

		// withRegistryAuth is --with-registry-auth (#44): send the manager's
		// credential for --image to the nodes.
		withRegistryAuth bool

		hostname string
		dns      []string
		user     string
		capAdd   []string
		capDrop  []string
		readOnly bool

		healthCmd         string
		healthInterval    string
		healthTimeout     string
		healthRetries     int
		healthStartPeriod string

		constraints    []string
		placementPrefs []string

		restartCondition   string
		restartDelay       string
		restartMaxAttempts uint64
		restartWindow      string

		updateOrder           string
		updateParallelism     uint64
		updateDelay           string
		updateFailureAction   string
		updateMonitor         string
		updateMaxFailureRatio float64

		rollbackOrder           string
		rollbackParallelism     uint64
		rollbackDelay           string
		rollbackFailureAction   string
		rollbackMonitor         string
		rollbackMaxFailureRatio float64
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
			// A golden service boots a prebuilt image and skips image
			// preparation, so it is never pulled from a registry. Accepting
			// --with-registry-auth here would resolve a credential for the
			// placeholder image and silently send a payload nothing consumes.
			if golden != "" && withRegistryAuth {
				return fmt.Errorf("--with-registry-auth cannot be used with --golden: golden services are not pulled from a registry")
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
			return createService(serviceCreateOptions{
				name:               name,
				image:              image,
				withRegistryAuth:   withRegistryAuth,
				replicas:           replicas,
				cpu:                cpu,
				memory:             memory,
				disk:               disk,
				balloonMib:         balloonMib,
				balloonSet:         cmd.Flags().Changed("balloon-mib"),
				cpuset:             cpuset,
				cpusetSet:          cmd.Flags().Changed("cpuset"),
				env:                env,
				command:            command,
				args:               args,
				labels:             labels,
				publish:            publish,
				publishMode:        publishMode,
				mounts:             mounts,
				volumes:            volumes,
				networks:           networks,
				secrets:            secrets,
				configs:            configs,
				mode:               mode,
				hostname:           hostname,
				dns:                dns,
				user:               user,
				capAdd:             capAdd,
				capDrop:            capDrop,
				readOnly:           readOnly,
				health:             healthOptionsFromFlags(cmd, healthCmd, healthInterval, healthTimeout, healthRetries, healthStartPeriod),
				constraints:        constraints,
				placementPrefs:     placementPrefs,
				restartSet:         anyFlagChanged(cmd, "restart-condition", "restart-delay", "restart-max-attempts", "restart-window"),
				restartCond:        restartCondition,
				restartDelay:       restartDelay,
				restartAttempts:    restartMaxAttempts,
				restartWindow:      restartWindow,
				updateSet:          anyFlagChanged(cmd, "update-order", "update-parallelism", "update-delay", "update-failure-action", "update-monitor", "update-max-failure-ratio"),
				updateOrder:        updateOrder,
				updateParallel:     updateParallelism,
				updateDelay:        updateDelay,
				updateFailAction:   updateFailureAction,
				updateMonitor:      updateMonitor,
				updateMaxRatio:     updateMaxFailureRatio,
				rollbackSet:        anyFlagChanged(cmd, "rollback-order", "rollback-parallelism", "rollback-delay", "rollback-failure-action", "rollback-monitor", "rollback-max-failure-ratio"),
				rollbackOrder:      rollbackOrder,
				rollbackParallel:   rollbackParallelism,
				rollbackDelay:      rollbackDelay,
				rollbackFailAction: rollbackFailureAction,
				rollbackMonitor:    rollbackMonitor,
				rollbackMaxRatio:   rollbackMaxFailureRatio,
			})
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Service name (required)")
	cmd.Flags().StringVar(&image, "image", "", "Container image (or use --golden)")
	cmd.Flags().StringVar(&golden, "golden", "", "Boot a prebuilt golden image (e.g., ubuntu-24.04-docker@1.0.0)")
	cmd.Flags().BoolVar(&withRegistryAuth, "with-registry-auth", false, "Send registry authentication details for --image to the nodes (resolved from the manager's docker config)")
	cmd.Flags().Uint64Var(&replicas, "replicas", 1, "Number of replicas")
	cmd.Flags().Float64Var(&cpu, "cpu", 0, "CPU limit (cores, e.g., 1.5)")
	cmd.Flags().StringVar(&memory, "memory", "", "Memory limit (e.g., 512M, 1G)")
	cmd.Flags().StringVar(&disk, "disk", "", "Minimum rootfs disk size for the VM (e.g., 10G)")
	cmd.Flags().IntVar(&balloonMib, "balloon-mib", 0, "Memory-balloon target in MiB, must be less than the memory limit (the guest can reclaim it via deflate-on-OOM)")
	cmd.Flags().StringVar(&cpuset, "cpuset", "", "Pin the VM to these host CPUs (Linux cpulist: 0, 0-3, 0-3,8); the host must have those CPUs online")
	cmd.Flags().StringArrayVarP(&env, "env", "e", nil, "Environment variables (e.g., KEY=value)")
	cmd.Flags().StringArrayVar(&command, "command", nil, "Override default container command")
	cmd.Flags().StringArrayVar(&args, "args", nil, "Container arguments")
	cmd.Flags().StringArrayVarP(&labels, "label", "l", nil, "Service labels (e.g., key=value)")
	cmd.Flags().StringArrayVarP(&publish, "publish", "p", nil, "Publish a host port to the guest ([host:]container[/tcp|udp], e.g. 8080:80)")
	cmd.Flags().StringVar(&publishMode, "publish-mode", types.PublishModeIngress, "Port publish mode: ingress (cluster load-balanced) or host (per-replica host port)")
	cmd.Flags().StringArrayVar(&mounts, "mount", nil, "Mount a volume or host path: type=volume|bind,source=<src>,target=<path>[,readonly] (repeatable)")
	cmd.Flags().StringArrayVarP(&volumes, "volume", "v", nil, "Mount a volume or host path: <src>:<dst>[:ro|rw] (repeatable)")
	cmd.Flags().StringArrayVar(&networks, "network", nil, "Attach the service to a user-defined network (repeatable)")
	cmd.Flags().StringArrayVar(&secrets, "secret", nil, "Grant access to a secret: [src=]NAME[,target=PATH][,mode=0400][,uid=N][,gid=N] (repeatable)")
	cmd.Flags().StringArrayVar(&configs, "config", nil, "Grant access to a config: [src=]NAME[,target=PATH][,mode=0444][,uid=N][,gid=N] (repeatable)")
	cmd.Flags().StringVar(&mode, "mode", modeReplicated, "Service mode: replicated or global")
	cmd.Flags().StringVar(&hostname, "hostname", "", "Guest VM hostname")
	cmd.Flags().StringArrayVar(&dns, "dns", nil, "DNS nameserver for the guest (repeatable)")
	cmd.Flags().StringVar(&user, "user", "", "Run the workload as user[:group] (not supported)")
	cmd.Flags().StringArrayVar(&capAdd, "cap-add", nil, "Add a Linux capability (not supported)")
	cmd.Flags().StringArrayVar(&capDrop, "cap-drop", nil, "Drop a Linux capability (not supported)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "Mount the root filesystem read-only (not supported)")
	cmd.Flags().StringVar(&healthCmd, "health-cmd", "", "Container healthcheck command, run via the guest shell (enables the healthcheck)")
	cmd.Flags().StringVar(&healthInterval, "health-interval", "", "Time between healthcheck runs (default 30s)")
	cmd.Flags().StringVar(&healthTimeout, "health-timeout", "", "Time to wait for a healthcheck to complete (default 30s)")
	cmd.Flags().IntVar(&healthRetries, "health-retries", 0, "Consecutive failures before a task is unhealthy (default 3)")
	cmd.Flags().StringVar(&healthStartPeriod, "health-start-period", "", "Grace period before healthcheck failures count (default 0s)")
	cmd.Flags().StringArrayVar(&constraints, "constraint", nil, "Placement constraint, key==value or key!=value (repeatable, e.g. node.hostname==worker1)")
	cmd.Flags().StringArrayVar(&placementPrefs, "placement-pref", nil, "Placement preference, spread=<key> (repeatable, e.g. spread=node.labels.zone)")
	cmd.Flags().StringVar(&restartCondition, "restart-condition", "", "Restart condition: none, on-failure or any (default any)")
	cmd.Flags().StringVar(&restartDelay, "restart-delay", "", "Delay between restart attempts (e.g. 5s)")
	cmd.Flags().Uint64Var(&restartMaxAttempts, "restart-max-attempts", 0, "Maximum restart attempts before giving up (0 = unlimited)")
	cmd.Flags().StringVar(&restartWindow, "restart-window", "", "Time window used to evaluate the restart policy (e.g. 1h)")
	cmd.Flags().StringVar(&updateOrder, "update-order", "", "Update order: stop-first or start-first")
	cmd.Flags().Uint64Var(&updateParallelism, "update-parallelism", 0, "Tasks updated in parallel (0 = unlimited)")
	cmd.Flags().StringVar(&updateDelay, "update-delay", "", "Delay between updates (e.g. 10s)")
	cmd.Flags().StringVar(&updateFailureAction, "update-failure-action", "", "Action on update failure: pause, continue or rollback")
	cmd.Flags().StringVar(&updateMonitor, "update-monitor", "", "Duration to monitor a new task for failure (e.g. 30s)")
	cmd.Flags().Float64Var(&updateMaxFailureRatio, "update-max-failure-ratio", 0, "Fraction of tasks that may fail before the failure action (0-1)")
	cmd.Flags().StringVar(&rollbackOrder, "rollback-order", "", "Rollback order: stop-first or start-first")
	cmd.Flags().Uint64Var(&rollbackParallelism, "rollback-parallelism", 0, "Tasks rolled back in parallel (0 = unlimited)")
	cmd.Flags().StringVar(&rollbackDelay, "rollback-delay", "", "Delay between rollback steps (e.g. 10s)")
	cmd.Flags().StringVar(&rollbackFailureAction, "rollback-failure-action", "", "Action on rollback failure: pause or continue")
	cmd.Flags().StringVar(&rollbackMonitor, "rollback-monitor", "", "Duration to monitor a rolled-back task for failure")
	cmd.Flags().Float64Var(&rollbackMaxFailureRatio, "rollback-max-failure-ratio", 0, "Fraction of tasks that may fail during rollback (0-1)")

	cobra.CheckErr(cmd.MarkFlagRequired("name"))

	return cmd
}

// newServiceUpdateCommand updates a service
func newServiceUpdateCommand() *cobra.Command {
	var (
		replicas    uint64
		cpuLimit    float64
		memoryLimit string
		balloonMib  int
		cpuset      string
		image       string
		env         []string
		envRemove   []string
		publishMode string
		force       bool
		rollback    bool

		withRegistryAuth bool

		mounts   []string
		volumes  []string
		networks []string
		secrets  []string
		configs  []string

		hostname string
		dns      []string
		user     string
		capAdd   []string
		capDrop  []string
		readOnly bool

		healthCmd         string
		healthInterval    string
		healthTimeout     string
		healthRetries     int
		healthStartPeriod string

		mode           string
		constraints    []string
		placementPrefs []string

		restartCondition   string
		restartDelay       string
		restartMaxAttempts uint64
		restartWindow      string

		updateOrder           string
		updateParallelism     uint64
		updateDelay           string
		updateFailureAction   string
		updateMonitor         string
		updateMaxFailureRatio float64

		rollbackOrder           string
		rollbackParallelism     uint64
		rollbackDelay           string
		rollbackFailureAction   string
		rollbackMonitor         string
		rollbackMaxFailureRatio float64
	)

	cmd := &cobra.Command{
		Use:   "update <service-id>",
		Short: "Update a service",
		Long: `Update an existing service's configuration.

Supports updating replicas, resource limits, image, environment variables,
placement, restart policy, update/rollback configuration and mode.`,
		Example: `  swarmcracker service update my-service --replicas 5
  swarmcracker service update my-service --constraint node.hostname==worker1
  swarmcracker service update my-service --update-order start-first --replicas 4
  swarmcracker service update my-service --rollback`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return updateService(args[0], serviceUpdateOptions{
				replicas:       replicas,
				replicasSet:    cmd.Flags().Changed("replicas"),
				cpuLimit:       cpuLimit,
				memoryLimit:    memoryLimit,
				image:          image,
				env:            env,
				envRemove:      envRemove,
				publishMode:    publishMode,
				publishModeSet: cmd.Flags().Changed("publish-mode"),
				force:          force,
				rollbackAction: rollback,

				// --with-registry-auth (#44) is tri-state: the set bit
				// distinguishes "absent" (preserve) from an explicit
				// --with-registry-auth=false (clear). updateService resolves
				// the payload before any RPC.
				withRegistryAuth:    withRegistryAuth,
				withRegistryAuthSet: anyFlagChanged(cmd, "with-registry-auth"),
				balloonMib:          balloonMib,
				balloonSet:          anyFlagChanged(cmd, "balloon-mib"),
				cpuset:              cpuset,
				cpusetSet:           anyFlagChanged(cmd, "cpuset"),
				mounts:              mounts,
				volumes:             volumes,
				mountsSet:           anyFlagChanged(cmd, "mount", "volume"),
				networks:            networks,
				networksSet:         anyFlagChanged(cmd, "network"),
				secrets:             secrets,
				secretsSet:          anyFlagChanged(cmd, "secret"),
				configs:             configs,
				configsSet:          anyFlagChanged(cmd, "config"),

				hostname:    hostname,
				dns:         dns,
				hostnameSet: cmd.Flags().Changed("hostname"),
				dnsSet:      cmd.Flags().Changed("dns"),
				user:        user,
				capAdd:      capAdd,
				capDrop:     capDrop,
				readOnly:    readOnly,

				health: healthOptionsFromFlags(cmd, healthCmd, healthInterval, healthTimeout, healthRetries, healthStartPeriod),

				modeSet:        cmd.Flags().Changed("mode"),
				mode:           mode,
				constraints:    constraints,
				placementPrefs: placementPrefs,
				placementSet:   anyFlagChanged(cmd, "constraint", "placement-pref"),

				restartSet:      anyFlagChanged(cmd, "restart-condition", "restart-delay", "restart-max-attempts", "restart-window"),
				restartCond:     restartCondition,
				restartDelay:    restartDelay,
				restartAttempts: restartMaxAttempts,
				restartWindow:   restartWindow,

				updateSet:        anyFlagChanged(cmd, "update-order", "update-parallelism", "update-delay", "update-failure-action", "update-monitor", "update-max-failure-ratio"),
				updateOrder:      updateOrder,
				updateParallel:   updateParallelism,
				updateDelay:      updateDelay,
				updateFailAction: updateFailureAction,
				updateMonitor:    updateMonitor,
				updateMaxRatio:   updateMaxFailureRatio,

				rollbackSet:        anyFlagChanged(cmd, "rollback-order", "rollback-parallelism", "rollback-delay", "rollback-failure-action", "rollback-monitor", "rollback-max-failure-ratio"),
				rollbackOrder:      rollbackOrder,
				rollbackParallel:   rollbackParallelism,
				rollbackDelay:      rollbackDelay,
				rollbackFailAction: rollbackFailureAction,
				rollbackMonitor:    rollbackMonitor,
				rollbackMaxRatio:   rollbackMaxFailureRatio,
			})
		},
	}

	cmd.Flags().Uint64Var(&replicas, "replicas", 0, "Number of replicas (0 = no change)")
	cmd.Flags().Float64Var(&cpuLimit, "cpu-limit", 0, "CPU limit in cores (0 = no change)")
	cmd.Flags().StringVar(&memoryLimit, "memory-limit", "", "Memory limit (e.g., 512M, 1G)")
	cmd.Flags().IntVar(&balloonMib, "balloon-mib", 0, "Memory-balloon target in MiB; must be less than the memory limit (0 removes the balloon)")
	cmd.Flags().StringVar(&cpuset, "cpuset", "", "Pin the VM to these host CPUs (Linux cpulist: 0, 0-3, 0-3,8); the host must have those CPUs online, and an empty value removes the pin")
	cmd.Flags().StringVar(&image, "image", "", "New container image")
	cmd.Flags().StringArrayVar(&env, "env-add", nil, "Add environment variable (e.g., KEY=value)")
	cmd.Flags().StringArrayVar(&envRemove, "env-rm", nil, "Remove environment variable")
	cmd.Flags().StringVar(&publishMode, "publish-mode", "", "Change the publish mode of existing published ports (ingress|host)")
	cmd.Flags().StringArrayVar(&mounts, "mount", nil, "Replace mounts: type=volume|bind,source=<src>,target=<path>[,readonly] (repeatable)")
	cmd.Flags().StringArrayVarP(&volumes, "volume", "v", nil, "Replace mounts: <src>:<dst>[:ro|rw] (repeatable)")
	cmd.Flags().StringArrayVar(&networks, "network", nil, "Replace the service's networks (repeatable)")
	cmd.Flags().StringArrayVar(&secrets, "secret", nil, "Replace the service's secrets (repeatable)")
	cmd.Flags().StringArrayVar(&configs, "config", nil, "Replace the service's configs (repeatable)")
	cmd.Flags().BoolVar(&rollback, "rollback", false, "Roll back to the service's previous spec")
	cmd.Flags().StringVar(&mode, "mode", "", "Change the service mode: replicated or global")
	cmd.Flags().StringVar(&hostname, "hostname", "", "Set the guest VM hostname")
	cmd.Flags().StringArrayVar(&dns, "dns", nil, "Set the guest DNS nameservers (repeatable)")
	cmd.Flags().StringVar(&user, "user", "", "Run the workload as user[:group] (not supported)")
	cmd.Flags().StringArrayVar(&capAdd, "cap-add", nil, "Add a Linux capability (not supported)")
	cmd.Flags().StringArrayVar(&capDrop, "cap-drop", nil, "Drop a Linux capability (not supported)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "Mount the root filesystem read-only (not supported)")
	cmd.Flags().StringVar(&healthCmd, "health-cmd", "", "Container healthcheck command, run via the guest shell (enables the healthcheck)")
	cmd.Flags().StringVar(&healthInterval, "health-interval", "", "Time between healthcheck runs (default 30s)")
	cmd.Flags().StringVar(&healthTimeout, "health-timeout", "", "Time to wait for a healthcheck to complete (default 30s)")
	cmd.Flags().IntVar(&healthRetries, "health-retries", 0, "Consecutive failures before a task is unhealthy (default 3)")
	cmd.Flags().StringVar(&healthStartPeriod, "health-start-period", "", "Grace period before healthcheck failures count (default 0s)")
	cmd.Flags().StringArrayVar(&constraints, "constraint", nil, "Replace placement constraints (key==value or key!=value; repeatable)")
	cmd.Flags().StringArrayVar(&placementPrefs, "placement-pref", nil, "Replace placement preferences (spread=<key>; repeatable)")
	cmd.Flags().StringVar(&restartCondition, "restart-condition", "", "Restart condition: none, on-failure or any")
	cmd.Flags().StringVar(&restartDelay, "restart-delay", "", "Delay between restart attempts (e.g. 5s)")
	cmd.Flags().Uint64Var(&restartMaxAttempts, "restart-max-attempts", 0, "Maximum restart attempts before giving up (0 = unlimited)")
	cmd.Flags().StringVar(&restartWindow, "restart-window", "", "Time window used to evaluate the restart policy (e.g. 1h)")
	cmd.Flags().StringVar(&updateOrder, "update-order", "", "Update order: stop-first or start-first")
	cmd.Flags().Uint64Var(&updateParallelism, "update-parallelism", 0, "Tasks updated in parallel (0 = unlimited)")
	cmd.Flags().StringVar(&updateDelay, "update-delay", "", "Delay between updates (e.g. 10s)")
	cmd.Flags().StringVar(&updateFailureAction, "update-failure-action", "", "Action on update failure: pause, continue or rollback")
	cmd.Flags().StringVar(&updateMonitor, "update-monitor", "", "Duration to monitor a new task for failure (e.g. 30s)")
	cmd.Flags().Float64Var(&updateMaxFailureRatio, "update-max-failure-ratio", 0, "Fraction of tasks that may fail before the failure action (0-1)")
	cmd.Flags().StringVar(&rollbackOrder, "rollback-order", "", "Rollback order: stop-first or start-first")
	cmd.Flags().Uint64Var(&rollbackParallelism, "rollback-parallelism", 0, "Tasks rolled back in parallel (0 = unlimited)")
	cmd.Flags().StringVar(&rollbackDelay, "rollback-delay", "", "Delay between rollback steps (e.g. 10s)")
	cmd.Flags().StringVar(&rollbackFailureAction, "rollback-failure-action", "", "Action on rollback failure: pause or continue")
	cmd.Flags().StringVar(&rollbackMonitor, "rollback-monitor", "", "Duration to monitor a rolled-back task for failure")
	cmd.Flags().Float64Var(&rollbackMaxFailureRatio, "rollback-max-failure-ratio", 0, "Fraction of tasks that may fail during rollback (0-1)")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force update even if no changes detected")
	cmd.Flags().BoolVar(&withRegistryAuth, "with-registry-auth", false, "Send registry authentication details for --image to the nodes (resolved from the manager's docker config); pass --with-registry-auth=false to clear them")

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

// anyFlagChanged reports whether the user set any of the named flags, so an
// update only overrides a spec section that was actually requested.
func anyFlagChanged(cmd *cobra.Command, names ...string) bool {
	for _, n := range names {
		if cmd.Flags().Changed(n) {
			return true
		}
	}
	return false
}

// healthOptionsFromFlags reads the five --health-* flags together with their
// per-flag Changed bits. The bits let buildHealthcheck tell an explicit zero
// (--health-retries 0, --health-interval 0s) from an unset flag, so an explicit
// zero is rejected instead of silently replaced by a Docker default.
func healthOptionsFromFlags(cmd *cobra.Command, command, interval, timeout string, retries int, startPeriod string) healthOptions {
	return healthOptions{
		cmd:            command,
		cmdSet:         cmd.Flags().Changed("health-cmd"),
		interval:       interval,
		intervalSet:    cmd.Flags().Changed("health-interval"),
		timeout:        timeout,
		timeoutSet:     cmd.Flags().Changed("health-timeout"),
		retries:        retries,
		retriesSet:     cmd.Flags().Changed("health-retries"),
		startPeriod:    startPeriod,
		startPeriodSet: cmd.Flags().Changed("health-start-period"),
	}
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

	return listServicesWithClient(ctx, client, os.Stdout, format, filter, quiet)
}

// listServicesWithClient is the body of `service ls` with the control client and
// output writer injected, so the table/JSON rendering — including the credential
// redaction on the JSON path — is unit-testable without a cluster.
func listServicesWithClient(ctx context.Context, client api.ControlClient, w io.Writer, format, filter string, quiet bool) error {
	resp, err := client.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}

	if len(resp.Services) == 0 {
		fmt.Fprintln(w, "No services found")
		return nil
	}

	// Apply filter if specified
	services := resp.Services
	if filter != "" {
		services = filterServices(services, filter)
	}

	// Output based on format
	if format == "json" {
		// marshalServicesJSON masks the registry credential each service may
		// carry, so it can never reach stdout.
		data, _ := marshalServicesJSON(services)
		fmt.Fprintln(w, string(data))
		return nil
	}

	// Table format
	if quiet {
		for _, svc := range services {
			fmt.Fprintln(w, svc.ID)
		}
		return nil
	}

	fmt.Fprintf(w, "%-20s %-20s %-10s %-24s %s\n", "ID", "NAME", "REPLICAS", "PORTS", "IMAGE")
	fmt.Fprintln(w, strings.Repeat("-", 100))
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
		fmt.Fprintf(w, "%-20s %-20s %-10s %-24s %s\n", id, name, replicas, formatServicePorts(svc), image)
	}
	fmt.Fprintf(w, "\nTotal: %d service(s)\n", len(services))

	return nil
}

// resolveNetworkTargets resolves --network names/IDs to full network IDs.
// A service may join several networks (one NIC per network, in order); a
// network listed more than once is attached only once.
func resolveNetworkTargets(ctx context.Context, client api.ControlClient, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		id, err := resolveNetworkRef(ctx, client, n)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

// formatGrant renders a secret/config grant as "name -> target" (never the
// value, which is not present in the service spec).
func formatGrant(name string, ft *api.FileTarget) string {
	if ft == nil || ft.Name == "" {
		return name
	}
	return fmt.Sprintf("%s -> %s", name, ft.Name)
}

// buildSecretReferences resolves --secret values (name/ID + target/mode) to
// SwarmKit secret references.
func buildSecretReferences(ctx context.Context, client api.ControlClient, specs []string) ([]*api.SecretReference, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	list, err := client.ListSecrets(ctx, &api.ListSecretsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}
	byName := make(map[string]*api.Secret, len(list.Secrets))
	byID := make(map[string]*api.Secret, len(list.Secrets))
	for _, s := range list.Secrets {
		byName[s.Spec.Annotations.Name] = s
		byID[s.ID] = s
	}

	refs := make([]*api.SecretReference, 0, len(specs))
	for _, raw := range specs {
		ps, err := parseSecretSpec(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid --secret %q: %w", raw, err)
		}
		sec := byName[ps.Source]
		if sec == nil {
			sec = byID[ps.Source]
		}
		if sec == nil {
			return nil, fmt.Errorf("secret %q not found", ps.Source)
		}
		target := ps.Target
		if target == "" {
			target = "/run/secrets/" + sec.Spec.Annotations.Name
		}
		refs = append(refs, &api.SecretReference{
			SecretID:   sec.ID,
			SecretName: sec.Spec.Annotations.Name,
			Target: &api.SecretReference_File{
				File: &api.FileTarget{Name: target, Mode: ps.Mode, UID: ps.UID, GID: ps.GID},
			},
		})
	}
	return refs, nil
}

// buildConfigReferences resolves --config values to SwarmKit config references.
func buildConfigReferences(ctx context.Context, client api.ControlClient, specs []string) ([]*api.ConfigReference, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	list, err := client.ListConfigs(ctx, &api.ListConfigsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list configs: %w", err)
	}
	byName := make(map[string]*api.Config, len(list.Configs))
	byID := make(map[string]*api.Config, len(list.Configs))
	for _, c := range list.Configs {
		byName[c.Spec.Annotations.Name] = c
		byID[c.ID] = c
	}

	refs := make([]*api.ConfigReference, 0, len(specs))
	for _, raw := range specs {
		ps, err := parseSecretSpec(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid --config %q: %w", raw, err)
		}
		cfg := byName[ps.Source]
		if cfg == nil {
			cfg = byID[ps.Source]
		}
		if cfg == nil {
			return nil, fmt.Errorf("config %q not found", ps.Source)
		}
		target := ps.Target
		if target == "" {
			target = "/config/" + cfg.Spec.Annotations.Name
		}
		refs = append(refs, &api.ConfigReference{
			ConfigID:   cfg.ID,
			ConfigName: cfg.Spec.Annotations.Name,
			Target: &api.ConfigReference_File{
				File: &api.FileTarget{Name: target, Mode: ps.Mode, UID: ps.UID, GID: ps.GID},
			},
		})
	}
	return refs, nil
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

	return inspectServiceWithClient(ctx, client, os.Stdout, serviceID, format, pretty)
}

// inspectServiceWithClient is the body of `service inspect` with the control
// client and output writer injected, so the rendering — including the credential
// redaction on the JSON path — is unit-testable without a cluster.
func inspectServiceWithClient(ctx context.Context, client api.ControlClient, w io.Writer, serviceID, format string, pretty bool) error {
	// Resolve by full ID, unique ID prefix, or name.
	resolvedID, err := resolveServiceRef(ctx, client, serviceID)
	if err != nil {
		return err
	}

	resp, err := client.GetService(ctx, &api.GetServiceRequest{ServiceID: resolvedID})
	if err != nil {
		return fmt.Errorf("failed to get service: %w", err)
	}

	if format == "json" {
		// marshalServiceJSON masks the registry credential the service carries,
		// so it can never reach stdout.
		data, _ := marshalServiceJSON(resp.Service, pretty)
		fmt.Fprintln(w, string(data))
		return nil
	}

	writeServiceText(w, resp.Service)
	return nil
}

// writeServiceText renders the human-readable `service inspect` output.
func writeServiceText(w io.Writer, svc *api.Service) {
	fmt.Fprintf(w, "ID: %s\n", svc.ID)
	fmt.Fprintf(w, "Name: %s\n", svc.Spec.Annotations.Name)
	fmt.Fprintf(w, "Version: %d\n", svc.Meta.Version.Index)
	if r := svc.Spec.GetReplicated(); r != nil {
		fmt.Fprintf(w, "Replicas: %d\n", r.Replicas)
	} else if g := svc.Spec.GetGlobal(); g != nil {
		fmt.Fprintf(w, "Mode: global\n")
	}
	if svc.Spec.Task.GetContainer() != nil {
		container := svc.Spec.Task.GetContainer()
		fmt.Fprintf(w, "Image: %s\n", container.Image)
		if len(container.Command) > 0 {
			fmt.Fprintf(w, "Command: %v\n", container.Command)
		}
		if len(container.Args) > 0 {
			fmt.Fprintf(w, "Args: %v\n", container.Args)
		}
		if len(container.Env) > 0 {
			fmt.Fprintf(w, "Environment:\n")
			for _, env := range container.Env {
				fmt.Fprintf(w, "  %s\n", env)
			}
		}
		if container.Hostname != "" {
			fmt.Fprintf(w, "Hostname: %s\n", container.Hostname)
		}
		if dns := container.DNSConfig; dns != nil && len(dns.Nameservers) > 0 {
			fmt.Fprintf(w, "DNS: %s\n", strings.Join(dns.Nameservers, ", "))
		}
		if container.User != "" {
			fmt.Fprintf(w, "User: %s\n", container.User)
		}
		if container.ReadOnly {
			fmt.Fprintf(w, "Read-only rootfs: true\n")
		}
		if len(container.CapabilityAdd) > 0 || len(container.CapabilityDrop) > 0 {
			fmt.Fprintf(w, "Capabilities: add=%v drop=%v\n", container.CapabilityAdd, container.CapabilityDrop)
		}
	}
	if svc.Spec.Endpoint != nil && len(svc.Spec.Endpoint.Ports) > 0 {
		fmt.Fprintf(w, "Published Ports:\n")
		for _, p := range svc.Spec.Endpoint.Ports {
			fmt.Fprintf(w, "  %d: -> %d/%s (%s)\n", p.PublishedPort, p.TargetPort, strings.ToLower(p.Protocol.String()), strings.ToLower(p.PublishMode.String()))
		}
	}
	if svc.Endpoint != nil && len(svc.Endpoint.VirtualIPs) > 0 {
		fmt.Fprintf(w, "Virtual IPs:\n")
		for _, v := range svc.Endpoint.VirtualIPs {
			fmt.Fprintf(w, "  %s (%s)\n", v.Addr, v.NetworkID)
		}
	}
	if nets := svc.Spec.Task.Networks; len(nets) > 0 {
		fmt.Fprintf(w, "Networks:\n")
		for _, n := range nets {
			if n.Target != "" {
				fmt.Fprintf(w, "  %s\n", n.Target)
			}
		}
	}
	if container := svc.Spec.Task.GetContainer(); container != nil {
		if len(container.Secrets) > 0 {
			fmt.Fprintf(w, "Secrets:\n")
			for _, s := range container.Secrets {
				fmt.Fprintf(w, "  %s\n", formatGrant(s.SecretName, s.GetFile()))
			}
		}
		if len(container.Configs) > 0 {
			fmt.Fprintf(w, "Configs:\n")
			for _, c := range container.Configs {
				fmt.Fprintf(w, "  %s\n", formatGrant(c.ConfigName, c.GetFile()))
			}
		}
	}
	if svc.Spec.Task.Resources != nil && svc.Spec.Task.Resources.Limits != nil {
		limits := svc.Spec.Task.Resources.Limits
		if limits.NanoCPUs > 0 {
			fmt.Fprintf(w, "CPU Limit: %.2f cores\n", float64(limits.NanoCPUs)/1e9)
		}
		if limits.MemoryBytes > 0 {
			fmt.Fprintf(w, "Memory Limit: %d bytes\n", limits.MemoryBytes)
		}
	}
	if p := svc.Spec.Task.Placement; p != nil && (len(p.Constraints) > 0 || len(p.Preferences) > 0) {
		fmt.Fprintf(w, "Placement:\n")
		for _, c := range p.Constraints {
			fmt.Fprintf(w, "  constraint: %s\n", c)
		}
		for _, pref := range p.Preferences {
			if s := pref.GetSpread(); s != nil {
				fmt.Fprintf(w, "  preference: spread=%s\n", s.SpreadDescriptor)
			}
		}
	}
	if rp := svc.Spec.Task.Restart; rp != nil {
		fmt.Fprintf(w, "Restart Policy: %s", restartConditionString(rp.Condition))
		if rp.MaxAttempts > 0 {
			fmt.Fprintf(w, " (max %d attempts)", rp.MaxAttempts)
		}
		fmt.Fprintf(w, "\n")
	}
	if uc := svc.Spec.Update; uc != nil {
		fmt.Fprintf(w, "Update Config: order=%s parallelism=%d failure-action=%s\n",
			updateOrderString(uc.Order), uc.Parallelism, failureActionString(uc.FailureAction))
	}
	if uc := svc.Spec.Rollback; uc != nil {
		fmt.Fprintf(w, "Rollback Config: order=%s parallelism=%d failure-action=%s\n",
			updateOrderString(uc.Order), uc.Parallelism, failureActionString(uc.FailureAction))
	}
	if len(svc.Spec.Annotations.Labels) > 0 {
		fmt.Fprintf(w, "Labels:\n")
		for k, v := range svc.Spec.Annotations.Labels {
			fmt.Fprintf(w, "  %s=%s\n", k, v)
		}
	}
}

func listServiceTasks(serviceID, format, filter string, quiet, noTrunc bool, health clusterHealthOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	return listServiceTasksWithClient(ctx, client, os.Stdout, serviceID, format, filter, quiet, noTrunc, health)
}

// listServiceTasksWithClient is the body of `service ps` with the control
// client and output writer injected, so the table/JSON rendering is
// unit-testable without a cluster.
func listServiceTasksWithClient(ctx context.Context, client api.ControlClient, w io.Writer, serviceID, format, filter string, quiet, noTrunc bool, health clusterHealthOptions) error {
	if err := health.validate(); err != nil {
		return err
	}

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
		fmt.Fprintf(w, "No tasks found for service %s\n", serviceID)
		return nil
	}

	// Apply additional filters
	if filter != "" {
		tasks = filterTasks(tasks, filter, "", "", true)
	}

	// Table format: --quiet prints only IDs, so skip the health fan-out.
	if format != "json" && quiet {
		for _, task := range tasks {
			fmt.Fprintln(w, task.ID)
		}
		return nil
	}

	lh := fetchLiveHealth(ctx, client, tasks, health)

	// Output based on format
	if format == "json" {
		return writeTasksJSON(w, tasks, lh)
	}

	// ERROR is appended last, so STATUS (field 2 here) and HEALTH (field 3) keep
	// their positions for the scripts that parse them. The rule spans the header
	// it follows instead of a hard-coded width.
	header := fmt.Sprintf("%-20s %-12s %-12s %-20s %s %s", "ID", "STATUS", "HEALTH", "NODE", "IMAGE", "ERROR")
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, strings.Repeat("-", len(header)))
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
		row := fmt.Sprintf("%-20s %-12s %-12s %-20s %s", taskID, status, taskHealthDisplay(task, lh), nodeID, image)
		// A task with no reason prints no trailing column at all, so a row keeps
		// exactly the shape it had before ERROR existed.
		if reason := taskErrorColumn(task); reason != "" {
			row += " " + reason
		}
		fmt.Fprintln(w, row)
	}
	fmt.Fprintf(w, "\nTotal: %d task(s)\n", len(tasks))

	return nil
}

func createService(opts serviceCreateOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	return createServiceWithClient(ctx, client, opts)
}

// createServiceWithClient is createService's create path. It takes the control
// client so tests can drive it with a fake (the same pattern as
// listServiceTasksWithClient) and assert that a failed --with-registry-auth
// resolution sends no request.
func createServiceWithClient(ctx context.Context, client api.ControlClient, opts serviceCreateOptions) error {
	// --with-registry-auth resolves the manager's credential for the image
	// before the spec is built and before any RPC, so a missing credential is a
	// clear, pre-create error rather than an opaque pull failure on a node.
	if opts.withRegistryAuth {
		encoded, err := resolveRegistryAuth(opts.image)
		if err != nil {
			return err
		}
		opts.registryAuth = encoded
	}

	spec, err := buildServiceSpec(opts)
	if err != nil {
		return err
	}

	// Resolve --network names to IDs and attach the service to them.
	targets, err := resolveNetworkTargets(ctx, client, opts.networks)
	if err != nil {
		return err
	}
	if len(targets) > 0 {
		spec.Task.Networks = buildNetworkAttachments(targets)
	}

	// Resolve --secret/--config and attach them to the container.
	if refs, err := buildSecretReferences(ctx, client, opts.secrets); err != nil {
		return err
	} else if len(refs) > 0 {
		spec.Task.GetContainer().Secrets = refs
	}
	if refs, err := buildConfigReferences(ctx, client, opts.configs); err != nil {
		return err
	} else if len(refs) > 0 {
		spec.Task.GetContainer().Configs = refs
	}

	resp, err := client.CreateService(ctx, &api.CreateServiceRequest{
		Spec: spec,
	})
	if err != nil {
		return fmt.Errorf("failed to create service: %w", err)
	}

	fmt.Printf("Service %s created with ID: %s\n", opts.name, resp.Service.ID)
	if r := spec.GetReplicated(); r != nil {
		fmt.Printf("Replicas: %d\n", r.Replicas)
	} else {
		fmt.Printf("Mode: global\n")
	}
	fmt.Printf("Image: %s\n", opts.image)

	return nil
}

// resolveRegistryAuth resolves the --with-registry-auth credential for a service
// image into the base64 X-Registry-Auth payload the spec carries. On the
// credential-absence path image.EncodeRegistryAuth already names the registry and
// the `docker login <host>` remedy and wraps ErrNoRegistryCredentials, so that
// message is returned intact (errors.Is keeps working). Every other failure - an
// unparsable ref, a credential helper that exits non-zero, an encoding error - is
// wrapped with its own cause and no login suggestion, because `docker login`
// would not fix it. Neither the password nor the encoded payload is ever logged;
// only the registry host is, at debug level.
func resolveRegistryAuth(ref string) (string, error) {
	encoded, err := image.EncodeRegistryAuth(ref)
	if err == nil {
		if host, hostErr := image.RegistryHostForRef(ref); hostErr == nil {
			log.Debug().Str("registry", host).Msg("resolved registry credentials for image")
		}
		return encoded, nil
	}
	return "", fmt.Errorf("--with-registry-auth: %w", err)
}

func updateService(serviceID string, opts serviceUpdateOptions) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	return updateServiceWithClient(ctx, client, serviceID, opts)
}

// updateServiceWithClient is updateService's update path. It takes the control
// client so tests can drive it with a fake (the same ...WithClient seam as
// createServiceWithClient) and assert both the registry-auth ordering (no
// update RPC before a rejected resolution) and what spec was submitted.
func updateServiceWithClient(ctx context.Context, client api.ControlClient, serviceID string, opts serviceUpdateOptions) error {
	// Validate the flag combinations that need no service lookup first, so the
	// most helpful error is the one returned.
	if opts.withRegistryAuthSet && opts.rollbackAction {
		return fmt.Errorf("--with-registry-auth cannot be combined with --rollback: a rollback restores the previous spec and ignores other flags")
	}
	// --cpuset belongs in the same guard: the rollback branch below submits
	// PreviousSpec and never consults the pin, so accepting the flag here would
	// validate it and then silently drop it. Rejected for an explicit empty
	// value too, which is a request to clear the pin that a rollback cannot honour.
	if opts.cpusetSet && opts.rollbackAction {
		return fmt.Errorf("--cpuset cannot be combined with --rollback: a rollback restores the previous spec and ignores other flags")
	}
	if opts.withRegistryAuthSet && opts.withRegistryAuth && opts.image == "" {
		return fmt.Errorf("--with-registry-auth requires --image: the credential is resolved for the image the service will run")
	}

	// --cpuset is validated before any RPC. Absent preserves the service's
	// current pin (copied with the spec below); an explicit empty value removes
	// it; a value is canonicalised once here, so a malformed list fails before
	// the lookup and update RPCs and the stored form is the one the daemon and
	// the jailer parse.
	cpusetValue := ""
	if opts.cpusetSet && opts.cpuset != "" {
		value, err := canonicalCpuset(opts.cpuset)
		if err != nil {
			return err
		}
		cpusetValue = value
	}

	// Resolve the reference (full ID, unique ID prefix, or name) and fetch it.
	serviceID, err := resolveServiceRef(ctx, client, serviceID)
	if err != nil {
		return err
	}
	getResp, err := client.GetService(ctx, &api.GetServiceRequest{ServiceID: serviceID})
	if err != nil {
		return fmt.Errorf("failed to get service: %w", err)
	}
	svc := getResp.Service

	// A rollback reverts to the previous spec; other flags are ignored.
	if opts.rollbackAction {
		if svc.PreviousSpec == nil {
			return fmt.Errorf("service %q has no previous spec to roll back to", svc.Spec.Annotations.Name)
		}
		return applyServiceSpec(ctx, client, svc, svc.PreviousSpec.Copy())
	}

	// --with-registry-auth on a golden service would be stored but never
	// consumed (golden images boot their own init and are not pulled from a
	// registry), so reject it rather than silently dropping the flag. Clearing
	// the credential (an explicit false) stays valid.
	//
	// Whether the service is golden is only knowable from the spec the lookup
	// above fetched, so this rejection sits above the credential resolution:
	// placed after it, a golden service would resolve an unused credential (or
	// fail on a missing one) before reaching this clearer error. The lookup is
	// a read and mutates nothing.
	if opts.withRegistryAuthSet && opts.withRegistryAuth && svc.Spec.Annotations.Labels[types.GoldenLabel] != "" {
		return fmt.Errorf("--with-registry-auth cannot be used on a golden service: golden images are not pulled from a registry")
	}

	// --with-registry-auth (#44) resolves the CLI's credential for the target
	// image — the CLI dials the local socket, so in a normal install this runs
	// on the manager as the account that invokes swarmcracker. The service
	// lookup above is a read, so a missing credential is still a clear error
	// before the update RPC rather than an opaque pull failure on a node.
	if opts.withRegistryAuthSet && opts.withRegistryAuth {
		encoded, err := resolveRegistryAuth(opts.image)
		if err != nil {
			return err
		}
		opts.registryAuth = encoded
	}

	// Parse memory
	memoryBytes, err := parseMemory(opts.memoryLimit)
	if err != nil {
		return fmt.Errorf("invalid memory value: %w", err)
	}

	spec := svc.Spec.Copy()

	// --balloon-mib is tri-state: an absent flag preserves the service's current
	// label (copied above), a positive value sets it, and an explicit 0 removes
	// it. The target is validated against the memory the updated task will carry
	// — the --memory-limit given with this update, else the effective size of
	// the service's current spec (its Limits, else its Reservations, else the
	// default) — so the flag is never accepted and then ignored, and a size that
	// lives in Reservations (set by an API client) is resolved the same way the
	// node resolves it.
	if opts.balloonSet {
		switch {
		case opts.balloonMib < 0:
			return fmt.Errorf("invalid --balloon-mib %d: must be 0 (remove the balloon) or a positive integer MiB", opts.balloonMib)
		case opts.balloonMib == 0:
			delete(spec.Annotations.Labels, types.BalloonLabel)
		default:
			value, err := balloonLabelValue(int64(opts.balloonMib), true, serviceMemoryMib(memoryBytes, spec.Task.Resources))
			if err != nil {
				return err
			}
			if spec.Annotations.Labels == nil {
				spec.Annotations.Labels = make(map[string]string)
			}
			spec.Annotations.Labels[types.BalloonLabel] = value
		}
	}

	// --cpuset is tri-state too: absent leaves the copied label untouched, an
	// explicit empty value removes the pin, and a value replaces it with the
	// canonical form validated above.
	if opts.cpusetSet {
		if opts.cpuset == "" {
			delete(spec.Annotations.Labels, types.CpusetLabel)
		} else {
			if spec.Annotations.Labels == nil {
				spec.Annotations.Labels = make(map[string]string)
			}
			spec.Annotations.Labels[types.CpusetLabel] = cpusetValue
		}
	}

	// Apply the --with-registry-auth decision (#44): an absent flag preserves
	// the credential copied from the current spec, an explicit true replaces it
	// with the pre-resolved payload, and an explicit false clears it.
	if payload, apply := registryAuthForUpdate(opts.withRegistryAuthSet, opts.withRegistryAuth, opts.registryAuth); apply {
		c := spec.Task.GetContainer()
		if c == nil {
			return fmt.Errorf("service %q has no container spec", svc.Spec.Annotations.Name)
		}
		if payload == "" {
			c.PullOptions = nil
		} else {
			c.PullOptions = &api.ContainerSpec_PullOptions{RegistryAuth: payload}
		}
	}

	// Update replicas when the caller explicitly provided a count. This keeps
	// `service update` without --replicas a no-op while allowing an explicit
	// `--replicas 0` (scale to zero).
	if opts.replicasSet {
		if r := spec.GetReplicated(); r != nil {
			r.Replicas = opts.replicas
		} else {
			spec.Mode = &api.ServiceSpec_Replicated{
				Replicated: &api.ReplicatedService{
					Replicas: opts.replicas,
				},
			}
		}
	}

	// Update container spec
	container := spec.Task.GetContainer()
	if container != nil {
		// Update image if specified
		if opts.image != "" {
			container.Image = opts.image
		}

		// Update environment variables
		if len(opts.env) > 0 || len(opts.envRemove) > 0 {
			// Remove specified env vars
			if len(opts.envRemove) > 0 {
				removeSet := make(map[string]bool)
				for _, key := range opts.envRemove {
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
			if len(opts.env) > 0 {
				envMap := make(map[string]string)
				for _, e := range container.Env {
					parts := strings.SplitN(e, "=", 2)
					if len(parts) == 2 {
						envMap[parts[0]] = parts[1]
					}
				}
				for _, e := range opts.env {
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

	// Replace the container healthcheck. buildHealthcheck rejects any
	// --health-* flag that arrives without --health-cmd, so an update never
	// silently drops a flag.
	if opts.health.set() {
		c := spec.Task.GetContainer()
		if c == nil {
			return fmt.Errorf("service %q has no container spec", svc.Spec.Annotations.Name)
		}
		hc, err := buildHealthcheck(opts.health)
		if err != nil {
			return err
		}
		c.Healthcheck = hc
	}

	// Update resource limits
	if opts.cpuLimit > 0 || memoryBytes > 0 {
		if spec.Task.Resources == nil {
			spec.Task.Resources = &api.ResourceRequirements{}
		}
		if spec.Task.Resources.Limits == nil {
			spec.Task.Resources.Limits = &api.Resources{}
		}
		if opts.cpuLimit > 0 {
			spec.Task.Resources.Limits.NanoCPUs = int64(opts.cpuLimit * 1e9)
		}
		if memoryBytes > 0 {
			spec.Task.Resources.Limits.MemoryBytes = memoryBytes
		}
	}

	// Change the publish mode of the service's existing published ports. The
	// tasks are recreated on the spec change, so host-mode rules are released
	// and ingress rules reconcile accordingly.
	if opts.publishModeSet {
		mode, err := types.NormalizePublishMode(opts.publishMode)
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

	// Change the service mode. SwarmKit does not allow a service's mode to
	// change on update, so reject a real change with a clear error instead of
	// forwarding it and surfacing an opaque RPC failure.
	if opts.modeSet {
		mode, err := normalizeServiceMode(opts.mode)
		if err != nil {
			return err
		}
		current := modeReplicated
		if spec.GetGlobal() != nil {
			current = modeGlobal
		}
		if mode != current {
			return fmt.Errorf("changing the service mode (%s -> %s) is not supported; recreate the service", current, mode)
		}
	}

	// Replace placement.
	if opts.placementSet {
		placement, err := buildPlacement(opts.constraints, opts.placementPrefs)
		if err != nil {
			return err
		}
		spec.Task.Placement = placement
	}

	// Replace mounts.
	if opts.mountsSet {
		mounts, err := buildMounts(opts.mounts, opts.volumes)
		if err != nil {
			return err
		}
		if c := spec.Task.GetContainer(); c != nil {
			c.Mounts = mounts
		}
	}

	// Replace networks.
	if opts.networksSet {
		targets, err := resolveNetworkTargets(ctx, client, opts.networks)
		if err != nil {
			return err
		}
		spec.Task.Networks = buildNetworkAttachments(targets)
	}

	// Replace secrets.
	if opts.secretsSet {
		refs, err := buildSecretReferences(ctx, client, opts.secrets)
		if err != nil {
			return err
		}
		spec.Task.GetContainer().Secrets = refs
	}

	// Replace configs.
	if opts.configsSet {
		refs, err := buildConfigReferences(ctx, client, opts.configs)
		if err != nil {
			return err
		}
		spec.Task.GetContainer().Configs = refs
	}

	// Container-execution flags: reject the ones the microVM executor cannot
	// enforce, and apply the guest overrides we can honor.
	if err := validateUnsupportedExecFlags(opts.user, opts.capAdd, opts.capDrop, opts.readOnly); err != nil {
		return err
	}
	if opts.hostnameSet || opts.dnsSet {
		if spec.Annotations.Labels[types.GoldenLabel] != "" {
			return fmt.Errorf("--hostname/--dns are not supported with --golden: golden images boot their own init")
		}
		c := spec.Task.GetContainer()
		if c == nil {
			return fmt.Errorf("service %q has no container spec", svc.Spec.Annotations.Name)
		}
		if opts.hostnameSet {
			h, err := buildHostname(opts.hostname)
			if err != nil {
				return err
			}
			c.Hostname = h
		}
		if opts.dnsSet {
			d, err := buildDNS(opts.dns)
			if err != nil {
				return err
			}
			if len(d) == 0 {
				c.DNSConfig = nil
			} else {
				c.DNSConfig = &api.ContainerSpec_DNSConfig{Nameservers: d}
			}
		}
	}

	// Replace the restart policy.
	if opts.restartSet {
		rp, err := buildRestartPolicy(serviceCreateOptions{
			restartSet:      true,
			restartCond:     opts.restartCond,
			restartDelay:    opts.restartDelay,
			restartAttempts: opts.restartAttempts,
			restartWindow:   opts.restartWindow,
		})
		if err != nil {
			return err
		}
		spec.Task.Restart = rp
	}

	// Replace the update / rollback configuration.
	if opts.updateSet {
		uc, err := buildUpdateConfig("update", true, opts.updateOrder, opts.updateParallel, opts.updateDelay, opts.updateFailAction, opts.updateMonitor, opts.updateMaxRatio)
		if err != nil {
			return err
		}
		spec.Update = uc
	}
	if opts.rollbackSet {
		uc, err := buildUpdateConfig("rollback", true, opts.rollbackOrder, opts.rollbackParallel, opts.rollbackDelay, opts.rollbackFailAction, opts.rollbackMonitor, opts.rollbackMaxRatio)
		if err != nil {
			return err
		}
		spec.Rollback = uc
	}

	// Force update if requested
	if opts.force {
		spec.Task.ForceUpdate++
	}

	return applyServiceSpec(ctx, client, svc, spec)
}

// applyServiceSpec submits an updated spec for an existing service.
func applyServiceSpec(ctx context.Context, client api.ControlClient, svc *api.Service, spec *api.ServiceSpec) error {
	_, err := client.UpdateService(ctx, &api.UpdateServiceRequest{
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
	return updateService(serviceID, serviceUpdateOptions{replicas: replicas, replicasSet: true})
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
