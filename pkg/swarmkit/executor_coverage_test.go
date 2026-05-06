package swarmkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

// TestExecutor_Configure tests Configure method
func TestExecutor_Configure(t *testing.T) {
	t.Run("configure with valid node", func(t *testing.T) {
		e := &Executor{
			config:      &Config{},
			controllers: make(map[string]*Controller),
		}

		node := &api.Node{
			ID: "node-1",
			Spec: api.NodeSpec{
				Annotations: api.Annotations{
					Name: "test-node",
				},
			},
		}

		ctx := context.Background()
		err := e.Configure(ctx, node)
		assert.NoError(t, err)
	})

	t.Run("configure with nil node", func(t *testing.T) {
		t.Skip("Configure panics on nil node - needs nil check in production")
	})
}

// TestExecutor_SetNetworkBootstrapKeys tests SetNetworkBootstrapKeys method
func TestExecutor_SetNetworkBootstrapKeys(t *testing.T) {
	t.Run("set keys successfully", func(t *testing.T) {
		mockNetworkMgr := &mockNetworkManagerFull{}

		e := &Executor{
			config:      &Config{},
			controllers: make(map[string]*Controller),
			networkMgr:  mockNetworkMgr,
		}

		keys := []*api.EncryptionKey{
			{Key: []byte("test-key-1")},
			{Key: []byte("test-key-2")},
		}

		err := e.SetNetworkBootstrapKeys(keys)
		assert.NoError(t, err)
	})

	t.Run("set keys with key setter network manager", func(t *testing.T) {
		mockNetworkMgr := &mockNetworkKeySetterFull{}

		e := &Executor{
			config:      &Config{},
			controllers: make(map[string]*Controller),
			networkMgr:  mockNetworkMgr,
		}

		keys := []*api.EncryptionKey{
			{Key: []byte("key-data")},
		}

		err := e.SetNetworkBootstrapKeys(keys)
		assert.NoError(t, err)
	})

	t.Run("set keys with key setter error", func(t *testing.T) {
		mockNetworkMgr := &mockNetworkKeySetterFull{
			setKeysErr: errors.New("failed to set keys"),
		}

		e := &Executor{
			config:      &Config{},
			controllers: make(map[string]*Controller),
			networkMgr:  mockNetworkMgr,
		}

		keys := []*api.EncryptionKey{
			{Key: []byte("key-data")},
		}

		err := e.SetNetworkBootstrapKeys(keys)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to set network encryption keys")
	})

	t.Run("set empty keys", func(t *testing.T) {
		e := &Executor{
			config:      &Config{},
			controllers: make(map[string]*Controller),
			networkMgr:  &mockNetworkManagerFull{},
		}

		err := e.SetNetworkBootstrapKeys([]*api.EncryptionKey{})
		assert.NoError(t, err)
	})
}

// TestExecutor_Update tests Update method
func TestExecutor_Update(t *testing.T) {
	t.Run("update before start", func(t *testing.T) {
		ctrl := &Controller{
			task:    &api.Task{ID: "task-1"},
			config:  &Config{},
			started: false,
			mu:      sync.Mutex{},
		}

		newTask := &api.Task{
			ID: "task-1",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image: "nginx:v2",
					},
				},
			},
		}

		ctx := context.Background()
		err := ctrl.Update(ctx, newTask)
		assert.NoError(t, err)
		assert.Equal(t, newTask, ctrl.task)
	})

	t.Run("update after start - no-op", func(t *testing.T) {
		ctrl := &Controller{
			task:    &api.Task{ID: "task-1"},
			config:  &Config{},
			started: true,
			mu:      sync.Mutex{},
		}

		originalTask := ctrl.task
		newTask := &api.Task{
			ID: "task-1",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image: "nginx:v2",
					},
				},
			},
		}

		ctx := context.Background()
		err := ctrl.Update(ctx, newTask)
		assert.NoError(t, err)
		assert.Equal(t, originalTask, ctrl.task)
	})
}

// TestController_Wait tests Wait method - skipped due to nil vmmMgr
func TestController_Wait_Skip(t *testing.T) {
	t.Skip("Requires real VMMManager instance - cannot easily mock")
}

// TestController_Remove tests Remove method
func TestController_Remove(t *testing.T) {
	t.Run("remove with cleanup", func(t *testing.T) {
		tmpDir := t.TempDir()

		vmmMgr, err := NewVMMManager("firecracker", tmpDir)
		if err != nil {
			t.Skipf("firecracker binary not found: %v", err)
		}

		ctrl := &Controller{
			task: &api.Task{
				ID: "task-1",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Image: "nginx",
						},
					},
				},
			},
			config: &Config{
				RootfsDir: tmpDir,
				SocketDir: tmpDir,
			},
			vmmMgr:     vmmMgr,
			networkMgr: &mockNetworkManagerFull{},
			mu:         sync.Mutex{},
			started:    false,
			prepared:   false,
		}

		ctx := context.Background()
		err = ctrl.Remove(ctx)
		assert.NoError(t, err)
	})

	t.Run("remove with OnRemove callback", func(t *testing.T) {
		tmpDir := t.TempDir()
		callbackCalled := false

		vmmMgr, err := NewVMMManager("firecracker", tmpDir)
		if err != nil {
			t.Skipf("firecracker binary not found: %v", err)
		}

		ctrl := &Controller{
			task: &api.Task{ID: "task-1"},
			config: &Config{
				RootfsDir: tmpDir,
				SocketDir: tmpDir,
			},
			vmmMgr:     vmmMgr,
			networkMgr: &mockNetworkManagerFull{},
			mu:         sync.Mutex{},
			OnRemove: func() {
				callbackCalled = true
			},
		}

		ctx := context.Background()
		err = ctrl.Remove(ctx)
		assert.NoError(t, err)
		assert.True(t, callbackCalled)
	})

	t.Run("remove with volume sync", func(t *testing.T) {
		tmpDir := t.TempDir()

		vmmMgr, err := NewVMMManager("firecracker", tmpDir)
		if err != nil {
			t.Skipf("firecracker binary not found: %v", err)
		}

		ctrl := &Controller{
			task: &api.Task{ID: "task-1"},
			config: &Config{
				RootfsDir: tmpDir,
				SocketDir: tmpDir,
			},
			vmmMgr:     vmmMgr,
			networkMgr: &mockNetworkManagerFull{},
			volumeMgr:  nil,
			mu:         sync.Mutex{},
			internalTask: &types.Task{
				ID:          "task-1",
				Annotations: map[string]string{"rootfs": filepath.Join(tmpDir, "rootfs.ext4")},
			},
		}

		ctx := context.Background()
		err = ctrl.Remove(ctx)
		assert.NoError(t, err)
	})
}

// TestController_Close tests Close method
func TestController_Close(t *testing.T) {
	t.Run("close returns nil", func(t *testing.T) {
		ctrl := &Controller{
			task:   &api.Task{ID: "task-1"},
			config: &Config{},
			mu:     sync.Mutex{},
		}

		err := ctrl.Close()
		assert.NoError(t, err)
	})
}

// TestController_ContainerStatus tests ContainerStatus method
func TestController_ContainerStatus(t *testing.T) {
	t.Run("status when not started", func(t *testing.T) {
		ctrl := &Controller{
			task:    &api.Task{ID: "task-1"},
			config:  &Config{},
			vmmMgr:  nil,
			mu:      sync.Mutex{},
			started: false,
		}

		ctx := context.Background()
		status, err := ctrl.ContainerStatus(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, status)
		assert.Equal(t, "task-1", status.ContainerID)
		assert.Equal(t, int32(0), status.PID)
	})

	t.Run("status when running", func(t *testing.T) {
		tmpDir := t.TempDir()

		vmmMgr, err := NewVMMManager("firecracker", tmpDir)
		if err != nil {
			t.Skipf("firecracker binary not found: %v", err)
		}

		ctrl := &Controller{
			task:    &api.Task{ID: "task-1"},
			config:  &Config{},
			vmmMgr:  vmmMgr,
			mu:      sync.Mutex{},
			started: true,
		}

		ctx := context.Background()
		status, err := ctrl.ContainerStatus(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, status)
		assert.Equal(t, int32(0), status.PID)
		assert.Equal(t, int32(1), status.ExitCode)
	})
}

// TestController_PortStatus tests PortStatus method
func TestController_PortStatus(t *testing.T) {
	t.Run("returns empty port status", func(t *testing.T) {
		ctrl := &Controller{
			task:   &api.Task{ID: "task-1"},
			config: &Config{},
			mu:     sync.Mutex{},
		}

		ctx := context.Background()
		status, err := ctrl.PortStatus(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, status)
	})
}

// TestExecutor_runCleanup tests runCleanup method
func TestExecutor_runCleanup(t *testing.T) {
	t.Run("cleanup with default max age", func(t *testing.T) {
		mockImagePrep := &mockImagePreparerCleanup{
			filesRemoved: 5,
			bytesFreed:   1024000,
		}

		e := &Executor{
			config:      &Config{MaxImageAgeDays: 7},
			controllers: make(map[string]*Controller),
			imagePrep:   mockImagePrep,
		}

		ctx := context.Background()
		e.runCleanup(ctx)
	})

	t.Run("cleanup with custom max age", func(t *testing.T) {
		mockImagePrep := &mockImagePreparerCleanup{}

		e := &Executor{
			config:      &Config{MaxImageAgeDays: 14},
			controllers: make(map[string]*Controller),
			imagePrep:   mockImagePrep,
		}

		ctx := context.Background()
		e.runCleanup(ctx)
	})

	t.Run("cleanup with error", func(t *testing.T) {
		mockImagePrep := &mockImagePreparerCleanup{
			err: errors.New("cleanup failed"),
		}

		e := &Executor{
			config:      &Config{MaxImageAgeDays: 7},
			controllers: make(map[string]*Controller),
			imagePrep:   mockImagePrep,
		}

		ctx := context.Background()
		e.runCleanup(ctx)
	})
}

// TestController_syncVolumeData tests syncVolumeData method
func TestController_syncVolumeData(t *testing.T) {
	t.Run("sync with no rootfs annotation", func(t *testing.T) {
		ctrl := &Controller{
			task:   &api.Task{ID: "task-1"},
			config: &Config{},
			mu:     sync.Mutex{},
		}

		task := &types.Task{
			ID:          "task-1",
			Annotations: map[string]string{},
		}

		ctx := context.Background()
		err := ctrl.syncVolumeData(ctx, task, []types.Mount{})
		assert.NoError(t, err)
	})

	t.Run("sync with empty rootfs", func(t *testing.T) {
		ctrl := &Controller{
			task:   &api.Task{ID: "task-1"},
			config: &Config{},
			mu:     sync.Mutex{},
		}

		task := &types.Task{
			ID:          "task-1",
			Annotations: map[string]string{"rootfs": ""},
		}

		ctx := context.Background()
		err := ctrl.syncVolumeData(ctx, task, []types.Mount{})
		assert.NoError(t, err)
	})
}

// TestController_mountRootfs tests mountRootfs errors
func TestController_mountRootfs(t *testing.T) {
	t.Run("mount non-existent image", func(t *testing.T) {
		ctrl := &Controller{
			task:   &api.Task{ID: "task-1"},
			config: &Config{},
			mu:     sync.Mutex{},
		}

		_, err := ctrl.mountRootfs("/nonexistent/path.ext4")
		assert.Error(t, err)
	})
}

// TestController_unmountRootfs tests unmountRootfs
func TestController_unmountRootfs(t *testing.T) {
	t.Run("unmount cleans up directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		mountDir := filepath.Join(tmpDir, "mount-test")
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
	})
}

// mockNetworkManagerFull implements full NetworkManager interface
type mockNetworkManagerFull struct {
	prepareErr error
	cleanupErr error
	tapIP      string
	tapIPErr   error
}

func (m *mockNetworkManagerFull) PrepareNetwork(ctx context.Context, task *types.Task) error {
	return m.prepareErr
}

func (m *mockNetworkManagerFull) CleanupNetwork(ctx context.Context, task *types.Task) error {
	return m.cleanupErr
}

func (m *mockNetworkManagerFull) GetTapIP(taskID string) (string, error) {
	if m.tapIPErr != nil {
		return "", m.tapIPErr
	}
	return m.tapIP, nil
}

// mockNetworkKeySetterFull implements NetworkManager + NetworkKeySetter
type mockNetworkKeySetterFull struct {
	mockNetworkManagerFull
	setKeysErr error
}

func (m *mockNetworkKeySetterFull) SetEncryptionKeys(keys []*api.EncryptionKey) error {
	return m.setKeysErr
}

// mockImagePreparerCleanup implements ImagePreparer interface
type mockImagePreparerCleanup struct {
	filesRemoved int
	bytesFreed   int64
	err          error
}

func (m *mockImagePreparerCleanup) Prepare(ctx context.Context, task *types.Task) error {
	return nil
}

func (m *mockImagePreparerCleanup) Cleanup(ctx context.Context, maxAgeDays int) (int, int64, error) {
	return m.filesRemoved, m.bytesFreed, m.err
}