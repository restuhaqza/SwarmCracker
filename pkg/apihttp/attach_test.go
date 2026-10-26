package apihttp

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// postAttach sends one attach request through the authenticated handler.
func postAttach(t *testing.T, api *execTestAPI, token, path string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	all := append([]func(*http.Request){withExecToken(token)}, opts...)
	return api.do(t, http.MethodPost, path, all...)
}

// attachConsoleResponse is the fake node's answer: raw console bytes.
func attachConsoleResponse(body string) func(w http.ResponseWriter, _ *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}
}

// TestAttachWithoutBrokerIsNotImplemented pins the 501 contract: a manager with
// no node broker has no console path, and the answer names the capability so an
// SDK can feature-detect it.
func TestAttachWithoutBrokerIsNotImplemented(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/web-1/attach", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	doc := decodeErrorBody(t, rec)
	require.Equal(t, string(output.CodeNotImplemented), doc.Error.Code)
	require.Equal(t, "attach", doc.Error.Details["capability"])
	require.Empty(t, fc.gotTaskRef, "a 501 must not resolve the task")
}

// TestAttachProxiesResolvedTaskIDAndAudits pins the public handler's contract:
// the broker receives the resolved task ID and the operation name, the raw
// console bytes reach the caller unchanged, and the attempt is audited as
// task.attach with the resolved identity.
func TestAttachProxiesResolvedTaskIDAndAudits(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-resolved", ServiceID: "svc-1", Name: "web.1"}
	broker := &fakeBroker{respond: attachConsoleResponse("guest login: ")}
	api := newExecTestAPI(t, fc, broker)

	rec := postAttach(t, api, api.admin, "/v1/tasks/web-1/attach",
		func(r *http.Request) {
			r.Header.Set("Content-Type", "application/octet-stream")
			r.Body = io.NopCloser(strings.NewReader("whoami\n"))
		})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "guest login: ", rec.Body.String(), "console bytes are relayed unchanged")
	require.Equal(t, "web-1", fc.gotTaskRef, "the user reference is resolved server side")
	require.Equal(t, "task-resolved", broker.gotTaskRef, "the broker gets the concrete task ID")
	require.Equal(t, attachOperation, broker.gotOp)

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.attach", records[0].Action)
	assert.Equal(t, "ok", records[0].Result)
	assert.Equal(t, v1.KindTask, records[0].Resource.Kind)
	assert.Equal(t, "task-resolved", records[0].Resource.ID, "the audit names the resolved task")
	assert.Equal(t, "web.1", records[0].Resource.Name)
}

// TestAttachRelayedNodeFailureIsAuditedAsError pins that a node that answers
// "no console" is not a served session even though Proxy returned nil.
func TestAttachRelayedNodeFailureIsAuditedAsError(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1", Name: "web.1"}
	broker := &fakeBroker{respond: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", MediaType)
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w,
			`{"schemaVersion":1,"kind":"Error","error":{"code":"not_found","message":"task \"task-1\" has no console","details":{"reason":"console_unavailable"}}}`)
	}}
	api := newExecTestAPI(t, fc, broker)

	rec := postAttach(t, api, api.admin, "/v1/tasks/web-1/attach")

	require.Equal(t, http.StatusNotFound, rec.Code, "the relayed status reaches the caller")
	doc := decodeErrorBody(t, rec)
	require.Equal(t, "console_unavailable", doc.Error.Details["reason"])

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.attach", records[0].Action)
	assert.Equal(t, "error", records[0].Result, "a task with no console served no session")
	assert.Equal(t, "task-1", records[0].Resource.ID)
}

// TestAttachProxyFailureIsReported pins the resolve-failure path: Proxy returns
// an error, nothing has been written, and the caller gets the typed document.
func TestAttachProxyFailureIsReported(t *testing.T) {
	api := newExecTestAPI(t, nil, &fakeBroker{err: output.NewError(output.CodeUnavailable, "node offline")})

	rec := postAttach(t, api, api.admin, "/v1/tasks/web-1/attach")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	doc := decodeErrorBody(t, rec)
	require.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
}

// TestAttachRequiresExecScope pins the scope: attach hands over a console, which
// is exactly the privilege tasks:exec already carries.
func TestAttachRequiresExecScope(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := postAttach(t, api, api.reader, "/v1/tasks/web-1/attach")
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Zero(t, api.broker.calls)

	doc := decodeErrorBody(t, rec)
	require.Equal(t, string(output.CodeForbidden), doc.Error.Code)
	require.Equal(t, string(ScopeTasksExec), doc.Error.Details["requiredScope"])
}

// TestAttachRequiresAuthentication pins that the route is not reachable without
// a token on the TCP listener.
func TestAttachRequiresAuthentication(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := api.do(t, http.MethodPost, "/v1/tasks/web-1/attach")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Zero(t, api.broker.calls)
}

// TestAttachRejectsUnknownQueryParameters pins the T2 guard on this route: the
// transport already carries a TTY, so `?tty=` is a typo, not a mode.
func TestAttachRejectsUnknownQueryParameters(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := api.do(t, http.MethodPost, "/v1/tasks/web-1/attach?tty=true", withExecToken(api.admin))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Zero(t, api.broker.calls, "an uninterpretable request must not reach the node")

	doc := decodeErrorBody(t, rec)
	require.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	require.Equal(t, "tty", doc.Error.Details["parameter"])
}

// TestAttachEnforcesTaskSelectors pins that a runner token is confined to its
// tasks on the console route exactly as it is on exec.
func TestAttachEnforcesTaskSelectors(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-2", ServiceID: "svc-1", Name: "web.2"}
	api := newExecTestAPI(t, fc, nil)

	rec := postAttach(t, api, api.byTask, "/v1/tasks/web-2/attach")
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Zero(t, api.broker.calls)

	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "selector_mismatch", doc.Error.Details["reason"])
}

// TestAttachRouteIsPostOnly pins the method set of the public route.
func TestAttachRouteIsPostOnly(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := api.do(t, http.MethodGet, "/v1/tasks/web-1/attach", withExecToken(api.admin))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Contains(t, rec.Header().Get("Allow"), http.MethodPost)
}

// TestAttachBypassesIdempotency pins that a streaming POST is not wrapped by the
// Idempotency-Key middleware, which would buffer the console to replay it.
func TestAttachBypassesIdempotency(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1"}
	api := newExecTestAPI(t, fc, nil)

	rec := postAttach(t, api, api.admin, "/v1/tasks/web-1/attach",
		func(r *http.Request) { r.Header.Set("Idempotency-Key", "console-key") })
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Header().Get("Idempotency-Replayed"))
	require.Equal(t, 1, api.broker.calls)
}

// TestAttachKeepsFullDuplexThroughTheHandler is the public-side streaming proof:
// it drives the real handler and middleware over a real HTTP/1.1 server, with a
// fake broker that echoes one byte at a time. A byte written to the request must
// reach the "console" before the body closes, and a "console" byte must reach the
// caller before the console closes. The relayStatusRecorder wrapper must not hide
// EnableFullDuplex or Flush.
func TestAttachKeepsFullDuplexThroughTheHandler(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-1", ServiceID: "svc-1"}

	consoleGot := make(chan string, 1)
	broker := &fakeBroker{respond: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)

		stdin := make([]byte, 4)
		if _, err := io.ReadFull(r.Body, stdin); err != nil {
			return
		}
		consoleGot <- string(stdin)

		// Answer while the caller's body is still open. The real proxy flushes
		// every write; the controller reaches the underlying writer through
		// relayStatusRecorder's Unwrap.
		_, _ = io.WriteString(w, "pong")
		_ = http.NewResponseController(w).Flush()
		_, _ = io.Copy(io.Discard, r.Body)
	}}
	api := newExecTestAPI(t, fc, broker)

	srv := httptest.NewServer(api.handler)
	t.Cleanup(srv.Close)

	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/tasks/web-1/attach", stdinR)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+api.admin)
	req.Header.Set("Accept", MediaType)

	resCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			errCh <- doErr
			return
		}
		resCh <- resp
	}()

	_, err = stdinW.Write([]byte("ping"))
	require.NoError(t, err)

	select {
	case got := <-consoleGot:
		require.Equal(t, "ping", got, "stdin must reach the console before the body closes")
	case err := <-errCh:
		t.Fatalf("attach request: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("stdin was buffered: EnableFullDuplex did not reach the real writer")
	}

	var resp *http.Response
	select {
	case resp = <-resCh:
	case err := <-errCh:
		t.Fatalf("attach request: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the response never started")
	}
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	consoleOut := make(chan string, 1)
	go func() {
		buf := make([]byte, 4)
		if _, readErr := io.ReadFull(resp.Body, buf); readErr != nil {
			consoleOut <- ""
			return
		}
		consoleOut <- string(buf)
	}()

	select {
	case got := <-consoleOut:
		require.Equal(t, "pong", got, "a console byte must reach the caller while the console is open")
	case <-time.After(10 * time.Second):
		t.Fatal("the console byte was buffered behind the request body")
	}

	require.NoError(t, stdinW.Close())
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
}

// TestAttachClientAbortMidStreamIsAuditedOnce pins the abort path. The response
// copy in httputil.ReverseProxy panics http.ErrAbortHandler when its write to the
// caller fails, which is exactly what a Ctrl-C or a dropped tunnel produces. The
// handler must swallow that abort and still leave exactly one task.attach record
// naming the resolved task: the request was authorized and the console was
// reached, so a zero-record abort would hide the attempt from the audit log.
func TestAttachClientAbortMidStreamIsAuditedOnce(t *testing.T) {
	fc := &execFakeClient{fakeClient: &fakeClient{}}
	fc.task = v1.Task{ID: "task-abort", ServiceID: "svc-1", Name: "web.1"}

	writeFailed := make(chan struct{})
	broker := &fakeBroker{respond: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "boot")
		_ = http.NewResponseController(w).Flush()

		// Keep the console talking until the caller is gone, then abort exactly
		// as the relay does when its response copy fails. The deadline is a hang
		// guard, not the assertion: the write error is the trigger.
		chunk := []byte("console output, still typing on the client\r\n")
		expired := time.After(10 * time.Second)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-expired:
				return
			case <-ticker.C:
				_, writeErr := w.Write(chunk)
				flushErr := http.NewResponseController(w).Flush()
				if writeErr != nil || flushErr != nil {
					close(writeFailed)
					panic(http.ErrAbortHandler)
				}
			}
		}
	}}
	api := newExecTestAPI(t, fc, broker)

	srv := httptest.NewServer(api.handler)
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// A body the client never finishes: the caller is still "typing" when it
	// hangs up, which is what makes this a mid-stream abort.
	_, err = fmt.Fprintf(conn, "POST /v1/tasks/web-1/attach HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Authorization: Bearer %s\r\n"+
		"Content-Type: application/octet-stream\r\n"+
		"Content-Length: 1024\r\n\r\n"+
		"sta",
		srv.Listener.Addr().String(), api.admin)
	require.NoError(t, err)

	// Wait for the session to start before hanging up.
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	buf := make([]byte, 1024)
	seen := make([]byte, 0, 4096)
	for !strings.Contains(string(seen), "boot") {
		n, readErr := conn.Read(buf)
		require.NoError(t, readErr, "the session never started")
		seen = append(seen, buf[:n]...)
	}

	// Hang up mid-stream. Linger 0 sends a reset, so the next write from the
	// server cannot be absorbed by a socket buffer.
	tcp, ok := conn.(*net.TCPConn)
	require.True(t, ok)
	require.NoError(t, tcp.SetLinger(0))
	require.NoError(t, conn.Close())

	select {
	case <-writeFailed:
	case <-time.After(10 * time.Second):
		t.Fatal("the response copy never noticed the aborted caller")
	}

	// One record, written by the deferred audit after the panic is recovered.
	require.Eventually(t, func() bool {
		records, _, listErr := api.audit.List(10, "")
		return listErr == nil && len(records) == 1
	}, 5*time.Second, 5*time.Millisecond,
		"an aborted session must leave exactly one audit record")

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.attach", records[0].Action)
	assert.Equal(t, "error", records[0].Result, "an aborted session is not a completed one")
	assert.Equal(t, "task-abort", records[0].Resource.ID, "the audit names the resolved task")
	assert.Equal(t, "web.1", records[0].Resource.Name)
}

// TestVersionAdvertisesAttach pins that the version document and the route table
// agree: a wired broker serves attach, so it is advertised.
func TestVersionAdvertisesAttach(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := api.do(t, http.MethodGet, "/v1/version", withExecToken(api.admin))
	require.Equal(t, http.StatusOK, rec.Code)

	var info v1.VersionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	assert.Contains(t, info.Capabilities, attachOperation)
}
