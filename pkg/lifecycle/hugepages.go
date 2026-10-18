package lifecycle

import (
	"fmt"

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// defaultHugepagesPoolPath is where the host's hugepage pool is reported. The
// manager keeps a field for it so tests can use a fixture instead of the host.
const defaultHugepagesPoolPath = "/proc/meminfo"

// checkHugepages enforces a `vm create` task's hugepage request before the VMM
// starts. The memory size comes from the same machine-config the boot uses, so
// the check and the guest cannot disagree. A task that did not ask for
// hugepages returns immediately and never reads the host pool.
func (vm *VMMManager) checkHugepages(task *types.Task, config interface{}) error {
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

	path := vm.hugepagesPoolPath
	if path == "" {
		path = defaultHugepagesPoolPath
	}
	pool, err := jailer.ReadHugepagesPool(path)
	if err != nil {
		return fmt.Errorf("cannot verify hugepages for task %s: %w", task.ID, err)
	}

	_, memoryMB := vmSizeFromConfig(config)
	if err := jailer.CheckHugepages(pool, memoryMB); err != nil {
		return fmt.Errorf("task %s: %w", task.ID, err)
	}
	return nil
}
