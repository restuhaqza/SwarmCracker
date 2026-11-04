package jailer

// The jail uid allocator, tested without a VMM. What makes the allocator worth a
// test is the pair of rules it enforces: one uid per live instance, and no reuse
// until the instance is gone. The process table it reads is a fixture directory,
// so the test decides which uids look alive instead of depending on what happens
// to be running on the machine.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProc writes a process table with one live process per uid in uids.
func fakeProc(t *testing.T, uids []int) string {
	t.Helper()

	root := t.TempDir()
	pid := 100
	for _, uid := range uids {
		dir := filepath.Join(root, strconv.Itoa(pid))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		status := fmt.Sprintf("Name:\tfirecracker\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\n",
			uid, uid, uid, uid, uid, uid, uid, uid)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644))
		pid++
	}

	// /proc also holds entries that are not processes, and a pid that vanished
	// between the listing and the read. Neither may break a scan.
	require.NoError(t, os.WriteFile(filepath.Join(root, "self"), []byte("not a directory"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "9999"), 0o755))

	return root
}

func newAllocator(t *testing.T, first, size int) *UIDAllocator {
	t.Helper()

	alloc, err := NewUIDAllocator(first, size)
	require.NoError(t, err)
	return alloc
}

// TestNewUIDAllocatorRefusesAnUnusableRange pins the ranges that would either make
// a jail run as root or overflow the kernel's uid type.
func TestNewUIDAllocatorRefusesAnUnusableRange(t *testing.T) {
	tests := []struct {
		name  string
		first int
		size  int
		want  string
	}{
		{name: "root", first: 0, size: 10, want: "above root"},
		{name: "empty range", first: 200000, size: 0, want: "at least one uid"},
		{name: "past the uid space", first: maxJailUID, size: 2, want: "runs past"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewUIDAllocator(tt.first, tt.size)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// TestAllocateHandsOutTheRangeLowestFirst pins the packing rule and the exhaustion
// answer: a node filling up its range gets consecutive uids, and the uid after the
// last one is a distinct error rather than a silent reuse.
func TestAllocateHandsOutTheRangeLowestFirst(t *testing.T) {
	alloc := newAllocator(t, 2100, 3)
	alloc.procRoot = fakeProc(t, nil)

	for _, want := range []int{2100, 2101, 2102} {
		lease, err := alloc.Allocate()
		require.NoError(t, err)
		assert.Equal(t, want, lease.UID())
	}

	_, err := alloc.Allocate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoFreeUID)
}

// TestAllocateSkipsLiveUIDsAndNeverHandsOutALeasedOne pins the two reasons a uid is
// unavailable: a running process owns it, or this allocator already leased it. The
// fixture makes 2100 and 2103 look alive, so the first two allocations must land on
// 2101 and 2102 — a fixture with no live uids would pass even if the check were
// ignored.
func TestAllocateSkipsLiveUIDsAndNeverHandsOutALeasedOne(t *testing.T) {
	alloc := newAllocator(t, 2100, 4)
	alloc.procRoot = fakeProc(t, []int{2100, 2103})

	first, err := alloc.Allocate()
	require.NoError(t, err)
	assert.Equal(t, 2101, first.UID())

	second, err := alloc.Allocate()
	require.NoError(t, err)
	assert.Equal(t, 2102, second.UID(), "a uid this allocator leased must not be handed out twice")

	_, err = alloc.Allocate()
	require.Error(t, err, "every remaining uid is either alive or leased")
	assert.ErrorIs(t, err, ErrNoFreeUID)
}

// TestLeaseReleaseMakesAUIDReusableOnlyThen pins the reuse rule: the uid comes back
// only after Release, and the allocator keeps using it afterwards, which is what
// keeps a long-lived node's uids packed.
func TestLeaseReleaseMakesAUIDReusableOnlyThen(t *testing.T) {
	alloc := newAllocator(t, 2200, 1)
	alloc.procRoot = fakeProc(t, nil)

	lease, err := alloc.Allocate()
	require.NoError(t, err)
	require.Equal(t, 2200, lease.UID())
	assert.True(t, alloc.Leased(lease.UID()))

	_, err = alloc.Allocate()
	require.ErrorIs(t, err, ErrNoFreeUID, "a leased uid must not be reused while the instance runs")

	lease.Release()
	assert.False(t, alloc.Leased(lease.UID()))

	again, err := alloc.Allocate()
	require.NoError(t, err)
	assert.Equal(t, lease.UID(), again.UID(), "reuse after cleanup is what keeps the range from leaking")
}

// TestAStaleLeaseDoesNotFreeANewerOne pins the reason a lease carries a generation:
// a stop that is retried after another instance took the uid must not hand that
// instance's uid to a third one. Without the generation the second release would
// free a live instance's uid, and nothing else in the allocator would notice.
func TestAStaleLeaseDoesNotFreeANewerOne(t *testing.T) {
	alloc := newAllocator(t, 2250, 1)
	alloc.procRoot = fakeProc(t, nil)

	first, err := alloc.Allocate()
	require.NoError(t, err)
	first.Release()

	second, err := alloc.Allocate()
	require.NoError(t, err)
	require.Equal(t, first.UID(), second.UID())

	first.Release() // the retried teardown of the instance that used to hold this uid
	assert.True(t, alloc.Leased(second.UID()), "the newer lease must survive a stale release")

	_, err = alloc.Allocate()
	require.ErrorIs(t, err, ErrNoFreeUID)
}

// TestReserveRefusesAUIDItMustNotUse pins the explicit-uid path: a caller may name
// a uid, but only one the allocator could have handed out.
func TestReserveRefusesAUIDItMustNotUse(t *testing.T) {
	alloc := newAllocator(t, 2300, 2)
	alloc.procRoot = fakeProc(t, nil)

	_, err := alloc.Reserve(2301)
	require.NoError(t, err)

	_, err = alloc.Reserve(2301)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already leased")

	_, err = alloc.Reserve(2302)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the configured range")

	_, err = alloc.Reserve(2299)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the configured range")

	// The lowest free uid is the one that was not reserved: a caller that names a
	// uid must not change what an automatic allocation returns.
	lease, err := alloc.Allocate()
	require.NoError(t, err)
	assert.Equal(t, 2300, lease.UID())
}

// TestReserveRefusesAUIDInUseByARunningProcess pins the other half of Reserve: a
// caller cannot name a uid that a live process already owns, which is how a restore
// into a recorded uid is refused when the VMM it names is still running.
func TestReserveRefusesAUIDInUseByARunningProcess(t *testing.T) {
	alloc := newAllocator(t, 2400, 2)
	alloc.procRoot = fakeProc(t, []int{2400})

	_, err := alloc.Reserve(2400)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in use by a running process")

	// A uid the fixture names only as an effective uid is just as occupied.
	alloc.procRoot = fakeProc(t, []int{2401})
	_, err = alloc.Reserve(2401)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in use by a running process")
}

// TestAllocateFailsClosedWithoutAProcessTable pins the honest answer when the
// process table cannot be read: allocation stops instead of guessing that no uid is
// in use, which would be exactly the mistake the check exists to prevent.
func TestAllocateFailsClosedWithoutAProcessTable(t *testing.T) {
	alloc := newAllocator(t, 2500, 4)
	alloc.procRoot = filepath.Join(t.TempDir(), "no-such-proc")

	_, err := alloc.Allocate()
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoFreeUID)
	assert.Contains(t, err.Error(), "read the process table")

	_, err = alloc.Reserve(2500)
	require.Error(t, err)
}

// TestConcurrentAllocationsAreDistinct pins the concurrency guarantee: one start per
// goroutine, one uid each. A map read outside the lock, or a check-then-lease
// window, hands two instances the same uid and this test catches it under -race.
func TestConcurrentAllocationsAreDistinct(t *testing.T) {
	const instances = 8

	alloc := newAllocator(t, 2600, instances)
	alloc.procRoot = fakeProc(t, nil)

	uids := make([]int, instances)
	errs := make([]error, instances)

	var wg sync.WaitGroup
	for i := 0; i < instances; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var lease *Lease
			lease, errs[i] = alloc.Allocate()
			if lease != nil {
				uids[i] = lease.UID()
			}
		}(i)
	}
	wg.Wait()

	seen := make(map[int]struct{}, instances)
	for i, err := range errs {
		require.NoError(t, err)
		_, duplicate := seen[uids[i]]
		assert.False(t, duplicate, "uid %d was handed out twice", uids[i])
		seen[uids[i]] = struct{}{}
	}
	assert.Len(t, seen, instances)
}

// TestAllocateSeesARealVMMProcess proves the production process-table read against
// a real process: a child running as one of the range's uids must make that uid
// unavailable. The fixture tests above can only show that the allocator believes a
// table; this one shows the table it reads by default is the host's own.
func TestAllocateSeesARealVMMProcess(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("starting a process as another uid needs root")
	}

	const busyUID = 2687
	// The uid must not already be taken on this host, or the test proves nothing.
	if _, ok := mustProcUIDs(t)[busyUID]; ok {
		t.Skipf("uid %d is already in use on this host", busyUID)
	}

	child := exec.Command("sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
		Uid: uint32(busyUID),
		Gid: uint32(busyUID),
	}}
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})
	waitForUID(t, busyUID)

	alloc := newAllocator(t, busyUID, 2)
	lease, err := alloc.Allocate()
	require.NoError(t, err)
	assert.Equal(t, busyUID+1, lease.UID(), "a uid a live process runs as must be skipped")

	_, err = alloc.Reserve(busyUID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in use by a running process")
}

// mustProcUIDs reads the host process table, failing the test if it cannot.
func mustProcUIDs(t *testing.T) map[int]struct{} {
	t.Helper()

	uids, err := procUIDs("/proc")
	require.NoError(t, err)
	return uids
}

// waitForUID waits until the host's process table shows uid. Start returns before
// the child has run, so the first read races the exec.
func waitForUID(t *testing.T, uid int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok := mustProcUIDs(t)[uid]; ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("uid %d never appeared in the process table", uid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestProcUIDsReadsEveryFieldOfTheUidLine pins the parser on a status file it has to
// get right: the effective uid is what a dropped-privilege process actually runs as,
// and a VMM under that uid must count as in use.
func TestProcUIDsReadsEveryFieldOfTheUidLine(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "42"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "42", "status"),
		[]byte("Name:\tfirecracker\nUid:\t0\t2701\t0\t2702\n"), 0o644))

	uids, err := procUIDs(root)
	require.NoError(t, err)
	assert.Contains(t, uids, 0)
	assert.Contains(t, uids, 2701)
	assert.Contains(t, uids, 2702)
}

// TestReleasingAnUnknownUIDIsANoop pins the retry path: a teardown that runs twice
// releases the same uid twice, and a release that no longer matches a lease must be
// harmless.
func TestReleasingAnUnknownUIDIsANoop(t *testing.T) {
	alloc := newAllocator(t, 2800, 2)
	alloc.procRoot = fakeProc(t, nil)

	lease, err := alloc.Allocate()
	require.NoError(t, err)
	lease.Release()
	lease.Release()

	// The uid is free again, and no other lease was disturbed.
	next, err := alloc.Allocate()
	require.NoError(t, err)
	assert.Equal(t, 2800, next.UID())

	var unleased *Lease
	unleased.Release()
}

// TestReserveFailsClosedWithoutAProcessTable is Reserve's half of the same rule:
// naming a uid does not excuse the allocator from checking whether a live process owns
// it, and not being able to tell is an error, not a pass.
func TestReserveFailsClosedWithoutAProcessTable(t *testing.T) {
	alloc := newAllocator(t, 2810, 2)
	alloc.procRoot = filepath.Join(t.TempDir(), "no-such-proc")

	_, err := alloc.Reserve(2810)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoFreeUID)
	assert.Contains(t, err.Error(), "read the process table")
	assert.False(t, alloc.Leased(2810), "a refused reservation must not take the uid")
}

// TestNewUIDAllocatorAcceptsTheLastUIDOfTheSpace pins the boundary the range check is
// written against: a range whose last uid is the highest allowed one is valid, so a
// node configured there is not refused for being exactly at the limit.
func TestNewUIDAllocatorAcceptsTheLastUIDOfTheSpace(t *testing.T) {
	alloc, err := NewUIDAllocator(maxJailUID, 1)
	require.NoError(t, err)
	alloc.procRoot = fakeProc(t, nil)

	lease, err := alloc.Allocate()
	require.NoError(t, err)
	assert.Equal(t, maxJailUID, lease.UID())
}

// TestProcUIDsIgnoresAStatusWithoutAUidLine pins the parser against a process whose
// status carries no Uid line: it contributes no uid, and that is not an error, because
// one odd process must not stop the node from allocating.
func TestProcUIDsIgnoresAStatusWithoutAUidLine(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "7"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "7", "status"),
		[]byte("Name:\tkthreadd\nState:\tS\n"), 0o644))

	uids, err := procUIDs(root)
	require.NoError(t, err)
	assert.Empty(t, uids)
}
