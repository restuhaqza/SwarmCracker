package swarmkit

import (
	"errors"
	"fmt"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// cgroupControllers are the cgroup v2 controllers the daemon delegates before
// creating a per-task cgroup. cpuset is requested unconditionally: delegation is
// additive and reports per-controller availability, which is what lets a cpuset
// request be refused with a reason instead of by a failed cpuset.cpus write.
var cgroupControllers = []string{"cpu", "memory", "io", "cpuset"}

// cgroupManager is the subset of *jailer.CgroupManager the VMM needs. It exists
// so both start paths can be driven by a fake in tests: no unit test may write to
// /sys/fs/cgroup. The default implementation is the real cgroup manager.
type cgroupManager interface {
	EnsureControllers(controllers ...string) error
	CreateCgroup(taskID string, limits jailer.ResourceLimits) error
	AddProcess(taskID string, pid int) error
	RemoveCgroup(taskID string) error
	OnlineCPUs() (string, error)
}

var _ cgroupManager = (*jailer.CgroupManager)(nil)

// cpusetRequest returns the cpuset a task asked for, trimmed, or "" when it
// asked for none. The value is not validated here: that happens in
// cgroupResourceLimits via jailer.ParseCPUSet.
func cpusetRequest(task *types.Task) string {
	if task == nil {
		return ""
	}
	return strings.TrimSpace(task.Annotations[types.CpusetLabel])
}

// cgroupResourceLimits turns a task's declared resources into the cgroup limits
// for its VMM process. CPU and memory declared on the task win; otherwise the
// VM's own resolved size (vcpus/memoryMB, as booted) provides the cap, so a VM
// is never left unconstrained just because the spec was silent.
//
// The cpuset is validated and canonicalised here with jailer.ParseCPUSet — the
// one authoritative parser — because pkg/types deliberately does not know the
// cpuset syntax.
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

	// Fall back to the VM's resolved size: one microsecond-second of quota per
	// vCPU (1 vCPU = 1e9 nanoCPUs = 1e6 us), and the guest's memory plus the
	// VMM's headroom.
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

// onlineCPUsFor reports the host's online CPUs for an error message. It is
// best-effort: a manager that cannot be read says "unknown" instead of masking
// the original failure.
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
// per controller, so an unavailable cpu/memory/io controller (a quota-only
// problem) still lets the cpuset through. Any other error is treated as
// blocking, because the controllers' state cannot be established.
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
// the cpuset was applied, i.e. while writing cpu.max or memory.max. In that case
// the cgroup is usable for pinning and the quota problem is only a warning.
func cgroupFailureIsQuotaOnly(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "failed to set CPU limits") ||
		strings.Contains(msg, "failed to set memory limits")
}

// createVMCgroup resolves the task's limits and creates its cgroup. It returns
// true when a cgroup exists and the VMM process should be added to it.
//
// Failure policy: a cpuset that cannot be applied is returned as an error, so
// the VM never starts; a CPU/memory/IO quota problem is logged and the VM runs.
// When cgroups are disabled or the manager could not be created, a task that
// requests no cpuset is left exactly as before (no cgroup calls at all), while a
// task that does request one is rejected with the reason.
func (v *VMMManager) createVMCgroup(task *types.Task, vcpus, memoryMB int) (bool, error) {
	request := cpusetRequest(task)

	if v.cgroupMgr == nil {
		if request == "" {
			return false, nil
		}
		reason := "cgroups are disabled on this node (enable_cgroups=false)"
		if v.cgroupErr != nil {
			reason = "the cgroup manager could not be created: " + v.cgroupErr.Error()
		}
		return false, cpusetRejection(task.ID, request, "unknown", reason)
	}

	limits, err := cgroupResourceLimits(task, vcpus, memoryMB)
	if err != nil {
		return false, err
	}

	// Delegating the controllers is a prerequisite for the per-task cgroup: v2
	// only lists an undelegated controller in the child's file set.
	if err := v.cgroupMgr.EnsureControllers(cgroupControllers...); err != nil {
		if request != "" && controllerFailureBlocksCpuset(err) {
			return false, cpusetRejection(task.ID, request, onlineCPUsFor(v.cgroupMgr), err.Error())
		}
		v.logger.Warn().Err(err).Strs("controllers", cgroupControllers).
			Msg("Failed to enable cgroup controllers, continuing without the unavailable ones")
	}

	if err := v.cgroupMgr.CreateCgroup(task.ID, limits); err != nil {
		if request == "" {
			v.logger.Warn().Err(err).Str("task_id", task.ID).
				Msg("Failed to create cgroup, running the VM without cgroup limits")
			// A failed create can still have left a partial directory behind.
			v.removeVMCgroup(task.ID)
			return false, nil
		}
		if cgroupFailureIsQuotaOnly(err) {
			// The cpuset is in place; only the quota could not be written. Keep
			// the cgroup so the pinning still takes effect.
			v.logger.Warn().Err(err).Str("task_id", task.ID).
				Msg("Failed to apply cgroup quota limits, keeping the cpuset")
			return true, nil
		}
		v.removeVMCgroup(task.ID)
		return false, cpusetRejection(task.ID, request, onlineCPUsFor(v.cgroupMgr), err.Error())
	}

	return true, nil
}

// addVMCgroupProcess attaches the started VMM process to its cgroup. Failing to
// do so means a requested cpuset never took effect, so that is a hard error; a
// non-cpuset VM merely runs unconstrained.
func (v *VMMManager) addVMCgroupProcess(task *types.Task, created bool, pid int) error {
	if !created || v.cgroupMgr == nil {
		return nil
	}

	if err := v.cgroupMgr.AddProcess(task.ID, pid); err != nil {
		if request := cpusetRequest(task); request != "" {
			return cpusetRejection(task.ID, request, onlineCPUsFor(v.cgroupMgr), err.Error())
		}
		v.logger.Warn().Err(err).Str("task_id", task.ID).
			Msg("Failed to add the VM process to its cgroup, running the VM without cgroup limits")
	}

	return nil
}

// removeVMCgroup removes a task's cgroup, if any. It is safe to call when no
// cgroup exists (the jailer manager tolerates a missing directory) and makes no
// calls at all when cgroups are disabled.
func (v *VMMManager) removeVMCgroup(taskID string) {
	if v.cgroupMgr == nil {
		return
	}
	if err := v.cgroupMgr.RemoveCgroup(taskID); err != nil {
		v.logger.Warn().Err(err).Str("task_id", taskID).Msg("Failed to remove cgroup")
	}
}

// machineSize extracts the VM's resolved vCPU count and memory (MiB) from a
// translator config. A config that does not carry a machine-config yields
// zeroes, which simply means "no fallback from the guest size": the cgroup then
// uses the task's declared limits, if any.
func machineSize(config interface{}) (vcpus, memoryMB int) {
	cfg, ok := config.(map[string]interface{})
	if !ok {
		return 0, 0
	}
	machine, ok := cfg["machine-config"].(map[string]interface{})
	if !ok {
		return 0, 0
	}
	return toInt(machine["vcpu_count"]), toInt(machine["mem_size_mib"])
}
