package sandbox

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Admission: what the node will let a create have, decided before it has given it
// anything.
//
// A create allocates a network namespace, a disk and a VM, and a node has a finite
// amount of each. Admission is the decision that comes first, so a request the node
// cannot serve is refused while it still holds nothing: a refused create leaves no
// record, no namespace, no disk and no VM behind — nothing a teardown would have to
// find, and nothing a caller has to wonder about. It is also the only order that
// works: a check made after the disk is copied can only report the waste.
//
// # Two kinds of limit, both checked
//
//   - The node's capacity: the guest memory the node's sandboxes have been asked
//     for, and the bytes their disks occupy. A node has one of each.
//   - One owner's share: how many sandboxes a single owner may hold on the node at
//     once. A node with spare capacity still has to keep one caller from spending all
//     of it, and a node with no spare capacity must refuse the next caller whatever
//     its owner already holds.
//
// Both are checked on every create rather than whichever is cheaper to check: a
// request that fits one and not the other is still a request the node cannot serve.
// When more than one limit is exceeded the refusal names the first in the fixed order
// the checks run — the owner's count, then memory, then disk — so the report is a
// decision a caller can act on rather than a set of reasons with no priority.
//
// # What counts against a limit
//
// A sandbox holds memory and a disk from the moment the node starts building it until
// it is gone: a record in state created is a create in flight, and one in state ready
// is a running VM. A failed sandbox holds nothing (the create path frees what it
// allocated) and a stopped one is on its way out, so neither counts. The rule is the
// state machine's, not a second bookkeeping pass that could disagree with it.
//
// The owner a sandbox counts against is the owner it was created with, as the record
// stores it. A create that names no owner shares one bucket with every other
// unnamed create rather than escaping the per-owner limit: a limit a caller can leave
// by asking without an owner is not a limit.
//
// # Where the numbers come from
//
// Every limit is configuration, and a node that configures none gets the defaults in
// this file rather than no limit at all: an operator who has not thought about quota
// still needs the node not to be spent by one caller. A negative limit is refused when
// the manager is built (New), because it cannot be meant — it is a configuration
// mistake, and a node that silently read it as "no limit" would be a node that admits
// everything after a typo.
//
// Memory is measured in the MiB a sandbox was created with (the same number the
// launcher and the cgroup are given). Disk is measured as the bytes of the disks the
// node has materialized, with the disk a create is about to make counted at the size
// of the template image it copies — the most that copy can occupy, which is the
// conservative side to be wrong on.

// The default limits. They are per node and are what a node that configures nothing
// admits: a node whose operator never opened this file still must not be spent by one
// caller.
const (
	// DefaultMaxSandboxesPerOwner is how many sandboxes one owner may hold at once on
	// a node by default. It is low on purpose: a sandbox is a machine, and an owner
	// that needs more than ten at once is an owner whose operator should say so.
	DefaultMaxSandboxesPerOwner = 10
	// DefaultNodeMemoryMB is the guest memory a node admits, in MiB. A sandbox
	// defaults to 512 MiB, so this is sixteen of them.
	DefaultNodeMemoryMB = 8192
	// DefaultNodeDiskMB is the disk a node admits for sandbox disks, in MiB. The
	// reference templates' disks are around 2 GiB, so this is roughly the 50 GiB an
	// operator would give a scratch volume.
	DefaultNodeDiskMB = 51200
	// DefaultQuotaRetryAfter is the hint a refusal carries by default: how long a
	// caller should wait before asking again. It is a hint, not a promise — a sandbox
	// that is removed or reaped frees its share sooner, and one that is used again
	// frees nothing — and it is deliberately longer than a reaper pass so that a
	// caller that waits it out has a chance of finding room.
	DefaultQuotaRetryAfter = 60 * time.Second
)

// The resources a limit is about, as the names a refusal carries and the API reports.
// They are strings because they cross the API in details.resource, and the unit of
// limit and used follows the resource: a count for a sandbox count, MiB for memory and
// disk.
const (
	// QuotaResourceOwnerSandboxes limits the sandboxes one owner holds.
	QuotaResourceOwnerSandboxes = "sandboxes_per_owner"
	// QuotaResourceNodeMemoryMB limits the guest memory the node's sandboxes hold, in
	// MiB.
	QuotaResourceNodeMemoryMB = "node_memory_mb"
	// QuotaResourceNodeDiskMB limits the disk the node's sandboxes hold, in MiB.
	QuotaResourceNodeDiskMB = "node_disk_mb"
)

// Quota is the node's admission limits. A zero field means the default above it, so a
// node that configures nothing gets the defaults and a node that configures one limit
// still gets the rest.
//
// Zero cannot mean "no limit" as well as "not set": the two meanings cannot share one
// value without an operator's typo becoming an unlimited node, so the only way to
// widen a limit is to name a larger number.
type Quota struct {
	// MaxPerOwner is how many sandboxes one owner may hold on this node at once.
	MaxPerOwner int
	// MemoryMB is the guest memory this node admits for sandboxes, in MiB.
	MemoryMB int
	// DiskMB is the disk this node admits for sandbox disks, in MiB.
	DiskMB int
	// RetryAfter is the hint a refusal carries: how long a caller should wait before
	// asking again.
	RetryAfter time.Duration
}

// DefaultQuota is the admission policy of a node that configured none.
func DefaultQuota() Quota {
	return Quota{
		MaxPerOwner: DefaultMaxSandboxesPerOwner,
		MemoryMB:    DefaultNodeMemoryMB,
		DiskMB:      DefaultNodeDiskMB,
		RetryAfter:  DefaultQuotaRetryAfter,
	}
}

// resolve fills the defaults and refuses a configuration that cannot be meant.
//
// A negative limit is the mistake this catches: it is the value a subtraction or a
// misunderstood flag produces, and a node that admitted it as "no limit" would be
// unlimited after exactly the mistake that should stop it starting. It is refused here,
// when the manager is built, rather than at the first create — a node that cannot say
// what it admits must not be serving.
func (q Quota) resolve() (Quota, error) {
	for _, limit := range []struct {
		name  string
		value int
	}{
		{"max sandboxes per owner", q.MaxPerOwner},
		{"node memory (MiB)", q.MemoryMB},
		{"node disk (MiB)", q.DiskMB},
	} {
		if limit.value < 0 {
			return Quota{}, fmt.Errorf("sandbox: quota %s is negative (%d): a limit must be a positive number or zero for the default", limit.name, limit.value)
		}
	}
	if q.RetryAfter < 0 {
		return Quota{}, fmt.Errorf("sandbox: quota retry-after %s is negative", q.RetryAfter)
	}

	defaults := DefaultQuota()
	if q.MaxPerOwner == 0 {
		q.MaxPerOwner = defaults.MaxPerOwner
	}
	if q.MemoryMB == 0 {
		q.MemoryMB = defaults.MemoryMB
	}
	if q.DiskMB == 0 {
		q.DiskMB = defaults.DiskMB
	}
	if q.RetryAfter == 0 {
		q.RetryAfter = defaults.RetryAfter
	}
	return q, nil
}

// diskBudget is the node's disk limit in bytes, which is the unit the disks are
// measured in.
func (q Quota) diskBudget() int64 { return int64(q.DiskMB) << 20 }

// ErrQuota means the node cannot admit another sandbox: its own capacity or the
// caller's share of it is spent. It is a sentinel so a caller can tell "the node is
// full" from "this request is wrong", and a *QuotaError carries which limit and by
// how much.
var ErrQuota = errors.New("sandbox: quota exceeded")

// QuotaError is a refused admission, in the terms the caller needs to act: which limit
// was reached, what it allows, what was already in use, what this request asked for
// and when it is worth asking again.
//
// Limit and Used are in the unit Resource names — a count of sandboxes for
// QuotaResourceOwnerSandboxes, MiB for the two node resources — and Requested is the
// same unit, so a caller can see that the request was too large for what was left
// rather than only that it was refused.
type QuotaError struct {
	// Resource names the limit that was reached: one of the QuotaResource constants.
	Resource string
	// Owner is the owner a per-owner refusal is about. It is empty for a node-wide
	// limit, which is nobody's in particular.
	Owner string
	// Limit is what the limit allows, Used what was in use, Requested what this
	// create asked for.
	Limit     int64
	Used      int64
	Requested int64
	// RetryAfter is how long the caller should wait before asking again. It is the
	// node's hint, not a promise: a sandbox that is removed or reaped frees its share
	// sooner.
	RetryAfter time.Duration
}

// Error describes the refusal in the limit's own unit.
func (e *QuotaError) Error() string {
	switch e.Resource {
	case QuotaResourceOwnerSandboxes:
		owner := e.Owner
		if owner == "" {
			owner = "the unnamed owner"
		}
		return fmt.Sprintf("sandbox: owner %s already holds %d of the %d sandboxes this node allows one owner",
			owner, e.Used, e.Limit)
	case QuotaResourceNodeMemoryMB:
		return fmt.Sprintf("sandbox: this node has %d MiB of %d MiB of sandbox memory in use; the %d MiB this sandbox asked for does not fit",
			e.Used, e.Limit, e.Requested)
	case QuotaResourceNodeDiskMB:
		return fmt.Sprintf("sandbox: this node has %d MiB of %d MiB of sandbox disk in use; the %d MiB this sandbox needs does not fit",
			e.Used, e.Limit, e.Requested)
	default:
		return fmt.Sprintf("sandbox: the %s limit is spent: %d of %d in use, %d requested",
			e.Resource, e.Used, e.Limit, e.Requested)
	}
}

// Unwrap makes errors.Is(err, ErrQuota) true for a refusal.
func (e *QuotaError) Unwrap() error { return ErrQuota }

// held is what the node's existing sandboxes already hold: one owner's share, and the
// node's memory and disk.
type held struct {
	owners    map[string]int
	memoryMB  int64
	diskBytes int64
}

// heldNow measures what this node's sandboxes hold.
//
// The records are the only ledger: the node's writer is the one that says a sandbox
// exists, so a check that could not read them has nothing to compare against and
// refuses rather than admitting against an unknown. That is the same rule the store
// itself follows, and it is why a create cannot be admitted while the node cannot say
// what it is already running.
func (m *Manager) heldNow() (held, error) {
	records, _, err := m.cfg.Store.List()
	if err != nil {
		return held{}, fmt.Errorf("sandbox: read the sandboxes this node already has: %w", err)
	}

	h := held{owners: make(map[string]int, len(records))}
	for _, rec := range records {
		if !holdsResources(rec.State) {
			continue
		}
		h.owners[rec.Owner]++
		h.memoryMB += int64(rec.MemoryMB)
		h.diskBytes += diskBytes(rec.Disk)
	}
	return h, nil
}

// holdsResources reports whether a sandbox in state s occupies the node: memory and a
// disk. A sandbox being built is about to, and one that is ready has. A failed one was
// freed by the create path and a stopped one is on its way out, so neither counts —
// and counting them would make a node that has freed everything look full.
func holdsResources(s State) bool { return s == StateCreated || s == StateReady }

// diskBytes is how much disk one sandbox's disk occupies, or zero when the record
// names none and when the file cannot be measured at all.
//
// A disk that cannot be measured contributes nothing rather than failing the check:
// it is another sandbox's file, and refusing a create because of one is a refusal the
// caller could do nothing about. The limit is a guard against spending the disk, not
// an accounting of it.
func diskBytes(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// admit decides whether one more sandbox of this size and owner fits, before anything
// is allocated for it: the create path's entry to the checks below. It returns a
// *QuotaError (which is an ErrQuota) when it does not, and any other error when it
// could not be decided.
func (m *Manager) admit(tmpl template, size sandboxSize, owner string) error {
	current, err := m.heldNow()
	if err != nil {
		return err
	}

	// The sandbox does not exist yet, so its disk is counted at what copying the
	// template's image can occupy: the image's own size. That is the most the copy can
	// take (a reflink or sparse copy takes less), which is the side to be wrong on —
	// a reservation that was too small would admit the create that fills the disk.
	reservation, err := diskReservation(tmpl.artifact.Path)
	if err != nil {
		return err
	}
	return m.admitOne(current, size, reservation, owner)
}

// admitClones decides whether count clones of one image fit, before any of them exists.
//
// A clone is admitted like any other sandbox, and the numbers come from the image it
// resumes rather than from a template: the memory it will occupy is the memory the image
// is (a memory image is exactly as large as the guest's memory, which is why a fork does
// not choose its own size), and the disk it takes is the copy it makes of the checkpoint's
// disk — the same rule the create path applies to the artifact it copies. The chunks a
// memory image is stored in are deliberately not counted: they belong to the checkpoint
// and are shared by every clone, so a fork adds no chunk bytes.
//
// The whole fork is admitted or none of it is. The ledger is advanced as each clone is
// admitted, so a fork of ten is refused when the third of them would not fit — and it is
// refused *before* any of them exists, which is the point: no record, no namespace, no
// disk and no staged image for a fork the node will not take.
func (m *Manager) admitClones(size sandboxSize, diskImage string, count int, owner string) error {
	current, err := m.heldNow()
	if err != nil {
		return err
	}
	// The image a clone copies is the checkpoint's disk. A checkpoint without one cannot
	// be reserved for, and the fork is refused rather than admitted against a number
	// nobody has.
	reservation, err := diskReservation(diskImage)
	if err != nil {
		return err
	}

	owner = strings.TrimSpace(owner)
	for i := 0; i < count; i++ {
		if err := m.admitOne(current, size, reservation, owner); err != nil {
			return err
		}
		current = current.plusOne(size, reservation, owner)
	}
	return nil
}

// admitOne decides whether one more sandbox of this shape fits against a ledger, which is
// the node as it was measured plus whatever this call has already reserved for itself.
func (m *Manager) admitOne(current held, size sandboxSize, reservation int64, owner string) error {
	quota := m.cfg.Quota

	owner = strings.TrimSpace(owner)
	ownersShare := int64(current.owners[owner])

	// The order is fixed and the first refusal is the one reported, so a caller with
	// more than one problem is told which to act on first.
	if ownersShare+1 > int64(quota.MaxPerOwner) {
		return m.refuse(QuotaError{
			Resource: QuotaResourceOwnerSandboxes,
			Owner:    owner,
			Limit:    int64(quota.MaxPerOwner),
			Used:     ownersShare,
			// The unit of a sandbox count is sandboxes, and this create is one.
			Requested: 1,
		})
	}

	if memory := current.memoryMB + int64(size.memoryMB); memory > int64(quota.MemoryMB) {
		return m.refuse(QuotaError{
			Resource:  QuotaResourceNodeMemoryMB,
			Limit:     int64(quota.MemoryMB),
			Used:      current.memoryMB,
			Requested: int64(size.memoryMB),
		})
	}

	if disk := current.diskBytes + reservation; disk > quota.diskBudget() {
		return m.refuse(QuotaError{
			Resource:  QuotaResourceNodeDiskMB,
			Limit:     int64(quota.DiskMB),
			Used:      mibCeil(current.diskBytes),
			Requested: mibCeil(reservation),
		})
	}

	return nil
}

// plusOne is the ledger as it would be if one more sandbox of this shape were admitted.
// The owner map is copied rather than advanced in place: a ledger is a measurement of what
// a node holds, and a reservation being considered must not rewrite it.
func (h held) plusOne(size sandboxSize, diskBytes int64, owner string) held {
	next := h
	next.owners = make(map[string]int, len(h.owners)+1)
	for id, count := range h.owners {
		next.owners[id] = count
	}
	next.owners[owner]++
	next.memoryMB += int64(size.memoryMB)
	next.diskBytes += diskBytes
	return next
}

// mibCeil rounds a byte count up to whole MiB, for a report in the limit's own unit.
// Rounding up rather than down keeps the reported figures from adding up to less than
// the limit while the node refuses the request: a caller should be able to see that
// what is in use plus what it asked for is more than the limit, not have to trust it.
func mibCeil(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	return (bytes + (1 << 20) - 1) >> 20
}

// refuse stamps a refusal with the node's retry hint and reports it.
func (m *Manager) refuse(refusal QuotaError) error {
	refusal.RetryAfter = m.cfg.Quota.RetryAfter
	return &refusal
}

// diskReservation is the disk one sandbox is about to occupy: the size of the image it
// copies — the template's artifact for a create, the checkpoint's disk for a clone. An
// image that cannot be measured cannot be reserved for, so the sandbox is refused rather
// than admitted against a number nobody has.
func diskReservation(image string) (int64, error) {
	info, err := os.Stat(image)
	if err != nil {
		return 0, fmt.Errorf("sandbox: measure the image %s a sandbox would copy, to reserve its disk: %w", image, err)
	}
	return info.Size(), nil
}
