package broker

// The route a restore used to break, driven through the broker itself.
//
// SBX-001 proved the failure end to end on a real node: restore started the VM at
// <task>-restored.sock, so `vm exec <task>` and the broker's exec route with the
// task ID found nothing at <task>.sock (404), and the route with the restored ID
// derived <task>-restored.vsock, which no process ever bound (503).
//
// A restore now starts the task's VM at the task's own socket. These tests hold
// that to the two callers that mattered: the resolver, which answers the exec
// route by task ID, and the exec handler, which dials whatever muxer the resolver
// names.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

// TestRestoredTaskIsExecutableByItsTaskID is the end-to-end shape of the bug
// inside one daemon: a restore through the node's snapshots route, then the exec
// route for the same task ID. The restore's socket is the socket the resolver
// looks for, and the muxer the handler dials is that task's own.
func TestRestoredTaskIsExecutableByItsTaskID(t *testing.T) {
	socketDir := shortSocketDir(t)
	logDir := t.TempDir()

	info := testSnapshotInfo("snap-1", "task-1", time.Now().UTC())
	store := &fakeSnapshotStore{list: []*snapshot.SnapshotInfo{info}}
	// What pkg/snapshot leaves behind: Firecracker's API socket at the path the
	// restore was given.
	store.onRestore = func(socketPath string) {
		require.NoError(t, os.WriteFile(socketPath, nil, 0o600))
	}

	logOnly := Task{ID: "task-1", LogPath: filepath.Join(logDir, "task-1.log")}
	rec := serveSnapshotWithConfig(t, SnapshotHandlerConfig{Store: store, SocketDir: socketDir},
		logOnly, http.MethodPost, "/v1/tasks/task-1/snapshots?restore=snap-1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, filepath.Join(socketDir, "task-1.sock"), store.gotRestoreSock)

	// The exec route resolves the task ID the manager was asked about. Nothing
	// here knows about a restored ID, which is the point.
	task, ok, err := SocketDirSource{Dir: socketDir, LogDir: logDir}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, task.Running, "the VM the restore started answers at the task's socket")

	dial := &fakeDialer{guest: &fakeGuest{result: agent.ExecResult{ExitCode: 0}}}
	exec := NewExecHandler(dial.dial)
	execRec := httptest.NewRecorder()
	exec(context.Background(), task, execRec, execRequest(t, `{"command":["true"]}`))

	require.Equal(t, http.StatusOK, execRec.Code, execRec.Body.String())
	require.Equal(t, filepath.Join(socketDir, "task-1.vsock"), dial.gotPath,
		"the restored VM answers on the task's own muxer, not on a second identity's")
}

// TestRestoreOfTwoTasksKeepsTheirSocketsApart pins the collision a suffixed
// identity allowed: task "abc" restored to "abc-restored.sock" would have taken
// the socket of a task actually called "abc-restored". One socket per task ID has
// no such shape.
func TestRestoreOfTwoTasksKeepsTheirSocketsApart(t *testing.T) {
	socketDir := shortSocketDir(t)

	cases := []struct{ taskID, snapshotID string }{
		{"abc", "snap-0000000000000001"},
		// The ID a suffixed restore would have collided with: restoring "abc"
		// used to take "abc-restored.sock", which is this task's own socket.
		{"abc-restored", "snap-0000000000000002"},
	}

	restored := make(map[string]string, len(cases))
	for _, tc := range cases {
		info := testSnapshotInfo(tc.snapshotID, tc.taskID, time.Now().UTC())
		store := &fakeSnapshotStore{list: []*snapshot.SnapshotInfo{info}}

		rec := serveSnapshotWithConfig(t, SnapshotHandlerConfig{Store: store, SocketDir: socketDir},
			Task{ID: tc.taskID, LogPath: "/var/run/swarmcracker/" + tc.taskID + ".log"},
			http.MethodPost, "/v1/tasks/"+tc.taskID+"/snapshots?restore="+tc.snapshotID, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		for _, previous := range restored {
			require.NotEqual(t, previous, store.gotRestoreSock,
				"two tasks must never restore onto one API socket")
		}
		restored[tc.taskID] = store.gotRestoreSock
	}
}
