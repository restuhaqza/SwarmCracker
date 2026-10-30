package main

import (
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSnapshotDir is deliberately not the default: a template that hardcodes
// /var/lib/firecracker/snapshots instead of rendering the configured value
// would still pass a test that used the default.
const testSnapshotDir = "/srv/swarmcracker-test/snapshots"

// newTestInitConfig returns an initConfig with every field set to something
// non-empty, so the rendered unit never depends on zero values.
func newTestInitConfig() *initConfig {
	return &initConfig{
		Hostname:      "node-1",
		AdvertiseAddr: "10.0.0.1:4242",
		ListenAddr:    "0.0.0.0:4242",
		StateDir:      "/var/lib/swarmkit",
		ConfigDir:     "/etc/swarmcracker",
		KernelPath:    "/usr/share/firecracker/vmlinux",
		RootfsDir:     "/var/lib/firecracker/rootfs",
		SocketDir:     "/var/run/firecracker",
		SnapshotDir:   testSnapshotDir,
		VCPUs:         2,
		Memory:        1024,
		BridgeName:    "swarm-br0",
		Subnet:        "192.168.127.0/24",
		BridgeIP:      "192.168.127.1/24",
		EnableCNI:     true,
	}
}

// newTestJoinConfig mirrors newTestInitConfig for the worker unit.
func newTestJoinConfig() *joinConfig {
	return &joinConfig{
		ManagerAddr:   "10.0.0.1:4242",
		Token:         "SWMTKN-1-test",
		Hostname:      "node-2",
		StateDir:      "/var/lib/swarmkit",
		ConfigDir:     "/etc/swarmcracker",
		KernelPath:    "/usr/share/firecracker/vmlinux",
		RootfsDir:     "/var/lib/firecracker/rootfs",
		SocketDir:     "/var/run/firecracker",
		SnapshotDir:   testSnapshotDir,
		VCPUs:         2,
		Memory:        1024,
		BridgeName:    "swarm-br0",
		Subnet:        "192.168.127.0/24",
		BridgeIP:      "192.168.127.1/24",
		AdvertiseAddr: "10.0.0.2:4242",
		EnableCNI:     true,
	}
}

// readWritePaths returns the whitespace-separated entries of the unit's single
// ReadWritePaths= line.
func readWritePaths(t *testing.T, unit string) []string {
	t.Helper()

	var line string
	for _, l := range strings.Split(unit, "\n") {
		if strings.HasPrefix(l, "ReadWritePaths=") {
			require.Empty(t, line, "unit declares ReadWritePaths= more than once")
			line = l
		}
	}
	require.NotEmpty(t, line, "unit has no ReadWritePaths= line")
	return strings.Fields(strings.TrimPrefix(line, "ReadWritePaths="))
}

// execStartLine returns the unit's ExecStart= value with continuations joined,
// so a flag placed on its own continuation line is still visible.
func execStartLine(t *testing.T, unit string) string {
	t.Helper()

	var b strings.Builder
	inExec := false
	for _, l := range strings.Split(unit, "\n") {
		switch {
		case strings.HasPrefix(l, "ExecStart="):
			inExec = true
			b.WriteString(strings.TrimSuffix(strings.TrimPrefix(l, "ExecStart="), "\\"))
		case inExec && strings.HasSuffix(l, "\\"):
			b.WriteString(" ")
			b.WriteString(strings.TrimSuffix(l, "\\"))
		case inExec:
			b.WriteString(" ")
			b.WriteString(l)
			return b.String()
		}
	}
	t.Fatal("unit has no ExecStart= line")
	return ""
}

// TestRenderManagerServicePassesSnapshotDir covers the daemon flag: without it
// the node writes snapshots to its default directory, which the unit's
// ReadWritePaths may not allow.
func TestRenderManagerServicePassesSnapshotDir(t *testing.T) {
	unit, err := renderManagerService(newTestInitConfig())
	require.NoError(t, err)

	exec := execStartLine(t, string(unit))
	assert.Contains(t, exec, "--snapshot-dir "+testSnapshotDir,
		"manager unit must pass --snapshot-dir to the daemon")
}

// TestRenderManagerServiceAllowsSnapshotDirWrites is the regression test for
// the EROFS report: Firecracker is a child of the daemon, so the snapshot
// directory must be in the daemon unit's writable set for ProtectSystem=strict.
// The "-" prefix keeps a missing directory from failing the unit outright.
func TestRenderManagerServiceAllowsSnapshotDirWrites(t *testing.T) {
	unit, err := renderManagerService(newTestInitConfig())
	require.NoError(t, err)

	paths := readWritePaths(t, string(unit))
	assert.Contains(t, paths, "-"+testSnapshotDir,
		"manager unit must allow writes to the snapshot dir (with the - prefix)")
	assert.Contains(t, paths, "/var/lib/firecracker/rootfs",
		"existing writable paths must survive")
}

// TestRenderWorkerServicePassesSnapshotDir mirrors the manager checks for the
// worker unit, which has its own template.
func TestRenderWorkerServicePassesSnapshotDir(t *testing.T) {
	for _, isManager := range []bool{true, false} {
		cfg := newTestJoinConfig()
		cfg.IsManager = isManager

		unit, err := renderWorkerService(cfg)
		require.NoError(t, err)

		exec := execStartLine(t, string(unit))
		assert.Contains(t, exec, "--snapshot-dir "+testSnapshotDir,
			"worker unit must pass --snapshot-dir to the daemon (isManager=%v)", isManager)
	}
}

// TestRenderWorkerServiceAllowsSnapshotDirWrites is the worker half of the
// EROFS regression test.
func TestRenderWorkerServiceAllowsSnapshotDirWrites(t *testing.T) {
	unit, err := renderWorkerService(newTestJoinConfig())
	require.NoError(t, err)

	paths := readWritePaths(t, string(unit))
	assert.Contains(t, paths, "-"+testSnapshotDir,
		"worker unit must allow writes to the snapshot dir (with the - prefix)")
	assert.Contains(t, paths, "/var/lib/firecracker/rootfs",
		"existing writable paths must survive")
}

// countPath returns how many times entry appears in paths, so a test can tell a
// duplicated ReadWritePaths entry from a single one.
func countPath(paths []string, entry string) int {
	count := 0
	for _, p := range paths {
		if p == entry {
			count++
		}
	}
	return count
}

// TestRenderManagerServiceKeepsDefaultSnapshotDirWritable is the regression test
// for a custom --snapshot-dir un-whitelisting the default path: node-local
// `vm snapshot create` writes into the CLI's snapshot.snapshot_dir, which
// defaults to config.DefaultSnapshotDir, so that path must stay in
// ReadWritePaths even when the daemon is pointed elsewhere.
func TestRenderManagerServiceKeepsDefaultSnapshotDirWritable(t *testing.T) {
	unit, err := renderManagerService(newTestInitConfig())
	require.NoError(t, err)

	paths := readWritePaths(t, string(unit))
	assert.Contains(t, paths, "-"+config.DefaultSnapshotDir,
		"the CLI's default snapshot dir must stay writable when --snapshot-dir is custom")
	assert.Contains(t, paths, "-"+testSnapshotDir,
		"the configured snapshot dir must be writable")
	assert.Equal(t, 1, countPath(paths, "-"+config.DefaultSnapshotDir),
		"the default snapshot dir must appear exactly once")
}

// TestRenderManagerServiceDeduplicatesDefaultSnapshotDir guards the default
// case: when --snapshot-dir is left alone the unit must not list the same path
// twice.
func TestRenderManagerServiceDeduplicatesDefaultSnapshotDir(t *testing.T) {
	cfg := newTestInitConfig()
	cfg.SnapshotDir = config.DefaultSnapshotDir

	unit, err := renderManagerService(cfg)
	require.NoError(t, err)

	paths := readWritePaths(t, string(unit))
	assert.Equal(t, 1, countPath(paths, "-"+config.DefaultSnapshotDir),
		"the snapshot dir must be listed once when it equals the default")
}

// TestRenderWorkerServiceKeepsDefaultSnapshotDirWritable is the worker half of
// the custom-directory check.
func TestRenderWorkerServiceKeepsDefaultSnapshotDirWritable(t *testing.T) {
	for _, isManager := range []bool{true, false} {
		cfg := newTestJoinConfig()
		cfg.IsManager = isManager

		unit, err := renderWorkerService(cfg)
		require.NoError(t, err)

		paths := readWritePaths(t, string(unit))
		assert.Contains(t, paths, "-"+config.DefaultSnapshotDir,
			"the CLI's default snapshot dir must stay writable when --snapshot-dir is custom (isManager=%v)", isManager)
		assert.Contains(t, paths, "-"+testSnapshotDir,
			"the configured snapshot dir must be writable (isManager=%v)", isManager)
		assert.Equal(t, 1, countPath(paths, "-"+config.DefaultSnapshotDir),
			"the default snapshot dir must appear exactly once (isManager=%v)", isManager)
	}
}

// TestRenderWorkerServiceDeduplicatesDefaultSnapshotDir is the worker half of
// the deduplication check.
func TestRenderWorkerServiceDeduplicatesDefaultSnapshotDir(t *testing.T) {
	cfg := newTestJoinConfig()
	cfg.SnapshotDir = config.DefaultSnapshotDir

	unit, err := renderWorkerService(cfg)
	require.NoError(t, err)

	paths := readWritePaths(t, string(unit))
	assert.Equal(t, 1, countPath(paths, "-"+config.DefaultSnapshotDir),
		"the snapshot dir must be listed once when it equals the default")
}

// TestInitCommandSnapshotDirFlag pins the flag and its default so the unit and
// the config cannot drift from the daemon's own default.
func TestInitCommandSnapshotDirFlag(t *testing.T) {
	assertSnapshotDirFlag(t, newInitCommand())
}

// TestJoinCommandSnapshotDirFlag is the join half of the flag check.
func TestJoinCommandSnapshotDirFlag(t *testing.T) {
	assertSnapshotDirFlag(t, newJoinCommand())
}

func assertSnapshotDirFlag(t *testing.T, cmd *cobra.Command) {
	t.Helper()

	flag := cmd.Flags().Lookup("snapshot-dir")
	require.NotNil(t, flag, "%s must expose --snapshot-dir", cmd.Name())
	assert.Equal(t, config.DefaultSnapshotDir, flag.DefValue,
		"%s --snapshot-dir must default to config.DefaultSnapshotDir", cmd.Name())
}

// TestInitDirectoriesIncludeSnapshotDir guards the pre-create step: the daemon
// creates the directory too, but the unit should not depend on that race.
func TestInitDirectoriesIncludeSnapshotDir(t *testing.T) {
	assert.Contains(t, initDirectories(newTestInitConfig()), testSnapshotDir)
}

// TestWorkerDirectoriesIncludeSnapshotDir is the join half of the pre-create
// check. join uses 0700 for its directories, so only membership is asserted.
func TestWorkerDirectoriesIncludeSnapshotDir(t *testing.T) {
	assert.Contains(t, workerDirectories(newTestJoinConfig()), testSnapshotDir)
}
