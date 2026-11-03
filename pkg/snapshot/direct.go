package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// directLaunchSocketTimeout is how long a freshly started Firecracker process
// gets to create its API socket before the launch is abandoned. It is the window
// the current restore path waits, kept here so a directly launched instance
// behaves the same.
const directLaunchSocketTimeout = 10 * time.Second

// The seams one direct launch is tested through. A unit test cannot run a real
// Firecracker — it needs KVM, which an orb or a CI runner does not give it — so a
// test replaces the spawn with a process it controls and the two API calls with
// recorders. Everything between them is still the code under test: the stale socket
// left at the path, the console file, the paths a launch reports, and the cleanup
// when a step fails. Production never replaces them.
var (
	firecrackerLookPath = exec.LookPath
	directStartVMM      = startRestoredFirecracker
	directWaitForAPI    = waitForSocket
	directLoadSnapshot  = callSnapshotLoad
)

// DirectLauncher starts Firecracker directly: the firecracker binary becomes the
// caller's child (the daemon's, in production) with
// --api-sock <SocketDir>/<InstanceID>.sock, and the instance runs as that user
// with no jailer, no chroot, no cgroup and no network namespace.
//
// It is the dev path. RFC D3 puts every production restore behind the jailer, and
// this launcher is what remains reachable for development and for tests that have
// no KVM. It also reproduces, step for step, what Manager.RestoreFromSnapshotWithLog
// does today: remove the socket a previous process left at the path, start
// Firecracker with the instance's console attached, wait for the API socket, then
// PUT /snapshot/load with resume_vm so the guest is running when Start returns.
// Keeping that sequence here is what lets the jailed launcher be compared against
// the behaviour the node has now.
//
// The request's jail fields (JailUID, JailGID, NetNS) and its cold-boot fields
// (Kernel, Rootfs, VCPUs, MemoryMB) are ignored: a resumed instance takes its
// devices, kernel and size from the checkpoint state, and this launcher has no
// jail to enter. Kernel, Rootfs and the sizes are refused rather than ignored for
// a cold boot, which it cannot do at all.
type DirectLauncher struct {
	// SocketDir is the host directory the instance's API socket is created in:
	// <SocketDir>/<InstanceID>.sock. Required, because the socket is how the
	// caller reaches the instance it just started.
	SocketDir string

	// LogDir is the host directory the instance's serial console is written to:
	// <LogDir>/<InstanceID>.log, the same file `vm logs` reads for a task, so a
	// directly launched instance is readable like any other. Empty detaches the
	// console, which is what a launcher with nowhere to put logs (a dev run, a
	// test) wants.
	LogDir string
}

// The direct launcher satisfies the interface, and the assertion is where the
// build fails if the contract drifts rather than at the first caller.
var _ Launcher = DirectLauncher{}

// Start launches one instance directly and returns it running.
//
// It implements ModeRestore and ModeFork, which differ only in what the caller did
// to the disk beforehand (a fork reads its own rootfs, which the caller made) —
// both load the checkpoint and resume the guest. ModeCold is refused: booting
// needs the boot source, drive and machine configuration sent over the API, which
// is the lifecycle path's job, and a launcher that returned a VMM that never
// starts the guest would be the worst kind of success.
func (d DirectLauncher) Start(ctx context.Context, req LaunchRequest) (Launched, error) {
	if err := d.check(req); err != nil {
		return Launched{}, err
	}

	socketPath := d.socketPath(req.InstanceID)
	consolePath := d.consolePath(req.InstanceID)

	// A socket left at this path by a previous instance would be the one dialled
	// below, and the instance would look ready while its own process had not
	// started. Removing it is best effort, as it is in the restore path today: no
	// socket is the normal case.
	_ = os.Remove(socketPath)

	fcBinary, err := firecrackerLookPath("firecracker")
	if err != nil {
		return Launched{}, fmt.Errorf("firecracker binary not found in PATH: %w", err)
	}

	cmd, err := directStartVMM(fcBinary, socketPath, consolePath)
	if err != nil {
		return Launched{}, err
	}
	proc := cmd.Process

	if err := directWaitForAPI(socketPath, directLaunchSocketTimeout); err != nil {
		discardDirectVMM(proc, socketPath)
		return Launched{}, fmt.Errorf("firecracker API not ready after restore: %w", err)
	}

	// resume_vm=true, so a nil error here means the guest is running rather than
	// merely loaded. This is the call that has to succeed before Start may report
	// the instance as launched.
	if err := directLoadSnapshot(ctx, socketPath, req.State, req.Mem); err != nil {
		discardDirectVMM(proc, socketPath)
		return Launched{}, fmt.Errorf("failed to load snapshot memory: %w", err)
	}

	log.Info().
		Str("instance_id", req.InstanceID).
		Str("mode", string(req.Mode)).
		Str("socket", socketPath).
		Msg("Instance launched directly")

	return Launched{
		APISocket: socketPath,
		// A direct launch configures no vsock device: the checkpoint's own device
		// keeps the muxer path it recorded, which this launcher has no way to read.
		// Guessing <SocketDir>/<InstanceID>.vsock would point the guest agent at a
		// socket nobody listens on, so the caller that knows the device
		// configuration supplies it (see Launched.VsockPath).
		VsockPath:   "",
		ConsolePath: consolePath,
		StatePath:   req.State,
		MemPath:     req.Mem,
		Stop: func(stopCtx context.Context) error {
			return stopDirect(stopCtx, proc, socketPath)
		},
	}, nil
}

// check refuses a request this launcher cannot serve. The mode comes first: it is
// the one refusal that is about the launcher and not about the request's fields.
func (d DirectLauncher) check(req LaunchRequest) error {
	switch req.Mode {
	case ModeRestore, ModeFork:
	case ModeCold:
		return fmt.Errorf("direct launcher cannot cold-boot instance %q: it resumes a checkpoint, and booting a guest needs the boot source, drive and machine configuration a launcher does not send", req.InstanceID)
	case "":
		return fmt.Errorf("instance %q: launch mode is empty: want %q or %q", req.InstanceID, ModeRestore, ModeFork)
	default:
		return fmt.Errorf("instance %q: unknown launch mode %q: want %q or %q", req.InstanceID, req.Mode, ModeRestore, ModeFork)
	}

	// The ID names the instance's socket and console files, so it is checked as a
	// path element before either is built. The rule is the node's one rule for "this
	// string can name a file" (runtime.CheckTaskID); the sentinel it returns is what
	// a caller matches, the sentence is this launcher's.
	if err := runtime.CheckTaskID(req.InstanceID); err != nil {
		return fmt.Errorf("instance %q: %w", req.InstanceID, err)
	}
	if d.SocketDir == "" {
		return fmt.Errorf("instance %q: the direct launcher has no socket directory: the instance listens on <socket dir>/<instance ID>.sock", req.InstanceID)
	}
	if req.State == "" || req.Mem == "" {
		return fmt.Errorf("instance %q: a %q launch needs both checkpoint images, got state %q and memory %q",
			req.InstanceID, req.Mode, req.State, req.Mem)
	}
	return nil
}

// socketPath is the API socket an instance launched from this directory listens
// on. The suffix is the one runtime.VMSocketSuffix names, because the node's
// discovery, console attach and restore paths all find a VM's socket by it.
func (d DirectLauncher) socketPath(instanceID string) string {
	return filepath.Join(d.SocketDir, instanceID+runtime.VMSocketSuffix)
}

// consolePath is the instance's serial console, or "" when the launcher has no
// log directory and the console is detached.
func (d DirectLauncher) consolePath(instanceID string) string {
	if d.LogDir == "" {
		return ""
	}
	return filepath.Join(d.LogDir, instanceID+".log")
}

// discardDirectVMM ends a start that failed. Start returns no handle for a failed
// launch, so this is the only chance to end the process and clear the socket; both
// steps are best effort, and the failure that got here is the one worth reporting.
func discardDirectVMM(proc *os.Process, socketPath string) {
	if proc != nil {
		if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			log.Warn().Err(err).Int("pid", proc.Pid).Msg("Failed to kill firecracker after a failed launch")
		}
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warn().Err(err).Str("socket", socketPath).Msg("Failed to remove the socket of a failed launch")
	}
}

// stopDirect ends an instance this launcher started: the process, then the API
// socket it owned. The socket is this instance's file, so leaving it behind would
// let a later launch dial a dead VM.
//
// The process is stopped with the package's window — SIGTERM, a bounded wait, then
// SIGKILL — which is what a stop of a restored VM does today.
func stopDirect(ctx context.Context, proc *os.Process, socketPath string) error {
	if err := stopDirectProcess(ctx, proc); err != nil {
		// The process may still be alive, so the socket stays: it is still the
		// address of a running VM, and removing it would make the VM unreachable.
		return err
	}
	if socketPath != "" {
		if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn().Err(err).Str("socket", socketPath).Msg("Failed to remove the API socket of a stopped instance")
		}
	}
	return nil
}

// stopDirectProcess terminates one directly launched process: SIGTERM, a bounded
// wait for it to exit, then SIGKILL. A process that already exited is success,
// because the caller asked for the end state.
//
// The graceful window is the stop context's deadline when that is sooner than the
// package's window, and is skipped entirely when the context is already done: a
// caller that has run out of time needs the process killed, not another ten
// seconds of waiting.
func stopDirectProcess(ctx context.Context, proc *os.Process) error {
	if proc == nil {
		return nil
	}

	grace := restoredStopTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < grace {
			grace = remaining
		}
	}

	if err := signalProcess(proc, syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return fmt.Errorf("signal SIGTERM: %w", err)
	}

	if waitForDirectExit(ctx, proc.Pid, grace) {
		return nil
	}

	log.Warn().Int("pid", proc.Pid).
		Dur("grace", grace).
		Msg("Instance did not exit after SIGTERM; killing it")
	if err := signalProcess(proc, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal SIGKILL: %w", err)
	}
	return nil
}

// waitForDirectExit polls until the process is gone, the graceful window closes,
// or the caller gives up, reporting whether it exited in time. Reporting false is
// what makes the caller escalate to SIGKILL.
func waitForDirectExit(ctx context.Context, pid int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if !processAlive(pid) {
			return true
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(restoredStopPoll)
	}
}
