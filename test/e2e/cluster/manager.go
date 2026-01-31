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

// SwarmKitManager manages a SwarmKit manager instance for testing
type SwarmKitManager struct {
	ctx       context.Context
	cancel    context.CancelFunc
	binaryPath string
	stateDir   string
	addr      string
	joinToken string
	cmd       *exec.Cmd
	logger    zerolog.Logger
}

// NewSwarmKitManager creates a new SwarmKit manager
func NewSwarmKitManager(stateDir, addr string) (*SwarmKitManager, error) {
	if stateDir == "" {
		stateDir = os.TempDir()
	}

	if addr == "" {
		addr = "127.0.0.1:4242"
	}

	absStateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve state dir: %w", err)
	}

	if err := os.MkdirAll(absStateDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state dir: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &SwarmKitManager{
		ctx:        ctx,
		cancel:     cancel,
		stateDir:   absStateDir,
		addr:       addr,
		binaryPath: "swarmd",
		logger: log.With().
			Str("component", "swarmkit-manager").
			Str("addr", addr).
			Logger(),
	}, nil
}

// Start starts the SwarmKit manager
func (m *SwarmKitManager) Start() error {
	m.logger.Info().Msg("Starting SwarmKit manager")

	socketPath := filepath.Join(m.stateDir, "swarmd.sock")
	controlSocket := filepath.Join(m.stateDir, "control.sock")

	args := []string{
		"--listen-remote-api", m.addr,
		"--listen-control-api", socketPath,
		"--state-dir", m.stateDir,
		"--join-interval", "1s",
		"--election-tick", "5",
		"--heartbeat-tick", "1",
		"--debug",
	}

	// Check if swarmd binary exists
	if _, err := exec.LookPath(m.binaryPath); err != nil {
		return fmt.Errorf("swarmd binary not found: %w. Install from: https://github.com/moby/swarmkit", err)
	}

	m.cmd = exec.CommandContext(m.ctx, m.binaryPath, args...)
	m.cmd.Dir = m.stateDir
	m.cmd.Stdout = &logWriter{logger: m.logger, level: zerolog.InfoLevel}
	m.cmd.Stderr = &logWriter{logger: m.logger, level: zerolog.ErrorLevel}

	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start manager: %w", err)
	}

	// Wait for manager to be ready
	m.logger.Info().Msg("Waiting for manager to be ready...")
	if err := m.waitForReady(controlSocket); err != nil {
		m.Stop()
		return fmt.Errorf("manager failed to become ready: %w", err)
	}

	m.logger.Info().Msg("SwarmKit manager started successfully")

	// Generate join token for workers
	m.joinToken = fmt.Sprintf("SWMTKN-1-%s-%s", randomString(16), randomString(32))

	return nil
}

// GetProcess returns the underlying process for cleanup tracking
func (m *SwarmKitManager) GetProcess() *os.Process {
	if m.cmd != nil && m.cmd.Process != nil {
		return m.cmd.Process
	}
	return nil
}

// waitForReady waits for the manager socket to be available
func (m *SwarmKitManager) waitForReady(socketPath string) error {
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
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

// Stop stops the SwarmKit manager
func (m *SwarmKitManager) Stop() error {
	m.logger.Info().Msg("Stopping SwarmKit manager")

	if m.cancel != nil {
		m.cancel()
	}

	if m.cmd != nil && m.cmd.Process != nil {
		if err := m.cmd.Process.Kill(); err != nil {
			m.logger.Error().Err(err).Msg("Failed to kill manager process")
		}
	}

	// Clean up state directory
	if m.stateDir != "" {
		if err := os.RemoveAll(m.stateDir); err != nil {
			m.logger.Error().Err(err).Msg("Failed to clean up state directory")
		}
	}

	return nil
}

// GetAddr returns the manager API address
func (m *SwarmKitManager) GetAddr() string {
	return m.addr
}

// GetJoinToken returns the worker join token
func (m *SwarmKitManager) GetJoinToken() string {
	return m.joinToken
}

// GetStateDir returns the state directory path
func (m *SwarmKitManager) GetStateDir() string {
	return m.stateDir
}

// IsRunning checks if the manager is still running
func (m *SwarmKitManager) IsRunning() bool {
	if m.cmd == nil || m.cmd.Process == nil {
		return false
	}

	// Check if process is still alive
	if err := m.cmd.Process.Signal(os.Signal(nil)); err != nil {
		return false
	}

	return true
}

// logWriter writes command output to the logger
type logWriter struct {
	logger zerolog.Logger
	level  zerolog.Level
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	w.logger.WithLevel(w.level).Msg(string(p))
	return len(p), nil
}

// randomString generates a random string of specified length
func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[i%len(charset)]
	}
	return string(b)
}
