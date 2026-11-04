package broker

// The node registry as the broker's source of path truth (SBX-012).
//
// Every test here answers the same question from the other side: when the node
// knows the VM a task runs, does the broker use that VM's paths — socket, vsock
// muxer, console and netns — instead of deriving them from the task ID? The
// derivation stays as the one-release fallback for a VM with no record, so the
// last tests pin that it still answers exactly as before.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// fakeInstanceSource is the node's registry as the resolver reads it. It answers
// one record and remembers what it was asked, so a test can prove the registry is
// consulted for a task the derivation would also answer.
type fakeInstanceSource struct {
	instance vminstance.Instance
	found    bool
	err      error
	asked    []string
}

func (f *fakeInstanceSource) Get(id string) (vminstance.Instance, bool, error) {
	f.asked = append(f.asked, id)
	return f.instance, f.found, f.err
}

// instanceRecord builds the record a node writes for a running VM. The API socket
// and the muxer sit in a per-VM directory rather than beside a task ID, which is
// what a pooled or sandboxed VM looks like and what the derivation can never
// produce.
func instanceRecord(t *testing.T, taskID string) vminstance.Instance {
	t.Helper()

	dir := filepath.Join(t.TempDir(), taskID)
	return vminstance.Instance{
		ID:        taskID,
		Kind:      vminstance.KindTask,
		PID:       4242,
		APISocket: filepath.Join(dir, "firecracker.sock"),
		VsockPath: filepath.Join(dir, "agent.vsock"),
		Console:   filepath.Join(dir, "console.sock"),
		NetNS:     filepath.Join(dir, "netns"),
		Booted:    time.Now().UTC(),
	}
}

// touchSockets creates the files the record names, so the record describes a VM
// whose API socket answers a stat.
func touchSockets(t *testing.T, instance vminstance.Instance) {
	t.Helper()

	for _, path := range []string{instance.APISocket, instance.VsockPath, instance.Console} {
		if path == "" {
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, nil, 0o600))
	}
}

// TestLookupPrefersTheInstanceRecord is the SBX-012 rule: the record answers, and
// it answers every path — socket, vsock muxer, console and netns — not just the
// socket. The task's ID is not a function of any of them.
func TestLookupPrefersTheInstanceRecord(t *testing.T) {
	socketDir := t.TempDir()
	logDir := t.TempDir()
	record := instanceRecord(t, "task-1")
	touchSockets(t, record)

	src := SocketDirSource{
		Dir:       socketDir,
		LogDir:    logDir,
		Instances: &fakeInstanceSource{instance: record, found: true},
	}

	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)

	assert.Equal(t, record.APISocket, task.SocketPath)
	assert.Equal(t, record.VsockPath, task.VsockPath())
	assert.Equal(t, record.Console, task.ConsolePath)
	assert.Equal(t, record.NetNS, task.NetNS)
	assert.Equal(t, filepath.Join(logDir, "task-1.log"), task.LogPath)
	assert.True(t, task.Running)
}

// TestLookupInstanceRecordWinsOverTheDerivedSocket pins the precedence when both
// exist: a socket named after the task ID is exactly the guess the registry
// replaces, so the record's path is the one the broker dials.
func TestLookupInstanceRecordWinsOverTheDerivedSocket(t *testing.T) {
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(scruntime.SocketPath(socketDir, "task-1"), nil, 0o600))

	record := instanceRecord(t, "task-1")
	touchSockets(t, record)

	src := SocketDirSource{Dir: socketDir, Instances: &fakeInstanceSource{instance: record, found: true}}

	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, record.APISocket, task.SocketPath)
}

// TestLookupInstanceRecordWithAGoneSocketIsNotRunning pins the honest answer for
// a record whose VM has exited: a log-only task, not a fallback to the derived
// path. The derived socket may belong to nothing the caller asked about.
func TestLookupInstanceRecordWithAGoneSocketIsNotRunning(t *testing.T) {
	socketDir := t.TempDir()
	logDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "task-1.log"), []byte("boom\n"), 0o600))

	// The derived socket exists and is alive-looking; the record must still win,
	// so the answer is "not running" rather than that other socket.
	require.NoError(t, os.WriteFile(scruntime.SocketPath(socketDir, "task-1"), nil, 0o600))

	src := SocketDirSource{
		Dir:       socketDir,
		LogDir:    logDir,
		Instances: &fakeInstanceSource{instance: instanceRecord(t, "task-1"), found: true},
	}

	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, Task{ID: "task-1", LogPath: filepath.Join(logDir, "task-1.log")}, task)
}

// TestLookupInstanceRecordKeepsTheRestoredProvenance pins the migration half: the
// registry owns the paths, and the restored-instance record still says which VM
// the task inherited and which disk its state file names. An export that lost
// that would name a disk the artifact does not carry.
func TestLookupInstanceRecordKeepsTheRestoredProvenance(t *testing.T) {
	socketDir := t.TempDir()
	record := instanceRecord(t, "task-1")
	touchSockets(t, record)

	restored := restoredRecord(t, record.APISocket, "task-1", "source-task")
	src := SocketDirSource{
		Dir:       socketDir,
		Instances: &fakeInstanceSource{instance: record, found: true},
		Restored:  &fakeRestoredSource{record: restored, found: true},
	}

	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)

	assert.Equal(t, record.APISocket, task.SocketPath)
	assert.Equal(t, record.VsockPath, task.VsockPath(), "the registry's muxer, not the record's")

	vmID, _, diskPath := task.sourceVMIdentity("/var/lib/sc/rootfs")
	assert.Equal(t, "source-task", vmID)
	assert.Equal(t, restored.DiskPath, diskPath)
}

// TestLookupUnreadableInstanceRecordIsAnError pins the failure shape: a record the
// node holds but cannot use names no VM the broker may guess at, so the lookup
// reports it instead of falling through to a path derived from the task ID.
func TestLookupUnreadableInstanceRecordIsAnError(t *testing.T) {
	src := SocketDirSource{
		Dir:       t.TempDir(),
		Instances: &fakeInstanceSource{err: errors.New("record is corrupt")},
	}

	_, ok, err := src.Lookup(context.Background(), "task-1")
	require.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "instance record")
	assert.Contains(t, err.Error(), "record is corrupt")
}

// TestLookupWarnsOnceForAStaleInstanceRecord pins the latch: a record whose VM is
// gone is reported once, however many requests resolve it.
func TestLookupWarnsOnceForAStaleInstanceRecord(t *testing.T) {
	warnings := captureWarnings(t)
	src := NewSocketDirSource(t.TempDir(), "",
		nil, &fakeInstanceSource{instance: instanceRecord(t, "task-1"), found: true}, nil)

	for range 3 {
		task, ok, err := src.Lookup(context.Background(), "task-1")
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, task.Running)
	}

	require.Equal(t, 1, len(logLines(warnings)),
		"three lookups of one stale record warn once: %s", warnings.String())
	assert.Contains(t, warnings.String(), `"task_id":"task-1"`)
}

// TestLookupFallsBackToTheDerivationWithoutARecord pins the one-release fallback:
// the registry is wired and holds nothing for the task, so the broker resolves the
// socket named after the task ID exactly as it did before the registry existed.
func TestLookupFallsBackToTheDerivationWithoutARecord(t *testing.T) {
	socketDir := t.TempDir()
	socketPath := scruntime.SocketPath(socketDir, "legacy-task")
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))

	registry := &fakeInstanceSource{}
	src := SocketDirSource{Dir: socketDir, Instances: registry}

	task, ok, err := src.Lookup(context.Background(), "legacy-task")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, socketPath, task.SocketPath)
	assert.Equal(t, scruntime.VsockPath(socketDir, "legacy-task"), task.VsockPath())
	assert.Equal(t, []string{"legacy-task"}, registry.asked, "the registry is asked first, once")
}

// TestLookupFallsBackToTheRestoredRecordWithoutAnInstanceRecord pins the second
// legacy source: a migrated task whose VM predates the registry still resolves
// from its restored-instance record, muxer included.
func TestLookupFallsBackToTheRestoredRecordWithoutAnInstanceRecord(t *testing.T) {
	socketDir := t.TempDir()
	record := restoredRecord(t, scruntime.SocketPath(socketDir, "new-task"), "new-task", "source-task")
	touchSockets(t, vminstance.Instance{APISocket: record.SocketPath, VsockPath: record.VsockPath})

	src := SocketDirSource{
		Dir:       socketDir,
		Instances: &fakeInstanceSource{},
		Restored:  &fakeRestoredSource{record: record, found: true},
	}

	task, ok, err := src.Lookup(context.Background(), "new-task")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, record.SocketPath, task.SocketPath)
	assert.Equal(t, record.VsockPath, task.VsockPath())
	assert.NotEqual(t, scruntime.VsockPath(socketDir, "new-task"), task.VsockPath(),
		"a migrated VM answers on the source task's muxer")
}

// TestLookupResolvesARealInstanceStore runs the production implementation: a
// record written through vminstance.Store is what the broker then resolves. It is
// the contract test the fake cannot give — the store's validation and the
// resolver's reading have to agree about field names and shapes.
func TestLookupResolvesARealInstanceStore(t *testing.T) {
	stateDir := t.TempDir()
	socketDir := t.TempDir()

	store, err := vminstance.NewStore(vminstance.Config{
		Dir:   vminstance.Dir(stateDir),
		Alive: func(int) bool { return true },
	})
	require.NoError(t, err)

	record := instanceRecord(t, "task-1")
	touchSockets(t, record)
	require.NoError(t, store.Put(record))

	src := SocketDirSource{Dir: socketDir, Instances: store}
	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)

	assert.Equal(t, record.APISocket, task.SocketPath)
	assert.Equal(t, record.VsockPath, task.VsockPath())
	assert.Equal(t, record.Console, task.ConsolePath)
	assert.Equal(t, record.NetNS, task.NetNS)
}

// TestLookupInstanceRecordWithoutAMuxerUsesTheMigrationRecord pins the fallback
// the export needs: a registry record can name no muxer (a VM whose agent had not
// started, or a record written before the field), and a migrated task's source
// muxer then comes from the migration record. Without it the source's device
// directory would be derived from an empty path.
func TestLookupInstanceRecordWithoutAMuxerUsesTheMigrationRecord(t *testing.T) {
	socketDir := t.TempDir()
	record := instanceRecord(t, "task-1")
	record.VsockPath = ""
	touchSockets(t, record)

	restored := restoredRecord(t, record.APISocket, "task-1", "source-task")
	src := SocketDirSource{
		Dir:       socketDir,
		Instances: &fakeInstanceSource{instance: record, found: true},
		Restored:  &fakeRestoredSource{record: restored, found: true},
	}

	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)

	assert.Equal(t, restored.VsockPath, task.VsockPath())
	_, vmSocketDir, _ := task.sourceVMIdentity("/var/lib/sc/rootfs")
	assert.Equal(t, filepath.Dir(restored.VsockPath), vmSocketDir)
}

// TestLookupAnswersFromTheRegistryWhenNoDerivedSocketExists pins what SBX-011
// buys the broker: the record alone is enough. The socket directory holds nothing
// named after the task, so the answer can only have come from the registry — and
// the debug line says so, which is what an operator checks on a node where both
// paths could have answered.
func TestLookupAnswersFromTheRegistryWhenNoDerivedSocketExists(t *testing.T) {
	logs := captureWarnings(t)

	socketDir := t.TempDir()
	record := instanceRecord(t, "task-1")
	touchSockets(t, record)

	src := SocketDirSource{Dir: socketDir, Instances: &fakeInstanceSource{instance: record, found: true}}
	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)

	assert.Equal(t, record.APISocket, task.SocketPath)
	_, statErr := os.Stat(scruntime.SocketPath(socketDir, "task-1"))
	require.ErrorIs(t, statErr, os.ErrNotExist, "the filename derivation has nothing to answer with")
	assert.Contains(t, logs.String(), "Resolved the task from the instance registry")
}
