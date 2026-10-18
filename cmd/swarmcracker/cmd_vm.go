package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/golden"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// newVMCommand creates the VM command group
func newVMCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vm",
		Short: "Manage Firecracker microVMs",
		Long: `Manage Firecracker microVMs in the SwarmCracker cluster.

These commands provide VM-level operations like creating, listing, stopping, and viewing logs.`,
	}

	// Add subcommands
	cmd.AddCommand(newVMCreateCommand())
	cmd.AddCommand(newVMListCommand())
	cmd.AddCommand(newVMStopCommand())
	cmd.AddCommand(newVMLogsCommand())
	cmd.AddCommand(newVMSnapshotCommand())
	cmd.AddCommand(newVMAttachCommand())
	cmd.AddCommand(newVMExecCommand())
	cmd.AddCommand(newVMCpCommand())
	// status is also available at the top level (`swarmcracker status`); expose
	// it under `vm` too for consistency.
	cmd.AddCommand(newStatusCommand())

	return cmd
}

// vmCreateTaskHook, when non-nil, receives the task `vm create` has assembled
// and ends the command there. Production leaves it nil, so the command goes on
// to launch the VM exactly as before. Tests set it (and restore it with
// t.Cleanup) to drive the real command path — flag parsing, canonicalisation
// and task assembly — and assert what the task handed downstream carries,
// without needing KVM or a Firecracker install.
var vmCreateTaskHook func(task *types.Task) error

// newVMCreateCommand creates the VM create command
func newVMCreateCommand() *cobra.Command {
	var (
		name       string
		vcpus      int
		memory     int
		balloonMib int
		cpuset     string
		hugepages  bool
		numaNode   string
		network    string
		detach     bool
		env        []string
		goldenRef  string
		goldenDir  string
	)

	cmd := &cobra.Command{
		Use:   "create [image]",
		Short: "Create a Firecracker microVM from an OCI image or golden image",
		Long: `Create a Firecracker microVM from an OCI container image or a prebuilt golden image.

With an image argument, SwarmCracker pulls the container image, converts it to a
rootfs, and launches it as an isolated microVM using Firecracker.

With --golden, it boots a prebuilt golden image (see 'swarmcracker image build')
with its own init (systemd/OpenRC), so container runtimes such as Docker can run
inside the microVM.

Example:
  swarmcracker vm create alpine:latest
  swarmcracker vm create --name my-vm --cpu 2 --memory 1024 nginx:latest
  swarmcracker vm create --golden ubuntu-24.04-docker
  swarmcracker vm create --golden ubuntu-24.04-docker@1.0.0 --memory 4096 -d`,
		Args: cobra.MaximumNArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			imageRef := ""
			if len(args) > 0 {
				imageRef = args[0]
			}
			if goldenRef == "" && imageRef == "" {
				return fmt.Errorf("provide an image argument or --golden <name[@version]>")
			}
			if goldenRef != "" && imageRef != "" {
				return fmt.Errorf("--golden and an image argument are mutually exclusive")
			}

			// Validate --cpuset before the configuration is loaded and before
			// any RPC: a malformed list must never reach the executor, where it
			// can only fail (or silently mismatch) at VM start.
			cpusetValue := ""
			if cmd.Flags().Changed("cpuset") {
				value, err := canonicalCpuset(cpuset)
				if err != nil {
					return err
				}
				cpusetValue = value
			}

			// --numa-node is the same contract: validate the cpulist before the
			// configuration is loaded, so a malformed request never reaches the
			// executor. --hugepages is a boolean and needs no validation.
			numaNodeValue := ""
			if cmd.Flags().Changed("numa-node") {
				value, err := canonicalCPUSet("--numa-node", numaNode)
				if err != nil {
					return err
				}
				numaNodeValue = value
			}

			// Load configuration
			cfg, err := loadConfigWithOverrides(cfgFile)
			if err != nil {
				return fmt.Errorf("failed to load configuration: %w", err)
			}

			// Resolve a golden image, if requested, into task annotations the
			// executor and translator understand.
			var goldenAnnotations map[string]string
			if goldenRef != "" {
				if goldenDir == "" {
					goldenDir = defaultGoldenDir()
				}
				art, err := golden.Resolve(goldenDir, goldenRef)
				if err != nil {
					return err
				}
				imageRef = "golden:" + art.Ref
				goldenAnnotations = map[string]string{
					types.AnnotationRootfs:         art.Path,
					types.AnnotationPrebuiltRootfs: "true",
					types.AnnotationGolden:         art.Ref,
				}
				if art.Metadata.KernelProfile != "" {
					goldenAnnotations[types.AnnotationKernelProfile] = art.Metadata.KernelProfile
				}
				if len(art.Metadata.Init.BootArgs) > 0 {
					goldenAnnotations[types.AnnotationBootArgs] = strings.Join(art.Metadata.Init.BootArgs, " ")
				}
				// A full-OS guest needs more headroom than the single-workload default.
				if !cmd.Flags().Changed("cpu") && vcpus < 2 {
					vcpus = 2
				}
				if !cmd.Flags().Changed("memory") && memory < 2048 {
					memory = 2048
				}
				fmt.Printf("Using golden image %s (%s)\n", art.Ref, art.Path)
			}

			// Validate the memory-balloon target before any RPC. The VM's
			// effective memory is --memory (the golden-image default of 2048
			// above is already applied), so a target that is not smaller than it
			// can never be honoured and is rejected here with both numbers.
			balloonValue, err := balloonLabelValue(int64(balloonMib), cmd.Flags().Changed("balloon-mib"), int64(memory))
			if err != nil {
				return err
			}

			// Assemble the task that is handed downstream, carrying the
			// golden-image annotations and the validated balloon target,
			// cpuset, NUMA binding and hugepages request. Assembly is pure and
			// runs before the executor exists, so the task the command hands
			// downstream is observable through vmCreateTaskHook and identical on
			// both paths.
			task := buildVMCreateTask(imageRef, vcpus, memory, env, goldenAnnotations, balloonValue, cpusetValue, numaNodeValue, hugepages)

			// Override task ID with name if provided
			if name != "" {
				task.ID = name
			}

			// Tests observe the assembled task here instead of booting a VM;
			// production falls through with the hook nil.
			if hook := vmCreateTaskHook; hook != nil {
				return hook(task)
			}

			// Create executor
			exec, err := createExecutor(cfg)
			if err != nil {
				return fmt.Errorf("failed to create executor: %w", err)
			}
			defer exec.Close()

			// Create state manager for tracking VMs
			stateMgr, err := runtime.NewStateManager("")
			if err != nil {
				log.Warn().Err(err).Msg("Failed to create state manager, VM tracking disabled")
			}

			// Prepare context with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			// Setup signal handling for cleanup
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				sig := <-sigCh
				log.Info().Str("signal", sig.String()).Msg("Received interrupt signal, cleaning up...")
				cancel()
				if err := exec.Remove(context.Background(), task); err != nil {
					log.Warn().Err(err).Msg("Failed to remove task during interrupt cleanup")
				}
				os.Exit(1)
			}()

			// Prepare the task
			log.Info().Str("task_id", task.ID).Msg("Preparing task...")
			if err := exec.Prepare(ctx, task); err != nil {
				return fmt.Errorf("failed to prepare task: %w", err)
			}

			// In detached mode the VM outlives this process, so its console
			// must not inherit our stdout/stderr: a long-lived child holding
			// that descriptor keeps a caller's pipe open forever (e.g.
			// `... vm create -d | tail` never sees EOF). Send it to the VM log
			// file — the same path `swarmcracker vm logs` reads.
			var consoleFile *os.File
			if detach {
				logDir := ""
				if stateMgr != nil {
					logDir = stateMgr.GetLogDir()
				}
				if logDir == "" {
					logDir = "/var/log/swarmcracker"
				}
				if err := os.MkdirAll(logDir, 0o755); err != nil {
					log.Warn().Err(err).Msg("Could not create VM log directory")
				} else if f, ferr := os.OpenFile(filepath.Join(logDir, task.ID+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); ferr != nil {
					log.Warn().Err(ferr).Msg("Could not open VM log file; console stays attached")
				} else if serr := exec.SetConsoleWriter(f); serr != nil {
					log.Warn().Err(serr).Msg("Could not redirect VM console to log file")
					_ = f.Close()
				} else {
					consoleFile = f
				}
			}

			// Start the task
			log.Info().Str("task_id", task.ID).Msg("Starting task...")
			if err := exec.Start(ctx, task); err != nil {
				return fmt.Errorf("failed to start task: %w", err)
			}

			// The VM child holds its own copy of the descriptor; close ours.
			if consoleFile != nil {
				_ = consoleFile.Close()
			}

			if detach {
				log.Info().Str("task_id", task.ID).Msg("Task started in detached mode")

				// Save VM state for tracking
				if stateMgr != nil {
					// Get task details for state
					container, ok := task.Spec.Runtime.(*types.Container)
					if !ok {
						log.Warn().Str("task_id", task.ID).Msg("Task runtime is not a container, skipping state save")
						return nil
					}

					vmState := &runtime.VMState{
						ID:         task.ID,
						PID:        runtime.FindFirecrackerPID(task.ID),
						SocketPath: filepath.Join(cfg.Executor.SocketDir, task.ID+".sock"),
						Image:      container.Image,
						Command:    append(container.Command, container.Args...),
						Status:     "running",
						VCPUs:      vcpus,
						MemoryMB:   memory,
						KernelPath: cfg.Executor.KernelPath,
						LogPath:    filepath.Join(stateMgr.GetLogDir(), task.ID+".log"),
						// The VM boots from its own copy of the image rootfs; the
						// copy is freed when the VM is cleaned up, while an image
						// cache or a golden artifact is never marked ephemeral.
						RootfsPath:      task.Annotations[types.AnnotationRootfs],
						RootfsEphemeral: task.Annotations[types.AnnotationRootfsEphemeral] == "true",
					}

					// Get network info if available
					if len(task.Networks) > 0 {
						vmState.NetworkID = task.Networks[0].Network.ID
						vmState.IPAddresses = task.Networks[0].Addresses
					}

					// Add to state manager
					if err := stateMgr.Add(vmState); err != nil {
						log.Warn().Err(err).Msg("Failed to save VM state")
					} else {
						log.Info().Str("vm_id", task.ID).Msg("VM state saved")
					}
				}

				// Output the VM ID
				fmt.Println(task.ID)
				return nil
			}

			// Wait for completion
			log.Info().Str("task_id", task.ID).Msg("Waiting for task to complete...")
			status, err := exec.Wait(ctx, task)
			if err != nil {
				log.Error().Err(err).Msg("Task failed")
				return fmt.Errorf("task execution failed: %w", err)
			}

			log.Info().
				Str("task_id", task.ID).
				Str("state", taskStateString(status.State)).
				Msg("Task completed")

			// Output the VM ID
			fmt.Println(task.ID)

			// Cleanup
			log.Info().Str("task_id", task.ID).Msg("Cleaning up...")
			if err := exec.Remove(ctx, task); err != nil {
				log.Warn().Err(err).Msg("Cleanup failed (task may still be running)")
			}

			return nil
		},
	}

	// Flags
	cmd.Flags().StringVarP(&name, "name", "n", "", "Name for the VM (auto-generated if not specified)")
	cmd.Flags().IntVar(&vcpus, "cpu", 1, "Number of vCPUs to allocate")
	cmd.Flags().IntVarP(&memory, "memory", "m", 512, "Memory in MB to allocate")
	cmd.Flags().IntVar(&balloonMib, "balloon-mib", 0, "Memory-balloon target in MiB, must be less than --memory (the guest can reclaim it via deflate-on-OOM)")
	cmd.Flags().StringVar(&cpuset, "cpuset", "", "Pin the VM to these host CPUs (Linux cpulist: 0, 0-3, 0-3,8); the host must have those CPUs online")
	cmd.Flags().BoolVar(&hugepages, "hugepages", false, "Back the VM's guest memory with 2M huge pages; the host must have a sufficiently large hugepage pool")
	cmd.Flags().StringVar(&numaNode, "numa-node", "", "Bind the VM's memory to these host NUMA nodes (Linux cpulist: 0, 0-1); the host must have those nodes online")
	cmd.Flags().StringVar(&network, "network", "", "Network to attach the VM to")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "Run in detached mode (don't wait for completion)")
	cmd.Flags().StringArrayVarP(&env, "env", "e", []string{}, "Environment variables (e.g., -e KEY=value)")
	cmd.Flags().StringVar(&goldenRef, "golden", "", "Boot a prebuilt golden image (name or name@version) instead of an OCI image")
	cmd.Flags().StringVar(&goldenDir, "golden-dir", "", "Directory containing golden image artifacts (default: <rootfs-dir>/golden)")

	return cmd
}

// buildVMCreateTask assembles the task a `vm create` run hands downstream: the
// mock task for the requested resources, carrying the golden-image annotations
// (when a golden image was resolved) and the validated memory-balloon, cpuset,
// NUMA-node and hugepages values. It is a function of its inputs, rather than
// inline in RunE, so that the merge of the annotation sources is directly
// testable: the golden annotations and each validated value must all survive
// onto the task the executor is given. An assignment of goldenAnnotations in
// place of the merge would silently drop the balloon target or the placement
// (or vice versa); each with...Annotation helper always merges.
func buildVMCreateTask(imageRef string, vcpus, memory int, env []string, goldenAnnotations map[string]string, balloonValue, cpusetValue, numaNodeValue string, hugepages bool) *types.Task {
	task := createMockTask(imageRef, vcpus, memory, env)
	if goldenAnnotations != nil {
		task.Annotations = goldenAnnotations
	}
	task.Annotations = withBalloonAnnotation(task.Annotations, balloonValue)
	task.Annotations = withCpusetAnnotation(task.Annotations, cpusetValue)
	task.Annotations = withNumaNodeAnnotation(task.Annotations, numaNodeValue)
	task.Annotations = withHugepagesAnnotation(task.Annotations, hugepages)
	return task
}

// withAnnotation merges one annotation into a task's map, creating the map only
// when there is a value to add. It must merge: a `vm create` task may already
// carry the golden-image annotations, and replacing the map would silently drop
// them. An empty value leaves the map untouched, so a VM created without the
// flag is identical to before.
func withAnnotation(annotations map[string]string, key, value string) map[string]string {
	if value == "" {
		return annotations
	}
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[key] = value
	return annotations
}

// withBalloonAnnotation merges a validated --balloon-mib target into a task's
// annotation map.
func withBalloonAnnotation(annotations map[string]string, value string) map[string]string {
	return withAnnotation(annotations, types.BalloonLabel, value)
}

// withCpusetAnnotation merges a validated --cpuset pin into a task's annotation
// map. It must merge for the same reason as withBalloonAnnotation.
func withCpusetAnnotation(annotations map[string]string, value string) map[string]string {
	return withAnnotation(annotations, types.AnnotationCpuset, value)
}

// withNumaNodeAnnotation merges a canonical --numa-node nodelist into a task's
// annotation map, creating the map only when there is a binding to add. An
// empty value leaves the map untouched.
func withNumaNodeAnnotation(annotations map[string]string, value string) map[string]string {
	return withAnnotation(annotations, types.AnnotationNumaNode, value)
}

// withHugepagesAnnotation merges an opted-in --hugepages request into a task's
// annotation map. The stored form is "true": hugepages is a boolean request,
// not a page size, so --hugepages=false adds nothing at all.
func withHugepagesAnnotation(annotations map[string]string, requested bool) map[string]string {
	if !requested {
		return annotations
	}
	return withAnnotation(annotations, types.AnnotationHugepages, "true")
}

// newVMListCommand creates the VM list command
func newVMListCommand() *cobra.Command {
	// Reuse existing list command
	cmd := newListCommand()
	return cmd
}

// newVMStopCommand creates the VM stop command
func newVMStopCommand() *cobra.Command {
	// Reuse existing stop command
	cmd := newStopCommand()
	return cmd
}

// newVMLogsCommand creates the VM logs command
func newVMLogsCommand() *cobra.Command {
	// Reuse existing logs command
	cmd := newLogsCommand()
	return cmd
}

// newVMSnapshotCommand creates the VM snapshot command
func newVMSnapshotCommand() *cobra.Command {
	// Reuse existing snapshot command
	cmd := newSnapshotCommand()
	return cmd
}
