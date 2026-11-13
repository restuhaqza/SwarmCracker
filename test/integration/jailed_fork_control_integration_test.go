//go:build integration

package integration

// SBX-070 host evidence, the negative control: the same checkpoint resumed with
// nothing to thaw it, and what that has to look like.
//
// The release is not switched off, it is absent — and it is absent twice over,
// because a fork's writers are released in two places and only one of them belongs to
// pkg/sandbox:
//
//   - the guest's own post-restore hook thaws first (SBX-070's `prepare`), and it runs
//     only for a launch that carries an identity. This control launches with no
//     identity, so the hook is never asked;
//   - the release `buildFork` makes after the launch is not on this path either,
//     because the control drives the launcher itself rather than the manager.
//
// What is left is the guest the image froze, still frozen.
//
// # What this pins, and why it is not just "it fails"
//
// A bounded retry is landing in the release, and once it is there a "release failed"
// has two causes that look alike from the outside: the guest is still frozen, or one
// request was lost on a resumed guest (this host loses roughly one in three). Only the
// first leaves a frozen filesystem behind, and only the first is invisible to a retry.
// So the control requires the *blocked writer* and refuses the *lost request* by name:
// a write into the frozen guest must sit until its deadline and end as a timeout, not
// as a broken pipe, a reset connection, an EOF, or a dial against a path nobody bound.
// A later run where a frozen guest only produces a retry warning cannot pass this.
//
// The last step is the difference itself: the same write, in the same guest, after the
// release the fork path would have made. Released by hand it goes through, which is
// what makes the blocked write above a statement about the freeze and not about the
// harness.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	sandboxpkg "github.com/restuhaqza/swarmcracker/pkg/sandbox"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox/netpolicy"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

const (
	// forkControlID names the control instance. Its namespace is sbx-fork-ev2-norel
	// and its jail and cgroup carry the same name, so a leftover is recognisable.
	forkControlID = "fork-ev2-norel"

	// forkControlFile is the file the blocked write goes into.
	forkControlFile = "/workspace/fork-norel.bin"

	// forkBlockedWriteDeadline is how long the write is given. A released guest
	// completes this same write in tens of milliseconds, so anything close to this is
	// a writer that is sitting, which is the point.
	forkBlockedWriteDeadline = 20 * time.Second
)

// TestIntegration_JailedForkWithoutTheRelease is the control.
func TestIntegration_JailedForkWithoutTheRelease(t *testing.T) {
	requireSandboxHost(t)

	ctx := context.Background()
	node := newSliceNode(t)
	_, chk, _ := forkParent(t, node, forkParentID, forkCheckpointName)

	// A network of its own for the control instance: the launcher needs a namespace to
	// start the VMM in, and reusing the parent's would put two guests on one link.
	policy, err := netpolicy.New(netpolicy.Config{})
	require.NoError(t, err)
	network, err := policy.Attach(ctx, forkControlID, nil)
	require.NoError(t, err, "give the control instance a network of its own")
	t.Cleanup(func() { _ = policy.Detach(context.Background(), forkControlID) })

	// The instance's own copy of the disk: a fork never resumes onto the checkpoint's
	// file, and neither does this.
	disk := filepath.Join(node.rootfsDir, sandboxpkg.DirName, forkControlID+".ext4")
	require.NoError(t, os.MkdirAll(filepath.Dir(disk), 0o755))
	_, err = snapshot.CopyDisk(ctx, chk.Disk, disk)
	require.NoError(t, err, "give the control instance its own copy of the checkpoint's disk")

	state, mem := materializeCheckpointImages(t, node, chk)

	// The launcher is configured the way sandboxd configures the manager's — the same
	// jail base, log directory, cgroup parent and registry — with one deliberate
	// difference: a uid window of its own, because the parent is still running as a uid
	// from the manager's window and two launchers do not share a lease table.
	uids, err := jailer.NewUIDAllocator(jailedTestUIDBase+8, 4)
	require.NoError(t, err)
	launcher := snapshot.JailedLauncher{
		ChrootBaseDir: node.jailBase,
		JailerPath:    lookPath(t, "jailer"),
		ParentCgroup:  checkpointCgroup,
		LogDir:        node.logDir,
		UIDs:          uids,
		Registry:      node.instances,
		ReadyTimeout:  90 * time.Second,
		PidsMax:       64,
	}

	launched, err := launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: forkControlID,
		Mode:       snapshot.ModeFork,
		Kernel:     chk.Kernel,
		Rootfs:     disk,
		State:      state,
		Mem:        mem,
		VCPUs:      chk.VCPUs,
		MemoryMB:   chk.MemoryMB,
		NetNS:      network.NetNS,
		// The path the image froze, and the credential the resumed guest accepts: this
		// launch is the image as it was, with nothing published to it.
		VsockPath:  chk.VsockPath,
		AgentToken: chk.Token,
		Net: []snapshot.NetworkInterface{{
			IfaceID:  netpolicy.GuestInterface,
			HostTap:  network.Tap,
			GuestMAC: network.MAC,
		}},
		// What the node's registry records about this instance, so the run can find its
		// VMM and its jail the way it finds a sandbox's.
		Record: vminstance.Instance{
			Kind:    vminstance.KindSandbox,
			Disk:    disk,
			TAP:     network.Tap,
			GuestIP: network.GuestIP.String(),
			MAC:     network.MAC,
		},
		// No Identity: no metadata document, no post-restore hook, no thaw.
	})
	require.NoError(t, err, "the control instance resumes and its agent answers")
	t.Cleanup(func() { _ = launched.Stop(context.Background()) })
	t.Logf("control instance %s resumed with no identity and no release: netns=%s socket=%s",
		forkControlID, network.NetNS, launched.VsockPath)

	// The write that must sit. It is the same put the released clone completes in tens
	// of milliseconds, into a path of the same filesystem, with the same bytes.
	guest := dialGuestAt(t, launched.VsockPath, chk.Token)
	cloneBody := clonePayload(deterministicBytes(forkInheritedBytes), forkCloneBytes)

	blocked, cancel := context.WithTimeout(ctx, forkBlockedWriteDeadline)
	defer cancel()
	writeStart := time.Now()
	writeErr := guest.CopyTo(blocked, forkControlFile, bytes.NewReader(cloneBody), 0o644)
	sat := time.Since(writeStart)
	require.Error(t, writeErr, "a write into a guest nothing released does not complete")
	requireBlockedNotLost(t, writeErr, sat)
	t.Logf("the write sat for %s and ended as: %v", sat.Round(time.Millisecond), writeErr)

	// The difference itself: the release the fork path makes, made here by hand, and
	// the same write goes through. Without this the blocked write above would only say
	// that something was wrong, not that the freeze was what it was.
	released, err := dialGuestAt(t, launched.VsockPath, chk.Token).Quiesce(ctx, agent.QuiesceThaw)
	require.NoError(t, err, "release the control instance's writers")
	t.Logf("released by hand: action=%s mode=%s mounts=%v already=%v",
		released.Action, released.Mode, released.Mounts, released.Already)

	after := dialGuestAt(t, launched.VsockPath, chk.Token)
	require.NoError(t, after.CopyTo(ctx, forkControlFile, bytes.NewReader(cloneBody), 0o644),
		"the same write completes once the guest's writers are released")
	require.Equal(t, sha256Hex(cloneBody), sha256Hex(readGuestFile(t, after, forkControlFile)),
		"and it landed whole")
	t.Logf("after the release the same write completed in well under %s", forkBlockedWriteDeadline)
}

// requireBlockedNotLost holds the control's failure to the shape of a writer that sat
// on a frozen filesystem: it ended because its own deadline passed, and not because the
// request was lost on the way. It names what it refused, so a future failure that turns
// a frozen guest into a retry warning is a failure here rather than a pass.
func requireBlockedNotLost(t *testing.T, err error, sat time.Duration) {
	t.Helper()

	require.True(t, isTimeout(err),
		"the write ended because its deadline passed, not because the connection failed: %v", err)
	for _, lost := range []string{"broken pipe", "connection reset", "missing address", "no such file", "eof"} {
		require.NotContains(t, strings.ToLower(err.Error()), lost,
			"a blocked writer is not a lost request, but the error names %q: %v", lost, err)
	}
	require.GreaterOrEqual(t, sat, forkBlockedWriteDeadline/2,
		"the write really sat on the deadline rather than failing fast")
}

// isTimeout reports whether err is the connection deadline this package's client sets
// when a context ends, which is how a blocked write surfaces.
func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return strings.Contains(strings.ToLower(err.Error()), "i/o timeout")
}

// dialGuestAt dials a guest agent at a muxer path with the credential that guest
// accepts. The control has a record only inside the registry, so it is dialled by the
// path its launch reported.
func dialGuestAt(t *testing.T, vsockPath, token string) *agent.Client {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	guest, err := agent.DialWithToken(ctx, vsockPath, agent.DefaultPort, token)
	require.NoError(t, err, "dial the guest agent at %s", vsockPath)
	t.Cleanup(func() { _ = guest.Close() })
	return guest
}
