//go:build integration

package integration

// SBX-070 host evidence, step two: one fork of a live sandbox.
//
// What this file proves, and only this — four things, all of them cheap in the
// same run:
//
//	a. the clone is ready: its guest agent answered, which is this package's
//	   readiness rule, and its record says so;
//	b. the clone is a machine of its own while its parent keeps running: its own
//	   network namespace, link address, guest address, disk and jail — and a parent
//	   that is still ready, still answering, and does not have the clone's writes;
//	c. the clone's writers run again *after* ready. That is the property the
//	   release in the post-restore hook exists for (SBX-070: `prepare` thaws before
//	   it writes), and "it exists" is not "it works": a write is issued into the
//	   clone under a deadline, and a filesystem still carrying the checkpoint's
//	   freeze would block that writer until the deadline instead of failing fast;
//	d. the clone's disk is clean: e2fsck reports no error on it, and the files the
//	   clone wrote are on it exactly once — with the file it inherited from its
//	   parent intact — read back first from the guest and then from the image with
//	   the VM gone.
//
// It stops there. The negative control (a fork with no release, which must fail or
// hang), two clones at once, a fork from a stored checkpoint, and the zero-leftover
// audit are the next turns.
//
// Prerequisites: the same host and the same unmodified build of
// recipes/sandbox-base.yaml as jailed_checkpoint_integration_test.go stages (see
// that file for the build command), plus e2fsck and mount, because the disk half
// reads the clone's image after the VMM is gone:
//
//	SWARMCRACKER_TEST_TEMPLATE=/var/tmp/sbx-fork-golden/golden-sandbox-base-1.0.0.ext4 \
//	go test -tags=integration -run JailedFork -v ./test/integration/
//
// SWARMCRACKER_TEST_SCRATCH moves the working directory (default /var/tmp), and
// SWARMCRACKER_TEST_KEEP=1 keeps it after a run, where the jail's logs, the VMM's
// own log and the guest's console are worth reading.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	sandboxpkg "github.com/restuhaqza/swarmcracker/pkg/sandbox"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

const (
	// forkParentID and forkCloneID are the two sandboxes this run owns. Their
	// namespaces are sbx-fork-ev2 and sbx-fork-ev2-c1, and their records, disks,
	// jails and cgroups all carry those names.
	forkParentID = "fork-ev2"
	forkCloneID  = "fork-ev2-c1"
	// forkCheckpointName is the checkpoint taken of the live parent, which is what
	// the clone is resumed from.
	forkCheckpointName = "sbx-fork-c2"

	// forkInheritedFile is written by the parent before the checkpoint, so the
	// clone inherits it through the image; forkCloneFile and forkWritersLog are
	// written by the clone after it is ready.
	forkInheritedFile = "/workspace/fork-parent.txt"
	forkCloneFile     = "/workspace/fork-clone.bin"
	forkWritersLog    = "/workspace/fork-writers.log"

	// forkInheritedBytes and forkCloneBytes are the payload sizes: one small enough
	// to travel through the checkpoint's images in a single write, one large
	// enough to span several frames of the agent's file protocol.
	forkInheritedBytes = 64 << 10
	forkCloneBytes     = 1 << 20

	// forkWriterLines is how many lines a guest process appends to the log, one line
	// per whole write, each distinct: a lost write shows as a short log and a
	// doubled one as a long log or as a repeated line.
	forkWriterLines = 24

	// forkWriteDeadline bounds the writes the clone does after ready. A frozen
	// filesystem blocks its writers instead of failing them, so the deadline is
	// what turns "the release did not happen" into a failure with a sentence
	// rather than a hung run.
	forkWriteDeadline = 90 * time.Second
)

// TestIntegration_JailedFork is the run: create a sandbox, write a file into it,
// checkpoint it, fork it, and then hold the clone and its disk to what a clone
// must be.
func TestIntegration_JailedFork(t *testing.T) {
	requireSandboxHost(t)

	ctx := context.Background()
	node := newSliceNode(t)

	// a. The live parent, and the file the clone has to inherit from it.
	parent, err := node.manager.Create(ctx, sandboxpkg.CreateRequest{
		Template: node.templateRef,
		ID:       forkParentID,
		VCPUs:    checkpointVCPUs,
		MemoryMB: checkpointMemoryMB,
		TTL:      -1,
	})
	require.NoError(t, err, "create the sandbox the fork comes from")
	t.Cleanup(func() {
		if _, err := node.manager.Remove(context.Background(), parent.ID); err != nil {
			t.Logf("removing the parent %s after the run: %v", parent.ID, err)
		}
	})
	parentGuest := dialSandboxGuest(t, parent)

	inherited := deterministicBytes(forkInheritedBytes)
	require.NoError(t, parentGuest.CopyTo(ctx, forkInheritedFile, bytes.NewReader(inherited), 0o644),
		"write the file the clone has to inherit")
	t.Logf("parent ready: id=%s netns=%s mac=%s ip=%s disk=%s",
		parent.ID, parent.NetNS, parent.MAC, parent.GuestIP, parent.Disk)

	// The checkpoint of the live sandbox, and then the fork of it. The two are
	// timed apart because the question this run answers is how long a fork takes,
	// not how long a checkpoint does.
	checkpointStart := time.Now()
	chk, err := node.manager.Checkpoint(ctx, parent.ID, forkCheckpointName)
	require.NoError(t, err, "checkpoint the live sandbox")
	t.Logf("checkpoint %s of the live sandbox in %s: %d bytes, memory %d in %d chunks, disk copy %d",
		chk.Name, time.Since(checkpointStart).Round(time.Millisecond), chk.SizeBytes,
		chk.Mem.TotalSize, len(chk.Mem.Chunks), fileSize(t, chk.Disk))
	t.Cleanup(func() {
		if _, err := node.manager.DeleteCheckpoint(context.Background(), forkCheckpointName); err != nil {
			t.Logf("deleting checkpoint %s after the run: %v", forkCheckpointName, err)
		}
	})

	forkStart := time.Now()
	forks, err := node.manager.Fork(ctx, sandboxpkg.ForkRequest{
		Checkpoint: chk.Name,
		IDs:        []string{forkCloneID},
		TTL:        -1,
	})
	forkReady := time.Since(forkStart)
	require.NoError(t, err, "fork the live sandbox from its checkpoint")
	require.Len(t, forks, 1)
	clone := forks[0]
	t.Cleanup(func() {
		if _, err := node.manager.Remove(context.Background(), clone.ID); err != nil {
			t.Logf("removing the clone %s after the run: %v", clone.ID, err)
		}
	})
	t.Logf("fork ready: id=%s state=%s forked_from=%s netns=%s mac=%s ip=%s disk=%s copy=%s in %s",
		clone.ID, clone.State, clone.ForkedFrom, clone.NetNS, clone.MAC, clone.GuestIP,
		clone.Disk, clone.DiskCopy, forkReady.Round(time.Millisecond))

	// a. Ready means the clone's guest agent answered, not that a VMM started.
	require.Equal(t, sandboxpkg.StateReady, clone.State, "the clone is ready: its guest agent answered")
	require.Equal(t, forkCheckpointName, clone.ForkedFrom, "the clone names the checkpoint it came from")

	// The clone authenticates with a credential of its own. The image it was frozen
	// from carries the parent's, which is what the resumed guest accepts until its
	// post-restore hook runs; the host then publishes the clone's own beside the
	// clone's muxer, and that is the file every dial below uses. A clone that kept its
	// parent's token would be one credential shared by every clone of the image.
	cloneToken, found, err := agent.ReadTokenFile(clone.AgentTokenFile)
	require.NoError(t, err, "read the credential the node published for the clone")
	require.True(t, found, "the clone's own credential is published beside its muxer")
	require.NotEqual(t, chk.Token, cloneToken, "the clone does not authenticate with the token its image froze")

	// b. A machine of its own, beside a parent that is still running.
	require.NotEqual(t, parent.ID, clone.ID)
	require.NotEqual(t, parent.NetNS, clone.NetNS, "the clone has a network namespace of its own")
	require.NotEqual(t, parent.MAC, clone.MAC, "the clone has a link address of its own")
	require.NotEqual(t, parent.GuestIP, clone.GuestIP, "the clone has a guest address of its own")
	require.NotEqual(t, parent.Disk, clone.Disk, "the clone writes a disk of its own")
	require.NotEqual(t, parent.JailRoot, clone.JailRoot, "the clone runs in a jail of its own")
	// The TAP is deliberately the same name in both: the device is part of the state
	// a checkpoint freezes, so it is fixed per namespace rather than per sandbox.
	require.Equal(t, parent.TAP, clone.TAP, "each sandbox owns a tap0, in its own namespace")
	t.Logf("parent vs clone: netns %s vs %s, mac %s vs %s, ip %s vs %s",
		parent.NetNS, clone.NetNS, parent.MAC, clone.MAC, parent.GuestIP, clone.GuestIP)

	parentNow, err := node.manager.Get(parent.ID)
	require.NoError(t, err)
	require.Equal(t, sandboxpkg.StateReady, parentNow.State, "the parent is still ready after the fork")
	parentPID := instancePID(t, node, parent.ID)
	requireAlive(t, parentPID, "the parent's VMM")
	t.Logf("parent vmm pid %d is alive after the fork", parentPID)

	// c. The clone's writers run again after ready.
	cloneGuest := dialSandboxGuest(t, clone)
	cloneBody := clonePayload(inherited, forkCloneBytes)
	writersLog := wantWritersLog(forkWriterLines)
	writeDeadline, cancel := context.WithTimeout(ctx, forkWriteDeadline)
	defer cancel()

	writeStart := time.Now()
	require.NoError(t, cloneGuest.CopyTo(writeDeadline, forkCloneFile, bytes.NewReader(cloneBody), 0o644),
		"a write from the host into the clone's filesystem, after ready: a frozen filesystem would block it")
	// And a process inside the guest appending line by line, which is the writer the
	// post-restore hook exists to release: it blocks on its first write if the
	// filesystem is still frozen.
	execResult, err := cloneGuest.ExecStatus(writeDeadline, []string{"/bin/sh", "-c", fmt.Sprintf(
		`i=1; while [ $i -le %d ]; do printf 'line-%%02d\n' "$i" >> %s; i=$((i+1)); done`,
		forkWriterLines, forkWritersLog)}, agent.ExecOptions{})
	require.NoError(t, err, "a guest process writing after ready")
	require.Zero(t, execResult.ExitCode, "the guest process that wrote after ready exited cleanly")
	t.Logf("clone writers ran after ready in %s: %d bytes copied in, %d lines appended in the guest",
		time.Since(writeStart).Round(time.Millisecond), len(cloneBody), forkWriterLines)

	// What the clone holds, read back in the guest: the inherited file intact, its
	// own file exactly what was written, and the log complete and in order.
	require.Equal(t, sha256Hex(inherited), sha256Hex(readGuestFile(t, cloneGuest, forkInheritedFile)),
		"the clone inherited its parent's file through the checkpoint, byte for byte")
	require.Equal(t, sha256Hex(cloneBody), sha256Hex(readGuestFile(t, cloneGuest, forkCloneFile)),
		"the clone's own file is exactly what was written into it")
	require.Equal(t, writersLog, string(readGuestFile(t, cloneGuest, forkWritersLog)),
		"every line the guest wrote is there exactly once, in order")

	// And they are the clone's alone: the parent, still running, has neither file.
	_, statErr := parentGuest.Stat(ctx, forkCloneFile)
	require.Error(t, statErr, "the parent does not have the clone's file: the clone's writes did not land in its disk")
	_, statErr = parentGuest.Stat(ctx, forkWritersLog)
	require.Error(t, statErr, "the parent does not have the clone's writers log either")

	// d. The disk, once the guest is not holding it.
	//
	// The guest is asked first to put its filesystem where a clean shutdown leaves
	// it: every write flushed, the superblock marked clean. It is asked because the
	// alternative — stopping the VMM under a running guest — leaves a journal that
	// someone has to replay, and an e2fsck over *that* would be reporting an
	// interruption rather than anything about the fork: it would say "free blocks
	// count wrong, fix? no" about any VM that was ever killed. The check below is
	// therefore about the filesystem the clone wrote, and the guest's own answer is
	// what makes it one.
	var guestSaid bytes.Buffer
	cleanResult, err := cloneGuest.ExecStatus(ctx, []string{"/bin/sh", "-c", "sync && mount -o remount,ro / && findmnt -no OPTIONS /"},
		agent.ExecOptions{Stdout: &guestSaid, Stderr: &guestSaid})
	require.NoError(t, err, "flush the clone's filesystem and leave it clean")
	require.Zero(t, cleanResult.ExitCode, "the guest put its filesystem in a clean state: %s", strings.TrimSpace(guestSaid.String()))
	t.Logf("clone filesystem before the VMM stopped: %s", strings.TrimSpace(guestSaid.String()))

	disk := filepath.Join(clone.JailRoot, "root", "drives", "rootfs.ext4")
	stopCloneVMM(t, node, clone.ID)
	fsckClean(t, disk)
	requireCloneDisk(t, disk, inherited, cloneBody, writersLog)

	// The parent is still there, after all of it — and it is asked over a fresh
	// connection, so the answer is the guest's now rather than a socket this run
	// opened before the fork and has since been idle on.
	parentNow, err = node.manager.Get(parent.ID)
	require.NoError(t, err)
	require.Equal(t, sandboxpkg.StateReady, parentNow.State, "the parent is still ready at the end of the run")
	requireAlive(t, parentPID, "the parent's VMM")
	parentAgain := dialSandboxGuest(t, parentNow)
	require.Equal(t, sha256Hex(inherited), sha256Hex(readGuestFile(t, parentAgain, forkInheritedFile)),
		"the parent's own file is untouched by the fork")
}

// fileSize is one file's length, for a log line that states what is on disk rather
// than what a record claims.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err, "stat %s", path)
	return info.Size()
}

// dialSandboxGuest dials a sandbox's guest agent with the credential the node
// published for it. A record's muxer and token file are what a reader resolves; a
// record cannot be dialled without them, which is why they are written before a
// sandbox says ready.
func dialSandboxGuest(t *testing.T, rec sandboxpkg.Record) *agent.Client {
	t.Helper()

	token, found, err := agent.ReadTokenFile(rec.AgentTokenFile)
	require.NoError(t, err, "read sandbox %s's agent token", rec.ID)
	require.True(t, found, "sandbox %s published the credential its guest requires", rec.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	guest, err := agent.DialWithToken(ctx, rec.VsockPath, agent.DefaultPort, token)
	require.NoError(t, err, "dial sandbox %s's guest agent at %s", rec.ID, rec.VsockPath)
	t.Cleanup(func() { _ = guest.Close() })
	return guest
}

// readGuestFile reads one file out of a guest.
func readGuestFile(t *testing.T, guest *agent.Client, path string) []byte {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var body bytes.Buffer
	require.NoError(t, guest.CopyFrom(ctx, path, &body), "read %s out of the guest", path)
	return body.Bytes()
}

// clonePayload derives the clone's payload from the file it inherited, byte by
// byte, so a run that confused the two files — or that handed back the parent's
// bytes for the clone's — fails the comparison instead of passing on a shape both
// share.
func clonePayload(inherited []byte, size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = inherited[i%len(inherited)] ^ byte(i)
	}
	return out
}

// wantWritersLog is the log the guest's loop must produce: every line, once, in
// order.
func wantWritersLog(lines int) string {
	var log strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&log, "line-%02d\n", i)
	}
	return log.String()
}

// instancePID is the pid of a sandbox's VMM, as the node's registry published it.
func instancePID(t *testing.T, node *sliceNode, id string) int {
	t.Helper()

	inst, found, err := node.instances.Peek(id)
	require.NoError(t, err, "read instance %s's registry record", id)
	require.True(t, found, "instance %s is in the node's registry", id)
	require.Positive(t, inst.PID, "instance %s records the pid of its VMM", id)
	return inst.PID
}

// requireAlive refuses a pid that is not a live process.
func requireAlive(t *testing.T, pid int, what string) {
	t.Helper()

	require.NoError(t, syscall.Kill(pid, 0), "%s (pid %d) is alive", what, pid)
}

// stopCloneVMM stops the clone's VMM the way a teardown would, and leaves its jail
// and disk in place: the disk is what the fsck and the file checks read next.
func stopCloneVMM(t *testing.T, node *sliceNode, id string) {
	t.Helper()

	pid := instancePID(t, node, id)
	proc, err := os.FindProcess(pid)
	require.NoError(t, err)
	require.NoError(t, snapshot.StopProcess(proc), "stop the clone's VMM (pid %d)", pid)
	require.False(t, processAlive(pid), "the clone's VMM is gone")
	t.Logf("clone vmm pid %d stopped; its disk is left in place for the check", pid)
}

// processAlive reports whether pid is a live process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// fsckClean runs e2fsck over the image without writing to it and refuses any
// reported error. It is the disk's own answer about the filesystem the guest was
// writing, asked after the guest is gone.
func fsckClean(t *testing.T, disk string) {
	t.Helper()

	require.FileExists(t, disk, "the clone's jail holds the disk its guest wrote")
	out, err := exec.Command("e2fsck", "-fn", disk).CombinedOutput()
	t.Logf("e2fsck -fn %s:\n%s", disk, strings.TrimSpace(string(out)))
	require.NoError(t, err, "e2fsck reports no error on the clone's disk (exit codes above 3 mean it would have changed the filesystem)")
}

// requireCloneDisk reads the three files this run wrote out of the clone's image,
// with the VM gone, and holds them to what the guest said they were: the file it
// inherited, its own payload, and the log — complete, in order, and written once.
func requireCloneDisk(t *testing.T, disk string, inherited, cloneBody []byte, writersLog string) {
	t.Helper()

	mount := t.TempDir()
	out, err := exec.Command("mount", "-o", "loop,ro", disk, mount).CombinedOutput()
	require.NoError(t, err, "mount the clone's disk read-only: %s", out)
	defer func() {
		if out, err := exec.Command("umount", mount).CombinedOutput(); err != nil {
			t.Logf("unmount %s: %v: %s", mount, err, out)
		}
	}()

	onDisk := func(path string) []byte {
		body, err := os.ReadFile(filepath.Join(mount, strings.TrimPrefix(path, "/")))
		require.NoError(t, err, "read %s off the clone's disk", path)
		return body
	}

	require.Equal(t, sha256Hex(inherited), sha256Hex(onDisk(forkInheritedFile)),
		"the file the clone inherited is on its disk, unchanged")
	require.Equal(t, sha256Hex(cloneBody), sha256Hex(onDisk(forkCloneFile)),
		"the clone's own file is on its disk, byte for byte")
	require.Equal(t, writersLog, string(onDisk(forkWritersLog)),
		"the log on the clone's disk has every line once: no write lost, none doubled")
}
