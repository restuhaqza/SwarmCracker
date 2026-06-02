package e2e

import (
	"testing"
	"time"

	google_protobuf "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
)

// TestE2E_SwarmKit_Comprehensive validates SwarmKit API structures used by SwarmCracker.
// Note: Full deployment tests require swarmd-firecracker executor and are in full_workflow_test.go.
func TestE2E_SwarmKit_Comprehensive(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E in short mode")
	}

	t.Log("=== SwarmKit API Structure Validation ===")

	// Phase 1: SwarmKit service spec construction
	t.Run("Phase1_ServiceSpec", func(t *testing.T) {
		service := createSwarmKitService("test-service", "nginx:alpine", 2)

		assert.Equal(t, "test-service", service.Spec.Annotations.Name)
		assert.Equal(t, uint64(2), service.Spec.Mode.(*api.ServiceSpec_Replicated).Replicated.Replicas)
		assert.NotNil(t, service.Spec.Task)

		t.Logf("✓ Service spec: name=%s replicas=%d", service.Spec.Annotations.Name,
			service.Spec.Mode.(*api.ServiceSpec_Replicated).Replicated.Replicas)
	})

	// Phase 2: Task spec with container runtime
	t.Run("Phase2_TaskSpec", func(t *testing.T) {
		task := &api.Task{
			ID:        "task-test-1",
			ServiceID: "service-test-1",
			Status: api.TaskStatus{
				State:   api.TaskStateRunning,
				Message: "VM started via Firecracker",
			},
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image:   "redis:alpine",
						Command: []string{"redis-server"},
						Env:     []string{"MAX_MEMORY=512MB"},
					},
				},
				Resources: &api.ResourceRequirements{
					Reservations: &api.Resources{
						NanoCPUs:    2e9,           // 2 vCPUs
						MemoryBytes: 1024 * 1024 * 1024, // 1GB
					},
				},
			},
		}

		assert.Equal(t, api.TaskStateRunning, task.Status.State)
		assert.Equal(t, "task-test-1", task.ID)

		// Verify container spec is accessible
		container := task.Spec.GetContainer()
		assert.NotNil(t, container)
		assert.Equal(t, "redis:alpine", container.Image)

		t.Logf("✓ Task spec: id=%s state=%s image=%s", task.ID, task.Status.State, container.Image)
	})

	// Phase 3: Service with full lifecycle config
	t.Run("Phase3_FullServiceSpec", func(t *testing.T) {
		service := createSwarmKitService("full-service", "myapp:latest", 3)

		// Verify update config
		assert.NotNil(t, service.Spec.Update)
		assert.Equal(t, uint64(1), service.Spec.Update.Parallelism)
		assert.Equal(t, api.UpdateConfig_PAUSE, service.Spec.Update.FailureAction)

		// Verify rollback config
		assert.NotNil(t, service.Spec.Rollback)
		assert.Equal(t, api.UpdateConfig_PAUSE, service.Spec.Rollback.FailureAction)

		// Verify restart policy
		assert.NotNil(t, service.Spec.Task.Restart)
		assert.Equal(t, api.RestartOnFailure, service.Spec.Task.Restart.Condition)

		// Verify resources
		reservations := service.Spec.Task.Resources.Reservations
		assert.NotNil(t, reservations)
		assert.Equal(t, int64(1e9), reservations.NanoCPUs)

		t.Logf("✓ Full service spec validated: replicas=%d, parallelism=%d",
			service.Spec.Mode.(*api.ServiceSpec_Replicated).Replicated.Replicas,
			service.Spec.Update.Parallelism)
	})

	// Phase 4: Container-to-VM conversion spec
	t.Run("Phase4_FirecrackerSpec", func(t *testing.T) {
		// Simulates what translator does: task spec → Firecracker VM config
		service := createSwarmKitService("fc-test", "alpine:latest", 1)

		container := service.Spec.Task.GetContainer()
		assert.NotNil(t, container)
		assert.Equal(t, "alpine:latest", container.Image)

		reservations := service.Spec.Task.Resources.Reservations
		assert.NotNil(t, reservations)

		// These values map to Firecracker VM config:
		// NanoCPUs / 1e9 = vcpu_count
		// MemoryBytes / 1024 / 1024 = mem_size_mib
		vcpus := int(reservations.NanoCPUs / 1e9)
		memoryMB := int(reservations.MemoryBytes / 1024 / 1024)

		assert.Equal(t, 1, vcpus)
		assert.Equal(t, 512, memoryMB)

		t.Logf("✓ Firecracker spec: vcpus=%d memory_mb=%d image=%s", vcpus, memoryMB, container.Image)
	})

	t.Log("=== SwarmKit API Validation Completed ===")
}

// TestE2E_SwarmKit_TaskLifecycle validates task state machine transitions.
func TestE2E_SwarmKit_TaskLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E in short mode")
	}

	t.Log("=== Task Lifecycle State Machine ===")

	// Verify all task states are defined
	states := []api.TaskState{
		api.TaskStateNew,
		api.TaskStatePending,
		api.TaskStateAssigned,
		api.TaskStateAccepted,
		api.TaskStatePreparing,
		api.TaskStateReady,
		api.TaskStateStarting,
		api.TaskStateRunning,
		api.TaskStateCompleted,
		api.TaskStateShutdown,
		api.TaskStateFailed,
		api.TaskStateRejected,
		api.TaskStateOrphaned,
	}

	for _, state := range states {
		t.Logf("✓ TaskState: %s (%d)", state.String(), state)
	}

	assert.Equal(t, "NEW", api.TaskStateNew.String())
	assert.Equal(t, "RUNNING", api.TaskStateRunning.String())
	assert.Equal(t, "COMPLETE", api.TaskStateCompleted.String())
	assert.Equal(t, "FAILED", api.TaskStateFailed.String())
	assert.Equal(t, "SHUTDOWN", api.TaskStateShutdown.String())

	t.Log("=== Task Lifecycle Test Completed ===")
}

// createSwarmKitService creates a SwarmKit service specification.
func createSwarmKitService(name, image string, replicas uint64) *api.Service {
	return &api.Service{
		ID: name,
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{
				Name:   name,
				Labels: map[string]string{"test": "swarmkit-e2e"},
			},
			Task: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image:   image,
						Command: []string{},
						Args:    []string{},
						Env:     []string{"TEST=1"},
					},
				},
				Resources: &api.ResourceRequirements{
					Reservations: &api.Resources{
						NanoCPUs:    1e9,
						MemoryBytes: 512 * 1024 * 1024,
					},
				},
				Restart: &api.RestartPolicy{
					Condition: api.RestartOnFailure,
				},
			},
			Mode: &api.ServiceSpec_Replicated{
				Replicated: &api.ReplicatedService{
					Replicas: replicas,
				},
			},
			Update: &api.UpdateConfig{
				Parallelism:     1,
				Delay:           10 * time.Second,
				FailureAction:   api.UpdateConfig_PAUSE,
				Monitor:         google_protobuf.DurationProto(5e9),
				MaxFailureRatio: 0.1,
			},
			Rollback: &api.UpdateConfig{
				Parallelism:     1,
				Delay:           5 * time.Second,
				FailureAction:   api.UpdateConfig_PAUSE,
				Monitor:         google_protobuf.DurationProto(5e9),
				MaxFailureRatio: 0.1,
			},
		},
	}
}
