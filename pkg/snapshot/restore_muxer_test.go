package snapshot

// The vsock muxer a crashed VMM leaves behind.
//
// A snapshot records the VM's vsock device with its host-side path
// (<socketDir>/<taskID>.vsock). A VMM that died outside a stop path leaves that
// file on disk, and Firecracker cannot bind a path that already exists — the
// restore fails with EADDRINUSE before the guest ever resumes (#159). The restore
// path owns that cleanup, not its callers, so the CLI's restore and the node
// broker's cannot disagree about it.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// restoreFixture writes a minimal, complete snapshot and returns its info. The
// snapshot records no disk, so the restore reaches the socket cleanup without a
// copy: the two files exist and the state checksum matches.
func restoreFixture(t *testing.T, taskID string) *SnapshotInfo {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "snapshots", "snap-0123456789abcdef")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	statePath := filepath.Join(dir, "vm.state")
	memoryPath := filepath.Join(dir, "vm.mem")
	require.NoError(t, os.WriteFile(statePath, []byte("state"), 0o600))
	require.NoError(t, os.WriteFile(memoryPath, []byte("memory"), 0o600))

	checksum, err := sha256File(statePath)
	require.NoError(t, err)

	return &SnapshotInfo{
		ID:         "snap-0123456789abcdef",
		TaskID:     taskID,
		StatePath:  statePath,
		MemoryPath: memoryPath,
		Checksum:   checksum,
	}
}

// TestRestoreFromSnapshotClearsAStaleVsockMuxer pins the #159 fix at the seam both
// callers share: the file a crashed VMM left at <socketDir>/<taskID>.vsock is gone
// by the time Firecracker is looked up, so the restored VMM can bind its own.
// Hiding the binary proves the order — the cleanup ran, no process was started.
func TestRestoreFromSnapshotClearsAStaleVsockMuxer(t *testing.T) {
	info := restoreFixture(t, "task-1")
	socketDir := t.TempDir()

	muxerPath := runtime.VsockPath(socketDir, info.TaskID)
	require.NoError(t, os.WriteFile(muxerPath, nil, 0o600))

	// A stale API socket too: the restore unlinks both paths it is about to bind.
	apiSocket := runtime.SocketPath(socketDir, info.TaskID)
	require.NoError(t, os.WriteFile(apiSocket, nil, 0o600))

	mgr, err := NewManager(SnapshotConfig{SnapshotDir: filepath.Dir(filepath.Dir(info.StatePath))})
	require.NoError(t, err)

	t.Setenv("PATH", t.TempDir())

	err = mgr.RestoreFromSnapshot(context.Background(), info, apiSocket)
	require.Error(t, err)
	require.Contains(t, err.Error(), "firecracker binary not found",
		"the cleanup step runs before the VMM is started")

	_, statErr := os.Stat(muxerPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist,
		"a stale muxer would make the restored Firecracker fail with EADDRINUSE")
	_, statErr = os.Stat(apiSocket)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "the stale API socket is cleared too")
}

// TestRestoreFromSnapshotReportsAMuxerItCannotClear pins the other half: a path
// that exists and cannot be removed is reported before any process starts, rather
// than left for Firecracker to fail on with an error that names no cause.
func TestRestoreFromSnapshotReportsAMuxerItCannotClear(t *testing.T) {
	info := restoreFixture(t, "task-1")
	socketDir := t.TempDir()

	// A non-empty directory where the muxer socket belongs: os.Remove fails with
	// ENOTEMPTY, which is the shape of a path an operator has to look at.
	muxerPath := runtime.VsockPath(socketDir, info.TaskID)
	require.NoError(t, os.MkdirAll(muxerPath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(muxerPath, "occupant"), nil, 0o600))

	mgr, err := NewManager(SnapshotConfig{SnapshotDir: filepath.Dir(filepath.Dir(info.StatePath))})
	require.NoError(t, err)

	// PATH is real here: the failure must come from the cleanup, not from a
	// missing binary.
	err = mgr.RestoreFromSnapshot(context.Background(), info, runtime.SocketPath(socketDir, info.TaskID))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clear the stale vsock muxer")
	assert.Contains(t, err.Error(), muxerPath)
}
