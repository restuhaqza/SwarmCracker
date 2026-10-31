package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeRestoredRecord writes one record the way the node's restore path does —
// through the registry — and returns the record's file path, so a test reads
// exactly what a restore wrote rather than a hand-shaped fixture.
func writeRestoredRecord(t *testing.T, stateDir string, instance RestoredInstance) string {
	t.Helper()

	dir := RestoredInstanceDir(stateDir)
	require.NotEmpty(t, dir)
	reg, err := NewRestoredRegistry(dir)
	require.NoError(t, err)
	require.NoError(t, reg.Put(instance))
	return filepath.Join(dir, instance.TaskID+restoredRecordSuffix)
}

// writeRecordAt writes raw record bytes where a task's record file lives, creating
// the directory. It is for the shapes Put refuses to write (corrupt, foreign, a
// directory in the file's place).
func writeRecordAt(t *testing.T, path string, data []byte) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

// captureRestoredWarnings redirects the package logger for one test and returns
// the buffer. RestoredVsockPath warns about a record it cannot use; nothing else
// in this test file logs, so every line here is that warning.
func captureRestoredWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = previous })
	return &buf
}

// TestRestoredVsockPathReturnsTheRecordedMuxer is the whole point of the helper:
// a task started from a migration artifact answers on the source task's muxer,
// which is not the path its own ID derives.
func TestRestoredVsockPathReturnsTheRecordedMuxer(t *testing.T) {
	stateDir := t.TempDir()
	socketDir := t.TempDir()
	record := restoredFixture(t, socketDir)
	writeRestoredRecord(t, stateDir, record)

	got, ok := RestoredVsockPath(stateDir, record.TaskID)
	require.True(t, ok, "a record written by Put must resolve")
	assert.Equal(t, record.VsockPath, got)
	assert.NotEqual(t, VsockPath(socketDir, record.TaskID), got,
		"the derived muxer belongs to no process on a restored task")
}

// TestRestoredVsockPathWithoutARecord pins the silent miss: nothing to say means
// ok is false, so the caller derives the path exactly as it did before.
func TestRestoredVsockPathWithoutARecord(t *testing.T) {
	cases := []struct {
		name     string
		stateDir func(t *testing.T) string
	}{
		{
			name: "another task's record only",
			stateDir: func(t *testing.T) string {
				stateDir := t.TempDir()
				socketDir := t.TempDir()
				// A valid record, for a task that is not the one asked about.
				other := restoredFixture(t, socketDir)
				other.TaskID = "other-task"
				other.SocketPath = filepath.Join(socketDir, other.TaskID+VMSocketSuffix)
				writeRestoredRecord(t, stateDir, other)
				return stateDir
			},
		},
		{
			name:     "no state directory",
			stateDir: func(*testing.T) string { return "" },
		},
		{
			name:     "blank state directory",
			stateDir: func(*testing.T) string { return "   " },
		},
		{
			name:     "state directory that does not exist",
			stateDir: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") },
		},
		{
			name:     "record directory that does not exist",
			stateDir: func(t *testing.T) string { return t.TempDir() },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings := captureRestoredWarnings(t)
			stateDir := tc.stateDir(t)

			got, ok := RestoredVsockPath(stateDir, "new-task")
			assert.False(t, ok)
			assert.Empty(t, got)
			assert.Empty(t, strings.TrimSpace(warnings.String()),
				"a task with no record is the ordinary case: an exec of a locally booted task must log nothing")
		})
	}
}

// TestRestoredVsockPathIsReadOnly pins the constraint the helper exists for: a
// reader must not mutate what it inspects. It must not need the record directory
// — building the registry would create and probe it — and it must not touch the
// record it read.
func TestRestoredVsockPathIsReadOnly(t *testing.T) {
	t.Run("a missing record directory is not created", func(t *testing.T) {
		stateDir := t.TempDir()

		_, ok := RestoredVsockPath(stateDir, "new-task")
		require.False(t, ok)

		_, err := os.Stat(RestoredInstanceDir(stateDir))
		assert.True(t, os.IsNotExist(err),
			"a reader must not create the record directory: a non-root CLI has no business writing it")
	})

	t.Run("an existing record is left byte for byte", func(t *testing.T) {
		stateDir := t.TempDir()
		path := writeRestoredRecord(t, stateDir, restoredFixture(t, t.TempDir()))

		before, err := os.ReadFile(path)
		require.NoError(t, err)
		beforeInfo, err := os.Stat(path)
		require.NoError(t, err)

		_, ok := RestoredVsockPath(stateDir, "new-task")
		require.True(t, ok)

		after, err := os.ReadFile(path)
		require.NoError(t, err)
		afterInfo, err := os.Stat(path)
		require.NoError(t, err)

		assert.Equal(t, string(before), string(after))
		assert.Equal(t, beforeInfo.Mode(), afterInfo.Mode())
		assert.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime())
	})
}

// TestRestoredVsockPathAgreesWithGet pins that the CLI's resolver and the node's
// registry read one record the same way: one read sequence, so one verdict. A
// second copy of Get's steps is how the two would drift apart.
func TestRestoredVsockPathAgreesWithGet(t *testing.T) {
	cases := []struct {
		name string
		// write plants a record Put refuses to write. Nil means the record was
		// written through the registry, or that there is none.
		write   func(t *testing.T, path string)
		wantGet bool // Get reports the record
	}{
		{
			name:    "a record Put wrote",
			wantGet: true,
		},
		{
			name: "no record at all",
		},
		{
			name: "a corrupt record",
			write: func(t *testing.T, path string) {
				writeRecordAt(t, path, []byte("{not json"))
			},
		},
		{
			name: "another task's record",
			write: func(t *testing.T, path string) {
				writeRecordAt(t, path, []byte(`{"taskId":"other-task","vmId":"source-task"}`))
			},
		},
		{
			name: "a record that describes no VM",
			write: func(t *testing.T, path string) {
				writeRecordAt(t, path, []byte(`{"taskId":"new-task"}`))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureRestoredWarnings(t)

			stateDir := t.TempDir()
			reg, err := NewRestoredRegistry(RestoredInstanceDir(stateDir))
			require.NoError(t, err)
			if tc.wantGet {
				require.NoError(t, reg.Put(restoredFixture(t, t.TempDir())))
			}
			if tc.write != nil {
				tc.write(t, filepath.Join(RestoredInstanceDir(stateDir), "new-task"+restoredRecordSuffix))
			}

			record, found, getErr := reg.Get("new-task")
			recorded, ok := RestoredVsockPath(stateDir, "new-task")

			assert.Equal(t, tc.wantGet, found, "Get reports whether the file is there")
			assert.Equal(t, found && getErr == nil, ok,
				"a record one reader accepts is a record the other accepts")
			if tc.wantGet {
				assert.Equal(t, record.VsockPath, recorded, "both answer with the record's muxer")
			}
		})
	}
}

// TestRestoredVsockPathIgnoresAnUnusableRecord pins the ruling for a record the
// helper cannot trust: it answers "nothing recorded" rather than a guess, reports
// the record on every lookup, names the cause an operator has to fix, and leaves
// the file where a human can read it.
func TestRestoredVsockPathIgnoresAnUnusableRecord(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, path string)
		cause   string
	}{
		{
			name: "not JSON",
			prepare: func(t *testing.T, path string) {
				writeRecordAt(t, path, []byte("{not json"))
			},
			cause: "is not a usable record",
		},
		{
			name: "empty file",
			prepare: func(t *testing.T, path string) {
				writeRecordAt(t, path, nil)
			},
			cause: "is not a usable record",
		},
		{
			name: "another task's record",
			prepare: func(t *testing.T, path string) {
				writeRecordAt(t, path, []byte(`{"taskId":"other-task","vmId":"source-task"}`))
			},
			cause: "is not a usable record",
		},
		{
			name: "decodes but describes no usable VM",
			prepare: func(t *testing.T, path string) {
				bad := restoredFixture(t, t.TempDir())
				// A relative muxer path would address whatever the reader's
				// working directory happened to be, which is the shape validate
				// refuses.
				bad.VsockPath = "relative.vsock"
				data, err := json.Marshal(bad)
				require.NoError(t, err)
				writeRecordAt(t, path, data)
			},
			cause: "is not a usable record",
		},
		{
			name: "a record that cannot be read",
			// A directory where the record file belongs: reading it fails with
			// EISDIR whatever the process's privileges, so the cause can be pinned
			// without a file mode that a root test run ignores.
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.Mkdir(path, 0o700))
			},
			cause: "cannot be read",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings := captureRestoredWarnings(t)

			stateDir := t.TempDir()
			path := filepath.Join(RestoredInstanceDir(stateDir), "new-task"+restoredRecordSuffix)
			tc.prepare(t, path)

			for range 2 {
				got, ok := RestoredVsockPath(stateDir, "new-task")
				assert.False(t, ok)
				assert.Empty(t, got)
			}

			_, err := os.Stat(path)
			assert.NoError(t, err, "a record the helper cannot use is left in place for inspection")

			lines := strings.TrimSpace(warnings.String())
			require.NotEmpty(t, lines, "a record that was ignored is reported")
			assert.Equal(t, 2, strings.Count(lines, "\n")+1,
				"each lookup reports the record it ignored: the local CLI does at most one per process, and a library latch would only silence a caller that resolves in a loop")
			assert.Contains(t, lines, tc.cause,
				"the warning names the cause an operator has to fix, not one cause for every failure")
			assert.Contains(t, lines, path)
			assert.Contains(t, lines, "left in place")
		})
	}
}

// TestRestoredVsockPathRefusesATaskIDThatEscapes proves the shared task-ID rule is
// applied before the record path is built: a separator or a dot would otherwise
// address a file outside the record directory.
func TestRestoredVsockPathRefusesATaskIDThatEscapes(t *testing.T) {
	root := t.TempDir()
	socketDir := t.TempDir()
	stateDir := filepath.Join(root, "state")
	recordsDir := RestoredInstanceDir(stateDir)
	require.NoError(t, os.MkdirAll(recordsDir, 0o700))

	// A valid record for task "x", written where "../x" actually resolves: one
	// directory above the record directory. Every field is built for this record's
	// own IDs, so the file itself is one the helper would accept — a stale socket
	// path from the fixture's other task would let validate refuse it and the
	// assertion below would hold whether or not the ID rule ran.
	escape := restoredFixture(t, socketDir)
	escape.TaskID = "x"
	escape.SocketPath = filepath.Join(socketDir, escape.TaskID+VMSocketSuffix)
	require.NoError(t, escape.validate(), "the planted record must be one the helper would otherwise accept")

	data, err := json.Marshal(escape)
	require.NoError(t, err)

	escapedPath := filepath.Join(stateDir, escape.TaskID+restoredRecordSuffix)
	assert.Equal(t, escapedPath, filepath.Join(recordsDir, "..", escape.TaskID+restoredRecordSuffix),
		"the escape target is one directory above the record directory")
	writeRecordAt(t, escapedPath, data)

	// The same record inside the record directory resolves, so the refusals below
	// are about the escaped ID and not about the record's content.
	writeRecordAt(t, filepath.Join(recordsDir, escape.TaskID+restoredRecordSuffix), data)
	recorded, ok := RestoredVsockPath(stateDir, escape.TaskID)
	require.True(t, ok, "the planted record is usable in place")
	require.Equal(t, escape.VsockPath, recorded)

	for _, taskID := range []string{"../x", "..", ".", "a/b", `a\b`, "a b", "x" + strings.Repeat("9", MaxIDLength)} {
		t.Run(taskID, func(t *testing.T) {
			warnings := captureRestoredWarnings(t)

			got, ok := RestoredVsockPath(stateDir, taskID)
			assert.False(t, ok)
			assert.Empty(t, got)
			// The ID is refused before the path is built, so the valid record the
			// escaped path names is never read — and a record is only reported
			// once it has been read and refused. Without this the test would pass
			// for a helper that read outside the directory and then rejected the
			// file for its ID.
			assert.Empty(t, strings.TrimSpace(warnings.String()),
				"an ID that cannot name a file is refused before anything is read or reported")
		})
	}
}
