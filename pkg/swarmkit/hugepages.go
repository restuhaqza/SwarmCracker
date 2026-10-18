package swarmkit

import (
	"fmt"

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// defaultHugepagesPoolPath is where the host's hugepage pool is reported. The
// manager keeps a field for it so tests can use a fixture instead of the host.
const defaultHugepagesPoolPath = "/proc/meminfo"

// checkHugepages enforces a task's hugepage request before either start path
// creates anything. It is the single check point for both paths: Start calls it
// once, before dispatching to startDirect or startWithJailer, so a refusal
// cannot leave a process or a cgroup behind.
//
// The memory size is the one the config already resolved (machine-config's
// mem_size_mib), so the check and the boot cannot disagree. A task that did not
// ask for hugepages returns immediately and never reads the host pool.
func (v *VMMManager) checkHugepages(task *types.Task, config interface{}) error {
	if task == nil {
		return nil
	}

	requested, err := types.ParseHugepages(task.Annotations[types.HugepagesLabel])
	if err != nil {
		return fmt.Errorf("task %s: %w", task.ID, err)
	}
	if !requested {
		return nil
	}

	path := v.hugepagesPoolPath
	if path == "" {
		path = defaultHugepagesPoolPath
	}
	pool, err := jailer.ReadHugepagesPool(path)
	if err != nil {
		return fmt.Errorf("cannot verify hugepages for task %s: %w", task.ID, err)
	}

	_, memoryMB := machineSize(config)
	if err := jailer.CheckHugepages(pool, memoryMB); err != nil {
		return fmt.Errorf("task %s: %w", task.ID, err)
	}
	return nil
}
