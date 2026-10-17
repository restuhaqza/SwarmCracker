package swarmkit

import (
	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// createVMCgroup creates the task's per-task cgroup through the shared policy in
// pkg/jailer, so the daemon and `vm create` cannot drift apart. It returns true
// when a cgroup exists and the VMM process should be added to it.
//
// pkg/jailer owns the failure policy: a cpuset that cannot be applied is
// returned as an error (the VM never starts); a CPU/memory/IO quota problem is
// logged and the VM runs.
func (v *VMMManager) createVMCgroup(task *types.Task, vcpus, memoryMB int) (bool, error) {
	return jailer.CreateVMCgroup(v.cgroupMgr, v.cgroupErr, v.logger, jailer.CgroupRequest{
		Task:     task,
		VCPUs:    vcpus,
		MemoryMB: memoryMB,
	})
}

// addVMCgroupProcess attaches the started VMM process to its cgroup. Failing to
// do so means a requested cpuset never took effect, so that is a hard error; a
// non-cpuset VM merely runs unconstrained.
func (v *VMMManager) addVMCgroupProcess(task *types.Task, created bool, pid int) error {
	if !created {
		return nil
	}
	return jailer.AttachVMProcess(v.cgroupMgr, v.logger, task, pid)
}

// removeVMCgroup removes a task's cgroup, if any. It is safe to call when no
// cgroup exists and makes no calls at all when cgroups are disabled.
func (v *VMMManager) removeVMCgroup(taskID string) {
	jailer.RemoveVMCgroup(v.cgroupMgr, v.logger, taskID)
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
