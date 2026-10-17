package lifecycle

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cgroupCreate records one CreateCgroup call.
type cgroupCreate struct {
	taskID string
	limits jailer.ResourceLimits
}

// fakeCgroupManager is the injection seam for the lifecycle path: it records
// calls and returns programmed errors, so no test writes to /sys/fs/cgroup.
type fakeCgroupManager struct {
	mu sync.Mutex

	ensureCalls [][]string
	ensureErr   error

	created   []cgroupCreate
	createErr error

	added []struct {
		taskID string
		pid    int
	}
	addErr error

	removed   []string
	removeErr error

	online    string
	onlineErr error
}

func (f *fakeCgroupManager) EnsureControllers(controllers ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls = append(f.ensureCalls, append([]string(nil), controllers...))
	return f.ensureErr
}

func (f *fakeCgroupManager) CreateCgroup(taskID string, limits jailer.ResourceLimits) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, cgroupCreate{taskID: taskID, limits: limits})
	return f.createErr
}

func (f *fakeCgroupManager) AddProcess(taskID string, pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, struct {
		taskID string
		pid    int
	}{taskID, pid})
	return f.addErr
}

func (f *fakeCgroupManager) RemoveCgroup(taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, taskID)
	return f.removeErr
}

func (f *fakeCgroupManager) OnlineCPUs() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online, f.onlineErr
}

func (f *fakeCgroupManager) snapshot() (created []cgroupCreate, removed []string, ensure [][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cgroupCreate(nil), f.created...),
		append([]string(nil), f.removed...),
		append([][]string(nil), f.ensureCalls...)
}

// newCgroupVMM builds a lifecycle manager around the fake cgroup manager.
func newCgroupVMM(t *testing.T, mgr jailer.CgroupOps) *VMMManager {
	t.Helper()
	socketDir := t.TempDir()
	return &VMMManager{
		config:    &ManagerConfig{SocketDir: socketDir},
		vms:       make(map[string]*VMInstance),
		socketDir: socketDir,
		cgroupMgr: mgr,
	}
}

// cpusetTask is a `vm create`-shaped task: declared resources plus a cpuset
// annotation.
func cpusetTask(cpuset string) *types.Task {
	return &types.Task{
		ID: "task-lifecycle-cgroup",
		Spec: types.TaskSpec{Resources: types.ResourceRequirements{Limits: &types.Resources{
			NanoCPUs:    2e9,
			MemoryBytes: 512 * 1024 * 1024,
		}}},
		Annotations: map[string]string{types.CpusetLabel: cpuset},
	}
}

func machineConfig(vcpus, memoryMB int) map[string]interface{} {
	return map[string]interface{}{
		"machine-config": map[string]interface{}{
			"vcpu_count":   vcpus,
			"mem_size_mib": memoryMB,
		},
	}
}

// TestPrepareCgroup_NoAnnotationIsANoOp pins the lifecycle contract: a VM that
// does not ask to be pinned behaves exactly as before — no cgroup calls at all.
func TestPrepareCgroup_NoAnnotationIsANoOp(t *testing.T) {
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

// TestPrepareCgroup_DerivesLimitsFromSpec pins the exact limits a `vm create`
// with a cpuset annotation hands to the cgroup manager.
func TestPrepareCgroup_DerivesLimitsFromSpec(t *testing.T) {
	const mib = int64(1024 * 1024)
	fake := &fakeCgroupManager{online: "0-3"}
	vm := newCgroupVMM(t, fake)

	task := cpusetTask("3,1-2")
	created, err := vm.prepareCgroup(task, machineConfig(2, 512))
	require.NoError(t, err)
	assert.True(t, created)

	creates, _, ensure := fake.snapshot()
	require.Len(t, creates, 1)
	assert.Equal(t, "task-lifecycle-cgroup", creates[0].taskID)
	assert.Equal(t, jailer.ResourceLimits{
		CPUQuotaUs: 2_000_000,
		MemoryMax:  512*mib + types.CgroupMemoryHeadroom,
		MemoryHigh: (512*mib + types.CgroupMemoryHeadroom) * 9 / 10,
		IOWeight:   100,
		CpusetCPUs: "1-3",
	}, creates[0].limits)

	require.Len(t, ensure, 1)
	assert.Equal(t, jailer.DefaultControllers, ensure[0])
}

// TestPrepareCgroup_FallsBackToVMSize pins the fallback when the task declares
// no resources: the VM's own resolved size is the cap.
func TestPrepareCgroup_FallsBackToVMSize(t *testing.T) {
	const mib = int64(1024 * 1024)
	fake := &fakeCgroupManager{online: "0-3"}
	vm := newCgroupVMM(t, fake)

	task := &types.Task{
		ID:          "task-lifecycle-fallback",
		Annotations: map[string]string{types.CpusetLabel: "0"},
	}

	created, err := vm.prepareCgroup(task, machineConfig(4, 256))
	require.NoError(t, err)
	assert.True(t, created)

	creates, _, _ := fake.snapshot()
	require.Len(t, creates, 1)
	assert.Equal(t, int64(4_000_000), creates[0].limits.CPUQuotaUs)
	assert.Equal(t, 256*mib+types.CgroupMemoryHeadroom, creates[0].limits.MemoryMax)
}

// TestPrepareCgroup_MalformedCpuset pins that a malformed annotation fails the VM
// with the offending value named, before any cgroup is created.
func TestPrepareCgroup_MalformedCpuset(t *testing.T) {
	fake := &fakeCgroupManager{online: "0-3"}
	vm := newCgroupVMM(t, fake)

	_, err := vm.prepareCgroup(cpusetTask("0,a"), machineConfig(2, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"0,a"`)

	creates, _, ensure := fake.snapshot()
	assert.Empty(t, ensure)
	assert.Empty(t, creates)
}

// TestPrepareCgroup_CpusetControllerUnavailableIsFatal pins the hard failure:
// the request and the host's online CPUs are named, and no cgroup is created.
func TestPrepareCgroup_CpusetControllerUnavailableIsFatal(t *testing.T) {
	fake := &fakeCgroupManager{
		online: "0-3",
		ensureErr: &jailer.ControllersError{
			Requested:   jailer.DefaultControllers,
			Unavailable: []string{"cpuset"},
		},
	}
	vm := newCgroupVMM(t, fake)

	_, err := vm.prepareCgroup(cpusetTask("0-1"), machineConfig(2, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot apply cpuset "0-1"`)
	assert.Contains(t, err.Error(), "host online CPUs: 0-3")

	creates, _, _ := fake.snapshot()
	assert.Empty(t, creates)
}

// TestPrepareCgroup_QuotaFailureKeepsCpuset pins the warning side of the ruling:
// a quota write failure does not discard the cgroup that carries the pinning.
func TestPrepareCgroup_QuotaFailureKeepsCpuset(t *testing.T) {
	fake := &fakeCgroupManager{
		online: "0-3",
		createErr: &jailer.CgroupError{
			Stage: jailer.CgroupStageMemory,
			Field: "memory.max",
			Err:   errors.New("failed to write memory.max"),
		},
	}
	vm := newCgroupVMM(t, fake)

	created, err := vm.prepareCgroup(cpusetTask("0-1"), machineConfig(2, 512))
	require.NoError(t, err)
	assert.True(t, created, "the cpuset is in place, so the process must still be attached")

	_, removed, _ := fake.snapshot()
	assert.Empty(t, removed)
}

// TestPrepareCgroup_CpusetWriteFailureIsFatal pins that a failed cpuset.* write
// is fatal and cleans up the partially created cgroup.
func TestPrepareCgroup_CpusetWriteFailureIsFatal(t *testing.T) {
	fake := &fakeCgroupManager{
		online: "0-3",
		createErr: &jailer.CgroupError{
			Stage: jailer.CgroupStageCpuset,
			Field: "cpuset.cpus",
			Err:   errors.New("failed to write cpuset.cpus"),
		},
	}
	vm := newCgroupVMM(t, fake)

	_, err := vm.prepareCgroup(cpusetTask("0-1"), machineConfig(2, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot apply cpuset "0-1"`)
	assert.Contains(t, err.Error(), "cpuset.cpus")

	_, removed, _ := fake.snapshot()
	assert.Equal(t, []string{"task-lifecycle-cgroup"}, removed)
}

// TestStart_MalformedCpusetFailsBeforeFirecracker pins that Start refuses the VM
// before spawning a process.
func TestStart_MalformedCpusetFailsBeforeFirecracker(t *testing.T) {
	fake := &fakeCgroupManager{online: "0-3"}
	vm := newCgroupVMM(t, fake)

	err := vm.Start(context.Background(), cpusetTask("1-"), machineConfig(2, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"1-"`)

	creates, _, _ := fake.snapshot()
	assert.Empty(t, creates)
	assert.Empty(t, vm.vms, "a rejected cpuset must not register a VM")
}

// TestStart_ManagerCreationFailureIsNamed pins that a cpuset cannot be silently
// ignored when the cgroup manager is unavailable.
func TestStart_ManagerCreationFailureIsNamed(t *testing.T) {
	vm := newCgroupVMM(t, nil)
	vm.cgroupErr = errors.New("cgroup v2 not available on this system")

	err := vm.Start(context.Background(), cpusetTask("0-1"), machineConfig(2, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot apply cpuset "0-1"`)
	assert.Contains(t, err.Error(), "cgroup v2 not available on this system")
}

// TestStop_RemovesCgroup pins that a stopped VM's cgroup does not outlive it.
func TestStop_RemovesCgroup(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill() }()

	fake := &fakeCgroupManager{online: "0-3"}
	vm := newCgroupVMM(t, fake)
	vm.vms["task-stop"] = &VMInstance{ID: "task-stop", PID: cmd.Process.Pid, InitSystem: "none"}

	require.NoError(t, vm.Stop(context.Background(), &types.Task{ID: "task-stop"}))

	_, removed, _ := fake.snapshot()
	assert.Equal(t, []string{"task-stop"}, removed)
}

// TestRemove_RemovesCgroup pins the removal path's teardown too.
func TestRemove_RemovesCgroup(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill() }()

	fake := &fakeCgroupManager{online: "0-3"}
	vm := newCgroupVMM(t, fake)
	vm.vms["task-remove"] = &VMInstance{ID: "task-remove", PID: cmd.Process.Pid}

	require.NoError(t, vm.Remove(context.Background(), &types.Task{ID: "task-remove"}))

	_, removed, _ := fake.snapshot()
	assert.Equal(t, []string{"task-remove"}, removed)
}

// TestStart_LookPathFailureRemovesCgroup pins the teardown for a failure that
// happens after the cgroup exists but before any process does.
func TestStart_LookPathFailureRemovesCgroup(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	fake := &fakeCgroupManager{online: "0-3"}
	vm := newCgroupVMM(t, fake)

	err := vm.Start(context.Background(), cpusetTask("0-1"), machineConfig(2, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "firecracker binary not found")

	creates, removed, _ := fake.snapshot()
	require.Len(t, creates, 1, "the cgroup is created before the binary lookup")
	assert.Equal(t, []string{"task-lifecycle-cgroup"}, removed,
		"a VM that never ran must not leak its cgroup")
}

// TestStart_ProcessStartFailureRemovesCgroup pins the same for a cmd.Start
// failure: LookPath accepts the file (it is executable) and the kernel rejects
// the missing interpreter, so the failure lands after the cgroup exists.
func TestStart_ProcessStartFailureRemovesCgroup(t *testing.T) {
	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(binDir, "firecracker"),
		[]byte("#!/nonexistent/interpreter\n"),
		0o755,
	))
	t.Setenv("PATH", binDir)

	fake := &fakeCgroupManager{online: "0-3"}
	vm := newCgroupVMM(t, fake)

	err := vm.Start(context.Background(), cpusetTask("0-1"), machineConfig(2, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start firecracker")

	_, removed, _ := fake.snapshot()
	assert.Equal(t, []string{"task-lifecycle-cgroup"}, removed,
		"a VM whose process never started must not leak its cgroup")
}

// TestPrepareCgroup_TypedStagePolicy pins the `vm create` side of the shared
// classification: it too keys on the typed stage, never on error text, so a
// cpuset-stage failure is fatal and a quota-stage failure is only a warning.
func TestPrepareCgroup_TypedStagePolicy(t *testing.T) {
	tests := []struct {
		name      string
		stage     jailer.CgroupStage
		wantFatal bool
	}{
		{name: "a cpuset stage failure is fatal", stage: jailer.CgroupStageCpuset, wantFatal: true},
		{name: "a create-dir failure is fatal", stage: jailer.CgroupStageCreateDir, wantFatal: true},
		{name: "a CPU quota stage failure is a warning", stage: jailer.CgroupStageCPU, wantFatal: false},
		{name: "a memory quota stage failure is a warning", stage: jailer.CgroupStageMemory, wantFatal: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeCgroupManager{
				online: "0-3",
				createErr: &jailer.CgroupError{
					Stage: tt.stage,
					Err:   errors.New("stage failed"),
				},
			}
			vm := newCgroupVMM(t, fake)

			created, err := vm.prepareCgroup(cpusetTask("0-1"), machineConfig(2, 512))
			_, removed, _ := fake.snapshot()

			if tt.wantFatal {
				require.Error(t, err)
				assert.Contains(t, err.Error(), `cannot apply cpuset "0-1"`)
				assert.False(t, created)
				assert.Equal(t, []string{"task-lifecycle-cgroup"}, removed,
					"a fatal cpuset failure must remove the partial cgroup")
				return
			}
			require.NoError(t, err)
			assert.True(t, created, "the cpuset is in place, so the cgroup is kept")
			assert.Empty(t, removed)
		})
	}
}
