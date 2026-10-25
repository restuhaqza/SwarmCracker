package apihttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// fileStream is a fake node file endpoint: it answers a GET with raw bytes and
// records what the manager forwarded.
type fileStream struct {
	broker   *fakeBroker
	content  string
	gotQuery string
	gotBody  []byte
}

func newFileStream(content string) *fileStream {
	stream := &fileStream{broker: &fakeBroker{}, content: content}
	stream.broker.respond = func(w http.ResponseWriter, r *http.Request) {
		stream.gotQuery = r.URL.RawQuery
		if r.Method == http.MethodPut {
			stream.gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, stream.content)
	}
	return stream
}

func getFile(t *testing.T, api *execTestAPI, token, target string) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodGet, target, withToken(token))
}

func putFile(t *testing.T, api *execTestAPI, token, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodPut, target,
		withToken(token),
		func(r *http.Request) {
			r.Header.Set("Content-Type", "application/octet-stream")
			r.Body = io.NopCloser(strings.NewReader(body))
		})
}

func TestFilesRequireScope(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := getFile(t, api, api.reader, "/v1/tasks/web-1/files?path=/etc/hostname")
	require.Equal(t, http.StatusForbidden, rec.Code)

	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
	assert.Equal(t, string(ScopeTasksFiles), doc.Error.Details["requiredScope"])
	require.Zero(t, api.broker.calls)
}

func TestFilesTaskSelectorGates(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1"}
	api := newExecTestAPI(t, fc, &fakeBroker{})

	rec := getFile(t, api, api.byTask, "/v1/tasks/web-1/files?path=/etc/hostname")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "task-1", api.broker.gotTaskRef)
	assert.Equal(t, "files", api.broker.gotOp)

	fc.task = v1.Task{ID: "task-2", ServiceID: "svc-1"}
	rec = getFile(t, api, api.byTask, "/v1/tasks/web-2/files?path=/etc/hostname")
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, 1, api.broker.calls, "a mismatched selector must not reach the node")
}

func TestFilesPathValidation(t *testing.T) {
	cases := map[string]string{
		"missing":   "",
		"relative":  "etc/hostname",
		"traversal": "/etc/../etc/passwd",
		"dot":       ".",
		"root":      "/",
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			api := newExecTestAPI(t, nil, nil)

			target := "/v1/tasks/web-1/files"
			if path != "" {
				target += "?path=" + path
			}
			rec := getFile(t, api, api.admin, target)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Zero(t, api.broker.calls)
			assert.Empty(t, api.client.gotTaskRef, "an invalid query must not resolve the task")

			doc := decodeErrorBody(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)

			rec = putFile(t, api, api.admin, target, "x")
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Zero(t, api.broker.calls)
		})
	}
}

func TestFilesModeValidation(t *testing.T) {
	for _, mode := range []string{"0999", "1777", "abc", "-1"} {
		t.Run(mode, func(t *testing.T) {
			api := newExecTestAPI(t, nil, nil)

			rec := putFile(t, api, api.admin,
				"/v1/tasks/web-1/files?path=/etc/app.conf&mode="+mode, "x")
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Zero(t, api.broker.calls)

			doc := decodeErrorBody(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
		})
	}
}

// TestFilesPutRequiresOctetStream pins that a raw upload must declare
// application/octet-stream: a JSON body would be silently stored as text.
func TestFilesPutRequiresOctetStream(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	for name, contentType := range map[string]string{
		"missing": "none",
		"json":    MediaType,
		"text":    "text/plain",
	} {
		t.Run(name, func(t *testing.T) {
			rec := api.do(t, http.MethodPut, "/v1/tasks/web-1/files?path=/etc/app.conf",
				withToken(api.admin),
				func(r *http.Request) {
					if contentType != "none" {
						r.Header.Set("Content-Type", contentType)
					}
					r.Body = io.NopCloser(strings.NewReader("x"))
				})

			require.Equal(t, http.StatusUnsupportedMediaType, rec.Code, rec.Body.String())
			require.Zero(t, api.broker.calls)

			doc := decodeErrorBody(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
			assert.Equal(t, "unsupported_media_type", doc.Error.Details["reason"])
		})
	}
}

func TestFilesGetPassesRawBytesThrough(t *testing.T) {
	stream := newFileStream("raw file\n")
	api := newExecTestAPI(t, nil, stream.broker)

	rec := getFile(t, api, api.admin, "/v1/tasks/web-1/files?path=/etc/hostname")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "raw file\n", rec.Body.String())
	assert.Equal(t, "path=/etc/hostname", stream.gotQuery,
		"the query is forwarded exactly as the caller sent it")
}

func TestFilesPutForwardsTheBody(t *testing.T) {
	stream := newFileStream("")
	api := newExecTestAPI(t, nil, stream.broker)

	rec := putFile(t, api, api.admin,
		"/v1/tasks/web-1/files?path=/etc/app.conf&mode=0600", "payload bytes")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, "payload bytes", string(stream.gotBody))
	assert.Equal(t, "path=/etc/app.conf&mode=0600", stream.gotQuery)
}

// guardedBody reports whether anything read it before the broker did: the
// manager must stream an upload, never buffer it.
type guardedBody struct {
	data        []byte
	offset      int
	inBroker    atomic.Bool
	readOutside atomic.Bool
}

func (b *guardedBody) Close() error { return nil }

func (b *guardedBody) Read(p []byte) (int, error) {
	if !b.inBroker.Load() {
		b.readOutside.Store(true)
	}
	if b.offset >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.offset:])
	b.offset += n
	return n, nil
}

func TestFilesPutDoesNotBufferTheBody(t *testing.T) {
	body := &guardedBody{data: []byte(strings.Repeat("payload", 1024))}

	broker := &fakeBroker{}
	var got []byte
	broker.respond = func(w http.ResponseWriter, r *http.Request) {
		body.inBroker.Store(true)
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}
	api := newExecTestAPI(t, nil, broker)

	rec := api.do(t, http.MethodPut, "/v1/tasks/web-1/files?path=/etc/app.conf",
		withToken(api.admin),
		func(r *http.Request) {
			r.Header.Set("Content-Type", "application/octet-stream")
			r.Body = body
		})

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, len(body.data), len(got))
	assert.False(t, body.readOutside.Load(),
		"the manager must not read the upload before the broker streams it")
}

func TestFilesGetIsAudited(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1", Name: "web.1"}
	api := newExecTestAPI(t, fc, &fakeBroker{})

	rec := getFile(t, api, api.admin, "/v1/tasks/web-1/files?path=/etc/hostname")
	require.Equal(t, http.StatusOK, rec.Code)

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.files", records[0].Action)
	assert.Equal(t, "ok", records[0].Result)
	assert.Equal(t, v1.KindTask, records[0].Resource.Kind)
	assert.Equal(t, "task-1", records[0].Resource.ID)
	assert.NotContains(t, records[0].Resource.Name, "/etc/hostname")
}

func TestFilesAuditErrorOnRelayedFailure(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1", Name: "web.1"}
	broker := &fakeBroker{respond: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", MediaType)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w,
			`{"schemaVersion":1,"kind":"Error","error":{"code":"invalid","message":"directories are not supported","retryable":false,"details":{"reason":"directory_not_supported"}}}`)
	}}
	api := newExecTestAPI(t, fc, broker)

	rec := getFile(t, api, api.admin, "/v1/tasks/web-1/files?path=/var/log")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	assert.Equal(t, "directory_not_supported", doc.Error.Details["reason"])

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.files", records[0].Action)
	assert.Equal(t, "error", records[0].Result,
		"a relayed node failure means the file was never served")
}

func TestFilesAuditErrorOnSelectorMismatch(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-2", ServiceID: "svc-1", Name: "web.2"}
	api := newExecTestAPI(t, fc, &fakeBroker{})

	rec := getFile(t, api, api.byTask, "/v1/tasks/web-2/files?path=/etc/hostname")
	require.Equal(t, http.StatusForbidden, rec.Code)

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.files", records[0].Action)
	assert.Equal(t, "error", records[0].Result)
	assert.Equal(t, "task-2", records[0].Resource.ID)
}

// TestFilesAuditNeverCarriesThePath pins that the audit log records the task, not
// what was read.
func TestFilesAuditNeverCarriesThePath(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1", Name: "web.1"}
	api := newExecTestAPI(t, fc, &fakeBroker{})

	rec := getFile(t, api, api.admin, "/v1/tasks/web-1/files?path=/etc/hostname")
	require.Equal(t, http.StatusOK, rec.Code)

	raw, err := os.ReadFile(api.audit.path)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "/etc/hostname")
	assert.NotContains(t, raw, "path")
}

func TestFilesMissingTaskIsNotFound(t *testing.T) {
	fc := &execFakeClient{
		fakeClient: &fakeClient{},
		taskErr:    output.NewError(output.CodeNotFound, "task not found"),
	}
	api := newExecTestAPI(t, fc, nil)

	rec := getFile(t, api, api.admin, "/v1/tasks/nope/files?path=/etc/hostname")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Zero(t, api.broker.calls)

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "error", records[0].Result)
}

func TestFilesWithoutBrokerIsNotImplemented(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}

	store, err := OpenTokenStore(t.TempDir() + "/tokens.json")
	require.NoError(t, err)
	secret, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	srv, err := New(Config{Client: fc, Tokens: store, RateLimit: NewRateLimiter(1e9, 1e9)})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/web-1/files?path=/etc/hostname", nil)
	req.Header.Set("Accept", MediaType)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "files", doc.Error.Details["capability"])
}

// TestFilesAcceptsOctetStreamAccept pins media-type negotiation for a download:
// a caller that names the file media type must not receive 406.
func TestFilesAcceptsOctetStreamAccept(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := api.do(t, http.MethodGet, "/v1/tasks/web-1/files?path=/etc/hostname",
		withToken(api.admin),
		func(r *http.Request) { r.Header.Set("Accept", "application/octet-stream") })

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
