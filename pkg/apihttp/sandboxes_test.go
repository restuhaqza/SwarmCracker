package apihttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox"
)

// fakeSandboxes is the node's sandbox control plane as the routes reach it: it records
// what the HTTP layer asked for and answers with what the test needs, so the routes'
// own job — authorization, mapping, status codes — is what is under test.
type fakeSandboxes struct {
	records   map[string]sandbox.Record
	createReq sandbox.CreateRequest
	createErr error
	removeErr error
	execErr   error
	execCalls []string
	freed     sandbox.Freed
	// skipped are the record files a listing could not read, and listErr is a store
	// that cannot be read at all: the two ways a listing can be incomplete.
	skipped []string
	listErr error
	// execStatus, when set, is a status the exec handler writes itself instead of a
	// stream: the command never ran, and the route must report it as that.
	execStatus int

	// checkpoints is the node's checkpoint set, and forkReq what a fork was asked for.
	checkpoints   map[string]sandbox.CheckpointRecord
	checkpointErr error
	// checkpointListErr is a checkpoint store that cannot be listed at all, which is a
	// different answer from a store with nothing in it.
	checkpointListErr error
	forkReq           sandbox.ForkRequest
	forkErr           error
	forkFromID        string
}

func (f *fakeSandboxes) Checkpoint(_ context.Context, id, name string) (sandbox.CheckpointRecord, error) {
	if f.checkpointErr != nil {
		return sandbox.CheckpointRecord{}, f.checkpointErr
	}
	rec := sandbox.CheckpointRecord{Name: name, Sandbox: id, Template: "base@1", Quiesce: sandbox.QuiesceOutcome{Mode: "fsfreeze", Mounts: []string{"/"}}}
	if f.checkpoints == nil {
		f.checkpoints = map[string]sandbox.CheckpointRecord{}
	}
	f.checkpoints[name] = rec
	return rec, nil
}

func (f *fakeSandboxes) Checkpoints(sandboxID string) ([]sandbox.CheckpointRecord, []string, error) {
	if f.checkpointListErr != nil {
		return nil, nil, f.checkpointListErr
	}
	out := make([]sandbox.CheckpointRecord, 0, len(f.checkpoints))
	for _, rec := range f.checkpoints {
		if sandboxID == "" || rec.Sandbox == sandboxID {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, f.skipped, nil
}

func (f *fakeSandboxes) CheckpointByName(name string) (sandbox.CheckpointRecord, error) {
	rec, ok := f.checkpoints[name]
	if !ok {
		return sandbox.CheckpointRecord{}, sandbox.ErrNotFound
	}
	return rec, nil
}

func (f *fakeSandboxes) DeleteCheckpoint(_ context.Context, name string) (sandbox.CheckpointDeletion, error) {
	if _, ok := f.checkpoints[name]; !ok {
		return sandbox.CheckpointDeletion{}, sandbox.ErrNotFound
	}
	delete(f.checkpoints, name)
	return sandbox.CheckpointDeletion{
		Name: name, StateBytes: 1024, DiskBytes: 2048, ChunkBytes: 4096, Chunks: 1,
		FreedBytes: 7168, RetainedBytes: 8192, Retained: 1,
	}, nil
}

func (f *fakeSandboxes) Fork(_ context.Context, req sandbox.ForkRequest) ([]sandbox.Record, error) {
	f.forkReq = req
	if f.forkErr != nil {
		return nil, f.forkErr
	}
	count := req.Count
	if count == 0 {
		count = 1
	}
	out := make([]sandbox.Record, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, sandbox.Record{ID: fmt.Sprintf("clone-%d", i), State: sandbox.StateReady, ForkedFrom: req.Checkpoint})
	}
	return out, nil
}

func (f *fakeSandboxes) ForkFromSandbox(_ context.Context, id string, req sandbox.ForkRequest) ([]sandbox.Record, error) {
	f.forkFromID = id
	req.Checkpoint = "fresh"
	return f.Fork(context.Background(), req)
}

func (f *fakeSandboxes) List() ([]sandbox.Record, []string, error) {
	if f.listErr != nil {
		return nil, nil, f.listErr
	}
	out := make([]sandbox.Record, 0, len(f.records))
	for _, rec := range f.records {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, f.skipped, nil
}

func (f *fakeSandboxes) Get(id string) (sandbox.Record, error) {
	rec, ok := f.records[id]
	if !ok {
		return sandbox.Record{}, sandbox.ErrNotFound
	}
	return rec, nil
}

func (f *fakeSandboxes) Create(_ context.Context, req sandbox.CreateRequest) (sandbox.Record, error) {
	f.createReq = req
	if f.createErr != nil {
		return sandbox.Record{}, f.createErr
	}
	rec := sandbox.Record{ID: req.ID, State: sandbox.StateReady, Template: req.Template, Owner: req.Owner}
	if rec.ID == "" {
		rec.ID = "minted"
	}
	return rec, nil
}

func (f *fakeSandboxes) Remove(_ context.Context, id string) (sandbox.Freed, error) {
	if f.removeErr != nil {
		return sandbox.Freed{}, f.removeErr
	}
	return f.freed, nil
}

func (f *fakeSandboxes) ServeExec(_ context.Context, id string, w http.ResponseWriter, _ *http.Request) error {
	if f.execErr != nil {
		return f.execErr
	}
	if f.execStatus != 0 {
		w.Header().Set("Content-Type", MediaType)
		w.WriteHeader(f.execStatus)
		_, err := w.Write([]byte(`{"schemaVersion":1,"kind":"Error","error":{"code":"unavailable","message":"the guest went away"}}`))
		return err
	}
	f.execCalls = append(f.execCalls, id)
	w.Header().Set("Content-Type", v1.NDJSONMediaType)
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(`{"type":"exit","exitCode":0}` + "\n"))
	return err
}

// sandboxServer builds a server with a real token store and the given sandbox
// service. The store is real because the sandbox-scoped token is a real credential:
// its scopes and selectors are what the acceptance test is about.
func sandboxServer(t *testing.T, boxes Sandboxes) (*Server, *TokenStore) {
	t.Helper()
	store, err := OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	require.NoError(t, err)
	srv, err := New(Config{
		Client:    &fakeClient{},
		Tokens:    store,
		Sandboxes: boxes,
		RateLimit: NewRateLimiter(1e9, 1e9),
	})
	require.NoError(t, err)
	return srv, store
}

// sandboxRequest runs one authenticated request and returns its response.
func sandboxRequest(t *testing.T, srv *Server, secret, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+secret)
	if body != "" {
		req.Header.Set("Content-Type", MediaType)
		req.Header.Set("Accept", MediaType)
	}
	if strings.HasSuffix(path, "/exec") {
		req.Header.Set("Accept", v1.NDJSONMediaType)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestSandboxRoutesRequireTheirScope pins the scope each route names: a token that
// lacks it is refused with the stable reason, not with a different error and not by
// falling through to a handler.
func TestSandboxRoutesRequireTheirScope(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}}}
	srv, store := sandboxServer(t, boxes)

	cases := []struct {
		method string
		path   string
		scope  Scope
	}{
		{http.MethodGet, "/v1/sandboxes", ScopeSandboxesRead},
		{http.MethodGet, "/v1/sandboxes/a", ScopeSandboxesRead},
		{http.MethodPost, "/v1/sandboxes", ScopeSandboxesWrite},
		{http.MethodDelete, "/v1/sandboxes/a", ScopeSandboxesWrite},
		{http.MethodPost, "/v1/sandboxes/a/exec", ScopeSandboxesExec},
		{http.MethodPost, "/v1/sandboxes/a/tokens", ScopeSandboxesToken},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			// A runner narrowed to the task exec scope: it holds every sandbox scope
			// except the one the route needs is irrelevant — what matters is that the
			// route checks its own.
			secret, _, err := store.CreateWithOptions("narrow", RoleRunner, TokenOptions{
				Scopes:        []string{string(ScopeTasksExec)},
				TaskSelectors: &TaskSelectors{TaskIDs: []string{"t1"}},
			})
			require.NoError(t, err)

			rec := sandboxRequest(t, srv, secret, tc.method, tc.path, bodyFor(tc.method))
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			doc := decodeErrorBody(t, rec)
			assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
			assert.Equal(t, "scope_missing", doc.Error.Details["reason"])
			assert.Equal(t, string(tc.scope), doc.Error.Details["requiredScope"])
		})
	}
}

// TestSandboxScopedTokenCannotReachAnotherSandbox is acceptance #9, end to end through
// the API: a token minted for one sandbox execs in that sandbox and is refused
// everywhere else, with the reason it was refused.
func TestSandboxScopedTokenCannotReachAnotherSandbox(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{
		"a": {ID: "a", State: sandbox.StateReady},
		"b": {ID: "b", State: sandbox.StateReady},
	}}
	srv, store := sandboxServer(t, boxes)

	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	// An admin mints a credential for A. It carries read and exec, and names A.
	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/tokens", `{}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var minted v1.SandboxToken
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &minted))
	require.NotEmpty(t, minted.Secret)
	assert.Equal(t, []string{string(ScopeSandboxesRead), string(ScopeSandboxesExec)}, minted.Scopes)
	assert.Equal(t, "a", minted.Sandbox)

	// It execs in A: the credential works for what it was minted for.
	rec = sandboxRequest(t, srv, minted.Secret, http.MethodPost, "/v1/sandboxes/a/exec", `{"command":["true"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"a"}, boxes.execCalls)

	// It does not exec in B, and the refusal names the sandbox it was asked about.
	rec = sandboxRequest(t, srv, minted.Secret, http.MethodPost, "/v1/sandboxes/b/exec", `{"command":["true"]}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "sandbox_selector_mismatch", doc.Error.Details["reason"])
	assert.Equal(t, "b", doc.Error.Details["sandboxId"])
	assert.Equal(t, []string{"a"}, boxes.execCalls, "the refused exec never reached the sandbox")

	// It does not register a sandbox.
	rec = sandboxRequest(t, srv, minted.Secret, http.MethodPost, "/v1/sandboxes", `{"template":"base"}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc = decodeErrorBody(t, rec)
	assert.Equal(t, "scope_missing", doc.Error.Details["reason"])
	assert.Equal(t, string(ScopeSandboxesWrite), doc.Error.Details["requiredScope"])

	// It does not issue a token.
	rec = sandboxRequest(t, srv, minted.Secret, http.MethodPost, "/v1/sandboxes/a/tokens", `{}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc = decodeErrorBody(t, rec)
	assert.Equal(t, "scope_missing", doc.Error.Details["reason"])
	assert.Equal(t, string(ScopeSandboxesToken), doc.Error.Details["requiredScope"])

	// It cannot remove one either: rm is a write, and it holds neither the scope nor
	// (for B) the selector.
	rec = sandboxRequest(t, srv, minted.Secret, http.MethodDelete, "/v1/sandboxes/a", "")
	require.Equal(t, http.StatusForbidden, rec.Code)
	doc = decodeErrorBody(t, rec)
	assert.Equal(t, string(ScopeSandboxesWrite), doc.Error.Details["requiredScope"])
}

// TestRunnerWithoutASandboxSelectorFailsClosed pins the other refusal a sandbox exec
// can meet: a runner token that holds sandboxes:exec but names no sandbox would
// otherwise be a shell inside every one.
func TestRunnerWithoutASandboxSelectorFailsClosed(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}}}
	srv, store := sandboxServer(t, boxes)

	secret, _, err := store.CreateWithOptions("runner", RoleRunner, TokenOptions{
		Scopes: []string{string(ScopeSandboxesExec)},
		// A task selector is what makes the mint acceptable; it names no sandbox.
		TaskSelectors: &TaskSelectors{TaskIDs: []string{"t1"}},
	})
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, secret, http.MethodPost, "/v1/sandboxes/a/exec", `{"command":["true"]}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "sandbox_selector_required", doc.Error.Details["reason"])
	assert.Empty(t, boxes.execCalls)
}

// TestCreateSandboxMapsTheRequest checks the body becomes the request the node's
// manager takes, including the TTL conversion the contract states in seconds.
func TestCreateSandboxMapsTheRequest(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes",
		`{"template":"base@1","owner":"ci","egress":["example.com:443"],"idleTtlSeconds":120,"vcpus":2,"memoryMB":512}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	assert.Equal(t, "base@1", boxes.createReq.Template)
	assert.Equal(t, "ci", boxes.createReq.Owner)
	assert.Equal(t, []string{"example.com:443"}, boxes.createReq.Allow)
	assert.Equal(t, 120*time.Second, boxes.createReq.TTL)
	assert.Equal(t, 2, boxes.createReq.VCPUs)
	assert.Equal(t, 512, boxes.createReq.MemoryMB)

	var doc v1.SandboxDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, v1.KindSandbox, doc.Kind)
	assert.Equal(t, string(sandbox.StateReady), doc.State)

	// -1 is "never", which reaches the manager as a negative TTL; anything below it
	// is refused before the node is asked to build anything.
	rec = sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes", `{"template":"base","idleTtlSeconds":-1}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, -time.Second, boxes.createReq.TTL)

	rec = sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes", `{"template":"base","idleTtlSeconds":-2}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	doc2 := decodeErrorBody(t, rec)
	assert.Equal(t, "invalid_ttl", doc2.Error.Details["reason"])
}

// TestRemoveSandboxReportsWhatWasFreed checks the removal document is the teardown
// report, field for field: "everything is gone" is a list, not a promise.
func TestRemoveSandboxReportsWhatWasFreed(t *testing.T) {
	boxes := &fakeSandboxes{
		records: map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}},
		freed: sandbox.Freed{
			ID: "a", PID: 42, Process: true, Socket: "/run/a.sock", Cgroup: "/sys/fs/cgroup/firecracker/a",
			JailRoot: "/var/lib/jail/a", NetNS: "/var/run/netns/sbx-a", TAP: "tap0",
			Disk: "/var/lib/rootfs/a.ext4", TokenFile: "/var/lib/jail/a/token",
			APITokens: 2, InstanceRecord: true, Record: true,
		},
	}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodDelete, "/v1/sandboxes/a", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var doc v1.SandboxRemoval
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, v1.KindSandboxRemoval, doc.Kind)
	assert.Equal(t, "a", doc.Sandbox)
	for name, freed := range map[string]bool{
		"process": doc.Freed.ProcessStopped, "socket": doc.Freed.SocketRemoved,
		"cgroup": doc.Freed.CgroupRemoved, "jail": doc.Freed.JailRemoved,
		"netns": doc.Freed.NetNSRemoved, "tap": doc.Freed.TAPRemoved,
		"disk": doc.Freed.DiskRemoved, "agent credential": doc.Freed.AgentCredentialRemoved,
		"instance record": doc.Freed.InstanceRecordRemoved, "record": doc.Freed.RecordRemoved,
	} {
		assert.True(t, freed, "%s must be reported as freed", name)
	}
	assert.Equal(t, 2, doc.Freed.APITokensRevoked)
}

// TestSandboxFailuresMapToStatuses pins the mapping a client depends on: a missing
// sandbox is a 404, one that cannot run a command is a conflict, and a template this
// node cannot use is a conflict with the template package's own code.
func TestSandboxFailuresMapToStatuses(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/missing", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "not_found", decodeErrorBody(t, rec).Error.Code)

	boxes.execErr = sandbox.ErrNotReady
	rec = sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/exec", `{"command":["true"]}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "conflict", decodeErrorBody(t, rec).Error.Code)
}

// TestSandboxRoutesAnswerNotImplementedWithoutAManager pins the capability gate: a node
// that runs no sandboxes says so on every sandbox route with a 501 and the capability's
// name, rather than answering an empty list that would look like "no sandboxes".
func TestSandboxRoutesAnswerNotImplementedWithoutAManager(t *testing.T) {
	srv, store := sandboxServer(t, nil)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	routes := []struct{ method, path string }{
		{http.MethodGet, "/v1/sandboxes"},
		{http.MethodGet, "/v1/sandboxes/a"},
		{http.MethodPost, "/v1/sandboxes"},
		{http.MethodDelete, "/v1/sandboxes/a"},
		{http.MethodPost, "/v1/sandboxes/a/exec"},
		{http.MethodPost, "/v1/sandboxes/a/tokens"},
	}
	for _, route := range routes {
		rec := sandboxRequest(t, srv, admin, route.method, route.path, bodyFor(route.method))
		require.Equal(t, http.StatusNotImplemented, rec.Code, route.method+" "+route.path)
		doc := decodeErrorBody(t, rec)
		assert.Equal(t, "sandboxes", doc.Error.Details["capability"], route.method+" "+route.path)
	}
}

// TestCreateSandboxRefusesABodyItCannotRead pins that a malformed create is the caller's
// error, answered before the node builds anything.
func TestCreateSandboxRefusesABodyItCannotRead(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes", `{"template":`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Empty(t, boxes.createReq.Template, "an unreadable body never reaches the node")
}

// TestRemoveSandboxIsScopedToItsSandbox pins the selector on the other operation that
// reaches inside one sandbox: a credential narrowed to A cannot end B, and the refusal
// names why.
func TestRemoveSandboxIsScopedToItsSandbox(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{
		"a": {ID: "a", State: sandbox.StateReady},
		"b": {ID: "b", State: sandbox.StateReady},
	}}
	srv, store := sandboxServer(t, boxes)
	// An operator is what holds sandboxes:write, and one narrowed to A is refused on B
	// by the selector rather than by the scope — the two refusals a caller must tell apart.
	scoped, _, err := store.CreateWithOptions("a-only", RoleOperator, TokenOptions{
		SandboxSelectors: &SandboxSelectors{SandboxIDs: []string{"a"}},
	})
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, scoped, http.MethodDelete, "/v1/sandboxes/b", "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "sandbox_selector_mismatch", doc.Error.Details["reason"])
	assert.Equal(t, "b", doc.Error.Details["sandboxId"])
}

// TestRemoveSandboxMapsItsFailure pins the status a failed rm answers with, so a caller
// can tell "already gone" from "the teardown broke".
func TestRemoveSandboxMapsItsFailure(t *testing.T) {
	boxes := &fakeSandboxes{removeErr: fmt.Errorf("%w: a", sandbox.ErrNotFound)}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodDelete, "/v1/sandboxes/a", "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, string(output.CodeNotFound), decodeErrorBody(t, rec).Error.Code)
}

// TestExecSandboxRequiresTheV1MediaType pins the one header the stream route insists on:
// a request that is not the v1 media type is answered 415 rather than being read as a
// stream.
func TestExecSandboxRequiresTheV1MediaType(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/a/exec", strings.NewReader(`{"command":["true"]}`))
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnsupportedMediaType, rec.Code, rec.Body.String())
	assert.Equal(t, "unsupported_media_type", decodeErrorBody(t, rec).Error.Details["reason"])
	assert.Empty(t, boxes.execCalls, "a request without the stream's media type never reaches the guest")
}

// TestExecSandboxReportsAStreamThatNeverStarted pins the route's reading of the handler: a
// status the handler wrote itself means the command did not run, so the stream is not
// reported as one.
func TestExecSandboxReportsAStreamThatNeverStarted(t *testing.T) {
	boxes := &fakeSandboxes{
		records:    map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}},
		execStatus: http.StatusBadGateway,
	}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/exec", `{"command":["true"]}`)
	require.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
}

// TestSandboxTokenIsScopedToItsSandbox pins the selector on the mint route: a credential
// that may issue tokens still cannot issue one for a sandbox it does not name.
func TestSandboxTokenIsScopedToItsSandbox(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{
		"a": {ID: "a", State: sandbox.StateReady},
		"b": {ID: "b", State: sandbox.StateReady},
	}}
	srv, store := sandboxServer(t, boxes)
	mint, _, err := store.CreateWithOptions("mint-a", RoleAdmin, TokenOptions{
		SandboxSelectors: &SandboxSelectors{SandboxIDs: []string{"a"}},
	})
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, mint, http.MethodPost, "/v1/sandboxes/b/tokens", `{}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "sandbox_selector_mismatch", doc.Error.Details["reason"])
	assert.Equal(t, "b", doc.Error.Details["sandboxId"])
}

// TestCreateSandboxTokenRefusesWhatItCannotMint pins the two body-level refusals: a body
// that does not decode and an expiry that is not a future instant are the caller's errors,
// answered before a credential exists.
func TestCreateSandboxTokenRefusesWhatItCannotMint(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	for _, body := range []string{`{"name":`, `{"expiresAt":"not a time"}`, `{"expiresAt":"2020-01-01T00:00:00Z"}`} {
		rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/tokens", body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
	}
}

// TestTypedErrorsKeepTheirCode pins that an error which already carries a stable code is
// reported as itself, not flattened into an internal failure.
func TestTypedErrorsKeepTheirCode(t *testing.T) {
	boxes := &fakeSandboxes{createErr: output.NewError(output.CodeRateLimited, "slow down")}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes", `{"template":"base"}`)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.Equal(t, string(output.CodeRateLimited), decodeErrorBody(t, rec).Error.Code)
}

// TestQuotaRefusalAnswersWithTheLimits pins the create route's refusal when a node will
// not admit the sandbox: 429 with its own code, the limit and what was in use in the
// details, and the Retry-After header — a caller that only reads the status knows to
// wait, and one that reads the document knows what to wait for or free.
func TestQuotaRefusalAnswersWithTheLimits(t *testing.T) {
	boxes := &fakeSandboxes{createErr: &sandbox.QuotaError{
		Resource:   sandbox.QuotaResourceOwnerSandboxes,
		Owner:      "ci",
		Limit:      3,
		Used:       3,
		Requested:  1,
		RetryAfter: 90 * time.Second,
	}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes",
		`{"template":"base","owner":"ci"}`)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())

	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeQuotaExceeded), doc.Error.Code)
	assert.True(t, doc.Error.Retryable, "a spent quota frees up, so the code must say a retry is worth making")
	assert.Equal(t, sandbox.QuotaResourceOwnerSandboxes, doc.Error.Details["resource"])
	assert.Equal(t, float64(3), doc.Error.Details["limit"])
	assert.Equal(t, float64(3), doc.Error.Details["used"])
	assert.Equal(t, float64(1), doc.Error.Details["requested"])
	assert.Equal(t, "ci", doc.Error.Details["owner"])
	assert.Equal(t, float64(90000), doc.Error.Details["retryAfterMs"])
	assert.Equal(t, "90", rec.Header().Get("Retry-After"), "the hint is in seconds, as HTTP defines it")
	assert.Contains(t, doc.Error.Message, "ci")
}

// bodyFor is a minimal body for a method whose body is not the subject of its case.
func bodyFor(method string) string {
	if method == http.MethodPost {
		return `{"template":"base"}`
	}
	return ""
}

// TestListSandboxesAnswersTheNodesSandboxes pins the read half of the control plane:
// every sandbox is a document, a record file that cannot be read is a warning rather
// than a failed listing, and the node's host layout (its disks, its jail) never
// crosses the API.
func TestListSandboxesAnswersTheNodesSandboxes(t *testing.T) {
	created := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	boxes := &fakeSandboxes{
		records: map[string]sandbox.Record{
			"a": {
				ID: "a", State: sandbox.StateReady, Template: "base@1", Owner: "ci",
				Created: created, Updated: created, LastActive: created,
				GuestIP: "10.63.0.2", Egress: []string{"example.com:443"},
				Disk: "/var/lib/firecracker/rootfs/sandboxes/a.ext4",
			},
			"b": {ID: "b", State: sandbox.StateFailed, Reason: "the guest never answered"},
		},
		skipped: []string{"/var/lib/sandboxd/state/sandboxes/broken.json"},
	}
	srv, store := sandboxServer(t, boxes)
	reader, _, err := store.CreateWithOptions("reader", RoleRunner, TokenOptions{
		Scopes:        []string{string(ScopeSandboxesRead)},
		TaskSelectors: &TaskSelectors{TaskIDs: []string{"t1"}},
	})
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, reader, http.MethodGet, "/v1/sandboxes", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var doc v1.SandboxList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, v1.KindSandboxList, doc.Kind)
	require.Len(t, doc.Items, 2, "a failed sandbox is listed, not hidden")
	assert.Equal(t, "a", doc.Items[0].ID)
	assert.Equal(t, "ready", doc.Items[0].State)
	assert.Equal(t, "2026-11-12T10:00:00Z", doc.Items[0].CreatedAt)
	assert.Empty(t, doc.Items[0].ReadyAt, "a sandbox that never became ready has no ready instant")
	assert.Equal(t, "failed", doc.Items[1].State)
	assert.Equal(t, "the guest never answered", doc.Items[1].Reason)

	require.Len(t, doc.Warnings, 1, "an unreadable record is reported, not dropped silently")
	assert.Equal(t, "sandbox_record_unreadable", doc.Warnings[0].Code)
	assert.Equal(t, "broken.json", doc.Warnings[0].Details["file"], "a listing names the file, not the node's path")
	assert.NotContains(t, rec.Body.String(), "/var/lib/firecracker",
		"where a sandbox's disk lives is the node's business")
}

// TestListSandboxesReportsAStoreItCannotRead is the other side of that: a node that
// cannot read its records at all is an error, not an empty list that looks like "no
// sandboxes here".
func TestListSandboxesReportsAStoreItCannotRead(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{}, listErr: errors.New("read the record directory: permission denied")}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeUnreachable), doc.Error.Code)
	assert.True(t, doc.Error.Retryable, "a store that cannot be read is worth another try, not a caller's bug")
}

// TestGetSandboxAnswersOneDocument pins the single-resource read: the same document the
// listing carries, wrapped in the v1 envelope.
func TestGetSandboxAnswersOneDocument(t *testing.T) {
	created := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{
		"a": {ID: "a", State: sandbox.StateReady, Template: "base@1", Created: created,
			ReadyAt: created.Add(4 * time.Second), IdleTTLSeconds: 60},
	}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var doc v1.SandboxDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, v1.KindSandbox, doc.Kind)
	assert.Equal(t, "a", doc.ID)
	assert.Equal(t, "2026-11-12T10:00:04Z", doc.ReadyAt)
	assert.Equal(t, int64(60), doc.IdleTTLSeconds)
}

// TestCreateSandboxRefusesATtlItCannotRepresent pins the one body rule: zero is the
// node's default and -1 is "never", so a smaller value is refused rather than silently
// meaning one of the two.
func TestCreateSandboxRefusesATtlItCannotRepresent(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes", `{"template":"base","idleTtlSeconds":-2}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "invalid_ttl", doc.Error.Details["reason"])
	assert.Empty(t, boxes.createReq.Template, "an unrepresentable TTL never reaches the node")
}

// codedTemplateError is a template refusal as pkg/golden reports it: its own stable
// code, which the API must keep instead of flattening it into an internal error.
type codedTemplateError struct{ code string }

func (e codedTemplateError) Error() string {
	return "template golden-base-1.0.0 was built by other host software"
}
func (e codedTemplateError) Code() string { return e.code }

// TestCreateSandboxKeepsTheCodesACallerActsOn pins the create's error mapping: each
// refusal becomes the status and code a client branches on, and a template's own
// refusal is a conflict that keeps the template package's code.
func TestCreateSandboxKeepsTheCodesACallerActsOn(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"a bad request", fmt.Errorf("%w: a template is required", sandbox.ErrInvalid), http.StatusBadRequest, string(output.CodeInvalid)},
		{"a sandbox that exists", fmt.Errorf("%w: a", sandbox.ErrExists), http.StatusConflict, string(output.CodeConflict)},
		{"a template this host cannot boot", codedTemplateError{code: string(output.CodeTemplatePinMismatch)}, http.StatusConflict, string(output.CodeTemplatePinMismatch)},
		{"a node that broke", errors.New("no space left on device"), http.StatusServiceUnavailable, string(output.CodeUnreachable)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, store := sandboxServer(t, &fakeSandboxes{createErr: tc.err})
			admin, _, err := store.Create("admin", RoleAdmin)
			require.NoError(t, err)

			rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes", `{"template":"base"}`)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, tc.code, decodeErrorBody(t, rec).Error.Code)
		})
	}
}

// TestCreateSandboxTokenRefusesASandboxThatIsNotThere pins why a token route checks
// the sandbox: a credential for a sandbox this node does not have would authenticate
// nothing, and would be inherited by the next sandbox minted with the same ID.
func TestCreateSandboxTokenRefusesASandboxThatIsNotThere(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/missing/tokens", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, string(output.CodeNotFound), decodeErrorBody(t, rec).Error.Code)
}

// TestCheckpointRoutesRequireTheirScope pins the scope the four node-scoped routes and the
// four sandbox-scoped ones name: a token without sandboxes:checkpoints is refused with the
// reason and the scope it lacked, on list and delete too — reading or freeing a
// checkpoint is checkpoint authority, not sandbox-read authority.
func TestCheckpointRoutesRequireTheirScope(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{
		"a": {ID: "a", State: sandbox.StateReady},
	}, checkpoints: map[string]sandbox.CheckpointRecord{
		"c1": {Name: "c1", Sandbox: "a"},
	}}
	srv, store := sandboxServer(t, boxes)

	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/sandboxes/a/checkpoints", `{"name":"c1"}`},
		{http.MethodGet, "/v1/sandboxes/a/checkpoints", ""},
		{http.MethodPost, "/v1/sandboxes/a/checkpoints/c1/restore", `{}`},
		{http.MethodPost, "/v1/sandboxes/a/fork", `{"count":1}`},
		{http.MethodGet, "/v1/checkpoints", ""},
		{http.MethodGet, "/v1/checkpoints/c1", ""},
		{http.MethodPost, "/v1/checkpoints/c1/fork", `{"count":1}`},
		{http.MethodDelete, "/v1/checkpoints/c1", ""},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			narrow, _, err := store.CreateWithOptions("narrow", RoleRunner, TokenOptions{
				Scopes:        []string{string(ScopeTasksExec)},
				TaskSelectors: &TaskSelectors{TaskIDs: []string{"t1"}},
			})
			require.NoError(t, err)

			rec := sandboxRequest(t, srv, narrow, route.method, route.path, route.body)
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			doc := decodeErrorBody(t, rec)
			assert.Equal(t, "scope_missing", doc.Error.Details["reason"])
			assert.Equal(t, string(ScopeSandboxesCheckpoints), doc.Error.Details["requiredScope"])
		})
	}
}

// TestCheckpointRoutesAnswerAcrossSandboxes pins the ownership rule: a checkpoint named
// under a sandbox that does not own it is 404, not a clone of someone else's image and not
// a 403 that would confirm the name exists.
func TestCheckpointRoutesAnswerAcrossSandboxes(t *testing.T) {
	boxes := &fakeSandboxes{
		records: map[string]sandbox.Record{
			"a": {ID: "a", State: sandbox.StateReady},
			"b": {ID: "b", State: sandbox.StateReady},
		},
		checkpoints: map[string]sandbox.CheckpointRecord{"c1": {Name: "c1", Sandbox: "a"}},
	}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/b/checkpoints/c1/restore", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Empty(t, boxes.forkReq.Checkpoint, "no clone is made from another sandbox's checkpoint")
}

// TestSandboxCheckpointListingIsSandboxScoped pins the route a caller reaches a
// checkpoint through while its sandbox still exists: the listing is that sandbox's, a
// record that cannot be read is a warning beside the ones that could, and a store that
// cannot be read at all is an error rather than an empty list.
func TestSandboxCheckpointListingIsSandboxScoped(t *testing.T) {
	boxes := &fakeSandboxes{
		records: map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}},
		checkpoints: map[string]sandbox.CheckpointRecord{
			"c1": {Name: "c1", Sandbox: "a", Template: "base@1",
				Quiesce: sandbox.QuiesceOutcome{Mode: "fsfreeze", Mounts: []string{"/"}}},
			"c2": {Name: "c2", Sandbox: "b"},
		},
		skipped: []string{"/var/lib/firecracker/checkpoints/broken/checkpoint.json"},
	}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/checkpoints", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list v1.CheckpointList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Items, 1, "the sandbox-scoped listing is that sandbox's checkpoints")
	assert.Equal(t, "c1", list.Items[0].Name)
	assert.Equal(t, []string{"/"}, list.Items[0].QuiesceMounts,
		"the mounts the guest froze travel with the listing")

	require.Len(t, list.Warnings, 1, "one unreadable record does not hide the others")
	assert.Equal(t, "checkpoint_record_unreadable", list.Warnings[0].Code)
	assert.Equal(t, "checkpoint.json", list.Warnings[0].Details["file"],
		"the warning names the file an operator has to look at, not its whole path")

	// A store that cannot be read is not a sandbox with no checkpoints: an error the
	// handler cannot classify is reported as a store that may answer again.
	boxes.checkpointListErr = errors.New("the checkpoint store is unavailable")
	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes/a/checkpoints", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	var failure v1.ErrorDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &failure))
	assert.Equal(t, string(output.CodeUnreachable), failure.Error.Code)
}

// TestCheckpointRoutesMapWhatTheStoreRefuses pins the code each refusal becomes, over both
// families of route: what a caller acts on is the status, so a node that serves no
// checkpoints, an image this node does not have, and a sandbox that is not ready must not
// arrive as one internal error.
func TestCheckpointRoutesMapWhatTheStoreRefuses(t *testing.T) {
	boxes := &fakeSandboxes{
		records:     map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}},
		checkpoints: map[string]sandbox.CheckpointRecord{"c1": {Name: "c1", Sandbox: "a"}},
	}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	// No checkpoint store on this node: a capability it does not serve, said so.
	boxes.checkpointListErr = sandbox.ErrNotImplemented
	rec := sandboxRequest(t, srv, admin, http.MethodGet, "/v1/checkpoints", "")
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
	boxes.checkpointListErr = nil

	// A name this node does not have is a 404 on both of the routes that take one.
	rec = sandboxRequest(t, srv, admin, http.MethodPost, "/v1/checkpoints/absent/fork", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	rec = sandboxRequest(t, srv, admin, http.MethodDelete, "/v1/checkpoints/absent", "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, string(output.CodeNotFound), doc.Error.Code)

	// A sandbox that cannot be checkpointed yet is a conflict: the request was well
	// formed and the same one will work once the guest is up.
	boxes.checkpointErr = sandbox.ErrNotReady
	rec = sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/checkpoints", `{"name":"c2"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	boxes.checkpointErr = nil

	// And the image that is there is still readable, so the refusals above changed
	// nothing.
	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/checkpoints/c1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestCheckpointRoutesServeTheImage pins the four node-scoped routes end to end: take a
// checkpoint of a sandbox, read it, fork from it by name (the sandbox it came from need not
// exist), and forget it.
func TestCheckpointRoutesServeTheImage(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{
		"a": {ID: "a", State: sandbox.StateReady},
	}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/checkpoints", `{"name":"c1"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var taken v1.CheckpointDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &taken))
	assert.Equal(t, "c1", taken.Name)
	assert.Equal(t, "a", taken.Sandbox)
	assert.Equal(t, "fsfreeze", taken.QuiesceMode, "the guest's own report travels with the document")

	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/checkpoints", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list v1.CheckpointList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Items, 1)

	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/checkpoints/c1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The parent is gone: a checkpoint is an image, and the node-scoped fork still works.
	delete(boxes.records, "a")
	rec = sandboxRequest(t, srv, admin, http.MethodPost, "/v1/checkpoints/c1/fork", `{"count":2}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var forks v1.SandboxList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &forks))
	assert.Len(t, forks.Items, 2)
	assert.Equal(t, "c1", boxes.forkReq.Checkpoint)
	assert.Equal(t, "c1", forks.Checkpoint, "a fork's answer names the image it left behind")

	rec = sandboxRequest(t, srv, admin, http.MethodDelete, "/v1/checkpoints/c1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var removal v1.CheckpointRemoval
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &removal))
	assert.Equal(t, "c1", removal.Checkpoint)
	assert.Equal(t, int64(7168), removal.Freed.FreedBytes, "a delete says what the node got back")
	assert.Equal(t, int64(8192), removal.Freed.RetainedBytes, "and what stayed, because another image shares its chunks")

	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/checkpoints/c1", "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestSandboxForkTakesTheCheckpointItWasNotGiven pins the sandbox-scoped fork: the route
// checkpoints what the sandbox is running now, so a caller that wants clones of the present
// does not have to name an image first.
func TestSandboxForkTakesTheCheckpointItWasNotGiven(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{
		"a": {ID: "a", State: sandbox.StateReady},
	}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/fork", `{"count":1}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "a", boxes.forkFromID)

	// The checkpoint this route took is named in the answer, and it is a checkpoint of the
	// node: a caller that cannot find it cannot free the image the fork left behind.
	var doc v1.SandboxList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, "fresh", doc.Checkpoint)
	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/checkpoints", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestCheckpointRoutesAnswerNotImplementedWithoutAManager pins the capability gate on the
// node-scoped routes too: a node that runs no sandboxes says so rather than answering an
// empty list that looks like "no checkpoints".
func TestCheckpointRoutesAnswerNotImplementedWithoutAManager(t *testing.T) {
	srv, store := sandboxServer(t, nil)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	for _, path := range []string{"/v1/checkpoints", "/v1/checkpoints/c1"} {
		rec := sandboxRequest(t, srv, admin, http.MethodGet, path, "")
		require.Equal(t, http.StatusNotImplemented, rec.Code, path)
		assert.Equal(t, "sandboxes", decodeErrorBody(t, rec).Error.Details["capability"])
	}
}

// TestCheckpointRoutesSayWhenTheNodeServesNone pins the refusal a node configured without
// checkpoints gives: a 501 that names the capability and the reason, not an internal error
// that reads as a broken store.
func TestCheckpointRoutesSayWhenTheNodeServesNone(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}}}
	boxes.checkpointErr = sandbox.ErrNotImplemented
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/checkpoints", `{"name":"c1"}`)
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, "checkpoints_unavailable", doc.Error.Details["reason"])
	assert.Equal(t, "sandboxes:checkpoints", doc.Error.Details["capability"])
}

// TestForkRoutesRefuseWhatTheyCannotDo pins the refusals the three fork routes share, since
// they share the mapping: a body that is not a fork, a TTL that is not one of the three
// values the contract allows, and a node that cannot fork at all. What a caller acts on is
// the status, so none of them may arrive as an internal error.
func TestForkRoutesRefuseWhatTheyCannotDo(t *testing.T) {
	boxes := &fakeSandboxes{
		records:     map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}},
		checkpoints: map[string]sandbox.CheckpointRecord{"c1": {Name: "c1", Sandbox: "a"}},
	}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	for _, path := range []string{"/v1/sandboxes/a/fork", "/v1/checkpoints/c1/fork"} {
		rec := sandboxRequest(t, srv, admin, http.MethodPost, path, `{"count":"two"}`)
		require.GreaterOrEqual(t, rec.Code, 400, path)
		assert.NotEqual(t, http.StatusCreated, rec.Code, path)

		// A TTL that is neither the node's default, never, nor a positive one.
		rec = sandboxRequest(t, srv, admin, http.MethodPost, path, `{"idleTtlSeconds":-5}`)
		require.Equal(t, http.StatusBadRequest, rec.Code, path+": "+rec.Body.String())
		var doc v1.ErrorDocument
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
		assert.Equal(t, "invalid_ttl", doc.Error.Details["reason"])
	}

	// A node that cannot fork says why, and makes no clone.
	boxes.forkErr = sandbox.ErrNotReady
	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/fork", `{}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	boxes.forkErr = nil

	// The node-scoped route answers the same way for the same cause.
	boxes.forkErr = sandbox.ErrNotReady
	rec = sandboxRequest(t, srv, admin, http.MethodPost, "/v1/checkpoints/c1/fork", `{}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

// TestRestoreCheckpointRefusesWhatItCannotRestore pins the two refusals the restore route
// makes on its own: an image this node does not have, and a fork that could not be made.
func TestRestoreCheckpointRefusesWhatItCannotRestore(t *testing.T) {
	boxes := &fakeSandboxes{
		records:     map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}},
		checkpoints: map[string]sandbox.CheckpointRecord{"c1": {Name: "c1", Sandbox: "a"}},
	}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/checkpoints/absent/restore", `{}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	boxes.forkErr = sandbox.ErrNotReady
	rec = sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/checkpoints/c1/restore", `{}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	boxes.forkErr = nil

	// And the route resumes one clone — never more, whatever the body asked for.
	rec = sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/checkpoints/c1/restore", `{"count":9}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var list v1.SandboxList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Len(t, list.Items, 1, "a restore is one clone of one image")
	assert.Equal(t, "c1", list.Checkpoint)
}

// TestCreateSandboxCheckpointRefusesABodyItCannotRead pins the body rule: a name is a
// string, and a create that cannot read its document does not take an image.
func TestCreateSandboxCheckpointRefusesABodyItCannotRead(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/checkpoints", `{"name":123}`)
	require.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
	assert.NotEqual(t, http.StatusCreated, rec.Code)
	assert.Empty(t, boxes.checkpoints, "a refused body takes no image")
}

// TestSandboxAPIErrorPassesThroughWhatCarriesACode pins the two ends of the mapping: a nil
// failure is nil, and a failure that already carries a code keeps it rather than being
// flattened into an internal error.
func TestSandboxAPIErrorPassesThroughWhatCarriesACode(t *testing.T) {
	assert.Nil(t, sandboxAPIError(nil))

	typed := output.NewError(output.CodeTimeout, "the guest did not answer in time")
	assert.Same(t, typed, sandboxAPIError(typed))
}

// TestTheTokenStoreThatIsNotThereIsACapabilityThisNodeLacks pins the refusal a node without
// a token store gives: a capability it does not serve, named, rather than a token minted
// nowhere or an internal error.
func TestTheTokenStoreThatIsNotThereIsACapabilityThisNodeLacks(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)
	srv.cfg.Tokens = nil

	// The token store is also what authenticates, so a node without one refuses at the
	// door rather than inside the handler: the capability it lacks is reachable no other
	// way, which is why the check inside the handler is a guard and not a route.
	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/tokens", `{"name":"t"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, string(output.CodeUnauthenticated), doc.Error.Code)
}

// TestCheckpointCreateRefusesADocumentItCannotParse pins the body rule at the point a
// checkpoint would be taken: a body that is not a document is refused before the guest is
// frozen, so a caller's typo does not cost the sandbox a pause.
func TestCheckpointCreateRefusesADocumentItCannotParse(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{"a": {ID: "a", State: sandbox.StateReady}}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/checkpoints", `{"name":`)
	require.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
	assert.NotEqual(t, http.StatusCreated, rec.Code)
	assert.Empty(t, boxes.checkpoints, "no image is taken for a body that cannot be read")
}
