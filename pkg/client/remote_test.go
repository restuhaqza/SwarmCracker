package client

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const testAccept = "application/vnd.swarmcracker.v1+json"

// newTestRemote starts a TLS server and returns a client configured to trust
// it. The endpoint and CA default to the test server's.
func newTestRemote(t *testing.T, cfg Config, handler http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	caFile := filepath.Join(t.TempDir(), "ca.crt")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NotNil(t, certPEM)
	require.NoError(t, os.WriteFile(caFile, certPEM, 0o600))

	if cfg.Endpoint == "" {
		cfg.Endpoint = srv.URL
	}
	if cfg.CAFile == "" {
		cfg.CAFile = caFile
	}

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return c
}

func writeTestDocument(w http.ResponseWriter, status int, doc any) {
	w.Header().Set("Content-Type", testAccept)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(doc)
}

func codeOf(t *testing.T, err error) *output.Error {
	t.Helper()
	var typed *output.Error
	require.Truef(t, errors.As(err, &typed), "want *output.Error, got %T: %v", err, err)
	return typed
}

func TestRemoteSendsAuthAndAcceptHeaders(t *testing.T) {
	headers := make(chan http.Header, 1)
	c := newTestRemote(t, Config{Token: "s3cret"}, func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		writeTestDocument(w, http.StatusOK, v1.NewVersionInfo([]string{"v1"}, "test", "1", "cluster-1", nil))
	})

	id, err := c.ClusterID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "cluster-1", id)

	got := <-headers
	assert.Equal(t, "Bearer s3cret", got.Get("Authorization"))
	assert.Equal(t, testAccept, got.Get("Accept"))
}

func TestRemoteListServicesFollowsNext(t *testing.T) {
	next := "page-2"
	cursors := make(chan string, 4)
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		cursors <- cursor
		if r.URL.Path != "/v1/services" {
			t.Errorf("path = %q, want /v1/services", r.URL.Path)
		}
		switch cursor {
		case "":
			doc := v1.NewServiceList([]v1.Service{{ID: "svc-1", Name: "one"}, {ID: "svc-2", Name: "two"}})
			doc.Next = &next
			writeTestDocument(w, http.StatusOK, doc)
		case "page-2":
			writeTestDocument(w, http.StatusOK, v1.NewServiceList([]v1.Service{{ID: "svc-3", Name: "three"}}))
		default:
			t.Errorf("unexpected cursor %q", cursor)
			writeTestDocument(w, http.StatusOK, v1.NewServiceList(nil))
		}
	})

	services, err := c.ListServices(context.Background())
	require.NoError(t, err)
	require.Len(t, services, 3)
	assert.Equal(t, []string{"svc-1", "svc-2", "svc-3"}, []string{services[0].ID, services[1].ID, services[2].ID})

	// The first page carries no cursor; the second resumes from the first
	// page's next value.
	assert.Equal(t, "", <-cursors)
	assert.Equal(t, "page-2", <-cursors)
}

func TestRemoteListServicesEmptyIsEmptySlice(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		writeTestDocument(w, http.StatusOK, v1.NewServiceList(nil))
	})

	services, err := c.ListServices(context.Background())
	require.NoError(t, err)
	require.NotNil(t, services)
	assert.Empty(t, services)
}

func TestRemoteListServiceTasksPath(t *testing.T) {
	paths := make(chan string, 1)
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		writeTestDocument(w, http.StatusOK, v1.NewTaskList([]v1.Task{{ID: "task-1", ServiceID: "svc-1"}}))
	})

	tasks, err := c.ListServiceTasks(context.Background(), "svc-1")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "task-1", tasks[0].ID)
	assert.Equal(t, "/v1/services/svc-1/tasks", <-paths)
}

func TestRemoteGetServiceDocument(t *testing.T) {
	paths := make(chan string, 1)
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		writeTestDocument(w, http.StatusOK, v1.NewServiceDocument(v1.Service{
			ID:       "svc-1",
			Name:     "web",
			Revision: "3",
			Status:   v1.ServiceStatus{DesiredTasks: 2, RunningTasks: 2, Converged: true},
		}))
	})

	svc, err := c.GetService(context.Background(), "svc-1")
	require.NoError(t, err)
	assert.Equal(t, "/v1/services/svc-1", <-paths)
	assert.Equal(t, "svc-1", svc.ID)
	assert.Equal(t, "web", svc.Name)
	assert.Equal(t, "3", svc.Revision)
	assert.Equal(t, 2, svc.Status.RunningTasks)
}

func TestRemoteEscapesResourceIDAsOneSegment(t *testing.T) {
	paths := make(chan string, 1)
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.EscapedPath()
		writeTestDocument(w, http.StatusNotFound, v1.NewErrorDocument(v1.Error{Code: "not_found", Message: "nope"}))
	})

	_, err := c.GetService(context.Background(), "web/prod")
	require.Error(t, err)
	assert.Equal(t, "/v1/services/web%2Fprod", <-paths)
}

func TestRemoteGetServiceNotFoundIsTyped(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		writeTestDocument(w, http.StatusNotFound, v1.NewErrorDocument(v1.Error{
			Code:      string(output.CodeNotFound),
			Message:   `service "web" not found`,
			RequestID: "req-9",
			Details:   map[string]any{"ref": "web"},
		}))
	})

	_, err := c.GetService(context.Background(), "web")
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeNotFound, typed.Code)
	assert.Equal(t, `service "web" not found`, typed.Message)
	assert.Equal(t, "req-9", typed.RequestID)
	assert.Equal(t, "web", typed.Details["ref"])
	assert.False(t, typed.Retryable)
}

func TestRemoteClusterID(t *testing.T) {
	paths := make(chan string, 1)
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		writeTestDocument(w, http.StatusOK, v1.NewVersionInfo([]string{"v1"}, "v0.11.0", "1", "cluster-abc", nil))
	})

	id, err := c.ClusterID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "cluster-abc", id)
	assert.Equal(t, "/v1/version", <-paths)
}

func TestRemoteGetTaskUsesServerDocument(t *testing.T) {
	paths := make(chan string, 2)
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		writeTestDocument(w, http.StatusOK, v1.NewTaskDocument(v1.Task{
			ID:           "task-1",
			ServiceID:    "svc-1",
			NodeID:       "node-1",
			NodeName:     "worker-7",
			DesiredState: "running",
		}))
	})

	task, err := c.GetTask(context.Background(), "task-1")
	require.NoError(t, err)
	assert.Equal(t, "/v1/tasks/task-1", <-paths)
	assert.Equal(t, "worker-7", task.NodeName)
	// The server already resolved the node name; the client must not look it up
	// again over the local gRPC path.
	assert.Len(t, paths, 0)
}

func TestRemoteRejectsPlainHTTP(t *testing.T) {
	_, err := New(Config{Endpoint: "http://manager:4244", CAFile: "/tmp/ca.crt"})
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeUsage, typed.Code)
	assert.Contains(t, typed.Message, "TLS")
}

func TestRemoteRejectsHTTPSWithoutCA(t *testing.T) {
	_, err := New(Config{Endpoint: "https://manager:4244"})
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeUsage, typed.Code)
	assert.Contains(t, typed.Message, "CAFile")
}

func TestRemoteRejectsEndpointWithoutHost(t *testing.T) {
	_, err := New(Config{Endpoint: "https://", CAFile: "/tmp/ca.crt"})
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeUsage, typed.Code)
}

func TestRemoteRejectsUnreadableCA(t *testing.T) {
	_, err := New(Config{Endpoint: "https://manager:4244", CAFile: filepath.Join(t.TempDir(), "missing.crt")})
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeUsage, typed.Code)
	assert.Contains(t, typed.Message, "CA file")
}

func TestRemoteCloseIsSafe(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		writeTestDocument(w, http.StatusOK, v1.NewVersionInfo(nil, "test", "1", "cluster-1", nil))
	})

	require.NotNil(t, c.remote)
	require.NoError(t, c.Close())
	require.NoError(t, c.Close())
}

func TestRemoteTokenFile(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("  file-token\n"), 0o600))

	t.Run("read and trimmed", func(t *testing.T) {
		headers := make(chan http.Header, 1)
		c := newTestRemote(t, Config{TokenFile: tokenFile}, func(w http.ResponseWriter, r *http.Request) {
			headers <- r.Header.Clone()
			writeTestDocument(w, http.StatusOK, v1.NewVersionInfo(nil, "test", "1", "cluster-1", nil))
		})

		_, err := c.ClusterID(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "Bearer file-token", (<-headers).Get("Authorization"))
	})

	t.Run("explicit token wins", func(t *testing.T) {
		headers := make(chan http.Header, 1)
		c := newTestRemote(t, Config{Token: "explicit", TokenFile: tokenFile}, func(w http.ResponseWriter, r *http.Request) {
			headers <- r.Header.Clone()
			writeTestDocument(w, http.StatusOK, v1.NewVersionInfo(nil, "test", "1", "cluster-1", nil))
		})

		_, err := c.ClusterID(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "Bearer explicit", (<-headers).Get("Authorization"))
	})
}

func TestRemoteMapsStatusWithoutErrorDocument(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream is down\n"))
	})

	_, err := c.ListNodes(context.Background())
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeUnavailable, typed.Code)
	assert.True(t, typed.Retryable)
}

func TestRemoteDecodeFailureIsInternal(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", testAccept)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{not json"))
	})

	_, err := c.GetNode(context.Background(), "node-1")
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeInternal, typed.Code)
}

func TestRemoteTransportFailureIsUnreachable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestDocument(w, http.StatusOK, v1.NewVersionInfo(nil, "test", "1", "cluster-1", nil))
	}))

	caFile := filepath.Join(t.TempDir(), "ca.crt")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NotNil(t, certPEM)
	require.NoError(t, os.WriteFile(caFile, certPEM, 0o600))

	c, err := New(Config{Endpoint: srv.URL, CAFile: caFile})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })

	srv.Close()

	_, err = c.ClusterID(context.Background())
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeUnreachable, typed.Code)
	assert.True(t, typed.Retryable)
}
