package client

// The file, console and status calls of the sandbox client: what a caller can rely on is
// the transport — which path, which query, which media type in each direction — and the
// decoding of the two documents. The guest side is not here: the client never speaks to a
// guest.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// TestSandboxClientGetFileAsksForAndReportsTheShape pins the read's two halves: the client
// asks in both shapes and lets the node choose, and it reports back which one arrived from
// the header rather than guessing from the media type.
func TestSandboxClientGetFileAsksForAndReportsTheShape(t *testing.T) {
	handler := &sandboxHandler{respond: func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/files") || r.Method != http.MethodGet {
			return false
		}
		switch r.URL.Query().Get("path") {
		case "workspace/main.go":
			w.Header().Set("Content-Type", octetStreamMediaType)
			w.Header().Set(sandboxFileKindHeader, string(SandboxFileKindFile))
			_, _ = io.WriteString(w, "package main\n")
		case "workspace/src":
			w.Header().Set("Content-Type", tarMediaType)
			w.Header().Set(sandboxFileKindHeader, string(SandboxFileKindDir))
			_, _ = io.WriteString(w, "TARBYTES")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
		return true
	}}
	client, recorder := newUnixSandboxClient(t, handler)

	kind, body, err := client.GetSandboxFile(context.Background(), "a",
		SandboxFileQuery{Path: "workspace/main.go"})
	require.NoError(t, err)
	assert.Equal(t, SandboxFileKindFile, kind)
	file, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	assert.Equal(t, "package main\n", string(file))

	kind, body, err = client.GetSandboxFile(context.Background(), "a", SandboxFileQuery{Path: "workspace/src"})
	require.NoError(t, err)
	assert.Equal(t, SandboxFileKindDir, kind)
	archive, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	assert.Equal(t, "TARBYTES", string(archive))

	require.Len(t, recorder.requests, 2)
	first := recorder.requests[0]
	assert.Equal(t, "/v1/sandboxes/a/files", first.URL.Path)
	assert.Equal(t, "workspace/main.go", first.URL.Query().Get("path"))
	assert.Contains(t, first.Header.Get("Accept"), octetStreamMediaType)
	assert.Contains(t, first.Header.Get("Accept"), tarMediaType)

	// A path the node does not have is the typed failure, not an empty body, and a read
	// with no path is refused here rather than sent.
	_, _, err = client.GetSandboxFile(context.Background(), "a", SandboxFileQuery{Path: "nope"})
	require.Error(t, err)
	_, _, err = client.GetSandboxFile(context.Background(), "a", SandboxFileQuery{})
	require.Error(t, err)
}

// TestSandboxClientPutFileChoosesTheOperationWithTheMediaType holds the one thing that tells
// the two write shapes apart: one file is octet-stream, a tree is tar, and both answer with
// what the guest wrote.
func TestSandboxClientPutFileChoosesTheOperationWithTheMediaType(t *testing.T) {
	handler := &sandboxHandler{respond: func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/files") || r.Method != http.MethodPut {
			return false
		}
		w.Header().Set("Content-Type", sandboxTestMediaType)
		entries := 0
		if r.Header.Get("Content-Type") == tarMediaType {
			entries = 3
		}
		_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"SandboxFileWrite","path":"`+
			r.URL.Query().Get("path")+`","bytes":6,"entries":`+strconv.Itoa(entries)+`}`)
		return true
	}}
	client, recorder := newUnixSandboxClient(t, handler)

	doc, err := client.PutSandboxFile(context.Background(), "a",
		SandboxFileQuery{Path: "workspace/main.go", Mode: 0o600}, strings.NewReader("hello\n"))
	require.NoError(t, err)
	assert.Equal(t, v1.KindSandboxFileWrite, doc.Kind)
	assert.Equal(t, int64(6), doc.Bytes)

	doc, err = client.PutSandboxTree(context.Background(), "a",
		SandboxFileQuery{Path: "workspace/src"}, strings.NewReader("TARBYTES"))
	require.NoError(t, err)
	assert.Equal(t, 3, doc.Entries)

	require.Len(t, recorder.requests, 2)
	file, archive := recorder.requests[0], recorder.requests[1]
	assert.Equal(t, octetStreamMediaType, file.Header.Get("Content-Type"))
	assert.Equal(t, "0600", file.URL.Query().Get("mode"))
	assert.Equal(t, "hello\n", recorder.bodies[0])
	assert.Equal(t, tarMediaType, archive.Header.Get("Content-Type"))
	assert.Empty(t, archive.URL.Query().Get("mode"), "an archive carries its own modes")
	assert.Equal(t, "TARBYTES", recorder.bodies[1])

	// A write with no path would name the guest's file root, so it is refused here rather
	// than sent.
	_, err = client.PutSandboxTree(context.Background(), "a", SandboxFileQuery{}, strings.NewReader("x"))
	require.Error(t, err)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeInvalid, typed.Code)
	assert.Len(t, recorder.requests, 2, "the refused write never left the client")
}

// TestSandboxClientStatusAndLogs are the two reads that take a document and a stream: the
// status is decoded into its Go type, and the console is copied as bytes.
func TestSandboxClientStatusAndLogs(t *testing.T) {
	handler := &sandboxHandler{respond: func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			w.Header().Set("Content-Type", sandboxTestMediaType)
			_, _ = io.WriteString(w, `{"schemaVersion":1,"kind":"SandboxStatus","id":"a","state":"ready",
				"info":{"hostname":"sandbox-a","cpus":2,"tools":["sh"]},
				"net":{"interfaces":[{"name":"eth0","index":2,"up":true}],"routes":[],"resolvers":[]}}`)
		case strings.HasSuffix(r.URL.Path, "/logs"):
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, "Linux version 6.1\n")
		default:
			return false
		}
		return true
	}}
	client, recorder := newUnixSandboxClient(t, handler)

	status, err := client.SandboxStatus(context.Background(), "a")
	require.NoError(t, err)
	assert.Equal(t, v1.KindSandboxStatus, status.Kind)
	assert.Equal(t, "a", status.ID)
	assert.Equal(t, "sandbox-a", status.Info.Hostname)
	assert.Equal(t, 2, status.Info.CPUs)
	require.Len(t, status.Net.Interfaces, 1)
	assert.Equal(t, "eth0", status.Net.Interfaces[0].Name)
	assert.True(t, status.Net.Interfaces[0].Up)

	var console bytes.Buffer
	require.NoError(t, client.SandboxLogs(context.Background(), "a", &console))
	assert.Equal(t, "Linux version 6.1\n", console.String())

	assert.Equal(t, "/v1/sandboxes/a/status", recorder.requests[0].URL.Path)
	assert.Equal(t, "text/plain", recorder.requests[1].Header.Get("Accept"))
}

// TestSandboxFileLogsAndStatusFailuresAreTyped holds the failure paths a caller meets: a
// daemon that is not there, a sandbox the node does not have, a writer that refuses, and the
// two arguments the client refuses before a request is sent.
func TestSandboxFileLogsAndStatusFailuresAreTyped(t *testing.T) {
	handler := &sandboxHandler{respond: func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.URL.Path, "/v1/sandboxes/missing/") {
			if strings.HasSuffix(r.URL.Path, "/logs") {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, "boot\n")
				return true
			}
			return false
		}
		writeSandboxTestError(w, http.StatusNotFound, "not_found", "no such sandbox")
		return true
	}}
	client, _ := newUnixSandboxClient(t, handler)

	_, err := client.SandboxStatus(context.Background(), "missing")
	require.Error(t, err)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeNotFound, typed.Code, "the node's typed failure reaches the caller")

	err = client.SandboxLogs(context.Background(), "missing", io.Discard)
	require.Error(t, err)
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeNotFound, typed.Code)

	// An empty id names no sandbox, and is refused here rather than sent as a request for
	// the collection.
	_, err = client.SandboxStatus(context.Background(), "")
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeUsage, typed.Code)

	// A writer that refuses is the caller's problem, and it is reported rather than
	// swallowed: a console half-written is not a console read.
	err = client.SandboxLogs(context.Background(), "a", failingWriter{})
	require.Error(t, err)

	// A nil writer asks for the console and discards it, which is what a caller that only
	// wants the answer's status does.
	assert.NoError(t, client.SandboxLogs(context.Background(), "a", nil))
}

// failingWriter is a writer that refuses everything, so a stream that cannot be consumed is
// exercised rather than assumed.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// TestSandboxFileReadAndWriteFailuresAreTyped covers what a caller does with a node that
// answers badly: a read of a sandbox the node does not have, a write it refuses, a response
// that is not the document it promised, and a daemon that is not there at all.
func TestSandboxFileReadAndWriteFailuresAreTyped(t *testing.T) {
	var mode string
	handler := &sandboxHandler{respond: func(w http.ResponseWriter, r *http.Request) bool {
		mode = r.URL.Query().Get("path")
		switch mode {
		case "gone":
			writeSandboxTestError(w, http.StatusNotFound, "not_found", "no such sandbox")
		case "refused":
			writeSandboxTestError(w, http.StatusConflict, "conflict", "the sandbox is created, not ready")
		case "garbage":
			w.Header().Set("Content-Type", sandboxTestMediaType)
			_, _ = io.WriteString(w, "not a document")
		default:
			return false
		}
		return true
	}}
	client, _ := newUnixSandboxClient(t, handler)

	var typed *output.Error
	_, _, err := client.GetSandboxFile(context.Background(), "a", SandboxFileQuery{Path: "gone"})
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeNotFound, typed.Code)

	_, err = client.PutSandboxFile(context.Background(), "a", SandboxFileQuery{Path: "refused"}, strings.NewReader("x"))
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeConflict, typed.Code)

	_, err = client.PutSandboxFile(context.Background(), "a", SandboxFileQuery{Path: "garbage"}, strings.NewReader("x"))
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeInternal, typed.Code, "a body that is not the document is reported as it is")

	// An empty id is refused before the request is built.
	_, err = client.PutSandboxFile(context.Background(), "", SandboxFileQuery{Path: "a"}, nil)
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeUsage, typed.Code)

	// A daemon that is not there names its socket, which is the one piece of information
	// that makes the failure actionable.
	absent, err := NewSandboxClient(Config{SandboxSocket: filepath.Join(t.TempDir(), "absent.sock")})
	require.NoError(t, err)
	t.Cleanup(absent.Close)

	_, _, err = absent.GetSandboxFile(context.Background(), "a", SandboxFileQuery{Path: "x"})
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeUnreachable, typed.Code)

	err = absent.SandboxLogs(context.Background(), "a", io.Discard)
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeUnreachable, typed.Code)
}

// writeSandboxTestError answers with the v1 error document a node writes for a typed
// failure, which is what the client has to decode rather than read as text.
func writeSandboxTestError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", sandboxTestMediaType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v1.NewErrorDocument(v1.Error{Code: code, Message: message}))
}
