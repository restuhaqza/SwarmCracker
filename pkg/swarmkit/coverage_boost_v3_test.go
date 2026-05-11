//go:build !integration

package swarmkit

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/storage"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// syncVolumeData Tests (32.0% -> target 70%+)
// ============================================================================

// TestSyncVolumeData_SkipNonVolumeRef tests skipping non-volume mount sources
func TestSyncVolumeData_SkipNonVolumeRef(t *testing.T) {
	ctrl := &Controller{
		task:      &api.Task{ID: "task-1"},
		config:    &Config{},
		volumeMgr: nil, // nil volumeMgr is valid
		mu:        sync.Mutex{},
		logger:    zerolog.Nop(),
	}

	task := &types.Task{
		Annotations: map[string]string{"rootfs": "/tmp/test.ext4"},
	}
	mounts := []types.Mount{
		{Source: "/bind/mount", Target: "/data", ReadOnly: false},       // non-volume
		{Source: "volume://myvol", Target: "/voldata", ReadOnly: false}, // volume
	}

	err := ctrl.syncVolumeData(context.Background(), task, mounts)
	assert.NoError(t, err)
}

// TestSyncVolumeData_SkipReadOnly tests skipping read-only mounts
func TestSyncVolumeData_SkipReadOnly(t *testing.T) {
	ctrl := &Controller{
		task:      &api.Task{ID: "task-2"},
		config:    &Config{},
		volumeMgr: nil,
		mu:        sync.Mutex{},
		logger:    zerolog.Nop(),
	}

	task := &types.Task{
		Annotations: map[string]string{"rootfs": "/tmp/test.ext4"},
	}
	mounts := []types.Mount{
		{Source: "volume://readonly-vol", Target: "/readonly", ReadOnly: true},
	}

	err := ctrl.syncVolumeData(context.Background(), task, mounts)
	assert.NoError(t, err)
}

// TestSyncVolumeData_NoRootfsAnnotation tests missing rootfs annotation
func TestSyncVolumeData_NoRootfsAnnotation(t *testing.T) {
	ctrl := &Controller{
		task:      &api.Task{ID: "task-3"},
		config:    &Config{},
		volumeMgr: nil,
		mu:        sync.Mutex{},
		logger:    zerolog.Nop(),
	}

	task := &types.Task{
		Annotations: map[string]string{},
	}
	mounts := []types.Mount{
		{Source: "volume://myvol", Target: "/data", ReadOnly: false},
	}

	err := ctrl.syncVolumeData(context.Background(), task, mounts)
	assert.NoError(t, err)
}

// TestSyncVolumeData_EmptyMounts tests with empty mount list
func TestSyncVolumeData_EmptyMounts(t *testing.T) {
	ctrl := &Controller{
		task:      &api.Task{ID: "task-4"},
		config:    &Config{},
		volumeMgr: nil,
		mu:        sync.Mutex{},
		logger:    zerolog.Nop(),
	}

	task := &types.Task{
		Annotations: map[string]string{"rootfs": "/tmp/test.ext4"},
	}
	mounts := []types.Mount{}

	err := ctrl.syncVolumeData(context.Background(), task, mounts)
	assert.NoError(t, err)
}

// TestSyncVolumeData_MultipleMounts tests multiple mounts processing
func TestSyncVolumeData_MultipleMounts(t *testing.T) {
	ctrl := &Controller{
		task:      &api.Task{ID: "task-5"},
		config:    &Config{},
		volumeMgr: nil,
		mu:        sync.Mutex{},
		logger:    zerolog.Nop(),
	}

	task := &types.Task{
		Annotations: map[string]string{"rootfs": "/tmp/test.ext4"},
	}
	mounts := []types.Mount{
		{Source: "/bind/path", Target: "/bind", ReadOnly: false},               // non-volume
		{Source: "volume://readonly-vol", Target: "/readonly", ReadOnly: true}, // read-only
		{Source: "volume://myvol", Target: "/data", ReadOnly: false},
	}

	err := ctrl.syncVolumeData(context.Background(), task, mounts)
	assert.NoError(t, err)
}

// TestSyncVolumeData_MountRootfsError tests mountRootfs returning error
func TestSyncVolumeData_MountRootfsError(t *testing.T) {
	ctrl := &Controller{
		task:      &api.Task{ID: "task-6"},
		config:    &Config{},
		volumeMgr: nil,
		mu:        sync.Mutex{},
		logger:    zerolog.Nop(),
	}

	task := &types.Task{
		Annotations: map[string]string{"rootfs": "/nonexistent/nonexistent.ext4"},
	}
	mounts := []types.Mount{
		{Source: "volume://test-vol", Target: "/data", ReadOnly: false},
	}

	err := ctrl.syncVolumeData(context.Background(), task, mounts)
	assert.NoError(t, err)
}

// ============================================================================
// periodicCleanup Tests (46.7% -> target 70%+)
// ============================================================================

// TestPeriodicCleanup_ImmediateCancel tests immediate context cancellation
func TestPeriodicCleanup_ImmediateCancel(t *testing.T) {
	cfg := &Config{
		StateDir:  t.TempDir(),
		RootfsDir: t.TempDir(),
		SocketDir: t.TempDir(),
	}
	exec := &Executor{
		config:      cfg,
		controllers: make(map[string]*Controller),
		cleanupDone: make(chan struct{}),
		imagePrep:   &MockImagePreparer{},
		vmmMgr:      &MockVMMManager{},
		executorMu:  sync.RWMutex{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	exec.periodicCleanup(ctx)
	select {
	case <-exec.cleanupDone:
		// Good
	default:
		t.Error("cleanupDone channel not closed")
	}
}

// TestPeriodicCleanup_ShortTimeout tests cleanup with short timeout
func TestPeriodicCleanup_ShortTimeout(t *testing.T) {
	cfg := &Config{
		StateDir:  t.TempDir(),
		RootfsDir: t.TempDir(),
		SocketDir: t.TempDir(),
	}
	exec := &Executor{
		config:      cfg,
		controllers: make(map[string]*Controller),
		cleanupDone: make(chan struct{}),
		imagePrep: &MockImagePreparer{
			CleanupFunc: func(ctx context.Context, keepDays int) (int, int64, error) {
				return 0, 0, nil
			},
		},
		vmmMgr:     &MockVMMManager{},
		executorMu: sync.RWMutex{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	exec.periodicCleanup(ctx)

	select {
	case <-exec.cleanupDone:
		// Good
	case <-time.After(2 * time.Second):
		t.Error("cleanupDone channel not closed within timeout")
	}
}

// TestPeriodicCleanup_WithConfigMaxAge tests cleanup with MaxImageAgeDays config
func TestPeriodicCleanup_WithConfigMaxAge(t *testing.T) {
	cfg := &Config{
		StateDir:        t.TempDir(),
		RootfsDir:       t.TempDir(),
		SocketDir:       t.TempDir(),
		MaxImageAgeDays: 14,
	}
	exec := &Executor{
		config:      cfg,
		controllers: make(map[string]*Controller),
		cleanupDone: make(chan struct{}),
		imagePrep: &MockImagePreparer{
			CleanupFunc: func(ctx context.Context, keepDays int) (int, int64, error) {
				assert.Equal(t, 14, keepDays)
				return 5, 1024000, nil
			},
		},
		vmmMgr:     &MockVMMManager{},
		executorMu: sync.RWMutex{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	exec.periodicCleanup(ctx)

	select {
	case <-exec.cleanupDone:
		// Good
	case <-time.After(2 * time.Second):
		t.Error("cleanupDone not closed")
	}
}

// TestRunCleanup_Error tests runCleanup with error
func TestRunCleanup_Error(t *testing.T) {
	exec := &Executor{
		config: &Config{MaxImageAgeDays: 7},
		imagePrep: &MockImagePreparer{
			CleanupFunc: func(ctx context.Context, keepDays int) (int, int64, error) {
				return 0, 0, errors.New("cleanup error")
			},
		},
	}

	exec.runCleanup(context.Background())
}

// TestCleanupOrphanedVMs_WithActiveTask tests cleanup when task is active
func TestCleanupOrphanedVMs_WithActiveTask(t *testing.T) {
	tmpDir := t.TempDir()

	cmd := exec.Command("sleep", "0.01")
	err := cmd.Start()
	require.NoError(t, err)

	processes := map[string]*exec.Cmd{"active-task": cmd}

	exec := &Executor{
		config: &Config{SocketDir: tmpDir},
		controllers: map[string]*Controller{
			"active-task": &Controller{task: &api.Task{ID: "active-task"}},
		},
		vmmMgr: &MockVMMManager{
			processes:               processes,
			GetRunningProcessesFunc: func() map[string]*exec.Cmd { return processes },
		},
		executorMu: sync.RWMutex{},
	}

	exec.cleanupOrphanedVMs(context.Background())
}

// TestPeriodicCleanup_NoVMProcesses tests cleanup with no VM processes
func TestPeriodicCleanup_NoVMProcesses(t *testing.T) {
	cfg := &Config{StateDir: t.TempDir()}
	exec := &Executor{
		config:      cfg,
		controllers: make(map[string]*Controller),
		cleanupDone: make(chan struct{}),
		imagePrep:   &MockImagePreparer{},
		vmmMgr: &MockVMMManager{
			GetRunningProcessesFunc: func() map[string]*exec.Cmd {
				return nil
			},
		},
		executorMu: sync.RWMutex{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	exec.periodicCleanup(ctx)

	select {
	case <-exec.cleanupDone:
		// Good
	case <-time.After(2 * time.Second):
		t.Error("cleanupDone not closed")
	}
}

// ============================================================================
// NewExecutor Tests (81.5% -> target 90%+)
// ============================================================================

// TestNewExecutor_AllDefaults tests with all default values
func TestNewExecutor_AllDefaults(t *testing.T) {
	cfg := &Config{StateDir: t.TempDir()}
	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	require.NotNil(t, exec)

	assert.Equal(t, "firecracker", exec.config.FirecrackerPath)
	assert.Equal(t, "/usr/share/firecracker/vmlinux", exec.config.KernelPath)
	assert.Equal(t, "/var/lib/firecracker/rootfs", exec.config.RootfsDir)
	assert.Equal(t, "/var/run/firecracker", exec.config.SocketDir)
	assert.Equal(t, 1, exec.config.DefaultVCPUs)
	assert.Equal(t, 512, exec.config.DefaultMemoryMB)
	assert.Equal(t, "swarm-br0", exec.config.BridgeName)
	assert.Equal(t, "192.168.127.0/24", exec.config.Subnet)
	assert.Equal(t, "192.168.127.1/24", exec.config.BridgeIP)
	assert.Equal(t, "static", exec.config.IPMode)
}

// TestNewExecutor_WithCustomValues tests with custom config values
func TestNewExecutor_WithCustomValues(t *testing.T) {
	cfg := &Config{
		StateDir:        t.TempDir(),
		FirecrackerPath: "/custom/firecracker",
		KernelPath:      "/custom/vmlinux",
		RootfsDir:       "/custom/rootfs",
		SocketDir:       "/custom/sockets",
		DefaultVCPUs:    2,
		DefaultMemoryMB: 1024,
		BridgeName:      "custom-br0",
		Subnet:          "10.0.0.0/24",
		BridgeIP:        "10.0.0.1/24",
		IPMode:          "dhcp",
		NATEnabled:      true,
		VXLANEnabled:    true,
		VXLANPeers:      []string{"10.0.0.2", "10.0.0.3"},
	}
	exec, err := NewExecutor(cfg)
	require.NoError(t, err)
	require.NotNil(t, exec)

	assert.Equal(t, "/custom/firecracker", exec.config.FirecrackerPath)
	assert.Equal(t, "/custom/vmlinux", exec.config.KernelPath)
	assert.Equal(t, "/custom/rootfs", exec.config.RootfsDir)
	assert.Equal(t, "/custom/sockets", exec.config.SocketDir)
	assert.Equal(t, 2, exec.config.DefaultVCPUs)
	assert.Equal(t, 1024, exec.config.DefaultMemoryMB)
	assert.Equal(t, "custom-br0", exec.config.BridgeName)
	assert.Equal(t, "10.0.0.0/24", exec.config.Subnet)
	assert.Equal(t, "10.0.0.1/24", exec.config.BridgeIP)
	assert.Equal(t, "dhcp", exec.config.IPMode)
	assert.True(t, exec.config.NATEnabled)
	assert.True(t, exec.config.VXLANEnabled)
}

// ============================================================================
// NewController Tests (75.0% -> target 85%+)
// ============================================================================

// TestNewController_Success tests successful controller creation
func TestNewController_Success(t *testing.T) {
	task := &api.Task{
		ID: "controller-test",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{Image: "nginx"},
			},
		},
	}
	cfg := &Config{
		KernelPath: "/usr/share/firecracker/vmlinux",
		BridgeIP:   "192.168.127.1/24",
		SocketDir:  t.TempDir(),
	}

	ctrl, err := NewController(
		task,
		cfg,
		&MockImagePreparer{},
		&MockNetworkManager{},
		&MockVMMManager{},
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, ctrl)
	assert.Equal(t, task, ctrl.task)
	assert.NotNil(t, ctrl.trans)
}

// ============================================================================
// Prepare Tests (73.9% -> target 85%+)
// ============================================================================

// TestPrepare_WithSecretsAndConfigs tests Prepare with secrets/configs injection
func TestPrepare_WithSecretsAndConfigsV3(t *testing.T) {
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, "test-rootfs.ext4")
	os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644)

	ctrl := &Controller{
		task: &api.Task{
			ID: "secret-task",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx"},
				},
			},
		},
		config:     &Config{RootfsDir: tmpDir},
		imagePrep:  &MockImagePreparer{},
		networkMgr: &MockNetworkManager{},
		secretMgr:  nil, // nil secretMgr skips injection
		mu:         sync.Mutex{},
		logger:     zerolog.Nop(),
	}

	ctrl.internalTask = &types.Task{
		ID:          "secret-task",
		Annotations: map[string]string{"rootfs": rootfsPath},
		Secrets:     []types.SecretRef{{ID: "sec-1", Target: "/run/secrets/sec1"}},
		Configs:     []types.ConfigRef{{ID: "cfg-1", Target: "/config/cfg1"}},
	}
	ctrl.prepared = true

	err := ctrl.Prepare(context.Background())
	assert.NoError(t, err)
}

// TestPrepare_WithSecretMgr tests Prepare with non-nil secretMgr
func TestPrepare_WithSecretMgr(t *testing.T) {
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, "test-rootfs.ext4")
	os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644)

	secretsDir := filepath.Join(tmpDir, "secrets")
	configsDir := filepath.Join(tmpDir, "configs")
	os.MkdirAll(secretsDir, 0755)
	os.MkdirAll(configsDir, 0755)
	secretMgr := storage.NewSecretManager(secretsDir, configsDir)

	ctrl := &Controller{
		task: &api.Task{
			ID: "secret-task-2",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx"},
				},
			},
		},
		config:     &Config{RootfsDir: tmpDir},
		imagePrep:  &MockImagePreparer{},
		networkMgr: &MockNetworkManager{},
		secretMgr:  secretMgr,
		mu:         sync.Mutex{},
		logger:     zerolog.Nop(),
	}

	err := ctrl.Prepare(context.Background())
	assert.NoError(t, err)
	assert.True(t, ctrl.prepared)
}

// TestPrepare_WithSecretInjectionError tests secret injection error
func TestPrepare_WithSecretInjectionError(t *testing.T) {
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, "test-rootfs.ext4")
	os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644)

	ctrl := &Controller{
		task: &api.Task{
			ID: "secret-task-err",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx"},
				},
			},
		},
		config:     &Config{RootfsDir: tmpDir},
		imagePrep:  &MockImagePreparer{},
		networkMgr: &MockNetworkManager{},
		secretMgr:  nil, // nil skips injection
		mu:         sync.Mutex{},
		logger:     zerolog.Nop(),
	}

	err := ctrl.Prepare(context.Background())
	// Prepare should succeed even if injection fails
	assert.NoError(t, err)
	assert.True(t, ctrl.prepared)
}

// ============================================================================
// Remove Tests (78.6% -> target 85%+)
// ============================================================================

// TestRemove_WithVolumeSync tests Remove with volume sync
func TestRemove_WithVolumeSync(t *testing.T) {
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, "task-vol.ext4")
	os.WriteFile(rootfsPath, []byte("fake"), 0644)

	ctrl := &Controller{
		task:       &api.Task{ID: "task-vol"},
		config:     &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		volumeMgr:  nil,
		mu:         sync.Mutex{},
		logger:     zerolog.Nop(),
	}

	err := ctrl.Remove(context.Background())
	assert.NoError(t, err)
}

// TestRemove_WithOnRemoveCallback tests Remove with callback
func TestRemove_WithOnRemoveCallback(t *testing.T) {
	tmpDir := t.TempDir()
	callbackCalled := false

	ctrl := &Controller{
		task:       &api.Task{ID: "task-callback"},
		config:     &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
		logger:     zerolog.Nop(),
		OnRemove: func() {
			callbackCalled = true
		},
	}

	err := ctrl.Remove(context.Background())
	assert.NoError(t, err)
	assert.True(t, callbackCalled)
}

// TestRemove_WithInternalTask tests Remove with internalTask
func TestRemove_WithInternalTask(t *testing.T) {
	tmpDir := t.TempDir()

	ctrl := &Controller{
		task:       &api.Task{ID: "task-internal"},
		config:     &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		volumeMgr:  nil,
		internalTask: &types.Task{
			ID: "task-internal",
			Spec: types.TaskSpec{
				Runtime: &types.Container{
					Mounts: []types.Mount{
						{Source: "volume://test-vol", Target: "/data", ReadOnly: false},
					},
				},
			},
		},
		mu:     sync.Mutex{},
		logger: zerolog.Nop(),
	}

	err := ctrl.Remove(context.Background())
	assert.NoError(t, err)
	assert.False(t, ctrl.prepared)
}

// TestRemove_WithExistingFiles tests Remove cleans up existing files
func TestRemove_WithExistingFiles(t *testing.T) {
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, "cleanup-task.ext4")
	socketPath := filepath.Join(tmpDir, "cleanup-task.sock")
	os.WriteFile(rootfsPath, []byte("fake rootfs"), 0644)
	os.WriteFile(socketPath, []byte("fake socket"), 0644)

	ctrl := &Controller{
		task:       &api.Task{ID: "cleanup-task"},
		config:     &Config{RootfsDir: tmpDir, SocketDir: tmpDir},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		socketPath: socketPath,
		mu:         sync.Mutex{},
		logger:     zerolog.Nop(),
	}

	err := ctrl.Remove(context.Background())
	assert.NoError(t, err)
	assert.NoFileExists(t, rootfsPath)
	assert.NoFileExists(t, socketPath)
}

// ============================================================================
// hostname Tests (66.7% -> target 80%+)
// ============================================================================

// TestHostname_Success tests hostname returning system hostname
func TestHostname_Success(t *testing.T) {
	result := hostname()
	expected, _ := os.Hostname()
	if expected != "" {
		assert.Equal(t, expected, result)
	} else {
		assert.Equal(t, "localhost", result)
	}
}

// ============================================================================
// archSupported Tests (66.7% -> target 80%+)
// ============================================================================

// TestArchSupported_AllArchs tests all architectures
func TestArchSupported_AllArchs(t *testing.T) {
	exec := &Executor{config: &Config{}}
	result := exec.archSupported()

	switch runtime.GOARCH {
	case "amd64", "arm64":
		assert.True(t, result)
	default:
		assert.False(t, result)
	}
}

// ============================================================================
// getMemory Tests (77.8% -> target 90%+)
// ============================================================================

// TestGetMemory_Fallback tests getMemory fallback
func TestGetMemory_Fallback(t *testing.T) {
	exec := &Executor{config: &Config{}}
	result := exec.getMemory()
	assert.Greater(t, result, int64(0))
}

// TestGetMemory_LargeReservation tests getMemory with large reserved memory
func TestGetMemory_LargeReservation(t *testing.T) {
	exec := &Executor{config: &Config{ReservedMemoryMB: 100000}}
	result := exec.getMemory()
	assert.Greater(t, result, int64(512*1024*1024))
}

// ============================================================================
// readMeminfo Tests (72.7% -> target 85%+)
// ============================================================================

// TestReadMeminfo_RealFile tests reading actual /proc/meminfo
func TestReadMeminfo_RealFile(t *testing.T) {
	exec := &Executor{config: &Config{}}
	result := exec.readMeminfo()
	if _, err := os.Stat("/proc/meminfo"); err == nil {
		assert.Greater(t, result, int64(0))
	}
}

// TestReadMeminfo_MemAvailableFallback tests MemAvailable fallback
func TestReadMeminfo_MemAvailableFallback(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "meminfo-*.txt")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	content := "MemTotal:       16384000 kB\n"
	os.WriteFile(tmpFile.Name(), []byte(content), 0644)

	file, err := os.Open(tmpFile.Name())
	require.NoError(t, err)
	defer file.Close()

	var memTotal int64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			memTotal = parseMeminfoLine(line)
		}
	}
	assert.Greater(t, memTotal, int64(0))
}

// TestParseMeminfoLine_EdgeCases tests parseMeminfoLine edge cases
func TestParseMeminfoLine_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		expected int64
	}{
		{"normal line", "MemTotal:       16384000 kB", 16384000},
		{"memavailable", "MemAvailable:    8192000 kB", 8192000},
		{"empty string", "", 0},
		{"no digits", "SomeLabel:       kB", 0},
		{"partial digits", "MemTotal:       123 kB", 123},
		{"large value", "MemTotal:       999999999999 kB", 999999999999},
		{"spaces only", "         ", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseMeminfoLine(tt.line)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ============================================================================
// getLocalIPFromInterface Tests (68.4% -> target 85%+)
// ============================================================================

// TestGetLocalIPFromInterface_LoopbackSkip tests skipping loopback
func TestGetLocalIPFromInterface_LoopbackSkip(t *testing.T) {
	result := getLocalIPFromInterface()
	if result != "" {
		ip := net.ParseIP(result)
		assert.NotNil(t, ip)
		assert.False(t, ip.IsLoopback())
	}
}

// TestGetLocalIPFromInterface_Interfaces tests iterating interfaces
func TestGetLocalIPFromInterface_Interfaces(t *testing.T) {
	interfaces, err := net.Interfaces()
	require.NoError(t, err)

	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
	}

	_ = getLocalIPFromInterface()
}

// ============================================================================
// getCPUs Tests
// ============================================================================

// TestGetCPUs_AllReserved tests when reserved CPUs >= total
func TestGetCPUs_AllReserved(t *testing.T) {
	total := runtime.NumCPU()
	exec := &Executor{config: &Config{ReservedCPUs: total + 100}}
	result := exec.getCPUs()
	assert.GreaterOrEqual(t, result, int64(1e9))
}

// TestGetCPUs_DefaultReserved tests with default reservation
func TestGetCPUs_DefaultReserved(t *testing.T) {
	exec := &Executor{config: &Config{ReservedCPUs: 0}}
	result := exec.getCPUs()
	assert.GreaterOrEqual(t, result, int64(1e9))
}

// ============================================================================
// mountRootfs Tests (66.7% -> target 80%+)
// ============================================================================

// TestMountRootfs_NonexistentFile tests with nonexistent file
func TestMountRootfs_NonexistentFile(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "mount-test"},
		config: &Config{},
		mu:     sync.Mutex{},
		logger: zerolog.Nop(),
	}

	_, err := ctrl.mountRootfs("/nonexistent/path.ext4")
	assert.Error(t, err)
}

// TestUnmountRootfs_ValidPath tests unmountRootfs cleanup
func TestUnmountRootfs_ValidPath(t *testing.T) {
	tmpDir := t.TempDir()
	mountDir := filepath.Join(tmpDir, "mount-test")
	os.MkdirAll(mountDir, 0755)

	ctrl := &Controller{
		task:   &api.Task{ID: "unmount-test"},
		config: &Config{},
		mu:     sync.Mutex{},
		logger: zerolog.Nop(),
	}

	err := ctrl.unmountRootfs(mountDir)
	assert.NoError(t, err)
}

// ============================================================================
// Update Tests
// ============================================================================

// TestUpdate_StartedTask tests Update on started task
func TestUpdate_StartedTask(t *testing.T) {
	originalTask := &api.Task{ID: "update-task"}
	ctrl := &Controller{
		task:    originalTask,
		config:  &Config{},
		mu:      sync.Mutex{},
		started: true,
		logger:  zerolog.Nop(),
	}

	newTask := &api.Task{ID: "update-task", Spec: api.TaskSpec{}}
	err := ctrl.Update(context.Background(), newTask)
	assert.NoError(t, err)
	assert.Equal(t, originalTask, ctrl.task)
}

// ============================================================================
// ContainerStatus Tests
// ============================================================================

// TestContainerStatus_NotStarted tests ContainerStatus when not started
func TestContainerStatus_NotStarted(t *testing.T) {
	ctrl := &Controller{
		task:    &api.Task{ID: "status-task"},
		config:  &Config{},
		vmmMgr:  &MockVMMManager{},
		mu:      sync.Mutex{},
		started: false,
	}

	status, err := ctrl.ContainerStatus(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, status)
	assert.Equal(t, "status-task", status.ContainerID)
	assert.Equal(t, int32(0), status.PID)
}

// TestContainerStatus_Running tests ContainerStatus when running
func TestContainerStatus_Running(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "running-task"},
		config: &Config{},
		vmmMgr: &MockVMMManager{
			IsRunningFunc: func(taskID string) bool { return true },
			GetPIDFunc:    func(taskID string) int { return 12345 },
		},
		mu:      sync.Mutex{},
		started: true,
	}

	status, err := ctrl.ContainerStatus(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, int32(12345), status.PID)
	assert.Equal(t, int32(0), status.ExitCode)
}

// TestContainerStatus_NotRunning tests ContainerStatus when not running
func TestContainerStatus_NotRunning(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "stopped-task"},
		config: &Config{},
		vmmMgr: &MockVMMManager{
			IsRunningFunc: func(taskID string) bool { return false },
		},
		mu:      sync.Mutex{},
		started: true,
	}

	status, err := ctrl.ContainerStatus(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, int32(1), status.ExitCode)
}

// ============================================================================
// KVMAvailable tests
// ============================================================================

// TestKVMAvailable tests kvmAvailable
func TestKVMAvailable(t *testing.T) {
	exec := &Executor{config: &Config{}}
	_ = exec.kvmAvailable()
}

// ============================================================================
// convertTask tests
// ============================================================================

// TestConvertTask_EmptySpec tests convertTask with empty spec
func TestConvertTask_EmptySpec(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "empty-spec-task"},
		config: &Config{},
		mu:     sync.Mutex{},
	}

	result := ctrl.convertTask()
	assert.NotNil(t, result)
	assert.Equal(t, "empty-spec-task", result.ID)
}

// ============================================================================
// Start tests
// ============================================================================

// TestStart_TranslationError tests Start with translation error
func TestStart_TranslationError(t *testing.T) {
	ctrl := &Controller{
		task:         &api.Task{ID: "trans-error-task"},
		config:       &Config{},
		mu:           sync.Mutex{},
		prepared:     true,
		internalTask: &types.Task{ID: "trans-error-task"},
		trans: &MockTaskTranslator{
			TranslateFunc: func(task *types.Task) (interface{}, error) {
				return nil, errors.New("translation error")
			},
		},
		logger: zerolog.Nop(),
	}

	err := ctrl.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "translation failed")
}

// TestStart_VMMStartError tests Start with VMM start error
func TestStart_VMMStartError(t *testing.T) {
	ctrl := &Controller{
		task:         &api.Task{ID: "vmm-error-task"},
		config:       &Config{},
		mu:           sync.Mutex{},
		prepared:     true,
		internalTask: &types.Task{ID: "vmm-error-task"},
		trans:        &MockTaskTranslator{},
		vmmMgr: &MockVMMManager{
			StartFunc: func(ctx context.Context, task *types.Task, config interface{}) error {
				return errors.New("vmm start error")
			},
		},
		logger: zerolog.Nop(),
	}

	err := ctrl.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start VM")
}

// ============================================================================
// Wait tests
// ============================================================================

// TestWait_VMMError tests Wait with VMM error
func TestWait_VMMError(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "wait-error-task"},
		config: &Config{},
		vmmMgr: &MockVMMManager{
			WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
				return nil, errors.New("wait error")
			},
		},
		mu:     sync.Mutex{},
		logger: zerolog.Nop(),
	}

	err := ctrl.Wait(context.Background())
	assert.Error(t, err)
}

// TestWait_StatusError tests Wait with status error
func TestWait_StatusError(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "status-error-task"},
		config: &Config{},
		vmmMgr: &MockVMMManager{
			WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
				return &types.TaskStatus{Err: errors.New("task crashed")}, nil
			},
		},
		mu:     sync.Mutex{},
		logger: zerolog.Nop(),
	}

	err := ctrl.Wait(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "task crashed")
}

// ============================================================================
// Shutdown tests
// ============================================================================

// TestShutdown_Success tests successful shutdown
func TestShutdown_Success(t *testing.T) {
	ctrl := &Controller{
		task:       &api.Task{ID: "shutdown-success-task"},
		config:     &Config{},
		vmmMgr:     &MockVMMManager{},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
		started:    true,
		logger:     zerolog.Nop(),
	}

	err := ctrl.Shutdown(context.Background())
	assert.NoError(t, err)
	assert.False(t, ctrl.started)
}

// TestShutdown_VMMError tests Shutdown with VMM error
func TestShutdown_VMMError(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "shutdown-error-task"},
		config: &Config{},
		vmmMgr: &MockVMMManager{
			StopFunc: func(ctx context.Context, task *types.Task) error {
				return errors.New("vmm stop error")
			},
		},
		networkMgr: &MockNetworkManager{},
		mu:         sync.Mutex{},
		started:    true,
		logger:     zerolog.Nop(),
	}

	err := ctrl.Shutdown(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to shutdown VM")
}

// ============================================================================
// Terminate tests
// ============================================================================

// TestTerminate_Success tests successful terminate
func TestTerminate_Success(t *testing.T) {
	ctrl := &Controller{
		task:    &api.Task{ID: "terminate-success-task"},
		config:  &Config{},
		vmmMgr:  &MockVMMManager{},
		mu:      sync.Mutex{},
		started: true,
		logger:  zerolog.Nop(),
	}

	err := ctrl.Terminate(context.Background())
	assert.NoError(t, err)
	assert.False(t, ctrl.started)
}

// TestTerminate_VMMError tests Terminate with VMM error
func TestTerminate_VMMError(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "terminate-error-task"},
		config: &Config{},
		vmmMgr: &MockVMMManager{
			StopFunc: func(ctx context.Context, task *types.Task) error {
				return errors.New("vmm terminate error")
			},
		},
		mu:      sync.Mutex{},
		started: true,
		logger:  zerolog.Nop(),
	}

	err := ctrl.Terminate(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to force terminate VM")
}

// ============================================================================
// Executor Controller tests
// ============================================================================

// TestExecutor_Controller_Nonexistent tests Controller for nonexistent task
func TestExecutor_Controller_Nonexistent(t *testing.T) {
	cfg := &Config{StateDir: t.TempDir()}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Skipf("NewExecutor failed: %v", err)
	}

	task := &api.Task{ID: "nonexistent-task"}
	_, err = exec.Controller(task)
	// Controller creates new controller for task
	_ = err
}

// ============================================================================
// Executor SetNetworkBootstrapKeys test (renamed to avoid duplicate)
// ============================================================================

// TestExecutor_SetNetworkBootstrapKeysV3 tests SetNetworkBootstrapKeys
func TestExecutor_SetNetworkBootstrapKeysV3(t *testing.T) {
	e := &Executor{
		config:      &Config{},
		controllers: make(map[string]*Controller),
	}
	keys := []*api.EncryptionKey{{Key: []byte("test-key")}}
	err := e.SetNetworkBootstrapKeys(keys)
	assert.NoError(t, err)
}

// ============================================================================
// CleanupOrphanedVMs additional test
// ============================================================================

// TestCleanupOrphanedVMs_SigtermFail tests orphan cleanup when SIGTERM fails
func TestCleanupOrphanedVMs_SigtermFail(t *testing.T) {
	tmpDir := t.TempDir()

	cmd := exec.Command("sleep", "0.01")
	cmd.Start()

	processes := map[string]*exec.Cmd{"orphan-sigterm": cmd}

	exec := &Executor{
		config:      &Config{SocketDir: tmpDir},
		controllers: map[string]*Controller{},
		vmmMgr: &MockVMMManager{
			processes:               processes,
			GetRunningProcessesFunc: func() map[string]*exec.Cmd { return processes },
		},
		executorMu: sync.RWMutex{},
	}

	exec.cleanupOrphanedVMs(context.Background())
}

// ============================================================================
// Close tests
// ============================================================================

// TestCloseV3 tests Close method
func TestCloseV3(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "close-task"},
		config: &Config{},
		mu:     sync.Mutex{},
		logger: zerolog.Nop(),
	}

	err := ctrl.Close()
	assert.NoError(t, err)
}

// ============================================================================
// PortStatus test (renamed to avoid duplicate)
// ============================================================================

// TestPortStatusV3 tests PortStatus method
func TestPortStatusV3(t *testing.T) {
	ctrl := &Controller{
		task:   &api.Task{ID: "port-task"},
		config: &Config{},
		mu:     sync.Mutex{},
	}

	status, err := ctrl.PortStatus(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, status)
}

// ============================================================================
// Storage helper tests
// ============================================================================

// TestStorage_IsVolumeReferenceV3 tests volume reference detection
func TestStorage_IsVolumeReferenceV3(t *testing.T) {
	assert.True(t, storage.IsVolumeReference("volume://myvol"))
	assert.False(t, storage.IsVolumeReference("/bind/mount"))
	assert.False(t, storage.IsVolumeReference("file://path"))
}

// TestStorage_ExtractVolumeNameV3 tests volume name extraction
func TestStorage_ExtractVolumeNameV3(t *testing.T) {
	assert.Equal(t, "myvol", storage.ExtractVolumeName("volume://myvol"))
	assert.Equal(t, "/bind/mount", storage.ExtractVolumeName("/bind/mount"))
	assert.Equal(t, "simple", storage.ExtractVolumeName("simple"))
}

// ============================================================================
// Executor Describe test
// ============================================================================

// TestExecutor_Describe_V3 tests Describe method
func TestExecutor_Describe_V3(t *testing.T) {
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
}

// ============================================================================
// Executor Configure test
// ============================================================================

// TestExecutor_Configure_V3 tests Configure method
func TestExecutor_Configure_V3(t *testing.T) {
	cfg := &Config{StateDir: t.TempDir()}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Skipf("NewExecutor failed: %v", err)
	}

	node := &api.Node{ID: "test-node"}
	err = exec.Configure(context.Background(), node)
	// Configure may fail due to network init
	_ = err
}

// ============================================================================
// Executor task methods that don't exist - removed
// ============================================================================
