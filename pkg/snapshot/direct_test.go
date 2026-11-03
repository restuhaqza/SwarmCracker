package snapshot

// The direct launcher, tested without Firecracker. An orb and a CI runner have no
// KVM (and no /dev/net/tun), so a unit test cannot start a real VMM; what it can
// do is replace the spawn with a process it controls and the two API calls with
// recorders, and then hold the launcher to the behaviour the restore path has
// today: the socket path it chooses, the command line it builds, the console it
// attaches, the checkpoint it loads, and what it cleans up when a step fails.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// loadCall is one PUT /snapshot/load a fake launch saw.
type loadCall struct {
	socket string
	state  string
	mem    string
}

// fakeDirectVMM stands in for the Firecracker binary a direct launch starts: the
// spawn becomes a real child process a stop can signal, and the two API calls
// become recorders.
type fakeDirectVMM struct {
	// createSocket makes the spawn leave an API socket file behind, the way
	// Firecracker does, so a test can see whether it survives the launch.
	createSocket bool
	// onStart runs inside the spawn, before the process exists: it is how a test
	// inspects the filesystem at the moment the VMM would be exec'd.
	onStart func()

	lookPathErr error
	waitErr     error
	loadErr     error

	starts  int
	binary  string
	socket  string
	console string
	waited  []string
	loads   []loadCall
	proc    *os.Process
}

// install replaces the launcher's seams for one test and restores them afterwards.
func (f *fakeDirectVMM) install(t *testing.T) {
	t.Helper()

	previousLook := firecrackerLookPath
	previousStart := directStartVMM
	previousWait := directWaitForAPI
	previousLoad := directLoadSnapshot
	t.Cleanup(func() {
		firecrackerLookPath = previousLook
		directStartVMM = previousStart
		directWaitForAPI = previousWait
		directLoadSnapshot = previousLoad
	})

	firecrackerLookPath = func(string) (string, error) {
		if f.lookPathErr != nil {
			return "", f.lookPathErr
		}
		return "/fake/firecracker", nil
	}
	directStartVMM = f.start(t)
	directWaitForAPI = func(socketPath string, _ time.Duration) error {
		f.waited = append(f.waited, socketPath)
		return f.waitErr
	}
	directLoadSnapshot = func(_ context.Context, socketPath, statePath, memPath string) error {
		f.loads = append(f.loads, loadCall{socket: socketPath, state: statePath, mem: memPath})
		return f.loadErr
	}
}

// start is the fake spawn. The process is a real sleeper, because a stop path is
// only worth testing against something a signal can reach.
func (f *fakeDirectVMM) start(t *testing.T) func(string, string, string) (*exec.Cmd, error) {
	t.Helper()

	return func(binary, socketPath, consolePath string) (*exec.Cmd, error) {
		f.starts++
		f.binary, f.socket, f.console = binary, socketPath, consolePath
		if f.onStart != nil {
			f.onStart()
		}
		if f.createSocket {
			require.NoError(t, os.WriteFile(socketPath, nil, 0o600))
		}

		cmd := exec.Command("sleep", "30")
		require.NoError(t, cmd.Start())
		f.proc = cmd.Process
		t.Cleanup(func() {
			// A failed test must not leave the sleeper behind.
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		})
		return cmd, nil
	}
}

// needRestoreRequest is a request the direct launcher serves: an instance being
// resumed from its own checkpoint.
func needRestoreRequest() LaunchRequest {
	return LaunchRequest{
		InstanceID: "sbx-1a2b3c",
		Mode:       ModeRestore,
		Kernel:     "/kernel/vmlinux",
		Rootfs:     "/drives/rootfs.ext4",
		State:      "/snapshot/vm.state",
		Mem:        "/snapshot/vm.mem",
		VCPUs:      2,
		MemoryMB:   512,
		JailUID:    1000,
		JailGID:    1000,
		NetNS:      "sbx-1a2b3c",
	}
}

// waitForProcessGone waits the launcher's cleanup out and then asserts the process
// is gone. Polling is what makes the assertion possible at all: a kill is
// asynchronous with respect to the call that issued it.
func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.False(t, processAlive(pid), "the process the launcher started must be gone")
}

// waitForFile waits until path holds something and returns it. A spawned script
// writes its output asynchronously, so reading straight after Start would race it.
func waitForFile(t *testing.T, path string) string {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for {
		body, err := os.ReadFile(path)
		if err == nil && len(body) > 0 {
			return string(body)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s stayed empty: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDirectLauncherResumesTheCheckpoint pins what a launch returns and what it
// does on the way: one process, started by path lookup, listening on the socket
// derived from the instance ID, with the instance's console attached and the
// checkpoint the request names loaded into it.
//
// A fork is the same resume: the difference is which disk the caller prepared, not
// what the launcher sends.
func TestDirectLauncherResumesTheCheckpoint(t *testing.T) {
	for _, mode := range []LaunchMode{ModeRestore, ModeFork} {
		t.Run(string(mode), func(t *testing.T) {
			socketDir, logDir := t.TempDir(), t.TempDir()
			fake := &fakeDirectVMM{createSocket: true}
			fake.install(t)

			req := needRestoreRequest()
			req.Mode = mode

			launcher := DirectLauncher{SocketDir: socketDir, LogDir: logDir}
			launched, err := launcher.Start(context.Background(), req)
			require.NoError(t, err)

			wantSocket := filepath.Join(socketDir, req.InstanceID+runtime.VMSocketSuffix)
			assert.Equal(t, wantSocket, launched.APISocket)
			assert.Equal(t, filepath.Join(logDir, req.InstanceID+".log"), launched.ConsolePath)
			assert.Equal(t, req.State, launched.StatePath)
			assert.Equal(t, req.Mem, launched.MemPath)
			assert.Empty(t, launched.VsockPath,
				"a direct launch configures no vsock device, so it must not guess the muxer path")
			require.NotNil(t, launched.Stop, "the launcher must return the only handle to the process it started")

			assert.Equal(t, 1, fake.starts)
			assert.Equal(t, "/fake/firecracker", fake.binary, "the binary comes from PATH lookup")
			assert.Equal(t, wantSocket, fake.socket)
			assert.Equal(t, launched.ConsolePath, fake.console)
			assert.Equal(t, []string{wantSocket}, fake.waited)

			require.Len(t, fake.loads, 1)
			assert.Equal(t, loadCall{socket: wantSocket, state: req.State, mem: req.Mem}, fake.loads[0])
		})
	}
}

// TestDirectLauncherClearsTheStaleSocketBeforeStarting pins the first step of the
// current restore path: a socket file left at the instance's path is removed before
// Firecracker starts, because a launch that dialled it would believe a dead process
// was its own.
func TestDirectLauncherClearsTheStaleSocketBeforeStarting(t *testing.T) {
	socketDir := t.TempDir()
	socketPath := filepath.Join(socketDir, "sbx-1a2b3c"+runtime.VMSocketSuffix)
	require.NoError(t, os.WriteFile(socketPath, []byte("stale"), 0o600))

	staleWasThere := true
	fake := &fakeDirectVMM{onStart: func() {
		_, err := os.Stat(socketPath)
		staleWasThere = err == nil
	}}
	fake.install(t)

	_, err := (DirectLauncher{SocketDir: socketDir}).Start(context.Background(), needRestoreRequest())
	require.NoError(t, err)
	assert.False(t, staleWasThere, "a previous instance's socket must be gone before the new process starts")
}

// TestDirectLauncherReportsAMissingFirecrackerBinary pins the lookup failure as a
// launch that never spawns anything: no binary, no process, no socket.
func TestDirectLauncherReportsAMissingFirecrackerBinary(t *testing.T) {
	fake := &fakeDirectVMM{lookPathErr: exec.ErrNotFound}
	fake.install(t)

	_, err := (DirectLauncher{SocketDir: t.TempDir()}).Start(context.Background(), needRestoreRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "firecracker binary not found in PATH")
	assert.Zero(t, fake.starts, "nothing may be spawned without a binary")
}

// TestDirectLauncherEndsTheProcessWhenTheAPINeverAppears pins the first failed step
// after the spawn: Start returns no handle, so the launcher itself has to kill the
// process and clear the socket, and it must not go on to load a checkpoint into a
// VMM that never answered.
func TestDirectLauncherEndsTheProcessWhenTheAPINeverAppears(t *testing.T) {
	fake := &fakeDirectVMM{createSocket: true, waitErr: errors.New("socket not ready within 10s")}
	fake.install(t)

	launcher := DirectLauncher{SocketDir: t.TempDir(), LogDir: t.TempDir()}
	launched, err := launcher.Start(context.Background(), needRestoreRequest())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "firecracker API not ready after restore")
	assert.Nil(t, launched.Stop, "a failed launch must not hand back a stop for a process it already killed")
	assert.Empty(t, fake.loads, "a checkpoint must not be loaded into a VMM whose API never answered")

	require.NotNil(t, fake.proc)
	waitForProcessGone(t, fake.proc.Pid)
	_, statErr := os.Stat(fake.socket)
	assert.True(t, os.IsNotExist(statErr), "the socket of the abandoned process must be removed")
}

// TestDirectLauncherEndsTheProcessWhenTheLoadFails pins the second failed step: the
// VMM answered, but the checkpoint did not load, so the instance is not running and
// must not be left behind.
func TestDirectLauncherEndsTheProcessWhenTheLoadFails(t *testing.T) {
	fake := &fakeDirectVMM{createSocket: true, loadErr: errors.New("snapshot state is corrupt")}
	fake.install(t)

	launched, err := (DirectLauncher{SocketDir: t.TempDir()}).Start(context.Background(), needRestoreRequest())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load snapshot memory")
	assert.Nil(t, launched.Stop)

	require.NotNil(t, fake.proc)
	waitForProcessGone(t, fake.proc.Pid)
	_, statErr := os.Stat(fake.socket)
	assert.True(t, os.IsNotExist(statErr), "the socket of the abandoned process must be removed")
}

// TestDirectLauncherRefusesAnUnusableRequest pins the requests the launcher declines
// before it spawns anything. Each refusal is a caller mistake that would otherwise
// become a VMM started with the wrong paths — or a path outside the socket directory.
func TestDirectLauncherRefusesAnUnusableRequest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*LaunchRequest)
		bare   bool // a launcher with no socket directory
		want   string
	}{
		{name: "cold boot", mutate: func(r *LaunchRequest) { r.Mode = ModeCold }, want: "cannot cold-boot"},
		{name: "empty mode", mutate: func(r *LaunchRequest) { r.Mode = "" }, want: "launch mode is empty"},
		{name: "unknown mode", mutate: func(r *LaunchRequest) { r.Mode = "thaw" }, want: `unknown launch mode "thaw"`},
		{name: "empty instance ID", mutate: func(r *LaunchRequest) { r.InstanceID = "" }, want: "task ID is empty"},
		{name: "escaping instance ID", mutate: func(r *LaunchRequest) { r.InstanceID = "../escape" }, want: "path separator"},
		{name: "no socket directory", bare: true, want: "no socket directory"},
		{name: "no state image", mutate: func(r *LaunchRequest) { r.State = "" }, want: "both checkpoint images"},
		{name: "no memory image", mutate: func(r *LaunchRequest) { r.Mem = "" }, want: "both checkpoint images"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDirectVMM{}
			fake.install(t)

			req := needRestoreRequest()
			if tt.mutate != nil {
				tt.mutate(&req)
			}
			launcher := DirectLauncher{SocketDir: t.TempDir()}
			if tt.bare {
				launcher.SocketDir = ""
			}

			_, err := launcher.Start(context.Background(), req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Zero(t, fake.starts, "a refused request must not start a process")
		})
	}
}

// TestDirectLauncherRefusalUsesThePathRule pins that an instance ID is checked with
// the node's one rule for "this string can name a file" rather than a second copy,
// so the sentinel a caller matches is the shared one.
func TestDirectLauncherRefusalUsesThePathRule(t *testing.T) {
	fake := &fakeDirectVMM{}
	fake.install(t)

	req := needRestoreRequest()
	req.InstanceID = "../escape"

	_, err := (DirectLauncher{SocketDir: t.TempDir()}).Start(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, runtime.ErrTaskIDPathSeparator)
	assert.Zero(t, fake.starts)
}

// TestDirectLauncherStartsWithTheSameCommandLineAsTheRestorePath uses the real spawn
// (only the binary and the two API calls are replaced) to pin the command line and
// the console the restore path builds today: `firecracker --api-sock <socket>`, with
// the guest's ttyS0 going to the instance's own log file. A config file, a
// `--snapshot` flag (removed in modern Firecracker) or a detached console would each
// change how a launched instance behaves and be invisible in the fake above.
func TestDirectLauncherStartsWithTheSameCommandLineAsTheRestorePath(t *testing.T) {
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	binary := filepath.Join(dir, "firecracker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvPath + "\necho guest console output\n"
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o755))

	previousLook := firecrackerLookPath
	previousWait := directWaitForAPI
	previousLoad := directLoadSnapshot
	t.Cleanup(func() {
		firecrackerLookPath = previousLook
		directWaitForAPI = previousWait
		directLoadSnapshot = previousLoad
	})
	firecrackerLookPath = func(string) (string, error) { return binary, nil }
	directWaitForAPI = func(string, time.Duration) error { return nil }
	directLoadSnapshot = func(context.Context, string, string, string) error { return nil }

	socketDir, logDir := t.TempDir(), t.TempDir()
	launched, err := (DirectLauncher{SocketDir: socketDir, LogDir: logDir}).
		Start(context.Background(), needRestoreRequest())
	require.NoError(t, err)

	wantArgv := []string{"--api-sock", filepath.Join(socketDir, "sbx-1a2b3c"+runtime.VMSocketSuffix)}
	assert.Equal(t, wantArgv, strings.Fields(waitForFile(t, argvPath)))

	assert.Equal(t, filepath.Join(logDir, "sbx-1a2b3c.log"), launched.ConsolePath)
	assert.Contains(t, waitForFile(t, launched.ConsolePath), "guest console output",
		"the guest console must reach the instance's log file")
}

// TestDirectLauncherStopEndsTheInstance pins the handle Start returns: it signals the
// process, waits for it, removes the socket that process owned, and can be called
// again because a teardown may be retried.
func TestDirectLauncherStopEndsTheInstance(t *testing.T) {
	fake := &fakeDirectVMM{createSocket: true}
	fake.install(t)

	launched, err := (DirectLauncher{SocketDir: t.TempDir()}).Start(context.Background(), needRestoreRequest())
	require.NoError(t, err)
	require.NotNil(t, fake.proc)
	require.FileExists(t, launched.APISocket)

	require.NoError(t, launched.Stop(context.Background()))
	waitForProcessGone(t, fake.proc.Pid)
	assert.NoFileExists(t, launched.APISocket, "the socket is the stopped instance's file")

	require.NoError(t, launched.Stop(context.Background()), "a retried stop must succeed")
}

// TestDirectLauncherStopHonoursTheCallersClock pins the bound the stop context
// carries: a caller with no time left — a cancelled context, or a deadline sooner
// than the package's grace — gets the process killed instead of the ten-second
// window. The process ignores SIGTERM through the signal seam, so only the
// escalation can end it, which is what makes the elapsed time meaningful.
func TestDirectLauncherStopHonoursTheCallersClock(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{name: "cancelled", ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, func() {}
		}},
		{name: "short deadline", ctx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 50*time.Millisecond)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDirectVMM{createSocket: true}
			fake.install(t)

			launched, err := (DirectLauncher{SocketDir: t.TempDir()}).Start(context.Background(), needRestoreRequest())
			require.NoError(t, err)
			require.NotNil(t, fake.proc)

			previous := signalProcess
			signalProcess = func(proc *os.Process, sig syscall.Signal) error {
				if sig == syscall.SIGTERM {
					return nil // the process ignores the graceful signal
				}
				return proc.Signal(sig)
			}
			t.Cleanup(func() { signalProcess = previous })

			ctx, cancel := tt.ctx()
			defer cancel()

			start := time.Now()
			require.NoError(t, launched.Stop(ctx))
			elapsed := time.Since(start)

			waitForProcessGone(t, fake.proc.Pid)
			assert.Less(t, elapsed, 3*time.Second, "the stop must not wait out a grace the caller did not give it")
			assert.NoFileExists(t, launched.APISocket)
		})
	}
}

// TestDirectLauncherStopAcceptsAnInstanceThatAlreadyExited pins the retried
// teardown: a VMM that crashed on its own is already in the end state a stop asks
// for, so the stop succeeds instead of reporting a process that is not there.
func TestDirectLauncherStopAcceptsAnInstanceThatAlreadyExited(t *testing.T) {
	fake := &fakeDirectVMM{createSocket: true}
	fake.install(t)

	launched, err := (DirectLauncher{SocketDir: t.TempDir()}).Start(context.Background(), needRestoreRequest())
	require.NoError(t, err)
	require.NotNil(t, fake.proc)

	// The VMM died by itself, and the reaping happened: the process handle is done.
	require.NoError(t, fake.proc.Kill())
	_, _ = fake.proc.Wait()

	require.NoError(t, launched.Stop(context.Background()))
	assert.NoFileExists(t, launched.APISocket)
}

// TestDirectLauncherStopKeepsTheSocketWhenTheProcessSurvives pins the other side: a
// signal that fails leaves a process that may still be running, so its socket stays —
// it is still the address of a live VM, and removing it would make it unreachable.
func TestDirectLauncherStopKeepsTheSocketWhenTheProcessSurvives(t *testing.T) {
	fake := &fakeDirectVMM{createSocket: true}
	fake.install(t)

	launched, err := (DirectLauncher{SocketDir: t.TempDir()}).Start(context.Background(), needRestoreRequest())
	require.NoError(t, err)
	require.FileExists(t, launched.APISocket)

	previous := signalProcess
	signalProcess = func(*os.Process, syscall.Signal) error { return errors.New("operation not permitted") }
	t.Cleanup(func() { signalProcess = previous })

	err = launched.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SIGTERM")
	assert.FileExists(t, launched.APISocket, "a VM that may still run must keep its socket")
}
