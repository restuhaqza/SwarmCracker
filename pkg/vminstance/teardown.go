package vminstance

// The teardown side of the registry.
//
// A record is written before a VM is reachable and must outlive its VM exactly
// long enough for the resources the VM left behind to be freed: the netns, the TAP
// device and the disk are named only by the record, so a teardown that started
// from a task ID would guess at names the VM was not given. This file is that
// teardown: it frees what the record names, and deletes the record only once they
// are gone.
//
// It is why Recover keeps a stale record instead of deleting it. A record whose
// process is gone is the only handle to those resources; the startup pass reports
// it, and the teardown here is what a stop path, a SwarmKit remove, or a retried
// cleanup runs to free them and then drop the record.

import (
	"errors"
	"fmt"
	"os"

	"github.com/restuhaqza/swarmcracker/pkg/network"
)

// Leftovers frees the host resources one record names. Its fields are the seam a
// test replaces: the production values are the node's own removers, and a test
// asserts which names were freed without touching a host device.
type Leftovers struct {
	// DeleteTap removes the recorded TAP device. Nil selects
	// network.RemoveTAPDevice, which treats a device that is already gone as
	// success.
	DeleteTap func(name string) error
	// DeleteNetNS removes the recorded network namespace. Nil selects
	// network.RemoveNetNS, which treats a namespace that is already gone as
	// success.
	DeleteNetNS func(name string) error
	// DeleteDisk removes the recorded disk. Nil selects os.Remove, which ignores
	// a file that is already gone.
	DeleteDisk func(path string) error
}

// Release frees every resource inst names and reports all the failures it hit. A
// record that names nothing frees nothing: the paths are optional by construction,
// and a VM on the shared bridge with a shared disk names neither.
//
// The removers are idempotent, so a retried release is safe; a real failure is
// returned rather than logged, because the record is kept when one happens and the
// caller is the one that can decide whether to retry.
func (l Leftovers) Release(inst Instance) error {
	var problems []error

	if inst.TAP != "" {
		if err := l.deleteTap(inst.TAP); err != nil {
			problems = append(problems, fmt.Errorf("release the TAP device %s of instance %q: %w", inst.TAP, inst.ID, err))
		}
	}
	if inst.NetNS != "" {
		if err := l.deleteNetNS(inst.NetNS); err != nil {
			problems = append(problems, fmt.Errorf("release the netns %s of instance %q: %w", inst.NetNS, inst.ID, err))
		}
	}
	if inst.Disk != "" {
		if err := l.deleteDisk(inst.Disk); err != nil && !errors.Is(err, os.ErrNotExist) {
			problems = append(problems, fmt.Errorf("release the disk %s of instance %q: %w", inst.Disk, inst.ID, err))
		}
	}

	return errors.Join(problems...)
}

// deleteTap is the TAP remover this release uses.
func (l Leftovers) deleteTap(name string) error {
	if l.DeleteTap != nil {
		return l.DeleteTap(name)
	}
	return network.RemoveTAPDevice(name)
}

// deleteNetNS is the namespace remover this release uses.
func (l Leftovers) deleteNetNS(name string) error {
	if l.DeleteNetNS != nil {
		return l.DeleteNetNS(name)
	}
	return network.RemoveNetNS(name)
}

// deleteDisk is the disk remover this release uses.
func (l Leftovers) deleteDisk(path string) error {
	if l.DeleteDisk != nil {
		return l.DeleteDisk(path)
	}
	return os.Remove(path)
}

// Teardown frees the resources the record for id names and then deletes the
// record. found is false when the node has no record for id, which is success: a
// retried teardown and a VM that never got as far as a record both reach here with
// nothing to do.
//
// The order is the contract. The record is deleted only after every resource is
// gone, because it is the only thing that knows their names; when a release fails,
// the record stays and the error says what is still held, so the next attempt has
// the same handle this one did.
//
// A VM that is still running is refused, and its record is left alone: freeing the
// netns, TAP device and disk out from under a live VM is the opposite of a
// teardown, and stopping it is the stop path's job. So the liveness question is
// asked first (Get refuses a dead record), and only a record whose VM is gone is
// read again — with Peek, which does not filter — and released.
func Teardown(store Registry, id string, leftovers Leftovers) (bool, error) {
	if store == nil {
		return false, nil
	}

	if _, live, err := store.Get(id); err != nil {
		return false, fmt.Errorf("read the instance record of %q for teardown: %w", id, err)
	} else if live {
		return false, fmt.Errorf("instance %q is still running: stop it before tearing its resources down", id)
	}

	inst, found, err := store.Peek(id)
	if err != nil {
		return false, fmt.Errorf("read the instance record of %q for teardown: %w", id, err)
	}
	if !found {
		return false, nil
	}

	if err := leftovers.Release(inst); err != nil {
		return true, err
	}
	if err := store.Delete(id); err != nil {
		return true, fmt.Errorf("delete the instance record of %q: %w", id, err)
	}
	return true, nil
}
