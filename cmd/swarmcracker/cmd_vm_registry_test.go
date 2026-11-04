package main

// The CLI's view of the node's VMs, read from the instance registry (SBX-014).
//
// `vm list` and `vm status` used to know a VM only from the CLI's state file or
// from an API socket found in a directory. The registry is the third and
// authoritative source: a record names the socket, the PID and the boot time of a
// VM the node is running, including one that is still booting. These tests pin
// that the registry answers first, that a VM with no record still shows up by the
// one-release derivation, and — the part that must not be hidden — that the two
// are visibly different in the output.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordedStore opens a real instance registry under a state directory the CLI
// resolves through $SWARM_STATE_DIR, and returns it with the directory. The
// records are written with this process's PID, so the store treats them as live.
func recordedStore(t *testing.T) *vminstance.Store {
	t.Helper()

	stateDir := t.TempDir()
	t.Setenv("SWARM_STATE_DIR", stateDir)

	store, err := vminstance.NewStore(vminstance.Config{Dir: vminstance.Dir(stateDir)})
	require.NoError(t, err)
	return store
}

// putRecord writes a live record for id: a task VM whose socket is under the
// store's own directory, so nothing about it can be derived from the CLI's socket
// directory.
func putRecord(t *testing.T, store *vminstance.Store, id string) vminstance.Instance {
	t.Helper()

	dir := t.TempDir()
	record := vminstance.Instance{
		ID:        id,
		Kind:      vminstance.KindTask,
		PID:       os.Getpid(),
		APISocket: filepath.Join(dir, "firecracker.sock"),
		VsockPath: filepath.Join(dir, "agent.vsock"),
		Console:   filepath.Join(dir, "console.sock"),
		Metadata:  filepath.Join(dir, "runtime.json"),
		Booted:    time.Now().UTC(),
	}
	require.NoError(t, store.Put(record))
	return record
}

// TestLoadRecordedVMsReadsTheStore pins the reader: every live record becomes a
// row whose paths and PID are the record's, marked as coming from the registry,
// and the records come back too because they name each VM's metadata sidecar.
func TestLoadRecordedVMsReadsTheStore(t *testing.T) {
	store := recordedStore(t)
	record := putRecord(t, store, "sbx-1")

	vms, records, err := loadRecordedVMs(store)
	require.NoError(t, err)
	require.Len(t, vms, 1)

	assert.Equal(t, "sbx-1", vms[0].ID)
	assert.Equal(t, runtime.VMSourceRegistry, vms[0].Source)
	assert.Equal(t, record.APISocket, vms[0].SocketPath, "the record's socket, not a derived one")
	assert.Equal(t, record.PID, vms[0].PID)
	assert.WithinDuration(t, record.Booted, vms[0].StartTime, time.Second)
	assert.Equal(t, record.Metadata, records["sbx-1"].Metadata)

	// A node that keeps no registry here is an empty listing, not a failure.
	empty, noRecords, err := loadRecordedVMs(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
	assert.Empty(t, noRecords)
}

// TestMergeRecordedVMsPrefersTheRecord pins the precedence: the record is
// authoritative about the VM it describes, a VM only the registry knows is added,
// and every other row says it was derived rather than quietly looking identical.
func TestMergeRecordedVMsPrefersTheRecord(t *testing.T) {
	booted := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)

	fromState := &runtime.VMState{
		ID:         "task-1",
		Status:     "running",
		SocketPath: "/var/run/firecracker/task-1.sock",
		PID:        111,
		Image:      "nginx:alpine",
	}
	legacy := &runtime.VMState{
		ID:         "task-legacy",
		Status:     "running",
		SocketPath: "/var/run/firecracker/task-legacy.sock",
	}
	recorded := &runtime.VMState{
		ID:         "task-1",
		Status:     "running",
		SocketPath: "/var/lib/firecracker/task-1/firecracker.sock",
		PID:        222,
		StartTime:  booted,
		Source:     runtime.VMSourceRegistry,
	}
	recordOnly := &runtime.VMState{
		ID:         "sbx-booting",
		Status:     "running",
		SocketPath: "/var/lib/firecracker/sbx-booting/firecracker.sock",
		PID:        333,
		Source:     runtime.VMSourceRegistry,
	}

	merged := mergeRecordedVMs([]*runtime.VMState{fromState, legacy}, []*runtime.VMState{recorded, recordOnly})
	require.Len(t, merged, 3)
	assert.Equal(t, []string{"sbx-booting", "task-1", "task-legacy"},
		[]string{merged[0].ID, merged[1].ID, merged[2].ID}, "the listing is sorted by ID")

	// The record wins for the VM both sources know, and the state file's extra
	// detail (the image) survives.
	assert.Equal(t, recorded.SocketPath, merged[1].SocketPath)
	assert.Equal(t, 222, merged[1].PID)
	assert.Equal(t, booted, merged[1].StartTime)
	assert.Equal(t, runtime.VMSourceRegistry, merged[1].Source)
	assert.Equal(t, "nginx:alpine", merged[1].Image)

	// A VM with no record is kept and says so.
	assert.Equal(t, runtime.VMSourceDerived, merged[2].Source)

	// A VM only the registry knows is listed: this is the one that is still
	// booting, whose record exists before its API socket answers.
	assert.Equal(t, runtime.VMSourceRegistry, merged[0].Source)
}

// TestOutputTableShowsTheSource is the golden output of the listing: the SOURCE
// column is part of the table, because "the node recorded this VM" and "a socket
// file named this VM" are different facts and a reader must be able to see which
// one they are looking at.
func TestOutputTableShowsTheSource(t *testing.T) {
	vms := []*runtime.VMState{
		{ID: "sbx-recorded", Status: "running", StartTime: time.Now(), PID: 4242, Source: runtime.VMSourceRegistry},
		{ID: "task-legacy", Status: "running", StartTime: time.Now(), Source: runtime.VMSourceDerived},
	}

	out := captureStdout(t, func() { _ = outputTable(vms) })

	want := strings.Join([]string{
		"ID                   STATUS       SOURCE    HEALTH     SERVICE        IMAGE                     PID      STARTED",
		strings.Repeat("-", 116),
		// SERVICE and IMAGE are empty here: this node knows the VM but not what it
		// runs, which is exactly what a record-only row looks like.
		"sbx-recorded         Running ✓    registry  -                                                   4242     0s",
		"task-legacy          Running ✓    derived   -                                                   -        0s",
		"",
		"Total: 2 VM(s)",
	}, "\n")

	assert.Equal(t, want, trimTrailingSpaces(out))
}

// TestOutputJSONShowsTheSource pins the machine-readable half: a consumer can
// tell a recorded VM from a derived one without parsing the table.
func TestOutputJSONShowsTheSource(t *testing.T) {
	out := captureStdout(t, func() {
		_ = outputJSON([]*runtime.VMState{
			{ID: "sbx-1", Status: "running", Source: runtime.VMSourceRegistry},
			{ID: "legacy", Status: "running", Source: runtime.VMSourceDerived},
		})
	})

	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 2)
	assert.Equal(t, runtime.VMSourceRegistry, got[0]["source"])
	assert.Equal(t, runtime.VMSourceDerived, got[1]["source"])
}

// TestResolveRunningVMAnswersFromTheRegistry pins `vm status` on a VM the node
// recorded: it resolves with no socket anywhere in the scanned directory, and the
// answer says the record named it.
func TestResolveRunningVMAnswersFromTheRegistry(t *testing.T) {
	store := recordedStore(t)
	record := putRecord(t, store, "sbx-status")

	previous := vmSocketDir
	vmSocketDir = t.TempDir() // no sockets at all: only the record can answer
	t.Cleanup(func() { vmSocketDir = previous })

	vm, err := resolveRunningVM("sbx-")
	require.NoError(t, err)
	assert.Equal(t, "sbx-status", vm.ID)
	assert.Equal(t, record.APISocket, vm.SocketPath)
	assert.Equal(t, runtime.VMSourceRegistry, vm.Source)
}

// TestResolveRunningVMFallsBackToTheSocket pins the fallback: with no record and
// no live socket, the answer is the same "no running VMs" failure as before, so a
// node that keeps no registry behaves exactly as it did.
func TestResolveRunningVMFallsBackToTheSocket(t *testing.T) {
	recordedStore(t) // a registry with nothing in it

	previous := vmSocketDir
	vmSocketDir = t.TempDir()
	t.Cleanup(func() { vmSocketDir = previous })

	_, err := resolveRunningVM("anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no running VMs found")
}

// TestPrintVMStatusShowsTheSource pins the status view: the line names the source
// and says what it means, so a VM with no record is not presented as if the node
// had described it.
func TestPrintVMStatusShowsTheSource(t *testing.T) {
	cases := []struct {
		source string
		want   string
	}{
		{source: runtime.VMSourceRegistry, want: "Source:       registry (instance record)"},
		{source: runtime.VMSourceDerived, want: "Source:       derived (socket name, no record)"},
	}

	for _, tc := range cases {
		vm := &runtime.VMState{ID: "task-1", Status: "running", StartTime: time.Now(), Source: tc.source}

		out := captureStdout(t, func() { printVMStatus(vm) })
		assert.Contains(t, out, tc.want)
	}
}

// trimTrailingSpaces drops each line's right padding, so a golden table compares
// the columns and their values without depending on invisible trailing spaces.
func trimTrailingSpaces(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}
