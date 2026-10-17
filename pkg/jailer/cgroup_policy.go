package jailer

import (
	"errors"
	"fmt"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
)

// DefaultControllers are the cgroup v2 controllers a VM's per-task cgroup needs.
// cpuset is requested unconditionally: delegation is additive and reports
// per-controller availability, which is what lets a cpuset request be refused
// with a reason instead of by a failed cpuset.cpus write.
var DefaultControllers = []string{"cpu", "memory", "io", "cpuset"}

// CgroupOps is the cgroup-operation subset the shared VM policy needs. It is
// implemented by *CgroupManager and is the injection point for the VMM tests: no
// unit test may write to /sys/fs/cgroup.
type CgroupOps interface {
	EnsureControllers(controllers ...string) error
	CreateCgroup(taskID string, limits ResourceLimits) error
	AddProcess(taskID string, pid int) error
	RemoveCgroup(taskID string) error
	OnlineCPUs() (string, error)
}

// CgroupRequest is one VM's cgroup intent: the task it belongs to and the VM
// size to fall back to when the spec declares no CPU/memory limits.
type CgroupRequest struct {
	Task     *types.Task
	VCPUs    int
	MemoryMB int
}

// CpusetRequest returns the cpuset task asked to be pinned to, trimmed, or ""
// when it asked for none. The value is not validated here: CreateVMCgroup
// canonicalises it with ParseCPUSet, the one authoritative parser
// (pkg/types deliberately does not know the syntax).
func CpusetRequest(task *types.Task) string {
	if task == nil {
		return ""
	}
	return strings.TrimSpace(task.Annotations[types.CpusetLabel])
}

// CreateVMCgroup creates req's per-task cgroup and applies the one failure policy
// both VMM paths share. It returns true when a cgroup exists and the VMM process
// should be attached to it via AttachVMProcess.
//
// The policy:
//   - no cgroup manager and no cpuset requested: no cgroup calls at all, no
//     error (the VM runs exactly as it did before cgroups existed here);
//   - no cgroup manager but a cpuset requested: a fatal error naming the request
//     and why the manager is unavailable;
//   - a controller needed for the requested cpuset cannot be enabled: fatal;
//   - any cpuset stage (parse, cpuset.cpus, cpuset.mems) fails: fatal, naming the
//     request and the host's online CPUs, after removing the partial cgroup;
//   - a CPU/memory/IO quota stage fails: a warning, and the cgroup is kept so the
//     pinning still takes effect;
//   - no cpuset requested and CreateCgroup fails: a warning, the partial cgroup
//     is removed, and the VM runs without limits.
//
// A cpuset failure is never downgraded to a warning.
func CreateVMCgroup(mgr CgroupOps, managerErr error, logger zerolog.Logger, req CgroupRequest) (bool, error) {
	taskID := ""
	if req.Task != nil {
		taskID = req.Task.ID
	}
	request := CpusetRequest(req.Task)

	if mgr == nil {
		if request == "" {
			return false, nil
		}
		reason := "cgroups are disabled on this node (enable_cgroups=false)"
		if managerErr != nil {
			reason = "the cgroup manager could not be created: " + managerErr.Error()
		}
		return false, rejectCpuset(taskID, request, "unknown", reason)
	}

	limits, err := deriveResourceLimits(req.Task, req.VCPUs, req.MemoryMB)
	if err != nil {
		return false, err
	}

	// Delegating the controllers is a prerequisite for the per-task cgroup: v2
	// only lists an undelegated controller in the child's file set.
	if err := mgr.EnsureControllers(DefaultControllers...); err != nil {
		if request != "" && controllersBlockCpuset(err) {
			return false, rejectCpuset(taskID, request, onlineCPUs(mgr), err.Error())
		}
		logger.Warn().Err(err).Strs("controllers", DefaultControllers).
			Msg("Failed to enable cgroup controllers, continuing without the unavailable ones")
	}

	if err := mgr.CreateCgroup(taskID, limits); err != nil {
		if request == "" {
			logger.Warn().Err(err).Str("task_id", taskID).
				Msg("Failed to create cgroup, running the VM without cgroup limits")
			// A failed create can still have left a partial directory behind.
			RemoveVMCgroup(mgr, logger, taskID)
			return false, nil
		}
		if IsQuotaStage(err) {
			// The cpuset is in place; only the quota could not be written. Keep
			// the cgroup so the pinning still takes effect.
			logger.Warn().Err(err).Str("task_id", taskID).
				Msg("Failed to apply cgroup quota limits, keeping the cpuset")
			return true, nil
		}
		RemoveVMCgroup(mgr, logger, taskID)
		return false, rejectCpuset(taskID, request, onlineCPUs(mgr), err.Error())
	}

	return true, nil
}

// AttachVMProcess adds pid to task's cgroup, applying the same failure policy as
// CreateVMCgroup: a cpuset that never took effect (the process could not be
// attached) is a fatal error naming the request and the host's online CPUs; a
// task that asked for no cpuset merely runs without limits.
func AttachVMProcess(mgr CgroupOps, logger zerolog.Logger, task *types.Task, pid int) error {
	if mgr == nil || task == nil {
		return nil
	}

	if err := mgr.AddProcess(task.ID, pid); err != nil {
		if request := CpusetRequest(task); request != "" {
			return rejectCpuset(task.ID, request, onlineCPUs(mgr), err.Error())
		}
		logger.Warn().Err(err).Str("task_id", task.ID).
			Msg("Failed to add the VM process to its cgroup, running the VM without cgroup limits")
	}

	return nil
}

// RemoveVMCgroup removes a task's cgroup, if any. It is safe to call when no
// cgroup exists (the manager tolerates a missing directory) and makes no calls at
// all when cgroups are disabled. A failed removal is a warning: teardown must not
// fail because a cgroup was already gone.
func RemoveVMCgroup(mgr CgroupOps, logger zerolog.Logger, taskID string) {
	if mgr == nil {
		return
	}
	if err := mgr.RemoveCgroup(taskID); err != nil {
		logger.Warn().Err(err).Str("task_id", taskID).Msg("Failed to remove cgroup")
	}
}

// deriveResourceLimits turns a task's declared resources into the cgroup limits
// for its VMM process. CPU and memory declared on the task win; otherwise the
// VM's own resolved size (vcpus/memoryMB, as booted) provides the cap, so a VM is
// never left unconstrained just because the spec was silent.
func deriveResourceLimits(task *types.Task, vcpus, memoryMB int) (ResourceLimits, error) {
	resolved, err := types.ResolveCgroupLimits(task)
	if err != nil {
		return ResourceLimits{}, err
	}

	limits := ResourceLimits{
		CPUQuotaUs: resolved.CPUQuotaUs,
		MemoryMax:  resolved.MemoryMaxB,
		IOWeight:   100,
	}

	if resolved.CpusetCPUs != "" {
		cpus, err := ParseCPUSet(resolved.CpusetCPUs)
		if err != nil {
			taskID := ""
			if task != nil {
				taskID = task.ID
			}
			return ResourceLimits{}, fmt.Errorf("invalid cpuset annotation %s=%q on task %s: %w",
				types.CpusetLabel, resolved.CpusetCPUs, taskID, err)
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

// onlineCPUs reports the host's online CPUs for an error message. It is
// best-effort: a manager that cannot be read says "unknown" instead of masking
// the original failure.
func onlineCPUs(mgr CgroupOps) string {
	if mgr == nil {
		return "unknown"
	}
	cpus, err := mgr.OnlineCPUs()
	if err != nil || cpus == "" {
		return "unknown"
	}
	return cpus
}

// rejectCpuset builds the hard error a cpuset that cannot be applied must
// produce: it names the request and the host's online CPUs.
func rejectCpuset(taskID, request, online, reason string) error {
	return fmt.Errorf("cannot apply cpuset %q to task %s: %s (host online CPUs: %s)",
		request, taskID, reason, online)
}

// controllersBlockCpuset reports whether an EnsureControllers failure makes a
// requested cpuset unapplicable. A *ControllersError is inspected per controller,
// so an unavailable cpu/memory/io controller (a quota-only problem) still lets
// the cpuset through. Any other error is treated as blocking, because the
// controllers' state cannot be established.
func controllersBlockCpuset(err error) bool {
	var ctrlErr *ControllersError
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
