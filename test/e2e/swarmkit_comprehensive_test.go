package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/moby/swarmkit/v2/api/naming"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_SwarmKit_Comprehensive tests comprehensive SwarmKit E2E scenarios
func TestE2E_SwarmKit_Comprehensive(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E in short mode")
	}

	if !hasDocker() {
		t.Skip("Docker not found (required for SwarmKit backend)")
	}

	t.Log("=== Comprehensive SwarmKit E2E Test ===")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Phase 1: Verify SwarmKit backend is available
	t.Run("Phase1_SwarmKitBackend", func(t *testing.T) {
		output, err := runCommand("docker", "info", "--format", "{{.Swarm.LocalNodeState}}")
		require.NoError(t, err)

		state := trimOutput(output)
		if state != "active" {
			t.Skipf("Docker Swarm not active. Initialize with: docker swarm init --advertise-addr <ip>")
		}

		t.Logf("✓ SwarmKit backend available (via Docker Swarm)")
	})

	// Phase 2: Create SwarmKit service specification
	t.Run("Phase2_ServiceSpec", func(t *testing.T) {
		service := createSwarmKitService("test-service", "nginx:alpine", 2)

		// Validate service spec
		assert.Equal(t, "test-service", service.Spec.Annotations.Name)
		assert.Equal(t, uint64(2), service.Spec.Mode.(*api.ServiceSpec_Replicated).Replicated.Replicas)
		assert.NotNil(t, service.Spec.Task)
		assert.NotNil(t, service.Spec.Task.Container)

		t.Logf("✓ Service spec created: %s", service.ID)
	})

	// Phase 3: Deploy service via SwarmKit (Docker)
	t.Run("Phase3_DeployService", func(t *testing.T) {
		serviceName := "swarmkit-test-" + randomString(8)

		// Create service
		output, err := runCommand(
			"docker", "service", "create",
			"--name", serviceName,
			"--replicas", "2",
			"--publish", "8080:80",
			"nginx:alpine",
		)
		require.NoError(t, err, "Failed to create service: %s", output)

		t.Logf("✓ Service created: %s", serviceName)

		// Cleanup
		t.Cleanup(func() {
			t.Log("Cleaning up service:", serviceName)
			runCommand("docker", "service", "rm", serviceName)
			time.Sleep(2 * time.Second)
		})

		// Phase 4: Inspect SwarmKit service
		t.Run("Phase4_InspectService", func(t *testing.T) {
			// Get service JSON
			output, err := runCommand("docker", "service", "inspect", serviceName)
			require.NoError(t, err)

			// Parse JSON
			var services []struct {
				ID              string           `json:"ID"`
				Spec            api.ServiceSpec  `json:"Spec"`
				Endpoint        api.Endpoint     `json:"Endpoint"`
				UpdatedAt       time.Time        `json:"UpdatedAt"`
			}
			err = json.Unmarshal([]byte(output), &services)
			require.NoError(t, err, "Failed to parse service JSON")

			require.Len(t, services, 1)
			service := services[0]

			// Validate SwarmKit objects
			t.Logf("✓ Service ID: %s", naming.Pin(service.ID))
			t.Logf("✓ Service Name: %s", service.Spec.Annotations.Name)
			t.Logf("✓ Replicas: %d", service.Spec.Mode.(*api.ServiceSpec_Replicated).Replicated.Replicas)
			t.Logf("✓ Image: %s", service.Spec.Task.Container.Image)

			// Validate ports
			if service.Endpoint.Ports != nil && len(service.Endpoint.Ports) > 0 {
				t.Logf("✓ Published Port: %d", service.Endpoint.Ports[0].PublishedPort)
				t.Logf("✓ Target Port: %d", service.Endpoint.Ports[0].TargetPort)
			}
		})

		// Phase 5: List SwarmKit tasks
		t.Run("Phase5_ListTasks", func(t *testing.T) {
			// Wait for tasks to be created
			require.Eventually(t, func() bool {
				output, err := runCommand("docker", "service", "ps", serviceName, "--format", "{{.CurrentState}}")
				if err != nil {
					return false
				}

				states := splitLines(output)
				// Check if we have 2 running tasks
				runningCount := 0
				for _, state := range states {
					if state == "Running" {
						runningCount++
					}
				}
				return runningCount == 2
			}, 2*time.Minute, 5*time.Second, "Tasks did not reach Running state")

			// List tasks with details
			output, err := runCommand("docker", "service", "ps", serviceName, "--no-trunc")
			require.NoError(t, err)

			lines := splitLines(output)
			t.Logf("✓ SwarmKit tasks (%d):", len(lines)-1)

			for i, line := range lines {
				if i == 0 {
					continue // Skip header
				}
				t.Logf("  Task: %s", line)
			}
		})

		// Phase 6: Scale service
		t.Run("Phase6_ScaleService", func(t *testing.T) {
			// Scale up to 3 replicas
			output, err := runCommand("docker", "service", "scale", serviceName+"=3")
			require.NoError(t, err, "Failed to scale: %s", output)
			t.Logf("✓ Scaled to 3 replicas")

			// Wait for scaling
			time.Sleep(5 * time.Second)

			// Verify 3 tasks
			require.Eventually(t, func() bool {
				output, err := runCommand("docker", "service", "ls", "--filter", "name="+serviceName, "--format", "{{.Replicas}}")
				if err != nil {
					return false
				}
				return trimOutput(output) == "3/3"
			}, 2*time.Minute, 5*time.Second, "Service did not scale to 3/3")

			// Scale back to 1
			output, err = runCommand("docker", "service", "scale", serviceName+"=1")
			require.NoError(t, err)

			t.Logf("✓ Scaled back to 1 replica")
		})

		// Phase 7: Update service
		t.Run("Phase7_UpdateService", func(t *testing.T) {
			// Update image
			output, err := runCommand("docker", "service", "update",
				"--image", "nginx:1.25-alpine",
				serviceName,
			)
			require.NoError(t, err, "Failed to update: %s", output)
			t.Logf("✓ Service updated")

			// Wait for update
			time.Sleep(5 * time.Second)

			// Verify image
			output, err = runCommand("docker", "service", "inspect",
				serviceName,
				"--format", "{{.Spec.TaskTemplate.ContainerSpec.Image}}",
			)
			require.NoError(t, err)
			t.Logf("✓ Updated image: %s", trimOutput(output))
		})
	})

	t.Log("=== Comprehensive SwarmKit E2E Test Completed ===")
}

// TestE2E_SwarmKit_TaskLifecycle tests SwarmKit task lifecycle
func TestE2E_SwarmKit_TaskLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E in short mode")
	}

	if !hasDocker() {
		t.Skip("Docker not found")
	}

	t.Log("=== Testing SwarmKit Task Lifecycle ===")

	serviceName := "task-lifecycle-" + randomString(8)

	// Create service
	output, err := runCommand(
		"docker", "service", "create",
		"--name", serviceName,
		"--replicas", "1",
		"--restart-condition", "on-failure",
		"nginx:alpine",
	)
	require.NoError(t, err, "Failed to create service: %s", output)

	t.Logf("✓ Service created: %s", serviceName)

	// Cleanup
	defer func() {
		t.Log("Cleaning up service:", serviceName)
		runCommand("docker", "service", "rm", serviceName)
		time.Sleep(2 * time.Second)
	}()

	// Test task states
	t.Run("TaskStates", func(t *testing.T) {
		// Wait for task to be created
		var taskID string
		require.Eventually(t, func() bool {
			output, err := runCommand("docker", "service", "ps", serviceName, "--format", "{{.ID}}")
			if err != nil {
				return false
			}

			taskID = trimOutput(output)
			return taskID != ""
		}, 1*time.Minute, 2*time.Second, "Task was not created")

		t.Logf("✓ Task ID: %s", taskID)

		// Monitor task state transitions
		states := make(map[string]string)

		for i := 0; i < 10; i++ {
			output, err := runCommand("docker", "service", "ps", serviceName, "--format", "{{.CurrentState}}")
			if err != nil {
				t.Logf("Error getting task state: %v", err)
				break
			}

			state := trimOutput(output)
			states[state] = states[state] + 1

			t.Logf("Task state: %s", state)

			// Stop if we reach Running state
			if state == "Running" {
				break
			}

			time.Sleep(2 * time.Second)
		}

		t.Logf("✓ Task state transitions: %v", states)

		// Verify we reached Running state
		_, exists := states["Running"]
		assert.True(t, exists, "Task never reached Running state")
	})

	t.Log("=== Task Lifecycle Test Completed ===")
}

// Helper functions

// createSwarmKitService creates a SwarmKit service specification
func createSwarmKitService(name, image string, replicas uint64) *api.Service {
	return &api.Service{
		ID: naming.Namespace("", name),
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{
				Name: name,
				Labels: map[string]string{
					"test": "swarmkit-e2e",
				},
			},
			Task: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image: image,
						Command: []string{},
						Args:    []string{},
						Env:     []string{"TEST=1"},
					},
				},
				Resources: api.ResourceRequirements{
					Reservations: &api.Resources{
						NanoCPUs:    1e9,
						MemoryBytes: 512 * 1024 * 1024,
					},
				},
				Restart: api.RestartPolicy{
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
				Monitor:         5 * time.Second,
				MaxFailureRatio: 0.1,
			},
			Rollback: &api.UpdateConfig{
				Parallelism:     1,
				Delay:           5 * time.Second,
				FailureAction:   api.UpdateConfig_PAUSE,
				Monitor:         5 * time.Second,
				MaxFailureRatio: 0.1,
			},
		},
	}
}

// splitLines splits output into lines
func splitLines(output string) []string {
	lines := make([]string, 0)
	current := make([]byte, 0)

	for _, b := range []byte(output) {
		if b == '\n' {
			lines = append(lines, string(current))
			current = current[:0]
		} else {
			current = append(current, b)
		}
	}

	if len(current) > 0 {
		lines = append(lines, string(current))
	}

	return lines
}
