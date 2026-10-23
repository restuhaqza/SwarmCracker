package apihttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTestAPI is the phase-2 fixture: a real handler over a fake client, a
// token store with every role, and a real audit log.
type writeTestAPI struct {
	*testAPI
	audit    *AuditStore
	operator string
	reader   string
}

func newWriteTestAPI(t *testing.T, client Client) *writeTestAPI {
	t.Helper()
	if client == nil {
		client = &fakeClient{}
	}

	tokenStore, err := OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	require.NoError(t, err)

	admin, _, err := tokenStore.Create("admin", RoleAdmin)
	require.NoError(t, err)
	operator, _, err := tokenStore.Create("deploy", RoleOperator)
	require.NoError(t, err)
	reader, _, err := tokenStore.Create("dashboard", RoleReader)
	require.NoError(t, err)

	audit, err := OpenAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"), 0)
	require.NoError(t, err)

	srv, err := New(Config{
		Client:        client,
		Tokens:        tokenStore,
		Audit:         audit,
		ServerVersion: "0.11.0",
		GRPCVersion:   "v1",
		ClusterID:     "cluster-1",
		RateLimit:     NewRateLimiter(1e9, 1e9),
	})
	require.NoError(t, err)

	return &writeTestAPI{
		testAPI: &testAPI{
			handler: srv.Handler(),
			unix:    srv.UnixHandler(),
			token:   admin,
			store:   tokenStore,
		},
		audit:    audit,
		operator: operator,
		reader:   reader,
	}
}

// bindGuard is fakeClient plus the one Client-contract rule a handler test has
// to observe: a bind mount is refused unless the caller passed AllowBindMounts.
// It lets the HTTP test prove the role → flag → 403 chain end to end without
// importing the real client or duplicating the whole overlay.
type bindGuard struct {
	*fakeClient
}

func bindMountDenied() error {
	return output.NewError(output.CodeForbidden, "bind mounts require the admin role").
		WithDetails(map[string]any{
			"reason":       "bind_mount_requires_admin",
			"requiredRole": "admin",
		})
}

func (b bindGuard) CreateService(ctx context.Context, write v1.ServiceWrite, opts v1.WriteOptions) (v1.Service, error) {
	if !opts.AllowBindMounts && hasBindMount(write.Spec.Task.Mounts) {
		return v1.Service{}, bindMountDenied()
	}
	return b.fakeClient.CreateService(ctx, write, opts)
}

func (b bindGuard) UpdateService(ctx context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Service, error) {
	if !opts.AllowBindMounts {
		var decoded struct {
			Spec struct {
				Task struct {
					Mounts []v1.Mount `json:"mounts"`
				} `json:"task"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(patch, &decoded); err != nil {
			return v1.Service{}, output.NewErrorf(output.CodeInvalid, "test fake: %v", err)
		}
		if hasBindMount(decoded.Spec.Task.Mounts) {
			return v1.Service{}, bindMountDenied()
		}
	}
	return b.fakeClient.UpdateService(ctx, ref, patch, opts)
}

func hasBindMount(mounts []v1.Mount) bool {
	for _, mount := range mounts {
		if mount.Type == "bind" {
			return true
		}
	}
	return false
}

// doWrite sends a request with a body.
func (a *testAPI) doWrite(t *testing.T, method, target string, body []byte, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Accept", MediaType)
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

func withContentType(value string) func(*http.Request) {
	return withHeader("Content-Type", value)
}

func (a *writeTestAPI) records(t *testing.T) []v1.AuditRecord {
	t.Helper()
	records, _, err := a.audit.List(100, "")
	require.NoError(t, err)
	return records
}

func TestCreateServiceHTTP(t *testing.T) {
	fc := &fakeClient{createdService: v1.Service{
		ID: "svc-9", Name: "web", Revision: "7",
		Spec:   v1.ServiceSpec{Mode: "replicated", Task: v1.TaskSpec{Image: "nginx:alpine"}},
		Status: v1.ServiceStatus{DesiredTasks: 2, Converged: false},
	}}
	api := newWriteTestAPI(t, fc)

	body := []byte(`{"name":"web","spec":{"mode":"replicated","replicas":2,` +
		`"task":{"image":"nginx:alpine","registryAuth":"cmVnaXN0cnk="}}}`)

	rec := api.doWrite(t, http.MethodPost, "/v1/services", body,
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assertWireHeaders(t, rec)
	assert.Equal(t, `"7"`, rec.Header().Get("ETag"))

	var doc v1.ServiceDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, v1.KindService, doc.Kind)
	assert.Equal(t, "svc-9", doc.ID)

	// The credential is accepted on the write and never returned.
	assert.Equal(t, "cmVnaXN0cnk=", fc.gotWrite.Spec.Task.RegistryAuth)
	assert.NotContains(t, rec.Body.String(), "registryAuth")
	assert.NotContains(t, rec.Body.String(), "cmVnaXN0cnk=")

	require.Equal(t, 1, fc.createCalls)
	records := api.records(t)
	require.Len(t, records, 1)
	assert.Equal(t, "service.create", records[0].Action)
	assert.Equal(t, "ok", records[0].Result)
	assert.Equal(t, v1.KindService, records[0].Resource.Kind)
	assert.Equal(t, "svc-9", records[0].Resource.ID)
	assert.Equal(t, "web", records[0].Resource.Name)
	assert.Equal(t, "token", records[0].Actor.Type)
	assert.Equal(t, "deploy", records[0].Actor.Name)
}

func TestCreateServiceHTTPConflictAndAudit(t *testing.T) {
	fc := &fakeClient{writeErr: output.NewError(output.CodeConflict, "a service named web already exists")}
	api := newWriteTestAPI(t, fc)

	body := []byte(`{"name":"web","spec":{"task":{"image":"nginx"}}}`)
	rec := api.doWrite(t, http.MethodPost, "/v1/services", body,
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeConflict), doc.Error.Code)

	records := api.records(t)
	require.Len(t, records, 1)
	assert.Equal(t, "service.create", records[0].Action)
	assert.Equal(t, "error", records[0].Result)
	assert.Equal(t, "web", records[0].Resource.Name, "a failed create records the attempted name")
	assert.Empty(t, records[0].Resource.ID)
}

func TestServiceWriteRequiresWriteScope(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		body   []byte
		header func(*http.Request)
	}{
		{
			name: "create", method: http.MethodPost, target: "/v1/services",
			body: []byte(`{"name":"web","spec":{"task":{"image":"nginx"}}}`),
		},
		{
			name: "update", method: http.MethodPatch, target: "/v1/services/svc-1",
			body: []byte(`{"spec":{"replicas":2}}`),
			header: func(r *http.Request) {
				r.Header.Set("Content-Type", v1.MergePatchMediaType)
				r.Header.Set("If-Match", `"1"`)
			},
		},
		{name: "delete", method: http.MethodDelete, target: "/v1/services/svc-1"},
		{
			name: "rollback", method: http.MethodPost, target: "/v1/services/svc-1/rollback",
			header: func(r *http.Request) { r.Header.Set("If-Match", `"1"`) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeClient{}
			api := newWriteTestAPI(t, fc)

			opts := []func(*http.Request){withToken(api.reader)}
			if tc.header != nil {
				opts = append(opts, tc.header)
			}
			rec := api.doWrite(t, tc.method, tc.target, tc.body, opts...)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			doc := decodeError(t, rec)
			assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
			assert.Equal(t, string(ScopeServicesWrite), doc.Error.Details["requiredScope"])

			// No write reached the client and nothing was audited.
			assert.Zero(t, fc.createCalls)
			assert.Zero(t, fc.updateCalls)
			assert.Zero(t, fc.deleteCalls)
			assert.Zero(t, fc.rollbackCalls)
			assert.Empty(t, api.records(t))
		})
	}
}

func TestCreateServiceHTTPMediaType(t *testing.T) {
	api := newWriteTestAPI(t, &fakeClient{})
	body := []byte(`{"name":"web","spec":{"task":{"image":"nginx"}}}`)

	for _, contentType := range []string{"", "application/json", "text/plain"} {
		t.Run("content-type "+contentType, func(t *testing.T) {
			rec := api.doWrite(t, http.MethodPost, "/v1/services", body,
				withToken(api.operator), withContentType(contentType))
			require.Equal(t, http.StatusUnsupportedMediaType, rec.Code, rec.Body.String())

			doc := decodeError(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
			assert.Equal(t, "unsupported_media_type", doc.Error.Details["reason"])
			assert.Empty(t, api.records(t))
		})
	}

	t.Run("media type parameters are accepted", func(t *testing.T) {
		fc := &fakeClient{createdService: v1.Service{ID: "svc-1", Name: "web", Revision: "1"}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/services", body,
			withToken(api.operator), withContentType(MediaType+"; charset=utf-8"))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	})
}

func TestCreateServiceHTTPDecodeErrors(t *testing.T) {
	t.Run("unknown field", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/services",
			[]byte(`{"name":"web","spec":{"task":{"image":"nginx"}},"bogus":1}`),
			withToken(api.operator), withContentType(MediaType))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
		fields, ok := doc.Error.Details["fields"].(map[string]any)
		require.True(t, ok, "details.fields missing: %#v", doc.Error.Details)
		assert.Contains(t, fields, "bogus")
		assert.Zero(t, fc.createCalls)
	})

	t.Run("wrong field type", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/services",
			[]byte(`{"name":7,"spec":{"task":{"image":"nginx"}}}`),
			withToken(api.operator), withContentType(MediaType))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Equal(t, string(output.CodeInvalid), decodeError(t, rec).Error.Code)
		assert.Zero(t, fc.createCalls)
	})

	t.Run("empty body", func(t *testing.T) {
		api := newWriteTestAPI(t, &fakeClient{})
		rec := api.doWrite(t, http.MethodPost, "/v1/services", nil,
			withToken(api.operator), withContentType(MediaType))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	})

	t.Run("oversized body", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		// One byte over the cap, inside a well-formed document.
		body := []byte(`{"name":"web","spec":{"task":{"image":"` +
			strings.Repeat("a", maxWriteBodyBytes) + `"}}}`)

		rec := api.doWrite(t, http.MethodPost, "/v1/services", body,
			withToken(api.operator), withContentType(MediaType))
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
		assert.Equal(t, "payload_too_large", doc.Error.Details["reason"])
		assert.Zero(t, fc.createCalls)
	})
}

func TestCreateServiceHTTPIfNoneMatch(t *testing.T) {
	body := []byte(`{"name":"web","spec":{"task":{"image":"nginx"}}}`)

	t.Run("star is forwarded", func(t *testing.T) {
		fc := &fakeClient{createdService: v1.Service{ID: "svc-1", Name: "web", Revision: "1"}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/services", body,
			withToken(api.operator), withContentType(MediaType),
			withHeader("If-None-Match", "*"))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		assert.Equal(t, "*", fc.gotWriteOpts.IfNoneMatch)
	})

	t.Run("any other value is invalid", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/services", body,
			withToken(api.operator), withContentType(MediaType),
			withHeader("If-None-Match", `"1"`))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

		doc := decodeError(t, rec)
		fields, ok := doc.Error.Details["fields"].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, fields, "If-None-Match")
		assert.Zero(t, fc.createCalls)
	})
}

func TestUpdateServiceHTTP(t *testing.T) {
	patch := []byte(`{"spec":{"replicas":2}}`)

	t.Run("requires If-Match", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPatch, "/v1/services/svc-1", patch,
			withToken(api.operator), withContentType(v1.MergePatchMediaType))
		require.Equal(t, http.StatusPreconditionRequired, rec.Code, rec.Body.String())

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeConflict), doc.Error.Code)
		assert.Equal(t, "If-Match", doc.Error.Details["required"])
		assert.Zero(t, fc.updateCalls)
		assert.Empty(t, api.records(t))
	})

	t.Run("requires the merge-patch media type", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPatch, "/v1/services/svc-1", patch,
			withToken(api.operator), withContentType(MediaType),
			withHeader("If-Match", `"17"`))
		require.Equal(t, http.StatusUnsupportedMediaType, rec.Code, rec.Body.String())
		assert.Equal(t, "unsupported_media_type", decodeError(t, rec).Error.Details["reason"])
		assert.Zero(t, fc.updateCalls)
	})

	t.Run("stale revision is 412 with both revisions", func(t *testing.T) {
		fc := &fakeClient{writeErr: output.PreconditionFailed("17", "18")}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPatch, "/v1/services/svc-1", patch,
			withToken(api.operator), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", `"17"`))
		require.Equal(t, http.StatusPreconditionFailed, rec.Code, rec.Body.String())

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeConflict), doc.Error.Code)
		assert.Equal(t, "17", doc.Error.Details["expectedRevision"])
		assert.Equal(t, "18", doc.Error.Details["currentRevision"])

		records := api.records(t)
		require.Len(t, records, 1)
		assert.Equal(t, "service.update", records[0].Action)
		assert.Equal(t, "error", records[0].Result)
		assert.Equal(t, "svc-1", records[0].Resource.Name)
	})

	t.Run("weak validator is 412", func(t *testing.T) {
		fc := &fakeClient{writeErr: output.PreconditionFailed("17", "17")}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPatch, "/v1/services/svc-1", patch,
			withToken(api.operator), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", `W/"17"`))
		require.Equal(t, http.StatusPreconditionFailed, rec.Code, rec.Body.String())
	})

	t.Run("success returns the new revision", func(t *testing.T) {
		replicas := int64(2)
		fc := &fakeClient{updatedService: v1.Service{
			ID: "svc-1", Name: "web", Revision: "18",
			Spec: v1.ServiceSpec{Mode: "replicated", Replicas: &replicas},
		}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPatch, "/v1/services/web", patch,
			withToken(api.operator), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", `"17"`))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assertWireHeaders(t, rec)
		assert.Equal(t, `"18"`, rec.Header().Get("ETag"))
		assert.NotContains(t, rec.Body.String(), "registryAuth")

		assert.Equal(t, "web", fc.gotRef)
		assert.Equal(t, patch, fc.gotPatch, "the raw merge patch is forwarded")
		assert.Equal(t, `"17"`, fc.gotWriteOpts.IfMatch)

		records := api.records(t)
		require.Len(t, records, 1)
		assert.Equal(t, "service.update", records[0].Action)
		assert.Equal(t, "ok", records[0].Result)
		assert.Equal(t, "revision", records[0].Precondition)
	})

	t.Run("star is audited as an unconditional precondition", func(t *testing.T) {
		fc := &fakeClient{updatedService: v1.Service{ID: "svc-1", Name: "web", Revision: "18"}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPatch, "/v1/services/web", patch,
			withToken(api.operator), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", "*"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		records := api.records(t)
		require.Len(t, records, 1)
		assert.Equal(t, "any", records[0].Precondition)
	})

	t.Run("oversized patch is 413", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		body := []byte(`{"spec":{"task":{"image":"` + strings.Repeat("a", maxWriteBodyBytes) + `"}}}`)
		rec := api.doWrite(t, http.MethodPatch, "/v1/services/web", body,
			withToken(api.operator), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", `"17"`))
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
		assert.Equal(t, "payload_too_large", decodeError(t, rec).Error.Details["reason"])
		assert.Zero(t, fc.updateCalls)
	})
}

func TestDeleteServiceHTTP(t *testing.T) {
	t.Run("success is 204 without a body", func(t *testing.T) {
		fc := &fakeClient{deletedService: v1.Service{ID: "svc-1", Name: "web", Revision: "17"}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodDelete, "/v1/services/web", nil,
			withToken(api.operator), withHeader("If-Match", `"17"`))
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		assert.Empty(t, rec.Body.String())
		assert.Equal(t, APIVersion, rec.Header().Get(apiVersionHeader))

		assert.Equal(t, "web", fc.gotRef)
		assert.Equal(t, `"17"`, fc.gotWriteOpts.IfMatch)

		records := api.records(t)
		require.Len(t, records, 1)
		assert.Equal(t, "service.delete", records[0].Action)
		assert.Equal(t, "ok", records[0].Result)
		assert.Equal(t, "svc-1", records[0].Resource.ID, "the audit names the deleted document")
		assert.Equal(t, "web", records[0].Resource.Name)
		assert.Equal(t, "revision", records[0].Precondition)
	})

	t.Run("success without If-Match is allowed", func(t *testing.T) {
		fc := &fakeClient{deletedService: v1.Service{ID: "svc-1", Name: "web", Revision: "17"}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodDelete, "/v1/services/web", nil,
			withToken(api.operator))
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		assert.Empty(t, fc.gotWriteOpts.IfMatch, "no precondition was sent")
		assert.Equal(t, 1, fc.deleteCalls)
	})

	t.Run("ignoreMissing is 204 and audits the reference", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodDelete, "/v1/services/missing?ignoreMissing=true", nil,
			withToken(api.operator))
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		assert.True(t, fc.gotWriteOpts.IgnoreMissing)

		records := api.records(t)
		require.Len(t, records, 1)
		assert.Equal(t, "ok", records[0].Result)
		assert.Equal(t, "missing", records[0].Resource.Name)
	})

	t.Run("missing is 404", func(t *testing.T) {
		fc := &fakeClient{writeErr: output.NewError(output.CodeNotFound, "service not found")}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodDelete, "/v1/services/missing", nil,
			withToken(api.operator))
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

		records := api.records(t)
		require.Len(t, records, 1)
		assert.Equal(t, "error", records[0].Result)
	})

	t.Run("missing with If-Match is 412", func(t *testing.T) {
		fc := &fakeClient{writeErr: output.PreconditionFailed("17", "")}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodDelete, "/v1/services/missing", nil,
			withToken(api.operator), withHeader("If-Match", `"17"`))
		require.Equal(t, http.StatusPreconditionFailed, rec.Code, rec.Body.String())
	})

	t.Run("bad ignoreMissing is 400", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodDelete, "/v1/services/web?ignoreMissing=maybe", nil,
			withToken(api.operator))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Zero(t, fc.deleteCalls)
	})
}

func TestRollbackServiceHTTP(t *testing.T) {
	t.Run("success returns the new revision", func(t *testing.T) {
		fc := &fakeClient{rolledBackService: v1.Service{ID: "svc-1", Name: "web", Revision: "19"}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/services/web/rollback", nil,
			withToken(api.operator), withHeader("If-Match", `"18"`))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assertWireHeaders(t, rec)
		assert.Equal(t, `"19"`, rec.Header().Get("ETag"))

		assert.Equal(t, "web", fc.gotRef)
		assert.Equal(t, `"18"`, fc.gotWriteOpts.IfMatch)

		records := api.records(t)
		require.Len(t, records, 1)
		assert.Equal(t, "service.rollback", records[0].Action)
		assert.Equal(t, "ok", records[0].Result)
	})

	t.Run("If-Match is required", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/services/web/rollback", nil,
			withToken(api.operator))
		require.Equal(t, http.StatusPreconditionRequired, rec.Code, rec.Body.String())
		assert.Zero(t, fc.rollbackCalls)
	})

	t.Run("a body is rejected", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/services/web/rollback",
			[]byte(`{"spec":{}}`),
			withToken(api.operator), withHeader("If-Match", `"18"`))
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Zero(t, fc.rollbackCalls)
	})

	t.Run("whitespace-only body is empty", func(t *testing.T) {
		fc := &fakeClient{rolledBackService: v1.Service{ID: "svc-1", Revision: "19"}}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodPost, "/v1/services/web/rollback", []byte(" \n\t"),
			withToken(api.operator), withHeader("If-Match", `"18"`))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}

// TestGetServiceETagIsRevision pins the validator of a single service read: the
// revision a later PATCH expects, not a body hash.
func TestGetServiceETagIsRevision(t *testing.T) {
	fc := &fakeClient{service: v1.Service{
		ID: "svc-1", Name: "web", Revision: "17",
		Spec: v1.ServiceSpec{Mode: "replicated", Task: v1.TaskSpec{Image: "nginx"}},
	}}
	api := newTestAPI(t, fc)

	rec := api.do(t, http.MethodGet, "/v1/services/web", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, `"17"`, rec.Header().Get("ETag"))

	notModified := api.do(t, http.MethodGet, "/v1/services/web",
		withToken(api.token), withHeader("If-None-Match", `"17"`))
	require.Equal(t, http.StatusNotModified, notModified.Code)
	assert.Empty(t, notModified.Body.String())
	assert.Equal(t, `"17"`, notModified.Header().Get("ETag"))

	stale := api.do(t, http.MethodGet, "/v1/services/web",
		withToken(api.token), withHeader("If-None-Match", `"16"`))
	require.Equal(t, http.StatusOK, stale.Code)
	assert.NotEmpty(t, stale.Body.String())
}

func TestServiceWriteMethodNotAllowed(t *testing.T) {
	api := newWriteTestAPI(t, &fakeClient{})

	for _, tc := range []struct {
		method string
		target string
		allow  string
	}{
		{http.MethodPut, "/v1/services/svc-1", "GET, PATCH, DELETE"},
		{http.MethodPut, "/v1/services", "GET, POST"},
		{http.MethodGet, "/v1/services/svc-1/rollback", "POST"},
		{http.MethodPost, "/v1/services/svc-1/tasks", "GET"},
	} {
		rec := api.do(t, tc.method, tc.target, withToken(api.token))
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
		assert.Equalf(t, tc.allow, rec.Header().Get("Allow"), "%s %s", tc.method, tc.target)

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
		assert.Equalf(t, "method_not_allowed", doc.Error.Details["reason"],
			"%s %s must name the plumbing in details.reason", tc.method, tc.target)
	}
}

// TestBindMountsRequireAdminRole proves the role → WriteOptions → client-rule
// chain at the HTTP layer. The actual mapping lives in pkg/client (see its
// TestBindMountsRequireAdmin); bindGuard applies that one contract rule so the
// handler's flag is observable end to end.
func TestBindMountsRequireAdminRole(t *testing.T) {
	bindBody := []byte(`{"name":"web","spec":{"task":{"image":"nginx",` +
		`"mounts":[{"type":"bind","source":"/srv","target":"/data"}]}}}`)
	bindPatch := []byte(`{"spec":{"task":{"mounts":[{"type":"bind","source":"/srv","target":"/data"}]}}}`)

	t.Run("operator create is forbidden", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, bindGuard{fakeClient: fc})

		rec := api.doWrite(t, http.MethodPost, "/v1/services", bindBody,
			withToken(api.operator), withContentType(MediaType))
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
		assert.Equal(t, "admin", doc.Error.Details["requiredRole"])
		assert.Equal(t, "bind_mount_requires_admin", doc.Error.Details["reason"])
		assert.False(t, fc.gotWriteOpts.AllowBindMounts)
	})

	t.Run("admin create is allowed", func(t *testing.T) {
		fc := &fakeClient{createdService: v1.Service{ID: "svc-1", Name: "web", Revision: "1"}}
		api := newWriteTestAPI(t, bindGuard{fakeClient: fc})

		rec := api.doWrite(t, http.MethodPost, "/v1/services", bindBody,
			withToken(api.token), withContentType(MediaType))
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		assert.True(t, fc.gotWriteOpts.AllowBindMounts)
	})

	t.Run("operator patch is forbidden", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, bindGuard{fakeClient: fc})

		rec := api.doWrite(t, http.MethodPatch, "/v1/services/web", bindPatch,
			withToken(api.operator), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", `"17"`))
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.False(t, fc.gotWriteOpts.AllowBindMounts)
	})

	t.Run("admin patch is allowed", func(t *testing.T) {
		fc := &fakeClient{updatedService: v1.Service{ID: "svc-1", Name: "web", Revision: "18"}}
		api := newWriteTestAPI(t, bindGuard{fakeClient: fc})

		rec := api.doWrite(t, http.MethodPatch, "/v1/services/web", bindPatch,
			withToken(api.token), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", `"17"`))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.True(t, fc.gotWriteOpts.AllowBindMounts)
	})

	t.Run("operator patch that does not touch mounts is allowed", func(t *testing.T) {
		fc := &fakeClient{updatedService: v1.Service{ID: "svc-1", Name: "web", Revision: "18"}}
		api := newWriteTestAPI(t, bindGuard{fakeClient: fc})

		rec := api.doWrite(t, http.MethodPatch, "/v1/services/web",
			[]byte(`{"spec":{"replicas":2}}`),
			withToken(api.operator), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", `"17"`))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}

// TestServiceWriteDoesNotLeakRegistryAuth proves the invariant across every
// response shape a write can produce.
func TestServiceWriteDoesNotLeakRegistryAuth(t *testing.T) {
	fc := &fakeClient{
		createdService:    v1.Service{ID: "svc-1", Name: "web", Revision: "1"},
		updatedService:    v1.Service{ID: "svc-1", Name: "web", Revision: "2"},
		rolledBackService: v1.Service{ID: "svc-1", Name: "web", Revision: "3"},
	}
	api := newWriteTestAPI(t, fc)

	body := []byte(`{"name":"web","spec":{"task":{"image":"nginx","registryAuth":"topsecret"}}}`)
	responses := [][]byte{
		api.doWrite(t, http.MethodPost, "/v1/services", body,
			withToken(api.operator), withContentType(MediaType)).Body.Bytes(),
		api.doWrite(t, http.MethodPatch, "/v1/services/web",
			[]byte(`{"spec":{"task":{"registryAuth":"topsecret"}}}`),
			withToken(api.operator), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", `"1"`)).Body.Bytes(),
		api.doWrite(t, http.MethodPost, "/v1/services/web/rollback", nil,
			withToken(api.operator), withHeader("If-Match", `"2"`)).Body.Bytes(),
	}

	for _, response := range responses {
		assert.NotContains(t, string(response), "registryAuth")
		assert.NotContains(t, string(response), "topsecret")
	}
}
