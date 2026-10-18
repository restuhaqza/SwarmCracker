package swarmkit

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
// available). A 512 MiB request needs 256 pages and must be refused; a 256 MiB
// request needs 128 and fits.
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

// hugepagesTask is a daemon-shaped task with an optional hugepages request.
func hugepagesTask(request string) *types.Task {
	annotations := map[string]string{}
	if request != "" {
		annotations[types.HugepagesLabel] = request
	}
	return &types.Task{ID: "task-hugepages", Annotations: annotations}
}

// TestStart_RejectsShortHugepagesPoolBeforeAnythingStarts pins the single check
// point: Start refuses the task before either start path runs, so neither a
// process nor a cgroup is created.
func TestStart_RejectsShortHugepagesPoolBeforeAnythingStarts(t *testing.T) {
	for _, useJailer := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "jailer"}[useJailer], func(t *testing.T) {
			fake := &fakeCgroupManager{online: "0-7"}
			vmm := cgroupTestVMM(t, fake)
			vmm.useJailer = useJailer
			vmm.hugepagesPoolPath = writeHugepagesFixture(t, hugepagesMeminfo)

			err := vmm.Start(context.Background(), hugepagesTask("true"), cgroupTestConfig(1, 512))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "task-hugepages")
			assert.Contains(t, err.Error(), "512 MiB")
			assert.Contains(t, err.Error(), "128 free of 512 allocated")

			created, added, _, ensure := fake.snapshot()
			assert.Empty(t, ensure, "no cgroup may be created for a refused request")
			assert.Empty(t, created)
			assert.Empty(t, added)
			assert.Empty(t, vmm.processes, "a refused request must leave no process behind")
		})
	}
}

// TestStart_ChecksTheResolvedMemorySize pins that the check uses the same memory
// size the config boots: 256 MiB needs 128 pages, exactly what the pool has.
func TestStart_ChecksTheResolvedMemorySize(t *testing.T) {
	vmm := cgroupTestVMM(t, nil)
	vmm.hugepagesPoolPath = writeHugepagesFixture(t, hugepagesMeminfo)

	err := vmm.Start(context.Background(), hugepagesTask("true"), cgroupTestConfig(1, 256))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start firecracker",
		"the pool check must pass and the start must reach the missing binary")
	assert.NotContains(t, err.Error(), "huge pages")
}

// TestStart_NoHugepagesRequestDoesNotReadThePool pins the zero-behaviour change:
// the pool path is only touched when the task asked for hugepages.
func TestStart_NoHugepagesRequestDoesNotReadThePool(t *testing.T) {
	fake := &fakeCgroupManager{online: "0-7"}
	vmm := cgroupTestVMM(t, fake)
	vmm.hugepagesPoolPath = filepath.Join(t.TempDir(), "does-not-exist")

	err := vmm.Start(context.Background(), hugepagesTask(""), cgroupTestConfig(1, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start firecracker")
	assert.NotContains(t, err.Error(), "cannot verify hugepages")
}

// TestStart_RejectsMalformedHugepagesValue pins that a bad annotation fails the
// task instead of silently booting with 4K memory.
func TestStart_RejectsMalformedHugepagesValue(t *testing.T) {
	vmm := cgroupTestVMM(t, &fakeCgroupManager{})
	vmm.hugepagesPoolPath = writeHugepagesFixture(t, hugepagesMeminfo)

	err := vmm.Start(context.Background(), hugepagesTask("yes"), cgroupTestConfig(1, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid hugepages value "yes"`)
}
