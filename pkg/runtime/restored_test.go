package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restoredFixture is a valid record: task "new-task" runs the VM of the source
// task "source-task", so every host-side name comes from "source-task" while the
// API socket belongs to "new-task".
func restoredFixture(t *testing.T, socketDir string) RestoredInstance {
	t.Helper()

	return RestoredInstance{
		TaskID:     "new-task",
		VMID:       "source-task",
		ArtifactID: "mig-0123456789abcdef",
		SocketPath: filepath.Join(socketDir, "new-task"+VMSocketSuffix),
		TapName:    TapName("source-task", 0),
		VsockPath:  VsockPath(socketDir, "source-task"),
		DiskPath:   filepath.Join(socketDir, "tasks", "source-task.ext4"),
		CreatedAt:  time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}
}

func newTestRegistry(t *testing.T) (*RestoredRegistry, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), RestoredInstanceDirName)
	reg, err := NewRestoredRegistry(dir)
	require.NoError(t, err)
	return reg, dir
}

func TestRestoredInstanceDir(t *testing.T) {
	cases := []struct {
		name     string
		stateDir string
		want     string
	}{
		{name: "the daemon's state directory", stateDir: "/var/lib/swarmkit", want: "/var/lib/swarmkit/" + RestoredInstanceDirName},
		{name: "a trailing separator is cleaned", stateDir: "/var/lib/swarmkit/", want: "/var/lib/swarmkit/" + RestoredInstanceDirName},
		{name: "a relative state directory keeps its shape", stateDir: "state", want: "state/" + RestoredInstanceDirName},
		{name: "no state directory", stateDir: "", want: ""},
		{name: "blank state directory", stateDir: "   ", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RestoredInstanceDir(tc.stateDir))
		})
	}
}

func TestNewRestoredRegistry(t *testing.T) {
	t.Run("creates the directory 0700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", RestoredInstanceDirName)
		reg, err := NewRestoredRegistry(dir)
		require.NoError(t, err)
		require.NotNil(t, reg)

		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
			"the records name host paths and are readable only by the daemon")
	})

	t.Run("an existing directory is reused", func(t *testing.T) {
		dir := t.TempDir()
		reg, err := NewRestoredRegistry(dir)
		require.NoError(t, err)
		require.NotNil(t, reg)

		again, err := NewRestoredRegistry(dir)
		require.NoError(t, err, "a daemon restart must open the same directory again")
		assert.NotNil(t, again)
	})

	t.Run("an empty directory is refused", func(t *testing.T) {
		reg, err := NewRestoredRegistry(" ")
		require.Error(t, err)
		assert.Nil(t, reg)
		assert.Contains(t, err.Error(), "directory")
	})

	t.Run("a file in the way is reported", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "restored-instances")
		require.NoError(t, os.WriteFile(path, []byte("not a directory"), 0o600))

		reg, err := NewRestoredRegistry(path)
		require.Error(t, err)
		assert.Nil(t, reg)
		assert.Contains(t, err.Error(), path)
	})
}

func TestRestoredRegistryPutGetDelete(t *testing.T) {
	reg, dir := newTestRegistry(t)
	want := restoredFixture(t, "/var/run/firecracker")

	require.NoError(t, reg.Put(want))

	got, found, err := reg.Get(want.TaskID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, want.TaskID, got.TaskID)
	assert.Equal(t, want.VMID, got.VMID)
	assert.Equal(t, want.ArtifactID, got.ArtifactID)
	assert.Equal(t, want.SocketPath, got.SocketPath)
	assert.Equal(t, want.TapName, got.TapName)
	assert.Equal(t, want.VsockPath, got.VsockPath)
	assert.Equal(t, want.DiskPath, got.DiskPath)
	assert.True(t, want.CreatedAt.Equal(got.CreatedAt), "createdAt = %v, want %v", got.CreatedAt, want.CreatedAt)

	// One record per task, named after the task, so a human can find it.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a successful Put leaves exactly one file, no temporary remainder")
	assert.Equal(t, want.TaskID+".json", entries[0].Name())

	info, err := os.Stat(filepath.Join(dir, want.TaskID+".json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the record is written 0600, like the rest of the daemon's state")

	require.NoError(t, reg.Delete(want.TaskID))
	_, found, err = reg.Get(want.TaskID)
	require.NoError(t, err)
	assert.False(t, found)

	// Idempotent: a task's stop path may run twice (executor teardown and a
	// retried teardown), and the second run must not fail.
	require.NoError(t, reg.Delete(want.TaskID))
}

func TestRestoredRegistryGetMissing(t *testing.T) {
	reg, _ := newTestRegistry(t)

	got, found, err := reg.Get("never-restored")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, RestoredInstance{}, got)
}

func TestRestoredRegistryPutOverwrites(t *testing.T) {
	reg, _ := newTestRegistry(t)
	first := restoredFixture(t, "/var/run/firecracker")
	require.NoError(t, reg.Put(first))

	// A retried import re-records the same task; the record must describe the
	// last import, not accumulate.
	second := first
	second.ArtifactID = "mig-fedcba9876543210"
	second.TapName = TapName(first.VMID, 2)
	require.NoError(t, reg.Put(second))

	got, found, err := reg.Get(first.TaskID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, second.ArtifactID, got.ArtifactID)
	assert.Equal(t, second.TapName, got.TapName)

	all, err := reg.List()
	require.NoError(t, err)
	assert.Len(t, all, 1, "a second Put must replace the record, not add one")
}

// TestRestoredRegistryRestart proves the records outlive the daemon process: a
// second registry over the same directory resolves a restored task without
// having written anything itself.
func TestRestoredRegistryRestart(t *testing.T) {
	first, dir := newTestRegistry(t)
	want := restoredFixture(t, "/var/run/firecracker")
	require.NoError(t, first.Put(want))

	second, err := NewRestoredRegistry(dir)
	require.NoError(t, err)

	got, found, err := second.Get(want.TaskID)
	require.NoError(t, err)
	require.True(t, found, "a restarted daemon must still resolve a running restored VM")
	assert.Equal(t, want, got)

	all, err := second.List()
	require.NoError(t, err)
	assert.Equal(t, []RestoredInstance{want}, all)
}

func TestRestoredRegistryList(t *testing.T) {
	reg, _ := newTestRegistry(t)

	empty, err := reg.List()
	require.NoError(t, err)
	assert.Empty(t, empty)

	// Written out of order so the sort is observable.
	for _, taskID := range []string{"task-c", "task-a", "task-b"} {
		rec := restoredFixture(t, "/var/run/firecracker")
		rec.TaskID = taskID
		rec.SocketPath = filepath.Join("/var/run/firecracker", taskID+VMSocketSuffix)
		require.NoError(t, reg.Put(rec))
	}

	all, err := reg.List()
	require.NoError(t, err)
	require.Len(t, all, 3)

	ids := make([]string, 0, len(all))
	for _, rec := range all {
		ids = append(ids, rec.TaskID)
	}
	assert.Equal(t, []string{"task-a", "task-b", "task-c"}, ids, "List is sorted so startup reconciliation is deterministic")
	pinned := make([]string, len(ids))
	copy(pinned, ids)
	assert.True(t, sort.StringsAreSorted(pinned))
}

func TestRestoredRegistryRejectsBadRecords(t *testing.T) {
	socketDir := "/var/run/firecracker"

	cases := []struct {
		name    string
		mutate  func(*RestoredInstance)
		wantSub string
	}{
		{
			name:    "no task ID",
			mutate:  func(r *RestoredInstance) { r.TaskID = "" },
			wantSub: "taskId is empty",
		},
		{
			name:    "task ID with a path separator",
			mutate:  func(r *RestoredInstance) { r.TaskID = "a/b" },
			wantSub: "taskId",
		},
		{
			name:    "task ID with a dot",
			mutate:  func(r *RestoredInstance) { r.TaskID = ".." },
			wantSub: "taskId",
		},
		{
			name:    "no source VM ID",
			mutate:  func(r *RestoredInstance) { r.VMID = "" },
			wantSub: "vmId is empty",
		},
		{
			name:    "source VM ID with a path separator",
			mutate:  func(r *RestoredInstance) { r.VMID = "source/task" },
			wantSub: "vmId",
		},
		{
			name:    "no artifact ID",
			mutate:  func(r *RestoredInstance) { r.ArtifactID = " " },
			wantSub: "artifactId is empty",
		},
		{
			name:    "no socket path",
			mutate:  func(r *RestoredInstance) { r.SocketPath = "" },
			wantSub: "socketPath is empty",
		},
		{
			name:    "relative socket path",
			mutate:  func(r *RestoredInstance) { r.SocketPath = "new-task.sock" },
			wantSub: "socketPath",
		},
		{
			name:    "socket path of another task",
			mutate:  func(r *RestoredInstance) { r.SocketPath = filepath.Join(socketDir, "other-task.sock") },
			wantSub: "socketPath",
		},
		{
			name:    "socket path is a console socket",
			mutate:  func(r *RestoredInstance) { r.SocketPath = filepath.Join(socketDir, "new-task.console.sock") },
			wantSub: "socketPath",
		},
		{
			name:    "no TAP name",
			mutate:  func(r *RestoredInstance) { r.TapName = "" },
			wantSub: "tapName",
		},
		{
			name:    "TAP name of another task",
			mutate:  func(r *RestoredInstance) { r.TapName = TapName("other-task", 0) },
			wantSub: "tapName",
		},
		{
			name:    "TAP name without an index",
			mutate:  func(r *RestoredInstance) { r.TapName = "tap-0a76cd9b" },
			wantSub: "tapName",
		},
		{
			name:    "no vsock path",
			mutate:  func(r *RestoredInstance) { r.VsockPath = "" },
			wantSub: "vsockPath is empty",
		},
		{
			name:    "relative vsock path",
			mutate:  func(r *RestoredInstance) { r.VsockPath = "source-task.vsock" },
			wantSub: "vsockPath",
		},
		{
			name:    "vsock path of another task",
			mutate:  func(r *RestoredInstance) { r.VsockPath = VsockPath(socketDir, "other-task") },
			wantSub: "vsockPath",
		},
		{
			name:    "no disk path",
			mutate:  func(r *RestoredInstance) { r.DiskPath = "" },
			wantSub: "diskPath is empty",
		},
		{
			name:    "relative disk path",
			mutate:  func(r *RestoredInstance) { r.DiskPath = "tasks/source-task.ext4" },
			wantSub: "diskPath",
		},
		{
			name:    "no creation time",
			mutate:  func(r *RestoredInstance) { r.CreatedAt = time.Time{} },
			wantSub: "createdAt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, dir := newTestRegistry(t)
			rec := restoredFixture(t, socketDir)
			tc.mutate(&rec)

			err := reg.Put(rec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSub,
				"the error must name the field that is wrong: %v", err)

			entries, readErr := os.ReadDir(dir)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "a refused record must not be written")
		})
	}
}

func TestRestoredRegistryAcceptsEveryInterfaceIndex(t *testing.T) {
	reg, _ := newTestRegistry(t)

	for _, index := range []int{0, 1, 10} {
		rec := restoredFixture(t, "/var/run/firecracker")
		rec.TaskID = "task-" + string(rune('a'+index))
		rec.SocketPath = filepath.Join("/var/run/firecracker", rec.TaskID+VMSocketSuffix)
		rec.TapName = TapName(rec.VMID, index)
		require.NoError(t, reg.Put(rec), "the snapshot may record any interface index")
	}
}

func TestRestoredRegistryGetRejectsBadTaskIDs(t *testing.T) {
	reg, _ := newTestRegistry(t)

	for _, taskID := range []string{"", "a/b", "..", "a.b"} {
		_, found, err := reg.Get(taskID)
		require.Error(t, err, "task ID %q must be refused", taskID)
		assert.False(t, found)

		require.Error(t, reg.Delete(taskID), "task ID %q must be refused", taskID)
	}
}

// TestRestoredRegistryCorruptRecord pins the ruling: a corrupt record is an
// error the caller decides how to log, and the file is left alone so a human can
// inspect it. List skips it, because one bad file must not hide the others.
func TestRestoredRegistryCorruptRecord(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantSub string
	}{
		{
			name:    "not JSON",
			content: "{not json",
			wantSub: "corrupt",
		},
		{
			name:    "empty file",
			content: "",
			wantSub: "corrupt",
		},
		{
			name:    "record of another task",
			content: `{"taskId":"other-task","vmId":"source-task"}`,
			wantSub: "belongs to task",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, dir := newTestRegistry(t)
			good := restoredFixture(t, "/var/run/firecracker")
			require.NoError(t, reg.Put(good))

			path := filepath.Join(dir, "broken-task.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))

			_, found, err := reg.Get("broken-task")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSub)
			assert.False(t, found)

			_, statErr := os.Stat(path)
			assert.NoError(t, statErr, "a corrupt record is left in place for inspection")

			all, listErr := reg.List()
			require.NoError(t, listErr)
			require.Len(t, all, 1, "one corrupt record must not hide the healthy ones")
			assert.Equal(t, good.TaskID, all[0].TaskID)

			// Deleting the corrupt record still works: it is a file, not data
			// the registry needs to decode.
			require.NoError(t, reg.Delete("broken-task"))
			_, statErr = os.Stat(path)
			assert.True(t, os.IsNotExist(statErr))
		})
	}
}

// TestRestoredRegistryPutIsAtomic makes the rename fail (the destination is a
// directory) and asserts the failed write leaves no partial record and no
// temporary file behind.
func TestRestoredRegistryPutIsAtomic(t *testing.T) {
	reg, dir := newTestRegistry(t)
	rec := restoredFixture(t, "/var/run/firecracker")

	blocked := filepath.Join(dir, rec.TaskID+".json")
	require.NoError(t, os.Mkdir(blocked, 0o700))

	err := reg.Put(rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), rec.TaskID)
	assert.Contains(t, err.Error(), "rename into place",
		"the write must reach the rename, so the leftover check below is about a failed rename: %v", err)

	info, statErr := os.Stat(blocked)
	require.NoError(t, statErr, "the destination directory must be left untouched")
	assert.True(t, info.IsDir())

	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Len(t, entries, 1, "the only entry left is the blocking directory")
	assert.Equal(t, rec.TaskID+".json", entries[0].Name())
	for _, entry := range entries {
		assert.False(t, strings.HasSuffix(entry.Name(), ".tmp"),
			"a failed Put must remove its temporary file, found %q", entry.Name())
	}
}

func TestRestoredRegistryPutFailsWhenDirectoryIsGone(t *testing.T) {
	reg, dir := newTestRegistry(t)
	require.NoError(t, os.RemoveAll(dir))

	err := reg.Put(restoredFixture(t, "/var/run/firecracker"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporary")
}

func TestRestoredRegistryConcurrentAccess(t *testing.T) {
	reg, _ := newTestRegistry(t)

	const tasks = 24
	var wg sync.WaitGroup
	errs := make(chan error, tasks*4)

	for i := 0; i < tasks; i++ {
		taskID := "task-" + strings.Repeat("x", i%3) + string(rune('a'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := restoredFixture(t, "/var/run/firecracker")
			rec.TaskID = taskID
			rec.SocketPath = filepath.Join("/var/run/firecracker", taskID+VMSocketSuffix)

			if err := reg.Put(rec); err != nil {
				errs <- err
				return
			}
			if _, _, err := reg.Get(taskID); err != nil {
				errs <- err
				return
			}
			if _, err := reg.List(); err != nil {
				errs <- err
				return
			}
			if err := reg.Delete(taskID); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}

	all, err := reg.List()
	require.NoError(t, err)
	assert.Empty(t, all, "every task deleted its own record")
}

func TestRestoredRegistryZeroValueIsRefused(t *testing.T) {
	// A nil registry is what a daemon without the store would carry; every
	// operation must say so instead of addressing the working directory.
	var nilRegistry *RestoredRegistry
	empty := &RestoredRegistry{}

	cases := []struct {
		name string
		reg  *RestoredRegistry
	}{
		{name: "nil registry", reg: nilRegistry},
		{name: "registry without a directory", reg: empty},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, tc.reg.Put(restoredFixture(t, "/var/run/firecracker")))

			_, found, err := tc.reg.Get("new-task")
			require.Error(t, err)
			assert.False(t, found)

			require.Error(t, tc.reg.Delete("new-task"))

			records, err := tc.reg.List()
			require.Error(t, err)
			assert.Empty(t, records)
		})
	}
}

// TestRestoredRegistryVanishedDirectory pins what a registry does when its
// directory was removed under it: a lookup is "no record" (the same answer as a
// missing file), a delete is success (idempotent), and a listing is an error
// because a reconciliation that cannot see the directory must not conclude the
// node hosts nothing.
func TestRestoredRegistryVanishedDirectory(t *testing.T) {
	reg, dir := newTestRegistry(t)
	require.NoError(t, os.RemoveAll(dir))

	_, found, err := reg.Get("new-task")
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, reg.Delete("new-task"))

	_, err = reg.List()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restored-instance registry")
}

func TestRestoredInstanceJSON(t *testing.T) {
	rec := restoredFixture(t, "/var/run/firecracker")

	data, err := json.Marshal(rec)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	assert.Equal(t, []string{
		"artifactId", "createdAt", "diskPath", "socketPath", "tapName", "taskId", "vmId", "vsockPath",
	}, keys, "the on-disk record's keys are the contract a restarted daemon reads")
}
