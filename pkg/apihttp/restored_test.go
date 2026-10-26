package apihttp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// TestRestoredStopPinsTheRoute proves DELETE /v1/tasks/{id}/restored resolves the
// task, calls the transport once and answers 204 with the documented audit
// action.
func TestRestoredStopPinsTheRoute(t *testing.T) {
	broker := &fakeSnapshotBroker{}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	rec := api.do(t, http.MethodDelete, "/v1/tasks/web-1/restored", withSnapshotToken(api.admin))

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String(), "204 carries no document")
	require.Equal(t, 1, broker.stopCalls)
	assert.Equal(t, "task-1", broker.stoppedTask.ID)
	assert.Equal(t, "svc-1", broker.stoppedTask.ServiceID)
	assert.False(t, broker.gotDeadline, "the manager owns the snapshot deadline; the handler adds none")

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.restored.stop", records[0].Action)
	assert.Equal(t, "ok", records[0].Result)
	assert.Equal(t, v1.KindTask, records[0].Resource.Kind)
	assert.Equal(t, "task-1", records[0].Resource.ID)
	assert.Equal(t, "web.1", records[0].Resource.Name)
}

// TestRestoredStopMapsNoRestoredVM pins the documented 404: the node's typed
// not-found is relayed and the attempt is audited as an error.
func TestRestoredStopMapsNoRestoredVM(t *testing.T) {
	broker := &fakeSnapshotBroker{stopErr: output.NewError(output.CodeNotFound,
		"task has no restored VM on this node").
		WithDetails(map[string]any{"reason": "no_restored_vm"})}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	rec := api.do(t, http.MethodDelete, "/v1/tasks/web-1/restored", withSnapshotToken(api.admin))

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeNotFound), doc.Error.Code)
	assert.Equal(t, "no_restored_vm", doc.Error.Details["reason"])

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.restored.stop", records[0].Action)
	assert.Equal(t, "error", records[0].Result)
}

// TestRestoredStopRequiresTheSnapshotScope pins the scope: a reader token is 403
// with details.requiredScope and never reaches the transport.
func TestRestoredStopRequiresTheSnapshotScope(t *testing.T) {
	broker := &fakeSnapshotBroker{}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	rec := api.do(t, http.MethodDelete, "/v1/tasks/web-1/restored", withSnapshotToken(api.reader))

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(ScopeTasksSnapshots), doc.Error.Details["requiredScope"])
	assert.Zero(t, broker.stopCalls)
}

// TestRestoredStopRejectsUnknownQuery pins the empty query grammar at the route
// layer: a parameter this route does not declare is 400, not silently ignored.
func TestRestoredStopRejectsUnknownQuery(t *testing.T) {
	broker := &fakeSnapshotBroker{}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	rec := api.do(t, http.MethodDelete, "/v1/tasks/web-1/restored?id=snap-1",
		withSnapshotToken(api.admin))

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Zero(t, broker.stopCalls)
}

// TestRestoredStopHasNoOtherMethod pins 405 with Allow on a method the route does
// not implement, including when the capability is enabled.
func TestRestoredStopHasNoOtherMethod(t *testing.T) {
	broker := &fakeSnapshotBroker{}
	api := newSnapshotTestAPI(t, nil, broker, nil)

	rec := api.do(t, http.MethodGet, "/v1/tasks/web-1/restored", withSnapshotToken(api.admin))

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Header().Get("Allow"), http.MethodDelete)
	assert.Zero(t, broker.stopCalls)
}

// TestRestoredStopUnknownTaskAuditsTheStopAction pins the audit action of a
// resolve failure. An unknown task never reaches the transport, but the attempt
// still belongs to the stop route: recording task.snapshot for it would file a
// stop under the snapshot action.
func TestRestoredStopUnknownTaskAuditsTheStopAction(t *testing.T) {
	client := &execFakeClient{
		fakeClient: &fakeClient{},
		taskErr:    output.NewError(output.CodeNotFound, "task not found"),
	}
	broker := &fakeSnapshotBroker{}
	api := newSnapshotTestAPI(t, client, broker, nil)

	rec := api.do(t, http.MethodDelete, "/v1/tasks/ghost/restored", withSnapshotToken(api.admin))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Zero(t, broker.stopCalls)

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.restored.stop", records[0].Action)
	assert.Equal(t, "error", records[0].Result)
	assert.Equal(t, "ghost", records[0].Resource.Name,
		"an unresolved reference is recorded as the caller's reference")
}

// TestRestoredStopSelectorDeniedAuditsTheStopAction pins the same rule for a
// resolved task the caller's selector refuses: the denial is a
// task.restored.stop error, not a snapshot one.
func TestRestoredStopSelectorDeniedAuditsTheStopAction(t *testing.T) {
	client := &execFakeClient{
		fakeClient: &fakeClient{},
		serviceErr: output.NewError(output.CodeNotFound, "no such service"),
	}
	client.task = v1.Task{ID: "task-1", ServiceID: "svc-1", Name: "web.1"}
	broker := &fakeSnapshotBroker{}
	api := newSnapshotTestAPI(t, client, broker, nil)

	rec := api.do(t, http.MethodDelete, "/v1/tasks/web-1/restored", withSnapshotToken(api.byLabel))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Zero(t, broker.stopCalls)

	records, _, err := api.audit.List(10, "")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "task.restored.stop", records[0].Action)
	assert.Equal(t, "error", records[0].Result)
	assert.Equal(t, "task-1", records[0].Resource.ID)
}
