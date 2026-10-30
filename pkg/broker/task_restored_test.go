package broker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// fakeRestoredSource is the node's restored-instance store as the resolver reads
// it. It answers one record and remembers what it was asked, so a test can prove
// the store is consulted (and not consulted for an ID that never reaches it).
type fakeRestoredSource struct {
	record    scruntime.RestoredInstance
	found     bool
	err       error
	deleteErr error
	asked     []string
	deletes   []string
}

func (f *fakeRestoredSource) Get(taskID string) (scruntime.RestoredInstance, bool, error) {
	f.asked = append(f.asked, taskID)
	return f.record, f.found, f.err
}

// Delete is the other half of the store the node's stop paths use: the record goes
// once the VM it names is confirmed gone.
func (f *fakeRestoredSource) Delete(taskID string) error {
	f.deletes = append(f.deletes, taskID)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.found = false
	return nil
}

// restoredRecord builds the record a node writes for a task it started from a
// migration artifact: the API socket belongs to the new task, while the vsock
// muxer, the TAP device and the disk belong to the source task whose VM the new
// one inherited.
func restoredRecord(t *testing.T, socketPath, taskID, sourceID string) scruntime.RestoredInstance {
	t.Helper()

	return scruntime.RestoredInstance{
		TaskID:     taskID,
		VMID:       sourceID,
		ArtifactID: "mig-0123456789abcdef",
		SocketPath: socketPath,
		TapName:    scruntime.TapName(sourceID, 0),
		VsockPath:  scruntime.VsockPath(filepath.Dir(socketPath), sourceID),
		DiskPath:   filepath.Join("/var/lib/swarmcracker/tasks", sourceID+".ext4"),
		CreatedAt:  time.Now().UTC(),
	}
}

// captureWarnings redirects the package logger for one test and returns the
// buffer. The resolver logs the one warning a stale record deserves; nothing else
// in this package writes to the log, so every line here is that warning.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = previous })
	return &buf
}

// TestLookupPrefersTheRestoredRecord pins the precedence: a task with a record
// resolves to the VM that record names, not to the path derived from its ID. The
// record's socket lives in another directory here, so a resolver that derived
// <socketDir>/<id>.sock would report a different file.
func TestLookupPrefersTheRestoredRecord(t *testing.T) {
	socketDir := t.TempDir()
	logDir := t.TempDir()
	vmDir := t.TempDir()

	// The derived path exists and is live on its own: an implementation that
	// kept today's derivation would still look successful.
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))
	recordSocket := filepath.Join(vmDir, "task-1.sock")
	require.NoError(t, os.WriteFile(recordSocket, nil, 0o600))

	record := restoredRecord(t, recordSocket, "task-1", "source-task")
	src := SocketDirSource{
		Dir:       socketDir,
		LogDir:    logDir,
		IsRunning: func(string) bool { return true },
		Restored:  &fakeRestoredSource{record: record, found: true},
	}

	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)

	require.Equal(t, recordSocket, task.SocketPath,
		"the record's socket wins over <socketDir>/<taskID>.sock")
	require.True(t, task.Running)
	require.Equal(t, record.VsockPath, task.VsockPath(),
		"a restored VM answers on the source task's muxer, not on <id>.vsock")
	require.NotEqual(t, scruntime.VsockPath(socketDir, "task-1"), task.VsockPath(),
		"the derived muxer belongs to no process on a restored task")
	require.Equal(t, filepath.Join(socketDir, "task-1.console.sock"), task.ConsolePath,
		"the console socket stays per new task")
	require.Equal(t, filepath.Join(logDir, "task-1.log"), task.LogPath,
		"the console log stays per new task")
	require.True(t, task.guestAvailable())
	require.True(t, task.consoleAvailable())
}

// TestLookupRestoredTaskWithoutItsSocketIsNotRunning pins the stale record: the
// record says which VM the task runs, and that VM's socket is gone. The answer is
// "not running" — never a fall back to <socketDir>/<taskID>.sock, which after an
// ID reuse addresses a different task's VM.
func TestLookupRestoredTaskWithoutItsSocketIsNotRunning(t *testing.T) {
	cases := []struct {
		name    string
		withLog bool
	}{
		{name: "the console log is still there", withLog: true},
		{name: "no console log either", withLog: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings := captureWarnings(t)

			socketDir := t.TempDir()
			logDir := t.TempDir()
			vmDir := t.TempDir()

			// A live socket at the derived path is exactly what must not be used.
			require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))
			logPath := filepath.Join(logDir, "task-1.log")
			if tc.withLog {
				require.NoError(t, os.WriteFile(logPath, []byte("boom\n"), 0o600))
			}

			record := restoredRecord(t, filepath.Join(vmDir, "task-1.sock"), "task-1", "source-task")
			src := SocketDirSource{
				Dir:       socketDir,
				LogDir:    logDir,
				IsRunning: func(string) bool { return true },
				Restored:  &fakeRestoredSource{record: record, found: true},
			}

			task, ok, err := src.Lookup(context.Background(), "task-1")
			require.NoError(t, err)
			require.True(t, ok, "the record is evidence the task exists on this node")

			require.False(t, task.Running)
			require.Empty(t, task.SocketPath, "a record whose socket is gone must not fall back")
			require.Empty(t, task.ConsolePath)
			require.Empty(t, task.VsockPath())
			require.False(t, task.guestAvailable())
			require.False(t, task.consoleAvailable())
			require.Equal(t, logPath, task.LogPath,
				"the console log of the new task stays readable after its VM exits")

			lines := logLines(warnings)
			require.Len(t, lines, 1, "a stale record is reported once per resolution: %s", warnings.String())
			require.Contains(t, lines[0], "task-1")
			require.Contains(t, lines[0], `"level":"warn"`)
		})
	}
}

// TestLookupRestoredTaskWithAStaleSocketIsNotRunning pins the other half of
// liveness: the socket file exists but no process owns it (a crashed VMM leaves
// its socket behind). The record makes that socket the one to probe, and a dead
// probe is "not running" — the probe is never re-run against the derived path.
func TestLookupRestoredTaskWithAStaleSocketIsNotRunning(t *testing.T) {
	warnings := captureWarnings(t)

	socketDir := t.TempDir()
	logDir := t.TempDir()
	vmDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))
	recordSocket := filepath.Join(vmDir, "task-1.sock")
	require.NoError(t, os.WriteFile(recordSocket, nil, 0o600))

	record := restoredRecord(t, recordSocket, "task-1", "source-task")
	src := SocketDirSource{
		Dir:       socketDir,
		LogDir:    logDir,
		IsRunning: func(string) bool { return false },
		Restored:  &fakeRestoredSource{record: record, found: true},
	}

	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, task.Running, "a socket no process owns is not a running VM")
	require.Empty(t, task.SocketPath)
	require.Empty(t, task.VsockPath())
	require.Len(t, logLines(warnings), 1)
}

// TestLookupUnreadableRestoredRecordIsAnError pins that a record the store cannot
// use is a resolution failure, not a silent fallback: the node knows a record
// exists and cannot say which VM it names, so guessing would address a VM that may
// belong to another task.
func TestLookupUnreadableRestoredRecordIsAnError(t *testing.T) {
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	store := &fakeRestoredSource{err: errors.New("restored-instance record is corrupt")}
	src := SocketDirSource{Dir: socketDir, Restored: store}

	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.Error(t, err)
	require.ErrorContains(t, err, "task-1")
	require.ErrorContains(t, err, "restored")
	require.False(t, ok)
	require.Zero(t, task, "an unreadable record must not resolve a partial task")
	require.Equal(t, []string{"task-1"}, store.asked)
}

// TestLookupRestoredTaskWithASocketDirectoryIsNotRunning pins the last shape a
// "socket" can have: a directory named like one. No VMM creates that, so the record
// is stale and the task is not running — the resolver never dials a path it cannot
// use.
func TestLookupRestoredTaskWithASocketDirectoryIsNotRunning(t *testing.T) {
	captureWarnings(t)

	socketDir := t.TempDir()
	logDir := t.TempDir()
	vmDir := t.TempDir()

	recordSocket := filepath.Join(vmDir, "task-1.sock")
	require.NoError(t, os.Mkdir(recordSocket, 0o700))
	record := restoredRecord(t, recordSocket, "task-1", "source-task")

	task, ok, err := SocketDirSource{
		Dir:      socketDir,
		LogDir:   logDir,
		Restored: &fakeRestoredSource{record: record, found: true},
	}.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, task.Running)
	require.Empty(t, task.SocketPath)
	require.Equal(t, filepath.Join(logDir, "task-1.log"), task.LogPath)
}

// TestLookupUnreadableRestoredSocketIsAnError pins the difference between "the
// socket is gone" and "the socket cannot be looked at": only the first is a
// not-running answer. A stat the resolver cannot complete says nothing about the
// VM, so it is reported rather than guessed at.
func TestLookupUnreadableRestoredSocketIsAnError(t *testing.T) {
	socketDir := t.TempDir()

	// A file name past the filesystem's limit makes stat fail with ENAMETOOLONG,
	// which is neither "found" nor "not exist" — and unlike a permission error it
	// fails the same way as root, so the branch is covered in a container too.
	unusable := filepath.Join(t.TempDir(), strings.Repeat("a", 300)+".sock")
	record := restoredRecord(t, unusable, "task-1", "source-task")

	src := SocketDirSource{
		Dir:      socketDir,
		Restored: &fakeRestoredSource{record: record, found: true},
	}

	task, ok, err := src.Lookup(context.Background(), "task-1")
	require.Error(t, err)
	require.ErrorContains(t, err, "stat the restored VM socket")
	require.False(t, ok)
	require.Zero(t, task)
}

// TestLookupWithoutARecordIsUnchanged proves the resolver's contract for every
// task this node did not restore: with the store wired and empty, the answer is
// the same task, the same found flag and the same error as with no store at all.
func TestLookupWithoutARecordIsUnchanged(t *testing.T) {
	type fixture struct {
		name    string
		id      string
		setup   func(t *testing.T, socketDir, logDir string)
		probe   func(string) bool
		consult bool // whether the store is reached for this ID
	}

	cases := []fixture{
		{
			name: "a running task",
			id:   "task-1",
			setup: func(t *testing.T, socketDir, _ string) {
				require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))
			},
			consult: true,
		},
		{
			name: "a running task behind a live probe",
			id:   "task-1",
			setup: func(t *testing.T, socketDir, _ string) {
				require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))
			},
			probe:   func(string) bool { return true },
			consult: true,
		},
		{
			name: "a stale socket with a console log",
			id:   "task-1",
			setup: func(t *testing.T, socketDir, logDir string) {
				require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(logDir, "task-1.log"), []byte("boom\n"), 0o600))
			},
			probe:   func(string) bool { return false },
			consult: true,
		},
		{
			name: "an exited task with a console log",
			id:   "task-1",
			setup: func(t *testing.T, _, logDir string) {
				require.NoError(t, os.WriteFile(filepath.Join(logDir, "task-1.log"), []byte("boom\n"), 0o600))
			},
			consult: true,
		},
		{
			name: "a socket that is a directory",
			id:   "task-1",
			setup: func(t *testing.T, socketDir, _ string) {
				require.NoError(t, os.Mkdir(filepath.Join(socketDir, "task-1.sock"), 0o700))
			},
			consult: true,
		},
		{
			name: "a log that is a directory",
			id:   "task-1",
			setup: func(t *testing.T, _, logDir string) {
				require.NoError(t, os.Mkdir(filepath.Join(logDir, "task-1.log"), 0o700))
			},
			consult: true,
		},
		{
			name:    "a missing task",
			id:      "task-1",
			consult: true,
		},
		{
			name: "an ID with a separator",
			id:   "../task-1",
		},
		{
			name: "an empty ID",
			id:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socketDir := t.TempDir()
			logDir := t.TempDir()
			if tc.setup != nil {
				tc.setup(t, socketDir, logDir)
			}

			plainTask, plainFound, plainErr := SocketDirSource{
				Dir: socketDir, LogDir: logDir, IsRunning: tc.probe,
			}.Lookup(context.Background(), tc.id)

			store := &fakeRestoredSource{}
			wiredTask, wiredFound, wiredErr := SocketDirSource{
				Dir: socketDir, LogDir: logDir, IsRunning: tc.probe, Restored: store,
			}.Lookup(context.Background(), tc.id)

			require.Equal(t, plainFound, wiredFound)
			require.Equal(t, plainTask, wiredTask)
			if plainErr == nil {
				require.NoError(t, wiredErr)
			} else {
				require.EqualError(t, wiredErr, plainErr.Error())
			}

			if tc.consult {
				require.Equal(t, []string{tc.id}, store.asked,
					"a task without a record still asks the store, and resolves as before")
			} else {
				require.Empty(t, store.asked,
					"an untrusted ID is rejected before the store is touched")
			}
		})
	}
}

// TestLookupRestoredTaskKeepsTheLogPathPerTask pins the split the record does not
// change: the VM keeps the source task's vsock muxer, but the console log and the
// console socket are the new task's, because the executor's Firecracker process
// writes them under the ID SwarmKit created.
func TestLookupRestoredTaskKeepsTheLogPathPerTask(t *testing.T) {
	socketDir := t.TempDir()
	logDir := t.TempDir()
	vmDir := t.TempDir()

	recordSocket := filepath.Join(vmDir, "new-task.sock")
	require.NoError(t, os.WriteFile(recordSocket, nil, 0o600))
	record := restoredRecord(t, recordSocket, "new-task", "source-task")

	task, ok, err := SocketDirSource{
		Dir:      socketDir,
		LogDir:   logDir,
		Restored: &fakeRestoredSource{record: record, found: true},
	}.Lookup(context.Background(), "new-task")
	require.NoError(t, err)
	require.True(t, ok)

	require.Equal(t, filepath.Join(logDir, "new-task.log"), task.LogPath)
	require.Equal(t, filepath.Join(socketDir, "new-task.console.sock"), task.ConsolePath)
	require.Equal(t, scruntime.VsockPath(vmDir, "source-task"), task.VsockPath())
	require.Contains(t, task.VsockPath(), "source-task")
}

// TestLookupWarnsOncePerStaleRecord pins the latch: a record whose VM is gone is
// reported to the operator once, not on every exec, logs and health request that
// resolves the task. The daemon's source is the one that keeps the latch; a
// literal source has nowhere to hold it, which is why the constructor exists.
func TestLookupWarnsOncePerStaleRecord(t *testing.T) {
	warnings := captureWarnings(t)

	socketDir := t.TempDir()
	vmDir := t.TempDir()
	record := restoredRecord(t, filepath.Join(vmDir, "task-1.sock"), "task-1", "source-task")
	src := NewSocketDirSource(socketDir, "", nil, &fakeRestoredSource{record: record, found: true})

	for range 3 {
		task, ok, err := src.Lookup(context.Background(), "task-1")
		require.NoError(t, err)
		require.True(t, ok)
		require.False(t, task.Running, "the record's VM is gone")
	}

	require.Equal(t, 1, len(logLines(warnings)),
		"three lookups of one stale record warn once: %s", warnings.String())
	require.Contains(t, warnings.String(), `"level":"warn"`)
	require.Contains(t, warnings.String(), `"task_id":"task-1"`)
}

// TestLookupWarnsAgainForANewRecord pins the latch's key: it is the record, not
// the task. A task that is restored again — a retried import writes a new record
// naming a new socket — is a new fact and deserves its own warning.
func TestLookupWarnsAgainForANewRecord(t *testing.T) {
	warnings := captureWarnings(t)

	socketDir := t.TempDir()
	vmDir := t.TempDir()
	store := &fakeRestoredSource{found: true}
	store.record = restoredRecord(t, filepath.Join(vmDir, "task-1.sock"), "task-1", "source-task")
	src := NewSocketDirSource(socketDir, "", nil, store)

	_, _, err := src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.Equal(t, 1, len(logLines(warnings)))

	// The retried import wrote a record naming a different socket.
	store.record = restoredRecord(t, filepath.Join(vmDir, "task-1-retry.sock"), "task-1", "source-task")
	_, _, err = src.Lookup(context.Background(), "task-1")
	require.NoError(t, err)
	require.Equal(t, 2, len(logLines(warnings)),
		"a record naming a new socket is a new fact: %s", warnings.String())
}

// TestLookupRestoredTaskNamesTheVMsOwnDevice pins what an export of a restored
// task needs: the identity of the VM, not of the task that inherited it. The disk
// is the one the VM's state file records — <RootfsDir>/tasks/<newTaskID>.ext4
// belongs to no VM — and the ID is the source task's, whose hash names the TAP
// device and whose socket directory names the vsock muxer.
func TestLookupRestoredTaskNamesTheVMsOwnDevice(t *testing.T) {
	socketDir := t.TempDir()
	vmDir := t.TempDir()

	recordSocket := filepath.Join(vmDir, "new-task.sock")
	require.NoError(t, os.WriteFile(recordSocket, nil, 0o600))
	record := restoredRecord(t, recordSocket, "new-task", "source-task")

	task, ok, err := SocketDirSource{
		Dir:      socketDir,
		Restored: &fakeRestoredSource{record: record, found: true},
	}.Lookup(context.Background(), "new-task")
	require.NoError(t, err)
	require.True(t, ok)

	vmID, vsockDir, diskPath := task.sourceVMIdentity("/var/lib/firecracker/rootfs")
	require.Equal(t, "source-task", vmID)
	require.Equal(t, vmDir, vsockDir,
		"the muxer is derived in the directory the record's path is in")
	require.Equal(t, record.DiskPath, diskPath,
		"the disk to copy is the one the restored VM's state file names")
	require.NotEqual(t, filepath.Join("/var/lib/firecracker/rootfs", "tasks", "new-task.ext4"), diskPath,
		"the new task's per-task disk belongs to no VM on a restored task")
}

// TestSourceVMIdentityOfAPlainTaskIsTheDerivation pins the unchanged half: a task
// this node booted is its own VM, so an export names it and its per-task disk.
func TestSourceVMIdentityOfAPlainTaskIsTheDerivation(t *testing.T) {
	task := Task{ID: "task-1", SocketPath: "/var/run/firecracker/task-1.sock", Running: true}

	vmID, vsockDir, diskPath := task.sourceVMIdentity("/var/lib/firecracker/rootfs")
	require.Equal(t, "task-1", vmID)
	require.Equal(t, "/var/run/firecracker", vsockDir)
	require.Equal(t, "/var/lib/firecracker/rootfs/tasks/task-1.ext4", diskPath)
}

// logLines returns the non-empty lines a captured logger wrote.
func logLines(buf *bytes.Buffer) []string {
	var lines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
