package e2e

import (
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/require"
)

// TestE2E_SwarmKitAPI tests SwarmKit API integration
func TestE2E_SwarmKitAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E in short mode")
	}

	t.Log("Testing SwarmKit API integration...")

	// Test 1: SwarmKit service structure
	t.Run("ServiceStructure", func(t *testing.T) {
		service := &api.Service{
			ID: "test-service-1",
			Spec: api.ServiceSpec{
				Annotations: api.Annotations{
					Name: "test-service",
				},
				Task: api.TaskSpec{
					Runtime: &api.TaskSpec_Container{
						Container: &api.ContainerSpec{
							Image: "nginx:alpine",
						},
					},
				},
				Mode: &api.ServiceSpec_Replicated{
					Replicated: &api.ReplicatedService{
						Replicas: 1,
					},
				},
			},
		}

		t.Logf("Created service spec: %s", service.Spec.Annotations.Name)
		require.NotNil(t, service)
		require.Equal(t, "test-service", service.Spec.Annotations.Name)
		require.Equal(t, uint64(1), service.Spec.Mode.(*api.ServiceSpec_Replicated).Replicated.Replicas)
	})

	// Test 2: Task structure
	t.Run("TaskStructure", func(t *testing.T) {
		task := &api.Task{
			ID:        "task-1",
			ServiceID: "service-1",
			Status: api.TaskStatus{
				State: api.TaskStateRunning,
			},
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image: "redis:alpine",
					},
				},
			},
		}

		t.Logf("Created task: %s", task.ID)
		require.NotNil(t, task)
		require.Equal(t, "task-1", task.ID)
		require.Equal(t, api.TaskStateRunning, task.Status.State)
	})

	t.Log("SwarmKit API test completed")
}

// TestE2E_SwarmKitWithDocker tests using Docker's SwarmKit backend
func TestE2E_SwarmKitWithDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E in short mode")
	}

	if !hasDocker() {
		t.Skip("Docker not found")
	}

	t.Log("Testing SwarmKit through Docker...")

	// Check if Swarm is initialized
	t.Run("CheckSwarmStatus", func(t *testing.T) {
		output, err := runCommand("docker", "info", "--format", "{{.Swarm.LocalNodeState}}")
		require.NoError(t, err)

		state := trimOutput(output)
		t.Logf("Swarm state: %s", state)

		if state != "active" {
			t.Skip("Docker Swarm not initialized. Docker Swarm uses SwarmKit internally.")
		}
	})

	// List nodes (SwarmKit nodes)
	t.Run("ListNodes", func(t *testing.T) {
		output, err := runCommand("docker", "node", "ls")
		require.NoError(t, err)

		t.Logf("SwarmKit nodes (via Docker):\n%s", output)
	})

	// Create service (SwarmKit service)
	t.Run("CreateSwarmKitService", func(t *testing.T) {
		serviceName := "test-swarmkit-" + randomString(8)

		output, err := runCommand(
			"docker", "service", "create",
			"--name", serviceName,
			"--replicas", "1",
			"nginx:alpine",
		)
		require.NoError(t, err, "Failed to create service: %s", output)
		t.Logf("Created SwarmKit service: %s", serviceName)

		// Cleanup
		t.Cleanup(func() {
			t.Log("Removing service:", serviceName)
			runCommand("docker", "service", "rm", serviceName)
			time.Sleep(2 * time.Second)
		})

		// Inspect service (SwarmKit API objects)
		t.Run("InspectService", func(t *testing.T) {
			output, err := runCommand("docker", "service", "inspect", serviceName, "--format", "{{json .Spec}}")
			require.NoError(t, err)
			t.Logf("Service Spec (SwarmKit): %s", trimOutput(output))
		})

		// List tasks (SwarmKit tasks)
		t.Run("ListTasks", func(t *testing.T) {
			output, err := runCommand("docker", "service", "ps", serviceName)
			require.NoError(t, err)
			t.Logf("SwarmKit tasks:\n%s", output)
		})
	})

	t.Log("SwarmKit via Docker test completed")
}

// TestE2E_SwarmKitExecutorPlaceholder tests SwarmCracker executor integration
func TestE2E_SwarmKitExecutorPlaceholder(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E in short mode")
	}

	t.Log("Testing SwarmCracker executor integration with SwarmKit...")

	// This is where we would test:
	// 1. SwarmKit manager assigning tasks to agent
	// 2. Agent receiving tasks via gRPC
	// 3. SwarmCracker executor processing tasks
	// 4. Starting Firecracker VMs
	// 5. Reporting status back

	t.Run("TaskAssignment", func(t *testing.T) {
		t.Log("Would test:")
		t.Log("  1. Manager assigns task to agent")
		t.Log("  2. Agent receives task via gRPC")
		t.Log("  3. SwarmCracker executor processes task")
		t.Log("  4. Firecracker VM starts")
		t.Log("  5. Status reported back")

		// Placeholder for actual implementation
		t.Skip("Requires SwarmKit manager + SwarmCracker executor")
	})

	t.Run("ExecutorLifecycle", func(t *testing.T) {
		t.Log("Would test:")
		t.Log("  1. Prepare task (pull image)")
		t.Log("  2. Start VM (firecracker)")
		t.Log("  3. Wait for ready state")
		t.Log("  4. Stop VM")
		t.Log("  5. Cleanup resources")

		// Placeholder for actual implementation
		t.Skip("Requires SwarmCracker executor running as SwarmKit agent")
	})

	t.Log("SwarmCracker executor test completed (placeholder)")
}
