package swarmkit

import (
	"os/exec"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewExecutor(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
		},
		{
			name: "valid config with defaults",
			config: &Config{
				FirecrackerPath: "firecracker",
			},
			wantErr: false,
		},
		{
			name: "valid config with all fields",
			config: &Config{
				FirecrackerPath: "/usr/bin/firecracker",
				KernelPath:      "/vmlinux",
				RootfsDir:       "/rootfs",
				SocketDir:       "/sockets",
				DefaultVCPUs:    2,
				DefaultMemoryMB: 1024,
				BridgeName:      "test-br0",
				Debug:           true,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec, err := NewExecutor(tt.config)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, exec)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, exec)
				assert.NotNil(t, exec.controllers)

				if tt.config != nil {
					// Check defaults were set
					if tt.config.KernelPath == "" {
						assert.Equal(t, "/usr/share/firecracker/vmlinux", exec.config.KernelPath)
					}
					if tt.config.RootfsDir == "" {
						assert.Equal(t, "/var/lib/firecracker/rootfs", exec.config.RootfsDir)
					}
					if tt.config.SocketDir == "" {
						assert.Equal(t, "/var/run/firecracker", exec.config.SocketDir)
					}
					if tt.config.DefaultVCPUs == 0 {
						assert.Equal(t, 1, exec.config.DefaultVCPUs)
					}
					if tt.config.DefaultMemoryMB == 0 {
						assert.Equal(t, 512, exec.config.DefaultMemoryMB)
					}
					if tt.config.BridgeName == "" {
						assert.Equal(t, "swarm-br0", exec.config.BridgeName)
					}
				}
			}
		})
	}
}

func TestExecutor_Controller(t *testing.T) {
	exec, err := NewExecutor(&Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/vmlinux",
		RootfsDir:       "/rootfs",
		SocketDir:       "/sockets",
	})
	require.NoError(t, err)

	task := &api.Task{
		ID: "test-task",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx:latest",
				},
			},
		},
	}

	// First call creates new controller
	ctrl1, err := exec.Controller(task)
	assert.NoError(t, err)
	assert.NotNil(t, ctrl1)

	// Second call returns same controller
	ctrl2, err := exec.Controller(task)
	assert.NoError(t, err)
	assert.Same(t, ctrl1, ctrl2, "Should return same controller instance")
}

func TestController_convertTask(t *testing.T) {
	tests := []struct {
		name           string
		task           *api.Task
		expectedCmd    []string
		expectedArgs   []string
		expectedEnv    []string
		expectedMounts int
	}{
		{
			name: "task with command and args",
			task: &api.Task{
				ID:        "task-1",
				ServiceID: "svc-1",
				NodeID:    "node-1",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Image:   "nginx",
							Command: []string{"nginx"},
							Args:    []string{"-g", "daemon off"},
						},
					},
				},
			},
			expectedCmd:  []string{"nginx"},
			expectedArgs: []string{"-g", "daemon off"},
		},
		{
			name: "task with only args",
			task: &api.Task{
				ID: "task-2",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Image: "alpine",
							Args:  []string{"sh", "-c", "echo hello"},
						},
					},
				},
			},
			expectedCmd:  []string{},
			expectedArgs: []string{"sh", "-c", "echo hello"},
		},
		{
			name: "task with resources",
			task: &api.Task{
				ID: "task-3",
				Spec: api.TaskSpec{
					Resources: &api.ResourceRequirements{
						Reservations: &api.Resources{
							NanoCPUs:    1e9,               // 1 CPU
							MemoryBytes: 512 * 1024 * 1024, // 512 MB
						},
					},
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Image: "app",
						},
					},
				},
			},
		},
		{
			name: "task with env vars",
			task: &api.Task{
				ID: "task-4",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Image: "redis",
							Env:   []string{"REDIS_PORT=6379", "DEBUG=false"},
						},
					},
				},
			},
			expectedEnv: []string{"REDIS_PORT=6379", "DEBUG=false"},
		},
		{
			name: "task with mounts",
			task: &api.Task{
				ID: "task-5",
				Spec: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Image: "db",
							Mounts: []api.Mount{
								{
									Target:   "/data",
									Source:   "/host/data",
									ReadOnly: true,
								},
								{
									Target: "/config",
									Source: "/host/config",
								},
							},
						},
					},
				},
			},
			expectedMounts: 2,
		},
		{
			name: "task without container runtime",
			task: &api.Task{
				ID: "task-6",
				Spec: api.TaskSpec{
					Runtime: nil,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := &Controller{
				task: tt.task,
			}

			internalTask := ctrl.convertTask()

			// Basic fields
			assert.Equal(t, tt.task.ID, internalTask.ID)
			assert.Equal(t, tt.task.ServiceID, internalTask.ServiceID)
			assert.Equal(t, tt.task.NodeID, internalTask.NodeID)

			// Container runtime
			if tt.task.Spec.Runtime != nil {
				container := internalTask.Spec.Runtime.(*types.Container)
				assert.NotNil(t, container)

				if len(tt.expectedCmd) > 0 {
					assert.Equal(t, tt.expectedCmd, container.Command)
				}
				if len(tt.expectedArgs) > 0 {
					assert.Equal(t, tt.expectedArgs, container.Args)
				}
				if len(tt.expectedEnv) > 0 {
					assert.Equal(t, tt.expectedEnv, container.Env)
				}
				if tt.expectedMounts > 0 {
					assert.Equal(t, tt.expectedMounts, len(container.Mounts))
				}

				// Check first mount details
				if tt.expectedMounts > 0 {
					mount := tt.task.Spec.Runtime.(*api.TaskSpec_Container).Container.Mounts[0]
					assert.Equal(t, mount.Target, container.Mounts[0].Target)
					assert.Equal(t, mount.Source, container.Mounts[0].Source)
					assert.Equal(t, mount.ReadOnly, container.Mounts[0].ReadOnly)
				}

				// Check resources
				if tt.task.Spec.Resources != nil && tt.task.Spec.Resources.Reservations != nil {
					res := internalTask.Spec.Resources.Reservations
					assert.Equal(t, tt.task.Spec.Resources.Reservations.NanoCPUs, res.NanoCPUs)
					assert.Equal(t, tt.task.Spec.Resources.Reservations.MemoryBytes, res.MemoryBytes)
				}
			} else {
				// Should return minimal task
				assert.NotNil(t, internalTask)
			}
		})
	}
}

func TestVMMManager_NewVMMManager(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		socket  string
		wantErr bool
	}{
		{
			name:    "valid parameters",
			path:    "firecracker",
			socket:  "/tmp/test-sockets",
			wantErr: false,
		},
		{
			name:    "empty firecracker path resolves via LookPath",
			path:    "",
			socket:  "/tmp/sockets",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip if firecracker not in PATH (CI doesn't have it installed)
			if tt.path == "" {
				if _, err := exec.LookPath("firecracker"); err != nil {
					t.Skip("firecracker not in PATH")
				}
			}

			mgr, err := NewVMMManager(tt.path, tt.socket)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, mgr)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, mgr)
				assert.NotNil(t, mgr.processes)

				if tt.path != "" {
					assert.Equal(t, tt.path, mgr.firecrackerPath)
				} else {
					// LookPath resolves to absolute path
					assert.NotEmpty(t, mgr.firecrackerPath)
					assert.Contains(t, mgr.firecrackerPath, "firecracker")
				}

				if tt.socket != "" {
					assert.Equal(t, tt.socket, mgr.socketDir)
				} else {
					assert.Equal(t, "/var/run/firecracker", mgr.socketDir)
				}
			}
		})
	}
}
