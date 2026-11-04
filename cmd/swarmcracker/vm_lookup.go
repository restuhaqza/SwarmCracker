package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
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
//
// records is the node's instance registry, keyed by ID. It is how a VM's runtime
// metadata sidecar is found: the record names the file, and a VM with no record
// falls back to the one derived beside its socket.
func enrichVMs(vms []*runtime.VMState, records map[string]vminstance.Instance) {
	pids := runtime.FindFirecrackerPIDs()
	index := loadSwarmTaskIndex()

	for _, vm := range vms {
		if vm.PID == 0 {
			vm.PID = pids[vm.ID]
		}

		// Network details recorded by the daemon next to the VM socket. These
		// carry the guest IP for fallback (TAP/DHCP) allocations, which are
		// never written back to the SwarmKit task store and would otherwise be
		// invisible to `vm status`/`vm list`. The same file carries the daemon's
		// live healthcheck verdict, which is the only place it can be read:
		// SwarmKit has no health field and never polls a running task.
		if md, ok := vmMetadata(vm.ID, records); ok {
			if vm.NetworkID == "" {
				vm.NetworkID = md.NetworkID
			}
			if len(vm.IPAddresses) == 0 {
				vm.IPAddresses = append([]string(nil), md.IPAddresses...)
			}
			if vm.Health == nil {
				vm.Health = md.Health
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

// vmMetadata reads a VM's runtime metadata sidecar: the file the node's record
// names, or — for a VM with no record, and for a record written before the field
// existed — the one derived beside the API socket. The second half is the
// one-release fallback, not a second rule: a node that records its VMs never
// reaches it.
func vmMetadata(vmID string, records map[string]vminstance.Instance) (*runtime.VMMetadata, bool) {
	if record, ok := records[vmID]; ok && record.Metadata != "" {
		return runtime.ReadVMMetadataFile(record.Metadata)
	}
	return runtime.ReadVMMetadata(vmSocketDir, vmID)
}

// mergedVMs layers the node's three views of its VMs — the CLI state file, the
// instance registry and the socket scan — with the registry authoritative for
// every VM it names, then enriches the result with the daemon's runtime metadata.
// It is one function because `vm list`, `vm status` and `vm metrics` must not
// disagree about which VMs exist or where their metadata is.
func mergedVMs(stateMgr *runtime.StateManager) []*runtime.VMState {
	running, err := runtime.DiscoverRunningVMs(vmSocketDir)
	if err != nil {
		log.Warn().Err(err).Str("socket_dir", vmSocketDir).Msg("Failed to discover running VMs")
	}

	recorded, records, recordErr := loadRecordedVMs(vmInstanceStore())
	if recordErr != nil {
		log.Warn().Err(recordErr).Msg("Failed to read the instance registry; listing VMs from their sockets")
	}

	vms := mergeRecordedVMs(runtime.MergeVMs(stateMgr.List(), running), recorded)
	enrichVMs(vms, records)
	return vms
}

// loadRecordedVMs reads the node's instance registry. It returns the VMs as
// listing rows — every field a record states, with Source set to registry — and
// the records themselves, because a record also says where the VM's runtime
// metadata sidecar is, which is a path `enrichVMs` reads and cannot derive.
//
// A record whose process is gone is not returned: the store refuses to list one,
// so a dead VM never appears as a live row. A nil store is "this node keeps no
// registry here", which is an empty listing and no error.
func loadRecordedVMs(store *vminstance.Store) ([]*runtime.VMState, map[string]vminstance.Instance, error) {
	records := make(map[string]vminstance.Instance)
	if store == nil {
		return nil, records, nil
	}

	instances, _, err := store.List()
	if err != nil {
		return nil, records, err
	}

	vms := make([]*runtime.VMState, 0, len(instances))
	for _, instance := range instances {
		records[instance.ID] = instance
		vms = append(vms, &runtime.VMState{
			ID:         instance.ID,
			PID:        instance.PID,
			Status:     "running",
			SocketPath: instance.APISocket,
			StartTime:  instance.Booted,
			Source:     runtime.VMSourceRegistry,
		})
	}
	return vms, records, nil
}

// mergeRecordedVMs layers the registry over the listing the CLI state file and the
// socket scan produced. A record is authoritative about the VM it describes, so its
// socket, PID and boot time replace whatever the other sources guessed — and a VM
// only the registry knows (one that is still booting, or whose socket is not in the
// scanned directory) is added. Everything else keeps its row and is marked as
// derived, because that is what it is: no record answered for it.
func mergeRecordedVMs(vms []*runtime.VMState, recorded []*runtime.VMState) []*runtime.VMState {
	byID := make(map[string]*runtime.VMState, len(vms)+len(recorded))
	for _, vm := range vms {
		byID[vm.ID] = vm
	}

	for _, record := range recorded {
		existing, ok := byID[record.ID]
		if !ok {
			byID[record.ID] = record
			continue
		}
		if record.SocketPath != "" {
			existing.SocketPath = record.SocketPath
		}
		if record.PID > 0 {
			existing.PID = record.PID
		}
		if !record.StartTime.IsZero() {
			existing.StartTime = record.StartTime
		}
		existing.Source = runtime.VMSourceRegistry
	}

	out := make([]*runtime.VMState, 0, len(byID))
	for _, vm := range byID {
		if vm.Source == "" {
			vm.Source = runtime.VMSourceDerived
		}
		out = append(out, vm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
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

// resolveRunningVM finds a running VM by exact ID or unique prefix and returns a
// synthesized state enriched with task metadata. The node's registry answers
// first — a record names the VM's socket, PID and boot time, and it knows a VM
// that is still booting — and the socket scan answers for a VM with no record,
// which is the one-release fallback.
func resolveRunningVM(ref string) (*runtime.VMState, error) {
	store := vmInstanceStore()
	recorded, records, err := loadRecordedVMs(store)
	if err != nil {
		return nil, err
	}
	if vm, found, matchErr := matchVM(recorded, ref); matchErr != nil {
		return nil, matchErr
	} else if found {
		enrichVMs([]*runtime.VMState{vm}, records)
		return vm, nil
	}

	running, err := runtime.DiscoverRunningVMs(vmSocketDir)
	if err != nil {
		return nil, err
	}
	if len(running) == 0 {
		return nil, fmt.Errorf("no running VMs found under %s", vmSocketDir)
	}

	discovered := make([]*runtime.VMState, 0, len(running))
	for _, vm := range running {
		discovered = append(discovered, &runtime.VMState{
			ID:         vm.ID,
			Status:     "running",
			SocketPath: vm.SocketPath,
			StartTime:  vm.Started,
			Source:     runtime.VMSourceDerived,
		})
	}

	vm, found, matchErr := matchVM(discovered, ref)
	if matchErr != nil {
		return nil, matchErr
	}
	if !found {
		available := make([]string, 0, len(discovered))
		for _, vm := range discovered {
			available = append(available, vm.ID)
		}
		return nil, fmt.Errorf("%q does not match any running VM (available: %s)", ref, strings.Join(available, ", "))
	}

	enrichVMs([]*runtime.VMState{vm}, records)
	return vm, nil
}

// matchVM finds ref among vms: an exact ID first, then a unique prefix. found is
// false when nothing matches, so a caller can ask the next source; an ambiguous
// prefix is an error, because no later source could resolve it either.
func matchVM(vms []*runtime.VMState, ref string) (*runtime.VMState, bool, error) {
	for _, vm := range vms {
		if vm.ID == ref {
			return vm, true, nil
		}
	}

	matches := make([]*runtime.VMState, 0, len(vms))
	for _, vm := range vms {
		if strings.HasPrefix(vm.ID, ref) {
			matches = append(matches, vm)
		}
	}

	switch len(matches) {
	case 0:
		return nil, false, nil
	case 1:
		return matches[0], true, nil
	default:
		names := make([]string, 0, len(matches))
		for _, vm := range matches {
			names = append(names, vm.ID)
		}
		return nil, false, fmt.Errorf("%q matches multiple running VMs: %s", ref, strings.Join(names, ", "))
	}
}
