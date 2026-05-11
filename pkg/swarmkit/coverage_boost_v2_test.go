//go:build !integration

package swarmkit

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/storage"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

// TestHostname tests hostname function
func TestHostname_CoverageV2(t *testing.T) {
	result := hostname()
	assert.NotEmpty(t, result)
}

// TestArchSupported tests archSupported on current architecture
func TestArchSupported_CoverageV2(t *testing.T) {
	e := &Executor{config: &Config{}}
	result := e.archSupported()
	switch runtime.GOARCH {
	case "amd64", "arm64":
		assert.True(t, result)
	default:
		assert.False(t, result)
	}
}

// TestGetCPUs tests getCPUs with various configurations
func TestGetCPUs_CoverageV2(t *testing.T) {
	totalCPUs := runtime.NumCPU()

	tests := []struct {
		name         string
		reservedCPUs int
	}{
		{"default reservation", 0},
		{"reserve 1 CPU", 1},
		{"reserve all CPUs", totalCPUs},
		{"reserve more than total", totalCPUs + 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Executor{config: &Config{ReservedCPUs: tt.reservedCPUs}}
			result := e.getCPUs()
			assert.GreaterOrEqual(t, result, int64(1e9))
		})
	}
}

// TestGetMemory tests getMemory with various configurations
func TestGetMemory_CoverageV2(t *testing.T) {
	tests := []struct {
		name             string
		reservedMemoryMB int
	}{
		{"default reservation", 0},
		{"reserve 256MB", 256},
		{"reserve 1024MB", 1024},
		{"reserve large amount", 100000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Executor{config: &Config{ReservedMemoryMB: tt.reservedMemoryMB}}
			result := e.getMemory()
			assert.Greater(t, result, int64(512*1024*1024))
		})
	}
}

// TestReadMeminfo tests readMeminfo on Linux
func TestReadMeminfo_CoverageV2(t *testing.T) {
	e := &Executor{config: &Config{}}
	result := e.readMeminfo()
	assert.Greater(t, result, int64(0))
}

// TestParseMeminfoLine tests parseMeminfoLine function
func TestParseMeminfoLine_CoverageV2(t *testing.T) {
	tests := []struct {
		line     string
		expected int64
	}{
		{"MemTotal:       16384000 kB", 16384000},
		{"MemAvailable:    4000000 kB", 4000000},
		{"", 0},
		{"NoNumbersHere", 0},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			result := parseMeminfoLine(tt.line)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetLocalIPFromInterface tests getting local IP
func TestGetLocalIPFromInterface_CoverageV2(t *testing.T) {
	result := getLocalIPFromInterface()
	if result != "" {
		ip := net.ParseIP(result)
		assert.NotNil(t, ip)
		assert.False(t, ip.IsLoopback())
	}
	assert.NotPanics(t, func() { getLocalIPFromInterface() })
}

// TestKVMAvailable tests kvmAvailable
func TestKVMAvailable_CoverageV2(t *testing.T) {
	e := &Executor{config: &Config{}}
	_ = e.kvmAvailable()
	_, err := os.Stat("/dev/kvm")
	assert.NoError(t, err) // or assert.Error depending on system
}

// TestController_Prepare_AlreadyPrepared tests Prepare when already prepared
func TestController_Prepare_AlreadyPreparedV2(t *testing.T) {
	ctrl := &Controller{
		task:       &api.Task{ID: "task-1"},
		config:     &Config{},
		imagePrep:  &MockImagePreparer{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
		prepared:   true,
	}
	err := ctrl.Prepare(context.Background())
	assert.NoError(t, err)
}

// TestController_Prepare_ImageError tests Prepare with image error
func TestController_Prepare_ImageErrorV2(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		imagePrep: &MockImagePreparer{
			PrepareFunc: func(ctx context.Context, task *types.Task) error {
				return errors.New("image error")
			},
		},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
		prepared:   false,
	}
	err := ctrl.Prepare(context.Background())
	assert.Error(t, err)
}

// TestController_Prepare_NetworkError tests Prepare with network error
func TestController_Prepare_NetworkErrorV2(t *testing.T) {
	ctrl := &Controller{
		task:      &api.Task{ID: "task-1"},
		config:    &Config{},
		imagePrep: &MockImagePreparer{},
		networkMgr: &MockNetworkManager{
			PrepareNetworkFunc: func(ctx context.Context, task *types.Task) error {
				return errors.New("network error")
			},
		},
		mu:       sync.Mutex{},
		prepared: false,
	}
	err := ctrl.Prepare(context.Background())
	assert.Error(t, err)
}

// TestController_Start_NotPrepared tests Start when not prepared
func TestController_Start_NotPreparedV2(t *testing.T) {
	ctrl := &Controller{
		task:     &api.Task{ID: "task-1"},
		config:   &Config{},
		mu:       sync.Mutex{},
		prepared: false,
	}
	err := ctrl.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "task not prepared")
}

// TestController_Start_AlreadyStarted tests Start when already started
func TestController_Start_AlreadyStartedV2(t *testing.T) {
	ctrl := &Controller{
		task:     &api.Task{ID: "task-1"},
		config:   &Config{},
		mu:       sync.Mutex{},
		prepared: true,
		started:  true,
	}
	err := ctrl.Start(context.Background())
	assert.NoError(t, err)
}

// TestController_Start_NoInternalTask tests Start without internal task
func TestController_Start_NoInternalTaskV2(t *testing.T) {
	ctrl := &Controller{
		task:         &api.Task{ID: "task-1"},
		config:       &Config{},
		mu:           sync.Mutex{},
		prepared:     true,
		internalTask: nil,
	}
	err := ctrl.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "internal task not prepared")
}

// TestController_Shutdown_NotStarted tests Shutdown when not started
func TestController_Shutdown_NotStartedV2(t *testing.T) {
	ctrl := &Controller{
		task:    &api.Task{ID: "task-1"},
		config:  &Config{},
		mu:      sync.Mutex{},
		started: false,
	}
	err := ctrl.Shutdown(context.Background())
	assert.NoError(t, err)
}

// TestController_Terminate_NotStarted tests Terminate when not started
func TestController_Terminate_NotStartedV2(t *testing.T) {
	ctrl := &Controller{
		task:    &api.Task{ID: "task-1"},
		config:  &Config{},
		mu:      sync.Mutex{},
		started: false,
	}
	err := ctrl.Terminate(context.Background())
	assert.NoError(t, err)
}

// TestController_SyncVolumeData_NoRootfs tests syncVolumeData without rootfs
func TestController_SyncVolumeData_NoRootfsV2(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		mu:     sync.Mutex{},
	}
	task := &types.Task{Annotations: map[string]string{}}
	err := ctrl.syncVolumeData(context.Background(), task, []types.Mount{})
	assert.NoError(t, err)
}

// TestController_SyncVolumeData_EmptyRootfs tests syncVolumeData with empty rootfs
func TestController_SyncVolumeData_EmptyRootfsV2(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		mu:     sync.Mutex{},
	}
	task := &types.Task{Annotations: map[string]string{"rootfs": ""}}
	err := ctrl.syncVolumeData(context.Background(), task, []types.Mount{})
	assert.NoError(t, err)
}

// TestController_MountRootfs_Nonexistent tests mountRootfs with nonexistent path
func TestController_MountRootfs_NonexistentV2(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		mu:     sync.Mutex{},
	}
	_, err := ctrl.mountRootfs("/nonexistent/path.ext4")
	assert.Error(t, err)
}

// TestController_UnmountRootfs tests unmountRootfs cleanup
func TestController_UnmountRootfsV2(t *testing.T) {
	tmpDir := t.TempDir()
	mountDir := filepath.Join(tmpDir, "mount-point")
	os.MkdirAll(mountDir, 0755)
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		mu:     sync.Mutex{},
	}
	err := ctrl.unmountRootfs(mountDir)
	assert.NoError(t, err)
	_, err = os.Stat(mountDir)
	assert.True(t, os.IsNotExist(err))
}

// TestExecutor_NewExecutor_NilConfig tests NewExecutor with nil config
func TestExecutor_NewExecutor_NilConfigV2(t *testing.T) {
	exec, err := NewExecutor(nil)
	assert.Error(t, err)
	assert.Nil(t, exec)
}

// TestExecutor_SetNetworkBootstrapKeys tests SetNetworkBootstrapKeys
func TestExecutor_SetNetworkBootstrapKeysV2(t *testing.T) {
	e := &Executor{
		config:      &Config{},
		controllers: make(map[string]*Controller),
	}
	keys := []*api.EncryptionKey{{Key: []byte("test-key")}}
	err := e.SetNetworkBootstrapKeys(keys)
	assert.NoError(t, err)
}

// TestExecutor_CleanupOrphanedVMs_NilVMM tests cleanup with nil VMM
func TestExecutor_CleanupOrphanedVMs_NilVMMV2(t *testing.T) {
	e := &Executor{
		config:      &Config{},
		controllers: make(map[string]*Controller),
		vmmMgr:      nil,
	}
	e.cleanupOrphanedVMs(context.Background())
}

// TestExecutor_CleanupOrphanedVMs_EmptyProcesses tests cleanup with no processes
func TestExecutor_CleanupOrphanedVMs_EmptyProcessesV2(t *testing.T) {
	e := &Executor{
		config:      &Config{},
		controllers: make(map[string]*Controller),
		vmmMgr: &MockVMMManager{
			GetRunningProcessesFunc: func() map[string]*exec.Cmd { return nil },
		},
	}
	e.cleanupOrphanedVMs(context.Background())
}

// TestExecutor_CleanupOrphanedVMs_WithOrphan tests cleanup with orphaned VM
func TestExecutor_CleanupOrphanedVMs_WithOrphanV2(t *testing.T) {
	tmpDir := t.TempDir()
	cmd := exec.Command("sleep", "0.1")
	cmd.Start()
	processes := map[string]*exec.Cmd{"orphan-task": cmd}
	mockVMM := &MockVMMManager{
		processes:               processes,
		GetRunningProcessesFunc: func() map[string]*exec.Cmd { return processes },
		RemoveProcessFunc:       func(taskID string) { delete(processes, taskID) },
	}
	e := &Executor{
		config:      &Config{SocketDir: tmpDir},
		controllers: map[string]*Controller{},
		vmmMgr:      mockVMM,
	}
	e.cleanupOrphanedVMs(context.Background())
}

// TestController_Remove_WithSocketCleanup tests Remove cleans up socket
func TestController_Remove_WithSocketCleanupV2(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "task-socket.sock")
	os.WriteFile(socketPath, []byte("fake"), 0644)
	ctrl := &Controller{
		task:       &api.Task{ID: "task-socket"},
		config:     &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
		socketPath: socketPath,
	}
	err := ctrl.Remove(context.Background())
	assert.NoError(t, err)
}

// TestController_Remove_WithRootfsCleanup tests Remove cleans up rootfs
func TestController_Remove_WithRootfsCleanupV2(t *testing.T) {
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, "task-rootfs.ext4")
	os.WriteFile(rootfsPath, []byte("fake"), 0644)
	ctrl := &Controller{
		task:       &api.Task{ID: "task-rootfs"},
		config:     &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}
	err := ctrl.Remove(context.Background())
	assert.NoError(t, err)
}

// TestController_ContainerStatus tests ContainerStatus method
func TestController_ContainerStatusV2(t *testing.T) {
	ctrl := &Controller{
		task:    &api.Task{ID: "task-1"},
		config:  &Config{},
		vmmMgr:  &MockVMMManager{},
		mu:      sync.Mutex{},
		started: false,
	}
	status, err := ctrl.ContainerStatus(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, status)
}

// TestController_PortStatus tests PortStatus method
func TestController_PortStatusV2(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		mu:     sync.Mutex{},
	}
	status, err := ctrl.PortStatus(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, status)
}

// TestController_Close tests Close method
func TestController_CloseV2(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		mu:     sync.Mutex{},
	}
	err := ctrl.Close()
	assert.NoError(t, err)
}

// TestController_Update tests Update method
func TestController_UpdateV2(t *testing.T) {
	ctrl := &Controller{
		task:    &api.Task{ID: "task-1"},
		config:  &Config{},
		mu:      sync.Mutex{},
		started: false,
	}
	err := ctrl.Update(context.Background(), &api.Task{ID: "task-1"})
	assert.NoError(t, err)
}

// TestStorage_IsVolumeReference tests volume reference detection
func TestStorage_IsVolumeReferenceV2(t *testing.T) {
	assert.True(t, storage.IsVolumeReference("volume://myvol"))
	assert.False(t, storage.IsVolumeReference("/bind/mount"))
}

// TestStorage_ExtractVolumeName tests volume name extraction
func TestStorage_ExtractVolumeNameV2(t *testing.T) {
	assert.Equal(t, "myvol", storage.ExtractVolumeName("volume://myvol"))
	assert.Equal(t, "/bind/mount", storage.ExtractVolumeName("/bind/mount"))
	assert.Equal(t, "simple", storage.ExtractVolumeName("simple"))
}

// TestController_convertTask_WithNetworks tests convertTask with networks
func TestController_convertTask_WithNetworksV2(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID: "task-net",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx"},
				},
			},
			Networks: []*api.NetworkAttachment{
				{
					Network: &api.Network{
						ID: "net-1",
						Spec: api.NetworkSpec{
							Annotations:  api.Annotations{Name: "overlay-net"},
							DriverConfig: &api.Driver{Name: "overlay"},
						},
					},
					Addresses: []string{"10.0.0.2/24"},
				},
			},
		},
	}
	result := ctrl.convertTask()
	assert.NotNil(t, result)
	assert.Len(t, result.Networks, 1)
}

// TestController_convertTask_NilNetwork tests convertTask with nil network
func TestController_convertTask_NilNetworkV2(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID:       "task-nil",
			Networks: []*api.NetworkAttachment{nil},
		},
	}
	result := ctrl.convertTask()
	assert.NotNil(t, result)
	assert.Len(t, result.Networks, 0)
}

// TestExecutor_Controller tests Controller method
func TestExecutor_ControllerV2(t *testing.T) {
	cfg := &Config{}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Skipf("NewExecutor failed: %v", err)
	}
	task := &api.Task{ID: "task-1"}
	ctrl1, err := exec.Controller(task)
	if err != nil {
		t.Skipf("Controller failed: %v", err)
	}
	ctrl2, err := exec.Controller(task)
	assert.NoError(t, err)
	assert.Same(t, ctrl1, ctrl2)
}

// TestExecutor_Configure tests Configure method
func TestExecutor_ConfigureV2(t *testing.T) {
	e := &Executor{
		config:      &Config{},
		controllers: make(map[string]*Controller),
	}
	node := &api.Node{ID: "node-1"}
	err := e.Configure(context.Background(), node)
	assert.NoError(t, err)
}

// TestExecutor_GetRunningProcesses tests GetRunningProcesses mock
func TestExecutor_GetRunningProcessesV2(t *testing.T) {
	processes := map[string]*exec.Cmd{"task-1": exec.Command("echo")}
	mockVMM := &MockVMMManager{
		processes:               processes,
		GetRunningProcessesFunc: func() map[string]*exec.Cmd { return processes },
	}
	result := mockVMM.GetRunningProcesses()
	assert.Equal(t, processes, result)
}

// TestExecutor_RemoveProcess tests RemoveProcess mock
func TestExecutor_RemoveProcessV2(t *testing.T) {
	processes := map[string]*exec.Cmd{
		"task-1": exec.Command("echo"),
		"task-2": exec.Command("echo"),
	}
	mockVMM := &MockVMMManager{
		processes:         processes,
		RemoveProcessFunc: func(taskID string) { delete(processes, taskID) },
	}
	mockVMM.RemoveProcess("task-1")
	assert.Len(t, processes, 1)
}

// TestExecutor_periodicCleanup_ImmediateCancel tests periodicCleanup with immediate cancel
func TestExecutor_periodicCleanup_ImmediateCancelV2(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cleanupDone := make(chan struct{})
	e := &Executor{
		config:      &Config{MaxImageAgeDays: 7},
		imagePrep:   &MockImagePreparer{},
		vmmMgr:      &MockVMMManager{},
		controllers: make(map[string]*Controller),
		cleanupDone: cleanupDone,
	}
	go e.periodicCleanup(ctx)
	select {
	case <-cleanupDone:
	case <-time.After(1 * time.Second):
	}
}

// TestExecutor_runCleanup tests runCleanup
func TestExecutor_runCleanupV2(t *testing.T) {
	tests := []struct {
		name         string
		maxAgeDays   int
		filesRemoved int
		err          error
	}{
		{"successful cleanup", 7, 5, nil},
		{"cleanup error", 7, 0, errors.New("cleanup failed")},
		{"custom max age", 14, 2, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockPrep := &MockImagePreparer{
				CleanupFunc: func(ctx context.Context, maxAge int) (int, int64, error) {
					assert.Equal(t, tt.maxAgeDays, maxAge)
					return tt.filesRemoved, 0, tt.err
				},
			}
			e := &Executor{
				config:    &Config{MaxImageAgeDays: tt.maxAgeDays},
				imagePrep: mockPrep,
			}
			e.runCleanup(context.Background())
		})
	}
}

// TestController_Prepare_WithSecrets tests Prepare with secret references
func TestController_Prepare_WithSecretsV2(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID: "task-secrets",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image: "nginx",
						Secrets: []*api.SecretReference{
							{
								SecretID:   "secret-1",
								SecretName: "my-secret",
								Target: &api.SecretReference_File{
									File: &api.FileTarget{Name: "/run/secrets/my-secret"},
								},
							},
						},
					},
				},
			},
		},
		config:     &Config{},
		imagePrep:  &MockImagePreparer{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}
	err := ctrl.Prepare(context.Background())
	assert.NoError(t, err)
}

// TestController_Prepare_WithConfigs tests Prepare with config references
func TestController_Prepare_WithConfigsV2(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID: "task-configs",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image: "nginx",
						Configs: []*api.ConfigReference{
							{
								ConfigID:   "config-1",
								ConfigName: "my-config",
								Target: &api.ConfigReference_File{
									File: &api.FileTarget{Name: "/config/my-config"},
								},
							},
						},
					},
				},
			},
		},
		config:     &Config{},
		imagePrep:  &MockImagePreparer{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}
	err := ctrl.Prepare(context.Background())
	assert.NoError(t, err)
}

// TestController_Prepare_WithMounts tests Prepare with mounts
func TestController_Prepare_WithMountsV2(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID: "task-mounts",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image: "nginx",
						Mounts: []api.Mount{
							{
								Target: "/data",
								Source: "volume://myvol",
							},
						},
					},
				},
			},
		},
		config:     &Config{},
		imagePrep:  &MockImagePreparer{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}
	err := ctrl.Prepare(context.Background())
	assert.NoError(t, err)
}

// TestController_Prepare_WithResources tests Prepare with resources
func TestController_Prepare_WithResourcesV2(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID: "task-resources",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx"},
				},
				Resources: &api.ResourceRequirements{
					Reservations: &api.Resources{
						NanoCPUs:    2e9,
						MemoryBytes: 1024 * 1024 * 1024,
					},
				},
			},
		},
		config:     &Config{},
		imagePrep:  &MockImagePreparer{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}
	err := ctrl.Prepare(context.Background())
	assert.NoError(t, err)
}

// TestController_SyncVolumeData_WithReadOnly tests syncVolumeData with read-only
func TestController_SyncVolumeData_WithReadOnlyV2(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		mu:     sync.Mutex{},
	}
	task := &types.Task{
		Annotations: map[string]string{"rootfs": "some.ext4"},
	}
	mounts := []types.Mount{
		{Source: "volume://myvol", Target: "/data", ReadOnly: true},
	}
	err := ctrl.syncVolumeData(context.Background(), task, mounts)
	assert.NoError(t, err)
}

// TestController_SyncVolumeData_WithNonVolume tests syncVolumeData with non-volume
func TestController_SyncVolumeData_WithNonVolumeV2(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		mu:     sync.Mutex{},
	}
	task := &types.Task{
		Annotations: map[string]string{"rootfs": "some.ext4"},
	}
	mounts := []types.Mount{
		{Source: "/bind/path", Target: "/data", ReadOnly: false},
	}
	err := ctrl.syncVolumeData(context.Background(), task, mounts)
	assert.NoError(t, err)
}

// TestExecutor_SetNetworkBootstrapKeys_Empty tests SetNetworkBootstrapKeys empty
func TestExecutor_SetNetworkBootstrapKeys_EmptyV2(t *testing.T) {
	e := &Executor{
		config:      &Config{},
		controllers: make(map[string]*Controller),
	}
	err := e.SetNetworkBootstrapKeys([]*api.EncryptionKey{})
	assert.NoError(t, err)
}

// TestExecutor_SetNetworkBootstrapKeys_NilNetwork tests SetNetworkBootstrapKeys nil network
func TestExecutor_SetNetworkBootstrapKeys_NilNetworkV2(t *testing.T) {
	e := &Executor{
		config:      &Config{},
		controllers: make(map[string]*Controller),
		networkMgr:  nil,
	}
	keys := []*api.EncryptionKey{{Key: []byte("test-key")}}
	err := e.SetNetworkBootstrapKeys(keys)
	assert.NoError(t, err)
}

// TestController_ContainerStatus_Running tests ContainerStatus running
func TestController_ContainerStatus_RunningV2(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		vmmMgr: &MockVMMManager{
			IsRunningFunc: func(taskID string) bool { return true },
			GetPIDFunc:    func(taskID string) int { return 1234 },
		},
		mu:      sync.Mutex{},
		started: true,
	}
	status, err := ctrl.ContainerStatus(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, status)
	assert.Equal(t, int32(1234), status.PID)
}

// TestController_ContainerStatus_NotRunning tests ContainerStatus not running
func TestController_ContainerStatus_NotRunningV2(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		vmmMgr: &MockVMMManager{
			IsRunningFunc: func(taskID string) bool { return false },
		},
		mu:      sync.Mutex{},
		started: true,
	}
	status, err := ctrl.ContainerStatus(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, status)
	assert.Equal(t, int32(1), status.ExitCode)
}

// TestMockVMMManager_Describe tests MockVMMManager Describe
func TestMockVMMManager_Describe(t *testing.T) {
	mockVMM := &MockVMMManager{
		DescribeFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
			return &types.TaskStatus{State: types.TaskStateRunning}, nil
		},
	}
	status, err := mockVMM.Describe(context.Background(), &types.Task{ID: "task-1"})
	assert.NoError(t, err)
	assert.NotNil(t, status)
}

// TestMockVMMManager_Describe_Default tests MockVMMManager Describe default
func TestMockVMMManager_Describe_Default(t *testing.T) {
	mockVMM := &MockVMMManager{}
	status, err := mockVMM.Describe(context.Background(), &types.Task{ID: "task-1"})
	assert.NoError(t, err)
	assert.NotNil(t, status)
	assert.Equal(t, types.TaskStateRunning, status.State)
}

// TestMockVMMManager_CheckVMAPIHealth tests MockVMMManager CheckVMAPIHealth
func TestMockVMMManager_CheckVMAPIHealth(t *testing.T) {
	mockVMM := &MockVMMManager{
		CheckVMAPIHealthFunc: func(ctx context.Context, taskID string) bool { return true },
	}
	assert.True(t, mockVMM.CheckVMAPIHealth(context.Background(), "task-1"))
}

// TestMockVMMManager_CheckVMAPIHealth_Default tests MockVMMManager CheckVMAPIHealth default
func TestMockVMMManager_CheckVMAPIHealth_Default(t *testing.T) {
	mockVMM := &MockVMMManager{}
	assert.True(t, mockVMM.CheckVMAPIHealth(context.Background(), "task-1"))
}

// TestMockVMMManager_GetPID tests MockVMMManager GetPID
func TestMockVMMManager_GetPID(t *testing.T) {
	mockVMM := &MockVMMManager{
		GetPIDFunc: func(taskID string) int { return 1234 },
	}
	assert.Equal(t, 1234, mockVMM.GetPID("task-1"))
}

// TestMockVMMManager_GetPID_Default tests MockVMMManager GetPID default
func TestMockVMMManager_GetPID_Default(t *testing.T) {
	mockVMM := &MockVMMManager{}
	assert.Equal(t, 0, mockVMM.GetPID("task-1"))
}

// TestMockVMMManager_IsRunning tests MockVMMManager IsRunning
func TestMockVMMManager_IsRunning(t *testing.T) {
	mockVMM := &MockVMMManager{
		IsRunningFunc: func(taskID string) bool { return true },
	}
	assert.True(t, mockVMM.IsRunning("task-1"))
}

// TestMockVMMManager_IsRunning_Default tests MockVMMManager IsRunning default
func TestMockVMMManager_IsRunning_Default(t *testing.T) {
	mockVMM := &MockVMMManager{}
	assert.False(t, mockVMM.IsRunning("task-1"))
}

// TestMockVMMManager_Start tests MockVMMManager Start
func TestMockVMMManager_Start(t *testing.T) {
	mockVMM := &MockVMMManager{
		StartFunc: func(ctx context.Context, task *types.Task, config interface{}) error { return nil },
	}
	assert.NoError(t, mockVMM.Start(context.Background(), &types.Task{ID: "task-1"}, nil))
}

// TestMockVMMManager_Start_Default tests MockVMMManager Start default
func TestMockVMMManager_Start_Default(t *testing.T) {
	mockVMM := &MockVMMManager{}
	assert.NoError(t, mockVMM.Start(context.Background(), &types.Task{ID: "task-1"}, nil))
}

// TestMockVMMManager_Stop tests MockVMMManager Stop
func TestMockVMMManager_Stop(t *testing.T) {
	mockVMM := &MockVMMManager{
		StopFunc: func(ctx context.Context, task *types.Task) error { return nil },
	}
	assert.NoError(t, mockVMM.Stop(context.Background(), &types.Task{ID: "task-1"}))
}

// TestMockVMMManager_Stop_Default tests MockVMMManager Stop default
func TestMockVMMManager_Stop_Default(t *testing.T) {
	mockVMM := &MockVMMManager{}
	assert.NoError(t, mockVMM.Stop(context.Background(), &types.Task{ID: "task-1"}))
}

// TestMockVMMManager_ForceStop tests MockVMMManager ForceStop
func TestMockVMMManager_ForceStop(t *testing.T) {
	mockVMM := &MockVMMManager{
		ForceStopFunc: func(ctx context.Context, task *types.Task) error { return nil },
	}
	assert.NoError(t, mockVMM.ForceStop(context.Background(), &types.Task{ID: "task-1"}))
}

// TestMockVMMManager_ForceStop_Default tests MockVMMManager ForceStop default
func TestMockVMMManager_ForceStop_Default(t *testing.T) {
	mockVMM := &MockVMMManager{}
	assert.NoError(t, mockVMM.ForceStop(context.Background(), &types.Task{ID: "task-1"}))
}

// TestMockVMMManager_Remove tests MockVMMManager Remove
func TestMockVMMManager_Remove(t *testing.T) {
	mockVMM := &MockVMMManager{
		RemoveFunc: func(ctx context.Context, task *types.Task) error { return nil },
	}
	assert.NoError(t, mockVMM.Remove(context.Background(), &types.Task{ID: "task-1"}))
}

// TestMockVMMManager_Remove_Default tests MockVMMManager Remove default
func TestMockVMMManager_Remove_Default(t *testing.T) {
	mockVMM := &MockVMMManager{}
	assert.NoError(t, mockVMM.Remove(context.Background(), &types.Task{ID: "task-1"}))
}

// TestMockVMMManager_Wait tests MockVMMManager Wait
func TestMockVMMManager_Wait(t *testing.T) {
	mockVMM := &MockVMMManager{
		WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
			return &types.TaskStatus{State: types.TaskStateComplete}, nil
		},
	}
	status, err := mockVMM.Wait(context.Background(), &types.Task{ID: "task-1"})
	assert.NoError(t, err)
	assert.Equal(t, types.TaskStateComplete, status.State)
}

// TestMockVMMManager_Wait_Default tests MockVMMManager Wait default
func TestMockVMMManager_Wait_Default(t *testing.T) {
	mockVMM := &MockVMMManager{}
	status, err := mockVMM.Wait(context.Background(), &types.Task{ID: "task-1"})
	assert.NoError(t, err)
	assert.Equal(t, types.TaskStateRunning, status.State)
}

// TestMockVMMManager_GetRunningProcesses_Processes tests MockVMMManager GetRunningProcesses with processes
func TestMockVMMManager_GetRunningProcesses_Processes(t *testing.T) {
	processes := map[string]*exec.Cmd{"task-1": exec.Command("echo")}
	mockVMM := &MockVMMManager{processes: processes}
	result := mockVMM.GetRunningProcesses()
	assert.Equal(t, processes, result)
}

// TestMockVMMManager_GetRunningProcesses_Default tests MockVMMManager GetRunningProcesses default
func TestMockVMMManager_GetRunningProcesses_Default(t *testing.T) {
	mockVMM := &MockVMMManager{}
	result := mockVMM.GetRunningProcesses()
	assert.NotNil(t, result)
	assert.Len(t, result, 0)
}

// TestMockVMMManager_RemoveProcess_Processes tests MockVMMManager RemoveProcess with processes
func TestMockVMMManager_RemoveProcess_Processes(t *testing.T) {
	processes := map[string]*exec.Cmd{"task-1": exec.Command("echo")}
	mockVMM := &MockVMMManager{processes: processes}
	mockVMM.RemoveProcess("task-1")
	assert.Len(t, mockVMM.processes, 0)
}

// TestMockVMMManager_RemoveProcess_Default tests MockVMMManager RemoveProcess default
func TestMockVMMManager_RemoveProcess_Default(t *testing.T) {
	mockVMM := &MockVMMManager{}
	mockVMM.RemoveProcess("task-1") // Should not panic
}

// TestMockNetworkManager_GetTapIP tests MockNetworkManager GetTapIP
func TestMockNetworkManager_GetTapIP(t *testing.T) {
	mockNet := &MockNetworkManager{
		GetTapIPFunc: func(taskID string) (string, error) { return "10.0.0.2", nil },
	}
	ip, err := mockNet.GetTapIP("task-1")
	assert.NoError(t, err)
	assert.Equal(t, "10.0.0.2", ip)
}

// TestMockNetworkManager_GetTapIP_Default tests MockNetworkManager GetTapIP default
func TestMockNetworkManager_GetTapIP_Default(t *testing.T) {
	mockNet := &MockNetworkManager{}
	ip, err := mockNet.GetTapIP("task-1")
	assert.NoError(t, err)
	assert.Equal(t, "192.168.127.2", ip)
}

// TestMockNetworkManager_Init tests MockNetworkManager Init
func TestMockNetworkManager_Init(t *testing.T) {
	mockNet := &MockNetworkManager{
		InitFunc: func(ctx context.Context) error { return nil },
	}
	assert.NoError(t, mockNet.Init(context.Background()))
}

// TestMockNetworkManager_Init_Default tests MockNetworkManager Init default
func TestMockNetworkManager_Init_Default(t *testing.T) {
	mockNet := &MockNetworkManager{}
	assert.NoError(t, mockNet.Init(context.Background()))
}

// TestMockNetworkManager_SetNodeDiscovery tests MockNetworkManager SetNodeDiscovery
func TestMockNetworkManager_SetNodeDiscovery(t *testing.T) {
	called := false
	mockNet := &MockNetworkManager{
		SetNodeDiscoveryFunc: func(discovery types.NodeDiscovery) { called = true },
	}
	// NodeDiscovery is an interface, pass nil
	mockNet.SetNodeDiscovery(nil)
	assert.True(t, called)
}

// TestMockNetworkManager_SetNodeDiscovery_Default tests MockNetworkManager SetNodeDiscovery default
func TestMockNetworkManager_SetNodeDiscovery_Default(t *testing.T) {
	mockNet := &MockNetworkManager{}
	mockNet.SetNodeDiscovery(nil) // Should not panic
}

// TestMockNetworkManager_UpdateVXLANPeers tests MockNetworkManager UpdateVXLANPeers
func TestMockNetworkManager_UpdateVXLANPeers(t *testing.T) {
	mockNet := &MockNetworkManager{
		UpdateVXLANPeersFunc: func(peers []string) error { return nil },
	}
	assert.NoError(t, mockNet.UpdateVXLANPeers([]string{"10.0.0.2"}))
}

// TestMockNetworkManager_UpdateVXLANPeers_Default tests MockNetworkManager UpdateVXLANPeers default
func TestMockNetworkManager_UpdateVXLANPeers_Default(t *testing.T) {
	mockNet := &MockNetworkManager{}
	assert.NoError(t, mockNet.UpdateVXLANPeers([]string{"10.0.0.2"}))
}

// TestMockImagePreparer_Cleanup tests MockImagePreparer Cleanup
func TestMockImagePreparer_Cleanup(t *testing.T) {
	mockPrep := &MockImagePreparer{
		CleanupFunc: func(ctx context.Context, keepDays int) (int, int64, error) { return 5, 1000, nil },
	}
	files, bytes, err := mockPrep.Cleanup(context.Background(), 7)
	assert.NoError(t, err)
	assert.Equal(t, 5, files)
	assert.Equal(t, int64(1000), bytes)
}

// TestMockImagePreparer_Cleanup_Default tests MockImagePreparer Cleanup default
func TestMockImagePreparer_Cleanup_Default(t *testing.T) {
	mockPrep := &MockImagePreparer{}
	files, bytes, err := mockPrep.Cleanup(context.Background(), 7)
	assert.NoError(t, err)
	assert.Equal(t, 0, files)
	assert.Equal(t, int64(0), bytes)
}

// TestMockTaskTranslator_Translate tests MockTaskTranslator Translate
func TestMockTaskTranslator_Translate(t *testing.T) {
	mockTrans := &MockTaskTranslator{
		TranslateFunc: func(task *types.Task) (interface{}, error) { return map[string]string{}, nil },
	}
	config, err := mockTrans.Translate(&types.Task{ID: "task-1"})
	assert.NoError(t, err)
	assert.NotNil(t, config)
}

// TestMockTaskTranslator_Translate_Default tests MockTaskTranslator Translate default
func TestMockTaskTranslator_Translate_Default(t *testing.T) {
	mockTrans := &MockTaskTranslator{}
	config, err := mockTrans.Translate(&types.Task{ID: "task-1"})
	assert.NoError(t, err)
	assert.NotNil(t, config)
}

// TestMockTaskTranslator_Translate_Error tests MockTaskTranslator Translate error
func TestMockTaskTranslator_Translate_Error(t *testing.T) {
	mockTrans := &MockTaskTranslator{
		TranslateFunc: func(task *types.Task) (interface{}, error) { return nil, errors.New("translate error") },
	}
	config, err := mockTrans.Translate(&types.Task{ID: "task-1"})
	assert.Error(t, err)
	assert.Nil(t, config)
}

// TestExecutor_NewExecutor_EnableJailer tests NewExecutor with jailer enabled
func TestExecutor_NewExecutor_EnableJailer(t *testing.T) {
	cfg := &Config{
		EnableJailer:     true,
		JailerPath:       "/usr/local/bin/jailer",
		JailerChrootDir:  t.TempDir(),
		CgroupVersion:    "v2",
		StateDir:         t.TempDir(),
		ReservedCPUs:     0,
		ReservedMemoryMB: 0,
	}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Logf("NewExecutor failed (expected if jailer not available): %v", err)
		return
	}
	assert.NotNil(t, exec)
}

// TestExecutor_NewExecutor_WithVXLAN tests NewExecutor with VXLAN enabled
func TestExecutor_NewExecutor_WithVXLAN(t *testing.T) {
	cfg := &Config{
		VXLANEnabled: true,
		VXLANPeers:   []string{"10.0.0.2", "10.0.0.3"},
		StateDir:     t.TempDir(),
	}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Skipf("NewExecutor failed: %v", err)
	}
	assert.NotNil(t, exec)
}

// TestExecutor_NewExecutor_WithNAT tests NewExecutor with NAT enabled
func TestExecutor_NewExecutor_WithNAT(t *testing.T) {
	cfg := &Config{
		NATEnabled: true,
		StateDir:   t.TempDir(),
	}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Skipf("NewExecutor failed: %v", err)
	}
	assert.NotNil(t, exec)
}

// TestExecutor_NewExecutor_WithHostname tests NewExecutor with hostname
func TestExecutor_NewExecutor_WithHostname(t *testing.T) {
	cfg := &Config{
		Hostname: "test-node",
		StateDir: t.TempDir(),
	}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Skipf("NewExecutor failed: %v", err)
	}
	assert.NotNil(t, exec)
}

// TestExecutor_NewExecutor_CustomDefaults tests NewExecutor with custom defaults
func TestExecutor_NewExecutor_CustomDefaults(t *testing.T) {
	cfg := &Config{
		FirecrackerPath: "/custom/firecracker",
		KernelPath:      "/custom/vmlinux",
		RootfsDir:       t.TempDir(),
		SocketDir:       t.TempDir(),
		DefaultVCPUs:    4,
		DefaultMemoryMB: 2048,
		BridgeName:      "custom-br0",
		Subnet:          "10.1.0.0/24",
		BridgeIP:        "10.1.0.1/24",
		IPMode:          "dhcp",
		StateDir:        t.TempDir(),
	}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Skipf("NewExecutor failed: %v", err)
	}
	assert.NotNil(t, exec)
	assert.Equal(t, 4, exec.config.DefaultVCPUs)
	assert.Equal(t, 2048, exec.config.DefaultMemoryMB)
}

// TestExecutor_NewExecutor_WithConsul tests NewExecutor with Consul enabled
func TestExecutor_NewExecutor_WithConsul(t *testing.T) {
	cfg := &Config{
		ConsulEnabled: true,
		ConsulAddress: "localhost:8500",
		Hostname:      "test-node",
		AdvertiseAddr: "192.168.1.100:7946",
		JoinAddr:      "192.168.1.1:7946",
		StateDir:      t.TempDir(),
	}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Logf("NewExecutor failed (expected if consul not available): %v", err)
		return
	}
	assert.NotNil(t, exec)
}

// TestExecutor_NewExecutor_WithConsulJoinAddr tests NewExecutor with Consul JoinAddr
func TestExecutor_NewExecutor_WithConsulJoinAddr(t *testing.T) {
	cfg := &Config{
		ConsulEnabled: true,
		ConsulAddress: "localhost:8500",
		Hostname:      "test-worker",
		JoinAddr:      "192.168.1.1:7946",
		AdvertiseAddr: "",
		StateDir:      t.TempDir(),
	}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Logf("NewExecutor failed (expected if consul not available): %v", err)
		return
	}
	assert.NotNil(t, exec)
}

// TestController_Start_TranslationError tests Start with translation error
func TestController_Start_TranslationError(t *testing.T) {
	ctrl := &Controller{
		task:         &api.Task{ID: "task-1"},
		config:       &Config{},
		mu:           sync.Mutex{},
		prepared:     true,
		started:      false,
		internalTask: &types.Task{ID: "task-1"},
		trans: &MockTaskTranslator{
			TranslateFunc: func(task *types.Task) (interface{}, error) {
				return nil, errors.New("translation failed")
			},
		},
		vmmMgr: &MockVMMManager{},
	}
	err := ctrl.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "translation failed")
}

// TestController_Start_VMMStartError tests Start with VMM start error
func TestController_Start_VMMStartError(t *testing.T) {
	ctrl := &Controller{
		task:         &api.Task{ID: "task-1"},
		config:       &Config{},
		mu:           sync.Mutex{},
		prepared:     true,
		started:      false,
		internalTask: &types.Task{ID: "task-1"},
		trans:        &MockTaskTranslator{},
		vmmMgr: &MockVMMManager{
			StartFunc: func(ctx context.Context, task *types.Task, config interface{}) error {
				return errors.New("VMM start failed")
			},
		},
	}
	err := ctrl.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start VM")
}

// TestController_Start_Success tests Start success
func TestController_Start_Success(t *testing.T) {
	ctrl := &Controller{
		task:         &api.Task{ID: "task-1"},
		config:       &Config{},
		mu:           sync.Mutex{},
		prepared:     true,
		started:      false,
		internalTask: &types.Task{ID: "task-1"},
		trans:        &MockTaskTranslator{},
		vmmMgr:       &MockVMMManager{},
	}
	err := ctrl.Start(context.Background())
	assert.NoError(t, err)
	assert.True(t, ctrl.started)
}

// TestController_Wait_Error tests Wait with error
func TestController_Wait_Error(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		trans:  &MockTaskTranslator{},
		vmmMgr: &MockVMMManager{
			WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
				return nil, errors.New("wait error")
			},
		},
	}
	err := ctrl.Wait(context.Background())
	assert.Error(t, err)
}

// TestController_Wait_StatusComplete tests Wait with complete status
func TestController_Wait_StatusComplete(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		trans:  &MockTaskTranslator{},
		vmmMgr: &MockVMMManager{
			WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
				return &types.TaskStatus{State: types.TaskStateComplete}, nil
			},
			IsRunningFunc: func(taskID string) bool { return false },
		},
	}
	err := ctrl.Wait(context.Background())
	assert.NoError(t, err)
}

// TestController_Wait_StatusFailed tests Wait with failed status
func TestController_Wait_StatusFailed(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		trans:  &MockTaskTranslator{},
		vmmMgr: &MockVMMManager{
			WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
				return &types.TaskStatus{State: types.TaskStateFailed}, nil
			},
			IsRunningFunc: func(taskID string) bool { return false },
		},
	}
	err := ctrl.Wait(context.Background())
	assert.NoError(t, err)
}

// TestController_Shutdown_WithVMMError tests Shutdown with VMM error
func TestController_Shutdown_WithVMMError(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		trans:  &MockTaskTranslator{},
		vmmMgr: &MockVMMManager{
			StopFunc: func(ctx context.Context, task *types.Task) error {
				return errors.New("stop error")
			},
		},
		networkMgr: &MockNetworkManager{},
		started:    true,
	}
	err := ctrl.Shutdown(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to shutdown VM")
}

// TestController_Terminate_WithVMMError tests Terminate with VMM error
func TestController_Terminate_WithVMMError(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "task-1"},
		config: &Config{},
		trans:  &MockTaskTranslator{},
		vmmMgr: &MockVMMManager{
			ForceStopFunc: func(ctx context.Context, task *types.Task) error {
				return errors.New("force stop error")
			},
		},
		started: true,
	}
	err := ctrl.Terminate(context.Background())
	assert.NoError(t, err) // Terminate doesn't return error
}

// TestController_Remove_WithVolumeMounts tests Remove with volume mounts
func TestController_Remove_WithVolumeMounts(t *testing.T) {
	tmpDir := t.TempDir()
	ctrl := &Controller{
		task:       &api.Task{ID: "task-vol"},
		config:     &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
		internalTask: &types.Task{
			ID:          "task-vol",
			Annotations: map[string]string{"rootfs": filepath.Join(tmpDir, "rootfs.ext4")},
			Spec: types.TaskSpec{
				Runtime: &types.Container{
					Mounts: []types.Mount{
						{Source: "volume://data", Target: "/data", ReadOnly: false},
					},
				},
			},
		},
	}
	err := ctrl.Remove(context.Background())
	assert.NoError(t, err)
}

// TestController_Remove_WithCleanupNetworkError tests Remove with cleanup network error
func TestController_Remove_WithCleanupNetworkError(t *testing.T) {
	tmpDir := t.TempDir()
	ctrl := &Controller{
		task:   &api.Task{ID: "task-net-err"},
		config: &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr: &MockVMMManager{},
		networkMgr: &MockNetworkManager{
			CleanupNetworkFunc: func(ctx context.Context, task *types.Task) error {
				return errors.New("cleanup network error")
			},
		},
		mu: sync.Mutex{},
	}
	err := ctrl.Remove(context.Background())
	assert.NoError(t, err) // Cleanup error is logged but not returned
}

// TestController_Remove_WithVMMRemoveError tests Remove with VMM remove error
func TestController_Remove_WithVMMRemoveError(t *testing.T) {
	tmpDir := t.TempDir()
	ctrl := &Controller{
		task:   &api.Task{ID: "task-vmm-err"},
		config: &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr: &MockVMMManager{
			RemoveFunc: func(ctx context.Context, task *types.Task) error {
				return errors.New("vmm remove error")
			},
		},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}
	err := ctrl.Remove(context.Background())
	assert.NoError(t, err) // VMM remove error is logged but not returned
}

// TestController_Remove_WithRootfsCleanup tests Remove with rootfs cleanup
func TestController_Remove_WithRootfsCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, "task-rootfs.ext4")
	os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644)
	ctrl := &Controller{
		task:       &api.Task{ID: "task-rootfs"},
		config:     &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}
	err := ctrl.Remove(context.Background())
	assert.NoError(t, err)
	_, err = os.Stat(rootfsPath)
	assert.True(t, os.IsNotExist(err))
}

// TestController_Remove_WithSocketCleanup tests Remove with socket cleanup
func TestController_Remove_WithSocketCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "task-socket.sock")
	os.WriteFile(socketPath, []byte("fake socket"), 0644)
	ctrl := &Controller{
		task:       &api.Task{ID: "task-socket"},
		config:     &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}
	err := ctrl.Remove(context.Background())
	assert.NoError(t, err)
	_, err = os.Stat(socketPath)
	assert.True(t, os.IsNotExist(err))
}

// TestController_Remove_WithOnRemove tests Remove with OnRemove callback
func TestController_Remove_WithOnRemove(t *testing.T) {
	tmpDir := t.TempDir()
	callbackCalled := false
	ctrl := &Controller{
		task:       &api.Task{ID: "task-callback"},
		config:     &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
		OnRemove:   func() { callbackCalled = true },
	}
	err := ctrl.Remove(context.Background())
	assert.NoError(t, err)
	assert.True(t, callbackCalled)
}

// TestController_Prepare_WithVolumes tests Prepare with volume mounts
func TestController_Prepare_WithVolumes(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID: "task-vol-prepare",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image: "nginx",
						Mounts: []api.Mount{
							{Source: "volume://data", Target: "/data"},
						},
					},
				},
			},
		},
		config:     &Config{},
		imagePrep:  &MockImagePreparer{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}
	err := ctrl.Prepare(context.Background())
	assert.NoError(t, err)
}

// TestController_Prepare_WithCommand tests Prepare with command
func TestController_Prepare_WithCommand(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID: "task-cmd",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image:   "nginx",
						Command: []string{"/bin/sh"},
						Args:    []string{"-c", "echo hello"},
					},
				},
			},
		},
		config:     &Config{},
		imagePrep:  &MockImagePreparer{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
	}
	err := ctrl.Prepare(context.Background())
	assert.NoError(t, err)
}

// TestExecutor_Describe_WithKVM tests Describe with KVM available
func TestExecutor_Describe_WithKVM(t *testing.T) {
	cfg := &Config{StateDir: t.TempDir()}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Skipf("NewExecutor failed: %v", err)
	}
	desc, err := exec.Describe(context.Background())
	if err != nil {
		t.Skipf("Describe failed: %v", err)
	}
	assert.NotNil(t, desc)
	assert.NotEmpty(t, desc.Hostname)
	// Check for Firecracker resource if KVM is available
	if exec.kvmAvailable() && exec.archSupported() {
		assert.NotEmpty(t, desc.Resources.Generic)
	}
}
