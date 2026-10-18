package lifecycle

import (
	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// ensureCgroupManager returns the manager, creating the real one on first use.
//
// Creation is lazy — and its failure remembered — so a `vm create` without a
// cpuset annotation never touches /sys/fs/cgroup, exactly as before cgroups
// existed here.
func (vm *VMMManager) ensureCgroupManager() (jailer.CgroupOps, error) {
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

// prepareCgroup applies the task's cgroup when it asked to be pinned, through the
// same policy the daemon uses (pkg/jailer). It returns false — and makes no
// cgroup calls at all — when the task carries neither a cpuset nor a NUMA node
// list, which keeps `vm create` exactly as it was for every other VM.
//
// A cgroup is needed when either is requested: cpuset.cpus pins the CPUs and
// cpuset.mems binds the guest memory to the NUMA nodes, so a NUMA-only request
// has nowhere to go without a cgroup.
//
// A request that cannot be applied is a hard error, naming the request and the
// host's online CPUs; a CPU/memory/IO quota problem is only a warning and the
// returned bool still reports the created cgroup, so the placement takes effect.
func (vm *VMMManager) prepareCgroup(task *types.Task, config interface{}) (bool, error) {
	if jailer.CpusetRequest(task) == "" && jailer.NumaRequest(task) == "" {
		return false, nil
	}

	mgr, mgrErr := vm.ensureCgroupManager()
	vcpus, memoryMB := vmSizeFromConfig(config)
	return jailer.CreateVMCgroup(mgr, mgrErr, log.Logger, jailer.CgroupRequest{
		Task:     task,
		VCPUs:    vcpus,
		MemoryMB: memoryMB,
	})
}

// addCgroupProcess attaches the started VMM process to the cgroup prepareCgroup
// created. A cpuset that never took effect is a hard error naming the request.
func (vm *VMMManager) addCgroupProcess(task *types.Task, pid int) error {
	vm.cgroupMu.Lock()
	mgr := vm.cgroupMgr
	vm.cgroupMu.Unlock()
	return jailer.AttachVMProcess(mgr, log.Logger, task, pid)
}

// removeCgroup removes a task's cgroup, if any. It makes no calls when no cgroup
// manager exists (i.e. when the task never requested a cpuset).
func (vm *VMMManager) removeCgroup(taskID string) {
	vm.cgroupMu.Lock()
	mgr := vm.cgroupMgr
	vm.cgroupMu.Unlock()
	jailer.RemoveVMCgroup(mgr, log.Logger, taskID)
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
