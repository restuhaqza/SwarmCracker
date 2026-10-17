package jailer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubCgroupOps is the injection seam for the shared policy: it records calls
// and returns programmed errors, so no test writes to /sys/fs/cgroup.
type stubCgroupOps struct {
	ensureErr error
	createErr error
	addErr    error
	removeErr error
	online    string

	ensureCalls [][]string
	created     []string
	added       []int
	removed     []string
}

func (s *stubCgroupOps) EnsureControllers(controllers ...string) error {
	s.ensureCalls = append(s.ensureCalls, append([]string(nil), controllers...))
	return s.ensureErr
}

func (s *stubCgroupOps) CreateCgroup(taskID string, _ ResourceLimits) error {
	s.created = append(s.created, taskID)
	return s.createErr
}

func (s *stubCgroupOps) AddProcess(taskID string, pid int) error {
	s.added = append(s.added, pid)
	return s.addErr
}

func (s *stubCgroupOps) RemoveCgroup(taskID string) error {
	s.removed = append(s.removed, taskID)
	return s.removeErr
}

func (s *stubCgroupOps) OnlineCPUs() (string, error) { return s.online, nil }

func pinnedTask() *types.Task {
	return &types.Task{ID: "task-pinned", Annotations: map[string]string{types.CpusetLabel: "0-1"}}
}

func plainTask() *types.Task { return &types.Task{ID: "task-plain"} }

// TestDeriveResourceLimits pins the derivation both VMM paths share: declared
// limits win, the VM's resolved size is the fallback, and the cpuset annotation
// is canonicalised (or rejected by naming the offending value).
func TestDeriveResourceLimits(t *testing.T) {
	const mib = int64(1024 * 1024)

	tests := []struct {
		name     string
		task     *types.Task
		vcpus    int
		memoryMB int
		want     ResourceLimits
		wantErr  string
	}{
		{
			name:     "the VM size is the fallback when the spec is silent",
			task:     &types.Task{ID: "t1"},
			vcpus:    2,
			memoryMB: 512,
			want: ResourceLimits{
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
			want: ResourceLimits{
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
			got, err := deriveResourceLimits(tt.task, tt.vcpus, tt.memoryMB)

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

// TestCreateVMCgroup_FailurePolicy pins the one failure policy both call sites
// use: a cpuset that cannot be applied is always fatal, quota problems are
// warnings, and a task that asked for no cpuset behaves as if cgroups were off.
func TestCreateVMCgroup_FailurePolicy(t *testing.T) {
	cpusetUnavailable := &ControllersError{Requested: DefaultControllers, Unavailable: []string{"cpuset"}}
	cpuUnavailable := &ControllersError{Requested: DefaultControllers, Unavailable: []string{"cpu"}}

	tests := []struct {
		name        string
		noManager   bool
		managerErr  error
		task        *types.Task
		ensureErr   error
		createErr   error
		wantCreated bool
		wantErr     []string
		wantRemoved []string
	}{
		{
			name:      "no manager and no cpuset is a no-op",
			noManager: true,
			task:      plainTask(),
		},
		{
			name:      "no manager rejects a requested cpuset",
			noManager: true,
			task:      pinnedTask(),
			wantErr:   []string{`cannot apply cpuset "0-1"`, "cgroups are disabled"},
		},
		{
			name:       "a manager creation failure rejects a requested cpuset",
			noManager:  true,
			managerErr: errors.New("cgroup v2 not available on this system"),
			task:       pinnedTask(),
			wantErr:    []string{`cannot apply cpuset "0-1"`, "cgroup v2 not available"},
		},
		{
			name:      "unavailable cpuset controller is fatal",
			task:      pinnedTask(),
			ensureErr: cpusetUnavailable,
			wantErr:   []string{`cannot apply cpuset "0-1"`, "host online CPUs: 0-7"},
		},
		{
			name:        "an unavailable quota controller is a warning",
			task:        pinnedTask(),
			ensureErr:   cpuUnavailable,
			wantCreated: true,
		},
		{
			name:        "a cpuset stage failure is fatal",
			task:        pinnedTask(),
			createErr:   &CgroupError{Stage: CgroupStageCpuset, Field: "cpuset.cpus", Err: errors.New("boom")},
			wantErr:     []string{`cannot apply cpuset "0-1"`, "host online CPUs: 0-7"},
			wantRemoved: []string{"task-pinned"},
		},
		{
			name:        "an untyped create failure with a cpuset stays fatal",
			task:        pinnedTask(),
			createErr:   errors.New("boom"),
			wantErr:     []string{`cannot apply cpuset "0-1"`},
			wantRemoved: []string{"task-pinned"},
		},
		{
			name:        "a quota stage failure is a warning",
			task:        pinnedTask(),
			createErr:   &CgroupError{Stage: CgroupStageCPU, Err: errors.New("cpu.max is read-only")},
			wantCreated: true,
		},
		{
			name:        "a create failure without a cpuset warns and cleans up",
			task:        plainTask(),
			createErr:   errors.New("boom"),
			wantRemoved: []string{"task-plain"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stubCgroupOps{
				online:    "0-7",
				ensureErr: tt.ensureErr,
				createErr: tt.createErr,
			}
			var ops CgroupOps = stub
			if tt.noManager {
				ops = nil
			}

			created, err := CreateVMCgroup(ops, tt.managerErr, zerolog.Nop(), CgroupRequest{
				Task:     tt.task,
				VCPUs:    1,
				MemoryMB: 512,
			})

			if len(tt.wantErr) > 0 {
				require.Error(t, err)
				for _, want := range tt.wantErr {
					assert.Contains(t, err.Error(), want)
				}
				assert.False(t, created)
				assert.Equal(t, tt.wantRemoved, stub.removed)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantCreated, created)
			assert.Equal(t, tt.wantRemoved, stub.removed)
		})
	}
}

// TestAttachVMProcess_FailurePolicy pins the attach half: a requested cpuset that
// never took effect is fatal, anything else is a warning.
func TestAttachVMProcess_FailurePolicy(t *testing.T) {
	t.Run("a cpuset that cannot be attached is fatal", func(t *testing.T) {
		stub := &stubCgroupOps{online: "0-7", addErr: errors.New("cgroup.procs is read-only")}

		err := AttachVMProcess(stub, zerolog.Nop(), pinnedTask(), 4242)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `cannot apply cpuset "0-1"`)
		assert.Contains(t, err.Error(), "host online CPUs: 0-7")
		assert.Equal(t, []int{4242}, stub.added)
	})

	t.Run("an attach failure without a cpuset is a warning", func(t *testing.T) {
		stub := &stubCgroupOps{addErr: errors.New("no such file")}

		assert.NoError(t, AttachVMProcess(stub, zerolog.Nop(), plainTask(), 4242))
	})

	t.Run("no manager makes no calls", func(t *testing.T) {
		assert.NoError(t, AttachVMProcess(nil, zerolog.Nop(), pinnedTask(), 4242))
	})
}

// TestCreateCgroup_TypedCpusetStage pins the typed error of a real (temp-dir)
// manager: the failing cpuset write reports its stage and field, so callers can
// classify it without the message text.
func TestCreateCgroup_TypedCpusetStage(t *testing.T) {
	mgr, base := newTempCgroupManager(t)
	taskID := "task-typed-cpuset"
	require.NoError(t, os.MkdirAll(filepath.Join(base, taskID, "cpuset.cpus"), 0o755))

	online := filepath.Join(t.TempDir(), "cpu-online")
	require.NoError(t, os.WriteFile(online, []byte("0-3\n"), 0o644))
	mgr.cpuOnlinePath = online

	err := mgr.CreateCgroup(taskID, ResourceLimits{CpusetCPUs: "4-7", CpusetMems: "0"})
	require.Error(t, err)

	var cgroupErr *CgroupError
	require.True(t, errors.As(err, &cgroupErr), "want a *CgroupError, got %T", err)
	assert.Equal(t, taskID, cgroupErr.TaskID)
	assert.Equal(t, CgroupStageCpuset, cgroupErr.Stage)
	assert.Equal(t, "cpuset.cpus", cgroupErr.Field)
	assert.False(t, IsQuotaStage(err), "a cpuset failure is never a quota failure")
	assert.Contains(t, err.Error(), "0-3", "the error still names the host's online CPUs")
}

// TestCreateCgroup_TypedQuotaStage pins the other side: a quota write failure is
// typed as a quota stage, which is what lets the policy keep the cgroup.
func TestCreateCgroup_TypedQuotaStage(t *testing.T) {
	mgr, base := newTempCgroupManager(t)
	taskID := "task-typed-cpu"
	require.NoError(t, os.MkdirAll(filepath.Join(base, taskID, "cpu.max"), 0o755))

	err := mgr.CreateCgroup(taskID, ResourceLimits{CPUQuotaUs: 500000})
	require.Error(t, err)

	var cgroupErr *CgroupError
	require.True(t, errors.As(err, &cgroupErr), "want a *CgroupError, got %T", err)
	assert.Equal(t, CgroupStageCPU, cgroupErr.Stage)
	assert.True(t, IsQuotaStage(err))
}

// TestAddProcess_TypedAttachStage pins the attach stage of a real manager.
func TestAddProcess_TypedAttachStage(t *testing.T) {
	mgr, _ := newTempCgroupManager(t)

	err := mgr.AddProcess("missing-cgroup", os.Getpid())
	require.Error(t, err)

	var cgroupErr *CgroupError
	require.True(t, errors.As(err, &cgroupErr), "want a *CgroupError, got %T", err)
	assert.Equal(t, CgroupStageAttach, cgroupErr.Stage)
	assert.False(t, IsQuotaStage(err))
}

// TestIsQuotaStage pins the predicate itself, including wrapping and non-cgroup
// errors: only a typed cpu/memory/io stage counts as a quota failure.
func TestIsQuotaStage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "cpu", err: &CgroupError{Stage: CgroupStageCPU}, want: true},
		{name: "memory", err: &CgroupError{Stage: CgroupStageMemory}, want: true},
		{name: "io", err: &CgroupError{Stage: CgroupStageIO}, want: true},
		{name: "cpuset", err: &CgroupError{Stage: CgroupStageCpuset}, want: false},
		{name: "create-dir", err: &CgroupError{Stage: CgroupStageCreateDir}, want: false},
		{name: "attach", err: &CgroupError{Stage: CgroupStageAttach}, want: false},
		{name: "wrapped quota", err: errors.Join(errors.New("outer"), &CgroupError{Stage: CgroupStageCPU}), want: true},
		{name: "untyped", err: errors.New("failed to set CPU limits"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsQuotaStage(tt.err))
		})
	}
}
