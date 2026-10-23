package apihttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func doEvents(t *testing.T, api *writeTestAPI, target string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Accept", "text/event-stream")
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	api.handler.ServeHTTP(rec, req)
	return rec
}

func closedEventClient(event v1.Event) *fakeClient {
	ch := make(chan v1.Event, 1)
	ch <- event
	close(ch)
	return &fakeClient{events: ch}
}

func TestEventsSSEFrames(t *testing.T) {
	fc := closedEventClient(v1.Event{
		SchemaVersion: v1.SchemaVersion,
		Kind:          v1.KindEvent,
		Cursor:        "17",
		Type:          "service.updated",
		Resource:      v1.EventResource{Kind: v1.KindService, ID: "svc-1", Name: "web"},
		Labels:        map[string]string{"team": "payments"},
	})
	api := newWriteTestAPI(t, fc)

	rec := doEvents(t, api, "/v1/events?types=service&since=16&include=object",
		withToken(api.operator))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	body := rec.Body.String()
	assert.Contains(t, body, "event: ready")
	assert.Contains(t, body, "id: 17")
	assert.Contains(t, body, "event: service.updated")
	assert.Contains(t, body, `"type":"service.updated"`)
	assert.Contains(t, body, `"cursor":"17"`)

	assert.Equal(t, []string{"service"}, fc.gotEventOpts.Types)
	assert.Equal(t, "16", fc.gotEventOpts.Since)
	assert.True(t, fc.gotEventOpts.IncludeObject)
	assert.True(t, fc.gotEventOpts.IncludeConfigValues, "operator has configs:read-values")
}

func TestEventsNDJSONFrames(t *testing.T) {
	fc := closedEventClient(v1.Event{
		SchemaVersion: v1.SchemaVersion, Kind: v1.KindEvent, Cursor: "5",
		Type: "node.removed", Resource: v1.EventResource{Kind: v1.KindNode, ID: "n1", Name: "worker-a"},
	})
	api := newWriteTestAPI(t, fc)

	rec := doEvents(t, api, "/v1/events", withToken(api.reader),
		withHeader("Accept", "application/x-ndjson"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	assert.Contains(t, lines[0], `"type":"ready"`)
	assert.Contains(t, lines[1], `"type":"node.removed"`)
	assert.False(t, fc.gotEventOpts.IncludeConfigValues, "reader has no configs:read-values")
}

func TestEventsLabelFilter(t *testing.T) {
	fc := closedEventClient(v1.Event{
		SchemaVersion: v1.SchemaVersion, Kind: v1.KindEvent, Cursor: "3",
		Type: "service.updated", Resource: v1.EventResource{Kind: v1.KindService, ID: "s1", Name: "web"},
		Labels: map[string]string{"team": "payments"},
	})
	api := newWriteTestAPI(t, fc)

	rec := doEvents(t, api, "/v1/events?label=team=other", withToken(api.reader))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "service.updated", "the label filter drops the event")
	assert.Contains(t, rec.Body.String(), "event: ready")
}

func TestEventsInvalidQuery(t *testing.T) {
	api := newWriteTestAPI(t, &fakeClient{})

	for _, target := range []string{
		"/v1/events?types=volume",
		"/v1/events?since=not-a-cursor",
		"/v1/events?include=everything",
		"/v1/events?label==",
	} {
		rec := doEvents(t, api, target, withToken(api.reader))
		assert.Equal(t, http.StatusBadRequest, rec.Code, target)
	}
}

func TestEventsCursorExpired(t *testing.T) {
	fc := &fakeClient{watchErr: output.NewError(output.CodeCursorExpired, "compacted").
		WithDetails(map[string]any{"action": "relist"})}
	api := newWriteTestAPI(t, fc)

	rec := doEvents(t, api, "/v1/events?since=1", withToken(api.reader))
	require.Equal(t, http.StatusGone, rec.Code, rec.Body.String())
	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeCursorExpired), doc.Error.Code)
	assert.Equal(t, "relist", doc.Error.Details["action"])
}

func TestEventsLastEventIDResumes(t *testing.T) {
	fc := closedEventClient(v1.Event{SchemaVersion: v1.SchemaVersion, Kind: v1.KindEvent, Cursor: "9"})
	api := newWriteTestAPI(t, fc)

	rec := doEvents(t, api, "/v1/events", withToken(api.reader),
		withHeader("Last-Event-ID", "8"))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "8", fc.gotEventOpts.Since)
}

func TestEventsStreamErrorIsAFrame(t *testing.T) {
	events := make(chan v1.Event)
	errs := make(chan error, 1)
	errs <- output.NewError(output.CodeCursorExpired, "compacted")
	close(errs)
	api := newWriteTestAPI(t, &fakeClient{events: events, eventErrs: errs})

	rec := doEvents(t, api, "/v1/events?since=1", withToken(api.reader))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "event: error")
	assert.Contains(t, rec.Body.String(), `"code":"cursor_expired"`)
}

func TestEventsRequiresEventsReadScope(t *testing.T) {
	// A token narrowed to services:read must not open the event stream.
	api := newWriteTestAPI(t, &fakeClient{})
	store := api.store
	secret, _, err := store.CreateWithOptions("narrow", RoleReader, TokenOptions{Scopes: []string{string(ScopeServicesRead)}})
	require.NoError(t, err)

	rec := doEvents(t, api, "/v1/events", withToken(secret))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc := decodeError(t, rec)
	assert.Equal(t, string(ScopeEventsRead), doc.Error.Details["requiredScope"])
}

func TestEventQueryIncludesConfigValuesOnlyForPrivileged(t *testing.T) {
	fc := &fakeClient{}
	api := newWriteTestAPI(t, fc)
	fc.events = make(chan v1.Event)
	close(fc.events)

	_ = doEvents(t, api, "/v1/events", withToken(api.operator))
	assert.True(t, fc.gotEventOpts.IncludeConfigValues)

	_ = doEvents(t, api, "/v1/events", withToken(api.reader))
	assert.False(t, fc.gotEventOpts.IncludeConfigValues)
}

// ensure the fake's context signature is exercised without a real stream.
var _ = context.Background
