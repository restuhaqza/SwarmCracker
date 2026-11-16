package apihttp

// The sandbox event stream: the bus itself, the documents the sandbox routes
// publish, and the one stream that carries them beside the cluster watch.

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox"
)

// nextSandboxChange is the change a route (or a loop) published, as a watcher of the bus
// sees it.
func nextSandboxChange(t *testing.T, watcher <-chan sandbox.Change) sandbox.Change {
	t.Helper()
	select {
	case change := <-watcher:
		return change
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was published on the sandbox bus")
		return sandbox.Change{}
	}
}

// failedCreate is fakeSandboxes whose create leaves a failed record behind, which is
// the shape pkg/sandbox answers with: the record says what the sandbox is, and the
// error says why it is not usable.
type failedCreate struct {
	*fakeSandboxes
	rec sandbox.Record
}

func (f failedCreate) Create(context.Context, sandbox.CreateRequest) (sandbox.Record, error) {
	return f.rec, sandbox.ErrInvalid
}

// TestCreateSandboxPublishesACreatedEvent pins the document a created sandbox
// announces: the type name, the resource kind and id, the record's own clock, and the
// sandbox document itself for a watcher that asked for objects.
func TestCreateSandboxPublishesACreatedEvent(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	watcher, stop := srv.cfg.SandboxEvents.Subscribe()
	defer stop()

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes",
		`{"template":"base","id":"sbx-1","owner":"ci"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	change := nextSandboxChange(t, watcher)
	assert.Equal(t, sandbox.ChangeCreated, change.Kind, "the bus carries what happened, not the wire type")
	assert.Equal(t, "sbx-1", change.ID)
	require.NotNil(t, change.Record)

	event := sandboxChangeEvent(change)
	assert.Equal(t, "sandbox.created", event.Type)
	assert.Equal(t, v1.EventTypeSandboxCreated, event.Type)
	assert.Equal(t, v1.SchemaVersion, event.SchemaVersion)
	assert.Equal(t, v1.KindEvent, event.Kind)
	assert.Equal(t, v1.KindSandbox, event.Resource.Kind)
	assert.Equal(t, "sbx-1", event.Resource.ID)
	assert.Empty(t, event.Cursor, "a sandbox event is live-only: it has no watch cursor")
	assert.NotEmpty(t, event.At)
	object, ok := event.Object.(v1.Sandbox)
	require.True(t, ok, "the event carries the sandbox document, got %T", event.Object)
	assert.Equal(t, "sbx-1", object.ID)
	assert.Equal(t, "base", object.Template)
}

// TestFailedCreatePublishesAFailedEvent pins the two halves of a failed create: one that
// left a record announces the sandbox it failed to make, and one refused before a record
// existed announces nothing, because there is nothing to announce.
func TestFailedCreatePublishesAFailedEvent(t *testing.T) {
	t.Run("a failed record is announced", func(t *testing.T) {
		failed := sandbox.Record{ID: "sbx-1", State: sandbox.StateFailed, Reason: "the disk could not be copied"}
		boxes := failedCreate{fakeSandboxes: &fakeSandboxes{records: map[string]sandbox.Record{}}, rec: failed}
		srv, store := sandboxServer(t, boxes)
		admin, _, err := store.Create("admin", RoleAdmin)
		require.NoError(t, err)

		watcher, stop := srv.cfg.SandboxEvents.Subscribe()
		defer stop()

		rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes", `{"template":"base","id":"sbx-1"}`)
		require.NotEqual(t, http.StatusCreated, rec.Code)

		change := nextSandboxChange(t, watcher)
		assert.Equal(t, sandbox.ChangeFailed, change.Kind)
		require.NotNil(t, change.Record)
		assert.Equal(t, "the disk could not be copied", change.Record.Reason)

		event := sandboxChangeEvent(change)
		assert.Equal(t, "sandbox.failed", event.Type)
		assert.Equal(t, v1.EventTypeSandboxFailed, event.Type)
		assert.Equal(t, "sbx-1", event.Resource.ID)
	})

	t.Run("a refused create is silent", func(t *testing.T) {
		boxes := &fakeSandboxes{
			records: map[string]sandbox.Record{},
			// A create refused before a record existed: nothing to announce.
			createErr: sandbox.ErrInvalid,
		}
		srv, store := sandboxServer(t, boxes)
		admin, _, err := store.Create("admin", RoleAdmin)
		require.NoError(t, err)

		watcher, stop := srv.cfg.SandboxEvents.Subscribe()
		defer stop()

		rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes", `{"template":"base"}`)
		require.Equal(t, http.StatusBadRequest, rec.Code)

		select {
		case change := <-watcher:
			t.Fatalf("a create refused before a record existed published %q", change.Kind)
		case <-time.After(100 * time.Millisecond):
		}
	})
}

// TestRemoveSandboxPublishesARemovalWithoutAnObject pins the removal document: the id
// and nothing else, because the record it would describe is already gone.
func TestRemoveSandboxPublishesARemovalWithoutAnObject(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{"sbx-1": {ID: "sbx-1", State: sandbox.StateReady}}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	watcher, stop := srv.cfg.SandboxEvents.Subscribe()
	defer stop()

	rec := sandboxRequest(t, srv, admin, http.MethodDelete, "/v1/sandboxes/sbx-1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	change := nextSandboxChange(t, watcher)
	assert.Equal(t, sandbox.ChangeRemoved, change.Kind)
	assert.Nil(t, change.Record, "a removed sandbox has no record to carry")

	event := sandboxChangeEvent(change)
	assert.Equal(t, "sandbox.removed", event.Type)
	assert.Equal(t, v1.KindSandbox, event.Resource.Kind)
	assert.Equal(t, "sbx-1", event.Resource.ID)
	assert.Nil(t, event.Object, "a removed sandbox has no document to carry")
}

// TestForkPublishesOneCreatedEventPerClone pins that a clone announces itself as what it
// is — a sandbox that was created — so a consumer needs no second type for a fork.
func TestForkPublishesOneCreatedEventPerClone(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{"sbx-1": {ID: "sbx-1", State: sandbox.StateReady}}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	watcher, stop := srv.cfg.SandboxEvents.Subscribe()
	defer stop()

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/sbx-1/fork", `{"count":2}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	seen := make(map[string]bool)
	for i := 0; i < 2; i++ {
		change := nextSandboxChange(t, watcher)
		assert.Equal(t, sandbox.ChangeCreated, change.Kind, "a clone announces itself as what it is: created")

		event := sandboxChangeEvent(change)
		assert.Equal(t, v1.EventTypeSandboxCreated, event.Type)
		seen[event.Resource.ID] = true
	}
	assert.Equal(t, map[string]bool{"clone-0": true, "clone-1": true}, seen,
		"two clones, two changes: a fork of two is two sandboxes that were created")
}

// TestClusterEventKindsStripsTheLocalKind pins the split: the cluster client is given
// only the kinds it watches, and the caller's request for sandbox events is remembered
// separately. An empty filter means everything, which includes the local kind.
func TestClusterEventKindsStripsTheLocalKind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		types   []string
		want    []string
		sandbox bool
	}{
		{"no filter", nil, nil, true},
		{"sandbox only", []string{"sandbox"}, []string{}, true},
		{"mixed", []string{"service", "sandbox"}, []string{"service"}, true},
		{"cluster only", []string{"service"}, []string{"service"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, sandboxWanted := clusterEventKinds(tc.types)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.sandbox, sandboxWanted)
		})
	}
}

// sandboxEventServer is sandboxServer with the cluster client the caller supplies, for
// the stream tests that have to drive the cluster watch and the sandbox bus at once. A
// zero heartbeat is the server's default; a test that is about the keep-alive asks for a
// short one.
func sandboxEventServer(t *testing.T, boxes Sandboxes, client Client, heartbeat time.Duration) (*Server, *TokenStore) {
	t.Helper()
	store, err := OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	require.NoError(t, err)
	srv, err := New(Config{
		Client:         client,
		Tokens:         store,
		Sandboxes:      boxes,
		RateLimit:      NewRateLimiter(1e9, 1e9),
		EventHeartbeat: heartbeat,
	})
	require.NoError(t, err)
	return srv, store
}

// openEventStream opens a live NDJSON stream and consumes its ready frame, so the
// subscription exists before the caller triggers anything. A stream has to be read while
// it is written, which a response recorder cannot do.
func openEventStream(t *testing.T, live *httptest.Server, token, target, accept string) (*bufio.Reader, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, live.URL+target, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", accept)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Contains(t, line, "ready")

	return reader, func() {
		cancel()
		_ = resp.Body.Close()
	}
}

// nextEventLine reads one more line of the stream, or fails the test when none arrives.
func nextEventLine(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		lines <- line
	}()
	select {
	case line := <-lines:
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived on the stream")
		return ""
	}
}

// TestSandboxEventStreamCarriesTheEvents pins the stream end to end: a caller who asks
// for sandbox events gets the one a create publishes, with its document when include is
// set, and the cluster watch is not asked for anything at all.
func TestSandboxEventStreamCarriesTheEvents(t *testing.T) {
	client := &fakeClient{}
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{}}
	srv, store := sandboxEventServer(t, boxes, client, 0)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	live := httptest.NewServer(srv.Handler())
	defer live.Close()

	reader, closeStream := openEventStream(t, live, admin, "/v1/events?types=sandbox&include=object", v1.NDJSONMediaType)
	defer closeStream()

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes", `{"template":"base","id":"sbx-1"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	line := nextEventLine(t, reader)
	assert.Contains(t, line, `"type":"sandbox.created"`)
	assert.Contains(t, line, `"kind":"Sandbox"`)
	assert.Contains(t, line, `"id":"sbx-1"`)
	assert.Contains(t, line, `"cursor":""`, "a sandbox event has no watch cursor")
	assert.Contains(t, line, `"object":{"id":"sbx-1"`, "include=object embeds the document")

	assert.Nil(t, client.gotEventOpts.Types, "a sandbox-only stream opens no cluster watch")
}

// TestSandboxEventStreamMergesBothSources pins the merge: one stream, one caller, both
// the cluster's events and the node's sandbox events, with the local kind stripped from
// what the cluster watch is asked for.
func TestSandboxEventStreamMergesBothSources(t *testing.T) {
	cluster := make(chan v1.Event, 2)
	client := &fakeClient{events: cluster}
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{}}
	srv, store := sandboxEventServer(t, boxes, client, 0)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	live := httptest.NewServer(srv.Handler())
	defer live.Close()

	reader, closeStream := openEventStream(t, live, admin, "/v1/events?types=service,sandbox&include=object", v1.NDJSONMediaType)
	defer closeStream()

	assert.Equal(t, []string{"service"}, client.gotEventOpts.Types, "the cluster client is not asked for a kind it cannot watch")

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes", `{"template":"base","id":"sbx-1"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	cluster <- v1.Event{
		SchemaVersion: v1.SchemaVersion, Kind: v1.KindEvent, Cursor: "9",
		Type:     "service.updated",
		Resource: v1.EventResource{Kind: v1.KindService, ID: "svc-1", Name: "web"},
	}

	types := make(map[string]bool, 2)
	for i := 0; i < 2; i++ {
		line := nextEventLine(t, reader)
		for _, want := range []string{"sandbox.created", "service.updated"} {
			if strings.Contains(line, `"type":"`+want+`"`) {
				types[want] = true
			}
		}
	}
	assert.Equal(t, map[string]bool{"sandbox.created": true, "service.updated": true}, types,
		"one stream carries both sources")
}

// nextLineContaining reads lines until one contains want, for frames whose length is
// not one line: an SSE frame is three, and the ready frame's are skipped on the way.
func nextLineContaining(t *testing.T, reader *bufio.Reader, want string) string {
	t.Helper()
	lines := make(chan string, 16)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			lines <- line
			if err != nil {
				close(lines)
				return
			}
		}
	}()

	timeout := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the stream ended before a line contained %q", want)
			}
			if strings.Contains(line, want) {
				return line
			}
		case <-timeout:
			t.Fatalf("no line containing %q arrived on the stream", want)
		}
	}
}

// TestSandboxEventStreamHeartbeats pins the keep-alive on a sandbox-only stream: a caller
// watching for sandbox events opens no cluster watch, so the heartbeat is the only thing
// telling it the stream is still alive.
func TestSandboxEventStreamHeartbeats(t *testing.T) {
	for _, tc := range []struct {
		name   string
		accept string
		want   string
	}{
		{"ndjson frame", v1.NDJSONMediaType, `"type":"ping"`},
		{"sse comment", "text/event-stream", ": ping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			boxes := &fakeSandboxes{records: map[string]sandbox.Record{}}
			srv, store := sandboxEventServer(t, boxes, &fakeClient{}, 20*time.Millisecond)
			admin, _, err := store.Create("admin", RoleAdmin)
			require.NoError(t, err)

			live := httptest.NewServer(srv.Handler())
			defer live.Close()

			reader, closeStream := openEventStream(t, live, admin, "/v1/events?types=sandbox", tc.accept)
			defer closeStream()

			nextLineContaining(t, reader, tc.want)
		})
	}
}

// TestSandboxEventTypesAreTheContractsNames pins the one rule that turns a change kind into
// an event type against the v1 constants the contract documents: the resource's kind,
// lowercased, then the change's own name. A renamed constant fails here rather than
// appearing mid-stream as a type nobody filters on.
func TestSandboxEventTypesAreTheContractsNames(t *testing.T) {
	assert.Equal(t, v1.EventTypeSandboxCreated, sandboxEventType(sandbox.ChangeCreated))
	assert.Equal(t, v1.EventTypeSandboxFailed, sandboxEventType(sandbox.ChangeFailed))
	assert.Equal(t, v1.EventTypeSandboxRemoved, sandboxEventType(sandbox.ChangeRemoved))
}

// TestSandboxEventStreamCarriesWhatTheNodesLoopsPublish is the other origin of a change: not
// an API call, but the node's own loops — the reconcile pass failing a sandbox whose VMM is
// gone, and the reaper taking an idle one back. They publish on the same bus the API
// subscribes to, so a watcher sees them on the same stream, with the same rules (no cursor;
// the object only when the caller asked for it, and never for a removal).
func TestSandboxEventStreamCarriesWhatTheNodesLoopsPublish(t *testing.T) {
	client := &fakeClient{}
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{}}
	srv, store := sandboxEventServer(t, boxes, client, 0)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	live := httptest.NewServer(srv.Handler())
	defer live.Close()

	reader, closeStream := openEventStream(t, live, admin, "/v1/events?types=sandbox&include=object", v1.NDJSONMediaType)
	defer closeStream()

	// What a reconcile pass publishes: the record as it was marked, with the reason a
	// watcher cannot read anywhere else.
	lost := sandbox.Record{
		ID: "sbx-lost", State: sandbox.StateFailed, Updated: time.Now().UTC(),
		Reason: "the sandbox's VMM is gone: the instance is no longer in this node's registry",
	}
	srv.cfg.SandboxEvents.Publish(sandbox.RecordChange(sandbox.ChangeFailed, lost, time.Now()))
	// What the reaper publishes: a removal with no record, because there is none any more.
	srv.cfg.SandboxEvents.Publish(sandbox.RemovalChange("sbx-reaped", time.Now()))

	failed := nextEventLine(t, reader)
	assert.Contains(t, failed, `"type":"sandbox.failed"`)
	assert.Contains(t, failed, `"id":"sbx-lost"`)
	assert.Contains(t, failed, `"cursor":""`, "a change from a loop has no watch cursor either")
	assert.Contains(t, failed, "no longer in this node's registry",
		"the reason a watcher acts on travels with the event")

	removed := nextEventLine(t, reader)
	assert.Contains(t, removed, `"type":"sandbox.removed"`)
	assert.Contains(t, removed, `"id":"sbx-reaped"`)
	assert.NotContains(t, removed, `"object"`, "a removal carries no object, even with include=object")

	assert.Nil(t, client.gotEventOpts.Types, "a sandbox-only stream still opens no cluster watch")
}
