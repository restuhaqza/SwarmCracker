package client

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// TestStopRestoredTaskSendsDelete pins the remote method: DELETE on the task's
// restored route, with no body, and 204 accepted as success.
func TestStopRestoredTaskSendsDelete(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotBody string
	c := newTestRemote(t, Config{Token: "sc_test"}, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		gotBody = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	})

	require.NoError(t, c.StopRestoredTask(context.Background(), "task-1"))
	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/v1/tasks/task-1/restored", gotPath)
	assert.Equal(t, "Bearer sc_test", gotAuth)
	assert.Empty(t, gotBody, "the operation takes no query")
}

// TestStopRestoredTaskRelaysNoRestoredVM pins the typed error: the API's 404 is
// preserved with its reason, not flattened into a generic failure.
func TestStopRestoredTaskRelaysNoRestoredVM(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		writeTestDocument(w, http.StatusNotFound, map[string]any{
			"schemaVersion": 1,
			"kind":          "Error",
			"error": map[string]any{
				"code":    "not_found",
				"message": "task has no restored VM on this node",
				"details": map[string]any{"reason": "no_restored_vm"},
			},
		})
	})

	err := c.StopRestoredTask(context.Background(), "task-1")
	typed := codeOf(t, err)
	assert.Equal(t, output.CodeNotFound, typed.Code)
	assert.Equal(t, "no_restored_vm", typed.Details["reason"])
}

// TestStopRestoredTaskRejectsANon204Success pins the wire contract: a 2xx that is
// not 204 did not report a stopped VM.
func TestStopRestoredTaskRejectsANon204Success(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, _ *http.Request) {
		writeTestDocument(w, http.StatusOK, map[string]any{"ok": true})
	})

	err := c.StopRestoredTask(context.Background(), "task-1")
	typed := codeOf(t, err)
	assert.Equal(t, output.CodeInternal, typed.Code)
}

// TestStopRestoredTaskNeedsARemoteEndpoint pins the local refusal: no control
// socket call can stop a restored VM, because the node tracks its process.
func TestStopRestoredTaskNeedsARemoteEndpoint(t *testing.T) {
	c := &Client{}

	err := c.StopRestoredTask(context.Background(), "task-1")
	typed := codeOf(t, err)
	assert.Equal(t, output.CodeUsage, typed.Code)
}
