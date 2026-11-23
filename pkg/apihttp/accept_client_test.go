package apihttp

import (
	"context"
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
	"github.com/restuhaqza/swarmcracker/pkg/client"
)

// TestEveryAcceptTheClientSendsIsAccepted is the test that was missing when `sandbox logs`
// shipped broken.
//
// The client sends text/plain to the console route. The envelope gate accepted only a list of
// known media types and refused it, so `sandbox logs` failed on every sandbox — including,
// and worst, one that never became ready, where the console is the only thing that can say
// why. No test crossed the two sides: the CLI tests answered from a fake server that never ran
// the gate, and the gate's own tests named media types a caller does not send.
//
// This drives the real client against the real Handler over a socket, so a route whose Accept
// the gate would refuse fails here rather than in a host run.
func TestEveryAcceptTheClientSendsIsAccepted(t *testing.T) {
	boxes := fileBoxes()
	logPath := filepath.Join(t.TempDir(), "a.log")
	require.NoError(t, os.WriteFile(logPath, []byte("Linux version 6.1\n"), 0o600))
	boxes.logPath = logPath
	srv, store := sandboxServer(t, boxes)
	_, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	// The socket listener is the unauthenticated one: the file's mode is the credential on
	// that transport, which is why the client sends no token here.
	socket := socketServing(t, srv.UnixHandler())
	c, err := client.NewSandboxClient(client.Config{SandboxSocket: socket})
	require.NoError(t, err)
	t.Cleanup(c.Close)

	ctx := context.Background()

	// Each call composes its own Accept header; none of them may be refused by the gate.
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"list", func() error { _, err := c.ListSandboxes(ctx); return err }},
		{"get", func() error { _, err := c.GetSandbox(ctx, "a"); return err }},
		{"logs", func() error { return c.SandboxLogs(ctx, "a", io.Discard) }},
		{"read a file", func() error {
			_, body, err := c.GetSandboxFile(ctx, "a", client.SandboxFileQuery{Path: "workspace/main.go"})
			if body != nil {
				_ = body.Close()
			}
			return err
		}},
		{"exec", func() error {
			_, err := c.ExecSandbox(ctx, "a", v1.ExecRequest{Command: []string{"/bin/true"}}, io.Discard, io.Discard)
			return err
		}},
	} {
		err := tc.call()
		if err != nil {
			assert.NotContains(t, err.Error(), "unsupported Accept",
				"%s: the gate refused this client's own Accept header: %v", tc.name, err)
		}
	}

	// And the console route end to end, because that is the one that broke: the bytes a
	// failed boot writes are the point.
	var console strings.Builder
	require.NoError(t, c.SandboxLogs(ctx, "a", &console), "sandbox logs reaches the route and is not refused")
	assert.Equal(t, "Linux version 6.1\n", console.String())
}

// TestAcceptGateIsVersionNegotiationNotAMediaTypeWhitelist pins the rule and, just as
// importantly, what it deliberately lets through: a media type that says nothing about the
// envelope belongs to the handler, not to this gate.
func TestAcceptGateIsVersionNegotiationNotAMediaTypeWhitelist(t *testing.T) {
	boxes := fileBoxes()
	logPath := filepath.Join(t.TempDir(), "a.log")
	require.NoError(t, os.WriteFile(logPath, []byte("boot\n"), 0o600))
	boxes.logPath = logPath
	srv, store := sandboxServer(t, boxes)
	token, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	for _, tc := range []struct {
		accept string
		want   int
		why    string
	}{
		{"text/plain", http.StatusOK, "the console's media type makes no claim about the envelope"},
		{"application/x-tar", http.StatusOK, "nor does a tree's"},
		{"text/html", http.StatusOK, "nor an unclaimed type: the handler decides what it serves"},
		{"*/*", http.StatusOK, "any"},
		{MediaType, http.StatusOK, "an explicit v1 claim, in the shape this server produces"},
		{"application/vnd.swarmcracker.v1", http.StatusOK, "a v1 claim naming no shape is answered as the v1 default"},
		{"application/vnd.swarmcracker.v1+xml", http.StatusNotAcceptable, "a v1 claim in a shape this server cannot produce"},
		{"application/vnd.swarmcracker.v1+xml; q=0.9", http.StatusNotAcceptable, "the same, with a quality value"},
		{MediaType + "; q=0.9", http.StatusOK, "the same, with a quality value"},
		{"application/vnd.swarmcracker.v1+json, text/plain", http.StatusOK, "one acceptable entry is enough"},
		{"application/vnd.swarmcracker.v2+json", http.StatusNotAcceptable, "an explicit other version"},
		{"application/vnd.swarmcracker.v2+json; q=0.9", http.StatusNotAcceptable, "the same, with a quality value"},
		{"text/plain, application/vnd.swarmcracker.v2+json", http.StatusNotAcceptable, "an acceptable entry alongside an unacceptable version does not save it"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/a/logs", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", tc.accept)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		// A v2 claim must be refused before the handler runs; everything else reaches it.
		assert.Equal(t, tc.want, rec.Code, "%q: %s", tc.accept, tc.why)
		if tc.want == http.StatusNotAcceptable {
			doc := decodeErrorBody(t, rec)
			assert.Equal(t, "unsupported_version", doc.Error.Code, "%q", tc.accept)
			assert.Equal(t, []any{"v1"}, doc.Error.Details["supported"], "%q", tc.accept)
		}
	}
}

// socketServing serves h on a Unix socket and returns the path. The path is short because a
// unix address holds about 107 bytes, and t.TempDir() carries the test's name.
func socketServing(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "apic")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	socket := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{Handler: h, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}

// TestTheGateAcceptsEveryAcceptAnyClientSends is the inventory, kept next to the gate.
//
// The test above crosses the client and the server for the sandbox routes. This one names
// every media type any client in this repository asks for, and which client asks for it, so a
// route that serves bytes has a checked answer here rather than relying on `*/*` happening to
// be sent by somebody else. Adding a client with a new media type means adding a line, and
// the gate refuses to break it silently.
func TestTheGateAcceptsEveryAcceptAnyClientSends(t *testing.T) {
	// The client's constant, restated so a change to one side is visible on the other.
	const octetStreamTestMediaType = "application/octet-stream"

	for _, tc := range []struct{ accept, askedBy string }{
		{"application/vnd.swarmcracker.v1+json", "every document route (pkg/client mediaTypeV1)"},
		{"application/vnd.swarmcracker.v1+json, application/vnd.swarmcracker.v1+json", "the remote client's merged Accept"},
		{v1.NDJSONMediaType, "exec and task logs (pkg/client/exec.go, logs.go)"},
		{"application/x-ndjson", "the remote watch stream (pkg/client/watch_remote.go)"},
		{"application/octet-stream", "attach (pkg/client/attach.go) and a single-file read"},
		{"application/octet-stream, application/x-tar", "a sandbox file read, which may answer a tree (pkg/client/sandbox.go)"},
		// Exactly the header the client composes, not a transcription of it: the two
		// constants it joins are the ones asserted here.
		{octetStreamTestMediaType + ", application/x-tar", "the joined form, built from the client's own constant"},
		{"text/plain", "sandbox logs — the console route, which is what broke"},
		{"text/event-stream", "the event stream (pkg/apihttp/events.go responds with it)"},
		{"*/*", "callers that do not negotiate"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/a/logs", nil)
		req.Header.Set("Accept", tc.accept)
		assert.NoError(t, checkAccept(req),
			"%q (%s) must pass the version gate: it names no version, or v1's",
			tc.accept, tc.askedBy)
	}
}

// TestAVersionClaimMustAlsoNameAShapeThisServerProduces is the four cases the rule comes down
// to, stated together so the distinction cannot be lost one at a time.
//
// A media type that names no envelope promises nothing, so it is the handler's business. One
// that names the v1 envelope makes a request: for a version, and for a shape. This server
// produces v1 JSON, so a v1 claim in another shape is refused rather than answered with JSON
// under a header that asked for XML — "here is what you asked for" about something else is
// worse than an honest 406.
func TestAVersionClaimMustAlsoNameAShapeThisServerProduces(t *testing.T) {
	srv, store := sandboxServer(t, fileBoxes())
	token, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	for _, tc := range []struct {
		accept string
		want   int
	}{
		{MediaType, http.StatusOK},                                         // v1+json: the shape it produces
		{"application/vnd.swarmcracker.v1+xml", http.StatusNotAcceptable},  // v1, a shape it cannot
		{"text/plain", http.StatusOK},                                      // names no envelope at all
		{"application/vnd.swarmcracker.v2+json", http.StatusNotAcceptable}, // another version
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/sandboxes/a/status", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", tc.accept)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		assert.Equal(t, tc.want, rec.Code, "%q", tc.accept)
		if tc.want == http.StatusNotAcceptable {
			doc := decodeErrorBody(t, rec)
			assert.Equal(t, "unsupported_version", doc.Error.Code, "%q carries the same code as a wrong version", tc.accept)
			assert.Equal(t, []any{"v1"}, doc.Error.Details["supported"], "%q", tc.accept)
		}
	}
}
