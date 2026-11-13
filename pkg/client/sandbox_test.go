package client

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// sandboxHandler is the sandbox API as this client sees it: it answers the routes the
// client speaks and records what the client asked for, so the transport and the
// decoding are what these tests hold to account.
type sandboxHandler struct {
	requests []*http.Request
	bodies   []string
	// respond overrides the answer for a path prefix.
	respond func(w http.ResponseWriter, r *http.Request) bool
}

func (h *sandboxHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	h.requests = append(h.requests, r)
	h.bodies = append(h.bodies, string(body))

	if h.respond != nil && h.respond(w, r) {
		return
	}

	w.Header().Set("Content-Type", sandboxTestMediaType)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes":
		_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"SandboxList","items":[
			{"id":"a","state":"ready","template":"base@1","guestIp":"10.63.0.2"},{"id":"b","state":"failed","reason":"gone"}],"next":null}`)
	case r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"Sandbox","id":"a","state":"ready"}`)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
		w.Header().Set("Content-Type", v1.NDJSONMediaType)
		_, _ = io.WriteString(w, `{"type":"stdout","data":"b3V0Cg=="}`+"\n")
		_, _ = io.WriteString(w, `{"type":"stderr","data":"ZXJyCg=="}`+"\n")
		_, _ = io.WriteString(w, `{"type":"exit","exitCode":7}`+"\n")
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tokens"):
		w.Header().Set("Content-Type", sandboxTestMediaType)
		_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"SandboxToken","id":"tok_1","name":"n","sandbox":"a","scopes":["sandboxes:read"],"token":"sc_x"}`)
	case r.Method == http.MethodPost:
		w.Header().Set("Content-Type", sandboxTestMediaType)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"Sandbox","id":"minted","state":"ready","template":"base@1"}`)
	case r.Method == http.MethodDelete:
		w.Header().Set("Content-Type", sandboxTestMediaType)
		_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"SandboxRemoval","sandbox":"a","freed":{"processStopped":true,"recordRemoved":true,"apiTokensRevoked":2}}`)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// sandboxTestMediaType is the v1 media type the client asks for and the server answers.
const sandboxTestMediaType = "application/vnd.swarmcracker.v1+json"

// sandboxUnixServer serves the sandbox handler on a Unix socket, which is the local
// path the CLI takes: the socket's file mode is the credential, and no token is sent.
func sandboxUnixServer(t *testing.T, handler http.Handler) string {
	t.Helper()

	// The socket path must be short: a unix address holds 107 bytes.
	dir, err := os.MkdirTemp("", "sbxcl")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "sandboxd.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)

	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	return socket
}

func newUnixSandboxClient(t *testing.T, handler http.Handler) (*SandboxClient, *sandboxHandler) {
	t.Helper()
	recorder, ok := handler.(*sandboxHandler)
	require.True(t, ok, "the helper expects the recording handler")

	socket := sandboxUnixServer(t, handler)
	client, err := NewSandboxClient(Config{SandboxSocket: socket})
	require.NoError(t, err)
	t.Cleanup(client.Close)
	assert.Equal(t, socket, client.Socket())
	return client, recorder
}

// TestSandboxClientReadsTheDocuments pins the local path end to end: the list, one
// document, and the transports they take.
func TestSandboxClientReadsTheDocuments(t *testing.T) {
	client, recorder := newUnixSandboxClient(t, &sandboxHandler{})
	ctx := context.Background()

	items, err := client.ListSandboxes(ctx)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "a", items[0].ID)
	assert.Equal(t, "failed", items[1].State, "a failed sandbox is a document, not an error")

	one, err := client.GetSandbox(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, "a", one.ID)
	assert.Equal(t, "ready", one.State)

	require.NotEmpty(t, recorder.requests)
	last := recorder.requests[len(recorder.requests)-1]
	assert.Equal(t, sandboxTestMediaType, last.Header.Get("Accept"), "the client asks for the v1 media type")
	assert.Empty(t, last.Header.Get("Authorization"), "a local request carries no token: the socket is the credential")
}

// TestSandboxClientWrites pins the mutating calls: the body the create sends, the
// document it reads back, and the removal report.
func TestSandboxClientWrites(t *testing.T) {
	client, recorder := newUnixSandboxClient(t, &sandboxHandler{})
	ctx := context.Background()

	created, err := client.CreateSandbox(ctx, v1.SandboxCreate{
		Template: "base@1", Egress: []string{"example.com:443"}, IdleTTLSeconds: 60,
	})
	require.NoError(t, err)
	assert.Equal(t, "minted", created.ID)
	assert.Equal(t, "ready", created.State)

	var sent v1.SandboxCreate
	require.NoError(t, json.Unmarshal([]byte(recorder.bodies[len(recorder.bodies)-1]), &sent))
	assert.Equal(t, "base@1", sent.Template)
	assert.Equal(t, []string{"example.com:443"}, sent.Egress)

	removal, err := client.DeleteSandbox(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, "a", removal.Sandbox)
	assert.True(t, removal.Freed.RecordRemoved)
	assert.Equal(t, 2, removal.Freed.APITokensRevoked)

	token, err := client.CreateSandboxToken(ctx, "a", v1.SandboxTokenRequest{Name: "ci"})
	require.NoError(t, err)
	assert.Equal(t, "sc_x", token.Secret)
	assert.Equal(t, "a", token.Sandbox)

	// The token route is a POST with a body, so the client must send the media type.
	last := recorder.requests[len(recorder.requests)-1]
	assert.Equal(t, sandboxTestMediaType, last.Header.Get("Content-Type"))
}

// TestSandboxClientStreamsExec pins the exec path: the frames become the caller's
// writers, and the guest's exit status is the result — not an error.
func TestSandboxClientStreamsExec(t *testing.T) {
	client, recorder := newUnixSandboxClient(t, &sandboxHandler{})

	var stdout, stderr strings.Builder
	result, err := client.ExecSandbox(context.Background(), "a",
		v1.ExecRequest{Command: []string{"sh", "-c", "exit 7"}}, &stdout, &stderr)
	require.NoError(t, err)

	assert.Equal(t, 7, result.ExitCode)
	assert.Equal(t, "out\n", stdout.String())
	assert.Equal(t, "err\n", stderr.String())

	last := recorder.requests[len(recorder.requests)-1]
	assert.Equal(t, "/v1/sandboxes/a/exec", last.URL.Path, "the command goes to the sandbox's own exec route")
	var sent v1.ExecRequest
	require.NoError(t, json.Unmarshal([]byte(recorder.bodies[len(recorder.bodies)-1]), &sent))
	assert.Equal(t, []string{"sh", "-c", "exit 7"}, sent.Command)
}

// TestSandboxClientReturnsTheTypedRefusal pins the error path: the API's error document
// becomes an error with the same code and details, so a caller can tell a selector
// mismatch from a missing scope without parsing a message.
func TestSandboxClientReturnsTheTypedRefusal(t *testing.T) {
	handler := &sandboxHandler{respond: func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/exec") {
			return false
		}
		w.Header().Set("Content-Type", sandboxTestMediaType)
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"Error","error":{"code":"forbidden",
			"message":"token sandbox selectors do not match this sandbox","retryable":false,
			"details":{"reason":"sandbox_selector_mismatch","sandboxId":"b"}}}`)
		return true
	}}
	client, _ := newUnixSandboxClient(t, handler)

	_, err := client.ExecSandbox(context.Background(), "b", v1.ExecRequest{Command: []string{"true"}}, io.Discard, io.Discard)
	require.Error(t, err)

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeForbidden, typed.Code)
	assert.Equal(t, "sandbox_selector_mismatch", typed.Details["reason"])
	assert.Equal(t, "b", typed.Details["sandboxId"])
}

// TestSandboxClientNamesTheSocketItCouldNotReach pins the failure a caller meets first:
// the daemon is not running, and "connection refused" alone would not say where.
func TestSandboxClientNamesTheSocketItCouldNotReach(t *testing.T) {
	dir, err := os.MkdirTemp("", "sbxcl")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "absent.sock")

	client, err := NewSandboxClient(Config{SandboxSocket: socket})
	require.NoError(t, err)
	defer client.Close()

	_, err = client.ListSandboxes(context.Background())
	require.Error(t, err)

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeUnreachable, typed.Code)
	assert.Contains(t, typed.Message, socket)
	assert.Contains(t, typed.Message, "is sandboxd running?")
}

// TestSandboxClientSpeaksTLSWhenGivenAnEndpoint pins the remote path: the same routes
// over TLS with a bearer token, verified against the CA the caller named.
func TestSandboxClientSpeaksTLSWhenGivenAnEndpoint(t *testing.T) {
	handler := &sandboxHandler{}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	caFile := filepath.Join(t.TempDir(), "ca.crt")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	require.NoError(t, os.WriteFile(caFile, pemBytes, 0o600))

	client, err := NewSandboxClient(Config{Endpoint: server.URL, CAFile: caFile, Token: "sc_secret"})
	require.NoError(t, err)
	defer client.Close()
	assert.Empty(t, client.Socket(), "a remote client has no local socket to name")

	_, err = client.ListSandboxes(context.Background())
	require.NoError(t, err)

	last := handler.requests[len(handler.requests)-1]
	assert.Equal(t, "Bearer sc_secret", last.Header.Get("Authorization"))

	// Without a CA the client refuses to build: there is no unverified fallback.
	_, err = NewSandboxClient(Config{Endpoint: server.URL})
	require.Error(t, err)
}

// TestSandboxSocketPathResolvesInOneOrder pins the one rule the daemon and the CLI
// share about where the control socket is.
func TestSandboxSocketPathResolvesInOneOrder(t *testing.T) {
	t.Setenv(SandboxSocketEnv, "/from/env.sock")
	assert.Equal(t, "/explicit.sock", SandboxSocketPath("/explicit.sock"))
	assert.Equal(t, "/from/env.sock", SandboxSocketPath(""))
	assert.Equal(t, "/from/env.sock", SandboxSocketPath("   "))

	t.Setenv(SandboxSocketEnv, "")
	assert.Equal(t, DefaultSandboxSocket, SandboxSocketPath(""))
}

// TestSandboxRequestTimeoutIsNotTheReadsDefault pins the timeout a create needs: the
// manager API's read-sized default (which DefaultConfig carries) would time out a boot.
func TestSandboxRequestTimeoutIsNotTheReadsDefault(t *testing.T) {
	assert.Equal(t, sandboxClientTimeout, sandboxRequestTimeout(0))
	assert.Equal(t, sandboxClientTimeout, sandboxRequestTimeout(DefaultConfig().Timeout))
	assert.Equal(t, 3*time.Second, sandboxRequestTimeout(3*time.Second))
}

// TestSandboxClientRefusesBadReferences pins the local validation: an empty or dot
// segment never reaches the wire.
func TestSandboxClientRefusesBadReferences(t *testing.T) {
	client, recorder := newUnixSandboxClient(t, &sandboxHandler{})
	before := len(recorder.requests)

	for _, id := range []string{"", ".", ".."} {
		_, err := client.GetSandbox(context.Background(), id)
		require.Error(t, err, "id %q", id)
		_, err = client.DeleteSandbox(context.Background(), id)
		require.Error(t, err, "id %q", id)
		_, err = client.ExecSandbox(context.Background(), id, v1.ExecRequest{Command: []string{"true"}}, io.Discard, io.Discard)
		require.Error(t, err, "id %q", id)
		_, err = client.CreateSandboxToken(context.Background(), id, v1.SandboxTokenRequest{})
		require.Error(t, err, "id %q", id)
	}
	assert.Len(t, recorder.requests, before, "a refused reference is never sent")
}

// TestSandboxClientReportsWhatTheDaemonRefused pins that a refusal keeps its code: a
// caller branches on not_found and conflict, not on a message.
func TestSandboxClientReportsWhatTheDaemonRefused(t *testing.T) {
	handler := &sandboxHandler{respond: func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", sandboxTestMediaType)
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"Error","error":{"code":"not_found","message":"no such sandbox"}}`)
		default:
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"Error","error":{"code":"conflict","message":"no free disk"}}`)
		}
		return true
	}}
	client, _ := newUnixSandboxClient(t, handler)

	_, err := client.GetSandbox(context.Background(), "a")
	require.Error(t, err)
	assert.Equal(t, output.CodeNotFound, codeOf(t, err).Code)

	_, err = client.CreateSandbox(context.Background(), v1.SandboxCreate{Template: "base"})
	require.Error(t, err)
	assert.Equal(t, output.CodeConflict, codeOf(t, err).Code)
}

// TestSandboxClientRefusesAnEndpointItCannotUse pins the construction failures and the
// nil receiver: a caller that never got a client gets a clear answer rather than a
// panic, and a bad endpoint or CA file is refused before a request is built.
func TestSandboxClientRefusesAnEndpointItCannotUse(t *testing.T) {
	_, err := NewSandboxClient(Config{Endpoint: "://not a url"})
	assert.Error(t, err)

	_, err = NewSandboxClient(Config{Endpoint: "https://127.0.0.1:4290", CAFile: filepath.Join(t.TempDir(), "absent.pem")})
	assert.Error(t, err)

	var absent *SandboxClient
	assert.Empty(t, absent.Socket())
	absent.Close()
}

// TestSandboxClientKeepsTheCodeOfEveryRefusal pins the other two mutating calls: removing
// a sandbox and minting a credential keep the daemon's code, so a caller can tell "already
// gone" from "not allowed".
func TestSandboxClientKeepsTheCodeOfEveryRefusal(t *testing.T) {
	handler := &sandboxHandler{respond: func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", sandboxTestMediaType)
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"Error","error":{"code":"not_found","message":"no such sandbox"}}`)
		return true
	}}
	client, _ := newUnixSandboxClient(t, handler)

	_, err := client.DeleteSandbox(context.Background(), "a")
	require.Error(t, err)
	assert.Equal(t, output.CodeNotFound, codeOf(t, err).Code)

	_, err = client.CreateSandboxToken(context.Background(), "a", v1.SandboxTokenRequest{})
	require.Error(t, err)
	assert.Equal(t, output.CodeNotFound, codeOf(t, err).Code)
}

// TestSandboxClientReportsAResponseItCannotRead pins the decode failure: a daemon that
// answers with something other than the contract is an internal error, not a zero value
// a caller would take for an empty sandbox.
func TestSandboxClientReportsAResponseItCannotRead(t *testing.T) {
	handler := &sandboxHandler{respond: func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", sandboxTestMediaType)
		_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":`)
		return true
	}}
	client, _ := newUnixSandboxClient(t, handler)

	_, err := client.GetSandbox(context.Background(), "a")
	require.Error(t, err)
	assert.Equal(t, output.CodeInternal, codeOf(t, err).Code)
}

// TestSandboxClientReportsItsDeadline pins that a request the caller already gave up on is a
// timeout, not an unreachable daemon: the two make a caller do different things.
func TestSandboxClientReportsItsDeadline(t *testing.T) {
	client, err := NewSandboxClient(Config{SandboxSocket: filepath.Join(t.TempDir(), "absent.sock")})
	require.NoError(t, err)
	t.Cleanup(client.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = client.GetSandbox(ctx, "a")
	require.Error(t, err)
	assert.Equal(t, output.CodeTimeout, codeOf(t, err).Code)
}

// TestSandboxClientNamesAnUnreachableEndpoint pins the remote failure: with no socket to
// name, the error is the transport's own, and it is "unreachable" rather than a timeout.
func TestSandboxClientNamesAnUnreachableEndpoint(t *testing.T) {
	// A manager that was there a moment ago: its CA is real, and nothing is listening on
	// the port any more.
	server := httptest.NewTLSServer(&sandboxHandler{})
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	require.NoError(t, os.WriteFile(caFile, pemBytes, 0o600))
	endpoint := server.URL
	server.Close()

	client, err := NewSandboxClient(Config{Endpoint: endpoint, CAFile: caFile, Token: "sc_test"})
	require.NoError(t, err)
	t.Cleanup(client.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = client.GetSandbox(ctx, "a")
	require.Error(t, err)
	assert.Equal(t, output.CodeUnreachable, codeOf(t, err).Code)
}

// TestSandboxClientReportsADaemonThatHangsUp pins the transport failure a streaming call can
// meet after the request is sent: the connection ends without a response, and the caller is
// told the daemon was unreachable rather than given an empty result.
func TestSandboxClientReportsADaemonThatHangsUp(t *testing.T) {
	dir, err := os.MkdirTemp("", "sbxcl")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "sandboxd.sock")

	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	client, err := NewSandboxClient(Config{SandboxSocket: socket})
	require.NoError(t, err)
	t.Cleanup(client.Close)

	_, err = client.ExecSandbox(context.Background(), "a",
		v1.ExecRequest{Command: []string{"true"}}, io.Discard, io.Discard)
	require.Error(t, err)
	assert.Equal(t, output.CodeUnreachable, codeOf(t, err).Code)
}

// TestSandboxClientReportsAResponseThatStopsMidway pins a body that ends before its length
// promises: the caller learns the daemon could not be read, rather than receiving a
// truncated document as if it were complete.
func TestSandboxClientReportsAResponseThatStopsMidway(t *testing.T) {
	handler := &sandboxHandler{respond: func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", sandboxTestMediaType)
		w.Header().Set("Content-Length", "512")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"schemaVersion":1,`)
		// Returning now closes the connection with the body unfinished.
		return true
	}}
	client, _ := newUnixSandboxClient(t, handler)

	_, err := client.GetSandbox(context.Background(), "a")
	require.Error(t, err)
	assert.Equal(t, output.CodeUnreachable, codeOf(t, err).Code)
}
