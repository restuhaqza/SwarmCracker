package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/rs/zerolog/log"
)

// vmSocketDirDefault mirrors the daemon's default --socket-dir.
const vmSocketDirDefault = "/var/run/firecracker"

// vmSocketDir is the directory scanned for Firecracker VM sockets. It is bound
// to the persistent --socket-dir flag on the list command.
var vmSocketDir = vmSocketDirDefault

// enrichVMs fills in the host PID for every VM and, when the SwarmKit control
// API is reachable, the image/service/IP metadata for daemon-managed VMs. It is
// best-effort: listing still works with reduced detail when SwarmKit is down.
func enrichVMs(vms []*runtime.VMState) {
	pids := runtime.FindFirecrackerPIDs()
	index := loadSwarmTaskIndex()

	for _, vm := range vms {
		if vm.PID == 0 {
			vm.PID = pids[vm.ID]
		}

		// Network details recorded by the daemon next to the VM socket. These
		// carry the guest IP for fallback (TAP/DHCP) allocations, which are
		// never written back to the SwarmKit task store and would otherwise be
		// invisible to `vm status`/`vm list`.
		if md, ok := runtime.ReadVMMetadata(vmSocketDir, vm.ID); ok {
			if vm.NetworkID == "" {
				vm.NetworkID = md.NetworkID
			}
			if len(vm.IPAddresses) == 0 {
				vm.IPAddresses = append([]string(nil), md.IPAddresses...)
			}
		}

		info, ok := index[vm.ID]
		if !ok {
			continue
		}
		if vm.Image == "" {
			vm.Image = info.image
		}
		vm.Service = info.service
		if len(vm.IPAddresses) == 0 {
			vm.IPAddresses = info.ips
		}
	}
}

// swarmTaskInfo is the subset of SwarmKit task metadata used to enrich a VM.
type swarmTaskInfo struct {
	image   string
	service string
	ips     []string
}

// loadSwarmTaskIndex returns task metadata keyed by task ID. It returns nil when
// the control API is unavailable, and callers must treat that as "no extra
// metadata" rather than an error.
func loadSwarmTaskIndex() map[string]swarmTaskInfo {
	client, conn, err := getSwarmClientForTask()
	if err != nil {
		log.Debug().Err(err).Msg("Control API unavailable; listing VMs without service metadata")
		return nil
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tasks, err := client.ListTasks(ctx, &api.ListTasksRequest{})
	if err != nil {
		log.Debug().Err(err).Msg("Failed to list tasks; listing VMs without service metadata")
		return nil
	}

	serviceNames := make(map[string]string)
	if services, serr := client.ListServices(ctx, &api.ListServicesRequest{}); serr == nil {
		for _, svc := range services.Services {
			serviceNames[svc.ID] = svc.Spec.Annotations.Name
		}
	}

	index := make(map[string]swarmTaskInfo, len(tasks.Tasks))
	for _, task := range tasks.Tasks {
		info := swarmTaskInfo{service: serviceNames[task.ServiceID]}
		if container := task.Spec.GetContainer(); container != nil {
			info.image = container.Image
		}
		for _, network := range task.Networks {
			if network != nil {
				info.ips = append(info.ips, network.Addresses...)
			}
		}
		index[task.ID] = info
	}

	return index
}

// resolveRunningVM finds a running VM (daemon-managed or otherwise) by exact ID
// or unique prefix and returns a synthesized state enriched with task metadata.
func resolveRunningVM(ref string) (*runtime.VMState, error) {
	running, err := runtime.DiscoverRunningVMs(vmSocketDir)
	if err != nil {
		return nil, err
	}
	if len(running) == 0 {
		return nil, fmt.Errorf("no running VMs found under %s", vmSocketDir)
	}

	available := make([]string, 0, len(running))
	for _, vm := range running {
		if vm.ID == ref {
			return discoveredVMState(vm), nil
		}
		available = append(available, vm.ID)
	}

	matches := make([]runtime.RunningVM, 0, len(running))
	for _, vm := range running {
		if strings.HasPrefix(vm.ID, ref) {
			matches = append(matches, vm)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%q does not match any running VM (available: %s)", ref, strings.Join(available, ", "))
	case 1:
		return discoveredVMState(matches[0]), nil
	default:
		names := make([]string, 0, len(matches))
		for _, vm := range matches {
			names = append(names, vm.ID)
		}
		return nil, fmt.Errorf("%q matches multiple running VMs: %s", ref, strings.Join(names, ", "))
	}
}

// discoveredVMState converts a discovered VM into a VMState, enriched with any
// task metadata that is available.
func discoveredVMState(vm runtime.RunningVM) *runtime.VMState {
	state := &runtime.VMState{
		ID:         vm.ID,
		Status:     "running",
		SocketPath: vm.SocketPath,
		StartTime:  vm.Started,
	}
	enrichVMs([]*runtime.VMState{state})
	return state
}
