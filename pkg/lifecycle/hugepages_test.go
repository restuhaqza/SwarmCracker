package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hugepagesMeminfo is a host with 512 allocated 2M pages, 128 free (256 MiB
// available). A 512 MiB `vm create` request needs 256 pages and must be refused;
// a 256 MiB request needs 128 and fits.
const hugepagesMeminfo = "Hugepagesize:       2048 kB\n" +
	"HugePages_Total:     512\n" +
	"HugePages_Free:      128\n"

// writeHugepagesFixture writes a /proc/meminfo-style fixture for the pool seam.
func writeHugepagesFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meminfo")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

// hugepagesTask is a `vm create`-shaped task with an optional hugepages request.
func hugepagesTask(request string) *types.Task {
	annotations := map[string]string{}
	if request != "" {
		annotations[types.HugepagesLabel] = request
	}
	return &types.Task{ID: "task-lifecycle-hugepages", Annotations: annotations}
}

// TestStart_RejectsShortHugepagesPoolBeforeAnythingStarts pins that the pool is
// checked before the cgroup or the VMM exists, so a refused request leaves no
// process and no cgroup behind.
func TestStart_RejectsShortHugepagesPoolBeforeAnythingStarts(t *testing.T) {
	fake := &fakeCgroupManager{online: "0-3"}
	vm := newCgroupVMM(t, fake)
	vm.hugepagesPoolPath = writeHugepagesFixture(t, hugepagesMeminfo)

	err := vm.Start(context.Background(), hugepagesTask("true"), machineConfig(1, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task-lifecycle-hugepages")
	assert.Contains(t, err.Error(), "512 MiB")
	assert.Contains(t, err.Error(), "128 free of 512 allocated")

	creates, removed, ensure := fake.snapshot()
	assert.Empty(t, ensure, "no cgroup may be created for a refused request")
	assert.Empty(t, creates)
	assert.Empty(t, removed, "nothing was created, so nothing needs removing")
	assert.Empty(t, vm.vms, "a refused request must not register a VM")
}

// TestStart_LargeEnoughHugepagesPoolProceeds pins the accepting side: the check
// passes and Start reaches the next step (the missing binary).
func TestStart_LargeEnoughHugepagesPoolProceeds(t *testing.T) {
	t.Setenv("PATH", "")

	vm := newCgroupVMM(t, nil)
	vm.hugepagesPoolPath = writeHugepagesFixture(t, hugepagesMeminfo)

	err := vm.Start(context.Background(), hugepagesTask("true"), machineConfig(1, 256))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "firecracker binary not found")
	assert.NotContains(t, err.Error(), "huge pages")
}

// TestPrepareCgroup_NumaOnlyCreatesCgroup pins the widened gate: a NUMA node
// list alone must create a cgroup, because cpuset.mems has nowhere else to go.
func TestPrepareCgroup_NumaOnlyCreatesCgroup(t *testing.T) {
	fake := &fakeCgroupManager{online: "0-3"}
	vm := newCgroupVMM(t, fake)

	task := &types.Task{ID: "task-lifecycle-numa", Annotations: map[string]string{types.NumaLabel: "1,0"}}

	created, err := vm.prepareCgroup(task, machineConfig(2, 512))
	require.NoError(t, err)
	assert.True(t, created)

	creates, _, ensure := fake.snapshot()
	require.Len(t, ensure, 1)
	require.Len(t, creates, 1)
	assert.Equal(t, "0-1", creates[0].limits.CpusetMems)
	assert.Empty(t, creates[0].limits.CpusetCPUs, "a NUMA-only request must not pin CPUs")
}

// TestPrepareCgroup_NeitherRequestIsANoOp pins that the widened gate did not
// start touching cgroups for every other VM.
func TestPrepareCgroup_NeitherRequestIsANoOp(t *testing.T) {
	fake := &fakeCgroupManager{online: "0-3"}
	vm := newCgroupVMM(t, fake)

	created, err := vm.prepareCgroup(&types.Task{ID: "plain"}, machineConfig(1, 512))
	require.NoError(t, err)
	assert.False(t, created)

	creates, removed, ensure := fake.snapshot()
	assert.Empty(t, ensure)
	assert.Empty(t, creates)
	assert.Empty(t, removed)
}
