package broker

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

type stubTaskReader struct {
	task   v1.Task
	err    error
	gotRef string
}

func (s *stubTaskReader) GetTask(_ context.Context, ref string) (v1.Task, error) {
	s.gotRef = ref
	return s.task, s.err
}

type stubNodeReader struct {
	node  v1.Node
	err   error
	gotID string
}

func (s *stubNodeReader) GetNode(_ context.Context, nodeID string) (v1.Node, error) {
	s.gotID = nodeID
	return s.node, s.err
}

func newTestManager(t *testing.T, tasks TaskReader, nodes NodeReader, port int, client *http.Client) *Manager {
	t.Helper()

	mgr, err := NewManager(ManagerConfig{Tasks: tasks, Nodes: nodes, Port: port, Client: client})
	require.NoError(t, err)
	return mgr
}

func TestManagerResolve(t *testing.T) {
	managerStatus := func(address string) *v1.NodeManager {
		return &v1.NodeManager{Address: address}
	}

	cases := []struct {
		name     string
		task     v1.Task
		node     v1.Node
		port     int
		op       string
		wantURL  string
		wantCode output.Code
	}{
		{
			name:    "node address as an IP",
			task:    v1.Task{ID: "task-1", NodeID: "node-1"},
			node:    v1.Node{ID: "node-1", Address: "192.168.1.10"},
			op:      "exec",
			wantURL: "https://192.168.1.10:4245/v1/tasks/task-1/exec",
		},
		{
			name:    "node address carrying a port",
			task:    v1.Task{ID: "task-1", NodeID: "node-1"},
			node:    v1.Node{ID: "node-1", Address: "10.0.0.5:9999"},
			op:      "logs",
			wantURL: "https://10.0.0.5:4245/v1/tasks/task-1/logs",
		},
		{
			name:    "IPv6 literal",
			task:    v1.Task{ID: "task-1", NodeID: "node-1"},
			node:    v1.Node{ID: "node-1", Address: "fd00::1"},
			op:      "files",
			wantURL: "https://[fd00::1]:4245/v1/tasks/task-1/files",
		},
		{
			name:    "IPv6 literal carrying a port",
			task:    v1.Task{ID: "task-1", NodeID: "node-1"},
			node:    v1.Node{ID: "node-1", Address: "[fd00::1]:9999"},
			op:      "health",
			wantURL: "https://[fd00::1]:4245/v1/tasks/task-1/health",
		},
		{
			name:    "explicit broker port",
			task:    v1.Task{ID: "task-1", NodeID: "node-1"},
			node:    v1.Node{ID: "node-1", Address: "192.168.1.10"},
			port:    5000,
			op:      "exec",
			wantURL: "https://192.168.1.10:5000/v1/tasks/task-1/exec",
		},
		{
			name:    "fallback to the manager status address",
			task:    v1.Task{ID: "task-1", NodeID: "node-1"},
			node:    v1.Node{ID: "node-1", Manager: managerStatus("172.16.0.9:4242")},
			op:      "exec",
			wantURL: "https://172.16.0.9:4245/v1/tasks/task-1/exec",
		},
		{
			name:    "fallback to an IPv6 manager status address",
			task:    v1.Task{ID: "task-1", NodeID: "node-1"},
			node:    v1.Node{ID: "node-1", Manager: managerStatus("[fd00::2]:4242")},
			op:      "exec",
			wantURL: "https://[fd00::2]:4245/v1/tasks/task-1/exec",
		},
		{
			name:     "task without a node",
			task:     v1.Task{ID: "task-1"},
			node:     v1.Node{ID: "node-1", Address: "192.168.1.10"},
			op:       "exec",
			wantCode: output.CodeUnavailable,
		},
		{
			name:     "node without any address",
			task:     v1.Task{ID: "task-1", NodeID: "node-1"},
			node:     v1.Node{ID: "node-1"},
			op:       "exec",
			wantCode: output.CodeUnavailable,
		},
		{
			name:     "uppercase operation",
			task:     v1.Task{ID: "task-1", NodeID: "node-1"},
			node:     v1.Node{ID: "node-1", Address: "192.168.1.10"},
			op:       "Exec",
			wantCode: output.CodeInvalid,
		},
		{
			name:     "operation with a path separator",
			task:     v1.Task{ID: "task-1", NodeID: "node-1"},
			node:     v1.Node{ID: "node-1", Address: "192.168.1.10"},
			op:       "logs/../../etc",
			wantCode: output.CodeInvalid,
		},
		{
			name:     "empty operation",
			task:     v1.Task{ID: "task-1", NodeID: "node-1"},
			node:     v1.Node{ID: "node-1", Address: "192.168.1.10"},
			op:       "",
			wantCode: output.CodeInvalid,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &stubTaskReader{task: tc.task}
			nodes := &stubNodeReader{node: tc.node}
			mgr := newTestManager(t, tasks, nodes, tc.port, &http.Client{})

			route, err := mgr.Resolve(context.Background(), "task-ref", tc.op)

			if tc.wantCode != "" {
				var typed *output.Error
				require.ErrorAs(t, err, &typed)
				require.Equal(t, tc.wantCode, typed.Code)
				return
			}

			require.NoError(t, err)
			require.Equal(t, "task-ref", tasks.gotRef)
			require.Equal(t, tc.task.NodeID, nodes.gotID)
			require.Equal(t, tc.task, route.Task)
			require.Equal(t, tc.node, route.Node)
			require.Equal(t, tc.wantURL, route.URL.String())
		})
	}
}

func TestManagerResolveDefaultsPort(t *testing.T) {
	mgr := newTestManager(t,
		&stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		&stubNodeReader{node: v1.Node{ID: "node-1", Address: "192.168.1.10"}},
		0,
		&http.Client{},
	)

	route, err := mgr.Resolve(context.Background(), "task-1", "exec")
	require.NoError(t, err)
	require.Equal(t, "4245", route.URL.Port())
}

func TestManagerResolveReturnsSourceErrorsUnchanged(t *testing.T) {
	taskErr := errors.New("task store unavailable")
	mgr := newTestManager(t,
		&stubTaskReader{err: taskErr},
		&stubNodeReader{},
		0,
		&http.Client{},
	)
	_, err := mgr.Resolve(context.Background(), "task-1", "exec")
	require.ErrorIs(t, err, taskErr)

	nodeErr := errors.New("node store unavailable")
	mgr = newTestManager(t,
		&stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		&stubNodeReader{err: nodeErr},
		0,
		&http.Client{},
	)
	_, err = mgr.Resolve(context.Background(), "task-1", "exec")
	require.ErrorIs(t, err, nodeErr)
}

func TestNewManagerRequiresDependencies(t *testing.T) {
	_, err := NewManager(ManagerConfig{Nodes: &stubNodeReader{}, Client: &http.Client{}})
	require.Error(t, err, "tasks are required")

	_, err = NewManager(ManagerConfig{Tasks: &stubTaskReader{}, Client: &http.Client{}})
	require.Error(t, err, "nodes are required")

	_, err = NewManager(ManagerConfig{Tasks: &stubTaskReader{}, Nodes: &stubNodeReader{}})
	require.Error(t, err, "a client carrying the cluster mTLS transport is required")

	_, err = NewManager(ManagerConfig{
		Tasks:  &stubTaskReader{},
		Nodes:  &stubNodeReader{},
		Port:   70000,
		Client: &http.Client{},
	})
	require.Error(t, err, "an out-of-range port must be rejected")
}

// freePort returns a loopback port that was free a moment ago, for the
// unreachable-target test.
func freePort(t *testing.T) int {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := lis.Addr().(*net.TCPAddr).Port
	require.NoError(t, lis.Close())
	return port
}

// brokerTarget parses a broker base URL into the node address and port the
// manager would resolve from the task's node.
func brokerTarget(t *testing.T, baseURL string) (string, int) {
	t.Helper()

	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return u.Hostname(), port
}

func TestManagerProxyForwardsWithoutUserCredentials(t *testing.T) {
	pki := newTestPKI(t)

	type observed struct {
		task   string
		path   string
		method string
		query  string
		body   string
		keep   string
		auth   string
		cookie string
	}
	var got observed

	src := &fakeTaskSource{task: Task{ID: "task-1", SocketPath: "/var/run/firecracker/task-1.sock"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"exec": func(_ context.Context, task Task, w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			got = observed{
				task:   task.ID,
				path:   r.URL.Path,
				method: r.Method,
				query:  r.URL.RawQuery,
				body:   string(body),
				keep:   r.Header.Get("X-Keep"),
				auth:   r.Header.Get("Authorization") + r.Header.Get("Proxy-Authorization"),
				cookie: r.Header.Get("Cookie"),
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"kind":"ExecResult","ok":true}`)
		},
	})

	host, port := brokerTarget(t, baseURL)
	mgr := newTestManager(t,
		&stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		&stubNodeReader{node: v1.Node{ID: "node-1", Address: host}},
		port,
		pki.httpClient(),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-ref/exec?wait=1&follow=true", strings.NewReader("payload"))
	req.Header.Set("Authorization", "Bearer public-user-token")
	req.Header.Set("Proxy-Authorization", "Basic cHJveHk=")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("X-Keep", "kept")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	require.NoError(t, mgr.Proxy(context.Background(), rec, req, "task-ref", "exec"))

	require.Equal(t, http.StatusCreated, rec.Code)
	require.JSONEq(t, `{"kind":"ExecResult","ok":true}`, rec.Body.String())

	require.Equal(t, "task-1", got.task, "the node resolves the task, not the user's ref")
	require.Equal(t, "/v1/tasks/task-1/exec", got.path)
	require.Equal(t, http.MethodPost, got.method)
	require.Equal(t, "wait=1&follow=true", got.query)
	require.Equal(t, "payload", got.body)
	require.Equal(t, "kept", got.keep)
	require.Empty(t, got.auth, "the node broker must never see a public or proxy credential")
	require.Empty(t, got.cookie, "the node broker must never see the caller's cookies")
}

func TestManagerProxyUnreachableNodeIs503(t *testing.T) {
	port := freePort(t)
	mgr := newTestManager(t,
		&stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		&stubNodeReader{node: v1.Node{ID: "node-1", Address: "127.0.0.1"}},
		port,
		&http.Client{Transport: &http.Transport{}},
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-ref/health", nil)
	rec := httptest.NewRecorder()
	require.NoError(t, mgr.Proxy(context.Background(), rec, req, "task-ref", "health"),
		"a transport failure is rendered by Proxy, not returned")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	doc := decodeErrorDocument(t, rec.Body)
	require.Equal(t, string(codeUnreachable), doc.Error.Code)
	require.True(t, doc.Error.Retryable)
}

func TestManagerProxyReturnsResolveErrors(t *testing.T) {
	mgr := newTestManager(t,
		&stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		&stubNodeReader{node: v1.Node{ID: "node-1", Address: "127.0.0.1"}},
		0,
		&http.Client{},
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-ref/health", nil)
	rec := httptest.NewRecorder()
	err := mgr.Proxy(context.Background(), rec, req, "task-ref", "Health")

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, output.CodeInvalid, typed.Code)
	require.Equal(t, http.StatusOK, rec.Code, "nothing is written when resolution fails")
	require.Empty(t, rec.Body.String())
}

// TestManagerProxyRewritesRelayedNodeErrors proves the Task 17 parked finding:
// a node error relayed through the manager carries the public media type, the
// API version header and the caller's request ID.
func TestManagerProxyRewritesRelayedNodeErrors(t *testing.T) {
	pki := newTestPKI(t)

	cases := []struct {
		status int
		code   output.Code
	}{
		{http.StatusBadRequest, output.CodeInvalid},
		{http.StatusNotFound, codeNotFound},
		{http.StatusRequestEntityTooLarge, output.CodeInvalid},
		{http.StatusUnsupportedMediaType, output.CodeInvalid},
		{http.StatusInternalServerError, output.CodeInternal},
		{http.StatusNotImplemented, codeNotImplemented},
		{http.StatusServiceUnavailable, codeUnavailable},
	}

	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
			_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
				"exec": func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
					writeError(w, tc.status, tc.code, "node says no")
				},
			})

			host, port := brokerTarget(t, baseURL)
			mgr := newTestManager(t,
				&stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
				&stubNodeReader{node: v1.Node{ID: "node-1", Address: host}},
				port,
				pki.httpClient(),
			)

			req := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-ref/exec", nil)
			req.Header.Set("X-Request-Id", "req_abc123def456")
			rec := httptest.NewRecorder()
			require.NoError(t, mgr.Proxy(context.Background(), rec, req, "task-ref", "exec"))

			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, publicMediaType, rec.Header().Get("Content-Type"))
			require.Equal(t, publicAPIVersion, rec.Header().Get(apiVersionHeader))

			doc := decodeErrorDocument(t, rec.Body)
			require.Equal(t, string(tc.code), doc.Error.Code)
			require.Equal(t, "req_abc123def456", doc.Error.RequestID,
				"the relayed error must carry the caller's request ID")
		})
	}
}

// TestManagerProxyLeavesSuccessStreamsUntouched proves ModifyResponse only
// rewrites error documents: a 200 NDJSON stream keeps its media type and bytes.
func TestManagerProxyLeavesSuccessStreamsUntouched(t *testing.T) {
	pki := newTestPKI(t)

	const body = "{\"type\":\"stdout\",\"data\":\"aGkK\"}\n{\"type\":\"exit\",\"exitCode\":0}\n"

	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"exec": func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, body)
		},
	})

	host, port := brokerTarget(t, baseURL)
	mgr := newTestManager(t,
		&stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		&stubNodeReader{node: v1.Node{ID: "node-1", Address: host}},
		port,
		pki.httpClient(),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-ref/exec", nil)
	req.Header.Set("X-Request-Id", "req_abc123def456")
	rec := httptest.NewRecorder()
	require.NoError(t, mgr.Proxy(context.Background(), rec, req, "task-ref", "exec"))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))
	require.Equal(t, body, rec.Body.String())
}

// TestManagerProxyUsesErrorWriterForTransportFailures proves the public handler
// owns the envelope: a nil hook keeps the internal default, and a configured
// hook receives a typed unreachable failure.
func TestManagerProxyUsesErrorWriterForTransportFailures(t *testing.T) {
	port := freePort(t)

	type observed struct {
		called bool
		code   output.Code
		req    *http.Request
	}
	got := observed{}

	mgr, err := NewManager(ManagerConfig{
		Tasks:  &stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		Nodes:  &stubNodeReader{node: v1.Node{ID: "node-1", Address: "127.0.0.1"}},
		Port:   port,
		Client: &http.Client{Transport: &http.Transport{}},
		ErrorWriter: func(w http.ResponseWriter, r *http.Request, err error) {
			got.called = true
			got.req = r
			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			got.code = typed.Code
			w.Header().Set("Content-Type", publicMediaType)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"Error","error":{"code":"unreachable","message":"x","retryable":true}}`)
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-ref/exec", nil)
	rec := httptest.NewRecorder()
	require.NoError(t, mgr.Proxy(context.Background(), rec, req, "task-ref", "exec"),
		"a transport failure is rendered by Proxy, not returned")

	require.True(t, got.called, "ManagerConfig.ErrorWriter must render the failure")
	require.Equal(t, output.CodeUnreachable, got.code)
	require.NotNil(t, got.req)
	require.Equal(t, publicMediaType, rec.Header().Get("Content-Type"))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestManagerProxyStreamsFlushedChunks proves FlushInterval: -1: the handler
// sets a Content-Length and flushes the first chunk, then waits. A buffering
// proxy would never deliver the first chunk before the handler returns.
func TestManagerProxyStreamsFlushedChunks(t *testing.T) {
	pki := newTestPKI(t)

	const first = "{\"stream\":\"one\"}\n"
	const second = "{\"stream\":\"two\"}\n"

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseChunks := func() { releaseOnce.Do(func() { close(release) }) }

	src := &fakeTaskSource{task: Task{ID: "task-1"}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"logs": func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error("broker response writer is not a flusher")
				return
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Content-Length", strconv.Itoa(len(first)+len(second)))
			_, _ = io.WriteString(w, first)
			flusher.Flush()
			<-release
			_, _ = io.WriteString(w, second)
			flusher.Flush()
		},
	})

	host, port := brokerTarget(t, baseURL)
	mgr := newTestManager(t,
		&stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		&stubNodeReader{node: v1.Node{ID: "node-1", Address: host}},
		port,
		pki.httpClient(),
	)

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := mgr.Proxy(r.Context(), w, r, "task-ref", "logs"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer proxySrv.Close()
	// Registered after Close so it runs first: a failure before the release
	// would otherwise leave the handler blocked and hang the cleanup.
	defer releaseChunks()

	resp, err := http.Get(proxySrv.URL + "/v1/tasks/task-ref/logs")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	reader := bufio.NewReader(resp.Body)
	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		lineCh <- line
	}()

	select {
	case line := <-lineCh:
		require.Equal(t, first, line)
	case err := <-errCh:
		t.Fatalf("reading the first chunk: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the first chunk was buffered: FlushInterval must be -1")
	}

	releaseChunks()

	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, second, string(rest))
}
