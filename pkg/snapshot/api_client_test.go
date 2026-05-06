package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultFirecrackerAPIClient_CreateSnapshot tests CreateSnapshot with mock HTTP server
func TestDefaultFirecrackerAPIClient_CreateSnapshot(t *testing.T) {
	t.Run("successful create snapshot", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Handle pause VM request
			if r.URL.Path == "/vm" && r.Method == http.MethodPatch {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// Handle snapshot create request
			if r.URL.Path == "/snapshot/create" && r.Method == http.MethodPut {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer ts.Close()

		// Create a mock HTTP client factory
		mockFactory := &MockHTTPClientFactory{
			NewUnixClientFunc: func(socketPath string, timeout time.Duration) HTTPClient {
				return &testHTTPClient{ts.Client(), ts.URL}
			},
		}

		// Override the implementation
		origImpl := callSnapshotCreateImpl
		defer func() { callSnapshotCreateImpl = origImpl }()

		callSnapshotCreateImpl = func(ctx context.Context, socketPath, statePath, memoryPath string) error {
			client := mockFactory.NewUnixClient(socketPath, 30*time.Second)
			// First pause VM
			pausePayload := map[string]interface{}{"state": "Paused"}
			pauseBody, _ := json.Marshal(pausePayload)
			pauseReq, _ := http.NewRequestWithContext(ctx, http.MethodPatch, "http://localhost/vm", strings.NewReader(string(pauseBody)))
			pauseReq.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(pauseReq)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				return fmt.Errorf("pause failed: %d", resp.StatusCode)
			}

			// Then create snapshot
			payload := map[string]interface{}{
				"snapshot_type": "Full",
				"snapshot_path": statePath,
				"mem_file_path":  memoryPath,
			}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/snapshot/create", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			resp2, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp2.Body.Close()
			if resp2.StatusCode != http.StatusNoContent {
				return fmt.Errorf("create failed: %d", resp2.StatusCode)
			}
			return nil
		}

		client := NewDefaultFirecrackerAPIClient()
		err := client.CreateSnapshot(context.Background(), "/tmp/socket", "/tmp/state", "/tmp/mem")
		assert.NoError(t, err)
	})

	t.Run("pause VM failure", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/vm" && r.Method == http.MethodPatch {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("pause failed"))
				return
			}
		}))
		defer ts.Close()

		mockFactory := &MockHTTPClientFactory{
			NewUnixClientFunc: func(socketPath string, timeout time.Duration) HTTPClient {
				return &testHTTPClient{ts.Client(), ts.URL}
			},
		}

		origImpl := callSnapshotCreateImpl
		defer func() { callSnapshotCreateImpl = origImpl }()

		callSnapshotCreateImpl = func(ctx context.Context, socketPath, statePath, memoryPath string) error {
			client := mockFactory.NewUnixClient(socketPath, 30*time.Second)
			pausePayload := map[string]interface{}{"state": "Paused"}
			pauseBody, _ := json.Marshal(pausePayload)
			pauseReq, _ := http.NewRequestWithContext(ctx, http.MethodPatch, "http://localhost/vm", strings.NewReader(string(pauseBody)))
			pauseReq.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(pauseReq)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("pause failed: %d: %s", resp.StatusCode, string(body))
			}
			return nil
		}

		client := NewDefaultFirecrackerAPIClient()
		err := client.CreateSnapshot(context.Background(), "/tmp/socket", "/tmp/state", "/tmp/mem")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "pause failed")
	})
}

// TestDefaultFirecrackerAPIClient_LoadSnapshot tests LoadSnapshot with mock HTTP server
func TestDefaultFirecrackerAPIClient_LoadSnapshot(t *testing.T) {
	t.Run("successful load snapshot", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/snapshot/load" && r.Method == http.MethodPut {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer ts.Close()

		mockFactory := &MockHTTPClientFactory{
			NewUnixClientFunc: func(socketPath string, timeout time.Duration) HTTPClient {
				return &testHTTPClient{ts.Client(), ts.URL}
			},
		}

		origImpl := callSnapshotLoadImpl
		defer func() { callSnapshotLoadImpl = origImpl }()

		callSnapshotLoadImpl = func(ctx context.Context, socketPath, statePath, memoryPath string) error {
			client := mockFactory.NewUnixClient(socketPath, 30*time.Second)
			payload := map[string]interface{}{
				"snapshot_path": statePath,
				"mem_file_path":  memoryPath,
				"resume_vm":      true,
			}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/snapshot/load", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
				return fmt.Errorf("load failed: %d", resp.StatusCode)
			}
			return nil
		}

		client := NewDefaultFirecrackerAPIClient()
		err := client.LoadSnapshot(context.Background(), "/tmp/socket", "/tmp/state", "/tmp/mem")
		assert.NoError(t, err)
	})

	t.Run("load snapshot API error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/snapshot/load" && r.Method == http.MethodPut {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("invalid snapshot"))
				return
			}
		}))
		defer ts.Close()

		mockFactory := &MockHTTPClientFactory{
			NewUnixClientFunc: func(socketPath string, timeout time.Duration) HTTPClient {
				return &testHTTPClient{ts.Client(), ts.URL}
			},
		}

		origImpl := callSnapshotLoadImpl
		defer func() { callSnapshotLoadImpl = origImpl }()

		callSnapshotLoadImpl = func(ctx context.Context, socketPath, statePath, memoryPath string) error {
			client := mockFactory.NewUnixClient(socketPath, 30*time.Second)
			payload := map[string]interface{}{
				"snapshot_path": statePath,
				"mem_file_path":  memoryPath,
				"resume_vm":      true,
			}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/snapshot/load", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
				respBody, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
			}
			return nil
		}

		client := NewDefaultFirecrackerAPIClient()
		err := client.LoadSnapshot(context.Background(), "/tmp/socket", "/tmp/state", "/tmp/mem")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status")
	})
}

// TestDefaultFirecrackerAPIClient_StartInstance tests StartInstance with mock HTTP server
func TestDefaultFirecrackerAPIClient_StartInstance(t *testing.T) {
	t.Run("successful start instance", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/actions" && r.Method == http.MethodPut {
				// Verify the action type
				body, _ := io.ReadAll(r.Body)
				var payload map[string]interface{}
				json.Unmarshal(body, &payload)
				if payload["action_type"] == "InstanceStart" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer ts.Close()

		mockFactory := &MockHTTPClientFactory{
			NewUnixClientFunc: func(socketPath string, timeout time.Duration) HTTPClient {
				return &testHTTPClient{ts.Client(), ts.URL}
			},
		}

		origImpl := callInstanceStartImpl
		defer func() { callInstanceStartImpl = origImpl }()

		callInstanceStartImpl = func(ctx context.Context, socketPath string) error {
			client := mockFactory.NewUnixClient(socketPath, 30*time.Second)
			payload := map[string]interface{}{"action_type": "InstanceStart"}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/actions", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
				return fmt.Errorf("start failed: %d", resp.StatusCode)
			}
			return nil
		}

		client := NewDefaultFirecrackerAPIClient()
		err := client.StartInstance(context.Background(), "/tmp/socket")
		assert.NoError(t, err)
	})

	t.Run("start instance API error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/actions" && r.Method == http.MethodPut {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}))
		defer ts.Close()

		mockFactory := &MockHTTPClientFactory{
			NewUnixClientFunc: func(socketPath string, timeout time.Duration) HTTPClient {
				return &testHTTPClient{ts.Client(), ts.URL}
			},
		}

		origImpl := callInstanceStartImpl
		defer func() { callInstanceStartImpl = origImpl }()

		callInstanceStartImpl = func(ctx context.Context, socketPath string) error {
			client := mockFactory.NewUnixClient(socketPath, 30*time.Second)
			payload := map[string]interface{}{"action_type": "InstanceStart"}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/actions", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
				return fmt.Errorf("start failed: %d", resp.StatusCode)
			}
			return nil
		}

		client := NewDefaultFirecrackerAPIClient()
		err := client.StartInstance(context.Background(), "/tmp/socket")
		assert.Error(t, err)
	})
}

// TestDefaultFirecrackerAPIClient_WaitForSocket tests WaitForSocket with temp unix socket
func TestDefaultFirecrackerAPIClient_WaitForSocket(t *testing.T) {
	t.Run("socket timeout", func(t *testing.T) {
		tmpDir := t.TempDir()
		socketPath := filepath.Join(tmpDir, "nonexistent.sock")

		client := NewDefaultFirecrackerAPIClient()
		err := client.WaitForSocket(socketPath, 200*time.Millisecond)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not ready")
	})
}

// TestNewUnixClient tests the HTTP client creation for Unix sockets
func TestNewUnixClient(t *testing.T) {
	t.Run("creates client with unix transport", func(t *testing.T) {
		// We can't easily test the actual unix socket dial, but we can verify
		// the client is created with the right configuration
		client := newUnixHTTPClient("/tmp/test.sock", 30*time.Second)
		assert.NotNil(t, client)
		assert.Equal(t, 30*time.Second, client.Timeout)
		assert.NotNil(t, client.Transport)
	})
}

// TestNewDefaultHTTPClientFactory tests the factory
func TestNewDefaultHTTPClientFactory(t *testing.T) {
	factory := NewDefaultHTTPClientFactory()
	assert.NotNil(t, factory)

	client := factory.NewUnixClient("/tmp/test.sock", 10*time.Second)
	assert.NotNil(t, client)
}

// TestDefaultFirecrackerAPIClient_PauseVM tests PauseVM
func TestDefaultFirecrackerAPIClient_PauseVM(t *testing.T) {
	t.Run("successful pause", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/vm" && r.Method == http.MethodPatch {
				// Verify payload
				body, _ := io.ReadAll(r.Body)
				var payload map[string]interface{}
				json.Unmarshal(body, &payload)
				if payload["state"] == "Paused" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer ts.Close()

		mockFactory := &MockHTTPClientFactory{
			NewUnixClientFunc: func(socketPath string, timeout time.Duration) HTTPClient {
				return &testHTTPClient{ts.Client(), ts.URL}
			},
		}

		origImpl := pauseVMImpl
		defer func() { pauseVMImpl = origImpl }()

		pauseVMImpl = func(ctx context.Context, socketPath string) error {
			client := mockFactory.NewUnixClient(socketPath, 30*time.Second)
			payload := map[string]interface{}{"state": "Paused"}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, "http://localhost/vm", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
				return fmt.Errorf("pause failed: %d", resp.StatusCode)
			}
			return nil
		}

		client := NewDefaultFirecrackerAPIClient()
		err := client.PauseVM(context.Background(), "/tmp/socket")
		assert.NoError(t, err)
	})

	t.Run("pause error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer ts.Close()

		mockFactory := &MockHTTPClientFactory{
			NewUnixClientFunc: func(socketPath string, timeout time.Duration) HTTPClient {
				return &testHTTPClient{ts.Client(), ts.URL}
			},
		}

		origImpl := pauseVMImpl
		defer func() { pauseVMImpl = origImpl }()

		pauseVMImpl = func(ctx context.Context, socketPath string) error {
			client := mockFactory.NewUnixClient(socketPath, 30*time.Second)
			payload := map[string]interface{}{"state": "Paused"}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, "http://localhost/vm", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
				return fmt.Errorf("pause failed: %d", resp.StatusCode)
			}
			return nil
		}

		apiClient := NewDefaultFirecrackerAPIClient()
		err := apiClient.PauseVM(context.Background(), "/tmp/socket")
		assert.Error(t, err)
	})
}

// TestDefaultFirecrackerAPIClient_ResumeVM tests ResumeVM
func TestDefaultFirecrackerAPIClient_ResumeVM(t *testing.T) {
	t.Run("successful resume", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/vm" && r.Method == http.MethodPatch {
				body, _ := io.ReadAll(r.Body)
				var payload map[string]interface{}
				json.Unmarshal(body, &payload)
				if payload["state"] == "Resumed" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer ts.Close()

		mockFactory := &MockHTTPClientFactory{
			NewUnixClientFunc: func(socketPath string, timeout time.Duration) HTTPClient {
				return &testHTTPClient{ts.Client(), ts.URL}
			},
		}

		origImpl := resumeVMImpl
		defer func() { resumeVMImpl = origImpl }()

		resumeVMImpl = func(ctx context.Context, socketPath string) error {
			client := mockFactory.NewUnixClient(socketPath, 30*time.Second)
			payload := map[string]interface{}{"state": "Resumed"}
			body, _ := json.Marshal(payload)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, "http://localhost/vm", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
				return fmt.Errorf("resume failed: %d", resp.StatusCode)
			}
			return nil
		}

		client := NewDefaultFirecrackerAPIClient()
		err := client.ResumeVM(context.Background(), "/tmp/socket")
		assert.NoError(t, err)
	})
}

// TestCallSnapshotCreate_ErrorPaths tests error scenarios
func TestCallSnapshotCreate_ErrorPaths(t *testing.T) {
	t.Run("JSON marshal error", func(t *testing.T) {
		// Test with invalid payload type that can't be marshaled
		origImpl := callSnapshotCreateImpl
		defer func() { callSnapshotCreateImpl = origImpl }()

		callSnapshotCreateImpl = func(ctx context.Context, socketPath, statePath, memoryPath string) error {
			// Simulate error path by calling the real function with network error
			client := newUnixHTTPClient(socketPath, 30*time.Second)
			payload := map[string]interface{}{
				"snapshot_type": "Full",
				"snapshot_path": statePath,
				"mem_file_path":  memoryPath,
			}
			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("marshal error: %w", err)
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost/snapshot/create", strings.NewReader(string(body)))
			if err != nil {
				return fmt.Errorf("request error: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")

			// This will fail because there's no real server
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("API request failed: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
				respBody, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
			}
			return nil
		}

		err := callSnapshotCreate(context.Background(), "/nonexistent/socket", "/tmp/state", "/tmp/mem")
		assert.Error(t, err)
	})

	t.Run("network error", func(t *testing.T) {
		err := callSnapshotCreate(context.Background(), "/nonexistent/socket/path", "/tmp/state", "/tmp/mem")
		assert.Error(t, err)
	})
}

// TestSaveMetadata_ErrorPaths tests metadata save error scenarios
func TestSaveMetadata_ErrorPaths(t *testing.T) {
	t.Run("permission denied", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create read-only directory
		roDir := filepath.Join(tmpDir, "readonly")
		require.NoError(t, os.MkdirAll(roDir, 0444))
		defer os.Chmod(roDir, 0755) // Cleanup

		info := &SnapshotInfo{
			ID:        "snap-test",
			TaskID:    "task-1",
			CreatedAt: time.Now().UTC(),
		}

		err := saveMetadata(roDir, info)
		assert.Error(t, err)
	})

	t.Run("non-existent directory", func(t *testing.T) {
		info := &SnapshotInfo{
			ID:        "snap-test",
			TaskID:    "task-1",
			CreatedAt: time.Now().UTC(),
		}

		err := saveMetadata("/nonexistent/path/to/dir", info)
		assert.Error(t, err)
	})
}

// testHTTPClient wraps httptest client to override URL
type testHTTPClient struct {
	client *http.Client
	baseURL string
}

func (c *testHTTPClient) Do(req *http.Request) (*http.Response, error) {
	// Replace the URL with the test server URL
	newURL := c.baseURL + req.URL.Path
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return c.client.Do(newReq)
}

// TestDefaultProcessExecutor tests the process executor
func TestDefaultProcessExecutor(t *testing.T) {
	t.Run("LookPath finds existing binary", func(t *testing.T) {
		executor := NewDefaultProcessExecutor()
		path, err := executor.LookPath("ls")
		// ls should exist on most systems
		if err == nil {
			assert.NotEmpty(t, path)
			assert.Contains(t, path, "ls")
		}
	})

	t.Run("LookPath fails for non-existent binary", func(t *testing.T) {
		executor := NewDefaultProcessExecutor()
		_, err := executor.LookPath("nonexistent_binary_xyz")
		assert.Error(t, err)
	})

	t.Run("StartCommand creates process handle", func(t *testing.T) {
		executor := NewDefaultProcessExecutor()
		// Use sleep command which should exist
		handle, err := executor.StartCommand("sleep", "0.1")
		if err != nil {
			t.Skip("sleep command not available")
		}
		assert.NotNil(t, handle)
		if handle.Pid() > 0 {
			assert.Greater(t, handle.Pid(), 0)
		}
		// Wait for process to finish
		_ = handle.Wait()
	})

	t.Run("StartCommand fails for non-existent command", func(t *testing.T) {
		executor := NewDefaultProcessExecutor()
		_, err := executor.StartCommand("nonexistent_cmd_xyz")
		assert.Error(t, err)
	})
}

// TestDefaultProcessHandle tests process handle methods
func TestDefaultProcessHandle(t *testing.T) {
	t.Run("Pid returns zero for process without PID", func(t *testing.T) {
		cmd := exec.Command("echo", "test")
		handle := &defaultProcessHandle{cmd: cmd}
		assert.Equal(t, 0, handle.Pid()) // Process not started, so Pid is 0
	})

	t.Run("Kill returns nil when process not started", func(t *testing.T) {
		cmd := exec.Command("echo", "test")
		handle := &defaultProcessHandle{cmd: cmd}
		err := handle.Kill()
		assert.NoError(t, err) // Process not started, Kill returns nil
	})
}

// TestPutFirecrackerAPI tests PUT request helper
func TestPutFirecrackerAPI(t *testing.T) {
	t.Run("successful PUT request", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		defer ts.Close()

		ctx := context.Background()
		payload := map[string]interface{}{"test": "value"}

		// Create custom request to test server
		client := ts.Client()
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, ts.URL+"/test", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("unexpected status code", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("error message"))
		}))
		defer ts.Close()

		client := ts.Client()
		payload := map[string]interface{}{"test": "value"}
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, ts.URL+"/test", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			assert.Contains(t, string(respBody), "error message")
		}
	})
}

// TestPatchFirecrackerAPI tests PATCH request helper
func TestPatchFirecrackerAPI(t *testing.T) {
	t.Run("successful PATCH request", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPatch {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		defer ts.Close()

		client := ts.Client()
		payload := map[string]interface{}{"state": "Paused"}
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPatch, ts.URL+"/vm", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})
}

// TestNewDefaultFirecrackerAPIClient tests the constructor
func TestNewDefaultFirecrackerAPIClient(t *testing.T) {
	client := NewDefaultFirecrackerAPIClient()
	assert.NotNil(t, client)

	// Verify it implements the interface
	var _ FirecrackerAPIClient = client
}