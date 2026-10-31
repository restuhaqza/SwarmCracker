package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordRestoredVM writes the record a node writes when it restores a migrated
// task: the API socket belongs to the new task, the vsock muxer and TAP device to
// the source task whose VM the snapshot carried.
func recordRestoredVM(t *testing.T, stateDir, socketDir, taskID, sourceID string) scruntime.RestoredInstance {
	t.Helper()

	record := scruntime.RestoredInstance{
		TaskID:     taskID,
		VMID:       sourceID,
		ArtifactID: "mig-0123456789abcdef",
		SocketPath: filepath.Join(socketDir, taskID+".sock"),
		TapName:    scruntime.TapName(sourceID, 0),
		VsockPath:  scruntime.VsockPath(socketDir, sourceID),
		DiskPath:   filepath.Join("/var/lib/swarmcracker/tasks", sourceID+".ext4"),
		CreatedAt:  time.Now().UTC(),
	}

	reg, err := scruntime.NewRestoredRegistry(scruntime.RestoredInstanceDir(stateDir))
	require.NoError(t, err)
	require.NoError(t, reg.Put(record))
	return record
}

// TestLocalVsockPathPrefersTheRecord is the decision the two commands share,
// asserted directly: the record wins when it exists, and the derivation is
// untouched when it does not.
func TestLocalVsockPathPrefersTheRecord(t *testing.T) {
	socketDir := t.TempDir()

	t.Run("recorded muxer", func(t *testing.T) {
		stateDir := t.TempDir()
		record := recordRestoredVM(t, stateDir, socketDir, "new-task", "source-task")

		got := localVsockPath(socketDir, stateDir, record.TaskID)
		assert.Equal(t, record.VsockPath, got)
		assert.NotEqual(t, scruntime.VsockPath(socketDir, record.TaskID), got,
			"a restored VM answers on the source task's muxer")
	})

	t.Run("derived muxer without a record", func(t *testing.T) {
		// A state directory that exists and holds no record for this task: the
		// empty state dir case must behave the same, but a populated one proves
		// the lookup is by task and not by "any record".
		stateDir := t.TempDir()
		recordRestoredVM(t, stateDir, socketDir, "other-task", "source-task")

		assert.Equal(t, scruntime.VsockPath(socketDir, "new-task"),
			localVsockPath(socketDir, stateDir, "new-task"))
	})

	t.Run("derived muxer without a state dir", func(t *testing.T) {
		assert.Equal(t, scruntime.VsockPath(socketDir, "new-task"),
			localVsockPath(socketDir, "", "new-task"),
			"an unset state directory is exactly today's behaviour")
	})
}

// TestExecLocalDialsTheRecordedMuxer drives the real local path against a VM that
// looks like a restored one: <new-task>.sock is alive, only <source-task>.vsock
// is listening. A resolver that kept the derivation fails here with ENOENT.
func TestExecLocalDialsTheRecordedMuxer(t *testing.T) {
	vm := startFakeVMWithVsock(t, "mig-exec", "mig-exec-source", nil)
	stateDir := t.TempDir()
	recordRestoredVM(t, stateDir, vm.SocketDir, "mig-exec", "mig-exec-source")

	var out bytes.Buffer
	err := execInVM(context.Background(), "mig-exec", vmExecOptions{
		socketDir: vm.SocketDir,
		stateDir:  stateDir,
		command:   []string{"echo", "restored"},
		stdout:    &out,
		stderr:    &out,
		rawFD:     -1,
	})
	require.NoError(t, err)
	assert.Equal(t, "restored\n", out.String())
}

// TestExecLocalDerivesTheMuxerWithoutARecord pins the unchanged path: a task this
// node booted itself has no record, so the muxer is derived from its own ID.
func TestExecLocalDerivesTheMuxerWithoutARecord(t *testing.T) {
	vm := startFakeVM(t, "cold-exec", nil)
	stateDir := t.TempDir()

	var out bytes.Buffer
	err := execInVM(context.Background(), "cold-exec", vmExecOptions{
		socketDir: vm.SocketDir,
		stateDir:  stateDir,
		command:   []string{"echo", "cold"},
		stdout:    &out,
		stderr:    &out,
		rawFD:     -1,
	})
	require.NoError(t, err)
	assert.Equal(t, "cold\n", out.String())
}

// TestVMCpUsesTheRecordedMuxer is the same two cases for `vm cp`: a copy out of a
// restored VM has to reach the source task's muxer, and a copy out of an ordinary
// one still uses its own.
func TestVMCpUsesTheRecordedMuxer(t *testing.T) {
	t.Run("restored task", func(t *testing.T) {
		vm := startFakeVMWithVsock(t, "mig-cp", "mig-cp-source", nil)
		stateDir := t.TempDir()
		recordRestoredVM(t, stateDir, vm.SocketDir, "mig-cp", "mig-cp-source")

		guest := filepath.Join(t.TempDir(), "guest.txt")
		require.NoError(t, os.WriteFile(guest, []byte("migrated payload"), 0o644))

		local := filepath.Join(t.TempDir(), "local.txt")
		require.NoError(t, copyFileToOrFromVM(
			context.Background(), vm.SocketDir, stateDir, "mig-cp:"+guest, local, 0o644))

		got, err := os.ReadFile(local)
		require.NoError(t, err)
		assert.Equal(t, "migrated payload", string(got))
	})

	t.Run("task without a record", func(t *testing.T) {
		vm := startFakeVM(t, "cold-cp", nil)
		stateDir := t.TempDir()

		local := filepath.Join(t.TempDir(), "local.txt")
		require.NoError(t, os.WriteFile(local, []byte("cold payload"), 0o644))

		guest := filepath.Join(t.TempDir(), "guest.txt")
		require.NoError(t, copyFileToOrFromVM(
			context.Background(), vm.SocketDir, stateDir, local, "cold-cp:"+guest, 0o600))

		got, err := os.ReadFile(guest)
		require.NoError(t, err)
		assert.Equal(t, "cold payload", string(got))
	})
}

// TestVMCpCommandEndToEndUsesTheRecordedMuxer runs the command itself, so the
// --state-dir flag is proven to reach the dial rather than only the helper.
func TestVMCpCommandEndToEndUsesTheRecordedMuxer(t *testing.T) {
	vm := startFakeVMWithVsock(t, "mig-cp-cli", "mig-cp-cli-source", nil)
	stateDir := t.TempDir()
	recordRestoredVM(t, stateDir, vm.SocketDir, "mig-cp-cli", "mig-cp-cli-source")

	local := filepath.Join(t.TempDir(), "local.txt")
	require.NoError(t, os.WriteFile(local, []byte("cli payload"), 0o644))
	guest := filepath.Join(t.TempDir(), "guest.txt")

	cmd := newVMCpCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"--socket-dir", vm.SocketDir,
		"--state-dir", stateDir,
		local, "mig-cp-cli:" + guest,
	})

	require.NoError(t, cmd.Execute())

	got, err := os.ReadFile(guest)
	require.NoError(t, err)
	assert.Equal(t, "cli payload", string(got))
}

// TestVMExecCommandEndToEndUsesTheRecordedMuxer does the same for `vm exec`.
func TestVMExecCommandEndToEndUsesTheRecordedMuxer(t *testing.T) {
	vm := startFakeVMWithVsock(t, "mig-exec-cli", "mig-exec-cli-source", nil)
	stateDir := t.TempDir()
	recordRestoredVM(t, stateDir, vm.SocketDir, "mig-exec-cli", "mig-exec-cli-source")

	cmd := newVMExecCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--socket-dir", vm.SocketDir,
		"--state-dir", stateDir,
		"mig-exec-cli", "--", "echo", "cli",
	})

	require.NoError(t, cmd.Execute())
	assert.Equal(t, "cli\n", out.String())
}
