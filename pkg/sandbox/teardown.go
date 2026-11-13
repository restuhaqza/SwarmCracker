package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// The teardown side of a sandbox.
//
// A sandbox is more resources than a VM: a process inside a jail, a cgroup under
// that jail's parent, a network namespace with a tap and a policy in it, a disk of
// its own on the host, a suffix of a credential in two places, and a record. rm is
// the promise that all of them are gone, and this file is that promise: it stops the
// process first, frees the rest, and deletes the record only after everything the
// record names is gone — because the record is the only thing that knows those names.
//
// It is why a failed create leaves a record rather than nothing, and why a teardown
// that could not finish keeps the record: the file is the handle the next attempt
// works from.

const (
	// stopGrace is how long a VMM is given to exit after SIGTERM before it is
	// killed. A microVM has no shutdown sequence of its own to wait for — the guest
	// is not asked, the VMM simply goes — so this is room for the process to be
	// reaped, not a graceful shutdown.
	stopGrace = 5 * time.Second

	// stopPoll is how often a teardown asks whether the process is gone.
	stopPoll = 25 * time.Millisecond

	// cgroupBase is the cgroup v2 hierarchy the jailer creates an instance's cgroup
	// under (snapshot.JailedLauncher's jailedCgroupBase). It is the node's one
	// hierarchy, not configuration.
	cgroupBase = "/sys/fs/cgroup"
)

// Freed is what one teardown took back, named resource by resource so a caller can
// see that nothing was left. A field is empty when the sandbox never had that
// resource, and a field is set only when this call is what freed it: a retried rm
// reports what the first attempt freed as already gone.
type Freed struct {
	// ID is the sandbox the report is about.
	ID string
	// PID is the VMM process that was stopped, and Process reports whether there
	// was one to stop.
	PID     int
	Process bool
	// Socket is the Firecracker API socket that was removed.
	Socket string
	// Cgroup is the instance's cgroup directory that was removed.
	Cgroup string
	// JailRoot is the jail directory that was removed.
	JailRoot string
	// NetNS is the network namespace that was removed with the TAP device, the
	// addresses, the routes and the nftables policy inside it.
	NetNS string
	// TAP is the device the namespace held.
	TAP string
	// Disk is the sandbox's own disk that was deleted.
	Disk string
	// TokenFile is the host copy of the sandbox's agent token that was deleted.
	TokenFile string
	// APITokens is how many API tokens scoped to this sandbox were revoked.
	APITokens int
	// InstanceRecord reports whether the node's instance registry had a record for
	// the sandbox and this call deleted it.
	InstanceRecord bool
	// Record reports whether the sandbox record itself was deleted.
	Record bool
}

// Remove ends a sandbox and frees everything it holds.
//
// The order is the contract:
//
//  1. The VMM is stopped. If it cannot be stopped, nothing is written and nothing
//     else is freed: the namespace, disk and jail are this running VM's, and taking
//     them from under it would be the opposite of a teardown. The record stays the
//     handle to a sandbox that is still running.
//  2. The record becomes stopped. This is written now, before the rest is freed, so
//     a teardown that dies half-way leaves "this node ended it" rather than "this
//     sandbox is ready" pointing at resources that are going away.
//  3. Everything else is freed: the socket, the cgroup, the jail, the namespace
//     (with the tap and the policy in it), the disk, the host's copy of the agent
//     token, the node's instance record, and the API tokens scoped to this sandbox.
//  4. The record is deleted. It is the last step because it is the only list of what
//     the previous three steps had to free.
//
// A sandbox that is already stopped is not an error: rm is retried, and a retried rm
// finishes a teardown rather than refusing it.
func (m *Manager) Remove(ctx context.Context, id string) (Freed, error) {
	// One teardown at a time, for the same reason one create at a time: the disk,
	// the jail and the namespace a teardown frees are named by an ID, and two
	// teardowns running at once would race to free the same names while a create
	// could be handing them to a new sandbox.
	m.slots <- struct{}{}
	defer func() { <-m.slots }()

	rec, err := m.Get(id)
	if err != nil {
		return Freed{ID: id}, err
	}

	freed := Freed{ID: rec.ID}
	if err := m.stopVM(ctx, &rec, &freed); err != nil {
		return freed, err
	}

	if rec.State != StateStopped {
		if !CanTransition(rec.State, StateStopped) {
			return freed, fmt.Errorf("sandbox %s: %s cannot become %s", rec.ID, rec.State, StateStopped)
		}
		rec.State = StateStopped
		rec.Updated = m.now().UTC()
		if err := m.cfg.Store.Put(rec); err != nil {
			return freed, err
		}
	}

	if err := m.freeResidual(ctx, &rec, &freed); err != nil {
		return freed, err
	}

	if m.cfg.RevokeTokens != nil {
		n, err := m.cfg.RevokeTokens(ctx, rec.ID)
		freed.APITokens = n
		if err != nil {
			// The sandbox is gone but a credential that names it is not: that is a
			// failure to report, and the record stays so the next rm retries it.
			return freed, fmt.Errorf("revoke the API tokens scoped to sandbox %s: %w", rec.ID, err)
		}
	}

	if err := m.cfg.Store.Delete(rec.ID); err != nil {
		return freed, err
	}
	freed.Record = true

	log.Info().
		Str("sandbox_id", freed.ID).
		Int("pid", freed.PID).
		Str("netns", freed.NetNS).
		Str("cgroup", freed.Cgroup).
		Str("jail_root", freed.JailRoot).
		Str("disk", freed.Disk).
		Bool("instance_record", freed.InstanceRecord).
		Int("api_tokens", freed.APITokens).
		Msg("Sandbox is removed")

	return freed, nil
}

// free frees everything a sandbox holds without changing its record. It is the
// teardown a failed create runs, and it is idempotent: every step tolerates work
// another call already did.
func (m *Manager) free(ctx context.Context, rec *Record) (Freed, error) {
	freed := Freed{ID: rec.ID}
	if err := m.stopVM(ctx, rec, &freed); err != nil {
		// The process is the resource that makes the rest unsafe to free.
		return freed, err
	}
	return freed, m.freeResidual(ctx, rec, &freed)
}

// stopVM stops the sandbox's VMM and removes what is only reachable through the
// process: its API socket, its cgroup and its jail. It reports what it removed in
// freed.
//
// The process is named by the node's instance registry, not by the sandbox record,
// because the registry is where a VMM's pid is a fact: the launcher wrote it there
// before the guest was reachable, and the record a control plane keeps does not
// guess at a pid. A sandbox with no instance record never got as far as a VMM, and
// stopping it is nothing to do.
func (m *Manager) stopVM(ctx context.Context, rec *Record, freed *Freed) error {
	if m.cfg.Instances != nil {
		inst, found, err := m.cfg.Instances.Peek(rec.ID)
		switch {
		case err != nil:
			return fmt.Errorf("sandbox %s: read the instance record: %w", rec.ID, err)
		case found:
			if inst.PID > 0 {
				if err := stopVMM(inst.PID, stopGrace); err != nil {
					return fmt.Errorf("sandbox %s: stop its VMM (pid %d): %w", rec.ID, inst.PID, err)
				}
				freed.PID, freed.Process = inst.PID, true
			}
			if inst.APISocket != "" {
				if err := removeFile(inst.APISocket); err != nil {
					return fmt.Errorf("sandbox %s: remove its API socket %s: %w", rec.ID, inst.APISocket, err)
				}
				freed.Socket = inst.APISocket
			}
		}
	}

	// The host's copy of the agent token sits beside the muxer inside the jail, so it
	// goes before the jail does — otherwise the jail's removal would be what removed
	// it, and this report would claim a step it did not take. A credential that
	// outlives the thing it authenticates is the leftover this file exists to prevent.
	if rec.AgentTokenFile != "" {
		if err := removeFile(rec.AgentTokenFile); err != nil {
			return fmt.Errorf("sandbox %s: remove its agent token %s: %w", rec.ID, rec.AgentTokenFile, err)
		}
		freed.TokenFile = rec.AgentTokenFile
	}

	// The jail holds the guest's disk, its memory-bearing VMM and the only copy of
	// Firecracker's own log. Removing it is the end of everything the jail was.
	if rec.JailRoot != "" {
		if err := os.RemoveAll(rec.JailRoot); err != nil {
			return fmt.Errorf("sandbox %s: remove its jail %s: %w", rec.ID, rec.JailRoot, err)
		}
		freed.JailRoot = rec.JailRoot
	}

	cgroup := m.cgroupPath(rec.ID)
	removed, err := removeCgroup(cgroup)
	if err != nil {
		return fmt.Errorf("sandbox %s: remove its cgroup %s: %w", rec.ID, cgroup, err)
	}
	if removed {
		freed.Cgroup = cgroup
	}
	// The jailer creates the parent cgroup too, so a node that runs one instance at
	// a time would collect an empty parent per launch. Removing it is best effort:
	// it fails while another sandbox still lives in it, which is exactly when it
	// must stay.
	_, _ = removeCgroup(filepath.Dir(cgroup))

	return nil
}

// freeResidual frees what a sandbox holds beyond its process: its network, its
// disk, the host's copy of its token, and the node's instance record.
func (m *Manager) freeResidual(ctx context.Context, rec *Record, freed *Freed) error {
	var problems []error

	// Detach is the inverse of the attach that built the sandbox: it closes this
	// sandbox's egress endpoints before it removes the namespace, and removing the
	// namespace is removing the tap, its addresses, its routes and the nftables
	// policy in it — there is nothing else to delete. It is idempotent, so a
	// teardown that found the namespace already gone is a success.
	if err := m.cfg.Network.Detach(ctx, rec.ID); err != nil {
		problems = append(problems, fmt.Errorf("sandbox %s: detach its network: %w", rec.ID, err))
	} else if rec.NetNS != "" {
		freed.NetNS, freed.TAP = rec.NetNS, rec.TAP
	}

	// The instance record names the netns and the disk, so the registry's own
	// teardown is what frees them and then deletes the record. It refuses a live VM,
	// which is why this runs after the process is gone.
	if m.cfg.Instances != nil {
		found, err := vminstance.Teardown(m.cfg.Instances, rec.ID, vminstance.Leftovers{})
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf("sandbox %s: release the instance record: %w", rec.ID, err))
		case found:
			freed.InstanceRecord = true
		}
	}

	// A sandbox whose create failed before a launch never wrote an instance record,
	// so its disk is not named by one. Removing it here is idempotent, and it is the
	// same file the registry's teardown would have freed.
	if rec.Disk != "" {
		if err := removeFile(rec.Disk); err != nil {
			problems = append(problems, fmt.Errorf("sandbox %s: remove its disk %s: %w", rec.ID, rec.Disk, err))
		} else {
			freed.Disk = rec.Disk
		}
	}

	return errors.Join(problems...)
}

// cgroupPath is the cgroup the jailer created for one instance:
// <cgroupBase>/<ParentCgroup>/<id>. It mirrors the jailer's layout, which is why
// ParentCgroup must be the value the launcher was given.
func (m *Manager) cgroupPath(id string) string {
	return filepath.Join(cgroupBase, m.cfg.ParentCgroup, id)
}

// stopVMM ends one sandbox's VMM process. It is a seam for the same reason the
// launcher's own are: a unit test cannot make a process refuse to die (a root-owned
// test can signal anything), so it records the pid and returns the refusal, which is
// the one path that must not free a live VM's resources. Production never replaces it.
var stopVMM = stopProcess

// stopProcess ends one process and waits for it to be gone: SIGTERM, a bounded
// grace, SIGKILL, and then the wait a SIGKILL needs. It is idempotent — a process
// that is already gone is success — because a teardown is retried.
func stopProcess(pid int, grace time.Duration) error {
	if pid <= 0 || !vminstance.ProcessAlive(pid) {
		return nil
	}

	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !vminstance.ProcessAlive(pid) {
			return nil
		}
		time.Sleep(stopPoll)
	}

	_ = syscall.Kill(pid, syscall.SIGKILL)
	// A killed process is gone from the scheduler but its /proc entry can outlive
	// the signal by a moment; the wait is bounded so a process that refuses to die
	// is reported rather than hung on.
	for i := 0; i < 200; i++ {
		if !vminstance.ProcessAlive(pid) {
			return nil
		}
		time.Sleep(stopPoll)
	}
	return fmt.Errorf("process %d is still running after SIGKILL", pid)
}

// removeFile removes one file, treating an absent one as done.
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// removeCgroup removes one cgroup directory and reports whether there was one. An
// absent directory is done, and a directory the kernel still holds (a process in it, a
// controller reference) is the caller's to report.
//
// "Whether there was one" is the answer a teardown report needs: an instance whose
// launcher created no cgroup has none to free, and saying it removed one would be a
// claim it cannot support.
func removeCgroup(path string) (bool, error) {
	err := os.Remove(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case errors.Is(err, syscall.EBUSY), errors.Is(err, syscall.ENOTEMPTY):
		// A cgroup with a process in it, or with a child, is still in use: the
		// caller's before-the-fact check (the process is gone) makes this the rare
		// case, and saying so is better than pretending it was removed.
		return false, fmt.Errorf("cgroup %s is still in use: %w", path, err)
	default:
		return false, err
	}
}
