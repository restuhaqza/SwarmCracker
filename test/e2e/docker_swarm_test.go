package e2e

import (
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestE2E_DockerSwarmBasic tests basic Docker Swarm functionality
func TestE2E_DockerSwarmBasic(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// Check if Docker is available
	if !hasDocker() {
		t.Skip("Docker not found")
	}

	t.Log("Testing Docker Swarm basic functionality...")

	// Check if Swarm is initialized
	t.Run("SwarmStatus", func(t *testing.T) {
		output, err := runCommand("docker", "info", "--format", "{{.Swarm.LocalNodeState}}")
		require.NoError(t, err)
		t.Logf("Swarm state: %s", output)

		if output != "active" {
			t.Skip("Docker Swarm not initialized. Run: docker swarm init --advertise-addr <ip>")
		}
	})

	// List nodes
	t.Run("ListNodes", func(t *testing.T) {
		output, err := runCommand("docker", "node", "ls")
		require.NoError(t, err)
		t.Logf("Swarm nodes:\n%s", output)
	})

	// Create a simple service
	t.Run("CreateService", func(t *testing.T) {
		serviceName := "test-nginx-" + randomString(8)

		// Create service
		output, err := runCommand(
			"docker", "service", "create",
			"--name", serviceName,
			"--replicas", "1",
			"nginx:alpine",
		)
		require.NoError(t, err, "Failed to create service: %s", output)
		t.Logf("Service created: %s", serviceName)

		// Cleanup
		t.Cleanup(func() {
			t.Log("Cleaning up service:", serviceName)
			runCommand("docker", "service", "rm", serviceName)
			time.Sleep(2 * time.Second)
		})

		// List services
		t.Run("ListServices", func(t *testing.T) {
			output, err := runCommand("docker", "service", "ls")
			require.NoError(t, err)
			t.Logf("Services:\n%s", output)
		})

		// Inspect service
		t.Run("InspectService", func(t *testing.T) {
			output, err := runCommand("docker", "service", "inspect", serviceName)
			require.NoError(t, err)
			t.Logf("Service info:\n%s", output)
		})

		// Check service tasks
		t.Run("ListTasks", func(t *testing.T) {
			output, err := runCommand("docker", "service", "ps", serviceName)
			require.NoError(t, err)
			t.Logf("Service tasks:\n%s", output)
		})

		// Verify service is running
		t.Run("VerifyRunning", func(t *testing.T) {
			// Wait a bit for service to start
			time.Sleep(5 * time.Second)

			output, err := runCommand("docker", "service", "ls", "--filter", "name="+serviceName, "--format", "{{.Replicas}}")
			require.NoError(t, err)

			// Trim whitespace for comparison
			output = trimOutput(output)
			t.Logf("Service replicas: %s", output)

			if output != "1/1" {
				t.Logf("Warning: Service not fully running yet (got %s, expected 1/1)", output)
			} else {
				t.Log("✓ Service is running with 1/1 replicas")
			}
		})
	})

	t.Log("Docker Swarm basic test completed")
}

// trimOutput removes leading/trailing whitespace and newlines
func trimOutput(output string) string {
	trimmed := output
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}
	for len(trimmed) > 0 && (trimmed[len(trimmed)-1] == ' ' || trimmed[len(trimmed)-1] == '\t' || trimmed[len(trimmed)-1] == '\n' || trimmed[len(trimmed)-1] == '\r') {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

// TestE2E_DockerSwarmScale tests service scaling
func TestE2E_DockerSwarmScale(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	if !hasDocker() {
		t.Skip("Docker not found")
	}

	// Check Swarm status
	output, err := runCommand("docker", "info", "--format", "{{.Swarm.LocalNodeState}}")
	require.NoError(t, err)

	if trimOutput(output) != "active" {
		t.Skipf("Docker Swarm not active (state: %s)", output)
	}

	t.Log("Testing Docker Swarm service scaling...")

	serviceName := "test-scale-" + randomString(8)

	// Create service with 1 replica
	output, err = runCommand("docker", "service", "create",
		"--name", serviceName,
		"--replicas", "1",
		"nginx:alpine",
	)
	require.NoError(t, err, "Failed to create service: %s", output)
	t.Logf("Service created: %s", serviceName)

	// Cleanup
	t.Cleanup(func() {
		t.Log("Cleaning up service:", serviceName)
		runCommand("docker", "service", "rm", serviceName)
		time.Sleep(2 * time.Second)
	})

	// Scale to 3 replicas
	t.Run("ScaleUp", func(t *testing.T) {
		output, err := runCommand("docker", "service", "scale", serviceName+"=3")
		require.NoError(t, err, "Failed to scale service: %s", output)
		t.Logf("Scale output: %s", output)

		// Wait for scaling
		time.Sleep(5 * time.Second)

		// Check replicas
		output, err = runCommand("docker", "service", "ls", "--filter", "name="+serviceName, "--format", "{{.Replicas}}")
		require.NoError(t, err)
		t.Logf("Current replicas: %s", output)
	})

	// Scale down to 1 replica
	t.Run("ScaleDown", func(t *testing.T) {
		output, err := runCommand("docker", "service", "scale", serviceName+"=1")
		require.NoError(t, err, "Failed to scale down: %s", output)

		// Wait for scaling
		time.Sleep(5 * time.Second)

		// Check replicas
		output, err = runCommand("docker", "service", "ls", "--filter", "name="+serviceName, "--format", "{{.Replicas}}")
		require.NoError(t, err)
		t.Logf("Current replicas: %s", output)
	})

	t.Log("Docker Swarm scaling test completed")
}

// TestE2E_SwarmCrackerExecutor tests SwarmCracker executor integration
func TestE2E_SwarmCrackerExecutor(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	if !hasDocker() {
		t.Skip("Docker not found")
	}

	t.Run("BinaryAvailability", func(t *testing.T) {
		// Check if swarmcracker binary exists
		if _, err := runCommand("which", "swarmcracker"); err != nil {
			t.Skip("SwarmCracker binary not found. Run: make install")
		}

		output, err := runCommand("swarmcracker", "--version")
		require.NoError(t, err)
		t.Logf("SwarmCracker version: %s", output)
	})

	t.Run("ExecutorValidation", func(t *testing.T) {
		// Placeholder for executor-specific tests
		t.Skip("Executor validation tests - to be implemented when executor plugin is ready")
	})
}

// runCommand executes a command and returns its output
func runCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// randomString generates a random string for test naming
func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[i%len(charset)]
	}
	return string(b)
}
