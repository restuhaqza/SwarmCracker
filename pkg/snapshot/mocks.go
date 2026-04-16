// Package snapshot provides mock implementations for testing.

package snapshot

import (
	"context"
	"net/http"
	"time"
)

// MockFirecrackerAPIClient is a mock implementation for testing.
type MockFirecrackerAPIClient struct {
	PauseVMFunc        func(ctx context.Context, socketPath string) error
	ResumeVMFunc       func(ctx context.Context, socketPath string) error
	CreateSnapshotFunc func(ctx context.Context, socketPath, statePath, memoryPath string) error
	LoadSnapshotFunc   func(ctx context.Context, socketPath, statePath, memoryPath string) error
	StartInstanceFunc  func(ctx context.Context, socketPath string) error
	WaitForSocketFunc  func(socketPath string, timeout time.Duration) error
}

func (m *MockFirecrackerAPIClient) PauseVM(ctx context.Context, socketPath string) error {
	if m.PauseVMFunc != nil {
		return m.PauseVMFunc(ctx, socketPath)
	}
	return nil
}

func (m *MockFirecrackerAPIClient) ResumeVM(ctx context.Context, socketPath string) error {
	if m.ResumeVMFunc != nil {
		return m.ResumeVMFunc(ctx, socketPath)
	}
	return nil
}

func (m *MockFirecrackerAPIClient) CreateSnapshot(ctx context.Context, socketPath, statePath, memoryPath string) error {
	if m.CreateSnapshotFunc != nil {
		return m.CreateSnapshotFunc(ctx, socketPath, statePath, memoryPath)
	}
	return nil
}

func (m *MockFirecrackerAPIClient) LoadSnapshot(ctx context.Context, socketPath, statePath, memoryPath string) error {
	if m.LoadSnapshotFunc != nil {
		return m.LoadSnapshotFunc(ctx, socketPath, statePath, memoryPath)
	}
	return nil
}

func (m *MockFirecrackerAPIClient) StartInstance(ctx context.Context, socketPath string) error {
	if m.StartInstanceFunc != nil {
		return m.StartInstanceFunc(ctx, socketPath)
	}
	return nil
}

func (m *MockFirecrackerAPIClient) WaitForSocket(socketPath string, timeout time.Duration) error {
	if m.WaitForSocketFunc != nil {
		return m.WaitForSocketFunc(socketPath, timeout)
	}
	return nil
}

// MockProcessExecutor is a mock implementation for testing.
type MockProcessExecutor struct {
	LookPathFunc    func(file string) (string, error)
	StartCommandFunc func(name string, arg ...string) (ProcessHandle, error)
}

func (m *MockProcessExecutor) LookPath(file string) (string, error) {
	if m.LookPathFunc != nil {
		return m.LookPathFunc(file)
	}
	return "/usr/bin/" + file, nil
}

func (m *MockProcessExecutor) StartCommand(name string, arg ...string) (ProcessHandle, error) {
	if m.StartCommandFunc != nil {
		return m.StartCommandFunc(name, arg...)
	}
	return &MockProcessHandle{pid: 12345}, nil
}

// MockProcessHandle is a mock process handle.
type MockProcessHandle struct {
	pid  int
	KillFunc func() error
	WaitFunc func() error
}

func (h *MockProcessHandle) Pid() int {
	return h.pid
}

func (h *MockProcessHandle) Kill() error {
	if h.KillFunc != nil {
		return h.KillFunc()
	}
	return nil
}

func (h *MockProcessHandle) Wait() error {
	if h.WaitFunc != nil {
		return h.WaitFunc()
	}
	return nil
}

// MockHTTPClient is a mock HTTP client.
type MockHTTPClient struct {
	DoFunc func(req *http.Request) (*http.Response, error)
}

func (c *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if c.DoFunc != nil {
		return c.DoFunc(req)
	}
	return &http.Response{StatusCode: http.StatusNoContent}, nil
}

// MockHTTPClientFactory is a mock HTTP client factory.
type MockHTTPClientFactory struct {
	NewUnixClientFunc func(socketPath string, timeout time.Duration) HTTPClient
}

func (f *MockHTTPClientFactory) NewUnixClient(socketPath string, timeout time.Duration) HTTPClient {
	if f.NewUnixClientFunc != nil {
		return f.NewUnixClientFunc(socketPath, timeout)
	}
	return &MockHTTPClient{}
}