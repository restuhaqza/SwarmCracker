// Package api holds the phase-1 acceptance suite for the public HTTP API.
//
// This is not a unit test of one package: it wires the real apihttp server
// (route table, bearer auth, pagination, ETag, error envelopes), a real token
// store, real TLS and the real pkg/client remote transport together, and then
// asserts the read contract the way an external consumer sees it. Nothing here
// needs root, a cluster or a listener other than httptest's loopback TLS server.
package api

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/apihttp"
	"github.com/restuhaqza/swarmcracker/pkg/client"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// serverVersion and grpcVersion are what the harness advertises; the real
	// server echoes them on /v1/version.
	serverVersion = "0.11.0"
	grpcVersion   = "v1"
	// testClusterID is the fake client's cluster identity. The server config
	// leaves ClusterID empty on purpose, so /v1/version proves it can fall back
	// to the client and the remote ClusterID read exercises the same path.
	testClusterID = "cluster-phase1"
)

// TestPhase1 is the phase-1 read contract as one executable suite.
func TestPhase1(t *testing.T) {
	t.Run("version", func(t *testing.T) {
		h := newHarness(t, newFakeClient())

		status, header, body := h.do(t, "/v1/version")
		require.Equal(t, http.StatusOK, status, string(body))
		assert.Equal(t, apihttp.MediaType, header.Get("Content-Type"))
		assert.Equal(t, apihttp.APIVersion, header.Get("X-SwarmCracker-Api-Version"))

		var info v1.VersionInfo
		require.NoError(t, json.Unmarshal(body, &info), string(body))
		assert.Equal(t, []string{"v1"}, info.APIVersions)
		assert.Equal(t, serverVersion, info.ServerVersion)
		assert.Equal(t, grpcVersion, info.GRPCProtocolVersion)
		assert.Equal(t, []string{"read"}, info.Capabilities)
		assert.Equal(t, testClusterID, info.ClusterID)
	})

	t.Run("missing token is 401 unauthenticated", func(t *testing.T) {
		h := newHarness(t, newFakeClient())

		status, header, body := h.do(t, "/v1/version", withoutToken)
		require.Equal(t, http.StatusUnauthorized, status, string(body))
		assert.Equal(t, "Bearer", header.Get("WWW-Authenticate"))
		assert.Equal(t, apihttp.MediaType, header.Get("Content-Type"))

		var doc v1.ErrorDocument
		require.NoError(t, json.Unmarshal(body, &doc), string(body))
		assert.Equal(t, v1.KindError, doc.Kind)
		assert.Equal(t, string(output.CodeUnauthenticated), doc.Error.Code)
	})

	t.Run("empty list body is exact", func(t *testing.T) {
		h := newHarness(t, &fakeClient{})

		status, header, body := h.do(t, "/v1/services")
		require.Equal(t, http.StatusOK, status, string(body))
		assert.Equal(t, apihttp.MediaType, header.Get("Content-Type"))

		// Read the raw bytes: re-marshalling would hide a stray field or a
		// pretty-printed body. The encoder terminates the one-line document
		// with exactly one newline, so the byte count is pinned too.
		const want = `{"schemaVersion":1,"kind":"ServiceList","items":[],"next":null}`
		assert.Equal(t, want+"\n", string(body), "body must be exactly the contract document plus one newline")
	})

	t.Run("pagination follows next", func(t *testing.T) {
		h := newHarness(t, newFakeClient())

		status, _, body := h.do(t, "/v1/services?limit=2")
		require.Equal(t, http.StatusOK, status, string(body))

		var first v1.ServiceList
		require.NoError(t, json.Unmarshal(body, &first), string(body))
		require.Len(t, first.Items, 2)
		assert.Equal(t, "svc-1", first.Items[0].ID)
		assert.Equal(t, "svc-2", first.Items[1].ID)
		require.NotNil(t, first.Next, "a truncated page must carry a next cursor")

		status, _, body = h.do(t, "/v1/services?limit=2&cursor="+url.QueryEscape(*first.Next))
		require.Equal(t, http.StatusOK, status, string(body))

		var second v1.ServiceList
		require.NoError(t, json.Unmarshal(body, &second), string(body))
		require.Len(t, second.Items, 1)
		assert.Equal(t, "svc-3", second.Items[0].ID)
		assert.Nil(t, second.Next, "the last page must end the stream")
	})

	t.Run("limit over the ceiling is invalid", func(t *testing.T) {
		h := newHarness(t, newFakeClient())

		status, _, body := h.do(t, "/v1/services?limit=5000")
		require.Equal(t, http.StatusBadRequest, status, string(body))

		var doc v1.ErrorDocument
		require.NoError(t, json.Unmarshal(body, &doc), string(body))
		assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)

		fields, ok := doc.Error.Details["fields"].(map[string]any)
		require.True(t, ok, "invalid limit names the offending field: %s", body)
		assert.Contains(t, fields, "limit")
	})

	t.Run("etag makes a conditional read cheap", func(t *testing.T) {
		h := newHarness(t, newFakeClient())

		status, header, _ := h.do(t, "/v1/services")
		require.Equal(t, http.StatusOK, status)
		etag := header.Get("ETag")
		require.NotEmpty(t, etag)

		status, header, body := h.do(t, "/v1/services", withHeader("If-None-Match", etag))
		require.Equal(t, http.StatusNotModified, status, string(body))
		assert.Empty(t, body, "304 must not carry a body")
		assert.Equal(t, etag, header.Get("ETag"))
	})

	t.Run("label selector filters", func(t *testing.T) {
		h := newHarness(t, newFakeClient())

		status, _, body := h.do(t, "/v1/services?label=team%3Dpayments")
		require.Equal(t, http.StatusOK, status, string(body))

		var list v1.ServiceList
		require.NoError(t, json.Unmarshal(body, &list), string(body))
		require.Len(t, list.Items, 1)
		assert.Equal(t, "svc-1", list.Items[0].ID)
		assert.Nil(t, list.Next)
	})

	t.Run("remote client reads the same server", func(t *testing.T) {
		h := newHarness(t, newFakeClient())
		c := h.remoteClient(t)

		ctx := context.Background()

		services, err := c.ListServices(ctx)
		require.NoError(t, err)
		require.Len(t, services, 3, "the three-item fixture fits in one default page")
		assert.Equal(t, []string{"svc-1", "svc-2", "svc-3"}, serviceIDs(services))

		clusterID, err := c.ClusterID(ctx)
		require.NoError(t, err)
		assert.Equal(t, testClusterID, clusterID)

		svc, err := c.GetService(ctx, "svc-2")
		require.NoError(t, err)
		assert.Equal(t, "api", svc.Name)

		_, err = c.GetService(ctx, "svc-missing")
		require.Error(t, err)
		var typed *output.Error
		require.ErrorAs(t, err, &typed, "a remote failure must keep the stable code: %v", err)
		assert.Equal(t, output.CodeNotFound, typed.Code)
	})

	t.Run("remote client follows every next cursor", func(t *testing.T) {
		// One item more than the server's default page size, so the client has
		// no choice but to follow a cursor to see the whole stream.
		const total = 101

		h := newHarness(t, paginatedFakeClient(total))

		// Prove the fixture really spans two pages first: if it fit in one, the
		// client assertion below would pass without following anything.
		status, _, body := h.do(t, "/v1/services")
		require.Equal(t, http.StatusOK, status, string(body))

		var first v1.ServiceList
		require.NoError(t, json.Unmarshal(body, &first), string(body))
		require.Len(t, first.Items, 100, "the default page must stop at the page size")
		require.NotNil(t, first.Next, "a truncated default page must carry a next cursor")

		services, err := h.remoteClient(t).ListServices(context.Background())
		require.NoError(t, err)
		require.Len(t, services, total, "the client must follow every next cursor")
		assert.Equal(t, fixtureIDs(total), serviceIDs(services), "the pages must concatenate in order")
	})
}

// harness is one real server on a loopback TLS listener with a real token
// store, plus the CA file a remote client needs to verify it.
type harness struct {
	ts     *httptest.Server
	client *http.Client
	token  string
	caFile string
}

// newHarness wires apihttp's real handler to fake, mints a reader token in a
// fresh store and starts a TLS server. Config.CertDir is deliberately empty:
// httptest owns the certificate, so the listener's certificate loading is not
// under test here.
func newHarness(t *testing.T, fake *fakeClient) *harness {
	t.Helper()

	store, err := apihttp.OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	require.NoError(t, err)
	token, _, err := store.Create("phase1-reader", apihttp.RoleReader)
	require.NoError(t, err)

	srv, err := apihttp.New(apihttp.Config{
		Client:        fake,
		Tokens:        store,
		ServerVersion: serverVersion,
		GRPCVersion:   grpcVersion,
		Capabilities:  []string{"read"},
		// ClusterID stays empty so /v1/version proves the client fallback.
	})
	require.NoError(t, err)

	ts := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(ts.Close)

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	require.NotEmpty(t, caPEM)
	require.NoError(t, os.WriteFile(caFile, caPEM, 0o600))

	return &harness{
		ts:     ts,
		client: ts.Client(),
		token:  token,
		caFile: caFile,
	}
}

// remoteClient returns the real remote transport configured against this
// harness's TLS listener and CA file.
func (h *harness) remoteClient(t *testing.T) *client.Client {
	t.Helper()

	c, err := client.New(client.Config{
		Endpoint: h.ts.URL,
		CAFile:   h.caFile,
		Token:    h.token,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// do performs one authenticated GET over TLS. mutate may override headers after
// the defaults are set, which is how the missing-token and If-None-Match cases
// are expressed.
func (h *harness) do(t *testing.T, path string, mutate ...func(*http.Request)) (int, http.Header, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.ts.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", apihttp.MediaType)
	req.Header.Set("Authorization", "Bearer "+h.token)
	for _, m := range mutate {
		m(req)
	}

	resp, err := h.client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// Every response -- success, error or 304 -- carries the version header.
	require.Equal(t, apihttp.APIVersion, resp.Header.Get("X-SwarmCracker-Api-Version"),
		"%s must advertise the API version", path)
	// Every response with a body is a v1 document. net/http strips
	// Content-Type from a 304 before it reaches the wire
	// (net/http/transfer.go suppressedHeaders304), even though writeJSON sets
	// it, so a 304 can only be checked for its validator and empty body.
	if resp.StatusCode != http.StatusNotModified {
		require.Equal(t, apihttp.MediaType, resp.Header.Get("Content-Type"),
			"%s must serve the v1 media type", path)
	}

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, body
}

func withoutToken(r *http.Request) { r.Header.Del("Authorization") }

func withHeader(name, value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(name, value) }
}

func serviceIDs(services []v1.Service) []string {
	ids := make([]string, 0, len(services))
	for _, svc := range services {
		ids = append(ids, svc.ID)
	}
	return ids
}

// fakeClient is a complete apihttp.Client with deterministic fixtures. It
// replaces SwarmKit, not the HTTP layer: the server under test is the real one.
type fakeClient struct {
	services  []v1.Service
	tasks     []v1.Task
	nodes     []v1.Node
	networks  []v1.Network
	configs   []v1.Config
	secrets   []v1.Secret
	clusterID string
}

// newFakeClient returns fixtures ordered by createdAt then id, which is the
// ordering the Client contract promises and pagination relies on. svc-1 is the
// only service labelled team=payments.
func newFakeClient() *fakeClient {
	return &fakeClient{
		clusterID: testClusterID,
		services: []v1.Service{
			{
				ID:        "svc-1",
				Name:      "web",
				Revision:  "1",
				CreatedAt: "2026-01-01T00:00:00Z",
				Labels:    map[string]string{"team": "payments"},
				Spec: v1.ServiceSpec{
					Mode: "replicated",
					Task: v1.TaskSpec{Image: "nginx:alpine"},
				},
				Status: v1.ServiceStatus{DesiredTasks: 1, RunningTasks: 1, Converged: true},
			},
			{
				ID:        "svc-2",
				Name:      "api",
				Revision:  "1",
				CreatedAt: "2026-01-02T00:00:00Z",
				Labels:    map[string]string{"team": "search"},
				Spec: v1.ServiceSpec{
					Mode: "replicated",
					Task: v1.TaskSpec{Image: "ghcr.io/example/api:2"},
				},
				Status: v1.ServiceStatus{DesiredTasks: 1, RunningTasks: 1, Converged: true},
			},
			{
				ID:        "svc-3",
				Name:      "worker",
				Revision:  "1",
				CreatedAt: "2026-01-03T00:00:00Z",
				Spec: v1.ServiceSpec{
					Mode: "global",
					Task: v1.TaskSpec{Image: "ghcr.io/example/worker:3"},
				},
				Status: v1.ServiceStatus{DesiredTasks: 1, RunningTasks: 1, Converged: true},
			},
		},
		tasks: []v1.Task{{
			ID:           "task-1",
			ServiceID:    "svc-1",
			ServiceName:  "web",
			NodeID:       "node-1",
			NodeName:     "manager-1",
			Revision:     "1",
			State:        "running",
			DesiredState: "running",
			CreatedAt:    "2026-01-01T00:00:00Z",
			Image:        "nginx:alpine",
		}},
		nodes: []v1.Node{{
			ID:           "node-1",
			Hostname:     "manager-1",
			Revision:     "1",
			CreatedAt:    "2026-01-01T00:00:00Z",
			Role:         "manager",
			Membership:   "accepted",
			Availability: "active",
			State:        "ready",
			Address:      "10.0.0.1",
		}},
		networks: []v1.Network{{
			ID:        "net-1",
			Name:      "frontend",
			Revision:  "1",
			CreatedAt: "2026-01-01T00:00:00Z",
			Driver:    "overlay",
		}},
		configs: []v1.Config{{
			ID:        "cfg-1",
			Name:      "settings",
			Revision:  "1",
			CreatedAt: "2026-01-01T00:00:00Z",
		}},
		secrets: []v1.Secret{{
			ID:        "sec-1",
			Name:      "password",
			Revision:  "1",
			CreatedAt: "2026-01-01T00:00:00Z",
		}},
	}
}

// paginatedFakeClient returns n services ordered by createdAt then id. The item
// structs are minimal on purpose: a fixture larger than the default page size
// stays cheap.
func paginatedFakeClient(n int) *fakeClient {
	ids := fixtureIDs(n)
	services := make([]v1.Service, 0, n)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range ids {
		services = append(services, v1.Service{
			ID:        id,
			Name:      id,
			Revision:  "1",
			CreatedAt: start.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
		})
	}
	return &fakeClient{clusterID: testClusterID, services: services}
}

// fixtureIDs is the ordered ID sequence paginatedFakeClient produces.
func fixtureIDs(n int) []string {
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		ids = append(ids, fmt.Sprintf("svc-%03d", i))
	}
	return ids
}

func (f *fakeClient) ListServices(context.Context) ([]v1.Service, error) { return f.services, nil }

func (f *fakeClient) GetService(_ context.Context, id string) (v1.Service, error) {
	for _, svc := range f.services {
		if svc.ID == id {
			return svc, nil
		}
	}
	return v1.Service{}, output.NewErrorf(output.CodeNotFound, "service %q not found", id)
}

func (f *fakeClient) ListServiceTasks(_ context.Context, serviceID string) ([]v1.Task, error) {
	tasks := make([]v1.Task, 0, len(f.tasks))
	for _, task := range f.tasks {
		if task.ServiceID == serviceID {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

func (f *fakeClient) ListTasks(context.Context) ([]v1.Task, error) { return f.tasks, nil }

func (f *fakeClient) GetTask(_ context.Context, id string) (v1.Task, error) {
	for _, task := range f.tasks {
		if task.ID == id {
			return task, nil
		}
	}
	return v1.Task{}, output.NewErrorf(output.CodeNotFound, "task %q not found", id)
}

func (f *fakeClient) ListNodes(context.Context) ([]v1.Node, error) { return f.nodes, nil }

func (f *fakeClient) GetNode(_ context.Context, id string) (v1.Node, error) {
	for _, node := range f.nodes {
		if node.ID == id {
			return node, nil
		}
	}
	return v1.Node{}, output.NewErrorf(output.CodeNotFound, "node %q not found", id)
}

func (f *fakeClient) ListNetworks(context.Context) ([]v1.Network, error) { return f.networks, nil }

func (f *fakeClient) GetNetwork(_ context.Context, id string) (v1.Network, error) {
	for _, network := range f.networks {
		if network.ID == id {
			return network, nil
		}
	}
	return v1.Network{}, output.NewErrorf(output.CodeNotFound, "network %q not found", id)
}

func (f *fakeClient) ListConfigs(context.Context) ([]v1.Config, error) { return f.configs, nil }

func (f *fakeClient) GetConfig(_ context.Context, id string) (v1.Config, error) {
	for _, cfg := range f.configs {
		if cfg.ID == id {
			return cfg, nil
		}
	}
	return v1.Config{}, output.NewErrorf(output.CodeNotFound, "config %q not found", id)
}

func (f *fakeClient) ListSecrets(context.Context) ([]v1.Secret, error) { return f.secrets, nil }

func (f *fakeClient) GetSecret(_ context.Context, id string) (v1.Secret, error) {
	for _, secret := range f.secrets {
		if secret.ID == id {
			return secret, nil
		}
	}
	return v1.Secret{}, output.NewErrorf(output.CodeNotFound, "secret %q not found", id)
}

func (f *fakeClient) ClusterID(context.Context) (string, error) { return f.clusterID, nil }

// The phase-1 acceptance suite is read-only. These four exist so the fake
// satisfies apihttp.Client; they answer not_implemented rather than pretending a
// write happened.
func (f *fakeClient) CreateService(context.Context, v1.ServiceWrite, v1.WriteOptions) (v1.Service, error) {
	return v1.Service{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

func (f *fakeClient) UpdateService(context.Context, string, []byte, v1.WriteOptions) (v1.Service, error) {
	return v1.Service{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

func (f *fakeClient) DeleteService(context.Context, string, v1.WriteOptions) (v1.Service, error) {
	return v1.Service{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

func (f *fakeClient) RollbackService(context.Context, string, v1.WriteOptions) (v1.Service, error) {
	return v1.Service{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

// The named writes are phase 2 as well. They answer not_implemented for the same
// reason: this suite must never pretend a mutation happened.
func (f *fakeClient) CreateConfig(context.Context, v1.ConfigWrite, v1.WriteOptions) (v1.Config, error) {
	return v1.Config{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

func (f *fakeClient) UpdateConfig(context.Context, string, []byte, v1.WriteOptions) (v1.Config, error) {
	return v1.Config{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

func (f *fakeClient) DeleteConfig(context.Context, string, v1.WriteOptions) (v1.Config, error) {
	return v1.Config{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

func (f *fakeClient) CreateSecret(context.Context, v1.SecretWrite, v1.WriteOptions) (v1.Secret, error) {
	return v1.Secret{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

func (f *fakeClient) UpdateSecret(context.Context, string, []byte, v1.WriteOptions) (v1.Secret, error) {
	return v1.Secret{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

func (f *fakeClient) DeleteSecret(context.Context, string, v1.WriteOptions) (v1.Secret, error) {
	return v1.Secret{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

func (f *fakeClient) CreateNetwork(context.Context, v1.NetworkWrite, v1.WriteOptions) (v1.Network, error) {
	return v1.Network{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

func (f *fakeClient) DeleteNetwork(context.Context, string, v1.WriteOptions) (v1.Network, error) {
	return v1.Network{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}

func (f *fakeClient) UpdateNode(context.Context, string, []byte, v1.WriteOptions) (v1.Node, error) {
	return v1.Node{}, output.NewError(output.CodeNotImplemented, "phase1 fake has no write surface")
}
