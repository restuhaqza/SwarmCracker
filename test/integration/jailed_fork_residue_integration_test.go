//go:build integration

package integration

// SBX-070 host evidence, the last gate: what a run leaves behind.
//
// A sandbox's residue is not the sandbox. A checkpoint's memory image is a store of
// 4 MiB chunks addressed by content, and every checkpoint of one image shares most of
// them, so the thing that has to be provable is not "the record is gone" but "the
// chunks nothing names any more are gone" — a prune that silently does nothing leaves
// a node that fills up one image at a time, and it looks exactly like a clean node
// until the disk is full.
//
// So this run counts. The chunk store is counted before the delete and after it, the
// checkpoint directory is listed after it, and then the host's own tables are asked
// about every name the run created: its namespaces, its cgroups, its jails, its
// mounts, its VMM processes, and finally its scratch directory.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// The names this run creates, under its own prefix so the audit is about this run
	// and not about anything else on the node.
	forkResidueParentID   = "fork-ev5"
	forkResidueClone      = "fork-ev5-c1"
	forkResidueCheckpoint = "sbx-fork-c5"
)

// TestIntegration_JailedForkResidue is the audit.
func TestIntegration_JailedForkResidue(t *testing.T) {
	requireSandboxHost(t)

	ctx := context.Background()
	node := newSliceNode(t)
	parent, chk, _ := forkParent(t, node, forkResidueParentID, forkResidueCheckpoint)
	clones, _ := forkTwins(t, node, chk, []string{forkResidueClone})
	clone := clones[0]

	// The image, in the store, before anything is forgotten.
	beforeFiles, beforeBytes := treeStats(t, node.blobRoot)
	t.Logf("chunk store %s with the checkpoint published: %d files, %d bytes (memory image %d bytes in %d chunks)",
		node.blobRoot, beforeFiles, beforeBytes, chk.Mem.TotalSize, len(chk.Mem.Chunks))
	require.Positive(t, beforeFiles, "the memory image is in the store as files")

	// The sandboxes go first: a sandbox owns no chunk, so this must not move them.
	for _, rec := range []struct{ id, jail string }{
		{clone.ID, clone.JailRoot},
		{parent.ID, parent.JailRoot},
	} {
		freed, err := node.manager.Remove(ctx, rec.id)
		require.NoError(t, err, "remove %s", rec.id)
		require.False(t, processAlivePID(freed.PID), "the VMM of %s is gone", rec.id)
		require.NoDirExists(t, rec.jail, "the jail of %s is gone", rec.id)
		require.NoDirExists(t, netnsPath(rec.id), "the namespace of %s is gone", rec.id)
		require.NoDirExists(t, cgroupPath(rec.id), "the cgroup of %s is gone", rec.id)
		t.Logf("%s removed: pid=%d netns=%v cgroup=%v jail=%v disk=%v",
			rec.id, freed.PID, freed.NetNS, freed.Cgroup, freed.JailRoot, freed.Disk)
	}

	// The delete that must reclaim the image: the last checkpoint of it is forgotten,
	// so every chunk it named is unreferenced and has to go.
	deletion, err := node.manager.DeleteCheckpoint(ctx, chk.Name)
	require.NoError(t, err, "delete the checkpoint")
	t.Logf("DeleteCheckpoint %s: state %d B, disk %d B, chunks %d (%d B), freed %d B, retained %d checkpoints / %d B",
		deletion.Name, deletion.StateBytes, deletion.DiskBytes,
		deletion.Chunks, deletion.ChunkBytes, deletion.FreedBytes,
		deletion.Retained, deletion.RetainedBytes)

	afterFiles, afterBytes := treeStats(t, node.blobRoot)
	t.Logf("chunk store after the delete: %d files, %d bytes (was %d / %d)", afterFiles, afterBytes, beforeFiles, beforeBytes)
	require.Zero(t, afterFiles, "the delete reclaimed every chunk: the store holds no file at all")

	// The checkpoint's own directory: the record, the state document, the disk copy and
	// the credential are one directory, and it is gone.
	require.Empty(t, dirNames(t, node.checkpoints.Dir()),
		"the checkpoint store holds no entry after the delete")
	require.NoDirExists(t, node.checkpoints.Path(chk.Name), "the checkpoint's own directory is gone")

	// And the host's own tables, asked about every name this run made.
	requireNoVMMNamed(t, []string{forkResidueParentID, forkResidueClone})
	requireNoMountUnder(t, node.scratch)
	t.Logf("no VMM, no mount and no checkpoint entry left for this run")

	// Finally the scratch directory, which holds the template, the labels, the jails'
	// logs and the state: removing it leaves the host as the run found it.
	require.NoError(t, os.RemoveAll(node.scratch))
	require.NoDirExists(t, node.scratch, "the run's scratch directory is gone")
	t.Logf("scratch %s removed; nothing of this run is left on the host", node.scratch)
}

// treeStats counts the non-directory entries under root and their bytes, which is what
// "the store holds nothing" has to mean: an empty directory is not a reclaimed chunk,
// and a store with files whose name nothing references is the leak this audit is for.
func treeStats(t *testing.T, root string) (files int, bytes int64) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files++
		bytes += info.Size()
		return nil
	})
	require.NoError(t, err, "walk %s", root)
	return files, bytes
}

// dirNames is a directory's entries, sorted, for a log line that says what is there
// rather than that something is.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		require.NoDirExists(t, dir, "read %s", dir)
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// requireNoVMMNamed scans the host's processes for one whose command line names any of
// names. A VMM outliving its sandbox is invisible in every table the manager keeps —
// its record is gone — so the process table is the place to look.
func requireNoVMMNamed(t *testing.T, names []string) {
	t.Helper()

	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	require.NoError(t, err)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		line := strings.ReplaceAll(string(cmdline), "\x00", " ")
		for _, name := range names {
			if strings.Contains(line, name) {
				require.Failf(t, "a VMM outlived its sandbox",
					"pid %d still names %s: %s", pid, name, strings.TrimSpace(line))
			}
		}
	}
}

// requireNoMountUnder refuses a mount that mentions dir: a jail's disk left mounted is
// the residue that hides behind an unmounted-looking tree.
func requireNoMountUnder(t *testing.T, dir string) {
	t.Helper()

	mounts, err := os.ReadFile("/proc/mounts")
	require.NoError(t, err)
	for _, line := range strings.Split(string(mounts), "\n") {
		require.NotContains(t, line, dir, "no mount is left under %s: %s", dir, line)
	}
}

// netnsPath is where a sandbox's namespace lives, spelled the way the manager spells
// it rather than the way this file would.
func netnsPath(id string) string { return "/var/run/netns/sbx-" + id }

// cgroupPath is where the jailer creates a sandbox's cgroup.
func cgroupPath(id string) string { return filepath.Join("/sys/fs/cgroup", checkpointCgroup, id) }

// processAlivePID reports whether a pid from a teardown report is still a live process.
func processAlivePID(pid int) bool {
	if pid <= 0 {
		return false
	}
	return processAlive(pid)
}
