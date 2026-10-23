// Package api also holds the phase-2 acceptance suite: the real write, wait,
// dry-run, idempotency, event and rate-limit surfaces over a real TLS listener.
package api

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
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

// phase2Client embeds the phase-1 read fake and implements the writes with
// deterministic, in-memory behavior. It never touches SwarmKit.
type phase2Client struct {
	*fakeClient
	createCalls int
	updateCalls int
	deleteCalls int
	watch       chan v1.Event
}

func newPhase2Client() *phase2Client {
	return &phase2Client{fakeClient: newFakeClient()}
}

func (f *phase2Client) CreateService(_ context.Context, write v1.ServiceWrite, opts v1.WriteOptions) (v1.Service, error) {
	if opts.DryRun {
		return v1.Service{Name: write.Name, Spec: v1.ServiceSpec{Mode: "replicated", Task: v1.TaskSpec{Image: write.Spec.Task.Image}}}, nil
	}
	f.createCalls++
	service := v1.Service{
		ID:        "svc-new",
		Name:      write.Name,
		Revision:  "1",
		CreatedAt: "2026-10-23T00:00:00Z",
		Spec: v1.ServiceSpec{
			Mode: "replicated",
			Task: v1.TaskSpec{Image: write.Spec.Task.Image},
		},
		Status: v1.ServiceStatus{DesiredTasks: 1, RunningTasks: 1, Converged: true},
	}
	f.services = append(f.services, service)
	return service, nil
}

func (f *phase2Client) UpdateService(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Service, error) {
	if opts.DryRun {
		for i := range f.services {
			if f.services[i].ID == ref || f.services[i].Name == ref {
				return f.services[i], nil
			}
		}
	}
	f.updateCalls++
	for i := range f.services {
		if f.services[i].ID == ref || f.services[i].Name == ref {
			f.services[i].Revision = "2"
			f.services[i].Spec.Replicas = int64Ptr(3)
			return f.services[i], nil
		}
	}
	return v1.Service{}, outputNotFound("service", ref)
}

func (f *phase2Client) DeleteService(_ context.Context, ref string, _ v1.WriteOptions) (v1.Service, error) {
	f.deleteCalls++
	for i := range f.services {
		if f.services[i].ID == ref || f.services[i].Name == ref {
			out := f.services[i]
			f.services = append(f.services[:i], f.services[i+1:]...)
			return out, nil
		}
	}
	return v1.Service{}, outputNotFound("service", ref)
}

func (f *phase2Client) RollbackService(_ context.Context, ref string, _ v1.WriteOptions) (v1.Service, error) {
	f.updateCalls++
	for i := range f.services {
		if f.services[i].ID == ref || f.services[i].Name == ref {
			f.services[i].Revision = "3"
			return f.services[i], nil
		}
	}
	return v1.Service{}, outputNotFound("service", ref)
}

func (f *phase2Client) CreateConfig(_ context.Context, write v1.ConfigWrite, _ v1.WriteOptions) (v1.Config, error) {
	config := v1.Config{
		ID: "cfg-new", Name: write.Name, Revision: "1",
		CreatedAt: "2026-10-23T00:00:00Z", Labels: write.Labels,
	}
	if write.Data != "" {
		config.Data = &write.Data
	}
	f.configs = append(f.configs, config)
	return config, nil
}

func (f *phase2Client) UpdateConfig(_ context.Context, ref string, _ []byte, _ v1.WriteOptions) (v1.Config, error) {
	for i := range f.configs {
		if f.configs[i].ID == ref || f.configs[i].Name == ref {
			f.configs[i].Revision = "2"
			return f.configs[i], nil
		}
	}
	return v1.Config{}, outputNotFound("config", ref)
}

func (f *phase2Client) DeleteConfig(_ context.Context, ref string, _ v1.WriteOptions) (v1.Config, error) {
	for i := range f.configs {
		if f.configs[i].ID == ref || f.configs[i].Name == ref {
			out := f.configs[i]
			f.configs = append(f.configs[:i], f.configs[i+1:]...)
			return out, nil
		}
	}
	return v1.Config{}, outputNotFound("config", ref)
}

func (f *phase2Client) CreateSecret(_ context.Context, write v1.SecretWrite, _ v1.WriteOptions) (v1.Secret, error) {
	secret := v1.Secret{ID: "sec-new", Name: write.Name, Revision: "1", CreatedAt: "2026-10-23T00:00:00Z", Labels: write.Labels}
	f.secrets = append(f.secrets, secret)
	return secret, nil
}

func (f *phase2Client) UpdateSecret(_ context.Context, ref string, _ []byte, _ v1.WriteOptions) (v1.Secret, error) {
	for i := range f.secrets {
		if f.secrets[i].ID == ref || f.secrets[i].Name == ref {
			f.secrets[i].Revision = "2"
			return f.secrets[i], nil
		}
	}
	return v1.Secret{}, outputNotFound("secret", ref)
}

func (f *phase2Client) DeleteSecret(_ context.Context, ref string, _ v1.WriteOptions) (v1.Secret, error) {
	for i := range f.secrets {
		if f.secrets[i].ID == ref || f.secrets[i].Name == ref {
			out := f.secrets[i]
			f.secrets = append(f.secrets[:i], f.secrets[i+1:]...)
			return out, nil
		}
	}
	return v1.Secret{}, outputNotFound("secret", ref)
}

func (f *phase2Client) CreateNetwork(_ context.Context, write v1.NetworkWrite, _ v1.WriteOptions) (v1.Network, error) {
	network := v1.Network{ID: "net-new", Name: write.Name, Revision: "1", CreatedAt: "2026-10-23T00:00:00Z", Driver: write.Driver}
	f.networks = append(f.networks, network)
	return network, nil
}

func (f *phase2Client) DeleteNetwork(_ context.Context, ref string, _ v1.WriteOptions) (v1.Network, error) {
	for i := range f.networks {
		if f.networks[i].ID == ref || f.networks[i].Name == ref {
			out := f.networks[i]
			f.networks = append(f.networks[:i], f.networks[i+1:]...)
			return out, nil
		}
	}
	return v1.Network{}, outputNotFound("network", ref)
}

func (f *phase2Client) UpdateNode(_ context.Context, ref string, _ []byte, opts v1.WriteOptions) (v1.Node, error) {
	if opts.DryRun {
		for i := range f.nodes {
			if f.nodes[i].ID == ref || f.nodes[i].Hostname == ref {
				return f.nodes[i], nil
			}
		}
	}
	for i := range f.nodes {
		if f.nodes[i].ID == ref || f.nodes[i].Hostname == ref {
			f.nodes[i].Revision = "2"
			f.nodes[i].Availability = "drain"
			return f.nodes[i], nil
		}
	}
	return v1.Node{}, outputNotFound("node", ref)
}

func (f *phase2Client) WatchEvents(_ context.Context, _ v1.EventOptions) (<-chan v1.Event, <-chan error, error) {
	events := f.watch
	if events == nil {
		events = make(chan v1.Event)
	}
	errs := make(chan error)
	return events, errs, nil
}

func outputNotFound(kind, ref string) error {
	return output.NewError(output.CodeNotFound, kind+" "+ref+" not found")
}

func int64Ptr(v int64) *int64 { return &v }

// phase2Harness is the real server plus the operator and reader tokens.
type phase2Harness struct {
	ts       *httptest.Server
	client   *http.Client
	caFile   string
	operator string
	reader   string
	admin    string
	fake     *phase2Client
}

func newPhase2Harness(t *testing.T, fake *phase2Client) *phase2Harness {
	t.Helper()
	if fake == nil {
		fake = newPhase2Client()
	}

	store, err := apihttp.OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	require.NoError(t, err)
	operator, _, err := store.Create("phase2-operator", apihttp.RoleOperator)
	require.NoError(t, err)
	reader, _, err := store.Create("phase2-reader", apihttp.RoleReader)
	require.NoError(t, err)
	admin, _, err := store.Create("phase2-admin", apihttp.RoleAdmin)
	require.NoError(t, err)

	srv, err := apihttp.New(apihttp.Config{
		Client:         fake,
		Tokens:         store,
		ServerVersion:  serverVersion,
		GRPCVersion:    grpcVersion,
		Capabilities:   []string{"read", "write", "events"},
		RateLimit:      apihttp.NewRateLimiter(1e9, 1e9),
		EventHeartbeat: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	ts := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(ts.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	require.NoError(t, os.WriteFile(caFile, caPEM, 0o600))

	return &phase2Harness{ts: ts, client: ts.Client(), caFile: caFile, operator: operator, reader: reader, admin: admin, fake: fake}
}

func (h *phase2Harness) do(t *testing.T, method, path string, body []byte, token string, headers ...func(*http.Request)) (int, http.Header, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, h.ts.URL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Accept", apihttp.MediaType)
	if body != nil {
		req.Header.Set("Content-Type", apihttp.MediaType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, mutate := range headers {
		mutate(req)
	}
	resp, err := h.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, out
}

func (h *phase2Harness) remote(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New(client.Config{Endpoint: h.ts.URL, CAFile: h.caFile, Token: h.operator})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestPhase2(t *testing.T) {
	t.Run("service write, idempotency, patch and delete", func(t *testing.T) {
		h := newPhase2Harness(t, nil)
		body := []byte(`{"name":"web","spec":{"task":{"image":"nginx:alpine"}}}`)

		status, header, out := h.do(t, http.MethodPost, "/v1/services", body, h.operator,
			withHeader("Idempotency-Key", "phase2-key"))
		require.Equal(t, http.StatusCreated, status, string(out))
		assert.Equal(t, `"1"`, header.Get("ETag"))
		assert.Empty(t, header.Get("Idempotency-Replayed"))

		status, header, out = h.do(t, http.MethodPost, "/v1/services", body, h.operator,
			withHeader("Idempotency-Key", "phase2-key"))
		require.Equal(t, http.StatusCreated, status, string(out))
		assert.Equal(t, "true", header.Get("Idempotency-Replayed"))
		assert.Equal(t, 1, h.fake.createCalls)

		status, header, out = h.do(t, http.MethodPatch, "/v1/services/svc-new",
			[]byte(`{"spec":{"replicas":3}}`), h.operator,
			withHeader("If-Match", `"1"`),
			withHeader("Content-Type", "application/merge-patch+json"))
		require.Equal(t, http.StatusOK, status, string(out))
		assert.Equal(t, `"2"`, header.Get("ETag"))
		assert.Equal(t, 1, h.fake.updateCalls)

		status, _, out = h.do(t, http.MethodPatch, "/v1/services/svc-new?dryRun=true",
			[]byte(`{"spec":{"replicas":5}}`), h.operator,
			withHeader("If-Match", `"2"`),
			withHeader("Content-Type", "application/merge-patch+json"))
		require.Equal(t, http.StatusOK, status, string(out))
		assert.Contains(t, string(out), `"dryRun":true`)
		assert.Equal(t, 1, h.fake.updateCalls, "a dry run never reaches the mutation")

		status, _, out = h.do(t, http.MethodDelete, "/v1/services/svc-new", nil, h.operator,
			withHeader("If-Match", `"2"`))
		require.Equal(t, http.StatusNoContent, status, string(out))
		assert.Equal(t, 1, h.fake.deleteCalls)
	})

	t.Run("config value visibility and secret payload never leaks", func(t *testing.T) {
		h := newPhase2Harness(t, nil)
		status, _, out := h.do(t, http.MethodPost, "/v1/configs",
			[]byte(`{"name":"app","data":"listen 8080"}`), h.operator)
		require.Equal(t, http.StatusCreated, status, string(out))
		assert.Contains(t, string(out), `"data":"listen 8080"`, "operator has configs:read-values")

		status, _, out = h.do(t, http.MethodGet, "/v1/configs/cfg-new", nil, h.reader)
		require.Equal(t, http.StatusOK, status, string(out))
		assert.NotContains(t, string(out), `"data"`)

		status, _, out = h.do(t, http.MethodPost, "/v1/secrets",
			[]byte(`{"name":"token","data":"c2VjcmV0"}`), h.operator)
		require.Equal(t, http.StatusCreated, status, string(out))
		assert.NotContains(t, string(out), `"data"`)

		status, _, out = h.do(t, http.MethodGet, "/v1/secrets/sec-new", nil, h.operator)
		require.Equal(t, http.StatusOK, status, string(out))
		assert.NotContains(t, string(out), `"data"`)
	})

	t.Run("network create has no update and node patches", func(t *testing.T) {
		h := newPhase2Harness(t, nil)
		status, _, out := h.do(t, http.MethodPost, "/v1/networks",
			[]byte(`{"name":"app","driver":"overlay"}`), h.operator)
		require.Equal(t, http.StatusCreated, status, string(out))

		status, header, out := h.do(t, http.MethodPatch, "/v1/networks/net-new",
			[]byte(`{"labels":{"x":"y"}}`), h.operator,
			withHeader("Content-Type", "application/merge-patch+json"))
		require.Equal(t, http.StatusMethodNotAllowed, status, string(out))
		assert.Equal(t, "GET, DELETE", header.Get("Allow"))
		assert.Contains(t, string(out), `"reason":"method_not_allowed"`)

		status, _, out = h.do(t, http.MethodPatch, "/v1/nodes/node-1",
			[]byte(`{"availability":"drain"}`), h.admin,
			withHeader("If-Match", `"1"`),
			withHeader("Content-Type", "application/merge-patch+json"))
		require.Equal(t, http.StatusOK, status, string(out))
		assert.Contains(t, string(out), `"availability":"drain"`)
	})

	t.Run("wait converged and events reach the remote client", func(t *testing.T) {
		h := newPhase2Harness(t, nil)
		h.fake.watch = make(chan v1.Event, 1)
		h.fake.watch <- v1.Event{
			SchemaVersion: v1.SchemaVersion, Kind: v1.KindEvent, Cursor: "9",
			Type: "service.updated", Resource: v1.EventResource{Kind: v1.KindService, ID: "svc-1", Name: "web"},
		}
		close(h.fake.watch)

		status, _, out := h.do(t, http.MethodPost, "/v1/services?wait=converged&timeoutMs=1000",
			[]byte(`{"name":"web","spec":{"task":{"image":"nginx:alpine"}}}`), h.operator)
		require.Equal(t, http.StatusCreated, status, string(out))
		assert.NotContains(t, string(out), "wait_timeout")

		c := h.remote(t)
		events, errs, err := c.WatchEvents(t.Context(), v1.EventOptions{})
		require.NoError(t, err)
		select {
		case event := <-events:
			assert.Equal(t, "9", event.Cursor)
			assert.Equal(t, "service.updated", event.Type)
		case err := <-errs:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("no event arrived")
		}
	})

	t.Run("rate limit is per token", func(t *testing.T) {
		fake := newPhase2Client()
		store, err := apihttp.OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
		require.NoError(t, err)
		token, _, err := store.Create("limited", apihttp.RoleReader)
		require.NoError(t, err)
		other, _, err := store.Create("other", apihttp.RoleReader)
		require.NoError(t, err)
		srv, err := apihttp.New(apihttp.Config{
			Client: fake, Tokens: store,
			RateLimit: apihttp.NewRateLimiter(0.001, 1),
		})
		require.NoError(t, err)
		ts := httptest.NewTLSServer(srv.Handler())
		t.Cleanup(ts.Close)

		do := func(secret string) *http.Response {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/v1/version", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+secret)
			req.Header.Set("Accept", apihttp.MediaType)
			resp, err := ts.Client().Do(req)
			require.NoError(t, err)
			t.Cleanup(func() { resp.Body.Close() })
			return resp
		}
		require.Equal(t, http.StatusOK, do(token).StatusCode)
		limited := do(token)
		require.Equal(t, http.StatusTooManyRequests, limited.StatusCode)
		assert.NotEmpty(t, limited.Header.Get("Retry-After"))
		assert.Equal(t, http.StatusOK, do(other).StatusCode)
	})
}
