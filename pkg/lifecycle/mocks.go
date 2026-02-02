package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// ProcessExecutor defines the interface for process operations
type ProcessExecutor interface {
	FindProcess(pid int) (Process, error)
	LookPath(file string) (string, error)
	Command(name string, args ...string) Cmd
	CommandContext(ctx context.Context, name string, args ...string) Cmd
	StartProcess(cmd Cmd) (Process, error)
}

// Process represents an OS process
type Process interface {
	Pid() int
	Kill() error
	Signal(signal syscall.Signal) error
	Wait() (*os.ProcessState, error)
	Release() error
}

// Cmd represents a command being prepared or run
type Cmd interface {
	Start() error
	Wait() error
	Run() error
	Output() ([]byte, error)
	CombinedOutput() ([]byte, error)
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)
	Process() Process
}

// HTTPClient defines the interface for HTTP operations
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
	Get(url string) (*http.Response, error)
}

// RealProcessExecutor implements ProcessExecutor using os/exec
type RealProcessExecutor struct{}

func (r *RealProcessExecutor) FindProcess(pid int) (Process, error) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil, err
	}
	return &realProcess{proc}, nil
}

// realProcess wraps os.Process
type realProcess struct {
	*os.Process
}

func (r *realProcess) Pid() int {
	return r.Process.Pid
}

func (r *realProcess) Signal(sig syscall.Signal) error {
	return r.Process.Signal(sig)
}

func (r *RealProcessExecutor) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (r *RealProcessExecutor) Command(name string, args ...string) Cmd {
	return &realCmd{cmd: exec.Command(name, args...)}
}

func (r *RealProcessExecutor) CommandContext(ctx context.Context, name string, args ...string) Cmd {
	return &realCmd{cmd: exec.CommandContext(ctx, name, args...)}
}

func (r *RealProcessExecutor) StartProcess(cmd Cmd) (Process, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process(), nil
}

// realCmd wraps exec.Cmd
type realCmd struct {
	cmd *exec.Cmd
}

func (r *realCmd) Start() error {
	return r.cmd.Start()
}

func (r *realCmd) Wait() error {
	return r.cmd.Wait()
}

func (r *realCmd) Run() error {
	return r.cmd.Run()
}

func (r *realCmd) Output() ([]byte, error) {
	return r.cmd.Output()
}

func (r *realCmd) CombinedOutput() ([]byte, error) {
	return r.cmd.CombinedOutput()
}

func (r *realCmd) SetStdin(rd io.Reader) {
	r.cmd.Stdin = rd
}

func (r *realCmd) SetStdout(wr io.Writer) {
	r.cmd.Stdout = wr
}

func (r *realCmd) SetStderr(wr io.Writer) {
	r.cmd.Stderr = wr
}

func (r *realCmd) Process() Process {
	if r.cmd.Process == nil {
		return nil
	}
	return &realProcess{r.cmd.Process}
}

// RealHTTPClient implements HTTPClient using net/http
type RealHTTPClient struct {
	timeout time.Duration
}

func NewRealHTTPClient(timeout time.Duration) HTTPClient {
	return &RealHTTPClient{timeout: timeout}
}

func (r *RealHTTPClient) Do(req *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: r.timeout}
	return client.Do(req)
}

func (r *RealHTTPClient) Get(url string) (*http.Response, error) {
	client := &http.Client{Timeout: r.timeout}
	return client.Get(url)
}

// MockProcessExecutor is a mock implementation for testing
type MockProcessExecutor struct {
	Processes map[int]Process
	Commands  map[string]struct {
		Output []byte
		Err    error
	}
	Binaries map[string]string
	Calls    []string
}

func NewMockProcessExecutor() *MockProcessExecutor {
	return &MockProcessExecutor{
		Processes: make(map[int]Process),
		Commands: make(map[string]struct {
			Output []byte
			Err    error
		}),
		Binaries: make(map[string]string),
		Calls:    make([]string, 0),
	}
}

func (m *MockProcessExecutor) FindProcess(pid int) (Process, error) {
	if proc, exists := m.Processes[pid]; exists {
		return proc, nil
	}
	return nil, fmt.Errorf("process not found: %d", pid)
}

func (m *MockProcessExecutor) LookPath(file string) (string, error) {
	m.Calls = append(m.Calls, "LookPath:"+file)
	if path, exists := m.Binaries[file]; exists {
		return path, nil
	}
	return "", fmt.Errorf("binary not found: %s", file)
}

func (m *MockProcessExecutor) Command(name string, args ...string) Cmd {
	m.Calls = append(m.Calls, "Command:"+name)
	result := m.Commands[name]
	return &mockCmd{
		output:   result.Output,
		err:      result.Err,
		name:     name,
		args:     args,
		executor: m,
	}
}

func (m *MockProcessExecutor) CommandContext(ctx context.Context, name string, args ...string) Cmd {
	return m.Command(name, args...)
}

func (m *MockProcessExecutor) StartProcess(cmd Cmd) (Process, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process(), nil
}

// mockCmd is a mock command implementation
type mockCmd struct {
	output   []byte
	err      error
	name     string
	args     []string
	executor *MockProcessExecutor
	process  Process
	started  bool
}

func (m *mockCmd) Start() error {
	m.started = true
	return m.err
}

func (m *mockCmd) Wait() error {
	return m.err
}

func (m *mockCmd) Run() error {
	return m.err
}

func (m *mockCmd) Output() ([]byte, error) {
	return m.output, m.err
}

func (m *mockCmd) CombinedOutput() ([]byte, error) {
	return m.output, m.err
}

func (m *mockCmd) SetStdin(rd io.Reader)  {}
func (m *mockCmd) SetStdout(wr io.Writer) {}
func (m *mockCmd) SetStderr(wr io.Writer) {}

func (m *mockCmd) Process() Process {
	if m.process != nil {
		return m.process
	}
	return &mockProcess{pid: 12345}
}

// mockProcess is a mock process implementation
type mockProcess struct {
	pid     int
	killed  bool
	signals []syscall.Signal
}

func (m *mockProcess) Pid() int {
	return m.pid
}

func (m *mockProcess) Kill() error {
	m.killed = true
	return nil
}

func (m *mockProcess) Signal(sig syscall.Signal) error {
	m.signals = append(m.signals, sig)
	if sig == syscall.Signal(0) {
		return fmt.Errorf("process not found")
	}
	return nil
}

func (m *mockProcess) Wait() (*os.ProcessState, error) {
	return nil, nil
}

func (m *mockProcess) Release() error {
	return nil
}

// MockHTTPClient is a mock HTTP client for testing
type MockHTTPClient struct {
	Responses map[string]MockHTTPResponse
	Calls     []HTTPCall
	Errors    map[string]error
}

type MockHTTPResponse struct {
	StatusCode int
	Body       []byte
	Err        error
}

type HTTPCall struct {
	Method string
	URL    string
}

func NewMockHTTPClient() *MockHTTPClient {
	return &MockHTTPClient{
		Responses: make(map[string]MockHTTPResponse),
		Calls:     make([]HTTPCall, 0),
		Errors:    make(map[string]error),
	}
}

func (m *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	m.Calls = append(m.Calls, HTTPCall{
		Method: req.Method,
		URL:    req.URL.String(),
	})

	key := req.Method + ":" + req.URL.String()
	if resp, exists := m.Responses[key]; exists {
		if resp.Err != nil {
			return nil, resp.Err
		}
		return &http.Response{
			StatusCode: resp.StatusCode,
			Body:       io.NopCloser(bytes.NewReader(resp.Body)),
		}, nil
	}

	if err, exists := m.Errors[key]; exists {
		return nil, err
	}

	return nil, fmt.Errorf("unexpected request: %s", key)
}

func (m *MockHTTPClient) Get(url string) (*http.Response, error) {
	m.Calls = append(m.Calls, HTTPCall{
		Method: "GET",
		URL:    url,
	})

	if resp, exists := m.Responses["GET:"+url]; exists {
		if resp.Err != nil {
			return nil, resp.Err
		}
		return &http.Response{
			StatusCode: resp.StatusCode,
			Body:       io.NopCloser(bytes.NewReader(resp.Body)),
		}, nil
	}

	return nil, fmt.Errorf("unexpected GET request: %s", url)
}

// Helper to create mock HTTP responses
func (m *MockHTTPClient) SetResponse(method, url string, statusCode int, body []byte) {
	m.Responses[method+":"+url] = MockHTTPResponse{
		StatusCode: statusCode,
		Body:       body,
	}
}

func (m *MockHTTPClient) SetError(method, url string, err error) {
	m.Errors[method+":"+url] = err
}

// VMMManagerInternal wraps VMMManager with testable interfaces
type VMMManagerInternal struct {
	*VMMManager
	processExecutor ProcessExecutor
	httpClient      HTTPClient
}

// NewVMMManagerWithExecutors creates a VMMManager with custom executors
func NewVMMManagerWithExecutors(config *ManagerConfig, procExec ProcessExecutor, httpClient HTTPClient) *VMMManagerInternal {
	if config == nil {
		config = &ManagerConfig{}
	}

	os.MkdirAll(config.SocketDir, 0755)

	return &VMMManagerInternal{
		VMMManager: &VMMManager{
			config:    config,
			vms:       make(map[string]*VMInstance),
			socketDir: config.SocketDir,
		},
		processExecutor: procExec,
		httpClient:      httpClient,
	}
}

// Start starts a VM using the mocked executors
func (vm *VMMManagerInternal) Start(ctx context.Context, task *types.Task, config interface{}) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}

	vm.mu.Lock()
	defer vm.mu.Unlock()

	log.Info().
		Str("task_id", task.ID).
		Msg("Starting VM")

	// Check if VM already exists
	if _, exists := vm.vms[task.ID]; exists {
		return fmt.Errorf("VM already exists for task %s", task.ID)
	}

	socketPath := filepath.Join(vm.socketDir, task.ID+".sock")

	// Create config JSON string
	configStr, ok := config.(string)
	if !ok {
		// If config is not already a string, try to marshal it
		configBytes, err := json.Marshal(config)
		if err != nil {
			return fmt.Errorf("failed to marshal config: %w", err)
		}
		configStr = string(configBytes)
	}

	// Validate config is not empty
	if strings.TrimSpace(configStr) == "" {
		return fmt.Errorf("invalid config: empty configuration")
	}

	// Find Firecracker binary
	fcBinary, err := vm.processExecutor.LookPath("firecracker")
	if err != nil {
		return fmt.Errorf("firecracker binary not found in PATH: %w", err)
	}

	log.Debug().Str("binary", fcBinary).Msg("Using Firecracker binary")

	// Start Firecracker process
	cmd := vm.processExecutor.Command(fcBinary,
		"--api-sock", socketPath,
		"--config-file", "/dev/stdin",
	)

	cmd.SetStdin(strings.NewReader(configStr))
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)

	process, err := vm.processExecutor.StartProcess(cmd)
	if err != nil {
		return fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Wait for API server to be ready
	if err := vm.waitForAPIServerWithClient(socketPath, 10*time.Second); err != nil {
		process.Kill()
		return fmt.Errorf("firecracker API server not ready: %w", err)
	}

	// Start the VM instance
	if err := vm.startInstanceWithClient(ctx, socketPath); err != nil {
		process.Kill()
		return fmt.Errorf("failed to start instance: %w", err)
	}

	// Store VM instance
	initSystem := "none"
	gracePeriod := 10
	if initSys, ok := task.Annotations["init_system"]; ok {
		initSystem = initSys
	}

	vmInstance := &VMInstance{
		ID:             task.ID,
		PID:            process.Pid(),
		State:          VMStateRunning,
		SocketPath:     socketPath,
		InitSystem:     initSystem,
		GracePeriodSec: gracePeriod,
		CreatedAt:      time.Now(),
	}

	vm.vms[task.ID] = vmInstance

	log.Info().
		Str("task_id", task.ID).
		Int("pid", process.Pid()).
		Msg("VM started")

	return nil
}

// waitForAPIServerWithClient waits for API server using custom HTTP client
func (vm *VMMManagerInternal) waitForAPIServerWithClient(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			resp, err := vm.httpClient.Get("http://unix" + socketPath + "/")
			if err == nil {
				resp.Body.Close()
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("API server not ready within timeout")
}

// startInstanceWithClient starts a VM instance using custom HTTP client
func (vm *VMMManagerInternal) startInstanceWithClient(ctx context.Context, socketPath string) error {
	client := vm.httpClient
	actions := ActionsType{ActionType: "InstanceStart"}

	body, _ := json.Marshal(actions)
	req, _ := http.NewRequestWithContext(ctx, "PUT",
		"http://unix"+socketPath+"/actions",
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to start instance: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

// sendCtrlAltDelWithClient sends Ctrl+Alt+Del using custom HTTP client
func (vm *VMMManagerInternal) sendCtrlAltDelWithClient(ctx context.Context, socketPath string) error {
	client := vm.httpClient
	actions := ActionsType{ActionType: "SendCtrlAltDel"}

	body, _ := json.Marshal(actions)
	req, _ := http.NewRequestWithContext(ctx, "PUT",
		"http://unix"+socketPath+"/actions",
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

// findProcess wraps process finding
func (vm *VMMManagerInternal) findProcess(pid int) (Process, error) {
	return vm.processExecutor.FindProcess(pid)
}

// Default implementations
var (
	defaultProcessExecutor ProcessExecutor = &RealProcessExecutor{}
	defaultHTTPClient      HTTPClient      = NewRealHTTPClient(5 * time.Second)
)
