// Launchers: the seam between a snapshot and the Firecracker process that loads
// it.
//
// Today the restore path starts Firecracker itself (Manager.RestoreFromSnapshotWithLog),
// which means the way an instance is started is baked into the package that
// stores checkpoints. The sandbox work needs two ways to start the same
// checkpoint — directly, as the daemon's child, for dev and as the behaviour
// that exists now, and through the jailer, inside a chroot with a per-instance
// uid, cgroup and network namespace (RFC D3) — and it needs the two to be
// interchangeable at the call site. That is what this interface is for: a caller
// holds a Launcher, and which launcher it holds is a wiring decision, not a
// second restore implementation.
//
// The interface describes a launch, not a store: it never touches snapshot
// metadata, checksums or the restored-instance registry. Those stay with the
// manager, which is also what owns the per-task lock a restore and a stop share.
package snapshot

import "context"

// LaunchMode says how a launch asks an instance to enter the world.
//
// The mode is a property of the request, not of the checkpoint: the same state
// and memory files are ModeRestore for the instance that produced them and
// ModeFork for a second instance that must not share the first one's disk. A
// launcher decides where the disk and memory come from by the mode, so a request
// that names the wrong one produces an instance with another instance's writes
// instead of an error.
type LaunchMode string

const (
	// ModeCold boots an instance from its kernel and rootfs with no checkpoint
	// involved: a template build, or a plain `vm create`.
	ModeCold LaunchMode = "cold"
	// ModeRestore resumes an instance from its own checkpoint. State and Mem are
	// that instance's images and Rootfs is the disk it recorded, so writing to
	// that disk is writing to the instance's own disk.
	ModeRestore LaunchMode = "restore"
	// ModeFork resumes a checkpoint as a new instance — a template, or a sibling
	// taken as a starting point — so the instance needs its own copy of the disk
	// and must never write the recorded path.
	ModeFork LaunchMode = "fork"
)

// LaunchRequest is everything a launcher needs to start one instance.
//
// The paths in it are host paths: the files an instance is started from. A
// launcher that runs Firecracker inside a jail copies them to the chroot paths a
// checkpoint records (the RFC's /kernel/vmlinux, /drives/rootfs.ext4,
// /snapshot/vm.state) and reports those back in Launched.StatePath and
// Launched.MemPath, because those are what /snapshot/load is told. So one request
// describes one instance to either launcher; where the instance sees its files is
// the launcher's decision, not the caller's.
type LaunchRequest struct {
	// InstanceID names the instance. The launcher derives its per-instance host
	// paths from it (<...>/<InstanceID>.sock), so it must be one safe path
	// element — runtime.CheckTaskID is the rule — and its uniqueness is the
	// caller's to guarantee: nothing here refuses a second launch under an ID
	// that is already running.
	InstanceID string

	// Mode is how the instance enters the world: cold, restore or fork.
	Mode LaunchMode

	// Kernel is the kernel image to boot. A restore carries the kernel in its
	// checkpoint state and ignores it.
	Kernel string

	// Rootfs is the disk the instance boots from or resumes onto. For ModeFork it
	// must be the instance's own copy, never the checkpoint's recorded path.
	Rootfs string

	// State is the checkpoint's Firecracker state file (vm.state): devices,
	// kernel and vCPU state. Empty for ModeCold.
	State string

	// Mem is the checkpoint's memory image (vm.mem). Empty for ModeCold.
	Mem string

	// VCPUs and MemoryMB are the instance's size. A restore takes both from the
	// checkpoint state and ignores them.
	VCPUs    int
	MemoryMB int

	// JailUID and JailGID are the uid and gid the VMM runs as. Zero means no
	// jailer, which is the direct (dev) path; a jailed launcher refuses a zero uid
	// because it would run the VMM as root, and reads a zero gid as "the same
	// number as the uid".
	JailUID int
	JailGID int

	// NetNS is the network namespace to start the VMM in, by name or path. Empty
	// means the node's own namespace (the shared bridge path).
	NetNS string
}

// Launched is a running instance: the host paths that reach it and the one way
// to end it.
//
// It is returned only for an instance that is running — the VMM is up and, for a
// resume, the checkpoint is loaded and the guest has been resumed — so a caller
// may dial APISocket as soon as Start returns. Paths the launcher did not choose
// or does not know are empty rather than guessed: a guessed vsock muxer path
// would send the guest agent to a socket nobody listens on.
type Launched struct {
	// APISocket is the Firecracker API socket the instance listens on.
	APISocket string

	// VsockPath is the vsock muxer the guest agent answers on. A launcher that
	// configured no vsock device leaves it empty, as does one that resumed a
	// checkpoint whose recorded muxer path it has no way to read; the caller that
	// knows the device configuration (or the registry a restore writes) supplies
	// it.
	VsockPath string

	// ConsolePath is the serial console file the instance writes to. Empty when
	// the console is detached.
	ConsolePath string

	// StatePath and MemPath are the checkpoint images the instance loaded. Both
	// are empty for a cold boot.
	StatePath string
	MemPath   string

	// Stop ends the instance. It is the only handle the launcher returns for the
	// process, so a caller that never calls it leaks the VM. It is idempotent:
	// stopping an instance whose process already exited is success, which is what
	// makes a retried teardown safe. The context bounds the graceful window, not
	// the kill.
	Stop func(context.Context) error
}

// Launcher starts one instance and hands back what is needed to reach and end it.
//
// Two implementations are the design (RFC D3): DirectLauncher, which starts
// Firecracker as the daemon's child — the dev path, and what the restore path
// does today — and the jailed launcher, which stages the same files inside a
// chroot and starts the VM through the jailer with its own uid, cgroup and
// network namespace. Callers hold this interface so that moving an instance from
// one to the other is a choice made where launchers are constructed.
//
// Every implementation must keep this contract:
//
//   - Start returns a Launched only for an instance that is running. A resume
//     whose memory backend has not been loaded yet is not running.
//   - A failed Start leaves nothing behind: no process, no socket, no cgroup, no
//     chroot and no netns. The caller has no handle to clean up with, and the
//     next launch under the same ID must not inherit the failure.
//   - Stop is the only way an instance ends, and it is idempotent.
//   - Start and Stop do not serialize themselves per instance. A caller that can
//     race a launch and a stop of the same instance (the manager can: a restore
//     and a stop share one task's socket) holds its own lock.
//   - The request's paths mean whatever the launcher's namespace makes them, so a
//     caller builds them for the launcher it holds.
type Launcher interface {
	// Start launches the instance the request describes and returns it running.
	Start(ctx context.Context, req LaunchRequest) (Launched, error)
}
