package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateSnapshot_UnixSocketSuccess tests CreateSnapshot with real Unix socket server
func TestCreateSnapshot_UnixSocketSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	mgr, err := NewManager(SnapshotConfig{SnapshotDir: tmpDir})
	require.NoError(t, err)

	socketPath := filepath.Join(tmpDir, "fc.sock")

	pauseCalled := false
	createCalled := false

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/vm", func(w http.ResponseWriter, r *http.Request) {
		pauseCalled = true
		assert.Equal(t, http.MethodPatch, r.Method)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/snapshot/create", func(w http.ResponseWriter, r *http.Request) {
		createCalled = true
		assert.Equal(t, http.MethodPut, r.Method)

		var payload map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&payload)
		require.NoError(t, err)

		statePath := payload["snapshot_path"].(string)
		memoryPath := payload["mem_file_path"].(string)

		require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0755))
		require.NoError(t, os.MkdirAll(filepath.Dir(memoryPath), 0755))
		require.NoError(t, os.WriteFile(statePath, []byte("mock state"), 0644))
		require.NoError(t, os.WriteFile(memoryPath, []byte("mock memory"), 0644))

		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond)

	info, err := mgr.CreateSnapshot(
		context.Background(),
		"task-1",
		socketPath,
		CreateOptions{
			ServiceID:  "nginx",
			NodeID:     "worker-1",
			VCPUCount:  2,
			MemoryMB:   1024,
			RootfsPath: "/path/to/rootfs.ext4",
		},
	)

	require.NoError(t, err)
	assert.NotNil(t, info)
	assert.Equal(t, "task-1", info.TaskID)
	assert.True(t, pauseCalled)
	assert.True(t, createCalled)
	assert.FileExists(t, info.StatePath)
	assert.FileExists(t, info.MemoryPath)
}

// TestCallSnapshotCreate_Success tests callSnapshotCreate with real Unix socket
func TestCallSnapshotCreate_Success(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fc.sock")
	statePath := filepath.Join(tmpDir, "vm.state")
	memoryPath := filepath.Join(tmpDir, "vm.mem")

	pauseCalled := false
	createCalled := false

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/vm", func(w http.ResponseWriter, r *http.Request) {
		pauseCalled = true
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/snapshot/create", func(w http.ResponseWriter, r *http.Request) {
		createCalled = true
		require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0755))
		require.NoError(t, os.MkdirAll(filepath.Dir(memoryPath), 0755))
		require.NoError(t, os.WriteFile(statePath, []byte("state"), 0644))
		require.NoError(t, os.WriteFile(memoryPath, []byte("memory"), 0644))
		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond)

	err = callSnapshotCreate(context.Background(), socketPath, statePath, memoryPath)
	require.NoError(t, err)
	assert.True(t, pauseCalled)
	assert.True(t, createCalled)
}

// TestCallSnapshotCreate_PauseError tests callSnapshotCreate pause error
func TestCallSnapshotCreate_PauseError(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fc.sock")
	statePath := filepath.Join(tmpDir, "vm.state")
	memoryPath := filepath.Join(tmpDir, "vm.mem")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/vm", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("pause failed"))
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond)

	err = callSnapshotCreate(context.Background(), socketPath, statePath, memoryPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to pause VM")
}

// TestCallSnapshotCreate_SnapshotError tests callSnapshotCreate snapshot error
func TestCallSnapshotCreate_SnapshotError(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fc.sock")
	statePath := filepath.Join(tmpDir, "vm.state")
	memoryPath := filepath.Join(tmpDir, "vm.mem")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/vm", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/snapshot/create", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid params"))
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond)

	err = callSnapshotCreate(context.Background(), socketPath, statePath, memoryPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status")
}

// TestPutFirecrackerAPI_UnixSocketSuccess tests putFirecrackerAPI with Unix socket
func TestPutFirecrackerAPI_UnixSocketSuccess(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
	}{
		{"204 No Content", http.StatusNoContent},
		{"200 OK", http.StatusOK},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			socketPath := filepath.Join(tmpDir, "fc.sock")

			listener, err := net.Listen("unix", socketPath)
			require.NoError(t, err)
			defer listener.Close()

			requestReceived := false
			mux := http.NewServeMux()
			mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
				requestReceived = true
				assert.Equal(t, http.MethodPut, r.Method)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				w.WriteHeader(tc.statusCode)
			})

			server := &http.Server{Handler: mux}
			go func() {
				_ = server.Serve(listener)
			}()
			defer server.Shutdown(context.Background())

			require.Eventually(t, func() bool {
				_, err := os.Stat(socketPath)
				return err == nil
			}, 2*time.Second, 100*time.Millisecond)

			payload := map[string]interface{}{"key": "value"}
			err = putFirecrackerAPI(context.Background(), socketPath, "/test", payload)
			require.NoError(t, err)
			assert.True(t, requestReceived)
		})
	}
}

// TestPutFirecrackerAPI_UnixSocketError tests putFirecrackerAPI error responses
func TestPutFirecrackerAPI_UnixSocketError(t *testing.T) {
	errorCases := []struct {
		name       string
		statusCode int
		respBody   string
	}{
		{"Bad Request", http.StatusBadRequest, "invalid request"},
		{"Unauthorized", http.StatusUnauthorized, "unauthorized"},
		{"Not Found", http.StatusNotFound, "not found"},
		{"Internal Error", http.StatusInternalServerError, "server error"},
	}

	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			socketPath := filepath.Join(tmpDir, "fc.sock")

			listener, err := net.Listen("unix", socketPath)
			require.NoError(t, err)
			defer listener.Close()

			mux := http.NewServeMux()
			mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				if tc.respBody != "" {
					w.Write([]byte(tc.respBody))
				}
			})

			server := &http.Server{Handler: mux}
			go func() {
				_ = server.Serve(listener)
			}()
			defer server.Shutdown(context.Background())

			require.Eventually(t, func() bool {
				_, err := os.Stat(socketPath)
				return err == nil
			}, 2*time.Second, 100*time.Millisecond)

			payload := map[string]interface{}{"key": "value"}
			err = putFirecrackerAPI(context.Background(), socketPath, "/test", payload)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "unexpected status")
			assert.Contains(t, err.Error(), fmt.Sprintf("%d", tc.statusCode))
		})
	}
}

// TestPutFirecrackerAPI_UnixSocketConnectionError tests connection failure
func TestPutFirecrackerAPI_UnixSocketConnectionError(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "nonexistent.sock")

	payload := map[string]interface{}{"key": "value"}
	err := putFirecrackerAPI(context.Background(), socketPath, "/test", payload)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "API request failed")
}

// TestPutFirecrackerAPI_UnixSocketMarshalError tests JSON marshal error
func TestPutFirecrackerAPI_UnixSocketMarshalError(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fc.sock")

	payload := make(chan int)

	err := putFirecrackerAPI(context.Background(), socketPath, "/test", payload)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal request")
}

// TestPatchFirecrackerAPI_UnixSocketSuccess tests patchFirecrackerAPI with Unix socket
func TestPatchFirecrackerAPI_UnixSocketSuccess(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
	}{
		{"204 No Content", http.StatusNoContent},
		{"200 OK", http.StatusOK},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			socketPath := filepath.Join(tmpDir, "fc.sock")

			listener, err := net.Listen("unix", socketPath)
			require.NoError(t, err)
			defer listener.Close()

			requestReceived := false
			mux := http.NewServeMux()
			mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
				requestReceived = true
				assert.Equal(t, http.MethodPatch, r.Method)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				w.WriteHeader(tc.statusCode)
			})

			server := &http.Server{Handler: mux}
			go func() {
				_ = server.Serve(listener)
			}()
			defer server.Shutdown(context.Background())

			require.Eventually(t, func() bool {
				_, err := os.Stat(socketPath)
				return err == nil
			}, 2*time.Second, 100*time.Millisecond)

			payload := map[string]interface{}{"key": "value"}
			err = patchFirecrackerAPI(context.Background(), socketPath, "/test", payload)
			require.NoError(t, err)
			assert.True(t, requestReceived)
		})
	}
}

// TestPatchFirecrackerAPI_UnixSocketError tests patchFirecrackerAPI error responses
func TestPatchFirecrackerAPI_UnixSocketError(t *testing.T) {
	errorCases := []struct {
		name       string
		statusCode int
		respBody   string
	}{
		{"Bad Request", http.StatusBadRequest, "invalid request"},
		{"Unauthorized", http.StatusUnauthorized, "unauthorized"},
		{"Not Found", http.StatusNotFound, "not found"},
		{"Internal Error", http.StatusInternalServerError, "server error"},
	}

	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			socketPath := filepath.Join(tmpDir, "fc.sock")

			listener, err := net.Listen("unix", socketPath)
			require.NoError(t, err)
			defer listener.Close()

			mux := http.NewServeMux()
			mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				if tc.respBody != "" {
					w.Write([]byte(tc.respBody))
				}
			})

			server := &http.Server{Handler: mux}
			go func() {
				_ = server.Serve(listener)
			}()
			defer server.Shutdown(context.Background())

			require.Eventually(t, func() bool {
				_, err := os.Stat(socketPath)
				return err == nil
			}, 2*time.Second, 100*time.Millisecond)

			payload := map[string]interface{}{"key": "value"}
			err = patchFirecrackerAPI(context.Background(), socketPath, "/test", payload)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "unexpected status")
		})
	}
}

// TestPatchFirecrackerAPI_UnixSocketConnectionError tests connection failure
func TestPatchFirecrackerAPI_UnixSocketConnectionError(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "nonexistent.sock")

	payload := map[string]interface{}{"key": "value"}
	err := patchFirecrackerAPI(context.Background(), socketPath, "/test", payload)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "API request failed")
}

// TestPatchFirecrackerAPI_UnixSocketMarshalError tests JSON marshal error
func TestPatchFirecrackerAPI_UnixSocketMarshalError(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fc.sock")

	payload := make(chan int)

	err := patchFirecrackerAPI(context.Background(), socketPath, "/test", payload)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal request")
}

// TestPutFirecrackerAPI_ContextCancelled tests context cancellation
func TestPutFirecrackerAPI_ContextCancelled(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fc.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	payload := map[string]interface{}{"key": "value"}
	err = putFirecrackerAPI(ctx, socketPath, "/test", payload)
	assert.Error(t, err)
}

// TestPatchFirecrackerAPI_ContextCancelled tests context cancellation
func TestPatchFirecrackerAPI_ContextCancelled(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fc.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	payload := map[string]interface{}{"key": "value"}
	err = patchFirecrackerAPI(ctx, socketPath, "/test", payload)
	assert.Error(t, err)
}

// TestCallSnapshotLoad_HTTPError tests callSnapshotLoad with HTTP error
func TestCallSnapshotLoad_HTTPError(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fc.sock")
	statePath := filepath.Join(tmpDir, "vm.state")
	memoryPath := filepath.Join(tmpDir, "vm.mem")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/snapshot/load", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("load failed"))
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond)

	err = callSnapshotLoad(context.Background(), socketPath, statePath, memoryPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status")
}

// TestCallInstanceStart_HTTPError tests callInstanceStart with HTTP error
func TestCallInstanceStart_HTTPError(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "fc.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/actions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte("already running"))
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	require.Eventually(t, func() bool {
		_, err := os.Stat(socketPath)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond)

	err = callInstanceStart(context.Background(), socketPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected status")
}
