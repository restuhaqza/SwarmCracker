package apihttp

// The file, console and status half of the sandbox fake, and the tests for the three
// routes that use them.
//
// What is under test here is the HTTP layer's own job: which scope and selector each route
// requires, which media type each read and write answers with and accepts, and how the
// manager's failures (no such sandbox, not ready, a template without `untar`) become status
// codes and details. The guest-side behaviour — framing, tar, and the confinement that
// decides what an archive may write — is tested where it lives, in pkg/agent and
// pkg/sandbox.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox"
)

// note records one file operation the routes asked for.
func (f *fakeSandboxes) note(call string) {
	f.fileCalls = append(f.fileCalls, call)
}

func (f *fakeSandboxes) DescribeFile(_ context.Context, _, guestPath string) (sandbox.File, error) {
	f.note("describe " + guestPath)
	if f.describeErr != nil {
		return sandbox.File{}, f.describeErr
	}
	file, ok := f.files[guestPath]
	if !ok {
		return sandbox.File{}, fmt.Errorf("%w: %s", sandbox.ErrNotFound, guestPath)
	}
	return file, nil
}

func (f *fakeSandboxes) SendFile(_ context.Context, _, guestPath string, kind sandbox.FileKind, w io.Writer) (agent.TarSummary, error) {
	f.note("send " + guestPath)
	if f.sendErr != nil {
		return agent.TarSummary{}, f.sendErr
	}
	body := f.fileContents[guestPath]
	if _, err := io.WriteString(w, body); err != nil {
		return agent.TarSummary{}, err
	}
	if kind == sandbox.FileKindDir {
		return agent.TarSummary{Entries: 2, Bytes: int64(len(body))}, nil
	}
	return agent.TarSummary{}, nil
}

func (f *fakeSandboxes) PutFile(_ context.Context, _, guestPath string, mode uint32, r io.Reader) (int64, error) {
	f.note("put " + guestPath)
	if f.putErr != nil {
		return 0, f.putErr
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	f.putMode, f.putBytes = mode, int64(len(body))
	f.remember(guestPath, string(body))
	return int64(len(body)), nil
}

func (f *fakeSandboxes) ExtractTree(_ context.Context, _, guestPath string, r io.Reader) (agent.TarSummary, error) {
	f.note("extract " + guestPath)
	if f.extractErr != nil {
		return agent.TarSummary{}, f.extractErr
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return agent.TarSummary{}, err
	}
	f.remember(guestPath, string(body))

	entries, written := 0, int64(0)
	reader := tar.NewReader(bytes.NewReader(body))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return agent.TarSummary{}, err
		}
		entries++
		if header.Typeflag == tar.TypeReg {
			written += header.Size
		}
	}
	return agent.TarSummary{Entries: entries, Bytes: written}, nil
}

func (f *fakeSandboxes) Status(_ context.Context, _ string) (sandbox.Status, error) {
	f.note("status")
	if f.statusErr != nil {
		return sandbox.Status{}, f.statusErr
	}
	return f.status, nil
}

func (f *fakeSandboxes) ServerLogPath(id string) (string, error) {
	f.note("log " + id)
	if f.logErr != nil {
		return "", f.logErr
	}
	return f.logPath, nil
}

func (f *fakeSandboxes) remember(guestPath, body string) {
	if f.written == nil {
		f.written = map[string]string{}
	}
	f.written[guestPath] = body
}

// sandboxFileRequest runs one authenticated file request with an explicit content type,
// which the shared helper cannot express (it always sends the v1 media type).
func sandboxFileRequest(t *testing.T, srv *Server, secret, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// fileBoxes is one ready sandbox whose guest holds a file and a directory, plus a link
// (the third kind, which cannot be copied).
func fileBoxes() *fakeSandboxes {
	return &fakeSandboxes{
		records: map[string]sandbox.Record{
			"a": {ID: "a", State: sandbox.StateReady, Template: "base@1"},
			"b": {ID: "b", State: sandbox.StateReady, Template: "base@1"},
		},
		files: map[string]sandbox.File{
			"workspace/main.go": {Kind: sandbox.FileKindFile, Entry: agent.FileEntry{Name: "main.go", Type: "file", Size: 14}},
			"workspace/src":     {Kind: sandbox.FileKindDir, Entry: agent.FileEntry{Name: "src", Type: "dir"}},
			"workspace/link.go": {Kind: sandbox.FileKindOther, Entry: agent.FileEntry{Name: "link.go", Type: "symlink", Target: "main.go"}},
		},
		fileContents: map[string]string{
			"workspace/main.go": "package main\n",
			"workspace/src":     "",
		},
	}
}

// TestGetSandboxFileChoosesTheShapeFromTheGuest is the reading half of a copy: the guest is
// asked what the path is, and the answer chooses the media type and the operation. The
// caller never has to say which one it wants, and never gets a file when it named a
// directory.
func TestGetSandboxFileChoosesTheShapeFromTheGuest(t *testing.T) {
	boxes := fileBoxes()
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/files?path=workspace/main.go", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "file", rec.Header().Get("X-Swarmcracker-File-Kind"))
	assert.Equal(t, "package main\n", rec.Body.String())

	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/files?path=workspace/src", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, tarMediaType, rec.Header().Get("Content-Type"))
	assert.Equal(t, "dir", rec.Header().Get("X-Swarmcracker-File-Kind"))

	assert.Equal(t, []string{"describe workspace/main.go", "send workspace/main.go",
		"describe workspace/src", "send workspace/src"}, boxes.fileCalls,
		"a read describes the path before it streams it, once per request")
}

// TestGetSandboxFileRefusesWhatIsNotAFileOrADirectory covers the third kind: a link is
// refused before anything is written, because a response's shape has to be decided first.
func TestGetSandboxFileRefusesWhatIsNotAFileOrADirectory(t *testing.T) {
	boxes := fileBoxes()
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/files?path=workspace/link.go", "")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "unsupported_file_kind", doc.Error.Details["reason"])
	assert.Equal(t, "symlink", doc.Error.Details["kind"])
	assert.Equal(t, []string{"describe workspace/link.go"}, boxes.fileCalls, "nothing was streamed")
}

// TestGetSandboxFileRequiresAPath pins the one parameter these routes cannot default: an
// empty path would name the guest's file root, which is not what a caller meant.
func TestGetSandboxFileRequiresAPath(t *testing.T) {
	boxes := fileBoxes()
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/files", "")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "missing_path", decodeErrorBody(t, rec).Error.Details["reason"])

	// A parameter this route does not implement is a refusal, not a request that copies
	// something the caller did not ask for.
	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/files?path=workspace/main.go&kind=file", "")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Empty(t, boxes.fileCalls, "an unimplemented parameter never reaches the manager")
}

// TestPutSandboxFileChoosesTheOperationFromTheContentType is the writing half: one file for
// octet-stream, a tree for tar, and the mode the caller named.
func TestPutSandboxFileChoosesTheOperationFromTheContentType(t *testing.T) {
	boxes := fileBoxes()
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxFileRequest(t, srv, admin, http.MethodPut,
		"/v1/sandboxes/a/files?path=workspace/main.go&mode=0600", "application/octet-stream", "package main\n")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var write v1.SandboxFileWrite
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &write))
	assert.Equal(t, v1.KindSandboxFileWrite, write.Kind)
	assert.Equal(t, "workspace/main.go", write.Path)
	assert.Equal(t, int64(len("package main\n")), write.Bytes)
	assert.Zero(t, write.Entries, "a single file is not a tree")
	assert.Equal(t, uint32(0o600), boxes.putMode, "the mode the caller named reaches the guest")
	assert.Equal(t, "package main\n", boxes.written["workspace/main.go"])

	archive := testArchive(t, map[string]string{"workspace/src/lib.go": "package src\n"})
	rec = sandboxFileRequest(t, srv, admin, http.MethodPut,
		"/v1/sandboxes/a/files?path=workspace/src", tarMediaType, string(archive))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &write))
	assert.Equal(t, 1, write.Entries, "a tree reports how many entries landed")
	assert.Equal(t, int64(len("package src\n")), write.Bytes)
	assert.Equal(t, string(archive), boxes.written["workspace/src"])

	assert.Equal(t, []string{"put workspace/main.go", "extract workspace/src"}, boxes.fileCalls)

	// The default mode is a workspace file's, and it is the one the guest is told when the
	// caller names none.
	rec = sandboxFileRequest(t, srv, admin, http.MethodPut,
		"/v1/sandboxes/a/files?path=workspace/README.md", "application/octet-stream; charset=binary", "read me\n")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, uint32(defaultSandboxFileMode), boxes.putMode)
}

// TestPutSandboxFileRefusesWhatItCannotWriteAs pins the two refusals before any byte is
// written: a body that is neither a file nor an archive, and a mode that is not a
// permission set.
func TestPutSandboxFileRefusesWhatItCannotWriteAs(t *testing.T) {
	boxes := fileBoxes()
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxFileRequest(t, srv, admin, http.MethodPut, "/v1/sandboxes/a/files?path=workspace/x",
		MediaType, `{}`)
	require.Equal(t, http.StatusUnsupportedMediaType, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "unsupported_media_type", doc.Error.Details["reason"])
	assert.Equal(t, MediaType, doc.Error.Details["contentType"])

	rec = sandboxFileRequest(t, srv, admin, http.MethodPut, "/v1/sandboxes/a/files?path=workspace/x&mode=999",
		"application/octet-stream", "x")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "invalid_mode", decodeErrorBody(t, rec).Error.Details["reason"])

	assert.Empty(t, boxes.fileCalls, "neither request reached the manager")
}

// TestPutSandboxFileNamesTheCapabilityAnOldTemplateLacks is the reason `untar` is an
// optional capability: a template built before it answers 501 naming the operation, and the
// caller knows to send one file instead — or to rebuild the template.
func TestPutSandboxFileNamesTheCapabilityAnOldTemplateLacks(t *testing.T) {
	boxes := fileBoxes()
	boxes.extractErr = &sandbox.MissingCapabilityError{Sandbox: "a", Capability: agent.CapUntar}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	archive := testArchive(t, map[string]string{"workspace/src/lib.go": "package src\n"})
	rec := sandboxFileRequest(t, srv, admin, http.MethodPut,
		"/v1/sandboxes/a/files?path=workspace/src", tarMediaType, string(archive))
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())

	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeNotImplemented), doc.Error.Code)
	assert.Equal(t, "template_capability_missing", doc.Error.Details["reason"])
	assert.Equal(t, agent.CapUntar, doc.Error.Details["capability"])

	// The same template still takes a single file: the capability decision is about the
	// tree, not about the route.
	rec = sandboxFileRequest(t, srv, admin, http.MethodPut,
		"/v1/sandboxes/a/files?path=workspace/main.go", "application/octet-stream", "package main\n")
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestSandboxFileRoutesRequireTheirScopeAndSelector pins both refusals, which are what
// makes a workspace copy a per-sandbox power: the scope says the token may copy at all, and
// the selector says from which sandbox.
func TestSandboxFileRoutesRequireTheirScopeAndSelector(t *testing.T) {
	boxes := fileBoxes()
	srv, store := sandboxServer(t, boxes)

	// No sandboxes:files: refused with the stable reason and the scope it needed.
	reader, _, err := store.Create("reader", RoleReader)
	require.NoError(t, err)
	rec := sandboxRequest(t, srv, reader, http.MethodGet, "/v1/sandboxes/a/files?path=workspace/main.go", "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "scope_missing", doc.Error.Details["reason"])
	assert.Equal(t, string(ScopeSandboxesFiles), doc.Error.Details["requiredScope"])

	// A runner that holds the scope and names no sandbox: refused, because the scope alone
	// would be every sandbox's workspace.
	runner := createRunnerToken(t, store, []string{string(ScopeSandboxesFiles)}, nil)
	rec = sandboxRequest(t, srv, runner, http.MethodGet, "/v1/sandboxes/a/files?path=workspace/main.go", "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "sandbox_selector_required", decodeErrorBody(t, rec).Error.Details["reason"])

	// A token minted for sandbox b, asking about sandbox a's workspace: the cross-sandbox
	// refusal, in both directions of the copy.
	narrowed := createRunnerToken(t, store, []string{string(ScopeSandboxesFiles)}, []string{"b"})
	rec = sandboxRequest(t, srv, narrowed, http.MethodGet, "/v1/sandboxes/a/files?path=workspace/main.go", "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc = decodeErrorBody(t, rec)
	assert.Equal(t, "sandbox_selector_mismatch", doc.Error.Details["reason"])
	assert.Equal(t, "a", doc.Error.Details["sandboxId"])

	rec = sandboxFileRequest(t, srv, narrowed, http.MethodPut, "/v1/sandboxes/a/files?path=workspace/main.go",
		"application/octet-stream", "x")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "sandbox_selector_mismatch", decodeErrorBody(t, rec).Error.Details["reason"])

	// Its own sandbox is its own: the same token copies there.
	rec = sandboxRequest(t, srv, narrowed, http.MethodGet, "/v1/sandboxes/b/files?path=workspace/main.go", "")
	assert.NotEqual(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

// TestSandboxFileFailuresMapToStatuses pins what a caller can act on: a sandbox this node
// does not have is a 404, one that is not ready is a 409, and a guest that cannot be
// reached is not reported as a missing path.
func TestSandboxFileFailuresMapToStatuses(t *testing.T) {
	boxes := fileBoxes()
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/files?path=workspace/nope", "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, string(output.CodeNotFound), decodeErrorBody(t, rec).Error.Code)

	boxes.describeErr = sandbox.ErrNotReady
	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/files?path=workspace/main.go", "")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	boxes.describeErr = nil
	boxes.putErr = sandbox.ErrInvalid
	rec = sandboxFileRequest(t, srv, admin, http.MethodPut, "/v1/sandboxes/a/files?path=workspace/main.go",
		"application/octet-stream", "x")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestGetSandboxLogsServesTheConsole pins the three answers a console read has: the file,
// a sandbox that has written none, and a node that records none at all — which is a
// capability this node does not serve, not a sandbox that is broken.
func TestGetSandboxLogsServesTheConsole(t *testing.T) {
	boxes := fileBoxes()
	logPath := filepath.Join(t.TempDir(), "a.log")
	require.NoError(t, os.WriteFile(logPath, []byte("Linux version 6.1\n"), 0o600))
	boxes.logPath = logPath
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/logs", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "Linux version 6.1\n", rec.Body.String())

	boxes.logPath = filepath.Join(t.TempDir(), "missing.log")
	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/logs", "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	boxes.logErr = sandbox.ErrNotImplemented
	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/logs", "")
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "logs_unavailable", doc.Error.Details["reason"])
	assert.Equal(t, "sandboxes:logs", doc.Error.Details["capability"])

	boxes.logErr = sandbox.ErrNotFound
	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/logs", "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestSandboxLogsAndStatusRequireTheSandboxSelector holds the two reads that dial into the
// guest to the same rule exec follows: a token minted for one sandbox cannot read another's
// console or ask another's guest what it is.
func TestSandboxLogsAndStatusRequireTheSandboxSelector(t *testing.T) {
	boxes := fileBoxes()
	srv, store := sandboxServer(t, boxes)

	narrowed := createRunnerToken(t, store, []string{string(ScopeSandboxesRead)}, []string{"b"})
	rec := sandboxRequest(t, srv, narrowed, http.MethodGet, "/v1/sandboxes/a/status", "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "sandbox_selector_mismatch", decodeErrorBody(t, rec).Error.Details["reason"])

	rec = sandboxRequest(t, srv, narrowed, http.MethodGet, "/v1/sandboxes/a/logs", "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "sandbox_selector_mismatch", decodeErrorBody(t, rec).Error.Details["reason"])

	// Neither refusal reached the node's manager.
	assert.Empty(t, boxes.fileCalls)
}

// TestGetSandboxStatusReportsTheGuestsOwnAnswer checks the document a caller decides on: the
// record it already knows, plus what the guest said about itself — and that both came from
// the guest agent's own operations rather than from a command's output.
func TestGetSandboxStatusReportsTheGuestsOwnAnswer(t *testing.T) {
	boxes := fileBoxes()
	boxes.status = sandbox.Status{
		Record: boxes.records["a"],
		Info: agent.GuestInfo{
			Hostname: "sandbox-a", MachineID: "machine-a", KernelRelease: "6.1.0", Machine: "x86_64",
			OSName: "Alpine Linux v3.20", OSID: "alpine", CPUs: 2, MemoryTotalBytes: 512 << 20,
			UptimeSeconds: 42, Protocol: agent.Version, AgentVersion: "1.2.3", Tools: []string{"sh", "tar"},
		},
		Net: agent.GuestNet{
			Interfaces: []agent.NetInterface{{Name: "eth0", Index: 2, MAC: "02:00:00:00:00:01", MTU: 1500, Up: true, Addresses: []string{"10.63.0.2/30"}}},
			Routes:     []agent.NetRoute{{Family: "ipv4", Destination: "default", Gateway: "10.63.0.1", Device: "eth0"}},
			Resolvers:  []string{"1.1.1.1"},
		},
	}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/status", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var doc v1.SandboxStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, v1.KindSandboxStatus, doc.Kind)
	assert.Equal(t, "a", doc.ID, "the record travels with the guest's report")
	assert.Equal(t, string(sandbox.StateReady), doc.State)
	assert.Equal(t, "sandbox-a", doc.Info.Hostname)
	assert.Equal(t, "6.1.0", doc.Info.KernelRelease)
	assert.Equal(t, uint64(512<<20), doc.Info.MemoryTotalBytes)
	assert.Equal(t, []string{"sh", "tar"}, doc.Info.Tools)
	require.Len(t, doc.Net.Interfaces, 1)
	assert.Equal(t, []string{"10.63.0.2/30"}, doc.Net.Interfaces[0].Addresses)
	require.Len(t, doc.Net.Routes, 1)
	assert.Equal(t, "10.63.0.1", doc.Net.Routes[0].Gateway)

	assert.Equal(t, []string{"status"}, boxes.fileCalls)

	boxes.statusErr = sandbox.ErrNotReady
	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/status", "")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

// TestSandboxStatusDocumentKeepsEmptyArrays is the shape a client depends on: a guest with
// no routes reports an empty list, never a missing field, so a consumer can iterate without
// checking first.
func TestSandboxStatusDocumentKeepsEmptyArrays(t *testing.T) {
	doc := v1.NewSandboxStatus(v1.Sandbox{ID: "a", State: "ready"}, v1.SandboxGuestInfo{}, v1.SandboxGuestNet{})

	data, err := json.Marshal(doc)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"interfaces":[]`)
	assert.Contains(t, string(data), `"routes":[]`)
	assert.Contains(t, string(data), `"resolvers":[]`)
}

// createRunnerToken mints a runner token holding scopes and, when given IDs, a sandbox
// selector naming them.
func createRunnerToken(t *testing.T, store *TokenStore, scopes, sandboxes []string) string {
	t.Helper()

	options := TokenOptions{
		Scopes: scopes,
		// A task selector is what makes a runner token acceptable at all; it names no
		// sandbox, so the sandbox rule is the one under test.
		TaskSelectors: &TaskSelectors{TaskIDs: []string{"t1"}},
	}
	if sandboxes != nil {
		options.SandboxSelectors = &SandboxSelectors{SandboxIDs: sandboxes}
	}
	secret, _, err := store.CreateWithOptions("runner", RoleRunner, options)
	require.NoError(t, err)
	return secret
}

// testArchive builds a tar archive in memory from slash-separated names to contents.
func testArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for name, body := range entries {
		require.NoError(t, writer.WriteHeader(&tar.Header{
			Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
		}))
		_, err := writer.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

// TestSandboxFileRoutesAreNotImplementedWithoutAControlPlane pins the answer every sandbox
// route gives on a node that serves no sandbox control plane: a capability, said so, rather
// than an empty answer or an internal error.
func TestSandboxFileRoutesAreNotImplementedWithoutAControlPlane(t *testing.T) {
	srv, store := sandboxServer(t, nil)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/sandboxes/a/files?path=workspace/main.go"},
		{http.MethodPut, "/v1/sandboxes/a/files?path=workspace/main.go"},
		{http.MethodGet, "/v1/sandboxes/a/logs"},
		{http.MethodGet, "/v1/sandboxes/a/status"},
	} {
		rec := sandboxRequest(t, srv, admin, tc.method, tc.path, "")
		require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
		doc := decodeErrorBody(t, rec)
		assert.Equal(t, "sandboxes", doc.Error.Details["capability"])
	}
}

// TestGetSandboxFileFailureAfterTheResponseStarted is the one failure a streaming read cannot
// turn into a document: the status and the media type are already written, so the caller gets
// a short body and the failure is logged. What must not happen is a second, contradictory
// answer — a status line or an error document appended to the file.
func TestGetSandboxFileFailureAfterTheResponseStarted(t *testing.T) {
	boxes := fileBoxes()
	boxes.sendErr = errors.New("the guest stopped mid-archive")
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/files?path=workspace/main.go", "")
	assert.Equal(t, http.StatusOK, rec.Code, "the read had already answered")
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.Empty(t, rec.Body.String(), "a failed copy is a short body, never a document after a file")
}

// TestSandboxStatusFailureModesAreTyped covers the two answers the status route can give
// besides a document: a sandbox this node does not have, and a node that serves no sandbox
// control plane at all (which is its own 501).
func TestSandboxStatusFailureModesAreTyped(t *testing.T) {
	boxes := fileBoxes()
	boxes.statusErr = sandbox.ErrNotFound
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/status", "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	boxes.statusErr = sandbox.ErrNotImplemented
	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/status", "")
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
}
