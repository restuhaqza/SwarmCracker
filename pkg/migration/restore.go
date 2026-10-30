package migration

// This file owns the second half of a live migration: taking the artifact a
// target staged and starting the VM it describes as the task SwarmKit just
// created there.
//
// The restore is not a boot. Firecracker's state file carries the source VM's
// device model, so the target must recreate the host-side names that model
// records before the load, and the guest resumes with its old addresses, so it
// must be re-addressed before anything may reach it. In order:
//
//	1. validate the request (a task that already runs a restored VM is refused),
//	   then verify the staged bytes against the manifest
//	2. create the TAP device under the recorded name, attached but down
//	3. load the snapshot (which materialises the disk and resumes the guest)
//	4. wait for the guest agent behind the recorded vsock muxer
//	5. re-address the guest: flush, add the task's address, replace the default route
//	6. bring the TAP up
//	7. record which source VM this new task inherited
//
// Steps 1 and 2 are ordered by what they protect: nothing is created and no
// process starts until the bytes on disk are the ones the manifest promises, and
// the TAP exists (down) before Firecracker loads, because the state file resolves
// its interface by name and a load with the device missing fails.
//
// The TAP stays down until the guest has its new address. An up TAP before the
// re-address would put a resumed guest on the bridge with the source node's
// address still configured, which is the one instant a stale address could answer
// for the service from two nodes at once.
//
// Every effect this file performs is an injected function (RestoreDeps), because
// pkg/migration is a leaf that builds everywhere: it must not start Firecracker,
// run ip(8), or touch netlink itself. The production wiring lives with the
// executor (T7); pkg/snapshot's manager already satisfies SnapshotRestorer, and
// DialAgent below adapts pkg/agent.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

const (
	// guestInterface is the single network interface every microVM in this
	// product has. The snapshot's device model names the host-side TAP, not the
	// guest-side interface, so this is not recoverable from the artifact.
	guestInterface = "eth0"

	// guestReadyTimeout bounds how long a restore waits for the guest agent
	// after the load. A restored guest skips the boot that would otherwise pace
	// it, but its agent still has to accept a connection; past this window the
	// VM is up with no way to re-address it, so the restore must fail instead of
	// waiting forever.
	guestReadyTimeout = 30 * time.Second

	// guestReadyInterval is the pause between guest-agent dials.
	guestReadyInterval = 200 * time.Millisecond

	// guestDialTimeout bounds one dial attempt. pkg/agent already retries the
	// muxer handshake inside its own window; this is the ceiling for one call.
	guestDialTimeout = 5 * time.Second

	// guestCommandTimeout bounds one re-addressing command. A guest that stops
	// answering must not hold the restore open.
	guestCommandTimeout = 30 * time.Second

	// maxGuestDialAttempts caps the wait independently of the clock. The
	// deadline is what normally ends it; the cap makes "bounded" true even if a
	// clock is misconfigured or the process is suspended across the window.
	maxGuestDialAttempts = int(guestReadyTimeout/guestReadyInterval) + 1
)

// agentRetryWait pauses between guest-agent dials and returns early when the
// context ends. It is a variable because it is the only effect in the wait that a
// unit test cannot observe: a test that proves the deadline is honoured must not
// spend the deadline doing it.
var agentRetryWait = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RestoreInput is one restore attempt on the node that will run the task.
type RestoreInput struct {
	// Artifact is the manifest of the VM to start, as the target received it
	// from the manager.
	Artifact Artifact
	// Dir is the target's staged directory holding the three files the manifest
	// describes.
	Dir string
	// TaskID is the NEW task: the one SwarmKit created for this node. The VM it
	// runs keeps the source task's host-side identity, which is why the record
	// this restore writes names both.
	TaskID string
	// GuestCIDR is the address the task's IPAM gave it, in CIDR form, for
	// example 192.168.127.37/24.
	GuestCIDR string
	// Gateway is the bridge's address, for example 192.168.127.2.
	Gateway string
	// Bridge is the bridge the TAP device joins.
	Bridge string
	// SocketDir is the directory the restored VM's API socket goes in:
	// <SocketDir>/<TaskID>.sock. The vsock muxer path is not chosen here: it is
	// the one recorded in the artifact.
	SocketDir string
}

// SnapshotRestorer loads a snapshot into a fresh VM: it materialises
// info.RootfsPath from info.DiskPath and resumes the guest. *snapshot.Manager
// implements it, so production needs no adapter.
//
// Obligation on an error: it must not leave a process behind. A load that starts
// Firecracker and then fails — a state file it refuses halfway, a muxer it cannot
// bind — must take that process down itself before it returns, because the
// restore cannot tell from the error whether anything was started. The undo calls
// StopRestored anyway (a missing process is success), which is the second net;
// this sentence is the contract the first one holds the restorer to.
type SnapshotRestorer interface {
	RestoreFromSnapshot(ctx context.Context, info *snapshot.SnapshotInfo, socketPath string) error
}

// RestoredVMStopper is the optional capability of a SnapshotRestorer that owns
// the process it started. A restore that fails after the load must kill the VM it
// created; the restorer is the only thing that can, because it holds the process.
// *snapshot.Manager implements it. A restorer without it is still usable, but a
// failed restore then reports that the VM could not be stopped.
type RestoredVMStopper interface {
	StopRestored(taskID string) error
}

// RestoredInstanceStore is the node's store of restored-instance records, as a
// restore uses it: read in step 1 (this task must not already run a VM), written
// in step 8, and deleted by the undo. *runtime.RestoredRegistry is the production
// implementation and the assertion below keeps its three signatures honest.
//
// It is an interface rather than the concrete registry because the undo's record
// delete leaves nothing behind to observe: the restore only writes a record at
// its last step, so a failed restore has nothing on disk to remove. The seam is
// what lets a test see that delete, and that it comes last.
type RestoredInstanceStore interface {
	Get(taskID string) (scruntime.RestoredInstance, bool, error)
	Put(instance scruntime.RestoredInstance) error
	Delete(taskID string) error
}

// The production restorer is pkg/snapshot's manager: it starts the Firecracker
// process, so it is also the thing that can stop it again. The assertions fail the
// build if either method's signature drifts, which is the only way the two
// packages can disagree without a test noticing at wiring time.
var (
	_ SnapshotRestorer      = (*snapshot.Manager)(nil)
	_ RestoredVMStopper     = (*snapshot.Manager)(nil)
	_ RestoredInstanceStore = (*scruntime.RestoredRegistry)(nil)
)

// Executor is the guest agent a restore drives: one command, argv only. There is
// no shell string anywhere in this path, so a value the caller supplies (an
// address, a gateway) can never become syntax.
//
// A non-zero exit is reported through err as well as the exit code, so the
// implementation can put the guest's stderr in the message: a restore failure
// that says only "exit status 1" tells an operator nothing about which command
// refused and why.
type Executor interface {
	Exec(ctx context.Context, argv []string) (exitCode int, err error)
}

// AgentDialer connects to the guest agent of the restored VM. DialAgent is the
// production implementation; tests substitute their own.
type AgentDialer interface {
	Dial(ctx context.Context, vsockPath string) (Executor, error)
}

// RestoreDeps are the effects the restore needs; production wires the real ones.
type RestoreDeps struct {
	// Snapshot loads the artifact's state and memory into a new VM.
	Snapshot SnapshotRestorer
	// TapCreate creates the TAP device, attached to the bridge and down.
	TapCreate func(name, bridge string) error
	// TapUp brings an existing TAP device up.
	TapUp func(name string) error
	// TapDelete removes a TAP device. It must treat a missing device as success:
	// a failed restore may be run again, and then there is nothing to delete.
	TapDelete func(name string) error
	// Agent dials the guest agent behind the artifact's vsock muxer.
	Agent AgentDialer
	// Registry stores the records that map a new task to the VM it runs. It is
	// read before anything is created (a task that already runs a restored VM
	// must be refused) and written at the end.
	Registry RestoredInstanceStore
	// Now is the clock the record's CreatedAt and the guest-agent deadline are
	// measured with. Nil selects time.Now; a test substitutes a fixed one.
	Now func() time.Time
}

// Restore starts the VM the artifact describes as the task TaskID, and returns
// the record it wrote.
//
// It is called in-process by the executor, once the target has staged the
// artifact's three files. On success the guest is running, re-addressed, attached
// to the bridge, and resolvable through the restored-instance registry.
//
// A task that already runs a restored VM is refused before any effect: the
// artifact's disk would be materialised over the file that VM is running from.
//
// On failure it undoes what it did, in reverse order: the VM the load may have
// started is stopped, the TAP device is removed, and any record for the task is
// deleted. The artifact itself is left on disk, so a retry (or an operator) can
// use it again: the bytes are the expensive part and they are not this node's to
// discard.
func Restore(ctx context.Context, in RestoreInput, deps RestoreDeps) (instance scruntime.RestoredInstance, err error) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	// Every value the restore hands to something else is trimmed once here, so
	// validation and use cannot disagree about it: an address reaches ip(8) as
	// argv, a dir and a socket dir reach the filesystem as a path. A trailing
	// space still starts with "/", so IsAbs accepts the path and the untrimmed
	// value would then name a directory nobody created.
	in.GuestCIDR = strings.TrimSpace(in.GuestCIDR)
	in.Gateway = strings.TrimSpace(in.Gateway)
	in.Dir = strings.TrimSpace(in.Dir)
	in.SocketDir = strings.TrimSpace(in.SocketDir)
	if err = validateRestoreInput(in, deps); err != nil {
		return scruntime.RestoredInstance{}, restoreStep(in.TaskID, stepValidate, err)
	}
	if err = verifyStagedFiles(in.Dir, in.Artifact); err != nil {
		return scruntime.RestoredInstance{}, restoreStep(in.TaskID, stepVerifyStagedFiles, err)
	}

	// effects is what a failure has to undo. It is recorded as each step
	// succeeds, never predicted, so a rollback only touches what exists.
	var effects restoreEffects
	defer func() {
		if effects.complete {
			return
		}
		if cleanupErr := undoRestore(in, deps, effects); cleanupErr != nil {
			err = errors.Join(err, restoreStep(in.TaskID, stepUndo, cleanupErr))
		}
	}()

	artifact := in.Artifact
	if err = deps.TapCreate(artifact.TapName, in.Bridge); err != nil {
		return scruntime.RestoredInstance{}, restoreStep(in.TaskID, stepCreateTap, err)
	}
	effects.tapCreated = true

	// The API socket belongs to the new task: it is created when Firecracker
	// starts, so it is the one host-side name the snapshot does not record.
	socketPath := filepath.Join(in.SocketDir, in.TaskID+scruntime.VMSocketSuffix)
	// Recorded before the call, not after it: a restorer that starts the process
	// and then fails leaves something to stop, and the undo is not allowed to
	// infer from a nil return that there is nothing behind it.
	effects.restorerCalled = true
	if err = deps.Snapshot.RestoreFromSnapshot(ctx, restorationInfo(in), socketPath); err != nil {
		return scruntime.RestoredInstance{}, restoreStep(in.TaskID, stepRestoreSnapshot, err)
	}

	guest, err := waitForGuestAgent(ctx, deps, artifact.VsockPath)
	if err != nil {
		return scruntime.RestoredInstance{}, restoreStep(in.TaskID, stepGuestAgent, err)
	}
	// The connection is this restore's; the VM outlives it.
	if closer, ok := guest.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}

	if err = readdressGuest(ctx, guest, in.GuestCIDR, in.Gateway); err != nil {
		return scruntime.RestoredInstance{}, restoreStep(in.TaskID, stepReaddressGuest, err)
	}

	if err = deps.TapUp(artifact.TapName); err != nil {
		return scruntime.RestoredInstance{}, restoreStep(in.TaskID, stepTapUp, err)
	}

	instance = scruntime.RestoredInstance{
		TaskID:     in.TaskID,
		VMID:       artifact.SourceTaskID,
		ArtifactID: artifact.ID,
		SocketPath: socketPath,
		TapName:    artifact.TapName,
		VsockPath:  artifact.VsockPath,
		DiskPath:   artifact.DiskPath,
		CreatedAt:  deps.Now().UTC(),
	}
	if err = deps.Registry.Put(instance); err != nil {
		// A running VM with no record is the state the record exists to prevent:
		// nothing could resolve the task to it, so a stop would kill nothing and
		// exec would dial a path nobody owns. The VM is undone below.
		return scruntime.RestoredInstance{}, restoreStep(in.TaskID, stepRecordInstance, err)
	}

	effects.complete = true
	return instance, nil
}

// restoreEffects is what a failed restore has to undo, in the order it was done.
type restoreEffects struct {
	tapCreated bool
	// restorerCalled is set before the snapshot is loaded, so a load that starts
	// the process and then fails still owes a stop.
	restorerCalled bool
	// complete is set only when the restore returned successfully; anything
	// short of that leaves effects for undoRestore.
	complete bool
}

// undoRestore reverses a failed restore: the VM the load may have started, the TAP
// device it created, and the record for the task.
//
// Every operation is best effort and every failure is reported, not ignored: the
// caller already has the step that failed, and a silent cleanup failure would
// leave a running VM or a device behind with nothing said about it. The artifact
// is deliberately untouched.
func undoRestore(in RestoreInput, deps RestoreDeps, effects restoreEffects) error {
	var problems []error

	// The stop is attempted whenever the restorer can stop, and it is not gated
	// on the load having returned nil: a restorer that starts Firecracker and
	// then fails owes a process that only it can take down. A process that never
	// started is not a failure — ErrNoRestoredVM means the VM is already gone,
	// which is what the undo wanted.
	if stopper, ok := deps.Snapshot.(RestoredVMStopper); ok {
		if err := stopper.StopRestored(in.TaskID); err != nil && !errors.Is(err, snapshot.ErrNoRestoredVM) {
			problems = append(problems, fmt.Errorf("stop the restored VM: %w", err))
		}
	} else if effects.restorerCalled {
		// Nothing was asked to load, so nothing can be running: only a restorer
		// that was actually invoked can have leaked a VM the undo cannot reach.
		problems = append(problems, errors.New(
			"the snapshot restorer cannot stop the VM it started, so it may still be running"))
	}

	if effects.tapCreated {
		if err := deps.TapDelete(in.Artifact.TapName); err != nil {
			problems = append(problems, fmt.Errorf("delete the TAP device %s: %w", in.Artifact.TapName, err))
		}
	}

	// Only a record this attempt wrote can be here: step 1 refused a task that
	// already had one, and the record is written last, so this delete is a no-op
	// on every path that reaches it today. It stays unconditional anyway —
	// deleting is idempotent (a missing record is success) and a step reordered
	// later must not be able to leave a record claiming a VM that is gone.
	if err := deps.Registry.Delete(in.TaskID); err != nil {
		problems = append(problems, fmt.Errorf("delete the restored-instance record: %w", err))
	}

	return errors.Join(problems...)
}

// The steps a failed restore names. They are the vocabulary of the error a
// caller sees: the cause says what the kernel, Firecracker or the guest said, and
// the step says which part of the restore was running.
const (
	stepValidate          = "validate the request"
	stepVerifyStagedFiles = "verify the staged artifact files"
	stepCreateTap         = "create the TAP device"
	stepRestoreSnapshot   = "restore the snapshot"
	stepGuestAgent        = "wait for the guest agent"
	stepReaddressGuest    = "re-address the guest"
	stepTapUp             = "bring the TAP device up"
	stepRecordInstance    = "record the restored instance"
	stepUndo              = "undo the failed restore"
)

// restoreStep wraps a failure with the task and the step it stopped at.
func restoreStep(taskID, step string, cause error) error {
	return fmt.Errorf("restore task %q: %s: %w", taskID, step, cause)
}

// validateRestoreInput refuses a request before anything is created. Everything
// here is cheap and local: an address is parsed, a name is checked against the
// kernel's rules, and the artifact is asked to validate itself. The input's
// strings have already been trimmed by Restore, so what is checked here is
// exactly what will be used.
func validateRestoreInput(in RestoreInput, deps RestoreDeps) error {
	if err := requireRestoreDeps(deps); err != nil {
		return err
	}
	if err := in.Artifact.Validate(); err != nil {
		return err
	}

	switch {
	case in.Dir == "":
		return errors.New("dir is empty: want the absolute directory holding the artifact's staged files")
	case !filepath.IsAbs(in.Dir):
		return fmt.Errorf("dir %q is relative: want the absolute directory holding the artifact's staged files", in.Dir)
	}
	if err := scruntime.CheckTaskID(in.TaskID); err != nil {
		return fmt.Errorf("taskId %q: %w", in.TaskID, err)
	}

	// The artifact's source task ID names the TAP device, the vsock muxer and the
	// disk path the target recreates, and the record's VM ID, which the registry
	// validates with this same rule. Asking here rather than at step 8 keeps a
	// manifest that cannot produce a record from starting a VM first.
	if err := scruntime.CheckTaskID(in.Artifact.SourceTaskID); err != nil {
		return fmt.Errorf("sourceTaskId %q: %w", in.Artifact.SourceTaskID, err)
	}

	// A restore without the disk copy would load a memory image against a
	// filesystem that never moved. The manifest allows the field to be absent
	// (a snapshot with no copy is still a valid artifact to move); only a restore
	// can say it is useless, so this is the check.
	if in.Artifact.DiskFile == (File{}) {
		return errors.New("artifact has no diskFile: a restore needs the disk copy, because the guest's filesystem is not part of the state or memory file")
	}

	// The muxer the snapshot recorded is where Firecracker will recreate the
	// socket, so the path's own name must be the source task's. Checking it here
	// keeps the registry's rule from failing the restore at the last step, after
	// a VM is running.
	if want := scruntime.VsockPath(filepath.Dir(in.Artifact.VsockPath), in.Artifact.SourceTaskID); in.Artifact.VsockPath != want {
		return fmt.Errorf("artifact vsockPath %q is not the muxer of task %q: want %s, the path the snapshot recorded",
			in.Artifact.VsockPath, in.Artifact.SourceTaskID, want)
	}

	prefix, err := netip.ParsePrefix(strings.TrimSpace(in.GuestCIDR))
	if err != nil {
		return fmt.Errorf("guestCidr %q is not an address prefix: want an IPv4 address and prefix length, for example 192.168.127.37/24: %w", in.GuestCIDR, err)
	}
	if !prefix.Addr().Is4() {
		return fmt.Errorf("guestCidr %q is not an IPv4 prefix: the guest interface this product gives a task is IPv4", in.GuestCIDR)
	}

	gateway, err := netip.ParseAddr(strings.TrimSpace(in.Gateway))
	if err != nil {
		return fmt.Errorf("gateway %q is not an IP address: want the bridge's IPv4 address, for example 192.168.127.2: %w", in.Gateway, err)
	}
	if !gateway.Is4() {
		return fmt.Errorf("gateway %q is not an IPv4 address: want the bridge's IPv4 address", in.Gateway)
	}

	if strings.TrimSpace(in.Bridge) == "" {
		return errors.New("bridge is empty: want the bridge the restored VM's TAP device joins")
	}
	if problem := interfaceNameProblem(in.Bridge); problem != "" {
		return fmt.Errorf("bridge %q is not a valid interface name: %s", in.Bridge, problem)
	}

	switch {
	case in.SocketDir == "":
		return errors.New("socketDir is empty: want the absolute directory the restored VM's API socket goes in")
	case !filepath.IsAbs(in.SocketDir):
		return fmt.Errorf("socketDir %q is relative: want the absolute directory the restored VM's API socket goes in", in.SocketDir)
	}

	// Last, because it is the only check that reads state rather than the
	// request: a task that already runs a restored VM must not be restored
	// again. The second attempt would load the artifact's disk over the file the
	// live VM is running from and take its record, leaving one process nobody
	// can resolve. Discarding the task's VM is the caller's job, not a rollback.
	return requireNoRestoredVM(deps.Registry, in.TaskID)
}

// requireNoRestoredVM refuses a restore for a task that already runs a restored
// VM, and refuses too when it cannot tell: a record that cannot be read is not
// evidence that the task is free, and guessing wrong here destroys a live VM.
func requireNoRestoredVM(registry RestoredInstanceStore, taskID string) error {
	existing, found, err := registry.Get(taskID)
	switch {
	case err != nil:
		return fmt.Errorf("task %q cannot be restored: the record of a restored VM could not be read, so this restore cannot tell whether the task already runs one: %w", taskID, err)
	case !found:
		return nil
	default:
		return fmt.Errorf("task %q already runs a restored VM (VM %q, restored at %s): that VM must be discarded first — stopped, and its record deleted",
			taskID, existing.VMID, existing.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
}

// requireRestoreDeps refuses a wiring that cannot finish a restore. It runs
// before any effect, so a missing effect is a request error rather than a panic
// halfway through, or a VM that starts and cannot be undone.
func requireRestoreDeps(deps RestoreDeps) error {
	switch {
	case deps.Snapshot == nil:
		return errors.New("deps.Snapshot is nil: want the snapshot restorer that loads the artifact")
	case deps.TapCreate == nil:
		return errors.New("deps.TapCreate is nil: want the TAP creator")
	case deps.TapUp == nil:
		return errors.New("deps.TapUp is nil: want the TAP bring-up")
	case deps.TapDelete == nil:
		return errors.New("deps.TapDelete is nil: want the TAP remover a failed restore needs")
	case deps.Agent == nil:
		return errors.New("deps.Agent is nil: want the guest-agent dialer")
	case deps.Registry == nil:
		return errors.New("deps.Registry is nil: want the restored-instance registry, without which a running VM cannot be resolved")
	}
	return nil
}

// stagedFile is one file the restore verifies, with the manifest field that
// describes it, so a failure can say which description did not match.
type stagedFile struct {
	field string
	file  File
}

// stagedFiles lists the artifact's files in field order. All three are required
// from a restore; validateRestoreInput has already refused an artifact without a
// disk copy.
func stagedFiles(artifact Artifact) []stagedFile {
	return []stagedFile{
		{"stateFile", artifact.StateFile},
		{"memoryFile", artifact.MemoryFile},
		{"diskFile", artifact.DiskFile},
	}
}

// verifyStagedFiles checks that each file in dir is exactly the file the manifest
// describes: the right size and the right SHA-256. It is the last thing that runs
// before any effect, because a manifest that arrived intact says nothing about
// the bytes that did.
func verifyStagedFiles(dir string, artifact Artifact) error {
	for _, entry := range stagedFiles(artifact) {
		path := filepath.Join(dir, entry.file.Name)

		stat, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%s %s: %w", entry.field, path, err)
		}
		if !stat.Mode().IsRegular() {
			return fmt.Errorf("%s %s is not a regular file: want the file the transfer staged", entry.field, path)
		}
		if stat.Size() != entry.file.Size {
			return fmt.Errorf("%s %s is %d bytes: the manifest describes %d", entry.field, path, stat.Size(), entry.file.Size)
		}

		sum, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("%s %s could not be checksummed: %w", entry.field, path, err)
		}
		if sum != entry.file.SHA256 {
			return fmt.Errorf("%s %s has SHA-256 %s: the manifest describes %s", entry.field, path, sum, entry.file.SHA256)
		}
	}
	return nil
}

// restorationInfo describes the artifact to pkg/snapshot. The three staged paths
// are under the target's own directory; RootfsPath is the path the state file
// recorded, which is where the restorer materialises the disk so the device model
// finds it. Checksum is the state file's digest, which the restorer verifies again
// before it loads.
//
// TaskID is the NEW task: pkg/snapshot tracks the process it starts by that field,
// and the node stops a restored task by the task it runs as. The source task's
// identity lives inside the state file and is not restated here.
func restorationInfo(in RestoreInput) *snapshot.SnapshotInfo {
	artifact := in.Artifact
	return &snapshot.SnapshotInfo{
		ID:         artifact.SnapshotID,
		TaskID:     in.TaskID,
		ServiceID:  artifact.ServiceID,
		NodeID:     artifact.SourceNodeID,
		CreatedAt:  artifact.SnapshotAt,
		StatePath:  filepath.Join(in.Dir, artifact.StateFile.Name),
		MemoryPath: filepath.Join(in.Dir, artifact.MemoryFile.Name),
		SizeBytes:  artifact.StateFile.Size + artifact.MemoryFile.Size + artifact.DiskFile.Size,
		RootfsPath: artifact.DiskPath,
		DiskPath:   filepath.Join(in.Dir, artifact.DiskFile.Name),
		Checksum:   artifact.StateFile.SHA256,
	}
}

// waitForGuestAgent dials the guest agent behind the restored VM's vsock muxer
// until it answers, the deadline passes, the context ends, or the attempt cap is
// reached. The muxer path is the one the snapshot recorded: Firecracker recreates
// the socket there when it loads the state, so the client waits at that path
// rather than deriving one from the new task.
func waitForGuestAgent(ctx context.Context, deps RestoreDeps, vsockPath string) (Executor, error) {
	deadline := deps.Now().Add(guestReadyTimeout)

	var lastErr error
	for attempt := 1; attempt <= maxGuestDialAttempts; attempt++ {
		guest, dialErr := dialGuest(ctx, deps, vsockPath)
		if dialErr == nil {
			return guest, nil
		}
		lastErr = dialErr

		if waitErr := agentRetryWait(ctx, guestReadyInterval); waitErr != nil {
			return nil, fmt.Errorf("no guest agent at %s: %w (last dial: %v)", vsockPath, waitErr, lastErr)
		}
		if !deps.Now().Add(guestReadyInterval).Before(deadline) {
			return nil, fmt.Errorf("no guest agent at %s after %s and %d attempts: %w",
				vsockPath, guestReadyTimeout, attempt, lastErr)
		}
	}
	return nil, fmt.Errorf("no guest agent at %s after %d attempts: %w", vsockPath, maxGuestDialAttempts, lastErr)
}

// dialGuest makes one bounded dial attempt. The attempt's own deadline covers a
// muxer that accepts the connection and then says nothing. Cancelling it when the
// attempt returns is safe: pkg/agent clears the connection's deadline before it
// returns, and the established connection is not tied to the context that dialled
// it — the restore's own commands carry their own.
func dialGuest(ctx context.Context, deps RestoreDeps, vsockPath string) (Executor, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, guestDialTimeout)
	defer cancel()

	return deps.Agent.Dial(attemptCtx, vsockPath)
}

// readdressGuest gives the resumed guest the new task's network identity. It is
// argv only, one command at a time, in this order: the old addresses go first so
// the guest cannot answer on the source node's address while the new one is
// added, and the default route is replaced last so no packet leaves under the
// old one.
func readdressGuest(ctx context.Context, guest Executor, cidr, gateway string) error {
	for _, argv := range readdressCommands(cidr, gateway) {
		if err := runGuestCommand(ctx, guest, argv); err != nil {
			return err
		}
	}
	return nil
}

// readdressCommands is the argv sequence, separated so a test can pin it without
// running anything. The interface is the product's single guest interface.
func readdressCommands(cidr, gateway string) [][]string {
	return [][]string{
		{"ip", "addr", "flush", "dev", guestInterface},
		{"ip", "addr", "add", cidr, "dev", guestInterface},
		{"ip", "route", "replace", "default", "via", gateway},
	}
}

// runGuestCommand runs one argv in the guest, bounded by its own deadline, and
// turns a non-zero exit into an error. Both shapes of failure are named: an
// executor that reports a status and no error is still a command that did not do
// what the restore needed.
func runGuestCommand(ctx context.Context, guest Executor, argv []string) error {
	commandCtx, cancel := context.WithTimeout(ctx, guestCommandTimeout)
	defer cancel()

	code, err := guest.Exec(commandCtx, argv)
	switch {
	case err != nil:
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	case code != 0:
		return fmt.Errorf("%s: the guest exited with status %d", strings.Join(argv, " "), code)
	}
	return nil
}

// DialAgent is the production AgentDialer: it dials the guest agent behind a
// restored VM's vsock muxer. The returned Executor is an *agent.Client wrapper
// that buffers the guest's stderr, because the restore needs it in a failure
// message rather than as a stream.
func DialAgent(ctx context.Context, vsockPath string) (Executor, error) {
	client, err := agent.Dial(ctx, vsockPath, agent.DefaultPort)
	if err != nil {
		return nil, err
	}
	return &agentExecutor{client: client}, nil
}

// guestAgent is the part of *agent.Client the adapter uses. It is an interface so
// the adapter's own behaviour — capturing stderr, refusing an empty command,
// passing an exit status through — can be tested without a socket or a guest.
type guestAgent interface {
	ExecStatus(ctx context.Context, cmd []string, opts agent.ExecOptions) (agent.ExecResult, error)
	Close() error
}

// agentExecutor adapts a guestAgent to Executor.
type agentExecutor struct {
	client guestAgent
}

// The adapter is what a restore gets from DialAgent: an Executor it can close.
var (
	_ Executor  = (*agentExecutor)(nil)
	_ io.Closer = (*agentExecutor)(nil)
)

// Exec runs one argv in the guest and returns its exit status. A non-zero exit is
// returned as an error carrying the guest's stderr, so the restore's message says
// what the command complained about; the code travels with it for a caller that
// logs statuses.
func (e *agentExecutor) Exec(ctx context.Context, argv []string) (int, error) {
	if len(argv) == 0 {
		return 0, errors.New("empty command")
	}

	var stderr bytes.Buffer
	result, err := e.client.ExecStatus(ctx, argv, agent.ExecOptions{Stderr: &stderr})
	if err != nil {
		return 0, err
	}
	if result.ExitCode != 0 {
		return result.ExitCode, fmt.Errorf("exit status %d: %s", result.ExitCode, guestStderr(stderr.String()))
	}
	return 0, nil
}

// Close closes the guest connection. It is what makes the Executor an io.Closer,
// so a restore can release the connection when it returns while the VM keeps
// running.
func (e *agentExecutor) Close() error {
	return e.client.Close()
}

// guestStderr renders what the guest wrote to stderr for a failure message. A
// command that failed silently says so, because an empty string in the middle of
// an error reads as a formatting bug.
func guestStderr(stderr string) string {
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		return "the guest wrote no error output"
	}
	return trimmed
}
