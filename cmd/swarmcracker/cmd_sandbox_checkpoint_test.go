package main

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
)

// TestSandboxCheckpointRemovePrintsWhatItFreedAndWhatItRetained pins the whole point of the
// removal response: a checkpoint's memory image is chunked and shared, so "removed" is not
// "the disk came back". --json must carry the API's own numbers rather than a summary.
func TestSandboxCheckpointRemovePrintsWhatItFreedAndWhatItRetained(t *testing.T) {
	resetAPIFlags(t)
	outputFormat = "json"

	doc := v1.NewCheckpointRemoval("c1")
	doc.Freed = v1.CheckpointFreed{
		StateBytes:    23526,
		DiskBytes:     2147483648,
		ChunkBytes:    134217728,
		Chunks:        32,
		FreedBytes:    2281724902,
		RetainedBytes: 536870912,
		Retained:      1,
	}

	var path string
	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		writeAPITestDocument(w, http.StatusOK, doc)
	})
	apiHost, apiCA = endpoint, caFile

	out, err := executeSandboxCommand(t, newSandboxCheckpointCommand(), "rm", "c1")
	require.NoError(t, err)
	assert.Equal(t, "DELETE /v1/checkpoints/c1", path)

	// The numbers as the API stated them: freed and retained, chunks and bytes.
	for _, want := range []string{`"freed"`, `"retainedBytes": 536870912`, `"chunkBytes": 134217728`,
		`"chunks": 32`, `"stateBytes": 23526`, `"diskBytes": 2147483648`, `"freedBytes": 2281724902`} {
		assert.Contains(t, out, want)
	}
	// And nothing about where the bytes live: the node's paths are not the caller's.
	assert.NotContains(t, out, "/var/lib")
	assert.NotContains(t, out, "jail")
}

// TestSandboxCheckpointRemoveTableSeparatesFreedFromRetained is the same claim in the table
// the human sees, because that is the rendering an operator reads before deciding the disk
// came back.
func TestSandboxCheckpointRemoveTableSeparatesFreedFromRetained(t *testing.T) {
	resetAPIFlags(t)

	doc := v1.NewCheckpointRemoval("c1")
	doc.Freed = v1.CheckpointFreed{Chunks: 32, FreedBytes: 1024, RetainedBytes: 4096, Retained: 2}

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeAPITestDocument(w, http.StatusOK, doc)
	})
	apiHost, apiCA = endpoint, caFile

	out, err := executeSandboxCommand(t, newSandboxCheckpointCommand(), "rm", "c1")
	require.NoError(t, err)
	assert.Contains(t, out, "c1 removed")
	assert.Contains(t, out, "freed:")
	assert.Contains(t, out, "retained: 4.0 KB referenced by 2 checkpoint(s)")
	assert.Contains(t, out, "not deduplicated")
}

// TestSandboxCheckpointListIsNodeScopedOrSandboxScoped pins which route each form takes:
// the node-scoped listing is the one that still finds the images of a sandbox that no
// longer exists.
func TestSandboxCheckpointListIsNodeScopedOrSandboxScoped(t *testing.T) {
	resetAPIFlags(t)
	outputFormat = "name"

	var paths []string
	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		writeAPITestDocument(w, http.StatusOK, v1.NewCheckpointList([]v1.Checkpoint{
			{Name: "c1", Sandbox: "sb-1"},
			{Name: "c2", Sandbox: "sb-1"},
		}))
	})
	apiHost, apiCA = endpoint, caFile

	out, err := executeSandboxCommand(t, newSandboxCheckpointCommand(), "ls")
	require.NoError(t, err)
	assert.Equal(t, "c1\nc2\n", out)

	out, err = executeSandboxCommand(t, newSandboxCheckpointCommand(), "ls", "sb-1")
	require.NoError(t, err)
	assert.Equal(t, "c1\nc2\n", out)

	assert.Equal(t, []string{"/v1/checkpoints", "/v1/sandboxes/sb-1/checkpoints"}, paths)
}

// TestSandboxCheckpointCreateSendsTheNameTheCallerChose pins the create body: the name is
// the caller's because it is how the image is forked and freed later.
func TestSandboxCheckpointCreateSendsTheNameTheCallerChose(t *testing.T) {
	resetAPIFlags(t)
	outputFormat = "name"

	var path, body string
	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		body = buf.String()
		writeAPITestDocument(w, http.StatusCreated, v1.NewCheckpointDocument(v1.Checkpoint{Name: "before-upgrade"}))
	})
	apiHost, apiCA = endpoint, caFile

	out, err := executeSandboxCommand(t, newSandboxCheckpointCommand(), "create", "sb-1", "before-upgrade")
	require.NoError(t, err)
	assert.Equal(t, "POST /v1/sandboxes/sb-1/checkpoints", path)
	assert.JSONEq(t, `{"name":"before-upgrade"}`, body)
	assert.Equal(t, "before-upgrade\n", out)
}

// TestSandboxForkNamesTheCheckpointItLeftBehind pins the requirement that makes a fork
// freeable: the sandbox-scoped fork mints a checkpoint, and a caller that is not told its
// name cannot find it to delete it.
func TestSandboxForkNamesTheCheckpointItLeftBehind(t *testing.T) {
	resetAPIFlags(t)
	outputFormat = "json"

	var path string
	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		doc := v1.NewSandboxList([]v1.Sandbox{{ID: "sb-clone-1", State: "ready", GuestIP: "10.0.9.3"}})
		doc.Checkpoint = "sb-1-fork-1729"
		writeAPITestDocument(w, http.StatusCreated, doc)
	})
	apiHost, apiCA = endpoint, caFile

	out, err := executeSandboxCommand(t, newSandboxForkCommand(), "sb-1")
	require.NoError(t, err)
	assert.Equal(t, "POST /v1/sandboxes/sb-1/fork", path)
	assert.Contains(t, out, `"checkpoint": "sb-1-fork-1729"`)

	// The human rendering says how to find it again, not only that it exists.
	out, err = executeSandboxCommand(t, newSandboxForkCommand(), "sb-1")
	require.NoError(t, err)
	assert.Contains(t, out, "sb-1-fork-1729")

	outputFormat = ""
	endpoint, caFile = newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		doc := v1.NewSandboxList([]v1.Sandbox{{ID: "sb-clone-1", State: "ready"}})
		doc.Checkpoint = "sb-1-fork-1729"
		writeAPITestDocument(w, http.StatusCreated, doc)
	})
	apiHost, apiCA = endpoint, caFile
	out, err = executeSandboxCommand(t, newSandboxForkCommand(), "sb-1")
	require.NoError(t, err)
	assert.Contains(t, out, "checkpoint sb-1-fork-1729 holds the image these were forked from.")
	assert.Contains(t, out, "sandbox checkpoint rm sb-1-fork-1729")
}

// TestSandboxForkFromACheckpointUsesTheNodeScopedRoute pins the other half: forking a
// stored checkpoint does not need its sandbox any more, so it goes to the node-scoped route.
func TestSandboxForkFromACheckpointUsesTheNodeScopedRoute(t *testing.T) {
	resetAPIFlags(t)
	outputFormat = "name"

	var path string
	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		doc := v1.NewSandboxList([]v1.Sandbox{{ID: "sb-clone-1"}, {ID: "sb-clone-2"}})
		doc.Checkpoint = "c1"
		writeAPITestDocument(w, http.StatusCreated, doc)
	})
	apiHost, apiCA = endpoint, caFile

	out, err := executeSandboxCommand(t, newSandboxForkCommand(), "--checkpoint", "c1", "sb-1", "--count", "2", "--idle-ttl", "30m")
	require.NoError(t, err)
	assert.Equal(t, "POST /v1/checkpoints/c1/fork", path)

	// The navigation argument is the sandbox only when a fresh checkpoint is taken from it;
	// with --checkpoint it is named explicitly, and the clone ids are what came back.
	assert.Equal(t, "sb-clone-1\nsb-clone-2\n", out)
}

// TestSandboxForkRefusesANegativeTTLThatIsNotNever pins the same rule the API applies, in
// the command: -1 means never, other negatives are a mistake.
func TestSandboxForkRefusesANegativeTTLThatIsNotNever(t *testing.T) {
	resetAPIFlags(t)

	_, err := executeSandboxCommand(t, newSandboxForkCommand(), "sb-1", "--idle-ttl", "-2s")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-idle-ttl")
}

// executeSandboxCommand runs one sandbox command with args and returns its stdout. The
// command dials the API host the test set through the same flags a caller uses, so what is
// asserted is the request the CLI really makes.
func executeSandboxCommand(t *testing.T, cmd *cobra.Command, args ...string) (string, error) {
	t.Helper()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}
