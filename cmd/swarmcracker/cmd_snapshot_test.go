package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotTestConfig points the snapshot commands at a throwaway snapshot
// directory, so a command that gets past the ID check cannot touch the real
// default path.
func snapshotTestConfig(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "version: 1\nsnapshot:\n  enabled: false\n  snapshot_dir: " + filepath.Join(dir, "snapshots") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	previous := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = previous })
}

// TestSnapshotCommandsRejectUnsafeIDs pins item 6 at the CLI: a snapshot ID is
// validated before any configuration or filesystem work, and a violation is a
// usage error (exit 2), not a not-found or an internal failure.
func TestSnapshotCommandsRejectUnsafeIDs(t *testing.T) {
	snapshotTestConfig(t)

	commands := map[string]func() (run func([]string) error, name string){
		"delete": func() (func([]string) error, string) {
			cmd := newSnapshotDeleteCommand()
			return func(args []string) error { return cmd.RunE(cmd, args) }, cmd.Name()
		},
		"restore": func() (func([]string) error, string) {
			cmd := newSnapshotRestoreCommand()
			return func(args []string) error { return cmd.RunE(cmd, args) }, cmd.Name()
		},
	}

	ids := []string{
		"../victim",
		"/",
		`C:\snap-0123456789abcdef`,
		`..\..\victim`,
		"snap-0123456789abcdef/child",
		"snap-0123456789ABCEDF",
	}

	for name, build := range commands {
		run, commandName := build()
		require.Equal(t, name, commandName)

		for _, id := range ids {
			t.Run(name+" "+id, func(t *testing.T) {
				err := run([]string{id})

				require.Error(t, err)
				var typed *output.Error
				require.ErrorAs(t, err, &typed, "the CLI must answer a typed error")
				assert.Equal(t, output.CodeUsage, typed.Code)
				assert.Equal(t, 2, output.ExitCode(err))
				assert.Equal(t, "snapshot-id", typed.Details["parameter"])
			})
		}
	}
}

// TestSnapshotCommandsAcceptTheGeneratedShape is the other half: a well-formed
// ID that does not exist is a store failure, not a usage error, so the
// validation never rejects a real snapshot.
func TestSnapshotCommandsAcceptTheGeneratedShape(t *testing.T) {
	snapshotTestConfig(t)

	const missing = "snap-00000000000000ff"

	deleteCmd := newSnapshotDeleteCommand()
	err := deleteCmd.RunE(deleteCmd, []string{missing})
	require.Error(t, err)
	assert.False(t, isUsageError(err), "a missing snapshot is not a usage error")
	assert.True(t, errors.Is(err, snapshot.ErrNotFound), "the store's not-found sentinel must survive")

	restoreCmd := newSnapshotRestoreCommand()
	err = restoreCmd.RunE(restoreCmd, []string{missing})
	require.Error(t, err)
	assert.False(t, isUsageError(err), "a missing snapshot is not a usage error")
}

// isUsageError reports whether err carries the usage code.
func isUsageError(err error) bool {
	var typed *output.Error
	return errors.As(err, &typed) && typed.Code == output.CodeUsage
}
