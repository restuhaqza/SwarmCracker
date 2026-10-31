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
			stateDir := tc.stateDir(t)

			got, ok := RestoredVsockPath(stateDir, "new-task")
			assert.False(t, ok)
			assert.Empty(t, got)
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

// TestRestoredVsockPathIgnoresAnUnusableRecord pins the ruling for a record the
// helper cannot trust: it answers "nothing recorded" rather than a guess, warns
// once, and leaves the file where a human can read it.
func TestRestoredVsockPathIgnoresAnUnusableRecord(t *testing.T) {
	cases := []struct {
		name    string
		content func(t *testing.T) []byte
	}{
		{
			name:    "not JSON",
			content: func(*testing.T) []byte { return []byte("{not json") },
		},
		{
			name:    "empty file",
			content: func(*testing.T) []byte { return nil },
		},
		{
			name: "another task's record",
			content: func(*testing.T) []byte {
				return []byte(`{"taskId":"other-task","vmId":"source-task"}`)
			},
		},
		{
			name: "decodes but describes no usable VM",
			content: func(t *testing.T) []byte {
				bad := restoredFixture(t, t.TempDir())
				// A relative muxer path would address whatever the reader's
				// working directory happened to be, which is the shape validate
				// refuses.
				bad.VsockPath = "relative.vsock"
				data, err := json.Marshal(bad)
				require.NoError(t, err)
				return data
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings := captureRestoredWarnings(t)

			stateDir := t.TempDir()
			path := filepath.Join(RestoredInstanceDir(stateDir), "new-task"+restoredRecordSuffix)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, tc.content(t), 0o600))

			for range 2 {
				got, ok := RestoredVsockPath(stateDir, "new-task")
				assert.False(t, ok)
				assert.Empty(t, got)
			}

			_, err := os.Stat(path)
			assert.NoError(t, err, "a record the helper cannot use is left in place for inspection")

			lines := strings.TrimSpace(warnings.String())
			require.NotEmpty(t, lines, "a record that was ignored is reported")
			assert.Equal(t, 1, strings.Count(lines, "\n")+1,
				"the warning is logged once per record, not once per lookup")
			assert.Contains(t, lines, path)
			assert.Contains(t, lines, "left in place")
		})
	}
}

// TestRestoredVsockPathRefusesATaskIDThatEscapes proves the shared task-ID rule
// is applied before the record path is built: a separator or a dot would
// otherwise address a file outside the record directory.
func TestRestoredVsockPathRefusesATaskIDThatEscapes(t *testing.T) {
	root := t.TempDir()
	// A valid record one level above the state directory: the shape "../x" would
	// address if the ID were not checked.
	outside := restoredFixture(t, t.TempDir())
	outside.TaskID = "x"
	data, err := json.Marshal(outside)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "x"+restoredRecordSuffix), data, 0o600))

	stateDir := filepath.Join(root, "state")
	require.NoError(t, os.MkdirAll(RestoredInstanceDir(stateDir), 0o700))

	for _, taskID := range []string{"../x", "..", ".", "a/b", `a\b`, "a b", "x" + strings.Repeat("9", MaxIDLength)} {
		t.Run(taskID, func(t *testing.T) {
			got, ok := RestoredVsockPath(stateDir, taskID)
			assert.False(t, ok)
			assert.Empty(t, got)
		})
	}
}
