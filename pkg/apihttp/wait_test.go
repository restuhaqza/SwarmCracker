package apihttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseWait(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantWait    bool
		wantTimeout time.Duration
		wantField   string
	}{
		{name: "absent", query: ""},
		{name: "converged uses the default", query: "wait=converged", wantWait: true, wantTimeout: 60 * time.Second},
		{name: "explicit timeout", query: "wait=converged&timeoutMs=1500", wantWait: true, wantTimeout: 1500 * time.Millisecond},
		{name: "max timeout", query: "wait=converged&timeoutMs=600000", wantWait: true, wantTimeout: 600 * time.Second},
		{name: "bad wait value", query: "wait=true", wantField: "wait"},
		{name: "empty wait value", query: "wait=", wantField: "wait"},
		{name: "timeout without wait", query: "timeoutMs=100", wantField: "timeoutMs"},
		{name: "zero timeout", query: "wait=converged&timeoutMs=0", wantField: "timeoutMs"},
		{name: "negative timeout", query: "wait=converged&timeoutMs=-1", wantField: "timeoutMs"},
		{name: "over the max", query: "wait=converged&timeoutMs=600001", wantField: "timeoutMs"},
		{name: "not an integer", query: "wait=converged&timeoutMs=1.5", wantField: "timeoutMs"},
		{name: "not a number", query: "wait=converged&timeoutMs=soon", wantField: "timeoutMs"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			query, err := url.ParseQuery(tc.query)
			require.NoError(t, err)

			wait, timeout, err := parseWait(query)
			if tc.wantField != "" {
				requireField(t, err, tc.wantField)
				assert.False(t, wait)
				assert.Zero(t, timeout)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantWait, wait)
			assert.Equal(t, tc.wantTimeout, timeout)
		})
	}
}

func TestParseDryRun(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		want      bool
		wantField string
	}{
		{name: "absent", query: ""},
		{name: "true", query: "dryRun=true", want: true},
		{name: "false", query: "dryRun=false"},
		{name: "capitalised is not a JSON boolean", query: "dryRun=True", wantField: "dryRun"},
		{name: "one is not a JSON boolean", query: "dryRun=1", wantField: "dryRun"},
		{name: "empty", query: "dryRun=", wantField: "dryRun"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			query, err := url.ParseQuery(tc.query)
			require.NoError(t, err)

			dryRun, err := parseDryRun(query)
			if tc.wantField != "" {
				requireField(t, err, tc.wantField)
				assert.False(t, dryRun)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, dryRun)
		})
	}
}

// scriptedServiceClient scripts GetService so a wait test can decide when the
// service converged. Every other method is the shared fake.
type scriptedServiceClient struct {
	*fakeClient
	mu     sync.Mutex
	docs   []v1.Service
	failAt int // 1-based GetService call that fails; 0 never fails
	calls  int
}

func (c *scriptedServiceClient) GetService(_ context.Context, id string) (v1.Service, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.gotServiceID = id
	c.calls++
	if c.failAt == c.calls {
		return v1.Service{}, errors.New("manager went away")
	}
	if len(c.docs) == 0 {
		return v1.Service{}, nil
	}
	index := c.calls - 1
	if index >= len(c.docs) {
		index = len(c.docs) - 1
	}
	return c.docs[index], nil
}

func (c *scriptedServiceClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func serviceAt(revision string, converged bool) v1.Service {
	return v1.Service{
		ID: "svc-1", Name: "web", Revision: revision,
		Spec:   v1.ServiceSpec{Mode: "replicated", Task: v1.TaskSpec{Image: "nginx:alpine"}},
		Status: v1.ServiceStatus{DesiredTasks: 2, RunningTasks: 1, Converged: converged},
	}
}

func waitTestServer(client Client) *Server {
	return &Server{cfg: Config{Client: client}}
}

func TestWaitForServiceConverges(t *testing.T) {
	client := &scriptedServiceClient{fakeClient: &fakeClient{}, docs: []v1.Service{
		serviceAt("7", false),
		serviceAt("7", false),
		serviceAt("7", true),
	}}
	server := waitTestServer(client)

	document, warning := server.waitForService(context.Background(), "svc-1", 5*time.Second)
	require.Nil(t, warning)
	assert.True(t, document.Status.Converged)
	assert.Equal(t, 3, client.callCount(), "the wait stops as soon as the service converged")
}

func TestWaitForServiceTimeout(t *testing.T) {
	client := &scriptedServiceClient{fakeClient: &fakeClient{}, docs: []v1.Service{
		serviceAt("7", false),
	}}
	server := waitTestServer(client)

	document, warning := server.waitForService(context.Background(), "svc-1", 120*time.Millisecond)
	require.NotNil(t, warning)
	assert.Equal(t, waitTimeoutCode, warning.Code)
	assert.Contains(t, warning.Message, "120ms")
	assert.False(t, document.Status.Converged, "a timeout reports the last document as not converged")
	assert.Equal(t, "svc-1", document.ID)
}

func TestWaitForServiceReadError(t *testing.T) {
	client := &scriptedServiceClient{
		fakeClient: &fakeClient{},
		docs:       []v1.Service{serviceAt("7", false)},
		failAt:     1,
	}
	server := waitTestServer(client)

	document, warning := server.waitForService(context.Background(), "svc-1", time.Second)
	require.NotNil(t, warning)
	assert.Equal(t, waitErrorCode, warning.Code)
	assert.Contains(t, warning.Message, "manager went away")
	assert.Empty(t, document.ID, "a read error before the first good document returns the zero document")
	assert.Equal(t, 1, client.callCount())
}

// TestWaitOnNonServiceWriteIsInvalid proves wait is a service capability, not a
// generic parameter that other writes silently ignore.
func TestWaitOnNonServiceWriteIsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		body   []byte
		want   *fakeClient
		calls  func(*fakeClient) int
	}{
		{
			name: "config create", method: http.MethodPost, target: "/v1/configs?wait=converged",
			body:  []byte(`{"name":"app"}`),
			calls: func(f *fakeClient) int { return f.configCreates },
		},
		{
			name: "secret create", method: http.MethodPost, target: "/v1/secrets?wait=converged",
			body:  []byte(`{"name":"token","data":"aGk="}`),
			calls: func(f *fakeClient) int { return f.secretCreates },
		},
		{
			name: "network create", method: http.MethodPost, target: "/v1/networks?wait=converged",
			body:  []byte(`{"name":"app"}`),
			calls: func(f *fakeClient) int { return f.networkCreates },
		},
		{
			name: "node patch", method: http.MethodPatch, target: "/v1/nodes/node-1?wait=converged",
			body:  []byte(`{"availability":"drain"}`),
			calls: func(f *fakeClient) int { return f.nodeUpdates },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeClient{}
			api := newWriteTestAPI(t, fc)

			opts := []func(*http.Request){
				withToken(api.token),
				withContentType(MediaType),
			}
			if tc.method == http.MethodPatch {
				opts[1] = withContentType(v1.MergePatchMediaType)
				opts = append(opts, withHeader("If-Match", `"1"`))
			}

			rec := api.doWrite(t, tc.method, tc.target, tc.body, opts...)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

			doc := decodeError(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
			fields, ok := doc.Error.Details["fields"].(map[string]any)
			require.True(t, ok, "details.fields missing: %#v", doc.Error.Details)
			assert.Contains(t, fields, "wait")
			assert.Zero(t, tc.calls(fc), "a rejected wait never reaches the client")
			assert.Empty(t, api.records(t))
		})
	}
}

func TestWaitAndDryRunAreMutuallyExclusive(t *testing.T) {
	fc := &fakeClient{}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPost, "/v1/services?dryRun=true&wait=converged",
		[]byte(`{"name":"web","spec":{"task":{"image":"nginx"}}}`),
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	fields, ok := doc.Error.Details["fields"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, fields, "wait")
	assert.Zero(t, fc.createCalls)
}

func TestDryRunOnDeleteIsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{name: "service", target: "/v1/services/web"},
		{name: "config", target: "/v1/configs/app"},
		{name: "secret", target: "/v1/secrets/token"},
		{name: "network", target: "/v1/networks/app"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeClient{}
			api := newWriteTestAPI(t, fc)

			rec := api.doWrite(t, http.MethodDelete, tc.target+"?dryRun=true", nil,
				withToken(api.token), withHeader("If-Match", `"1"`))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

			doc := decodeError(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
			fields, ok := doc.Error.Details["fields"].(map[string]any)
			require.True(t, ok, "details.fields missing: %#v", doc.Error.Details)
			assert.Contains(t, fields, "dryRun")
			assert.Zero(t, fc.deleteCalls+fc.configDeletes+fc.secretDeletes+fc.networkDeletes)
			assert.Empty(t, api.records(t))
		})
	}
}

func TestDryRunOnRollbackIsNotImplemented(t *testing.T) {
	fc := &fakeClient{}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPost, "/v1/services/web/rollback?dryRun=true", nil,
		withToken(api.operator), withHeader("If-Match", `"18"`))
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeNotImplemented), doc.Error.Code)
	assert.Equal(t, "dryRun", doc.Error.Details["capability"])
	assert.Zero(t, fc.rollbackCalls)
	assert.Empty(t, api.records(t))
}

func TestServiceCreateWaitConverged(t *testing.T) {
	fc := &scriptedServiceClient{
		fakeClient: &fakeClient{createdService: serviceAt("7", false)},
		docs:       []v1.Service{serviceAt("8", false), serviceAt("8", true)},
	}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPost, "/v1/services?wait=converged", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var doc v1.ServiceDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.True(t, doc.Status.Converged, "the response is the converged document")
	assert.Empty(t, doc.Warnings)
	assert.Equal(t, "8", doc.Revision, "the waited revision is the response's validator")
	assert.Equal(t, `"8"`, rec.Header().Get("ETag"))

	assert.Equal(t, "svc-1", fc.gotServiceID, "the wait polls the document the mutation returned")
	assert.Len(t, api.records(t), 1, "a wait that converges audits the mutation once")
}

func TestServiceCreateWaitTimeout(t *testing.T) {
	fc := &scriptedServiceClient{
		fakeClient: &fakeClient{createdService: serviceAt("7", false)},
		docs:       []v1.Service{serviceAt("7", false)},
	}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPost, "/v1/services?wait=converged&timeoutMs=150",
		idempotencyBody("web"), withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusCreated, rec.Code, "a wait timeout is a success, not a 504")
	assert.NotEqual(t, http.StatusGatewayTimeout, rec.Code)

	var doc v1.ServiceDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.False(t, doc.Status.Converged)
	require.Len(t, doc.Warnings, 1)
	assert.Equal(t, waitTimeoutCode, doc.Warnings[0].Code)
	assert.Contains(t, doc.Warnings[0].Message, "150ms")
	assert.Equal(t, "svc-1", doc.ID, "the document survives the timeout")

	assert.Len(t, api.records(t), 1, "a wait timeout still audits the committed mutation")
	assert.Equal(t, "svc-1", api.records(t)[0].Resource.ID)
}

func TestServiceCreateWaitReadErrorKeepsTheDocument(t *testing.T) {
	fc := &scriptedServiceClient{
		fakeClient: &fakeClient{createdService: serviceAt("7", false)},
		failAt:     1,
	}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPost, "/v1/services?wait=converged", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var doc v1.ServiceDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Len(t, doc.Warnings, 1)
	assert.Equal(t, waitErrorCode, doc.Warnings[0].Code)
	assert.Equal(t, "svc-1", doc.ID, "a transient read error must not lose the committed document")
	assert.Equal(t, "7", doc.Revision)
}

func TestServicePatchWaitConverged(t *testing.T) {
	fc := &scriptedServiceClient{
		fakeClient: &fakeClient{updatedService: serviceAt("18", false)},
		docs:       []v1.Service{serviceAt("18", true)},
	}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPatch, "/v1/services/web?wait=converged",
		[]byte(`{"spec":{"replicas":2}}`),
		withToken(api.operator), withContentType(v1.MergePatchMediaType),
		withHeader("If-Match", `"17"`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var doc v1.ServiceDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.True(t, doc.Status.Converged)
	assert.Empty(t, doc.Warnings)
	assert.Len(t, api.records(t), 1)
}

func TestServiceRollbackWait(t *testing.T) {
	fc := &scriptedServiceClient{
		fakeClient: &fakeClient{rolledBackService: serviceAt("19", false)},
		docs:       []v1.Service{serviceAt("19", false)},
	}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPost, "/v1/services/web/rollback?wait=converged&timeoutMs=120",
		nil, withToken(api.operator), withHeader("If-Match", `"18"`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var doc v1.ServiceDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.False(t, doc.Status.Converged)
	require.Len(t, doc.Warnings, 1)
	assert.Equal(t, waitTimeoutCode, doc.Warnings[0].Code)
}

// dryRunAwareClient stands in for pkg/client's dry-run contract at the fake
// boundary: a dry run returns a synthetic document and records no commit. The
// real zero-RPC proof lives in pkg/client's dry-run tests; this type lets the
// HTTP test assert the handler forwards the flag and reports it.
type dryRunAwareClient struct {
	*fakeClient
	commits int
}

func (d *dryRunAwareClient) CreateService(_ context.Context, write v1.ServiceWrite, opts v1.WriteOptions) (v1.Service, error) {
	if opts.DryRun {
		return v1.Service{Name: write.Name, Spec: v1.ServiceSpec{
			Mode: "replicated",
			Task: v1.TaskSpec{Image: write.Spec.Task.Image},
		}}, nil
	}
	d.commits++
	d.gotWrite = write
	d.gotWriteOpts = opts
	return d.createdService, nil
}

func (d *dryRunAwareClient) UpdateService(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Service, error) {
	if opts.DryRun {
		return v1.Service{ID: "svc-1", Name: "web", Revision: "17"}, nil
	}
	d.commits++
	d.gotRef = ref
	d.gotPatch = patch
	d.gotWriteOpts = opts
	return d.updatedService, nil
}

func TestDryRunServiceCreateHTTP(t *testing.T) {
	fc := &dryRunAwareClient{fakeClient: &fakeClient{}}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPost, "/v1/services?dryRun=true",
		[]byte(`{"name":"web","spec":{"task":{"image":"nginx:alpine"}}}`),
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var doc v1.ServiceDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.True(t, doc.DryRun)
	assert.Equal(t, "web", doc.Name)
	assert.Empty(t, doc.ID, "a create preview has no id")
	assert.Empty(t, doc.Revision, "a create preview has no revision")
	assert.Zero(t, fc.commits, "a dry run commits nothing")
	assert.Empty(t, api.records(t), "a dry run is not a mutation and is not audited")
}

func TestDryRunServicePatchHTTP(t *testing.T) {
	fc := &dryRunAwareClient{fakeClient: &fakeClient{}}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPatch, "/v1/services/web?dryRun=true",
		[]byte(`{"spec":{"replicas":2}}`),
		withToken(api.operator), withContentType(v1.MergePatchMediaType),
		withHeader("If-Match", `"17"`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var doc v1.ServiceDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.True(t, doc.DryRun)
	assert.Equal(t, "svc-1", doc.ID)
	assert.Equal(t, "17", doc.Revision, "an update preview carries the current metadata")
	assert.Zero(t, fc.commits)
	assert.Empty(t, api.records(t))
}

func TestDryRunServicePatchStillEvaluatesIfMatch(t *testing.T) {
	fc := &fakeClient{writeErr: output.PreconditionFailed("17", "18")}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPatch, "/v1/services/web?dryRun=true",
		[]byte(`{"spec":{"replicas":2}}`),
		withToken(api.operator), withContentType(v1.MergePatchMediaType),
		withHeader("If-Match", `"17"`))
	require.Equal(t, http.StatusPreconditionFailed, rec.Code, rec.Body.String())
	assert.Empty(t, api.records(t), "a failed dry run is not audited")
}

func TestDryRunNamedCreateAndPatchHTTP(t *testing.T) {
	t.Run("config create", func(t *testing.T) {
		fc := &fakeClient{createdConfig: v1.Config{Name: "app"}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/configs?dryRun=true",
			[]byte(`{"name":"app","data":"listen 8080"}`),
			withToken(api.token), withContentType(MediaType))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		var doc v1.ConfigDocument
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
		assert.True(t, doc.DryRun)
		assert.True(t, fc.gotNamedOpts.DryRun, "the handler forwards dryRun to the client")
		assert.Empty(t, api.records(t))
	})

	t.Run("secret create", func(t *testing.T) {
		fc := &fakeClient{createdSecret: v1.Secret{Name: "token"}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/secrets?dryRun=true",
			[]byte(`{"name":"token","data":"aGk="}`),
			withToken(api.token), withContentType(MediaType))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		var doc v1.SecretDocument
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
		assert.True(t, doc.DryRun)
		assert.True(t, fc.gotNamedOpts.DryRun)
		assert.Empty(t, api.records(t))
	})

	t.Run("network create", func(t *testing.T) {
		fc := &fakeClient{createdNetwork: v1.Network{Name: "app"}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/networks?dryRun=true",
			[]byte(`{"name":"app"}`),
			withToken(api.token), withContentType(MediaType))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

		var doc v1.NetworkDocument
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
		assert.True(t, doc.DryRun)
		assert.True(t, fc.gotNamedOpts.DryRun)
		assert.Empty(t, api.records(t))
	})

	t.Run("node patch", func(t *testing.T) {
		fc := &fakeClient{updatedNode: v1.Node{ID: "node-1", Revision: "9"}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPatch, "/v1/nodes/worker-a?dryRun=true",
			[]byte(`{"availability":"drain"}`),
			withToken(api.token), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", `"9"`))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var doc v1.NodeDocument
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
		assert.True(t, doc.DryRun)
		assert.True(t, fc.gotNamedOpts.DryRun)
		assert.Empty(t, api.records(t))
	})
}
