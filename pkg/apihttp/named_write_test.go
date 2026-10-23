package apihttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopeMatrixCase is one write route and the scope that reaches it.
type scopeMatrixCase struct {
	name   string
	method string
	target string
	scope  string
	body   []byte
	ctype  string
}

// writeRoutes lists every phase-2 named write route with its scope. The status
// matrix tests iterate it so a new route cannot ship with an untested scope.
func writeRoutes() []scopeMatrixCase {
	return []scopeMatrixCase{
		{"create config", http.MethodPost, "/v1/configs", "configs:write",
			[]byte(`{"name":"app"}`), MediaType},
		{"update config", http.MethodPatch, "/v1/configs/app", "configs:write",
			[]byte(`{"labels":{"a":"b"}}`), v1.MergePatchMediaType},
		{"delete config", http.MethodDelete, "/v1/configs/app", "configs:write", nil, ""},
		{"create secret", http.MethodPost, "/v1/secrets", "secrets:write",
			[]byte(`{"name":"token","data":"aGk="}`), MediaType},
		{"update secret", http.MethodPatch, "/v1/secrets/token", "secrets:write",
			[]byte(`{"labels":{"a":"b"}}`), v1.MergePatchMediaType},
		{"delete secret", http.MethodDelete, "/v1/secrets/token", "secrets:write", nil, ""},
		{"create network", http.MethodPost, "/v1/networks", "networks:write",
			[]byte(`{"name":"frontend"}`), MediaType},
		{"delete network", http.MethodDelete, "/v1/networks/frontend", "networks:write", nil, ""},
		{"update node", http.MethodPatch, "/v1/nodes/worker-a", "nodes:write",
			[]byte(`{"availability":"drain"}`), v1.MergePatchMediaType},
	}
}

// headerFor returns the request options a route needs: its body media type and,
// for a PATCH, a current If-Match.
func headerFor(tc scopeMatrixCase) []func(*http.Request) {
	opts := make([]func(*http.Request), 0, 2)
	if tc.ctype != "" {
		opts = append(opts, withContentType(tc.ctype))
	}
	if tc.method == http.MethodPatch {
		opts = append(opts, withHeader("If-Match", `"1"`))
	}
	return opts
}

func TestNamedWriteRoutesRequireTheirScope(t *testing.T) {
	for _, tc := range writeRoutes() {
		t.Run(tc.name, func(t *testing.T) {
			api := newWriteTestAPI(t, &fakeClient{})

			opts := append([]func(*http.Request){withToken(api.reader)}, headerFor(tc)...)
			rec := api.doWrite(t, tc.method, tc.target, tc.body, opts...)

			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			doc := decodeError(t, rec)
			assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
			assert.Equal(t, tc.scope, doc.Error.Details["requiredScope"],
				"a reader on a write route is 403, never 405 or 415")
		})
	}
}

func TestNamedWriteRoutesRejectWrongContentType(t *testing.T) {
	for _, tc := range writeRoutes() {
		if tc.method == http.MethodDelete {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			api := newWriteTestAPI(t, &fakeClient{})

			opts := append([]func(*http.Request){withToken(api.token)},
				withContentType("application/json"))
			rec := api.doWrite(t, tc.method, tc.target, tc.body, opts...)

			require.Equal(t, http.StatusUnsupportedMediaType, rec.Code, rec.Body.String())
			doc := decodeError(t, rec)
			assert.Equal(t, "unsupported_media_type", doc.Error.Details["reason"])
		})
	}
}

func TestNamedWritePatchesRequireIfMatch(t *testing.T) {
	api := newWriteTestAPI(t, &fakeClient{})

	for _, target := range []string{"/v1/configs/app", "/v1/secrets/token", "/v1/nodes/worker-a"} {
		rec := api.doWrite(t, http.MethodPatch, target, []byte(`{}`),
			withToken(api.token), withContentType(v1.MergePatchMediaType))
		require.Equal(t, http.StatusPreconditionRequired, rec.Code, target+": "+rec.Body.String())

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeConflict), doc.Error.Code)
		assert.Equal(t, "If-Match", doc.Error.Details["required"])
	}
}

func TestNamedWriteStalePreconditionIs412(t *testing.T) {
	fc := &fakeClient{namedErr: output.PreconditionFailed(`"1"`, `"2"`)}
	api := newWriteTestAPI(t, fc)

	for _, target := range []string{"/v1/configs/app", "/v1/secrets/token", "/v1/nodes/worker-a"} {
		rec := api.doWrite(t, http.MethodPatch, target, []byte(`{}`),
			withToken(api.token), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", `"1"`))
		require.Equal(t, http.StatusPreconditionFailed, rec.Code, target+": "+rec.Body.String())

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeConflict), doc.Error.Code)
		assert.Equal(t, output.ReasonPreconditionFailed, doc.Error.Details["reason"])
	}
}

func TestConfigWriteHTTP(t *testing.T) {
	payload := "listen 8080"
	fc := &fakeClient{
		createdConfig: v1.Config{ID: "cfg-1", Name: "app", Revision: "3", Data: &payload},
		updatedConfig: v1.Config{ID: "cfg-1", Name: "app", Revision: "4", Data: &payload},
		deletedConfig: v1.Config{ID: "cfg-1", Name: "app", Revision: "4"},
	}
	api := newWriteTestAPI(t, fc)

	// Operator holds configs:read-values, so it sees the payload it wrote.
	rec := api.doWrite(t, http.MethodPost, "/v1/configs",
		[]byte(`{"name":"app","labels":{"team":"payments"},"data":"listen 8080"}`),
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assertWireHeaders(t, rec)
	assert.Equal(t, `"3"`, rec.Header().Get("ETag"))
	assert.Equal(t, "listen 8080", fc.gotConfigWrite.Data)
	assert.Contains(t, rec.Body.String(), `"data":"listen 8080"`)

	rec = api.doWrite(t, http.MethodPatch, "/v1/configs/app",
		[]byte(`{"labels":{"team":"search"}}`),
		withToken(api.operator), withContentType(v1.MergePatchMediaType),
		withHeader("If-Match", `"3"`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, `"4"`, rec.Header().Get("ETag"))
	assert.Equal(t, "app", fc.gotNamedRef)
	assert.Equal(t, []byte(`{"labels":{"team":"search"}}`), fc.gotNamedPatch)
	assert.Equal(t, `"3"`, fc.gotNamedOpts.IfMatch)

	rec = api.doWrite(t, http.MethodDelete, "/v1/configs/app", nil,
		withToken(api.operator), withHeader("If-Match", `"4"`))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String())
	assert.Equal(t, `"4"`, fc.gotNamedOpts.IfMatch)

	records := api.records(t)
	require.Len(t, records, 3)
	for i, action := range []string{"config.create", "config.update", "config.delete"} {
		assert.Equal(t, action, records[i].Action)
		assert.Equal(t, v1.KindConfig, records[i].Resource.Kind)
		assert.Equal(t, "cfg-1", records[i].Resource.ID)
		assert.Equal(t, "ok", records[i].Result)
	}
}

// TestConfigWriteResponseRespectsReadValues pins the write response: a token that
// may write a value but not read values back does not get the payload echoed.
func TestConfigWriteResponseRespectsReadValues(t *testing.T) {
	payload := "listen 8080"
	fc := &fakeClient{
		createdConfig: v1.Config{ID: "cfg-1", Name: "app", Revision: "3", Data: &payload},
		updatedConfig: v1.Config{ID: "cfg-1", Name: "app", Revision: "4", Data: &payload},
	}
	api := newWriteTestAPI(t, fc)

	writeOnly := api.mint(t, "write-only", RoleOperator, TokenOptions{
		Scopes: []string{"configs:read", "configs:write"},
	})

	rec := api.doWrite(t, http.MethodPost, "/v1/configs",
		[]byte(`{"name":"app","data":"listen 8080"}`),
		withToken(writeOnly), withContentType(MediaType))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), payload)
	assert.NotContains(t, rec.Body.String(), `"data"`)

	rec = api.doWrite(t, http.MethodPatch, "/v1/configs/app",
		[]byte(`{"labels":{"a":"b"}}`),
		withToken(writeOnly), withContentType(v1.MergePatchMediaType),
		withHeader("If-Match", `"3"`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), payload)
	assert.NotContains(t, rec.Body.String(), `"data"`)
}

func TestConfigCreateRejectsUnknownAndOversizedBodies(t *testing.T) {
	api := newWriteTestAPI(t, &fakeClient{createdConfig: v1.Config{ID: "cfg-1"}})

	rec := api.doWrite(t, http.MethodPost, "/v1/configs",
		[]byte(`{"name":"app","payload":"x"}`),
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	doc := decodeError(t, rec)
	fields, ok := doc.Error.Details["fields"].(map[string]any)
	require.True(t, ok, rec.Body.String())
	assert.Contains(t, fields, "payload")

	oversized := []byte(`{"name":"app","data":"` +
		strings.Repeat("a", maxWriteBodyBytes) + `"}`)
	rec = api.doWrite(t, http.MethodPost, "/v1/configs", oversized,
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	doc = decodeError(t, rec)
	assert.Equal(t, "payload_too_large", doc.Error.Details["reason"])
}

func TestSecretWriteHTTP(t *testing.T) {
	fc := &fakeClient{
		createdSecret: v1.Secret{ID: "sec-1", Name: "token", Revision: "2", Driver: "vault"},
		updatedSecret: v1.Secret{ID: "sec-1", Name: "token", Revision: "3", Driver: "vault"},
		deletedSecret: v1.Secret{ID: "sec-1", Name: "token", Revision: "3"},
	}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPost, "/v1/secrets",
		[]byte(`{"name":"token","data":"aHVudGVyMg=="}`),
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, `"2"`, rec.Header().Get("ETag"))
	assert.Equal(t, "aHVudGVyMg==", fc.gotSecretWrite.Data)
	assert.NotContains(t, rec.Body.String(), "aHVudGVyMg==")
	assert.NotContains(t, rec.Body.String(), `"data"`,
		"no secret response carries a data field, ever")

	rec = api.doWrite(t, http.MethodPatch, "/v1/secrets/token",
		[]byte(`{"labels":{"env":"prod"}}`),
		withToken(api.operator), withContentType(v1.MergePatchMediaType),
		withHeader("If-Match", `"2"`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, `"3"`, rec.Header().Get("ETag"))

	rec = api.doWrite(t, http.MethodDelete, "/v1/secrets/token", nil,
		withToken(api.operator))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	records := api.records(t)
	require.Len(t, records, 3)
	for i, action := range []string{"secret.create", "secret.update", "secret.delete"} {
		assert.Equal(t, action, records[i].Action)
		assert.Equal(t, v1.KindSecret, records[i].Resource.Kind)
	}
}

func TestNetworkWriteHTTP(t *testing.T) {
	fc := &fakeClient{
		createdNetwork: v1.Network{ID: "net-1", Name: "frontend", Revision: "1", Driver: "overlay"},
		deletedNetwork: v1.Network{ID: "net-1", Name: "frontend", Revision: "1"},
	}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPost, "/v1/networks",
		[]byte(`{"name":"frontend","driver":"overlay","ipam":{"subnet":"10.0.1.0/24","gateway":"10.0.1.1"}}`),
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, `"1"`, rec.Header().Get("ETag"))
	require.NotNil(t, fc.gotNetworkWrit.IPAM)
	assert.Equal(t, "10.0.1.0/24", fc.gotNetworkWrit.IPAM.Subnet)

	rec = api.doWrite(t, http.MethodDelete, "/v1/networks/frontend", nil,
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	records := api.records(t)
	require.Len(t, records, 2)
	assert.Equal(t, "network.create", records[0].Action)
	assert.Equal(t, v1.KindNetwork, records[0].Resource.Kind)
	assert.Equal(t, "network.delete", records[1].Action)
	assert.Equal(t, v1.KindNetwork, records[1].Resource.Kind)
}

func TestNetworkHasNoUpdate(t *testing.T) {
	api := newWriteTestAPI(t, &fakeClient{})

	for _, method := range []string{http.MethodPatch, http.MethodPut} {
		rec := api.doWrite(t, method, "/v1/networks/frontend",
			[]byte(`{"labels":{"a":"b"}}`),
			withToken(api.operator), withContentType(v1.MergePatchMediaType),
			withHeader("If-Match", `"1"`))

		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, method+": "+rec.Body.String())
		assert.Equal(t, "GET, DELETE", rec.Header().Get("Allow"))

		doc := decodeError(t, rec)
		assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
		assert.Equal(t, "method_not_allowed", doc.Error.Details["reason"])
	}
}

func TestNodeWriteHTTP(t *testing.T) {
	fc := &fakeClient{updatedNode: v1.Node{
		ID: "node-1", Hostname: "worker-a", Revision: "7",
		Role: "worker", Membership: "accepted", Availability: "drain", State: "ready",
	}}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPatch, "/v1/nodes/worker-a",
		[]byte(`{"availability":"drain","labels":{"zone":"b"}}`),
		withToken(api.token), withContentType(v1.MergePatchMediaType),
		withHeader("If-Match", `"6"`))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, `"7"`, rec.Header().Get("ETag"))
	assert.Equal(t, "worker-a", fc.gotNamedRef)
	assert.Equal(t, []byte(`{"availability":"drain","labels":{"zone":"b"}}`), fc.gotNamedPatch)
	assert.Equal(t, `"6"`, fc.gotNamedOpts.IfMatch)

	var doc v1.NodeDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, "drain", doc.Availability)

	records := api.records(t)
	require.Len(t, records, 1)
	assert.Equal(t, "node.update", records[0].Action)
	assert.Equal(t, v1.KindNode, records[0].Resource.Kind)
	assert.Equal(t, "node-1", records[0].Resource.ID)
	assert.Equal(t, "worker-a", records[0].Resource.Name)
}

// TestConfigCreateRejectsNonUTF8Body pins the create-side value rule at the
// transport edge. Go's JSON decoder would otherwise replace the raw bytes with
// U+FFFD and store a payload the caller never sent.
func TestConfigCreateRejectsNonUTF8Body(t *testing.T) {
	fc := &fakeClient{createdConfig: v1.Config{ID: "cfg-1", Name: "bin", Revision: "1"}}
	api := newWriteTestAPI(t, fc)

	body := []byte("{\"name\":\"bin\",\"data\":\"\xff\xfe\"}")
	rec := api.doWrite(t, http.MethodPost, "/v1/configs", body,
		withToken(api.operator), withContentType(MediaType))

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	assert.Equal(t, "binary_not_supported", doc.Error.Details["reason"])
	fields, ok := doc.Error.Details["fields"].(map[string]any)
	require.True(t, ok, rec.Body.String())
	assert.Contains(t, fields, "data")
	assert.Zero(t, fc.configCreates, "a body that is not UTF-8 must never reach the client")
}

// TestNetworkCreateForwardsIfNoneMatch pins the create-only marker on networks.
func TestNetworkCreateForwardsIfNoneMatch(t *testing.T) {
	fc := &fakeClient{createdNetwork: v1.Network{ID: "net-1", Name: "frontend", Revision: "1"}}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPost, "/v1/networks", []byte(`{"name":"frontend"}`),
		withToken(api.operator), withContentType(MediaType), withHeader("If-None-Match", "*"))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "*", fc.gotNamedOpts.IfNoneMatch,
		"the create-only marker is forwarded for networks too")
}

// TestNamedWritePatchesOversizedBody pins the 413 on PATCH, not just create.
func TestNamedWritePatchesOversizedBody(t *testing.T) {
	fc := &fakeClient{updatedConfig: v1.Config{ID: "cfg-1", Name: "app", Revision: "2"}}
	api := newWriteTestAPI(t, fc)

	oversized := []byte(`{"labels":{"a":"` + strings.Repeat("a", maxWriteBodyBytes) + `"}}`)
	rec := api.doWrite(t, http.MethodPatch, "/v1/configs/app", oversized,
		withToken(api.operator), withContentType(v1.MergePatchMediaType),
		withHeader("If-Match", `"1"`))

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	doc := decodeError(t, rec)
	assert.Equal(t, "payload_too_large", doc.Error.Details["reason"])
	assert.Zero(t, fc.configUpdates)
}

// TestNamedDeleteIgnoreMissing pins the delete shapes: a miss with ignoreMissing
// is 204 and audited, while a stale precondition stays 412 even with it set.
func TestNamedDeleteIgnoreMissing(t *testing.T) {
	t.Run("missing is 204 and audited", func(t *testing.T) {
		fc := &fakeClient{}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodDelete, "/v1/configs/gone?ignoreMissing=true", nil,
			withToken(api.operator))
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		assert.True(t, fc.gotNamedOpts.IgnoreMissing)

		records := api.records(t)
		require.Len(t, records, 1)
		assert.Equal(t, "config.delete", records[0].Action)
		assert.Equal(t, "ok", records[0].Result)
		assert.Equal(t, "gone", records[0].Resource.Name,
			"an ignoreMissing miss audits the reference the caller used")
	})

	t.Run("stale If-Match wins over ignoreMissing", func(t *testing.T) {
		fc := &fakeClient{namedErr: output.PreconditionFailed(`"1"`, `"2"`)}
		api := newWriteTestAPI(t, fc)

		rec := api.doWrite(t, http.MethodDelete, "/v1/configs/gone?ignoreMissing=true", nil,
			withToken(api.operator), withHeader("If-Match", `"1"`))
		require.Equal(t, http.StatusPreconditionFailed, rec.Code, rec.Body.String())
		assert.True(t, fc.gotNamedOpts.IgnoreMissing,
			"the handler forwards both; the client evaluates the precondition first")

		records := api.records(t)
		require.Len(t, records, 1)
		assert.Equal(t, "error", records[0].Result)
	})
}

// TestNodePatchWeakIfMatch pins that the raw validator reaches the client and a
// weak failure maps to 412 with details.weak.
func TestNodePatchWeakIfMatch(t *testing.T) {
	weak := output.PreconditionFailed(`"6"`, `"6"`)
	weak.Details["weak"] = true
	fc := &fakeClient{namedErr: weak}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPatch, "/v1/nodes/worker-a",
		[]byte(`{"availability":"drain"}`),
		withToken(api.token), withContentType(v1.MergePatchMediaType),
		withHeader("If-Match", `W/"6"`))

	require.Equal(t, http.StatusPreconditionFailed, rec.Code, rec.Body.String())
	assert.Equal(t, `W/"6"`, fc.gotNamedOpts.IfMatch, "a weak validator is forwarded unchanged")

	doc := decodeError(t, rec)
	assert.Equal(t, output.ReasonPreconditionFailed, doc.Error.Details["reason"])
	assert.Equal(t, true, doc.Error.Details["weak"])
}

// TestNamedGetsCarryRevisionETag pins the validator of every single-resource
// read: the revision the later write expects, plus 304 on If-None-Match.
func TestNamedGetsCarryRevisionETag(t *testing.T) {
	fc := &fakeClient{
		config:  v1.Config{ID: "cfg-1", Name: "app", Revision: "3"},
		secret:  v1.Secret{ID: "sec-1", Name: "token", Revision: "4"},
		network: v1.Network{ID: "net-1", Name: "frontend", Revision: "2"},
		node:    v1.Node{ID: "node-1", Hostname: "worker-a", Revision: "9"},
	}
	api := newTestAPI(t, fc)

	for _, tc := range []struct {
		target string
		etag   string
	}{
		{"/v1/configs/cfg-1", `"3"`},
		{"/v1/secrets/sec-1", `"4"`},
		{"/v1/networks/net-1", `"2"`},
		{"/v1/nodes/node-1", `"9"`},
	} {
		rec := api.do(t, http.MethodGet, tc.target, withToken(api.token))
		require.Equal(t, http.StatusOK, rec.Code, tc.target+": "+rec.Body.String())
		assert.Equal(t, tc.etag, rec.Header().Get("ETag"), tc.target)

		rec = api.do(t, http.MethodGet, tc.target, withToken(api.token),
			withHeader("If-None-Match", tc.etag))
		require.Equal(t, http.StatusNotModified, rec.Code, tc.target+": "+rec.Body.String())
		assert.Empty(t, rec.Body.String())
	}
}

// TestNamedWriteAuditFailureDoesNotFailTheWrite pins the best-effort rule: a
// committed mutation answers its status even when the audit store cannot append.
func TestNamedWriteAuditFailureDoesNotFailTheWrite(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.jsonl")

	audit, err := OpenAuditStore(auditPath, 0)
	require.NoError(t, err)
	// A directory at the log path makes every append fail with EISDIR.
	require.NoError(t, os.Mkdir(auditPath, 0o700))

	tokens, err := OpenTokenStore(filepath.Join(dir, "tokens.json"))
	require.NoError(t, err)
	operator, _, err := tokens.Create("deploy", RoleOperator)
	require.NoError(t, err)

	srv, err := New(Config{
		Client: &fakeClient{createdConfig: v1.Config{ID: "cfg-1", Name: "app", Revision: "1"}},
		Tokens: tokens,
		Audit:  audit,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/configs",
		strings.NewReader(`{"name":"app"}`))
	req.Header.Set("Accept", MediaType)
	req.Header.Set("Content-Type", MediaType)
	req.Header.Set("Authorization", "Bearer "+operator)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}
