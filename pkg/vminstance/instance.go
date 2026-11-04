// Package vminstance keeps the node's registry of the microVMs it is running.
//
// Every host-side fact about a running VM — which process is the VMM, where its
// API socket and vsock muxer are, which console, netns and TAP device belong to
// it — is otherwise re-derived from a task ID and a directory glob. That
// derivation is a guess: it holds for the VMs the node booted itself from a
// SwarmKit task, and it silently resolves nothing for a pooled, restored or
// forked VM, whose paths are not a function of any task ID (pkg/snapshot's
// restored-instance records exist precisely because a restored VM keeps the
// source task's vsock muxer).
//
// This package replaces the guess with a record. One Instance is one running
// VM's host identity, written before the VM is reachable and deleted only after
// its process, netns, cgroup and disk are gone. Readers ask the registry
// instead of deriving: the broker's resolver, console attach, health, metadata
// and `vm list` read a record first, and fall back to the derivation only for
// legacy task VMs, which have none.
//
// # The filesystem is the state
//
// A record is one JSON file, <dir>/<id>.json, and that file is the only state:
// nothing is cached in memory, so a record written by the daemon that started a
// VM is visible to the daemon that restarts after it with no reconciliation
// step in between. Put writes a unique temporary beside the record, fsyncs it,
// renames it into place and fsyncs the directory, so a reader sees either the
// old record or the new one — never a partial one — and a crash leaves either
// no record or a complete record. A temporary a crash left behind is named so
// that neither a lookup nor a listing can mistake it for a record, and Recover
// removes it.
//
// # One writer
//
// The node's daemon is the only writer, which is what makes a lock unnecessary:
// the record file is the state and there is no in-memory map to keep consistent
// with it. Two Puts for one ID write different temporaries and the last rename
// wins, which is the only order any lock could have given them.
//
// # A record must not resolve to a dead process
//
// A record exists to be dialled, so a record whose VMM is gone is worse than no
// record: it would hand a caller a socket nobody listens on and make a dead VM
// look alive. Put refuses to write one, and Get and List refuse to return one.
// Recover is the load-time pass over the whole directory: it reports the stale
// records a previous daemon life left behind, and the temporaries a crash left
// behind, without deleting the stale records themselves — those name the netns,
// TAP device and disk of a VM that died outside a stop path, so the file is the
// only handle to the resources still to be torn down, and deleting it here would
// lose that work.
//
// Liveness is the PID the record names, and a PID is only as durable as the boot
// that assigned it: after a reboot the kernel may hand the same number to an
// unrelated process, and the record then looks alive. Nothing in the record can
// tell the two apart, so a caller that must be sure compares Booted with the
// node's own boot time before trusting a record from a previous life.
package vminstance

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// The kinds of owner a running VM can have. The kind is what lets a reader tell
// a SwarmKit task (which keeps the legacy filename derivation as a fallback)
// from a pooled or sandbox VM (which has no task ID to derive anything from).
const (
	// KindSandbox is a VM an agent sandbox owns: the sandbox ID is the
	// identity, nothing about it is a SwarmKit task.
	KindSandbox = "sandbox"
	// KindPool is a warm VM a pool holds before it is leased to a sandbox.
	KindPool = "pool"
	// KindTask is a VM serving a SwarmKit task: a service replica, or a
	// `vm create` VM.
	KindTask = "task"
)

// Instance is the node's record of one running microVM: the host-side identity
// every other component reads instead of re-deriving. It is a node-local
// record, not a public document — it holds host paths and PIDs and never leaves
// the node, so it has no v1 envelope.
type Instance struct {
	// ID is the instance's identity and names its record file. It is the
	// sandbox ID for KindSandbox, the pool slot for KindPool and the task ID
	// for KindTask, and it is the only name a caller needs to resolve the VM.
	ID string `json:"id"`
	// Kind is KindSandbox, KindPool or KindTask: what owns the VM.
	Kind string `json:"kind"`
	// OwnerID is the owner behind the kind — a sandbox's owner, a task's
	// service — kept so a record can be traced back without another lookup.
	// It is empty when the VM has no owner of that kind.
	OwnerID string `json:"ownerId,omitempty"`
	// NodeID is the SwarmKit node the VM runs on. It is empty for a VM that is
	// not part of a cluster.
	NodeID string `json:"nodeId,omitempty"`
	// PID is the VMM process. Its exit is what makes the record stale, so it
	// must name the process whose death means the VM is gone.
	PID int `json:"pid"`
	// APISocket is the VM's Firecracker API socket. It is authoritative: a
	// restored VM's socket is chosen for it and no rule can derive it.
	APISocket string `json:"apiSocket"`
	// VsockPath is the vsock muxer the guest agent answers on. It is empty for
	// a VM without an agent (a golden/template VM before the agent lands).
	VsockPath string `json:"vsockPath,omitempty"`
	// Console is the host path of the guest's console. It is empty for a VM
	// started without one.
	Console string `json:"console,omitempty"`
	// Metadata is the host path of the runtime metadata the daemon publishes for
	// this VM: the sidecar that carries the guest's addresses and the live
	// healthcheck verdict (see runtime.VMMetadata). It is a path the reader needs
	// and cannot derive from the instance ID — the sidecar is named by whoever
	// writes it — so the record states it. It is empty for a VM nobody publishes
	// metadata for, which is every VM that is not a daemon-managed task.
	Metadata string `json:"metadata,omitempty"`
	// NetNS is the network namespace the VM is isolated in. It is empty for a
	// VM on the shared bridge.
	NetNS string `json:"netns,omitempty"`
	// TAP is the host-side TAP device the VM's first interface is attached to.
	TAP string `json:"tap,omitempty"`
	// Disk is the disk the VM boots from, when the VM owns it outright: the
	// per-task rootfs copy a teardown may delete. It is empty for a VM booting a
	// disk it shares — an image cache, a golden artifact — because freeing one of
	// those would break every other VM that boots from it, and for a VM whose disk
	// the node does not manage.
	Disk string `json:"disk,omitempty"`
	// GuestIP is the guest's address. It is empty when the VM has no network.
	GuestIP string `json:"guestIp,omitempty"`
	// MAC is the guest's L2 address. A restored VM keeps the address recorded
	// in its snapshot, so it cannot be derived from the instance ID.
	MAC string `json:"mac,omitempty"`
	// JailUID is the uid the jailer runs the VM as. Zero means the VM is not
	// jailed, which is the direct (dev) path.
	JailUID int `json:"jailUid,omitempty"`
	// Booted is when the VM was booted. It is set by the caller, not by the
	// registry, so re-recording an instance does not move its age.
	Booted time.Time `json:"booted"`
	// Ready is set once the VM answered the readiness check the kind requires
	// (agent hello, or a reachable API socket). It is false while the VM is
	// still booting and its record is already written: the paths are true
	// before the VM is reachable, which is the whole point of writing first.
	Ready bool `json:"ready,omitempty"`
}

// Recorder is the write side of the registry: what a path that starts a VM
// needs to publish its identity. *Store is the production implementation; the
// interface lets a VMM manager be tested without a directory and lets a caller
// that cannot keep a registry (a `vm create` as a non-root user) run without one.
//
// A nil Recorder means "this caller keeps no registry": nothing is written and
// resolution falls back to the filename derivation, which is the behaviour every
// node had before the registry existed.
type Recorder interface {
	Put(Instance) error
}

// Registry is the registry as a path that starts and ends a VM reads it: Put when
// the VM exists, Get and Peek when its teardown runs, and Delete once its
// resources are free.
//
// The two reads are both needed, and they answer different questions. Get refuses
// a record whose process is gone — a record exists to be dialled — so a teardown
// uses it to ask "is this VM still running?" and refuses to free a live VM's
// resources. Peek reads the record without that filter, because the record of a VM
// that is already gone is exactly the handle to the netns, TAP device and disk
// that still have to be freed.
//
// *Store is the production implementation. A nil Registry means the caller keeps
// no registry, and every write and teardown is skipped.
type Registry interface {
	Recorder
	Get(id string) (Instance, bool, error)
	Peek(id string) (Instance, bool, error)
	Delete(id string) error
}

// validate refuses a record the registry could not serve. The rules are the
// ones a reader depends on: an ID that names a file inside the registry, a kind
// a reader can act on, a process to watch, and absolute host paths — a relative
// path would resolve against whatever working directory the reader happens to
// have.
func (i Instance) validate() error {
	if err := instanceIDProblem(i.ID); err != nil {
		return err
	}

	switch i.Kind {
	case KindSandbox, KindPool, KindTask:
	case "":
		return fmt.Errorf("kind is empty: want %s, %s or %s", KindSandbox, KindPool, KindTask)
	default:
		return fmt.Errorf("kind %q is not an instance kind: want %s, %s or %s", i.Kind, KindSandbox, KindPool, KindTask)
	}

	if i.PID <= 0 {
		return fmt.Errorf("pid is %d: want the VMM process the record describes", i.PID)
	}

	if err := hostPathProblem("apiSocket", i.APISocket, true); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"vsockPath", i.VsockPath},
		{"console", i.Console},
		{"metadata", i.Metadata},
		{"netns", i.NetNS},
		{"disk", i.Disk},
	} {
		if err := hostPathProblem(field.name, field.value, false); err != nil {
			return err
		}
	}

	if i.JailUID < 0 {
		return fmt.Errorf("jailUid is %d: want the jail uid, or 0 for an unjailed VM", i.JailUID)
	}
	if i.Booted.IsZero() {
		return errors.New("booted is zero: set it to the instant the VM was booted")
	}
	return nil
}

// hostPathProblem refuses a host path that cannot be used: an empty required
// path, or any path that is not absolute. An optional path may be empty, which
// is how a VM says "I have no vsock, console or netns".
func hostPathProblem(field, value string, required bool) error {
	if strings.TrimSpace(value) == "" {
		if required {
			return fmt.Errorf("%s is empty: want the VM's absolute host path", field)
		}
		return nil
	}
	if !filepath.IsAbs(value) {
		return fmt.Errorf("%s %q is relative: want an absolute host path", field, value)
	}
	return nil
}

// instanceIDProblem refuses an ID that cannot name a record file, wording the
// one rule that owns the shape (runtime.CheckTaskID) for this record's field.
// Only the sentence differs between the callers: the shape — one path element,
// no dot, no separator, bounded length — is shared, because two copies of it
// drift and the copy that drifts is the one that writes outside the directory.
func instanceIDProblem(id string) error {
	err := runtime.CheckTaskID(id)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, runtime.ErrTaskIDEmpty):
		return errors.New("id is empty: want the instance ID, which names its record file")
	case errors.Is(err, runtime.ErrTaskIDPathSeparator):
		return fmt.Errorf("id %q must not contain a path separator: it names a record file inside the registry", id)
	case errors.Is(err, runtime.ErrTaskIDDot):
		return fmt.Errorf("id %q must not contain a dot: it names a record file inside the registry", id)
	case errors.Is(err, runtime.ErrTaskIDWhitespace):
		return fmt.Errorf("id %q must not contain whitespace: it names a record file inside the registry", id)
	case errors.Is(err, runtime.ErrTaskIDTooLong):
		return fmt.Errorf("id is %d characters long: want at most %d, since it names a file", len(id), runtime.MaxIDLength)
	default:
		return err
	}
}
