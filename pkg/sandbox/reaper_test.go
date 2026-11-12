package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// reaper_test holds the reaper to what it promises: an idle sandbox is taken back
// with everything it holds, a sandbox that is not idle is left alone, one that cannot
// be taken back does not stop the others, and the daemon's loop is the same decision
// repeated on a tick.

// idleSandbox is a sandbox that has been unused for five minutes with a one-minute
// TTL, which is the state the reaper exists for.
func idleSandbox(f *fixture, id string) Record {
	rec := testRecord(id)
	rec.LastActive = f.now.Add(-5 * time.Minute)
	rec.IdleTTLSeconds = 60
	return rec
}

// TestReapTakesBackAnIdleSandboxAndFreesEverything is the reaper's whole job: the VM
// is stopped (a real process, so the kernel is what says it is gone), the disk, the
// jail and the host's copy of the agent token are deleted, the namespace is detached,
// the node's instance record and the sandbox's API tokens are revoked, and the record
// is the last thing to go.
func TestReapTakesBackAnIdleSandboxAndFreesEverything(t *testing.T) {
	f := newFixture(t)

	revoked := make([]string, 0, 1)
	f.manager.cfg.RevokeTokens = func(_ context.Context, id string) (int, error) {
		revoked = append(revoked, id)
		return 2, nil
	}

	// A process that is really running stands in for the VMM: the reaper's stop step
	// is exercised against the kernel rather than against a mock.
	proc := exec.Command("sleep", "60")
	require.NoError(t, proc.Start())
	t.Cleanup(func() { _ = proc.Process.Kill() })

	tmp := t.TempDir()
	disk := filepath.Join(tmp, "sbx-1.ext4")
	require.NoError(t, os.WriteFile(disk, []byte("disk"), 0o600))
	tokenFile := filepath.Join(tmp, "sbx-1.agent-token")
	require.NoError(t, agent.WriteTokenFile(tokenFile, "0123456789abcdef0123456789abcdef"))
	jailRoot := filepath.Join(tmp, "jail")
	require.NoError(t, os.MkdirAll(filepath.Join(jailRoot, "root"), 0o700))

	rec := idleSandbox(f, "sbx-1")
	rec.Disk, rec.AgentTokenFile, rec.JailRoot = disk, tokenFile, jailRoot
	rec.NetNS, rec.TAP = "/var/run/netns/sbx-sbx-1", "tap0"
	require.NoError(t, f.store.Put(rec))
	require.NoError(t, f.instances.Put(vminstance.Instance{
		ID: "sbx-1", Kind: vminstance.KindSandbox, PID: proc.Process.Pid,
		APISocket: filepath.Join(tmp, "sbx-1.sock"), NetNS: rec.NetNS, TAP: "tap0", Disk: disk,
	}))
	f.instances.alive = vminstance.ProcessAlive

	reaped, err := f.manager.Reap(context.Background(), f.now)
	require.NoError(t, err)
	require.Len(t, reaped, 1)

	entry := reaped[0]
	assert.Equal(t, "sbx-1", entry.ID)
	assert.Equal(t, StateReady, entry.State, "the entry says what the sandbox was when it was found")
	assert.Equal(t, 5*time.Minute, entry.IdleFor)
	assert.NoError(t, entry.Err)

	assert.Equal(t, proc.Process.Pid, entry.Freed.PID)
	assert.True(t, entry.Freed.Process, "the VMM is stopped")
	assert.False(t, vminstance.ProcessAlive(proc.Process.Pid), "the process is gone, not merely signalled")
	assert.Equal(t, disk, entry.Freed.Disk)
	assert.Equal(t, jailRoot, entry.Freed.JailRoot)
	assert.Equal(t, tokenFile, entry.Freed.TokenFile)
	assert.Equal(t, rec.NetNS, entry.Freed.NetNS)
	assert.Equal(t, "tap0", entry.Freed.TAP)
	assert.True(t, entry.Freed.InstanceRecord)
	assert.True(t, entry.Freed.Record)
	assert.Equal(t, 2, entry.Freed.APITokens)
	assert.Equal(t, []string{"sbx-1"}, revoked)
	assert.Equal(t, []string{"sbx-1"}, f.network.detached)

	for _, path := range []string{disk, tokenFile, jailRoot} {
		_, statErr := os.Stat(path)
		assert.True(t, os.IsNotExist(statErr), "%s must be gone", path)
	}
	_, found, err := f.store.Get("sbx-1")
	require.NoError(t, err)
	assert.False(t, found, "a reaped sandbox's record is gone")
}

// TestReapTakesEveryStateThatOutlivedItsTTL pins the one age rule: a create that died
// in state created and a failed record left behind are taken back exactly like an idle
// ready sandbox, because a second garbage-collection rule would be a second thing to
// get wrong.
func TestReapTakesEveryStateThatOutlivedItsTTL(t *testing.T) {
	f := newFixture(t)

	created := idleSandbox(f, "created")
	created.Disk = filepath.Join(t.TempDir(), "created.ext4")
	require.NoError(t, os.WriteFile(created.Disk, []byte("half a disk"), 0o600))
	created.State = StateCreated
	require.NoError(t, f.store.Put(created))

	failed := idleSandbox(f, "failed")
	failed.State, failed.Reason = StateFailed, "the guest never answered"
	require.NoError(t, f.store.Put(failed))

	reaped, err := f.manager.Reap(context.Background(), f.now)
	require.NoError(t, err)
	require.Len(t, reaped, 2)

	byID := map[string]Reaped{}
	for _, entry := range reaped {
		byID[entry.ID] = entry
	}

	assert.Equal(t, StateCreated, byID["created"].State)
	assert.Equal(t, created.Disk, byID["created"].Freed.Disk)
	assert.Equal(t, StateFailed, byID["failed"].State)
	assert.NoError(t, byID["failed"].Err)

	for _, id := range []string{"created", "failed"} {
		_, found, getErr := f.store.Get(id)
		require.NoError(t, getErr)
		assert.False(t, found, "%s must be gone", id)
	}
	_, statErr := os.Stat(created.Disk)
	assert.True(t, os.IsNotExist(statErr), "a create that died leaves no disk behind")
}

// TestReapKeepsASandboxItCannotStopAndTakesTheOthers is why Reap reports per entry: a
// sandbox whose VMM will not stop is still running, so its disk, jail and namespace
// stay, and the entry says why — and the idle sandbox beside it is still taken back.
func TestReapKeepsASandboxItCannotStopAndTakesTheOthers(t *testing.T) {
	f := newFixture(t)

	wedged := idleSandbox(f, "wedged")
	wedged.Disk = filepath.Join(t.TempDir(), "wedged.ext4")
	require.NoError(t, os.WriteFile(wedged.Disk, []byte("disk"), 0o600))
	require.NoError(t, f.store.Put(wedged))
	require.NoError(t, f.instances.Put(vminstance.Instance{
		ID: "wedged", Kind: vminstance.KindSandbox, PID: 4242,
	}))
	f.instances.alive = func(int) bool { return true }

	original := stopVMM
	stopVMM = func(pid int, _ time.Duration) error {
		if pid == 4242 {
			return fmt.Errorf("process is wedged")
		}
		return original(pid, 1*time.Second)
	}
	t.Cleanup(func() { stopVMM = original })

	other := idleSandbox(f, "other")
	require.NoError(t, f.store.Put(other))

	reaped, err := f.manager.Reap(context.Background(), f.now)
	require.NoError(t, err, "one stuck sandbox is a per-entry failure, not a failed reap")
	require.Len(t, reaped, 2)

	byID := map[string]Reaped{}
	for _, entry := range reaped {
		byID[entry.ID] = entry
	}

	require.Error(t, byID["wedged"].Err)
	assert.Contains(t, byID["wedged"].Err.Error(), "stop its VMM")
	assert.Equal(t, Freed{}, byID["wedged"].Freed, "a sandbox that was not taken back frees nothing")

	_, found, err := f.store.Get("wedged")
	require.NoError(t, err)
	assert.True(t, found, "the record stays the handle to a sandbox that is still running")
	_, statErr := os.Stat(wedged.Disk)
	assert.NoError(t, statErr, "its disk is still the running VM's")

	assert.NoError(t, byID["other"].Err)
	assert.True(t, byID["other"].Freed.Record)
	_, found, err = f.store.Get("other")
	require.NoError(t, err)
	assert.False(t, found)
}

// TestReapReportsWhatItCannotRead pins the two ways a node's records can be wrong:
// one file that does not decode is skipped (and left in place for a human), while a
// record directory that cannot be read at all is an error rather than an empty node.
func TestReapReportsWhatItCannotRead(t *testing.T) {
	t.Run("a bad record is skipped and the rest are reaped", func(t *testing.T) {
		f := newFixture(t)
		require.NoError(t, os.WriteFile(filepath.Join(f.store.Dir(), "broken.json"),
			[]byte("{not a record"), 0o600))
		require.NoError(t, f.store.Put(idleSandbox(f, "idle")))

		reaped, err := f.manager.Reap(context.Background(), f.now)
		require.NoError(t, err)
		require.Len(t, reaped, 1)
		assert.Equal(t, "idle", reaped[0].ID)

		_, statErr := os.Stat(filepath.Join(f.store.Dir(), "broken.json"))
		assert.NoError(t, statErr, "the reaper does not delete a record it cannot read")
	})

	t.Run("a directory it cannot read is an error", func(t *testing.T) {
		f := newFixture(t)
		dir := f.store.Dir()
		require.NoError(t, os.RemoveAll(dir))
		// A file where the directory should be: every read of the store fails, which is
		// the node-level failure the reaper must report instead of claiming an empty node.
		require.NoError(t, os.WriteFile(dir, []byte("not a directory"), 0o600))

		reaped, err := f.manager.Reap(context.Background(), f.now)
		require.Error(t, err)
		assert.Empty(t, reaped)
	})
}

// TestReapEveryTakesAnIdleSandboxOnATick is the daemon's shape of the reaper: a
// sandbox that idles after the loop started is taken back by a later pass, and the
// loop returns cleanly when its context ends.
func TestReapEveryTakesAnIdleSandboxOnATick(t *testing.T) {
	f := newFixture(t)

	// The sandbox is fresh when the loop starts, so its immediate first pass has
	// nothing to do; it idles only afterwards, and then only a tick can take it.
	rec := testRecord("later")
	rec.LastActive, rec.IdleTTLSeconds = f.now, 60
	require.NoError(t, f.store.Put(rec))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.manager.ReapEvery(ctx, 5*time.Millisecond) }()

	time.Sleep(20 * time.Millisecond)
	_, found, err := f.store.Get("later")
	require.NoError(t, err)
	require.True(t, found, "a sandbox inside its TTL is not taken")

	f.clock.Advance(5 * time.Minute)

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, found, err = f.store.Get("later")
		require.NoError(t, err)
		if !found {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the reaper did not take an idle sandbox within five seconds")
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "the loop stops with its context, not with an error")
	case <-time.After(5 * time.Second):
		t.Fatal("ReapEvery did not return after its context ended")
	}
}

// TestReapEverySurvivesAStoreThatCannotBeRead pins the loop's tolerance: a pass that
// cannot read the node's records is logged and retried, because a daemon that stopped
// reaping over one bad read would leak every sandbox it ever made.
func TestReapEverySurvivesAStoreThatCannotBeRead(t *testing.T) {
	f := newFixture(t)
	dir := f.store.Dir()
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.WriteFile(dir, []byte("not a directory"), 0o600))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	assert.NoError(t, f.manager.ReapEvery(ctx, 2*time.Millisecond),
		"a pass that cannot read the records is retried, and the loop still ends with its context")
}
