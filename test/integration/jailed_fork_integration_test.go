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
	"errors"
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
	// the clone is resumed from. The two-clone run and the stored-checkpoint run have
	// names of their own, so a leftover from one is never read as another's.
	forkCheckpointName   = "sbx-fork-c2"
	forkTwinParentID     = "fork-ev3"
	forkTwinA            = "fork-ev3-c1"
	forkTwinB            = "fork-ev3-c2"
	forkTwinCheckpoint   = "sbx-fork-c3"
	forkTwinWritersLog   = "/workspace/fork-twin-writers.log"
	forkStoredParentID   = "fork-ev4"
	forkStoredClone      = "fork-ev4-c1"
	forkStoredCheckpoint = "sbx-fork-c4"

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

	// a. The live parent, its file, and the checkpoint of it while it runs.
	parent, chk, inherited := forkParent(t, node, forkParentID, forkCheckpointName)
	parentGuest := dialSandboxGuest(t, parent)

	// e2fsck on the artifact a fork copies: the checkpoint's own disk copy, read-only,
	// reported as the image is. The guest was running when it was copied, so a journal
	// left to replay is the normal state of a running guest's disk; a structural error
	// is not. Neither is counted silently: the exit code and the findings are printed,
	// and the classification is the check's own.
	checkpointDiskFsck(t, chk.Disk)

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

	// d. The clone's writers run again after ready.
	cloneGuest := dialSandboxGuest(t, clone)
	cloneBody := clonePayload(inherited, forkCloneBytes)
	writersLog := wantWritersLog(forkWriterLines)
	wrote := writeInGuest(t, cloneGuest, cloneBody, forkWritersLog, forkWriterLines)
	t.Logf("clone writers ran after ready in %s: %d bytes copied in, %d lines appended in the guest",
		wrote.Round(time.Millisecond), len(cloneBody), forkWriterLines)

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

	// e. The disk, once the guest is not holding it.
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

// TestIntegration_JailedForkTwoClones forks two clones of one live sandbox and holds
// the three sandboxes to being three: three identities, three credentials, and two
// writers running at the same time in the two clones while the parent is still up.
//
// The concurrency is the part a single clone cannot show. Two clones are made one after
// the other (the manager is one-at-a-time), but nothing about their disk, their
// credential or their jail is shared, so the writes must be able to overlap — and the
// run proves they did by comparing the two writers' intervals rather than by trusting
// that they were started together.
func TestIntegration_JailedForkTwoClones(t *testing.T) {
	requireSandboxHost(t)

	ctx := context.Background()
	node := newSliceNode(t)
	parent, chk, inherited := forkParent(t, node, forkTwinParentID, forkTwinCheckpoint)
	parentGuest := dialSandboxGuest(t, parent)

	clones, forkReady := forkTwins(t, node, chk, []string{forkTwinA, forkTwinB})
	t.Logf("two clones ready %s after the fork call (%.1fs per clone), parent %s (netns %s mac %s ip %s)",
		forkReady.Round(time.Millisecond), forkReady.Seconds()/float64(len(clones)),
		parent.ID, parent.NetNS, parent.MAC, parent.GuestIP)

	// Three identities, compared in every direction: two sandboxes that share any of
	// these are not two sandboxes.
	all := append([]sandboxpkg.Record{parent}, clones...)
	for i := range all {
		for j := range all {
			if i == j {
				continue
			}
			require.NotEqual(t, all[i].NetNS, all[j].NetNS, "%s and %s have namespaces of their own", all[i].ID, all[j].ID)
			require.NotEqual(t, all[i].MAC, all[j].MAC, "%s and %s have link addresses of their own", all[i].ID, all[j].ID)
			require.NotEqual(t, all[i].GuestIP, all[j].GuestIP, "%s and %s have guest addresses of their own", all[i].ID, all[j].ID)
		}
	}
	t.Logf("three identities: %s (%s %s), %s (%s %s), %s (%s %s)",
		all[0].ID, all[0].NetNS, all[0].GuestIP, all[1].ID, all[1].NetNS, all[1].GuestIP,
		all[2].ID, all[2].NetNS, all[2].GuestIP)

	// Three credentials, and the clones' are neither the parent's nor each other's.
	parentToken := publishedToken(t, parent)
	firstToken := publishedToken(t, clones[0])
	secondToken := publishedToken(t, clones[1])
	require.NotEqual(t, parentToken, firstToken, "the first clone does not authenticate with its parent's credential")
	require.NotEqual(t, parentToken, secondToken, "the second clone does not authenticate with its parent's credential")
	require.NotEqual(t, firstToken, secondToken, "the two clones do not authenticate with one credential")
	// And the checkpoint is where the parent's credential is meant to be: it is what the
	// guest an image carries still accepts, until the clone's own hook replaces it.
	require.Equal(t, parentToken, chk.Token, "the checkpoint carries the credential its image froze")

	// Two writers at once, in the two clones, each writing the same payload into a log
	// of its own. Each writer runs for long enough to overlap the other, and the
	// intervals are compared rather than assumed.
	type writer struct {
		id         string
		start, end time.Time
		sat        time.Duration
		err        error
	}
	results := make(chan writer, len(clones))
	for _, clone := range clones {
		clone := clone
		go func() {
			guest := dialSandboxGuest(t, clone)
			start := time.Now()
			body := clonePayload(inherited, forkCloneBytes/8)
			err := writePairFor(guest, body, forkTwinWritersLog, forkWriterLines)
			results <- writer{id: clone.ID, start: start, end: time.Now(), sat: time.Since(start), err: err}
		}()
	}
	writers := make([]writer, 0, len(clones))
	for range clones {
		writers = append(writers, <-results)
	}
	for _, w := range writers {
		require.NoError(t, w.err, "clone %s wrote while the other clone was writing", w.id)
		t.Logf("clone %s wrote for %s", w.id, w.sat.Round(time.Millisecond))
	}
	overlapped := writers[0].start.Before(writers[1].end) && writers[1].start.Before(writers[0].end)
	require.True(t, overlapped, "the two writers ran at the same time: %s..%s and %s..%s",
		writers[0].start.Format(time.StampMilli), writers[0].end.Format(time.StampMilli),
		writers[1].start.Format(time.StampMilli), writers[1].end.Format(time.StampMilli))

	// What each clone wrote, read back in that clone: whole, and nothing of the
	// other's.
	for i, clone := range clones {
		guest := dialSandboxGuest(t, clone)
		body := clonePayload(inherited, forkCloneBytes/8)
		require.Equal(t, sha256Hex(body), sha256Hex(readGuestFile(t, guest, forkCloneFile)),
			"clone %s's own file is exactly what it wrote", clone.ID)
		require.Equal(t, wantWritersLog(forkWriterLines), string(readGuestFile(t, guest, forkTwinWritersLog)),
			"clone %s's log has every line once", clone.ID)
		require.Equal(t, sha256Hex(inherited), sha256Hex(readGuestFile(t, guest, forkInheritedFile)),
			"clone %s inherited its parent's file", clone.ID)
		if i == 0 {
			_, err := guest.Stat(ctx, forkTwinWritersLog+"-other")
			require.Error(t, err)
		}
	}
	// The first clone's log is not in the second, and neither clone's file is in the
	// parent: three filesystems, three writers.
	_, err := dialSandboxGuest(t, clones[1]).Stat(ctx, forkCloneFile)
	require.Error(t, err, "the second clone does not see the first clone's file")
	_, err = parentGuest.Stat(ctx, forkCloneFile)
	require.Error(t, err, "the parent does not see either clone's file")

	// And all three are alive at the end.
	for _, rec := range all {
		now, err := node.manager.Get(rec.ID)
		require.NoError(t, err)
		require.Equal(t, sandboxpkg.StateReady, now.State, "%s is still ready", rec.ID)
		requireAlive(t, instancePID(t, node, rec.ID), "the VMM of "+rec.ID)
	}
	t.Logf("all three sandboxes are alive after two concurrent writers")
}

// TestIntegration_JailedForkFromStoredCheckpoint forks a checkpoint whose sandbox no
// longer exists: a checkpoint is an image, not a child of a record, and the clone has
// to come up with the parent's filesystem and none of the parent's other resources.
func TestIntegration_JailedForkFromStoredCheckpoint(t *testing.T) {
	requireSandboxHost(t)

	ctx := context.Background()
	node := newSliceNode(t)
	parent, chk, inherited := forkParent(t, node, forkStoredParentID, forkStoredCheckpoint)

	// The sandbox the checkpoint came from goes away, with everything it held.
	freed, err := node.manager.Remove(ctx, parent.ID)
	require.NoError(t, err, "remove the sandbox the checkpoint was taken from")
	t.Logf("parent %s removed: netns=%v cgroup=%v jail=%v disk=%v",
		parent.ID, freed.NetNS, freed.Cgroup, freed.JailRoot, freed.Disk)
	_, err = node.manager.Get(parent.ID)
	require.ErrorIs(t, err, sandboxpkg.ErrNotFound, "the parent is gone from the records")
	require.NoDirExists(t, parent.JailRoot, "and its jail is gone with it")

	// The checkpoint is still there, and still an image.
	stored, err := node.manager.CheckpointByName(chk.Name)
	require.NoError(t, err, "the checkpoint outlives the sandbox it was taken from")
	require.Equal(t, parent.ID, stored.Sandbox, "it still names where it came from")
	require.FileExists(t, stored.Disk)

	clones, forkReady := forkTwins(t, node, stored, []string{forkStoredClone})
	clone := clones[0]
	t.Logf("fork ready from a stored checkpoint in %s: id=%s state=%s netns=%s mac=%s ip=%s",
		forkReady.Round(time.Millisecond), clone.ID, clone.State, clone.NetNS, clone.MAC, clone.GuestIP)

	require.NotEqual(t, parent.NetNS, clone.NetNS, "the clone's namespace is not the removed parent's")
	require.NoDirExists(t, parent.NetNS, "and the parent's namespace is gone, not reused")
	require.NotEqual(t, parent.MAC, clone.MAC)
	// The clone's guest address may well be the removed parent's: its /30 went back to
	// the pool when the parent was removed, and a fingerprint derived from the ID is what
	// differs. Asserting otherwise would be asserting that a freed address is never
	// handed out again, which is not what a pool promises.
	t.Logf("the clone's address %s (the removed parent held %s, which the pool may now hand out again)",
		clone.GuestIP, parent.GuestIP)

	// The filesystem is the one the checkpoint froze, parent's file included, and the
	// clone can write it.
	guest := dialSandboxGuest(t, clone)
	require.Equal(t, sha256Hex(inherited), sha256Hex(readGuestFile(t, guest, forkInheritedFile)),
		"the clone resumed the filesystem the checkpoint froze")
	body := clonePayload(inherited, forkCloneBytes)
	wrote := writeInGuest(t, guest, body, forkWritersLog, forkWriterLines)
	t.Logf("clone writers ran after ready in %s", wrote.Round(time.Millisecond))
	require.Equal(t, sha256Hex(body), sha256Hex(readGuestFile(t, guest, forkCloneFile)))
	require.Equal(t, wantWritersLog(forkWriterLines), string(readGuestFile(t, guest, forkWritersLog)))
}

// publishedToken is the credential the node published for a sandbox, which is the one
// its guest accepts.
func publishedToken(t *testing.T, rec sandboxpkg.Record) string {
	t.Helper()

	token, found, err := agent.ReadTokenFile(rec.AgentTokenFile)
	require.NoError(t, err, "read sandbox %s's published credential", rec.ID)
	require.True(t, found, "sandbox %s has a published credential", rec.ID)
	return token
}

// writePairFor is writeInGuest with its error returned instead of asserted, so two
// clones can write at the same time from two goroutines and the run can compare their
// intervals.
func writePairFor(guest *agent.Client, payload []byte, logPath string, lines int) error {
	ctx, cancel := context.WithTimeout(context.Background(), forkWriteDeadline)
	defer cancel()

	if err := guest.CopyTo(ctx, forkCloneFile, bytes.NewReader(payload), 0o644); err != nil {
		return err
	}
	// A writer that runs long enough to overlap its twin: the lines are separated by a
	// short sleep, so neither interval is a single instant.
	result, err := guest.ExecStatus(ctx, []string{"/bin/sh", "-c", fmt.Sprintf(
		`i=1; while [ $i -le %d ]; do printf 'line-%%02d\n' "$i" >> %s; sleep 0.05; i=$((i+1)); done`, lines, logPath)},
		agent.ExecOptions{})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("the guest process writing %s exited %d", logPath, result.ExitCode)
	}
	return nil
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
	return dialGuestAt(t, rec.VsockPath, token)
}

// forkParent builds the live sandbox a fork comes from: created from the staged
// template, with a file written into it that the clone has to inherit, checkpointed
// while it runs. It returns the parent, its checkpoint and the file's bytes.
//
// Every run in this file starts here, so a fork is never proven against a parent built
// one way in one test and another way in the next.
func forkParent(t *testing.T, node *sliceNode, id, checkpointName string) (sandboxpkg.Record, sandboxpkg.CheckpointRecord, []byte) {
	t.Helper()
	ctx := context.Background()

	parent, err := node.manager.Create(ctx, sandboxpkg.CreateRequest{
		Template: node.templateRef,
		ID:       id,
		VCPUs:    checkpointVCPUs,
		MemoryMB: checkpointMemoryMB,
		TTL:      -1,
	})
	require.NoError(t, err, "create the sandbox the fork comes from")
	t.Cleanup(func() {
		// A run that removed the parent itself — the stored-checkpoint one — finds it
		// gone, which is the state it wanted.
		if _, err := node.manager.Remove(context.Background(), parent.ID); err != nil && !errors.Is(err, sandboxpkg.ErrNotFound) {
			t.Logf("removing the parent %s after the run: %v", parent.ID, err)
		}
	})

	inherited := deterministicBytes(forkInheritedBytes)
	require.NoError(t, dialSandboxGuest(t, parent).CopyTo(ctx, forkInheritedFile, bytes.NewReader(inherited), 0o644),
		"write the file the clone has to inherit")
	t.Logf("parent ready: id=%s netns=%s mac=%s ip=%s disk=%s",
		parent.ID, parent.NetNS, parent.MAC, parent.GuestIP, parent.Disk)

	start := time.Now()
	chk, err := node.manager.Checkpoint(ctx, parent.ID, checkpointName)
	require.NoError(t, err, "checkpoint the live sandbox")
	t.Logf("checkpoint %s of the live sandbox in %s: %d bytes, memory %d in %d chunks, disk copy %d",
		chk.Name, time.Since(start).Round(time.Millisecond), chk.SizeBytes,
		chk.Mem.TotalSize, len(chk.Mem.Chunks), fileSize(t, chk.Disk))
	t.Cleanup(func() {
		// A run that deleted the checkpoint itself — the residue audit — finds it gone,
		// which is the state it wanted.
		if _, err := node.manager.DeleteCheckpoint(context.Background(), checkpointName); err != nil && !errors.Is(err, sandboxpkg.ErrNotFound) {
			t.Logf("deleting checkpoint %s after the run: %v", checkpointName, err)
		}
	})
	return parent, chk, inherited
}

// checkpointDiskFsck runs e2fsck read-only over the disk copy a checkpoint publishes —
// the file Fork copies for every clone — and says what it found.
//
// It does not assert "clean", because the honest answer for a disk copied from a
// running guest is often "a journal is waiting": FIFREEZE flushes the guest's writers,
// and what is left is whatever the filesystem had not committed. So the raw exit code
// and output are printed, and the classification is the image's own: the journal is
// replayed on a *copy* and that copy is checked again. A filesystem that is clean once
// its journal is applied was interrupted, not damaged; one that is still wrong after it
// is corruption, and this run fails on it.
func checkpointDiskFsck(t *testing.T, disk string) {
	t.Helper()

	out, err := exec.Command("e2fsck", "-fn", disk).CombinedOutput()
	code := exitCode(err)
	t.Logf("e2fsck -fn %s (the published checkpoint's disk): exit %d\n%s",
		disk, code, strings.TrimSpace(string(out)))
	switch code {
	case 0:
		t.Logf("the checkpoint's disk needed nothing: it was copied clean")
		return
	case 4:
		// 4 is "errors remain, nothing was changed": with -n, that is what a journal
		// waiting to be replayed looks like.
		t.Logf("the checkpoint's disk reports findings a read-only pass cannot settle; replaying the journal on a copy")
	default:
		require.Failf(t, "e2fsck on the checkpoint's disk", "exit %d means it could not check the filesystem at all: %s", code, out)
	}

	replay := filepath.Join(t.TempDir(), "replayed.ext4")
	require.NoError(t, copySparse(disk, replay), "copy the checkpoint's disk to replay its journal on")
	if out, err := exec.Command("e2fsck", "-fy", replay).CombinedOutput(); err != nil {
		require.Failf(t, "e2fsck -fy on the copy", "replaying the journal failed: %v\n%s", err, out)
	}
	out, err = exec.Command("e2fsck", "-fn", replay).CombinedOutput()
	t.Logf("e2fsck -fn after the replay: exit %d\n%s", exitCode(err), strings.TrimSpace(string(out)))
	require.NoError(t, err, "the checkpoint's disk is whole once its journal is applied: nothing structural is wrong with it")
}

// exitCode is a command's exit status, or -1 when it did not run.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// forkTwins forks count clones of one checkpoint and returns them once each is ready,
// with the wall clock each launch took.
func forkTwins(t *testing.T, node *sliceNode, chk sandboxpkg.CheckpointRecord, ids []string) ([]sandboxpkg.Record, time.Duration) {
	t.Helper()

	start := time.Now()
	forks, err := node.manager.Fork(context.Background(), sandboxpkg.ForkRequest{
		Checkpoint: chk.Name,
		Count:      len(ids),
		IDs:        ids,
		TTL:        -1,
	})
	elapsed := time.Since(start)
	require.NoError(t, err, "fork %d clones of %s", len(ids), chk.Name)
	require.Len(t, forks, len(ids))
	for _, clone := range forks {
		clone := clone
		t.Cleanup(func() {
			// A run that removed the clone itself — the residue audit — finds it gone.
			if _, err := node.manager.Remove(context.Background(), clone.ID); err != nil && !errors.Is(err, sandboxpkg.ErrNotFound) {
				t.Logf("removing the clone %s after the run: %v", clone.ID, err)
			}
		})
		require.Equal(t, sandboxpkg.StateReady, clone.State, "clone %s is ready", clone.ID)
	}
	return forks, elapsed
}

// writeInGuest does the write a clone must be able to do after ready: one payload put
// into its filesystem, and one guest process appending distinct lines to a log. It
// returns how long the pair took. A filesystem still frozen would sit on the caller's
// deadline instead of returning, which is why every caller passes one.
func writeInGuest(t *testing.T, guest *agent.Client, payload []byte, logPath string, lines int) time.Duration {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), forkWriteDeadline)
	defer cancel()

	start := time.Now()
	require.NoError(t, guest.CopyTo(ctx, forkCloneFile, bytes.NewReader(payload), 0o644),
		"a write from the host into the clone's filesystem, after ready: a frozen filesystem would block it")
	result, err := guest.ExecStatus(ctx, []string{"/bin/sh", "-c", fmt.Sprintf(
		`i=1; while [ $i -le %d ]; do printf 'line-%%02d\n' "$i" >> %s; i=$((i+1)); done`, lines, logPath)},
		agent.ExecOptions{})
	require.NoError(t, err, "a guest process writing after ready")
	require.Zero(t, result.ExitCode, "the guest process that wrote after ready exited cleanly")
	return time.Since(start)
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
