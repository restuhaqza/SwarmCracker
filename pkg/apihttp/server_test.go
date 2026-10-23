package apihttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeClient is the test double for the Client interface. One err field is
// enough: each test exercises a single failure path.
type fakeClient struct {
	services     []v1.Service
	service      v1.Service
	serviceTasks []v1.Task
	tasks        []v1.Task
	task         v1.Task
	nodes        []v1.Node
	node         v1.Node
	networks     []v1.Network
	network      v1.Network
	configs      []v1.Config
	config       v1.Config
	secrets      []v1.Secret
	secret       v1.Secret
	clusterID    string
	clusterErr   error
	err          error
	gotServiceID string

	// Write surface. One result field per method keeps a test explicit about
	// which call it exercised.
	createdService    v1.Service
	updatedService    v1.Service
	deletedService    v1.Service
	rolledBackService v1.Service
	writeErr          error
	gotWrite          v1.ServiceWrite
	gotPatch          []byte
	gotRef            string
	gotWriteOpts      v1.WriteOptions
	createCalls       int
	updateCalls       int
	deleteCalls       int
	rollbackCalls     int

	// Event stream.
	events       chan v1.Event
	eventErrs    chan error
	watchErr     error
	gotEventOpts v1.EventOptions

	// Named write surface (config, secret, network, node).
	createdConfig  v1.Config
	updatedConfig  v1.Config
	deletedConfig  v1.Config
	createdSecret  v1.Secret
	updatedSecret  v1.Secret
	deletedSecret  v1.Secret
	createdNetwork v1.Network
	deletedNetwork v1.Network
	updatedNode    v1.Node
	namedErr       error
	gotConfigWrite v1.ConfigWrite
	gotSecretWrite v1.SecretWrite
	gotNetworkWrit v1.NetworkWrite
	gotNamedRef    string
	gotNamedPatch  []byte
	gotNamedOpts   v1.WriteOptions
	configCreates  int
	configUpdates  int
	configDeletes  int
	secretCreates  int
	secretUpdates  int
	secretDeletes  int
	networkCreates int
	networkDeletes int
	nodeUpdates    int
}

func (f *fakeClient) ListServices(context.Context) ([]v1.Service, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.services, nil
}

func (f *fakeClient) GetService(_ context.Context, id string) (v1.Service, error) {
	f.gotServiceID = id
	if f.err != nil {
		return v1.Service{}, f.err
	}
	return f.service, nil
}

func (f *fakeClient) ListServiceTasks(_ context.Context, id string) ([]v1.Task, error) {
	f.gotServiceID = id
	if f.err != nil {
		return nil, f.err
	}
	return f.serviceTasks, nil
}

func (f *fakeClient) ListTasks(context.Context) ([]v1.Task, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tasks, nil
}

func (f *fakeClient) GetTask(context.Context, string) (v1.Task, error) {
	if f.err != nil {
		return v1.Task{}, f.err
	}
	return f.task, nil
}

func (f *fakeClient) ListNodes(context.Context) ([]v1.Node, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.nodes, nil
}

func (f *fakeClient) GetNode(context.Context, string) (v1.Node, error) {
	if f.err != nil {
		return v1.Node{}, f.err
	}
	return f.node, nil
}

func (f *fakeClient) ListNetworks(context.Context) ([]v1.Network, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.networks, nil
}

func (f *fakeClient) GetNetwork(context.Context, string) (v1.Network, error) {
	if f.err != nil {
		return v1.Network{}, f.err
	}
	return f.network, nil
}

func (f *fakeClient) ListConfigs(context.Context) ([]v1.Config, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.configs, nil
}

func (f *fakeClient) GetConfig(context.Context, string) (v1.Config, error) {
	if f.err != nil {
		return v1.Config{}, f.err
	}
	return f.config, nil
}

func (f *fakeClient) ListSecrets(context.Context) ([]v1.Secret, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.secrets, nil
}

func (f *fakeClient) GetSecret(context.Context, string) (v1.Secret, error) {
	if f.err != nil {
		return v1.Secret{}, f.err
	}
	return f.secret, nil
}

func (f *fakeClient) ClusterID(context.Context) (string, error) {
	return f.clusterID, f.clusterErr
}

func (f *fakeClient) WatchEvents(_ context.Context, opts v1.EventOptions) (<-chan v1.Event, <-chan error, error) {
	f.gotEventOpts = opts
	if f.watchErr != nil {
		return nil, nil, f.watchErr
	}
	events := f.events
	if events == nil {
		events = make(chan v1.Event)
		close(events)
	}
	errs := f.eventErrs
	if errs == nil {
		// An open channel that never fires: a closed one would race the events
		// channel in the handler's select and end the stream early.
		errs = make(chan error)
	}
	return events, errs, nil
}

func (f *fakeClient) CreateService(_ context.Context, write v1.ServiceWrite, opts v1.WriteOptions) (v1.Service, error) {
	f.createCalls++
	f.gotWrite = write
	f.gotWriteOpts = opts
	if f.writeErr != nil {
		return v1.Service{}, f.writeErr
	}
	return f.createdService, nil
}

func (f *fakeClient) UpdateService(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Service, error) {
	f.updateCalls++
	f.gotRef = ref
	f.gotPatch = patch
	f.gotWriteOpts = opts
	if f.writeErr != nil {
		return v1.Service{}, f.writeErr
	}
	return f.updatedService, nil
}

func (f *fakeClient) DeleteService(_ context.Context, ref string, opts v1.WriteOptions) (v1.Service, error) {
	f.deleteCalls++
	f.gotRef = ref
	f.gotWriteOpts = opts
	if f.writeErr != nil {
		return v1.Service{}, f.writeErr
	}
	return f.deletedService, nil
}

func (f *fakeClient) RollbackService(_ context.Context, ref string, opts v1.WriteOptions) (v1.Service, error) {
	f.rollbackCalls++
	f.gotRef = ref
	f.gotWriteOpts = opts
	if f.writeErr != nil {
		return v1.Service{}, f.writeErr
	}
	return f.rolledBackService, nil
}

func (f *fakeClient) CreateConfig(_ context.Context, write v1.ConfigWrite, opts v1.WriteOptions) (v1.Config, error) {
	f.configCreates++
	f.gotConfigWrite = write
	f.gotNamedOpts = opts
	if f.namedErr != nil {
		return v1.Config{}, f.namedErr
	}
	return f.createdConfig, nil
}

func (f *fakeClient) UpdateConfig(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Config, error) {
	f.configUpdates++
	f.gotNamedRef = ref
	f.gotNamedPatch = patch
	f.gotNamedOpts = opts
	if f.namedErr != nil {
		return v1.Config{}, f.namedErr
	}
	return f.updatedConfig, nil
}

func (f *fakeClient) DeleteConfig(_ context.Context, ref string, opts v1.WriteOptions) (v1.Config, error) {
	f.configDeletes++
	f.gotNamedRef = ref
	f.gotNamedOpts = opts
	if f.namedErr != nil {
		return v1.Config{}, f.namedErr
	}
	return f.deletedConfig, nil
}

func (f *fakeClient) CreateSecret(_ context.Context, write v1.SecretWrite, opts v1.WriteOptions) (v1.Secret, error) {
	f.secretCreates++
	f.gotSecretWrite = write
	f.gotNamedOpts = opts
	if f.namedErr != nil {
		return v1.Secret{}, f.namedErr
	}
	return f.createdSecret, nil
}

func (f *fakeClient) UpdateSecret(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Secret, error) {
	f.secretUpdates++
	f.gotNamedRef = ref
	f.gotNamedPatch = patch
	f.gotNamedOpts = opts
	if f.namedErr != nil {
		return v1.Secret{}, f.namedErr
	}
	return f.updatedSecret, nil
}

func (f *fakeClient) DeleteSecret(_ context.Context, ref string, opts v1.WriteOptions) (v1.Secret, error) {
	f.secretDeletes++
	f.gotNamedRef = ref
	f.gotNamedOpts = opts
	if f.namedErr != nil {
		return v1.Secret{}, f.namedErr
	}
	return f.deletedSecret, nil
}

func (f *fakeClient) CreateNetwork(_ context.Context, write v1.NetworkWrite, opts v1.WriteOptions) (v1.Network, error) {
	f.networkCreates++
	f.gotNetworkWrit = write
	f.gotNamedOpts = opts
	if f.namedErr != nil {
		return v1.Network{}, f.namedErr
	}
	return f.createdNetwork, nil
}

func (f *fakeClient) DeleteNetwork(_ context.Context, ref string, opts v1.WriteOptions) (v1.Network, error) {
	f.networkDeletes++
	f.gotNamedRef = ref
	f.gotNamedOpts = opts
	if f.namedErr != nil {
		return v1.Network{}, f.namedErr
	}
	return f.deletedNetwork, nil
}

func (f *fakeClient) UpdateNode(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Node, error) {
	f.nodeUpdates++
	f.gotNamedRef = ref
	f.gotNamedPatch = patch
	f.gotNamedOpts = opts
	if f.namedErr != nil {
		return v1.Node{}, f.namedErr
	}
	return f.updatedNode, nil
}

type testAPI struct {
	handler http.Handler
	unix    http.Handler
	token   string
	store   *TokenStore
}

func newTestAPI(t *testing.T, fc *fakeClient) *testAPI {
	t.Helper()
	return newTestAPIWithConfig(t, fc, Config{
		ServerVersion: "0.11.0",
		GRPCVersion:   "v1",
		ClusterID:     "cluster-1",
	})
}

func newTestAPIWithConfig(t *testing.T, fc *fakeClient, cfg Config) *testAPI {
	t.Helper()
	if fc == nil {
		fc = &fakeClient{}
	}
	cfg.Client = fc
	if cfg.RateLimit == nil {
		cfg.RateLimit = NewRateLimiter(1e9, 1e9)
	}

	store, err := OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	require.NoError(t, err)
	secret, _, err := store.Create("test", RoleReader)
	require.NoError(t, err)
	cfg.Tokens = store

	srv, err := New(cfg)
	require.NoError(t, err)

	return &testAPI{
		handler: srv.Handler(),
		unix:    srv.UnixHandler(),
		token:   secret,
		store:   store,
	}
}

func (a *testAPI) do(t *testing.T, method, target string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Accept", MediaType)
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

func (a *testAPI) doRawPath(t *testing.T, method, rawPath string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/", nil)
	// Set the path directly: httptest.NewRequest would parse "//host/x" as a
	// network-path reference and lose the doubled slash.
	req.URL.Path = rawPath
	req.Header.Set("Accept", MediaType)
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

func withToken(token string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	}
}

func withHeader(name, value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(name, value) }
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) v1.ErrorDocument {
	t.Helper()
	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc), rec.Body.String())
	return doc
}

func assertWireHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, MediaType, rec.Header().Get("Content-Type"))
	assert.Equal(t, APIVersion, rec.Header().Get(apiVersionHeader))
}

func TestVersionDocument(t *testing.T) {
	api := newTestAPI(t, nil)

	rec := api.do(t, http.MethodGet, "/v1/version", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assertWireHeaders(t, rec)

	var info v1.VersionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Equal(t, v1.SchemaVersion, info.SchemaVersion)
	assert.Equal(t, v1.KindVersionInfo, info.Kind)
	assert.Equal(t, []string{"v1"}, info.APIVersions)
	assert.Equal(t, "0.11.0", info.ServerVersion)
	assert.Equal(t, "v1", info.GRPCProtocolVersion)
	assert.Equal(t, "cluster-1", info.ClusterID)
	assert.Equal(t, []string{"read", "write", "events", "exec"}, info.Capabilities,
		"nil capabilities default to the served surface: read, write, events and exec")
}

func TestVersionCapabilitiesAndClusterFallback(t *testing.T) {
	fc := &fakeClient{clusterID: "from-client"}
	api := newTestAPIWithConfig(t, fc, Config{
		ServerVersion: "0.11.0",
		GRPCVersion:   "v1",
		Capabilities:  []string{"read", "write", "events", "exec"},
	})

	rec := api.do(t, http.MethodGet, "/v1/version", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code)

	var info v1.VersionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Equal(t, []string{"read", "write", "events", "exec"}, info.Capabilities)
	assert.Equal(t, "from-client", info.ClusterID, "empty config cluster ID falls back to the client")
}

func TestVersionClusterFallbackToleratesClientError(t *testing.T) {
	fc := &fakeClient{clusterErr: output.NewError(output.CodeUnavailable, "no leader")}
	api := newTestAPIWithConfig(t, fc, Config{ServerVersion: "0.11.0", GRPCVersion: "v1"})

	rec := api.do(t, http.MethodGet, "/v1/version", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var info v1.VersionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Empty(t, info.ClusterID)
	assert.Equal(t, []string{"read", "write", "events", "exec"}, info.Capabilities)
}

func TestEmptyServiceListIsExactlyEmpty(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})

	rec := api.do(t, http.MethodGet, "/v1/services", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code)
	assertWireHeaders(t, rec)

	require.JSONEq(t, `{
		"schemaVersion": 1,
		"kind": "ServiceList",
		"items": [],
		"next": null
	}`, rec.Body.String())
}

func TestAuth(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})

	t.Run("valid reader token reads", func(t *testing.T) {
		rec := api.do(t, http.MethodGet, "/v1/services", withToken(api.token))
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("admin token reads", func(t *testing.T) {
		admin, _, err := api.store.Create("admin", RoleAdmin)
		require.NoError(t, err)

		rec := api.do(t, http.MethodGet, "/v1/services", withToken(admin))
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("missing token is 401", func(t *testing.T) {
		rec := api.do(t, http.MethodGet, "/v1/services")
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
		assertWireHeaders(t, rec)

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeUnauthenticated), doc.Error.Code)
	})

	t.Run("bad token is 401", func(t *testing.T) {
		rec := api.do(t, http.MethodGet, "/v1/services", withToken("sc_nope"))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeUnauthenticated), doc.Error.Code)
	})

	t.Run("wrong scheme is 401", func(t *testing.T) {
		rec := api.do(t, http.MethodGet, "/v1/services", withHeader("Authorization", "Basic "+api.token))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("missing token is 401 even with an unsupported Accept", func(t *testing.T) {
		rec := api.do(t, http.MethodGet, "/v1/services", withHeader("Accept", "text/html"))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, "Bearer", rec.Header().Get("WWW-Authenticate"))
	})

	t.Run("unix handler needs no token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/services", nil)
		req.Header.Set("Accept", MediaType)
		rec := httptest.NewRecorder()
		api.unix.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}

func TestPaginationOverHTTP(t *testing.T) {
	fc := &fakeClient{services: []v1.Service{
		{ID: "svc-1", Name: "one", CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "svc-2", Name: "two", CreatedAt: "2026-01-02T00:00:00Z"},
		{ID: "svc-3", Name: "three", CreatedAt: "2026-01-03T00:00:00Z"},
	}}
	api := newTestAPI(t, fc)

	rec := api.do(t, http.MethodGet, "/v1/services?limit=2", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var first v1.ServiceList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &first))
	require.Len(t, first.Items, 2)
	assert.Equal(t, "svc-1", first.Items[0].ID)
	assert.Equal(t, "svc-2", first.Items[1].ID)
	require.NotNil(t, first.Next)

	second := api.do(t, http.MethodGet,
		"/v1/services?limit=2&cursor="+url.QueryEscape(*first.Next), withToken(api.token))
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())

	var page v1.ServiceList
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	assert.Equal(t, "svc-3", page.Items[0].ID)
	assert.Nil(t, page.Next)

	for _, target := range []string{
		"/v1/services?limit=5000",
		"/v1/services?limit=0",
		"/v1/services?limit=-1",
		"/v1/services?limit=abc",
		"/v1/services?limit=",
	} {
		bad := api.do(t, http.MethodGet, target, withToken(api.token))
		require.Equal(t, http.StatusBadRequest, bad.Code, target)

		doc := decodeError(t, bad)
		assert.Equal(t, string(output.CodeInvalid), doc.Error.Code, target)
		fields, ok := doc.Error.Details["fields"].(map[string]any)
		require.True(t, ok, target)
		assert.Contains(t, fields, "limit", target)
	}
}

func TestMalformedCursorIsInvalid(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})

	rec := api.do(t, http.MethodGet, "/v1/services?cursor=not-a-cursor", withToken(api.token))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	assert.Contains(t, doc.Error.Details, "cursor")

	// A query string that does not parse at all is a bad request too, rather
	// than being silently treated as "no parameters".
	broken := api.do(t, http.MethodGet, "/v1/services?cursor=%%%", withToken(api.token))
	require.Equal(t, http.StatusBadRequest, broken.Code)
	assert.Equal(t, string(output.CodeInvalid), decodeError(t, broken).Error.Code)
}

func TestLabelFilter(t *testing.T) {
	fc := &fakeClient{services: []v1.Service{
		{ID: "svc-1", Name: "one", CreatedAt: "2026-01-01T00:00:00Z", Labels: map[string]string{"team": "payments"}},
		{ID: "svc-2", Name: "two", CreatedAt: "2026-01-02T00:00:00Z", Labels: map[string]string{"team": "search"}},
		{ID: "svc-3", Name: "three", CreatedAt: "2026-01-03T00:00:00Z"},
	}}
	api := newTestAPI(t, fc)

	t.Run("key=value", func(t *testing.T) {
		rec := api.do(t, http.MethodGet, "/v1/services?label=team%3Dpayments", withToken(api.token))
		require.Equal(t, http.StatusOK, rec.Code)

		var list v1.ServiceList
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		require.Len(t, list.Items, 1)
		assert.Equal(t, "svc-1", list.Items[0].ID)
	})

	t.Run("key only", func(t *testing.T) {
		rec := api.do(t, http.MethodGet, "/v1/services?label=team", withToken(api.token))
		require.Equal(t, http.StatusOK, rec.Code)

		var list v1.ServiceList
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		require.Len(t, list.Items, 2)
	})

	t.Run("unknown value is an empty list", func(t *testing.T) {
		rec := api.do(t, http.MethodGet, "/v1/services?label=team%3Dnope", withToken(api.token))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"items":[]`)

		var list v1.ServiceList
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		assert.Empty(t, list.Items)
		assert.Nil(t, list.Next)
	})

	t.Run("unknown key is an empty list", func(t *testing.T) {
		rec := api.do(t, http.MethodGet, "/v1/services?label=missing%3Dx", withToken(api.token))
		require.Equal(t, http.StatusOK, rec.Code)

		var list v1.ServiceList
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		assert.Empty(t, list.Items)
	})
}

func TestLabelFilterRepeatedParamsAreANDed(t *testing.T) {
	fc := &fakeClient{services: []v1.Service{
		{ID: "svc-1", CreatedAt: "2026-01-01T00:00:00Z", Labels: map[string]string{"team": "payments", "tier": "gold"}},
		{ID: "svc-2", CreatedAt: "2026-01-02T00:00:00Z", Labels: map[string]string{"team": "payments", "tier": "silver"}},
		{ID: "svc-3", CreatedAt: "2026-01-03T00:00:00Z", Labels: map[string]string{"team": "search", "tier": "gold"}},
	}}
	api := newTestAPI(t, fc)

	rec := api.do(t, http.MethodGet,
		"/v1/services?label=team%3Dpayments&label=tier%3Dgold", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var list v1.ServiceList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Items, 1, "both selectors must match (logical AND)")
	assert.Equal(t, "svc-1", list.Items[0].ID)
}

func TestETagAndIfNoneMatch(t *testing.T) {
	api := newTestAPI(t, &fakeClient{services: []v1.Service{
		{ID: "svc-1", Name: "one", CreatedAt: "2026-01-01T00:00:00Z"},
	}})

	first := api.do(t, http.MethodGet, "/v1/services", withToken(api.token))
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.Regexp(t, `^"[0-9a-f]{64}"$`, etag)

	again := api.do(t, http.MethodGet, "/v1/services", withToken(api.token))
	assert.Equal(t, etag, again.Header().Get("ETag"), "identical body yields the same ETag")

	notModified := api.do(t, http.MethodGet, "/v1/services",
		withToken(api.token), withHeader("If-None-Match", etag))
	require.Equal(t, http.StatusNotModified, notModified.Code)
	assert.Empty(t, notModified.Body.String())
	assert.Equal(t, etag, notModified.Header().Get("ETag"))
	assert.Equal(t, APIVersion, notModified.Header().Get(apiVersionHeader))

	// A stale validator returns the full body.
	full := api.do(t, http.MethodGet, "/v1/services",
		withToken(api.token), withHeader("If-None-Match", `"stale"`))
	assert.Equal(t, http.StatusOK, full.Code)
	assert.NotEmpty(t, full.Body.String())
}

func TestGetNotFoundMapsTo404(t *testing.T) {
	fc := &fakeClient{err: output.NewError(output.CodeNotFound, "service nope not found")}
	api := newTestAPI(t, fc)

	rec := api.do(t, http.MethodGet, "/v1/services/nope", withToken(api.token))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assertWireHeaders(t, rec)
	assert.Equal(t, "nope", fc.gotServiceID)

	doc := decodeError(t, rec)
	assert.Equal(t, v1.KindError, doc.Kind)
	assert.Equal(t, string(output.CodeNotFound), doc.Error.Code)
	assert.False(t, doc.Error.Retryable)
	assert.NotEmpty(t, doc.Error.RequestID)
}

func TestGRPCErrorMapping(t *testing.T) {
	fc := &fakeClient{err: status.Error(codes.NotFound, "task gone")}
	api := newTestAPI(t, fc)

	rec := api.do(t, http.MethodGet, "/v1/tasks/t-1", withToken(api.token))
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, string(output.CodeNotFound), decodeError(t, rec).Error.Code)

	fc.err = status.Error(codes.Unavailable, "agent down")
	rec = api.do(t, http.MethodGet, "/v1/nodes/n-1", withToken(api.token))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
	assert.True(t, doc.Error.Retryable)
}

func TestUnreachableMapsTo503(t *testing.T) {
	fc := &fakeClient{err: output.NewError(output.CodeUnreachable, "dial manager: connection refused")}
	api := newTestAPI(t, fc)

	rec := api.do(t, http.MethodGet, "/v1/services", withToken(api.token))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assertWireHeaders(t, rec)

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeUnreachable), doc.Error.Code)
	assert.True(t, doc.Error.Retryable)
}

func TestUnknownRouteIsTypedNotFound(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})

	rec := api.do(t, http.MethodGet, "/v1/widgets", withToken(api.token))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assertWireHeaders(t, rec)

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeNotFound), doc.Error.Code)
	assert.Equal(t, "route", doc.Error.Details["resource"])
	assert.Contains(t, doc.Error.Message, "/v1/widgets")
}

func TestNonCanonicalPathsStayV1Documents(t *testing.T) {
	fc := &fakeClient{
		services: []v1.Service{{ID: "svc-1", CreatedAt: "2026-01-01T00:00:00Z"}},
		secrets:  []v1.Secret{{ID: "sec-1"}},
	}
	api := newTestAPI(t, fc)

	// ServeMux would normally answer each of these with its own text/html 307.
	cases := []struct{ raw, kind string }{
		{"//v1/services", v1.KindServiceList},
		{"/v1//services", v1.KindServiceList},
		{"/v1/services/../secrets", v1.KindSecretList},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			rec := api.doRawPath(t, http.MethodGet, tc.raw, withToken(api.token))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assertWireHeaders(t, rec)
			assert.Empty(t, rec.Header().Get("Location"), "must not fall through to a ServeMux redirect")

			var doc struct {
				Kind string `json:"kind"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
			assert.Equal(t, tc.kind, doc.Kind)
		})
	}

	t.Run("normalized unknown path is a typed 404", func(t *testing.T) {
		rec := api.doRawPath(t, http.MethodGet, "//v1/widgets", withToken(api.token))
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		assertWireHeaders(t, rec)
		assert.Empty(t, rec.Header().Get("Location"))

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeNotFound), doc.Error.Code)
		assert.Equal(t, "route", doc.Error.Details["resource"])
	})

	t.Run("trailing slash stays a typed 404", func(t *testing.T) {
		rec := api.doRawPath(t, http.MethodGet, "/v1/services/", withToken(api.token))
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		assertWireHeaders(t, rec)
		assert.Empty(t, rec.Header().Get("Location"))
	})
}

func TestUnsupportedAcceptIs406(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})

	for _, accept := range []string{
		"application/vnd.swarmcracker.v2+json",
		"text/html",
		"application/vnd.swarmcracker.v1+xml",
	} {
		rec := api.do(t, http.MethodGet, "/v1/version",
			withToken(api.token), withHeader("Accept", accept))
		require.Equal(t, http.StatusNotAcceptable, rec.Code, accept)
		assertWireHeaders(t, rec)

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeUnsupportedVersion), doc.Error.Code, accept)
		assert.Equal(t, []any{"v1"}, doc.Error.Details["supported"], accept)
	}

	for _, accept := range []string{"application/json", "*/*", MediaType, MediaType + "; q=0.9"} {
		rec := api.do(t, http.MethodGet, "/v1/version",
			withToken(api.token), withHeader("Accept", accept))
		assert.Equal(t, http.StatusOK, rec.Code, accept)
	}
}

func TestUnixHandlerRejectsUnsupportedAccept(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})

	req := httptest.NewRequest(http.MethodGet, "/v1/version", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	api.unix.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotAcceptable, rec.Code, rec.Body.String())
	assertWireHeaders(t, rec)

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeUnsupportedVersion), doc.Error.Code)
	assert.Equal(t, []any{"v1"}, doc.Error.Details["supported"])
}

func TestRequestIDIsStableWithinResponse(t *testing.T) {
	api := newTestAPI(t, &fakeClient{err: output.NewError(output.CodeNotFound, "gone")})

	rec := api.do(t, http.MethodGet, "/v1/configs/nope", withToken(api.token))
	require.Equal(t, http.StatusNotFound, rec.Code)

	doc := decodeError(t, rec)
	id := rec.Header().Get("X-Request-Id")
	require.Regexp(t, `^req_[0-9a-f]{12}$`, id)
	assert.Equal(t, id, doc.Error.RequestID)

	ok := api.do(t, http.MethodGet, "/v1/version", withToken(api.token))
	assert.Regexp(t, `^req_[0-9a-f]{12}$`, ok.Header().Get("X-Request-Id"))
}

func TestSuccessAndErrorHeaders(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})

	ok := api.do(t, http.MethodGet, "/v1/networks", withToken(api.token))
	require.Equal(t, http.StatusOK, ok.Code)
	assertWireHeaders(t, ok)

	bad := api.do(t, http.MethodGet, "/v1/networks?limit=nope", withToken(api.token))
	require.Equal(t, http.StatusBadRequest, bad.Code)
	assertWireHeaders(t, bad)
}

func TestSingleResourceDocuments(t *testing.T) {
	fc := &fakeClient{
		service: v1.Service{ID: "svc-1", Name: "web"},
		task:    v1.Task{ID: "task-1", ServiceID: "svc-1"},
		node:    v1.Node{ID: "node-1", Hostname: "mgr"},
		network: v1.Network{ID: "net-1", Name: "frontend"},
		config:  v1.Config{ID: "cfg-1", Name: "settings"},
		secret:  v1.Secret{ID: "sec-1", Name: "password"},
	}
	api := newTestAPI(t, fc)

	cases := []struct {
		target string
		kind   string
		id     string
	}{
		{"/v1/services/svc-1", v1.KindService, "svc-1"},
		{"/v1/tasks/task-1", v1.KindTask, "task-1"},
		{"/v1/nodes/node-1", v1.KindNode, "node-1"},
		{"/v1/networks/net-1", v1.KindNetwork, "net-1"},
		{"/v1/configs/cfg-1", v1.KindConfig, "cfg-1"},
		{"/v1/secrets/sec-1", v1.KindSecret, "sec-1"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			rec := api.do(t, http.MethodGet, tc.target, withToken(api.token))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			var doc struct {
				SchemaVersion int    `json:"schemaVersion"`
				Kind          string `json:"kind"`
				ID            string `json:"id"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
			assert.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
			assert.Equal(t, tc.kind, doc.Kind)
			assert.Equal(t, tc.id, doc.ID)
		})
	}
}

func TestListDocuments(t *testing.T) {
	fc := &fakeClient{
		tasks:    []v1.Task{{ID: "t-1"}},
		nodes:    []v1.Node{{ID: "n-1"}},
		networks: []v1.Network{{ID: "net-1"}},
		configs:  []v1.Config{{ID: "cfg-1"}},
		secrets:  []v1.Secret{{ID: "sec-1"}},
	}
	api := newTestAPI(t, fc)

	cases := []struct {
		target string
		kind   string
	}{
		{"/v1/tasks", v1.KindTaskList},
		{"/v1/nodes", v1.KindNodeList},
		{"/v1/networks", v1.KindNetworkList},
		{"/v1/configs", v1.KindConfigList},
		{"/v1/secrets", v1.KindSecretList},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			rec := api.do(t, http.MethodGet, tc.target, withToken(api.token))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			var doc struct {
				SchemaVersion int             `json:"schemaVersion"`
				Kind          string          `json:"kind"`
				Items         json.RawMessage `json:"items"`
				Next          *string         `json:"next"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
			assert.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
			assert.Equal(t, tc.kind, doc.Kind)
			assert.Contains(t, string(doc.Items), `"id"`)
			assert.Nil(t, doc.Next)
		})
	}
}

func TestServiceTasksRoute(t *testing.T) {
	fc := &fakeClient{serviceTasks: []v1.Task{
		{ID: "task-1", ServiceID: "svc-1", CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "task-2", ServiceID: "svc-1", CreatedAt: "2026-01-02T00:00:00Z"},
	}}
	api := newTestAPI(t, fc)

	rec := api.do(t, http.MethodGet, "/v1/services/svc-1/tasks?limit=1", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "svc-1", fc.gotServiceID)

	var list v1.TaskList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Equal(t, v1.KindTaskList, list.Kind)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "task-1", list.Items[0].ID)
	require.NotNil(t, list.Next)
}

func TestMethodNotAllowedIsTyped(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})

	// PUT is not part of the service surface; POST is (create). A method the
	// route does not serve stays a 405 and names what it does serve.
	rec := api.do(t, http.MethodPut, "/v1/services", withToken(api.token))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	assert.Equal(t, "GET, POST", rec.Header().Get("Allow"))
	assertWireHeaders(t, rec)

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	assert.Equal(t, "method_not_allowed", doc.Error.Details["reason"],
		"a 405 must name the plumbing in details.reason (contract §4.2)")
}

func TestHeadOnListRouteIs405(t *testing.T) {
	api := newTestAPI(t, &fakeClient{services: []v1.Service{{ID: "svc-1"}}})

	rec := api.do(t, http.MethodHead, "/v1/services", withToken(api.token))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	assert.Equal(t, "GET, POST", rec.Header().Get("Allow"))
	assertWireHeaders(t, rec)

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
}

// TestUnknownRoleTokenFileIsRejected pins the fail-closed load rule: a
// hand-edited role with no scope set must make the store unreadable rather than
// authorize anything. Create rejects an unknown role up front; the load path is
// the defence for a file edited outside the CLI.
func TestUnknownRoleTokenFileIsRejected(t *testing.T) {
	const secret = "sc_unknownrole"

	tokenPath := filepath.Join(t.TempDir(), "tokens.json")
	writeTokenFile(t, tokenPath, tokenFile{
		SchemaVersion: schemaVersion,
		Tokens: []diskToken{{
			ID:        "tok_00000000",
			Name:      "auditor",
			Role:      "auditor",
			Hash:      hashSecret(secret),
			CreatedAt: "2026-01-01T00:00:00Z",
		}},
	})

	store, err := OpenTokenStore(tokenPath)
	require.Error(t, err)
	assert.Nil(t, store)

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeInvalid, typed.Code)
	assert.Contains(t, typed.Message, "auditor")
}

func TestNewValidatesConfig(t *testing.T) {
	_, err := New(Config{})
	require.Error(t, err)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeInvalid, typed.Code)
}

func TestErrorStatusMapping(t *testing.T) {
	cases := []struct {
		code output.Code
		want int
	}{
		{output.CodeInvalid, http.StatusBadRequest},
		{output.CodeAmbiguous, http.StatusBadRequest},
		{output.CodeCursorExpired, http.StatusGone},
		{output.CodeNotFound, http.StatusNotFound},
		{output.CodeUnauthenticated, http.StatusUnauthorized},
		{output.CodeForbidden, http.StatusForbidden},
		{output.CodeConflict, http.StatusConflict},
		{output.CodeRateLimited, http.StatusTooManyRequests},
		{output.CodeUnavailable, http.StatusServiceUnavailable},
		{output.CodeUnreachable, http.StatusServiceUnavailable},
		{output.CodeTimeout, http.StatusGatewayTimeout},
		{output.CodeNotImplemented, http.StatusNotImplemented},
		{output.CodeUnsupportedVersion, http.StatusNotAcceptable},
		{output.CodeInternal, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			assert.Equal(t, tc.want, statusForCode(tc.code))
		})
	}
}

func TestErrorMessagesAreNotJSONHTML(t *testing.T) {
	api := newTestAPI(t, &fakeClient{err: output.NewError(output.CodeInternal, "<boom> & crack")})

	rec := api.do(t, http.MethodGet, "/v1/tasks/t-1", withToken(api.token))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.False(t, strings.Contains(rec.Body.String(), `\u003c`), rec.Body.String())
}

// mint creates a token with options and returns its secret.
func (a *testAPI) mint(t *testing.T, name, role string, opts TokenOptions) string {
	t.Helper()
	secret, _, err := a.store.CreateWithOptions(name, role, opts)
	require.NoError(t, err)
	return secret
}

// TestReaderReadsEveryPhase1Route pins the default role against the whole phase-1
// surface: if a route is added without a reachable role, this fails.
func TestReaderReadsEveryPhase1Route(t *testing.T) {
	fc := &fakeClient{
		services:     []v1.Service{{ID: "svc-1", CreatedAt: "2026-01-01T00:00:00Z"}},
		service:      v1.Service{ID: "svc-1"},
		serviceTasks: []v1.Task{{ID: "task-1"}},
		tasks:        []v1.Task{{ID: "task-1"}},
		task:         v1.Task{ID: "task-1"},
		nodes:        []v1.Node{{ID: "node-1"}},
		node:         v1.Node{ID: "node-1"},
		networks:     []v1.Network{{ID: "net-1"}},
		network:      v1.Network{ID: "net-1"},
		configs:      []v1.Config{{ID: "cfg-1"}},
		config:       v1.Config{ID: "cfg-1"},
		secrets:      []v1.Secret{{ID: "sec-1"}},
		secret:       v1.Secret{ID: "sec-1"},
	}
	api := newTestAPI(t, fc)

	for _, target := range []string{
		"/v1/version",
		"/v1/services",
		"/v1/services/svc-1",
		"/v1/services/svc-1/tasks",
		"/v1/tasks",
		"/v1/tasks/task-1",
		"/v1/nodes",
		"/v1/nodes/node-1",
		"/v1/networks",
		"/v1/networks/net-1",
		"/v1/configs",
		"/v1/configs/cfg-1",
		"/v1/secrets",
		"/v1/secrets/sec-1",
	} {
		t.Run(target, func(t *testing.T) {
			rec := api.do(t, http.MethodGet, target, withToken(api.token))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assertWireHeaders(t, rec)
		})
	}
}

// TestNarrowedTokenIsForbidden pins scope enforcement: a token may only reach the
// routes its own scope set covers.
func TestNarrowedTokenIsForbidden(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})

	narrowed := api.mint(t, "services-only", RoleReader, TokenOptions{
		Scopes: []string{"services:read"},
	})

	t.Run("granted scope works", func(t *testing.T) {
		rec := api.do(t, http.MethodGet, "/v1/services", withToken(narrowed))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	cases := []struct {
		target string
		scope  string
	}{
		{"/v1/version", "cluster:read"},
		{"/v1/services/svc-1/tasks", "tasks:read"},
		{"/v1/tasks", "tasks:read"},
		{"/v1/nodes", "nodes:read"},
		{"/v1/networks", "networks:read"},
		{"/v1/configs", "configs:read"},
		{"/v1/secrets", "secrets:read"},
		{"/v1/audit", "audit:read"},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			rec := api.do(t, http.MethodGet, tc.target, withToken(narrowed))
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assertWireHeaders(t, rec)
			assert.Empty(t, rec.Header().Get("WWW-Authenticate"),
				"an authenticated token that lacks a scope is not prompted to authenticate")

			doc := decodeError(t, rec)
			assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
			assert.Equal(t, tc.scope, doc.Error.Details["requiredScope"])
		})
	}
}

// TestRunnerScopeSetReachesTaskRoutes documents what the runner role unlocks
// once the phase-3 routes exist: its scope set is what those handlers check.
func TestRunnerScopeSetReachesTaskRoutes(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})

	runner := api.mint(t, "ci", RoleRunner, TokenOptions{
		TaskSelectors: &TaskSelectors{ServiceIDs: []string{"svc-1"}},
	})

	rec := api.do(t, http.MethodGet, "/v1/tasks", withToken(runner))
	assert.Equal(t, http.StatusOK, rec.Code, "a runner keeps every reader scope")

	operator := api.mint(t, "ops", RoleOperator, TokenOptions{})
	rec = api.do(t, http.MethodGet, "/v1/audit", withToken(operator))
	assert.Equal(t, http.StatusForbidden, rec.Code, "operator does not hold audit:read")

	admin := api.mint(t, "root", RoleAdmin, TokenOptions{})
	rec = api.do(t, http.MethodGet, "/v1/audit", withToken(admin))
	assert.Equal(t, http.StatusNotImplemented, rec.Code, "no audit store is configured in this test")
}

func TestAuditRoute(t *testing.T) {
	audit, err := OpenAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"), 0)
	require.NoError(t, err)

	api := newTestAPIWithConfig(t, &fakeClient{}, Config{
		ServerVersion: "0.11.0",
		GRPCVersion:   "v1",
		Audit:         audit,
	})

	t.Run("reader is forbidden", func(t *testing.T) {
		rec := api.do(t, http.MethodGet, "/v1/audit", withToken(api.token))
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

		doc := decodeError(t, rec)
		assert.Equal(t, "audit:read", doc.Error.Details["requiredScope"])
	})

	t.Run("admin reads an empty list", func(t *testing.T) {
		admin := api.mint(t, "root", RoleAdmin, TokenOptions{})

		rec := api.do(t, http.MethodGet, "/v1/audit", withToken(admin))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assertWireHeaders(t, rec)

		var doc struct {
			SchemaVersion int             `json:"schemaVersion"`
			Kind          string          `json:"kind"`
			Items         json.RawMessage `json:"items"`
			Next          *string         `json:"next"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
		assert.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
		assert.Equal(t, v1.KindAuditList, doc.Kind)
		assert.Equal(t, "[]", string(doc.Items), "an empty audit log is [] and never null")
		assert.Nil(t, doc.Next)
	})

	t.Run("unix handler reads it too", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/audit", nil)
		req.Header.Set("Accept", MediaType)
		rec := httptest.NewRecorder()
		api.unix.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("limit is validated", func(t *testing.T) {
		admin := api.mint(t, "root-2", RoleAdmin, TokenOptions{})
		rec := api.do(t, http.MethodGet, "/v1/audit?limit=5000", withToken(admin))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, string(output.CodeInvalid), decodeError(t, rec).Error.Code)
	})
}

// TestAuditRouteWithoutStoreIs501 keeps a manager that never configured the log
// from pretending it has one.
func TestAuditRouteWithoutStoreIs501(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})
	admin := api.mint(t, "root", RoleAdmin, TokenOptions{})

	rec := api.do(t, http.MethodGet, "/v1/audit", withToken(admin))
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeNotImplemented), doc.Error.Code)
	assert.Equal(t, "audit", doc.Error.Details["capability"])
}

// TestConfigValuesRequireReadValuesScope pins the one payload-bearing field: a
// plain configs:read token never receives data, on either config route.
func TestConfigValuesRequireReadValuesScope(t *testing.T) {
	payload := "listen 8080"
	fc := &fakeClient{
		config:  v1.Config{ID: "cfg-1", Data: &payload},
		configs: []v1.Config{{ID: "cfg-1", CreatedAt: "2026-01-01T00:00:00Z", Data: &payload}},
	}
	api := newTestAPI(t, fc)

	t.Run("reader gets metadata only", func(t *testing.T) {
		for _, target := range []string{"/v1/configs/cfg-1", "/v1/configs"} {
			rec := api.do(t, http.MethodGet, target, withToken(api.token))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), payload, target)
			assert.NotContains(t, rec.Body.String(), `"data"`, target)
		}
	})

	t.Run("operator with read-values gets the payload", func(t *testing.T) {
		operator := api.mint(t, "ops", RoleOperator, TokenOptions{})

		rec := api.do(t, http.MethodGet, "/v1/configs/cfg-1", withToken(operator))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"data":"`+payload+`"`)

		rec = api.do(t, http.MethodGet, "/v1/configs", withToken(operator))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"data":"`+payload+`"`)
	})

	t.Run("the client's slice is not mutated", func(t *testing.T) {
		require.NotNil(t, fc.configs[0].Data, "redaction must copy, not edit the client's data")
	})
}

// TestMethodRejectionIsIndependentOfScope keeps an unscoped 405 working: method
// support is a route property, not an authorization decision.
func TestMethodRejectionForNarrowToken(t *testing.T) {
	api := newTestAPI(t, &fakeClient{})
	narrowed := api.mint(t, "services-only", RoleReader, TokenOptions{
		Scopes: []string{"services:read"},
	})

	rec := api.do(t, http.MethodPost, "/v1/nodes", withToken(narrowed))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	assert.Equal(t, "GET", rec.Header().Get("Allow"))
}
