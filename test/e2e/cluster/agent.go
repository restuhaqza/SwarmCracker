package cluster

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// SwarmKitAgent manages a SwarmKit agent instance for testing
type SwarmKitAgent struct {
	ctx           context.Context
	cancel        context.CancelFunc
	binaryPath    string
	stateDir      string
	managerAddr   string
	managerToken  string
	foreignID     string
	cmd           *exec.Cmd
	logger        zerolog.Logger
}

// NewSwarmKitAgent creates a new SwarmKit agent
func NewSwarmKitAgent(stateDir, managerAddr, managerToken string) (*SwarmKitAgent, error) {
	if stateDir == "" {
		var err error
		stateDir, err = os.MkdirTemp("", "swarmkit-agent-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp dir: %w", err)
		}
	}

	if managerAddr == "" {
		managerAddr = "127.0.0.1:4242"
	}

	absStateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve state dir: %w", err)
	}

	if err := os.MkdirAll(absStateDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state dir: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &SwarmKitAgent{
		ctx:          ctx,
		cancel:       cancel,
		stateDir:     absStateDir,
		managerAddr:  managerAddr,
		managerToken: managerToken,
		foreignID:    randomString(8),
		binaryPath:   "swarmd",
		logger: log.With().
			Str("component", "swarmkit-agent").
			Str("manager", managerAddr).
			Logger(),
	}, nil
}

// Start starts the SwarmKit agent
func (a *SwarmKitAgent) Start() error {
	a.logger.Info().Msg("Starting SwarmKit agent")

	socketPath := filepath.Join(a.stateDir, "swarmd.sock")

	args := []string{
		"--join-addr", a.managerAddr,
		"--join-token", a.managerToken,
		"--state-dir", a.stateDir,
		"--foreign-id", a.foreignID,
		"--listen-remote-api", "0.0.0.0:0", // Random port
		"--listen-control-api", socketPath,
		"--debug",
	}

	// Check if swarmd binary exists
	if _, err := exec.LookPath(a.binaryPath); err != nil {
		return fmt.Errorf("swarmd binary not found: %w", err)
	}

	a.cmd = exec.CommandContext(a.ctx, a.binaryPath, args...)
	a.cmd.Dir = a.stateDir
	a.cmd.Stdout = &logWriter{logger: a.logger, level: zerolog.InfoLevel}
	a.cmd.Stderr = &logWriter{logger: a.logger, level: zerolog.ErrorLevel}

	if err := a.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start agent: %w", err)
	}

	// Wait for agent to connect to manager
	a.logger.Info().Msg("Waiting for agent to connect...")
	if err := a.waitForReady(socketPath); err != nil {
		a.Stop()
		return fmt.Errorf("agent failed to become ready: %w", err)
	}

	a.logger.Info().Msg("SwarmKit agent started successfully")

	return nil
}

// waitForReady waits for the agent socket to be available
func (a *SwarmKitAgent) waitForReady(socketPath string) error {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(socketPath); err == nil {
				return nil
			}
		}
	}
}

// Stop stops the SwarmKit agent
func (a *SwarmKitAgent) Stop() error {
	a.logger.Info().Msg("Stopping SwarmKit agent")

	if a.cancel != nil {
		a.cancel()
	}

	if a.cmd != nil && a.cmd.Process != nil {
		if err := a.cmd.Process.Kill(); err != nil {
			a.logger.Error().Err(err).Msg("Failed to kill agent process")
		}
	}

	// Clean up state directory
	if a.stateDir != "" {
		if err := os.RemoveAll(a.stateDir); err != nil {
			a.logger.Error().Err(err).Msg("Failed to clean up state directory")
		}
	}

	return nil
}

// GetStateDir returns the state directory path
func (a *SwarmKitAgent) GetStateDir() string {
	return a.stateDir
}

// GetForeignID returns the agent's foreign ID
func (a *SwarmKitAgent) GetForeignID() string {
	return a.foreignID
}

// IsRunning checks if the agent is still running
func (a *SwarmKitAgent) IsRunning() bool {
	if a.cmd == nil || a.cmd.Process == nil {
		return false
	}

	// Check if process is still alive
	if err := a.cmd.Process.Signal(os.Signal(nil)); err != nil {
		return false
	}

	return true
}

// GetProcess returns the underlying process for cleanup tracking
func (a *SwarmKitAgent) GetProcess() *os.Process {
	if a.cmd != nil && a.cmd.Process != nil {
		return a.cmd.Process
	}
	return nil
}

// SetExecutor configures the agent to use a custom executor (SwarmCracker)
func (a *SwarmKitAgent) SetExecutor(execPath string) {
	a.logger.Info().Str("executor", execPath).Msg("Configuring custom executor")
	// This will be used when starting the agent with custom executor flag
	// For now, we'll store it for future use
}
