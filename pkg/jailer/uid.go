// The jail uid allocator.
//
// Every jailed VMM runs as a host uid of its own. The uid is what the kernel, the
// cgroup and the host's own tools use to attribute a process, so two instances
// sharing one cannot be told apart: one instance's writes would be the other's,
// and a teardown that swept up everything owned by the uid would take both VMs
// with it. The node therefore owns a range of uids, hands one out per instance,
// and takes it back only once that instance is gone.
//
// Two rules make the range safe to reuse, and they are independent on purpose:
//
//   - A uid the allocator has leased is never handed out again, so a live
//     instance cannot be joined by a second one even if a caller ignores the id.
//   - A uid any live process runs as is never handed out either (see procUIDs),
//     which is what makes a restart safe: the leases live in this process's
//     memory, so after a crash or a daemon restart the process table is the only
//     remaining record of the VMMs still running.
package jailer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ErrNoFreeUID is what Allocate returns when every uid in the configured range is
// either leased or already in use. A caller that sees it is out of jail ids for
// this node, not configured wrongly.
var ErrNoFreeUID = errors.New("no free jail uid in the configured range")

// maxJailUID bounds a configured range. The kernel's uids are 32-bit, but a uid
// above 2^31-1 is not something to hand to a VMM: it has no sane interaction with
// the tools that print uids, and nobody's range needs it.
const maxJailUID = 1<<31 - 2

// UIDAllocator hands out the host uids jailed VMMs run as, from one configured
// range.
//
// It is safe for concurrent use: the launcher allocates from several goroutines,
// one per start, and two starts that raced to the same uid would put two VMs under
// one identity.
type UIDAllocator struct {
	first int
	size  int

	mu     sync.Mutex
	leased map[int]uint64 // uid -> the generation of the lease that holds it
	next   uint64

	// procRoot is the process table the "is this uid in use" check reads. It is a
	// field rather than a constant so a test can decide the answer from fixtures
	// instead of from whatever happens to be running on the machine; production
	// leaves it at /proc.
	procRoot string
}

// Lease is one uid handed out to one instance, and the handle that gives it back.
//
// Release is on the lease rather than on the allocator because the same uid is
// handed out again after cleanup: a stop that is retried after a new instance took
// the uid must not free the new instance's lease. Each lease carries a generation,
// so only the lease that currently holds a uid can release it.
type Lease struct {
	allocator  *UIDAllocator
	uid        int
	generation uint64
}

// UID is the uid this lease holds.
func (l *Lease) UID() int { return l.uid }

// Release returns the uid to the range, if this lease still holds it.
//
// It is the step that makes reuse legal, so the caller must run it only after the
// instance's process, socket, cgroup and jail directory are gone: until then a
// second VM under this uid would share an identity with a live one. Calling it
// twice, or after the uid was leased to a newer instance, is a no-op — which is
// what makes a retried teardown safe.
func (l *Lease) Release() {
	if l == nil || l.allocator == nil {
		return
	}
	l.allocator.release(l.uid, l.generation)
}

// NewUIDAllocator creates an allocator for [first, first+size).
//
// The range must not contain uid 0: a jail that runs as root is not a jail, and
// refusing the range is cheaper than refusing every launch from it. The range is
// checked against the host only when a uid is handed out, because the set of
// running processes changes and a range that is busy now may not be later.
func NewUIDAllocator(first, size int) (*UIDAllocator, error) {
	if first < 1 {
		return nil, fmt.Errorf("jail uid range must start above root, got %d", first)
	}
	if size < 1 {
		return nil, fmt.Errorf("jail uid range must hold at least one uid, got %d", size)
	}
	if first+size-1 > maxJailUID {
		return nil, fmt.Errorf("jail uid range %d+%d runs past %d", first, size, maxJailUID)
	}

	return &UIDAllocator{
		first:    first,
		size:     size,
		leased:   make(map[int]uint64, size),
		procRoot: "/proc",
	}, nil
}

// Allocate leases the lowest uid in the range that is neither leased nor in use,
// and returns it.
//
// Lowest-first is deliberate: a node that allocates and releases in a loop keeps
// its uids packed near the bottom of the range, so the uid a VM runs as is stable
// across restarts and an operator reading `ps` sees a compact set.
func (a *UIDAllocator) Allocate() (*Lease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	used, err := procUIDs(a.procRoot)
	if err != nil {
		// Failing closed is the whole point of the check: an allocator that
		// cannot see which uids are alive would hand a live VMM's uid to a second
		// instance rather than report that it cannot tell.
		return nil, fmt.Errorf("jail uid allocation: %w", err)
	}

	for uid := a.first; uid < a.first+a.size; uid++ {
		if a.busy(uid, used) {
			continue
		}
		return a.lease(uid), nil
	}

	return nil, fmt.Errorf("%w (%d uids from %d)", ErrNoFreeUID, a.size, a.first)
}

// Reserve leases one specific uid, for a caller that assigns uids outside a
// Launcher — a pool that holds ids ahead of time, or a test that has to prove two
// instances differ.
//
// A LaunchRequest that names a uid does not need this: the launcher reserves it
// itself as part of the launch, and reserving it twice refuses the launch.
//
// It refuses a uid that is leased or in use, and it refuses one outside the range:
// a caller cannot reach past the node's allocation by naming a uid.
func (a *UIDAllocator) Reserve(uid int) (*Lease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if uid < a.first || uid >= a.first+a.size {
		return nil, fmt.Errorf("jail uid %d is outside the configured range [%d, %d)",
			uid, a.first, a.first+a.size)
	}
	if _, ok := a.leased[uid]; ok {
		return nil, fmt.Errorf("jail uid %d is already leased by another instance", uid)
	}

	used, err := procUIDs(a.procRoot)
	if err != nil {
		return nil, fmt.Errorf("jail uid %d: %w", uid, err)
	}
	if _, ok := used[uid]; ok {
		return nil, fmt.Errorf("jail uid %d is in use by a running process", uid)
	}

	return a.lease(uid), nil
}

// Leased reports whether uid is currently leased by this allocator.
func (a *UIDAllocator) Leased(uid int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	_, ok := a.leased[uid]
	return ok
}

// lease records the lease and returns its handle. The caller holds the lock.
func (a *UIDAllocator) lease(uid int) *Lease {
	a.next++
	a.leased[uid] = a.next
	return &Lease{allocator: a, uid: uid, generation: a.next}
}

// release drops the lease only when it is the lease that still holds the uid.
func (a *UIDAllocator) release(uid int, generation uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.leased[uid] == generation {
		delete(a.leased, uid)
	}
}

// busy reports whether uid must not be handed out: it is leased here, or a live
// process already runs as it.
func (a *UIDAllocator) busy(uid int, used map[int]struct{}) bool {
	if _, ok := a.leased[uid]; ok {
		return true
	}
	_, inUse := used[uid]
	return inUse
}

// procUIDs reads the uids in use from the process table.
//
// The pid directory names are the only cheap way in; each process's /proc/<pid>/status
// then carries the four uids a process has (real, effective, saved and filesystem).
// A process that dropped privileges has its real uid in one field and its working
// uid in another, and a VMM's uid must be considered in use if *any* of them is it,
// so every field is collected.
//
// A process that exits during the scan disappears between the directory listing
// and the read; that is not an error and simply contributes nothing. A process
// table that cannot be listed at all is an error, because the answer to "which
// uids are in use" is then unknown, not empty.
func procUIDs(procRoot string) (map[int]struct{}, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("read the process table %s: %w", procRoot, err)
	}

	used := make(map[int]struct{}, len(entries))
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue // not a process: /proc is full of other files
		}

		status, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "status"))
		if err != nil {
			continue // the process is gone; an unreadable one cannot be claiming a uid
		}
		for _, uid := range statusUids(status) {
			used[uid] = struct{}{}
		}
	}

	return used, nil
}

// statusUids returns the uids named on a status file's Uid line, or nothing when
// the process has no such line.
func statusUids(status []byte) []int {
	const key = "Uid:"

	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, key) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, key))
		uids := make([]int, 0, len(fields))
		for _, field := range fields {
			uid, err := strconv.Atoi(field)
			if err != nil {
				continue
			}
			uids = append(uids, uid)
		}
		return uids
	}
	return nil
}
