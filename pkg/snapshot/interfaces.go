// Package snapshot provides interfaces for Firecracker API operations.
// This file defines interfaces to enable mocking in tests without requiring a real Firecracker process.

package snapshot

import (
	"context"
	"net/http"
	"os/exec"
	"time"
)

// FirecrackerAPIClient defines the interface for Firecracker API operations.
type FirecrackerAPIClient interface {
	// PauseVM pauses a running VM
	PauseVM(ctx context.Context, socketPath string) error
	// ResumeVM resumes a paused VM
	ResumeVM(ctx context.Context, socketPath string) error
	// CreateSnapshot creates a snapshot of a running VM
	CreateSnapshot(ctx context.Context, socketPath, statePath, memoryPath string) error
	// LoadSnapshot loads a snapshot into a VM
	LoadSnapshot(ctx context.Context, socketPath, statePath, memoryPath string) error
	// StartInstance starts/resumes a VM instance
	StartInstance(ctx context.Context, socketPath string) error
	// WaitForSocket waits for the Firecracker API socket to be ready
	WaitForSocket(socketPath string, timeout time.Duration) error
}

// DefaultFirecrackerAPIClient is the default implementation using real API calls.
type DefaultFirecrackerAPIClient struct{}

// NewDefaultFirecrackerAPIClient creates a new default API client.
func NewDefaultFirecrackerAPIClient() FirecrackerAPIClient {
	return &DefaultFirecrackerAPIClient{}
}

func (c *DefaultFirecrackerAPIClient) PauseVM(ctx context.Context, socketPath string) error {
	return pauseVMImpl(ctx, socketPath)
}

func (c *DefaultFirecrackerAPIClient) ResumeVM(ctx context.Context, socketPath string) error {
	return resumeVMImpl(ctx, socketPath)
}

func (c *DefaultFirecrackerAPIClient) CreateSnapshot(ctx context.Context, socketPath, statePath, memoryPath string) error {
	return callSnapshotCreateImpl(ctx, socketPath, statePath, memoryPath)
}

func (c *DefaultFirecrackerAPIClient) LoadSnapshot(ctx context.Context, socketPath, statePath, memoryPath string) error {
	return callSnapshotLoadImpl(ctx, socketPath, statePath, memoryPath)
}

func (c *DefaultFirecrackerAPIClient) StartInstance(ctx context.Context, socketPath string) error {
	return callInstanceStartImpl(ctx, socketPath)
}

func (c *DefaultFirecrackerAPIClient) WaitForSocket(socketPath string, timeout time.Duration) error {
	return waitForSocketImpl(socketPath, timeout)
}

// ProcessExecutor defines the interface for starting external processes.
type ProcessExecutor interface {
	LookPath(file string) (string, error)
	StartCommand(name string, arg ...string) (ProcessHandle, error)
}

// ProcessHandle represents a running process.
type ProcessHandle interface {
	Pid() int
	Kill() error
	Wait() error
}

// DefaultProcessExecutor is the default implementation using os/exec.
type DefaultProcessExecutor struct{}

// NewDefaultProcessExecutor creates a new default process executor.
func NewDefaultProcessExecutor() ProcessExecutor {
	return &DefaultProcessExecutor{}
}

func (e *DefaultProcessExecutor) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (e *DefaultProcessExecutor) StartCommand(name string, arg ...string) (ProcessHandle, error) {
	cmd := exec.Command(name, arg...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &defaultProcessHandle{cmd: cmd}, nil
}

type defaultProcessHandle struct {
	cmd *exec.Cmd
}

func (h *defaultProcessHandle) Pid() int {
	if h.cmd.Process != nil {
		return h.cmd.Process.Pid
	}
	return 0
}

func (h *defaultProcessHandle) Kill() error {
	if h.cmd.Process != nil {
		return h.cmd.Process.Kill()
	}
	return nil
}

func (h *defaultProcessHandle) Wait() error {
	return h.cmd.Wait()
}

// HTTPClientFactory creates HTTP clients for Unix socket communication.
type HTTPClientFactory interface {
	NewUnixClient(socketPath string, timeout time.Duration) HTTPClient
}

// HTTPClient wraps http.Client for interface compatibility.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// DefaultHTTPClientFactory is the default implementation.
type DefaultHTTPClientFactory struct{}

// NewDefaultHTTPClientFactory creates a new default factory.
func NewDefaultHTTPClientFactory() HTTPClientFactory {
	return &DefaultHTTPClientFactory{}
}

func (f *DefaultHTTPClientFactory) NewUnixClient(socketPath string, timeout time.Duration) HTTPClient {
	return newUnixHTTPClient(socketPath, timeout)
}

// Implementation functions that can be swapped for testing
var (
	pauseVMImpl            = pauseVM
	resumeVMImpl           = resumeVM
	callSnapshotCreateImpl = callSnapshotCreate
	callSnapshotLoadImpl   = callSnapshotLoad
	callInstanceStartImpl  = callInstanceStart
	waitForSocketImpl      = waitForSocket
)