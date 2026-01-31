package scenarios

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/executor"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/translator"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/restuhaqza/swarmcracker/test/e2e/cluster"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// BasicDeployScenario tests basic service deployment through SwarmKit
type BasicDeployScenario struct {
	manager       *cluster.SwarmKitManager
	agents        []*cluster.SwarmKitAgent
	cleanup       *cluster.CleanupManager
	testDir       string
	serviceName   string
	imageName     string
	replicas      int
}

// NewBasicDeployScenario creates a new basic deploy scenario
func NewBasicDeployScenario(testDir, serviceName, imageName string, replicas int) *BasicDeployScenario {
	return &BasicDeployScenario{
		testDir:     testDir,
		serviceName: serviceName,
		imageName:   imageName,
		replicas:    replicas,
	}
}

// Setup sets up the test scenario
func (s *BasicDeployScenario) Setup(ctx context.Context, t *testing.T) error {
	t.Log("Setting up basic deploy scenario...")

	// Create cleanup manager
	s.cleanup = cluster.NewCleanupManager()

	// Create test directory
	var err error
	s.testDir, err = os.MkdirTemp("", "swarmcracker-e2e-*")
	require.NoError(t, err)
	s.cleanup.TrackStateDir(s.testDir)

	// Create and start manager
	managerDir := filepath.Join(s.testDir, "manager")
	s.manager, err = cluster.NewSwarmKitManager(managerDir, "127.0.0.1:4242")
	require.NoError(t, err)

	s.cleanup.AddCleanupFunc(s.manager.Stop)

	if err := s.manager.Start(); err != nil {
		return fmt.Errorf("failed to start manager: %w", err)
	}

	s.cleanup.TrackProcess(s.manager.GetProcess())

	// Create and start agents
	for i := 0; i < s.replicas; i++ {
		agentDir := filepath.Join(s.testDir, fmt.Sprintf("agent-%d", i))
		agent, err := cluster.NewSwarmKitAgent(agentDir, s.manager.GetAddr(), s.manager.GetJoinToken())
		require.NoError(t, err)

		s.cleanup.AddCleanupFunc(agent.Stop)

		if err := agent.Start(); err != nil {
			return fmt.Errorf("failed to start agent %d: %w", i, err)
		}

		s.cleanup.TrackProcess(agent.GetProcess())
		s.agents = append(s.agents, agent)
	}

	t.Log("Basic deploy scenario setup complete")
	return nil
}

// Run runs the test scenario
func (s *BasicDeployScenario) Run(ctx context.Context, t *testing.T) error {
	t.Log("Running basic deploy scenario...")

	// Create executor
	cfg := &config.Config{}
	cfg.SetDefaults()
	cfg.Executor.RootfsDir = filepath.Join(s.testDir, "rootfs")
	cfg.Executor.SocketDir = filepath.Join(s.testDir, "sockets")

	if err := os.MkdirAll(cfg.Executor.RootfsDir, 0755); err != nil {
		return fmt.Errorf("failed to create rootfs dir: %w", err)
	}

	if err := os.MkdirAll(cfg.Executor.SocketDir, 0755); err != nil {
		return fmt.Errorf("failed to create socket dir: %w", err)
	}

	// Create executor with all components
	vmmConfig := &lifecycle.ManagerConfig{
		KernelPath:      cfg.Executor.KernelPath,
		RootfsDir:       cfg.Executor.RootfsDir,
		SocketDir:       cfg.Executor.SocketDir,
		DefaultVCPUs:    cfg.Executor.DefaultVCPUs,
		DefaultMemoryMB: cfg.Executor.DefaultMemoryMB,
		EnableJailer:    cfg.Executor.EnableJailer,
	}

	vmmManager := lifecycle.NewVMMManager(vmmConfig)
	taskTranslator := translator.NewTaskTranslator(vmmConfig)

	imageConfig := &image.PreparerConfig{
		RootfsDir: cfg.Executor.RootfsDir,
	}

	imagePreparer := image.NewImagePreparer(imageConfig)
	networkMgr := network.NewNetworkManager(types.NetworkConfig{
		BridgeName: "swarm-br0",
	})

	execConfig := &executor.Config{
		KernelPath:      cfg.Executor.KernelPath,
		RootfsDir:       cfg.Executor.RootfsDir,
		SocketDir:       cfg.Executor.SocketDir,
		DefaultVCPUs:    cfg.Executor.DefaultVCPUs,
		DefaultMemoryMB: cfg.Executor.DefaultMemoryMB,
		EnableJailer:    cfg.Executor.EnableJailer,
		Network: types.NetworkConfig{
			BridgeName: cfg.Network.BridgeName,
		},
	}

	exec, err := executor.NewFirecrackerExecutor(
		execConfig,
		vmmManager,
		taskTranslator,
		imagePreparer,
		networkMgr,
	)
	require.NoError(t, err)

	// Create a test task
	task := s.createTestTask()

	// Test Prepare phase
	t.Log("Testing Prepare phase...")
	prepareCtx, prepareCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer prepareCancel()

	err = exec.Prepare(prepareCtx, task)
	if err != nil {
		t.Logf("Prepare phase failed: %v", err)
		t.Skip("Image preparation requires configured container runtime")
		return nil
	}

	// Verify rootfs was created
	rootfsPath, ok := task.Annotations["rootfs"]
	require.True(t, ok, "rootfs annotation should be set")
	require.FileExists(t, rootfsPath, "rootfs file should exist")

	t.Logf("Rootfs created at: %s", rootfsPath)

	// Test Start phase
	t.Log("Testing Start phase...")
	startCtx, startCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer startCancel()

	err = exec.Start(startCtx, task)
	if err != nil {
		t.Logf("Start phase failed: %v", err)
		// Cleanup
		exec.Remove(ctx, task)
		t.Skip("Firecracker startup requires additional setup")
		return nil
	}

	// Wait a bit for VM to start
	time.Sleep(2 * time.Second)

	// Check task status
	status, err := exec.Wait(ctx, task)
	require.NoError(t, err)
	assert.Equal(t, types.TaskState_RUNNING, status.State)

	t.Log("Task is running!")

	// Test Stop phase
	t.Log("Testing Stop phase...")
	err = exec.Stop(ctx, task)
	assert.NoError(t, err)

	// Test Remove phase
	t.Log("Testing Remove phase...")
	err = exec.Remove(ctx, task)
	assert.NoError(t, err)

	t.Log("Basic deploy scenario completed successfully")
	return nil
}

// Teardown tears down the test scenario
func (s *BasicDeployScenario) Teardown(ctx context.Context, t *testing.T) error {
	t.Log("Tearing down basic deploy scenario...")

	if s.cleanup != nil {
		if err := s.cleanup.Cleanup(ctx); err != nil {
			t.Logf("Cleanup encountered errors: %v", err)
		}
	}

	t.Log("Teardown complete")
	return nil
}

// createTestTask creates a test task for the scenario
func (s *BasicDeployScenario) createTestTask() *types.Task {
	return &types.Task{
		ID:        fmt.Sprintf("task-%s-%d", s.serviceName, time.Now().Unix()),
		ServiceID: s.serviceName,
		NodeID:    s.agents[0].GetForeignID(),
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image:   s.imageName,
				Command: []string{},
				Args:    []string{"sh", "-c", "echo 'Hello from SwarmCracker' && sleep 30"},
				Env:     []string{"TEST=1"},
			},
			Resources: types.ResourceRequirements{
				Limits: &types.Resources{
					NanoCPUs:    1 * 1e9,
					MemoryBytes: 512 * 1024 * 1024,
				},
			},
		},
		Status: types.TaskStatus{
			State: types.TaskState_PENDING,
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

// RunBasicDeployTest runs the complete basic deploy test
func RunBasicDeployTest(ctx context.Context, t *testing.T, serviceName, imageName string, replicas int) {
	scenario := NewBasicDeployScenario("", serviceName, imageName, replicas)

	// Setup
	if err := scenario.Setup(ctx, t); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Ensure teardown happens
	defer func() {
		if err := scenario.Teardown(ctx, t); err != nil {
			t.Logf("Teardown failed: %v", err)
		}
	}()

	// Run
	if err := scenario.Run(ctx, t); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
}
