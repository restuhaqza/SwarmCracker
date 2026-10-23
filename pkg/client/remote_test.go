package client

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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
	var requests atomic.Int32
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
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
	// page's next value, and termination stops at exactly two requests.
	assert.Equal(t, "", <-cursors)
	assert.Equal(t, "page-2", <-cursors)
	assert.EqualValues(t, 2, requests.Load())
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

	// Close only drops idle connections; the client remains usable.
	id, err := c.ClusterID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "cluster-1", id)
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

// unrelatedCA returns a self-signed CA certificate that did not sign the test
// server's certificate, so a client trusting it must reject that server.
func unrelatedCA(t *testing.T) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "unrelated test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestRemoteRejectsCollapsedResourceIDs(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{name: "empty", id: ""},
		{name: "dot", id: "."},
		{name: "dotdot", id: ".."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				writeTestDocument(w, http.StatusOK, v1.NewServiceDocument(v1.Service{ID: "svc-1"}))
			})

			_, err := c.GetService(context.Background(), tt.id)
			require.Error(t, err)
			// A collapsed path would have returned a list or version document
			// with a nil error; reject it before any request leaves.
			assert.Equal(t, output.CodeUsage, codeOf(t, err).Code)
			assert.Zero(t, requests.Load())
		})
	}
}

func TestRemoteUsesExpectedPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
		doc  any
		call func(*Client) error
	}{
		{
			name: "GetNetwork",
			path: "/v1/networks/net-1",
			doc:  v1.NewNetworkDocument(v1.Network{ID: "net-1"}),
			call: func(c *Client) error { _, err := c.GetNetwork(context.Background(), "net-1"); return err },
		},
		{
			name: "GetConfig",
			path: "/v1/configs/cfg-1",
			doc:  v1.NewConfigDocument(v1.Config{ID: "cfg-1"}),
			call: func(c *Client) error { _, err := c.GetConfig(context.Background(), "cfg-1"); return err },
		},
		{
			name: "GetSecret",
			path: "/v1/secrets/sec-1",
			doc:  v1.NewSecretDocument(v1.Secret{ID: "sec-1"}),
			call: func(c *Client) error { _, err := c.GetSecret(context.Background(), "sec-1"); return err },
		},
		{
			name: "ListNetworks",
			path: "/v1/networks",
			doc:  v1.NewNetworkList(nil),
			call: func(c *Client) error { _, err := c.ListNetworks(context.Background()); return err },
		},
		{
			name: "ListConfigs",
			path: "/v1/configs",
			doc:  v1.NewConfigList(nil),
			call: func(c *Client) error { _, err := c.ListConfigs(context.Background()); return err },
		},
		{
			name: "ListSecrets",
			path: "/v1/secrets",
			doc:  v1.NewSecretList(nil),
			call: func(c *Client) error { _, err := c.ListSecrets(context.Background()); return err },
		},
		{
			name: "ListTasks",
			path: "/v1/tasks",
			doc:  v1.NewTaskList(nil),
			call: func(c *Client) error { _, err := c.ListTasks(context.Background()); return err },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := make(chan string, 1)
			c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				writeTestDocument(w, http.StatusOK, tt.doc)
			})

			require.NoError(t, tt.call(c))
			assert.Equal(t, tt.path, <-paths)
		})
	}
}

func TestRemoteCopiesRetryableFromErrorDocument(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		writeTestDocument(w, http.StatusNotFound, v1.NewErrorDocument(v1.Error{
			Code:      string(output.CodeNotFound),
			Message:   "try again",
			Retryable: true,
		}))
	})

	_, err := c.GetNode(context.Background(), "node-1")
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeNotFound, typed.Code)
	// not_found is not retryable by default; the server document wins.
	assert.True(t, typed.Retryable)
}

func TestRemoteClusterIDEmptyIsNotFound(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		writeTestDocument(w, http.StatusOK, v1.NewVersionInfo([]string{"v1"}, "test", "1", "", nil))
	})

	id, err := c.ClusterID(context.Background())
	require.Error(t, err)
	assert.Empty(t, id)
	assert.Equal(t, output.CodeNotFound, codeOf(t, err).Code)
}

func TestRemoteOmitsAuthorizationWithoutToken(t *testing.T) {
	headers := make(chan http.Header, 1)
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		writeTestDocument(w, http.StatusOK, v1.NewVersionInfo(nil, "test", "1", "cluster-1", nil))
	})

	_, err := c.ClusterID(context.Background())
	require.NoError(t, err)

	got := <-headers
	_, present := got["Authorization"]
	assert.False(t, present, "Authorization must be absent, not empty")
	assert.Equal(t, testAccept, got.Get("Accept"))
}

func TestRemoteUntrustedServerCertificateIsUnreachable(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeTestDocument(w, http.StatusOK, v1.NewVersionInfo(nil, "test", "1", "cluster-1", nil))
	}))
	t.Cleanup(srv.Close)

	caFile := filepath.Join(t.TempDir(), "other-ca.crt")
	require.NoError(t, os.WriteFile(caFile, unrelatedCA(t), 0o600))

	c, err := New(Config{Endpoint: srv.URL, CAFile: caFile})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })

	_, err = c.ClusterID(context.Background())
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeUnreachable, typed.Code)
	// Verification failed in the handshake: the request never reached a handler.
	assert.Zero(t, requests.Load())
}

func TestRemoteCanceledContextIsTimeout(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		writeTestDocument(w, http.StatusOK, v1.NewNodeList(nil))
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.ListNodes(ctx)
	require.Error(t, err)
	assert.Equal(t, output.CodeTimeout, codeOf(t, err).Code)
}

func TestRemoteDeadlineExceededIsTimeout(t *testing.T) {
	c := newTestRemote(t, Config{}, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.ListNodes(ctx)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, output.CodeTimeout, codeOf(t, err).Code)
}

func TestRemoteRepeatedCursorFailsFast(t *testing.T) {
	var requests atomic.Int32
	next := "same"
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		doc := v1.NewNodeList(nil)
		doc.Next = &next
		writeTestDocument(w, http.StatusOK, doc)
	})

	_, err := c.ListNodes(context.Background())
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeInternal, typed.Code)
	assert.Contains(t, typed.Message, "repeated cursor")
	// First page yields the cursor, second repeats it: fail instead of 1000 requests.
	assert.EqualValues(t, 2, requests.Load())
}

func TestRemoteOversizedResponseIsInternal(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", testAccept)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxResponseBytes+1))
	})

	_, err := c.ListNodes(context.Background())
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeInternal, typed.Code)
	assert.Contains(t, typed.Message, "exceeds")
}

func TestRemoteClientTimeoutIsTimeout(t *testing.T) {
	c := newTestRemote(t, Config{Timeout: 50 * time.Millisecond}, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	_, err := c.ListNodes(context.Background())
	require.Error(t, err)
	assert.Equal(t, output.CodeTimeout, codeOf(t, err).Code)
}
