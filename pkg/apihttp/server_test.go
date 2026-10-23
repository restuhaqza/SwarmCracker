package apihttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	assert.Equal(t, []string{"read"}, info.Capabilities, "nil capabilities default to read")
}

func TestVersionCapabilitiesAndClusterFallback(t *testing.T) {
	fc := &fakeClient{clusterID: "from-client"}
	api := newTestAPIWithConfig(t, fc, Config{
		ServerVersion: "0.11.0",
		GRPCVersion:   "v1",
		Capabilities:  []string{"read", "write"},
	})

	rec := api.do(t, http.MethodGet, "/v1/version", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code)

	var info v1.VersionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Equal(t, []string{"read", "write"}, info.Capabilities)
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
	assert.Equal(t, []string{"read"}, info.Capabilities)
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

	rec := api.do(t, http.MethodPost, "/v1/services", withToken(api.token))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	assert.Equal(t, "GET", rec.Header().Get("Allow"))
	assertWireHeaders(t, rec)

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
}

func TestHeadOnListRouteIs405(t *testing.T) {
	api := newTestAPI(t, &fakeClient{services: []v1.Service{{ID: "svc-1"}}})

	rec := api.do(t, http.MethodHead, "/v1/services", withToken(api.token))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	assert.Equal(t, "GET", rec.Header().Get("Allow"))
	assertWireHeaders(t, rec)

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
}

func TestUnknownRoleTokenIsForbidden(t *testing.T) {
	const secret = "sc_unknownrole"

	// Write the token file directly: TokenStore.Create rejects unknown roles, so
	// this is the hand-edited-file case the authentication defence covers.
	tokenPath := filepath.Join(t.TempDir(), "tokens.json")
	file := tokenFile{
		SchemaVersion: schemaVersion,
		Tokens: []diskToken{{
			ID:        "tok_00000000",
			Name:      "auditor",
			Role:      "auditor",
			Hash:      hashSecret(secret),
			CreatedAt: "2026-01-01T00:00:00Z",
		}},
	}
	data, err := json.Marshal(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(tokenPath, data, tokenFileMode))

	store, err := OpenTokenStore(tokenPath)
	require.NoError(t, err)

	srv, err := New(Config{
		Client:        &fakeClient{},
		Tokens:        store,
		ServerVersion: "0.11.0",
		GRPCVersion:   "v1",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/v1/version", nil)
	req.Header.Set("Accept", MediaType)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assertWireHeaders(t, rec)

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
	assert.Contains(t, doc.Error.Message, "auditor")
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
		{output.CodeCursorExpired, http.StatusBadRequest},
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
