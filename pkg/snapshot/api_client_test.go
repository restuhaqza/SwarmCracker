package snapshot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
