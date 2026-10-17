package swarmkit

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cgroupCreate records one CreateCgroup call.
type cgroupCreate struct {
	taskID string
	limits jailer.ResourceLimits
}

// cgroupAdd records one AddProcess call.
type cgroupAdd struct {
	taskID string
	pid    int
}

// fakeCgroupManager is the injected seam: it records every call and returns
// programmed errors, so no unit test touches /sys/fs/cgroup.
type fakeCgroupManager struct {
	mu sync.Mutex

	ensureCalls [][]string
	ensureErr   error

	created   []cgroupCreate
	createErr error

	added  []cgroupAdd
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
	f.added = append(f.added, cgroupAdd{taskID: taskID, pid: pid})
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

func (f *fakeCgroupManager) snapshot() (created []cgroupCreate, added []cgroupAdd, removed []string, ensure [][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cgroupCreate(nil), f.created...),
		append([]cgroupAdd(nil), f.added...),
		append([]string(nil), f.removed...),
		append([][]string(nil), f.ensureCalls...)
}

// cgroupTestVMM builds a manager whose cgroup manager is the fake and whose
// Firecracker binary does not exist, so a start path reaches the cgroup code
// without ever launching a VM.
func cgroupTestVMM(t *testing.T, mgr cgroupManager) *VMMManager {
	t.Helper()
	return &VMMManager{
		firecrackerPath: "/nonexistent/firecracker-binary",
		socketDir:       filepath.Join(t.TempDir(), "sockets"),
		cgroupMgr:       mgr,
		processes:       make(map[string]*exec.Cmd),
		processWaits:    make(map[string]*processWait),
		logger:          zerolog.Nop(),
	}
}

// cgroupTestConfig is the translator's minimal shape, carrying the VM size the
// VMM path falls back to.
func cgroupTestConfig(vcpus, memoryMB int) map[string]interface{} {
	return map[string]interface{}{
		"machine-config": map[string]interface{}{
			"vcpu_count":   vcpus,
			"mem_size_mib": memoryMB,
		},
	}
}

func TestCgroupResourceLimits(t *testing.T) {
	const mib = int64(1024 * 1024)

	tests := []struct {
		name     string
		task     *types.Task
		vcpus    int
		memoryMB int
		want     jailer.ResourceLimits
		wantErr  string
	}{
		{
			name:     "the VM size is the fallback when the spec is silent",
			task:     &types.Task{ID: "t1"},
			vcpus:    2,
			memoryMB: 512,
			want: jailer.ResourceLimits{
				CPUQuotaUs: 2_000_000,
				MemoryMax:  512*mib + types.CgroupMemoryHeadroom,
				MemoryHigh: (512*mib + types.CgroupMemoryHeadroom) * 9 / 10,
				IOWeight:   100,
			},
		},
		{
			name: "declared limits win and the cpuset is canonicalised",
			task: &types.Task{
				ID: "t1",
				Spec: types.TaskSpec{Resources: types.ResourceRequirements{Limits: &types.Resources{
					NanoCPUs:    1500000000,
					MemoryBytes: 256 * mib,
				}}},
				Annotations: map[string]string{types.CpusetLabel: "3,1-2"},
			},
			vcpus:    4,
			memoryMB: 1024,
			want: jailer.ResourceLimits{
				CPUQuotaUs: 1_500_000,
				MemoryMax:  256*mib + types.CgroupMemoryHeadroom,
				MemoryHigh: (256*mib + types.CgroupMemoryHeadroom) * 9 / 10,
				IOWeight:   100,
				CpusetCPUs: "1-3",
			},
		},
		{
			name:     "a malformed annotation is rejected by naming the value",
			task:     &types.Task{ID: "t1", Annotations: map[string]string{types.CpusetLabel: "0,a"}},
			vcpus:    1,
			memoryMB: 512,
			wantErr:  `"0,a"`,
		},
		{
			name: "an unrepresentable CPU request is rejected by naming the value",
			task: &types.Task{
				ID:   "t1",
				Spec: types.TaskSpec{Resources: types.ResourceRequirements{Limits: &types.Resources{NanoCPUs: 500}}},
			},
			vcpus:    1,
			memoryMB: 512,
			wantErr:  "500 NanoCPUs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cgroupResourceLimits(tt.task, tt.vcpus, tt.memoryMB)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestStartDirect_AppliesDerivedCgroupLimits pins the daemon's direct start path:
// the task's declared CPU/memory/cpuset reach CreateCgroup before the process
// exists, and a start failure tears the cgroup down again.
func TestStartDirect_AppliesDerivedCgroupLimits(t *testing.T) {
	fake := &fakeCgroupManager{online: "0-7"}
	vmm := cgroupTestVMM(t, fake)

	task := &types.Task{
		ID: "task-direct-cgroup",
		Spec: types.TaskSpec{Resources: types.ResourceRequirements{Limits: &types.Resources{
			NanoCPUs:    2e9,
			MemoryBytes: 512 * 1024 * 1024,
		}}},
		Annotations: map[string]string{types.CpusetLabel: "3,1-2"},
	}

	err := vmm.startDirect(context.Background(), task, cgroupTestConfig(2, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start firecracker")

	created, added, removed, ensure := fake.snapshot()
	require.Len(t, created, 1)
	assert.Equal(t, "task-direct-cgroup", created[0].taskID)
	assert.Equal(t, jailer.ResourceLimits{
		CPUQuotaUs: 2_000_000,
		MemoryMax:  512*1024*1024 + types.CgroupMemoryHeadroom,
		MemoryHigh: (512*1024*1024 + types.CgroupMemoryHeadroom) * 9 / 10,
		IOWeight:   100,
		CpusetCPUs: "1-3",
	}, created[0].limits)

	require.Len(t, ensure, 1, "controllers must be delegated exactly once per start")
	assert.Equal(t, cgroupControllers, ensure[0])

	assert.Empty(t, added, "no process exists to attach")
	assert.Equal(t, []string{"task-direct-cgroup"}, removed,
		"a failed start must not leave its cgroup behind")
}

// TestStartDirect_FallsBackToVMSize pins the quota/memory fallback: a task with
// no declared resources is still capped to the VM that was actually booted.
func TestStartDirect_FallsBackToVMSize(t *testing.T) {
	fake := &fakeCgroupManager{online: "0-7"}
	vmm := cgroupTestVMM(t, fake)

	task := &types.Task{ID: "task-direct-fallback"}

	err := vmm.startDirect(context.Background(), task, cgroupTestConfig(3, 256))
	require.Error(t, err)

	created, _, _, _ := fake.snapshot()
	require.Len(t, created, 1)
	assert.Equal(t, int64(3_000_000), created[0].limits.CPUQuotaUs)
	assert.Equal(t, int64(256*1024*1024)+types.CgroupMemoryHeadroom, created[0].limits.MemoryMax)
	assert.Empty(t, created[0].limits.CpusetCPUs, "no annotation means no pinning")
}

// TestStartDirect_MalformedCpusetFailsBeforeCreatingAnything pins the rejection
// before any cgroup or process work.
func TestStartDirect_MalformedCpusetFailsBeforeCreatingAnything(t *testing.T) {
	fake := &fakeCgroupManager{online: "0-7"}
	vmm := cgroupTestVMM(t, fake)

	task := &types.Task{
		ID:          "task-bad-cpuset",
		Annotations: map[string]string{types.CpusetLabel: "1-"},
	}

	err := vmm.startDirect(context.Background(), task, cgroupTestConfig(1, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"1-"`, "the error must name the offending value")

	created, added, removed, ensure := fake.snapshot()
	assert.Empty(t, ensure)
	assert.Empty(t, created)
	assert.Empty(t, added)
	assert.Empty(t, removed)
}

// TestStartDirect_CpusetControllerUnavailableIsFatal pins the hard failure: a
// cpuset that cannot be enabled stops the VM and names the request and the
// host's online CPUs.
func TestStartDirect_CpusetControllerUnavailableIsFatal(t *testing.T) {
	fake := &fakeCgroupManager{
		online: "0-7",
		ensureErr: &jailer.ControllersError{
			Requested:   cgroupControllers,
			Unavailable: []string{"cpuset"},
		},
	}
	vmm := cgroupTestVMM(t, fake)

	task := &types.Task{
		ID:          "task-unavailable-cpuset",
		Annotations: map[string]string{types.CpusetLabel: "0-1"},
	}

	err := vmm.startDirect(context.Background(), task, cgroupTestConfig(1, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot apply cpuset "0-1"`)
	assert.Contains(t, err.Error(), "host online CPUs: 0-7")

	created, added, _, _ := fake.snapshot()
	assert.Empty(t, created, "no cgroup may be created when the cpuset is unavailable")
	assert.Empty(t, added)
}

// TestStartDirect_QuotaControllerUnavailableIsWarning pins the ruling's other
// side: a quota-only controller problem does not stop a VM that asked for no
// cpuset.
func TestStartDirect_QuotaControllerUnavailableIsWarning(t *testing.T) {
	fake := &fakeCgroupManager{
		online: "0-7",
		ensureErr: &jailer.ControllersError{
			Requested:   cgroupControllers,
			Unavailable: []string{"cpu", "memory", "io"},
		},
	}
	vmm := cgroupTestVMM(t, fake)

	task := &types.Task{ID: "task-quota-warning"}

	err := vmm.startDirect(context.Background(), task, cgroupTestConfig(1, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start firecracker")

	created, _, _, _ := fake.snapshot()
	assert.Len(t, created, 1, "the VM still gets its cgroup and its size-based caps")
}

// TestStartDirect_CpusetWriteFailureIsFatal pins a failed cpuset.* write: the VM
// is not started and the partially created cgroup is removed.
func TestStartDirect_CpusetWriteFailureIsFatal(t *testing.T) {
	fake := &fakeCgroupManager{
		online:    "0-7",
		createErr: errors.New("failed to set cpuset limits: failed to write cpuset.cpus"),
	}
	vmm := cgroupTestVMM(t, fake)

	task := &types.Task{
		ID:          "task-cpuset-write",
		Annotations: map[string]string{types.CpusetLabel: "0-1"},
	}

	err := vmm.startDirect(context.Background(), task, cgroupTestConfig(1, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot apply cpuset "0-1"`)
	assert.Contains(t, err.Error(), "cpuset.cpus")

	_, added, removed, _ := fake.snapshot()
	assert.Empty(t, added)
	assert.Equal(t, []string{"task-cpuset-write"}, removed)
}

// TestStartDirect_CpusetQuotaFailureKeepsCgroup pins that a quota write failure
// is only a warning and does not discard the pinning.
func TestStartDirect_CpusetQuotaFailureKeepsCgroup(t *testing.T) {
	fake := &fakeCgroupManager{
		online:    "0-7",
		createErr: errors.New("failed to set CPU limits: failed to write cpu.max"),
	}
	vmm := cgroupTestVMM(t, fake)

	task := &types.Task{
		ID:          "task-cpuset-quota",
		Annotations: map[string]string{types.CpusetLabel: "0-1"},
	}

	err := vmm.startDirect(context.Background(), task, cgroupTestConfig(1, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start firecracker",
		"a quota failure must not surface as a cpuset failure")

	_, added, _, _ := fake.snapshot()
	assert.Empty(t, added, "Firecracker never started")
}

// TestStartDirect_AddProcessFailureIsFatal pins that a cpuset which never took
// effect (the process could not be attached) fails the VM.
func TestStartDirect_AddProcessFailureIsFatal(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}

	fake := &fakeCgroupManager{online: "0-7", addErr: errors.New("cgroup.procs is read-only")}
	vmm := cgroupTestVMM(t, fake)
	vmm.firecrackerPath = "/bin/sleep"

	task := &types.Task{
		ID:          "task-addprocess",
		Annotations: map[string]string{types.CpusetLabel: "0-1"},
	}

	err := vmm.startDirect(context.Background(), task, cgroupTestConfig(1, 512))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot apply cpuset "0-1"`)
	assert.Contains(t, err.Error(), "cgroup.procs is read-only")

	_, _, removed, _ := fake.snapshot()
	assert.Equal(t, []string{"task-addprocess"}, removed)
}

// TestStartWithJailer_RejectsCpusetBeforeJailerStart pins the second start path:
// the jailed path applies the same seam, derives its fallback from the jailer's
// VMConfig, and refuses a cpuset before the jailer (which is nil here) runs.
func TestStartWithJailer_RejectsCpusetBeforeJailerStart(t *testing.T) {
	fake := &fakeCgroupManager{
		online:    "0-7",
		createErr: errors.New("failed to set cpuset limits: boom"),
	}
	vmm := cgroupTestVMM(t, fake)
	vmm.useJailer = true

	task := &types.Task{
		ID:          "task-jailer-cpuset",
		Annotations: map[string]string{types.CpusetLabel: "0-1"},
	}
	config := map[string]interface{}{
		"machine-config": map[string]interface{}{
			"vcpu_count":   4,
			"mem_size_mib": 1024,
		},
		"boot-source": map[string]interface{}{
			"kernel_image_path": "/vmlinux",
			"boot_args":         "console=ttyS0",
		},
		"drives": []interface{}{
			map[string]interface{}{
				"drive_id":       "rootfs",
				"path_on_host":   "/rootfs.ext4",
				"is_root_device": true,
			},
		},
	}

	err := vmm.startWithJailer(context.Background(), task, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot apply cpuset "0-1"`)

	created, _, removed, _ := fake.snapshot()
	require.Len(t, created, 1)
	assert.Equal(t, int64(4_000_000), created[0].limits.CPUQuotaUs,
		"the jailed path falls back to the VMConfig size")
	assert.Equal(t, int64(1024*1024*1024)+types.CgroupMemoryHeadroom, created[0].limits.MemoryMax)
	assert.Equal(t, "1", created[0].limits.CpusetCPUs)
	assert.Equal(t, []string{"task-jailer-cpuset"}, removed)
}

// TestStop_RemovesCgroupOnDirectPath pins teardown on the non-jailer path, where
// no cgroup removal existed before.
func TestStop_RemovesCgroupOnDirectPath(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill() }()

	fake := &fakeCgroupManager{online: "0-7"}
	vmm := &VMMManager{
		socketDir: t.TempDir(),
		cgroupMgr: fake,
		processes: map[string]*exec.Cmd{"task-stop-cgroup": cmd},
		logger:    zerolog.Nop(),
	}

	task := &types.Task{ID: "task-stop-cgroup"}
	require.NoError(t, vmm.Stop(context.Background(), task))

	_, _, removed, _ := fake.snapshot()
	assert.Equal(t, []string{"task-stop-cgroup"}, removed)
}

// TestRemove_RemovesCgroupOnDirectPath pins teardown when a VM is removed rather
// than stopped.
func TestRemove_RemovesCgroupOnDirectPath(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill() }()

	fake := &fakeCgroupManager{online: "0-7"}
	vmm := &VMMManager{
		socketDir: t.TempDir(),
		cgroupMgr: fake,
		processes: map[string]*exec.Cmd{"task-remove-cgroup": cmd},
		logger:    zerolog.Nop(),
	}

	task := &types.Task{ID: "task-remove-cgroup"}
	require.NoError(t, vmm.Remove(context.Background(), task))

	_, _, removed, _ := fake.snapshot()
	assert.Equal(t, []string{"task-remove-cgroup"}, removed)
}

// TestCreateVMCgroup_Disabled pins that a disabled manager means no cgroup calls
// at all for a task that requests no cpuset, and a rejection that names the
// request when one is asked for.
func TestCreateVMCgroup_Disabled(t *testing.T) {
	vmm := &VMMManager{logger: zerolog.Nop()}

	created, err := vmm.createVMCgroup(&types.Task{ID: "t1"}, 1, 512)
	require.NoError(t, err)
	assert.False(t, created)

	_, err = vmm.createVMCgroup(&types.Task{
		ID:          "t2",
		Annotations: map[string]string{types.CpusetLabel: "0-1"},
	}, 1, 512)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot apply cpuset "0-1"`)
	assert.Contains(t, err.Error(), "cgroups are disabled")

	// A manager creation failure is named too, instead of ignoring the request.
	vmm.cgroupErr = errors.New("cgroup v2 not available on this system")
	_, err = vmm.createVMCgroup(&types.Task{
		ID:          "t3",
		Annotations: map[string]string{types.CpusetLabel: "0-1"},
	}, 1, 512)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cgroup v2 not available on this system")
}
