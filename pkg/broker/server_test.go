package broker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// fakeTaskSource is a TaskSource with the answer and the failure under test.
type fakeTaskSource struct {
	task  Task
	found bool
	err   error
	gotID string
}

func (f *fakeTaskSource) Lookup(_ context.Context, taskID string) (Task, bool, error) {
	f.gotID = taskID
	if f.err != nil {
		return Task{}, false, f.err
	}
	return f.task, f.found, nil
}

// newRouteServer returns a server whose Handler can be exercised without
// binding a socket. TLS is still required by NewServer, so it is configured.
func newRouteServer(t *testing.T, src TaskSource) *Server {
	t.Helper()

	srv, err := NewServer(ServerConfig{
		Addr:      "127.0.0.1:0",
		TLSConfig: newTestPKI(t).serverConfig(),
		Tasks:     src,
	})
	require.NoError(t, err)
	return srv
}

// startTestBroker binds a real listener and serves the broker over mTLS until
// the test ends, returning the base URL clients should dial.
func startTestBroker(t *testing.T, pki testPKI, src TaskSource, ops map[string]Handler) (*Server, string) {
	t.Helper()

	srv, err := NewServer(ServerConfig{
		Addr:      "127.0.0.1:0",
		TLSConfig: pki.serverConfig(),
		Tasks:     src,
	})
	require.NoError(t, err)
	for op, handler := range ops {
		require.NoError(t, srv.Handle(op, handler))
	}

	lis, err := srv.Listen()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx, lis) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-errCh)
	})

	return srv, "https://" + lis.Addr().String()
}

// decodeErrorDocument decodes a broker error response and asserts the v1
// envelope every error shares.
func decodeErrorDocument(t *testing.T, body io.Reader) v1.ErrorDocument {
	t.Helper()

	var doc v1.ErrorDocument
	require.NoError(t, json.NewDecoder(body).Decode(&doc))
	require.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
	require.Equal(t, v1.KindError, doc.Kind)
	return doc
}

func TestServerRoutesToRegisteredHandler(t *testing.T) {
	src := &fakeTaskSource{
		task:  Task{ID: "task-1", SocketPath: "/var/run/firecracker/task-1.sock"},
		found: true,
	}
	srv := newRouteServer(t, src)

	type observed struct {
		task   Task
		method string
		query  string
		header string
		body   string
	}
	var got observed
	require.NoError(t, srv.Handle("exec", func(_ context.Context, task Task, w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		got = observed{
			task:   task,
			method: r.Method,
			query:  r.URL.RawQuery,
			header: r.Header.Get("X-Test"),
			body:   string(body),
		}
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-ref/exec?follow=1&tail=10", strings.NewReader("payload"))
	req.Header.Set("X-Test", "yes")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Equal(t, "task-ref", src.gotID, "the source is asked for the ref in the URL")
	require.Equal(t, Task{ID: "task-1", SocketPath: "/var/run/firecracker/task-1.sock"}, got.task,
		"the handler receives the resolved task, not the raw ref")
	require.Equal(t, http.MethodPost, got.method)
	require.Equal(t, "follow=1&tail=10", got.query)
	require.Equal(t, "yes", got.header)
	require.Equal(t, "payload", got.body)
}

func TestServerUnknownTaskIsNotFound(t *testing.T) {
	src := &fakeTaskSource{found: false}
	srv := newRouteServer(t, src)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/missing/exec", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "missing", src.gotID)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	doc := decodeErrorDocument(t, rec.Body)
	require.Equal(t, string(codeNotFound), doc.Error.Code)
	require.False(t, doc.Error.Retryable)
}

func TestServerUnknownOpIsNotImplemented(t *testing.T) {
	srv := newRouteServer(t, &fakeTaskSource{task: Task{ID: "task-1"}, found: true})

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/shell", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	doc := decodeErrorDocument(t, rec.Body)
	require.Equal(t, string(codeNotImplemented), doc.Error.Code)
	require.False(t, doc.Error.Retryable)
}

// TestServerWrongMethodIs405Before501 pins the precedence the node contract
// needs: a method the operation does not implement is 405 with Allow, even when
// the operation's handler is absent (its capability is disabled). Only the
// operation's real method can reach the 501.
func TestServerWrongMethodIs405Before501(t *testing.T) {
	srv := newRouteServer(t, &fakeTaskSource{task: Task{ID: "task-1"}, found: true})

	// exec is POST only. No handler is registered: the capability is disabled.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/exec", nil))

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	require.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
	doc := decodeErrorDocument(t, rec.Body)
	require.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	require.Equal(t, "method_not_allowed", doc.Error.Details["reason"])

	// The real method still answers 501 while the operation is disabled.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/exec", nil))
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
	require.Equal(t, string(codeNotImplemented), decodeErrorDocument(t, rec.Body).Error.Code)

	// files serves GET and PUT: DELETE is 405 with both methods advertised.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/tasks/task-1/files", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	require.Equal(t, http.MethodGet+", "+http.MethodPut, rec.Header().Get("Allow"))
}

func TestServerMalformedPathsAreNotFound(t *testing.T) {
	srv := newRouteServer(t, &fakeTaskSource{task: Task{ID: "task-1"}, found: true})

	for _, path := range []string{
		"/",
		"/v1",
		"/v1/tasks",
		"/v1/tasks/",
		"/v1/tasks/task-1",
		"/v1/tasks/task-1/",
		"/v1/tasks//exec",
		"/v1/tasks/task-1/exec/extra",
		"/health",
		"/v1/nodes/node-1/health",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			require.Equal(t, http.StatusNotFound, rec.Code)
			require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			doc := decodeErrorDocument(t, rec.Body)
			require.Equal(t, string(codeNotFound), doc.Error.Code)
		})
	}
}

func TestServerTaskSourceErrorIsUnavailable(t *testing.T) {
	srv := newRouteServer(t, &fakeTaskSource{err: errors.New("state directory unreadable")})

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/health", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	doc := decodeErrorDocument(t, rec.Body)
	require.Equal(t, string(codeUnavailable), doc.Error.Code)
	require.True(t, doc.Error.Retryable)
}

// TestServerServesLogsForAnExitedTask pins the log-only resolution end to end:
// with the VM gone and only its console log left, logs still serves the backlog
// while exec and files answer a typed 404 instead of dialing.
func TestServerServesLogsForAnExitedTask(t *testing.T) {
	socketDir := t.TempDir()
	logDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "task-1.log"), []byte("boom\n"), 0o600))

	srv := newRouteServer(t, SocketDirSource{Dir: socketDir, LogDir: logDir})
	require.NoError(t, srv.Handle("logs", NewLogsHandler()))
	require.NoError(t, srv.Handle("exec",
		NewExecHandler((&fakeDialer{guest: &fakeGuest{}}).dial)))
	require.NoError(t, srv.Handle("files",
		NewFilesHandler((&fakeDialer{guest: &fakeFileGuest{}}).dial)))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/logs", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	execReq := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/exec",
		strings.NewReader(`{"command":["true"]}`))
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, execReq)
	require.Equal(t, http.StatusNotFound, rec.Code, "exec needs a live guest")
	require.Equal(t, string(codeNotFound), decodeErrorDocument(t, rec.Body).Error.Code)

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/files?path=/etc/hostname", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, "files needs a live guest")
	require.Equal(t, string(codeNotFound), decodeErrorDocument(t, rec.Body).Error.Code)
}

func TestServerHandleRejectsBadRegistrations(t *testing.T) {
	srv := newRouteServer(t, &fakeTaskSource{})

	require.Error(t, srv.Handle("", func(context.Context, Task, http.ResponseWriter, *http.Request) {}))
	require.NoError(t, srv.Handle("exec", func(context.Context, Task, http.ResponseWriter, *http.Request) {}))
	require.Error(t, srv.Handle("exec", func(context.Context, Task, http.ResponseWriter, *http.Request) {}),
		"registering an operation twice must fail rather than silently replace it")
}

func TestNewServerRequiresTLSAndTaskSource(t *testing.T) {
	pki := newTestPKI(t)

	_, err := NewServer(ServerConfig{Tasks: &fakeTaskSource{}})
	require.Error(t, err, "a broker server must never run without TLS")

	_, err = NewServer(ServerConfig{TLSConfig: pki.serverConfig()})
	require.Error(t, err, "a broker server needs a task source")
}

func TestServerListenRecordsAddr(t *testing.T) {
	srv, err := NewServer(ServerConfig{
		Addr:      "127.0.0.1:0",
		TLSConfig: newTestPKI(t).serverConfig(),
		Tasks:     &fakeTaskSource{},
	})
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:0", srv.Addr(), "before Listen the configured address is reported")

	lis, err := srv.Listen()
	require.NoError(t, err)
	defer lis.Close()
	require.Equal(t, lis.Addr().String(), srv.Addr(), "after Listen the bound address is reported")
}

func TestServerServeAcceptsClusterMTLSClient(t *testing.T) {
	pki := newTestPKI(t)
	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"health": func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})

	resp, err := pki.httpClient().Get(baseURL + "/v1/tasks/task-1/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestServerServeRejectsClientWithoutCertificate(t *testing.T) {
	pki := newTestPKI(t)
	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"health": func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})

	resp, err := pki.clientWithoutCertificate().Get(baseURL + "/v1/tasks/task-1/health")
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err, "the broker must require a cluster client certificate")
}

func TestServerServeRejectsForeignCA(t *testing.T) {
	pki := newTestPKI(t)
	other := newTestPKI(t)
	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"health": func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	})

	foreign := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{other.cert},
			RootCAs:      pki.pool,
			MinVersion:   tls.VersionTLS12,
		}},
	}
	resp, err := foreign.Get(baseURL + "/v1/tasks/task-1/health")
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err, "a certificate from another CA must not be trusted")
}
