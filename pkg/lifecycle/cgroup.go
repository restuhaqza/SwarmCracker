package lifecycle

import (
	"errors"
	"fmt"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// cgroupControllers are the cgroup v2 controllers the manager delegates before
// creating a per-task cgroup.
var cgroupControllers = []string{"cpu", "memory", "io", "cpuset"}

// cgroupManager is the subset of *jailer.CgroupManager the lifecycle manager
// needs. It is duplicated rather than shared with pkg/swarmkit so the CLI's VM
// path does not pull in the SwarmKit executor's dependencies; the shape matches
// the daemon's seam exactly, and it is the injection point for tests (no unit
// test may write to /sys/fs/cgroup).
type cgroupManager interface {
	EnsureControllers(controllers ...string) error
	CreateCgroup(taskID string, limits jailer.ResourceLimits) error
	AddProcess(taskID string, pid int) error
	RemoveCgroup(taskID string) error
	OnlineCPUs() (string, error)
}

var _ cgroupManager = (*jailer.CgroupManager)(nil)

// cpusetRequest returns the cpuset a task asked for, trimmed, or "" when it
// asked for none.
func cpusetRequest(task *types.Task) string {
	if task == nil {
		return ""
	}
	return strings.TrimSpace(task.Annotations[types.CpusetLabel])
}

// ensureCgroupManager returns the manager, creating the real one on first use.
//
// Creation is lazy — and its failure remembered — so a `vm create` without a
// cpuset annotation never touches /sys/fs/cgroup, exactly as before cgroups
// existed here.
func (vm *VMMManager) ensureCgroupManager() (cgroupManager, error) {
	vm.cgroupMu.Lock()
	defer vm.cgroupMu.Unlock()

	if vm.cgroupMgr != nil {
		return vm.cgroupMgr, nil
	}
	if vm.cgroupErr != nil {
		return nil, vm.cgroupErr
	}
	mgr, err := jailer.NewCgroupManager("")
	if err != nil {
		vm.cgroupErr = err
		return nil, err
	}
	vm.cgroupMgr = mgr
	return mgr, nil
}

// cgroupResourceLimits turns a task's declared resources into its VMM cgroup
// limits. Declared CPU/memory win; otherwise the VM's own size (vcpus/memoryMB)
// provides the cap. The cpuset is validated and canonicalised here with
// jailer.ParseCPUSet, the one authoritative parser; pkg/types deliberately does
// not know the syntax.
func cgroupResourceLimits(task *types.Task, vcpus, memoryMB int) (jailer.ResourceLimits, error) {
	resolved, err := types.ResolveCgroupLimits(task)
	if err != nil {
		return jailer.ResourceLimits{}, err
	}

	limits := jailer.ResourceLimits{
		CPUQuotaUs: resolved.CPUQuotaUs,
		MemoryMax:  resolved.MemoryMaxB,
		IOWeight:   100,
	}

	if resolved.CpusetCPUs != "" {
		cpus, err := jailer.ParseCPUSet(resolved.CpusetCPUs)
		if err != nil {
			return jailer.ResourceLimits{}, fmt.Errorf("invalid cpuset annotation %s=%q on task %s: %w",
				types.CpusetLabel, resolved.CpusetCPUs, task.ID, err)
		}
		limits.CpusetCPUs = cpus
	}

	if limits.CPUQuotaUs == 0 && vcpus > 0 {
		limits.CPUQuotaUs = int64(vcpus) * 1_000_000
	}
	if limits.MemoryMax == 0 && memoryMB > 0 {
		limits.MemoryMax = int64(memoryMB)<<20 + types.CgroupMemoryHeadroom
	}
	if limits.MemoryMax > 0 {
		limits.MemoryHigh = limits.MemoryMax * 9 / 10
	}

	return limits, nil
}

// onlineCPUsFor reports the host's online CPUs for an error message; a manager
// that cannot be read says "unknown" instead of masking the original failure.
func onlineCPUsFor(mgr cgroupManager) string {
	if mgr == nil {
		return "unknown"
	}
	cpus, err := mgr.OnlineCPUs()
	if err != nil || cpus == "" {
		return "unknown"
	}
	return cpus
}

// cpusetRejection builds the hard error a cpuset that cannot be applied must
// produce: it names the request and the host's online CPUs.
func cpusetRejection(taskID, request, online, reason string) error {
	return fmt.Errorf("cannot apply cpuset %q to task %s: %s (host online CPUs: %s)",
		request, taskID, reason, online)
}

// controllerFailureBlocksCpuset reports whether an EnsureControllers failure
// makes a requested cpuset unapplicable. A *jailer.ControllersError is inspected
// per controller, so an unavailable cpu/memory/io controller still lets the
// cpuset through. Any other error is treated as blocking, because the
// controllers' state cannot be established.
func controllerFailureBlocksCpuset(err error) bool {
	var ctrlErr *jailer.ControllersError
	if !errors.As(err, &ctrlErr) {
		return true
	}
	for _, c := range ctrlErr.Unavailable {
		if c == "cpuset" {
			return true
		}
	}
	return ctrlErr.Failures["cpuset"] != ""
}

// cgroupFailureIsQuotaOnly reports whether a CreateCgroup failure happened after
// the cpuset was applied, i.e. while writing cpu.max or memory.max.
func cgroupFailureIsQuotaOnly(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "failed to set CPU limits") ||
		strings.Contains(msg, "failed to set memory limits")
}

// prepareCgroup applies the task's cgroup when it asked to be pinned. It returns
// false (and makes no cgroup calls at all) when the task carries no cpuset
// annotation, which keeps `vm create` exactly as it was for every other VM.
//
// A cpuset that cannot be applied is a hard error, naming the request and the
// host's online CPUs; a CPU/memory/IO quota problem is only a warning and the
// returned bool still reports the created cgroup, so the pinning takes effect.
func (vm *VMMManager) prepareCgroup(task *types.Task, config interface{}) (bool, error) {
	request := cpusetRequest(task)
	if request == "" {
		return false, nil
	}

	mgr, err := vm.ensureCgroupManager()
	if err != nil {
		return false, cpusetRejection(task.ID, request, "unknown",
			"the cgroup manager could not be created: "+err.Error())
	}

	vcpus, memoryMB := vmSizeFromConfig(config)
	limits, err := cgroupResourceLimits(task, vcpus, memoryMB)
	if err != nil {
		return false, err
	}

	if err := mgr.EnsureControllers(cgroupControllers...); err != nil {
		if controllerFailureBlocksCpuset(err) {
			return false, cpusetRejection(task.ID, request, onlineCPUsFor(mgr), err.Error())
		}
		log.Warn().Err(err).Strs("controllers", cgroupControllers).
			Msg("Failed to enable cgroup controllers, continuing without the unavailable ones")
	}

	if err := mgr.CreateCgroup(task.ID, limits); err != nil {
		if cgroupFailureIsQuotaOnly(err) {
			log.Warn().Err(err).Str("task_id", task.ID).
				Msg("Failed to apply cgroup quota limits, keeping the cpuset")
			return true, nil
		}
		vm.removeCgroup(task.ID)
		return false, cpusetRejection(task.ID, request, onlineCPUsFor(mgr), err.Error())
	}

	return true, nil
}

// addCgroupProcess attaches the started VMM process to the cgroup prepareCgroup
// created. A cpuset that never took effect is a hard error naming the request.
func (vm *VMMManager) addCgroupProcess(task *types.Task, pid int) error {
	mgr, err := vm.ensureCgroupManager()
	if err != nil {
		return cpusetRejection(task.ID, cpusetRequest(task), "unknown",
			"the cgroup manager could not be created: "+err.Error())
	}
	if err := mgr.AddProcess(task.ID, pid); err != nil {
		return cpusetRejection(task.ID, cpusetRequest(task), onlineCPUsFor(mgr), err.Error())
	}
	return nil
}

// removeCgroup removes a task's cgroup, if any. It makes no calls when no cgroup
// manager exists (i.e. when the task never requested a cpuset) and tolerates a
// missing directory.
func (vm *VMMManager) removeCgroup(taskID string) {
	vm.cgroupMu.Lock()
	mgr := vm.cgroupMgr
	vm.cgroupMu.Unlock()
	if mgr == nil {
		return
	}
	if err := mgr.RemoveCgroup(taskID); err != nil {
		log.Warn().Err(err).Str("task_id", taskID).Msg("Failed to remove cgroup")
	}
}

// vmSizeFromConfig extracts the VM's resolved vCPU count and memory (MiB) from a
// translator config. A config without a machine-config yields zeroes, meaning
// "no fallback from the guest size".
func vmSizeFromConfig(config interface{}) (vcpus, memoryMB int) {
	configMap, ok := config.(map[string]interface{})
	if !ok {
		return 0, 0
	}
	machine, ok := configMap["machine-config"].(map[string]interface{})
	if !ok {
		return 0, 0
	}
	vcpus = toInt(machine["vcpu_count"])
	memoryMB = toInt(machine["mem_size_mib"])
	return vcpus, memoryMB
}

// toInt converts a decoded JSON/number value to an int, or 0 when it is not a
// number at all.
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}
