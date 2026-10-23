package apihttp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// logStreamBody is the NDJSON a fake node broker answers with.
const logStreamBody = `{"type":"log","stream":"stdout","time":"2026-10-24T12:00:00Z","data":"aGVsbG8K"}` + "\n" +
	`{"type":"eof"}` + "\n"

func getLogs(t *testing.T, api *execTestAPI, token, target string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	all := append([]func(*http.Request){withToken(token)}, opts...)
	return api.do(t, http.MethodGet, target, all...)
}

func TestLogsRequiresAuthentication(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := api.do(t, http.MethodGet, "/v1/tasks/web-1/logs")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Zero(t, api.broker.calls)
}

// TestLogsRequiresScope pins that a token narrowed below tasks:logs is refused.
func TestLogsRequiresScope(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	narrow, _, err := api.store.CreateWithOptions("narrow", RoleReader, TokenOptions{
		Scopes: []string{string(ScopeTasksRead)},
	})
	require.NoError(t, err)

	rec := getLogs(t, api, narrow, "/v1/tasks/web-1/logs")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
	assert.Equal(t, string(ScopeTasksLogs), doc.Error.Details["requiredScope"])
	require.Zero(t, api.broker.calls)
}

// TestLogsReaderStreams proves the plain reader role can tail a log: logs are
// reader-scoped.
func TestLogsReaderStreams(t *testing.T) {
	api := newExecTestAPI(t, nil, &fakeBroker{respond: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", v1.NDJSONMediaType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, logStreamBody)
	}})

	rec := getLogs(t, api, api.reader, "/v1/tasks/web-1/logs")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, v1.NDJSONMediaType, rec.Header().Get("Content-Type"))
	assert.Equal(t, logStreamBody, rec.Body.String(), "the stream is passed through byte for byte")

	frames := decodeLogFrames(t, rec.Body.Bytes())
	require.Len(t, frames, 2)
	assert.Equal(t, v1.LogFrameTypeLog, frames[0].Type)
	assert.Equal(t, []byte("hello\n"), frames[0].Data)
	assert.Equal(t, v1.LogFrameTypeEOF, frames[1].Type, "a non-follow stream ends with eof")
}

func decodeLogFrames(t *testing.T, body []byte) []v1.LogFrame {
	t.Helper()

	frames := []v1.LogFrame{}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	for {
		var frame v1.LogFrame
		err := decoder.Decode(&frame)
		if err == io.EOF {
			return frames
		}
		require.NoError(t, err)
		frames = append(frames, frame)
	}
}

func TestLogsResolvesTask(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-resolved"}
	api := newExecTestAPI(t, fc, nil)

	rec := getLogs(t, api, api.reader, "/v1/tasks/web-1/logs")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "web-1", fc.gotTaskRef)
	assert.Equal(t, "task-resolved", api.broker.gotTaskRef, "the broker gets the resolved task ID")
	assert.Equal(t, "logs", api.broker.gotOp)
}

func TestLogsMissingTaskIsNotFound(t *testing.T) {
	fc := &execFakeClient{
		fakeClient: &fakeClient{},
		taskErr:    output.NewError(output.CodeNotFound, "task not found"),
	}
	api := newExecTestAPI(t, fc, nil)

	rec := getLogs(t, api, api.reader, "/v1/tasks/nope/logs")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Zero(t, api.broker.calls)

	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeNotFound), doc.Error.Code)
}

func TestLogsQueryValidation(t *testing.T) {
	cases := map[string]string{
		"negative tail":    "tail=-1",
		"non-numeric tail": "tail=abc",
		"invalid since":    "since=yesterday",
		"invalid follow":   "follow=maybe",
	}

	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			api := newExecTestAPI(t, nil, nil)

			rec := getLogs(t, api, api.reader, "/v1/tasks/web-1/logs?"+query)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Zero(t, api.broker.calls, "an invalid query must not reach the node")

			doc := decodeErrorBody(t, rec)
			assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
		})
	}
}

// TestLogsPassesValidatedQueryThrough pins that tail/follow/since are forwarded
// unchanged once validated.
func TestLogsPassesValidatedQueryThrough(t *testing.T) {
	api := newExecTestAPI(t, nil, &fakeBroker{respond: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", v1.NDJSONMediaType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, r.URL.RawQuery+"\n")
	}})

	rec := getLogs(t, api, api.reader,
		"/v1/tasks/web-1/logs?tail=5&follow=false&since=2026-10-24T00:00:00Z")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "tail=5&follow=false&since=2026-10-24T00:00:00Z", strings.TrimSpace(rec.Body.String()))
}

// TestLogsIgnoresSelectors pins that a runner's selector does not gate logs: the
// reader scope is the whole authorization for a log.
func TestLogsIgnoresSelectors(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-2", ServiceID: "svc-1"}
	api := newExecTestAPI(t, fc, nil)

	rec := getLogs(t, api, api.byTask, "/v1/tasks/web-2/logs")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "task-2", api.broker.gotTaskRef)
}

// TestLogsAreNotAudited pins that logs carry no audit record: the action
// taxonomy has none for a read stream.
func TestLogsAreNotAudited(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1"}
	api := newExecTestAPI(t, fc, nil)

	rec := getLogs(t, api, api.admin, "/v1/tasks/web-1/logs")
	require.Equal(t, http.StatusOK, rec.Code)

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestLogsWithoutBrokerIsNotImplemented(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1"}

	store, err := OpenTokenStore(t.TempDir() + "/tokens.json")
	require.NoError(t, err)
	secret, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	srv, err := New(Config{Client: fc, Tokens: store, RateLimit: NewRateLimiter(1e9, 1e9)})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/web-1/logs", nil)
	req.Header.Set("Accept", MediaType)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeNotImplemented), doc.Error.Code)
	assert.Equal(t, "logs", doc.Error.Details["capability"])
}

func TestLogsProxyFailureIsReported(t *testing.T) {
	api := newExecTestAPI(t, nil, &fakeBroker{err: output.NewError(output.CodeUnreachable, "node offline")})

	rec := getLogs(t, api, api.reader, "/v1/tasks/web-1/logs")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeUnreachable), doc.Error.Code)
}

// TestLogsForwardsRequestID pins that a relayed node error can name the caller's
// request.
func TestLogsForwardsRequestID(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := getLogs(t, api, api.reader, "/v1/tasks/web-1/logs")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, rec.Header().Get("X-Request-Id"), api.broker.gotRequestID)
}

// TestLogsFormAndMethod pins the route plumbing: a POST to the logs path is a
// typed 405, not a 404.
func TestLogsMethodNotAllowed(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := api.do(t, http.MethodPost, "/v1/tasks/web-1/logs", withToken(api.admin))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Contains(t, rec.Header().Get("Allow"), http.MethodGet)
}
