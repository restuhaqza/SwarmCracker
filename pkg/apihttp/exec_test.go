package apihttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// execFakeClient is fakeClient with independent task/service failures, so a test
// can make the task exist while the service lookup misses.
type execFakeClient struct {
	*fakeClient
	taskErr      error
	serviceErr   error
	gotTaskRef   string
	gotServiceID string
}

func (f *execFakeClient) GetTask(_ context.Context, ref string) (v1.Task, error) {
	f.gotTaskRef = ref
	if f.taskErr != nil {
		return v1.Task{}, f.taskErr
	}
	return f.task, nil
}

func (f *execFakeClient) GetService(_ context.Context, id string) (v1.Service, error) {
	f.gotServiceID = id
	if f.serviceErr != nil {
		return v1.Service{}, f.serviceErr
	}
	return f.service, nil
}

// fakeBroker records the proxy call and answers with one NDJSON frame. Health is
// called concurrently by the list fan-out, so its state is mutex-guarded.
type fakeBroker struct {
	calls        int
	gotTaskRef   string
	gotOp        string
	gotRequestID string
	err          error
	respond      func(w http.ResponseWriter, r *http.Request)

	healthFn       func(ctx context.Context, task v1.Task) (*v1.TaskHealth, error)
	healthMu       sync.Mutex
	healthCalls    int
	gotHealthTasks []v1.Task
}

func (f *fakeBroker) Proxy(_ context.Context, w http.ResponseWriter, r *http.Request, taskRef, op string) error {
	f.calls++
	f.gotTaskRef = taskRef
	f.gotOp = op
	f.gotRequestID = r.Header.Get("X-Request-Id")

	if f.err != nil {
		return f.err
	}
	if f.respond != nil {
		f.respond(w, r)
		return nil
	}

	w.Header().Set("Content-Type", v1.NDJSONMediaType)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"type":"exit","exitCode":0}`+"\n")
	return nil
}

// Health serves the health capability. A test that does not configure healthFn
// answers "no health published", which is the 204 contract.
func (f *fakeBroker) Health(ctx context.Context, task v1.Task) (*v1.TaskHealth, error) {
	f.healthMu.Lock()
	f.healthCalls++
	f.gotHealthTasks = append(f.gotHealthTasks, task)
	f.healthMu.Unlock()

	if f.healthFn != nil {
		return f.healthFn(ctx, task)
	}
	return nil, nil
}

type execTestAPI struct {
	*testAPI
	srv      *Server
	audit    *AuditStore
	broker   *fakeBroker
	client   *execFakeClient
	admin    string
	operator string
	reader   string
	byTask   string
	byLabel  string
}

func newExecTestAPI(t *testing.T, client *execFakeClient, broker *fakeBroker) *execTestAPI {
	t.Helper()
	if client == nil {
		client = &execFakeClient{fakeClient: &fakeClient{}}
	}
	if broker == nil {
		broker = &fakeBroker{}
	}

	store, err := OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	require.NoError(t, err)

	mint := func(name, role string, selectors *TaskSelectors) string {
		secret, _, err := store.CreateWithOptions(name, role, TokenOptions{TaskSelectors: selectors})
		require.NoError(t, err)
		return secret
	}

	audit, err := OpenAuditStore(filepath.Join(t.TempDir(), "audit.jsonl"), 0)
	require.NoError(t, err)

	srv, err := New(Config{
		Client:        client,
		Broker:        broker,
		Tokens:        store,
		Audit:         audit,
		ServerVersion: "0.11.0",
		GRPCVersion:   "v1",
		ClusterID:     "cluster-1",
		RateLimit:     NewRateLimiter(1e9, 1e9),
	})
	require.NoError(t, err)

	admin := mint("admin", RoleAdmin, nil)
	return &execTestAPI{
		testAPI: &testAPI{
			handler: srv.Handler(),
			unix:    srv.UnixHandler(),
			token:   admin,
			store:   store,
		},
		srv:      srv,
		audit:    audit,
		broker:   broker,
		client:   client,
		admin:    admin,
		operator: mint("deploy", RoleOperator, nil),
		reader:   mint("dashboard", RoleReader, nil),
		byTask:   mint("runner-task", RoleRunner, &TaskSelectors{TaskIDs: []string{"task-1"}}),
		byLabel:  mint("runner-label", RoleRunner, &TaskSelectors{Labels: map[string]string{"tier": "web"}}),
	}
}

func withExecBody(body string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Content-Type", MediaType)
		r.Body = io.NopCloser(strings.NewReader(body))
	}
}

func withExecToken(token string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	}
}

func postExec(t *testing.T, api *execTestAPI, token, body string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	all := append([]func(*http.Request){withExecToken(token), withExecBody(body)}, opts...)
	return api.do(t, http.MethodPost, "/v1/tasks/web-1/exec", all...)
}

func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) v1.ErrorDocument {
	t.Helper()

	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, v1.KindError, doc.Kind)
	return doc
}

func TestExecRequiresAuthentication(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := api.do(t, http.MethodPost, "/v1/tasks/web-1/exec", withExecBody(`{"command":["true"]}`))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Zero(t, api.broker.calls)
}

func TestExecRequiresScope(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := postExec(t, api, api.reader, `{"command":["true"]}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Zero(t, api.broker.calls)

	doc := decodeErrorBody(t, rec)
	require.Equal(t, string(output.CodeForbidden), doc.Error.Code)
	require.Equal(t, string(ScopeTasksExec), doc.Error.Details["requiredScope"])
}

func TestExecTaskSelectorMatch(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1", Name: "web.1"}
	api := newExecTestAPI(t, fc, nil)

	rec := postExec(t, api, api.byTask, `{"command":["true"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, api.broker.calls)
	require.Equal(t, "task-1", api.broker.gotTaskRef)
	require.Equal(t, "exec", api.broker.gotOp)
}

func TestExecTaskSelectorMismatch(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-2", ServiceID: "svc-1"}
	api := newExecTestAPI(t, fc, nil)

	rec := postExec(t, api, api.byTask, `{"command":["true"]}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Zero(t, api.broker.calls)

	doc := decodeErrorBody(t, rec)
	require.Equal(t, string(output.CodeForbidden), doc.Error.Code)
	require.Equal(t, "selector_mismatch", doc.Error.Details["reason"])
}

func TestExecLabelSelector(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1"}
	fc.service = v1.Service{ID: "svc-1", Labels: map[string]string{"tier": "web"}}
	api := newExecTestAPI(t, fc, nil)

	rec := postExec(t, api, api.byLabel, `{"command":["true"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "svc-1", fc.gotServiceID)

	fc.service.Labels = map[string]string{"tier": "db"}
	rec = postExec(t, api, api.byLabel, `{"command":["true"]}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, 1, api.broker.calls, "a mismatched selector must not reach the broker")
}

// TestExecLabelSelectorFailsClosedWhenServiceIsMissing pins that a selector that
// needs labels cannot be satisfied by a missing service document.
func TestExecLabelSelectorFailsClosedWhenServiceIsMissing(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-missing"}
	fc.serviceErr = output.NewError(output.CodeNotFound, "service not found")
	api := newExecTestAPI(t, fc, nil)

	rec := postExec(t, api, api.byLabel, `{"command":["true"]}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Zero(t, api.broker.calls)
}

// TestExecTaskSelectorSurvivesMissingService proves a task-ID selector does not
// need the service document.
func TestExecTaskSelectorSurvivesMissingService(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-missing"}
	fc.serviceErr = output.NewError(output.CodeNotFound, "service not found")
	api := newExecTestAPI(t, fc, nil)

	rec := postExec(t, api, api.byTask, `{"command":["true"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, api.broker.calls)
}

// TestExecRunnerWithoutSelectorIsRejected calls the handler directly so the
// identity can carry role runner with no selector: the token store refuses to
// mint such a token precisely so this cannot happen over HTTP.
func TestExecRunnerWithoutSelectorIsRejected(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.task = v1.Task{ID: "task-1"}

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/web-1/exec",
		strings.NewReader(`{"command":["true"]}`))
	req.Header.Set("Content-Type", MediaType)
	ctx := context.WithValue(req.Context(), requestIDKey{}, "req_test")
	ctx = withIdentity(ctx, Identity{
		Type:   "token",
		Role:   RoleRunner,
		Scopes: []Scope{ScopeTasksExec},
	})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	api.srv.execTask(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	doc := decodeErrorBody(t, rec)
	require.Equal(t, "selector_required", doc.Error.Details["reason"])
	require.Zero(t, api.broker.calls)
}

func TestExecProxyReceivesResolvedTaskID(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-resolved", ServiceID: "svc-1"}
	api := newExecTestAPI(t, fc, nil)

	rec := postExec(t, api, api.admin, `{"command":["true"]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "web-1", fc.gotTaskRef, "the user reference is resolved server side")
	require.Equal(t, "task-resolved", api.broker.gotTaskRef, "the broker gets the concrete task ID")
}

func TestExecForwardsRequestIDToBroker(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := postExec(t, api, api.admin, `{"command":["true"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	requestID := rec.Header().Get("X-Request-Id")
	require.NotEmpty(t, requestID)
	require.Equal(t, requestID, api.broker.gotRequestID)
}

func TestExecRejectsStdinWithoutTTY(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := postExec(t, api, api.admin, `{"command":["cat"],"stdin":true}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Zero(t, api.broker.calls)

	doc := decodeErrorBody(t, rec)
	require.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	require.Equal(t, "stdin_requires_tty", doc.Error.Details["reason"])
}

func TestExecRejectsTTYBeforeProxying(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := postExec(t, api, api.admin, `{"command":["sh"],"tty":true,"stdin":true}`)
	require.Equal(t, http.StatusNotImplemented, rec.Code)
	require.Zero(t, api.broker.calls)
	require.Empty(t, api.client.gotTaskRef, "tty must be refused before the task is resolved")

	doc := decodeErrorBody(t, rec)
	require.Equal(t, string(output.CodeNotImplemented), doc.Error.Code)
	require.Equal(t, "tty_not_supported", doc.Error.Details["reason"])
}

func TestExecValidatesBody(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"malformed", `{"command":`},
		{"empty command", `{"command":[]}`},
		{"negative timeout", `{"command":["true"],"timeoutMs":-1}`},
		{"env key with equals", `{"command":["env"],"env":{"A=B":"x"}}`},
		{"empty env key", `{"command":["env"],"env":{"":"x"}}`},
		{"unknown field", `{"command":["true"],"nope":1}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newExecTestAPI(t, nil, nil)
			rec := postExec(t, api, api.admin, tc.body)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Zero(t, api.broker.calls)
			doc := decodeErrorBody(t, rec)
			require.Equal(t, string(output.CodeInvalid), doc.Error.Code)
		})
	}
}

func TestExecRequiresContentType(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := api.do(t, http.MethodPost, "/v1/tasks/web-1/exec",
		withExecToken(api.admin),
		func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json")
			r.Body = io.NopCloser(strings.NewReader(`{"command":["true"]}`))
		})

	require.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
	require.Zero(t, api.broker.calls)
}

func TestExecBodyCap(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	big := `{"command":["` + strings.Repeat("a", 1<<20) + `"]}`
	rec := postExec(t, api, api.admin, big)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Zero(t, api.broker.calls)
}

func TestExecAcceptsNDJSONAccept(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := postExec(t, api, api.admin, `{"command":["true"]}`,
		func(r *http.Request) { r.Header.Set("Accept", v1.NDJSONMediaType) })
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, v1.NDJSONMediaType, rec.Header().Get("Content-Type"))
	require.Equal(t, "exec", api.broker.gotOp)
}

func TestExecWithoutBrokerIsNotImplemented(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	store, err := OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	require.NoError(t, err)
	secret, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	srv, err := New(Config{
		Client:    fc,
		Tokens:    store,
		RateLimit: NewRateLimiter(1e9, 1e9),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/web-1/exec",
		strings.NewReader(`{"command":["true"]}`))
	req.Header.Set("Content-Type", MediaType)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	doc := decodeErrorBody(t, rec)
	require.Equal(t, string(output.CodeNotImplemented), doc.Error.Code)
}

func TestExecProxyFailureIsReported(t *testing.T) {
	api := newExecTestAPI(t, nil, &fakeBroker{err: output.NewError(output.CodeUnavailable, "node offline")})

	rec := postExec(t, api, api.admin, `{"command":["true"]}`)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	doc := decodeErrorBody(t, rec)
	require.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
}

// TestExecRelayedNodeFailureIsAuditedAsError pins that a node error relayed by
// the broker is a failed exec even though Proxy itself returned nil: the guest
// agent was unavailable, so the command never ran.
func TestExecRelayedNodeFailureIsAuditedAsError(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1", Name: "web.1"}
	broker := &fakeBroker{respond: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", MediaType)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w,
			`{"schemaVersion":1,"kind":"Error","error":{"code":"unavailable","message":"guest agent is unavailable"}}`)
	}}
	api := newExecTestAPI(t, fc, broker)

	rec := postExec(t, api, api.admin, `{"command":["true"]}`)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "the relayed status reaches the caller")

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.exec", records[0].Action)
	assert.Equal(t, "error", records[0].Result,
		"a relayed node failure means the stream was never served")
	assert.Equal(t, "task-1", records[0].Resource.ID)
}

// TestExecStatusRecorderPreservesStreaming pins the wrapper the audit relies on:
// every write is passed straight through (no buffering) and the response
// controller can still reach the underlying Flush.
func TestExecStatusRecorderPreservesStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	recorder := &relayStatusRecorder{ResponseWriter: rec}

	require.Zero(t, recorder.Status(), "nothing has been written yet")
	require.Equal(t, http.ResponseWriter(rec), recorder.Unwrap(),
		"Unwrap must expose the underlying writer")

	require.NoError(t, http.NewResponseController(recorder).Flush(),
		"the recorder must not hide Flush")
	require.True(t, rec.Flushed, "the flush must reach the underlying writer")

	n, err := recorder.Write([]byte("frame\n"))
	require.NoError(t, err)
	require.Equal(t, len("frame\n"), n)
	require.Equal(t, "frame\n", rec.Body.String(), "writes must not be buffered")
	require.Equal(t, http.StatusOK, recorder.Status(), "an implicit 200 is recorded")
}

// TestExecStatusRecorderKeepsFirstFinalStatus pins that the first final status
// wins while an informational response does not latch.
func TestExecStatusRecorderKeepsFirstFinalStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	recorder := &relayStatusRecorder{ResponseWriter: rec}

	recorder.WriteHeader(http.StatusEarlyHints)
	require.Zero(t, recorder.Status(), "an informational response is not the outcome")

	recorder.WriteHeader(http.StatusServiceUnavailable)
	recorder.WriteHeader(http.StatusOK)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Status(),
		"the first final status is the one that was served")
}

func TestExecAuditRecordsOkAndError(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1", Name: "web.1"}
	api := newExecTestAPI(t, fc, nil)

	const secretValue = "not-in-the-audit-log"
	rec := postExec(t, api, api.admin, `{"command":["env"],"env":{"TOKEN":"`+secretValue+`"}}`)
	require.Equal(t, http.StatusOK, rec.Code)

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.exec", records[0].Action)
	assert.Equal(t, "ok", records[0].Result)
	assert.Equal(t, v1.KindTask, records[0].Resource.Kind)
	assert.Equal(t, "task-1", records[0].Resource.ID)

	// A selector rejection inside the handler is audited as an error.
	fc.task = v1.Task{ID: "task-2", ServiceID: "svc-1", Name: "web.2"}
	rec = postExec(t, api, api.byTask, `{"command":["true"]}`)
	require.Equal(t, http.StatusForbidden, rec.Code)

	records, _, err = api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "error", records[1].Result)
	assert.Equal(t, "task-2", records[1].Resource.ID)

	body, err := os.ReadFile(api.audit.path)
	require.NoError(t, err)
	assert.NotContains(t, string(body), secretValue,
		"the audit log must never carry environment values")
	assert.NotContains(t, string(body), `"command"`)
}

// TestWriteErrorUsesRequestID pins the exported hook the broker calls for a
// transport failure: the document is public and carries the request's ID.
func TestWriteErrorUsesRequestID(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/web-1/exec", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey{}, "req_public123"))
	rec := httptest.NewRecorder()

	WriteError(rec, req, output.NewError(output.CodeUnreachable, "node broker is unreachable"))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, MediaType, rec.Header().Get("Content-Type"))

	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, string(output.CodeUnreachable), doc.Error.Code)
	require.Equal(t, "req_public123", doc.Error.RequestID)
}

// TestExecOperatorRunsAnyTask pins that an operator without selectors is not
// restricted to a task list.
func TestExecOperatorRunsAnyTask(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-anything", ServiceID: "svc-1"}
	api := newExecTestAPI(t, fc, nil)

	rec := postExec(t, api, api.operator, `{"command":["true"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "task-anything", api.broker.gotTaskRef)
}
