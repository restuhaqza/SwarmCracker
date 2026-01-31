package fixtures

import (
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TestTask returns a standard test task
func TestTask(id, image string) *types.Task {
	container := &types.Container{
		Image:   image,
		Command: []string{"/bin/sh"},
		Args:    []string{"-c", "echo hello && sleep 30"},
		Env:     []string{"TEST=1", "ENV=value"},
	}

	return &types.Task{
		ID:        id,
		ServiceID: "test-service",
		NodeID:    "node-1",
		Spec: types.TaskSpec{
			Runtime: container,
			Resources: types.ResourceRequirements{
				Limits: &types.Resources{
					NanoCPUs:    1 * 1e9,
					MemoryBytes: 512 * 1024 * 1024,
				},
			},
			Restart: types.RestartPolicy{
				Condition: types.RestartPolicy_ON_FAILURE,
			},
		},
		Status: types.TaskStatus{
			State:   types.TaskState_PENDING,
			Message: "Created for testing",
		},
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "swarm-br0",
							},
						},
					},
				},
			},
		},
		Annotations: make(map[string]string),
	}
}

// TestTaskWithResources returns a test task with specific resources
func TestTaskWithResources(id, image string, cpus int64, memoryMB int64) *types.Task {
	task := TestTask(id, image)
	if task.Spec.Resources.Limits != nil {
		task.Spec.Resources.Limits.NanoCPUs = cpus * 1e9
		task.Spec.Resources.Limits.MemoryBytes = memoryMB * 1024 * 1024
	}
	return task
}

// TestTaskWithCommand returns a test task with custom command
func TestTaskWithCommand(id, image string, command []string, args []string) *types.Task {
	task := TestTask(id, image)
	if container, ok := task.Spec.Runtime.(*types.Container); ok {
		container.Command = command
		container.Args = args
	}
	return task
}

// TestTaskWithEnv returns a test task with environment variables
func TestTaskWithEnv(id, image string, env []string) *types.Task {
	task := TestTask(id, image)
	if container, ok := task.Spec.Runtime.(*types.Container); ok {
		container.Env = env
	}
	return task
}

// TestNetwork returns a test network spec
func TestNetwork(id, name string) *types.NetworkSpec {
	return &types.NetworkSpec{
		DriverConfig: &types.DriverConfig{
			Bridge: &types.BridgeConfig{
				Name: name,
			},
		},
	}
}

// Common test images
const (
	ImageAlpine  = "alpine:latest"
	ImageNginx   = "nginx:alpine"
	ImageRedis   = "redis:alpine"
	ImageBusybox = "busybox:latest"
	ImageUbuntu  = "ubuntu:latest"
)

// Common test values
const (
	TestServiceName = "test-service"
	TestNetworkID   = "test-network-1"
	TestNodeID      = "test-node-1"
)

// Small task for quick tests
func SmallTestTask(id string) *types.Task {
	return TestTaskWithResources(id, ImageAlpine, 1, 256)
}

// Medium task for normal tests
func MediumTestTask(id string) *types.Task {
	return TestTaskWithResources(id, ImageNginx, 2, 512)
}

// Large task for stress tests
func LargeTestTask(id string) *types.Task {
	return TestTaskWithResources(id, ImageUbuntu, 4, 2048)
}
