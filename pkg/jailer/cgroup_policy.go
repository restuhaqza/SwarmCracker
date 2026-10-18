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

// NumaRequest returns the host NUMA-node list task asked its memory to be bound
// to, trimmed, or "" when it asked for none. Like CpusetRequest it is validated
// later by CreateVMCgroup, with the same cpulist parser.
func NumaRequest(task *types.Task) string {
	if task == nil {
		return ""
	}
	return strings.TrimSpace(task.Annotations[types.NumaLabel])
}

// CreateVMCgroup creates req's per-task cgroup and applies the one failure policy
// both VMM paths share. It returns true when a cgroup exists and the VMM process
// should be attached to it via AttachVMProcess.
//
// The policy:
//   - no cgroup manager and neither a cpuset nor a NUMA node list requested: no
//     cgroup calls at all, no error (the VM runs exactly as it did before
//     cgroups existed here);
//   - no cgroup manager but a placement requested: a fatal error naming the
//     request and why the manager is unavailable;
//   - a controller needed for the requested placement cannot be enabled: fatal;
//   - any placement stage (parse, cpuset.cpus, cpuset.mems) fails: fatal,
//     naming the request and the host's online CPUs, after removing the partial
//     cgroup;
//   - a CPU/memory/IO quota stage fails: a warning, and the cgroup is kept so the
//     pinning still takes effect;
//   - no placement requested and CreateCgroup fails: a warning, the partial
//     cgroup is removed, and the VM runs without limits.
//
// A placement failure is never downgraded to a warning.
func CreateVMCgroup(mgr CgroupOps, managerErr error, logger zerolog.Logger, req CgroupRequest) (bool, error) {
	taskID := ""
	if req.Task != nil {
		taskID = req.Task.ID
	}
	cpuset, numa := CpusetRequest(req.Task), NumaRequest(req.Task)
	requested := cpuset != "" || numa != ""

	if mgr == nil {
		if !requested {
			return false, nil
		}
		reason := "cgroups are disabled on this node (enable_cgroups=false)"
		if managerErr != nil {
			reason = "the cgroup manager could not be created: " + managerErr.Error()
		}
		return false, rejectPlacement(taskID, cpuset, numa, "unknown", errors.New(reason))
	}

	limits, err := deriveResourceLimits(req.Task, req.VCPUs, req.MemoryMB)
	if err != nil {
		return false, err
	}

	// Delegating the controllers is a prerequisite for the per-task cgroup: v2
	// only lists an undelegated controller in the child's file set.
	if err := mgr.EnsureControllers(DefaultControllers...); err != nil {
		if requested && controllersBlockCpuset(err) {
			return false, rejectPlacement(taskID, cpuset, numa, onlineCPUs(mgr), err)
		}
		logger.Warn().Err(err).Strs("controllers", DefaultControllers).
			Msg("Failed to enable cgroup controllers, continuing without the unavailable ones")
	}

	if err := mgr.CreateCgroup(taskID, limits); err != nil {
		if !requested {
			logger.Warn().Err(err).Str("task_id", taskID).
				Msg("Failed to create cgroup, running the VM without cgroup limits")
			// A failed create can still have left a partial directory behind.
			RemoveVMCgroup(mgr, logger, taskID)
			return false, nil
		}
		if IsQuotaStage(err) {
			// The placement is in place; only the quota could not be written.
			// Keep the cgroup so the pinning still takes effect.
			logger.Warn().Err(err).Str("task_id", taskID).
				Msg("Failed to apply cgroup quota limits, keeping the placement")
			return true, nil
		}
		RemoveVMCgroup(mgr, logger, taskID)
		return false, rejectPlacement(taskID, cpuset, numa, onlineCPUs(mgr), err)
	}

	return true, nil
}

// AttachVMProcess adds pid to task's cgroup, applying the same failure policy as
// CreateVMCgroup: a placement that never took effect (the process could not be
// attached) is a fatal error naming the request and the host's online CPUs; a
// task that asked for no placement merely runs without limits.
func AttachVMProcess(mgr CgroupOps, logger zerolog.Logger, task *types.Task, pid int) error {
	if mgr == nil || task == nil {
		return nil
	}

	if err := mgr.AddProcess(task.ID, pid); err != nil {
		cpuset, numa := CpusetRequest(task), NumaRequest(task)
		if cpuset != "" || numa != "" {
			return rejectPlacement(task.ID, cpuset, numa, onlineCPUs(mgr), err)
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
			return ResourceLimits{}, invalidPlacementAnnotation(task, "cpuset", types.CpusetLabel, resolved.CpusetCPUs, err)
		}
		limits.CpusetCPUs = cpus
	}

	// An explicit NUMA request overrides the auto-resolved memory nodes: the
	// manager only consults its parent/online fallback when CpusetMems is empty,
	// so a canonical non-empty value here wins. A malformed list is a hard error,
	// never a silent fallback to another node.
	if resolved.NumaNodes != "" {
		nodes, err := ParseCPUSet(resolved.NumaNodes)
		if err != nil {
			return ResourceLimits{}, invalidPlacementAnnotation(task, "NUMA node", types.NumaLabel, resolved.NumaNodes, err)
		}
		limits.CpusetMems = nodes
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

// invalidPlacementAnnotation builds the hard error for a placement annotation
// that is not valid Linux cpulist syntax. The cpuset and the NUMA-node request
// are both cpulists parsed by ParseCPUSet, so they share one constructor: the
// two rejections name the label, the value, and the task in the same way, and
// cannot drift apart. Both wrap the underlying parse error rather than
// introducing a type, matching the rest of this file.
func invalidPlacementAnnotation(task *types.Task, noun, label, value string, cause error) error {
	taskID := ""
	if task != nil {
		taskID = task.ID
	}
	return fmt.Errorf("invalid %s annotation %s=%q on task %s: %w",
		noun, label, value, taskID, cause)
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
// produce: it names the request and the host's online CPUs, each exactly once.
// A typed cgroup failure already describes its stage, and a cpuset write also
// names the host list it collided with, so those parts are not repeated.
func rejectCpuset(taskID, request, online string, cause error) error {
	var cgroupErr *CgroupError
	if errors.As(cause, &cgroupErr) && cgroupErr.TaskID == taskID {
		if cgroupErr.hostHinted {
			return fmt.Errorf("cannot apply cpuset %q to task %s: %s",
				request, taskID, cgroupErr.detail())
		}
		return fmt.Errorf("cannot apply cpuset %q to task %s: %s (host online CPUs: %s)",
			request, taskID, cgroupErr.detail(), online)
	}
	return fmt.Errorf("cannot apply cpuset %q to task %s: %v (host online CPUs: %s)",
		request, taskID, cause, online)
}

// rejectPlacement builds the hard error a placement request that cannot be
// applied must produce. A cpuset keeps its established message; a NUMA-only
// request names the node list so the operator can tell which request failed. A
// typed cgroup failure already describes its stage and, for cpuset.mems, names
// the host node list it collided with, so those parts are not repeated.
func rejectPlacement(taskID, cpuset, numa, online string, cause error) error {
	if cpuset != "" {
		return rejectCpuset(taskID, cpuset, online, cause)
	}

	var cgroupErr *CgroupError
	if errors.As(cause, &cgroupErr) && cgroupErr.TaskID == taskID {
		return fmt.Errorf("cannot apply NUMA node list %q to task %s: %s",
			numa, taskID, cgroupErr.detail())
	}
	return fmt.Errorf("cannot apply NUMA node list %q to task %s: %v",
		numa, taskID, cause)
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
