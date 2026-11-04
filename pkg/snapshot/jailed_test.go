package snapshot

// The jailed launcher, tested without a jailer. A real jailed launch needs KVM,
// /dev/net/tun and root, so what these tests hold the launcher to is everything
// around the VMM: what it stages inside the chroot and as whom, the jailer argument
// list, the chroot paths it sends to /snapshot/load, the window it leaves between
// loading a checkpoint and resuming the guest, the uid lease, and what a stop or a
// failed start leaves behind.
//
// The jailer itself is a script: it records its argv, creates the socket and cgroup
// directory the real jailer would create, and then waits to be signalled. The two
// API calls after it starts are recorders.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// jailedResume is one resume a fake launch saw.
type jailedResume struct {
	socket string
}

// fakeJail stands in for the jailer a jailed launch starts.
type fakeJail struct {
	dir string

	waitErr   error
	loadErr   error
	resumeErr error
	bootErr   error

	waited  []string
	loads   []loadCall
	resumes []jailedResume
	boots   []LaunchRequest

	argvPath string
}

// install replaces the launcher's seams for one test and restores them afterwards.
func (f *fakeJail) install(t *testing.T) {
	t.Helper()

	previousWait := jailedWaitForAPI
	previousLoad := jailedLoadCheckpoint
	previousResume := jailedResumeGuest
	previousBoot := jailedBootGuest
	t.Cleanup(func() {
		jailedWaitForAPI = previousWait
		jailedLoadCheckpoint = previousLoad
		jailedResumeGuest = previousResume
		jailedBootGuest = previousBoot
	})

	jailedWaitForAPI = func(socketPath string, timeout time.Duration) error {
		f.waited = append(f.waited, socketPath)
		// The real wait blocks until Firecracker answers; the fake waits for the
		// socket its jailer script creates, so a test that reads the jail afterwards
		// is reading a process that has started rather than one about to.
		if !awaitPath(t, socketPath, timeout) {
			return fmt.Errorf("socket not ready within %s", timeout)
		}
		return f.waitErr
	}
	jailedLoadCheckpoint = func(_ context.Context, socketPath, statePath, memPath string) error {
		f.loads = append(f.loads, loadCall{socket: socketPath, state: statePath, mem: memPath})
		return f.loadErr
	}
	jailedResumeGuest = func(_ context.Context, socketPath string) error {
		f.resumes = append(f.resumes, jailedResume{socket: socketPath})
		return f.resumeErr
	}
	jailedBootGuest = func(_ context.Context, _ string, req LaunchRequest) error {
		f.boots = append(f.boots, req)
		return f.bootErr
	}
}

// awaitPath waits until path exists, bounded by timeout, and reports whether it
// appeared.
func awaitPath(t *testing.T, path string, timeout time.Duration) bool {
	t.Helper()

	if timeout > 2*time.Second {
		timeout = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// pidPath is where the fake jailer records its pid: outside the jail, so a test can
// still find the VMM of a launch whose jail was already removed.
func (f *fakeJail) pidPath(instanceID string) string {
	return filepath.Join(f.dir, "pid-"+instanceID)
}

// jailerScript writes an executable that behaves like the jailer for one instance:
// it records its argv, writes the pid file the real jailer writes, creates the API
// socket and cgroup directory, and then waits to be stopped.
func (f *fakeJail) jailerScript(t *testing.T, launcher JailedLauncher, instanceID string) string {
	t.Helper()

	jailRoot := launcher.JailRoot(instanceID)
	f.argvPath = filepath.Join(f.dir, "argv-"+instanceID)
	cgroupDir := filepath.Join(launcher.cgroupRoot, launcher.ParentCgroup, instanceID)

	script := strings.Join([]string{
		"#!/bin/sh",
		"printf '%s\\n' \"$@\" > " + f.argvPath,
		"printf '%s\\n' $$ > " + f.pidPath(instanceID),
		"printf '%s\\n' $$ > " + filepath.Join(jailRoot, "root", "firecracker.pid"),
		"mkdir -p " + cgroupDir,
		"mkdir -p " + filepath.Join(jailRoot, "root", "run", "firecracker"),
		": > " + filepath.Join(jailRoot, "root", "run", "firecracker", instanceID+".sock"),
		"exec sleep 60",
		"",
	}, "\n")

	path := filepath.Join(f.dir, "jailer-"+instanceID)
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// jailedTestLauncher is a launcher configured for one test directory, with a uid
// range wide enough for the instances a test starts.
func jailedTestLauncher(t *testing.T) (JailedLauncher, *fakeJail) {
	t.Helper()

	// A short directory name: the API socket's host path has to fit in a unix socket
	// address, and a long temporary path would make every launch refuse for a reason
	// these tests are not about (see TestJailedLauncherRefusesAPathTooLongForASocket).
	dir, err := os.MkdirTemp("", "sj-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	fake := &fakeJail{dir: dir}

	binary := filepath.Join(fake.dir, "firecracker")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755))
	// A jailer that exists but does nothing: the tests that launch replace it with
	// jailerScript, and the ones that only inspect a launch need the resolution to
	// succeed.
	jailerBin := filepath.Join(fake.dir, "jailer")
	require.NoError(t, os.WriteFile(jailerBin, []byte("#!/bin/sh\n"), 0o755))

	alloc, err := jailer.NewUIDAllocator(3000, 8)
	require.NoError(t, err)

	launcher := JailedLauncher{
		ChrootBaseDir:   filepath.Join(fake.dir, "jails"),
		FirecrackerPath: binary,
		JailerPath:      jailerBin,
		ParentCgroup:    "sbxtest",
		LogDir:          filepath.Join(fake.dir, "logs"),
		UIDs:            alloc,
		cgroupRoot:      filepath.Join(fake.dir, "cgroup"),
	}
	return launcher, fake
}

// jailedSources writes the four host files a restore stages, with content that
// proves which source reached which chroot path.
func jailedSources(t *testing.T) (kernel, rootfs, state, mem string) {
	t.Helper()

	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
		return path
	}
	return write("vmlinux", "kernel-image"), write("rootfs.ext4", "disk-image"),
		write("vm.state", "vm-state"), write("vm.mem", "vm-memory")
}

// needJailedRestoreRequest is a request the jailed launcher serves: an instance
// resumed from its own checkpoint, staged from host files.
func needJailedRestoreRequest(t *testing.T, l JailedLauncher) LaunchRequest {
	t.Helper()

	kernel, rootfs, state, mem := jailedSources(t)
	return LaunchRequest{
		InstanceID: "sbx-1a2b3c",
		Mode:       ModeRestore,
		Kernel:     kernel,
		Rootfs:     rootfs,
		State:      state,
		Mem:        mem,
		JailUID:    0, // let the allocator choose, which is the production path
	}
}

// requireJailHost skips a test that cannot place files inside a jail here.
func requireJailHost(t *testing.T) {
	t.Helper()

	if goruntime.GOOS != "linux" {
		t.Skip("the jailer is Linux-only")
	}
	if os.Geteuid() != 0 {
		t.Skip("placing files inside a jail needs root (chown to the jail uid)")
	}
}

// ownerUID is the uid a file's inode is owned by.
func ownerUID(t *testing.T, path string) int {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err)
	stat, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok, "no inode metadata for %s", path)
	return int(stat.Uid)
}

// TestJailedLauncherStagesTheCheckpointInsideTheJail pins what a restore puts in the
// chroot and what it tells Firecracker: the four files at the checkpoint's stable
// paths, owned by the jail uid, the API socket at its host path, and /snapshot/load
// naming the chroot paths — never the host paths the caller passed.
func TestJailedLauncherStagesTheCheckpointInsideTheJail(t *testing.T) {
	requireJailHost(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = launched.Stop(context.Background()) })

	jailRoot := filepath.Join(launcher.JailRoot(req.InstanceID), "root")
	wantFiles := map[string]string{
		filepath.Join(jailRoot, "kernel", "vmlinux"):     "kernel-image",
		filepath.Join(jailRoot, "drives", "rootfs.ext4"): "disk-image",
		filepath.Join(jailRoot, "snapshot", "vm.state"):  "vm-state",
		filepath.Join(jailRoot, "snapshot", "vm.mem"):    "vm-memory",
	}
	for path, want := range wantFiles {
		body, err := os.ReadFile(path)
		require.NoError(t, err, "the instance must find %s in its jail", path)
		assert.Contains(t, string(body), want, "the staged file must be the caller's source")
	}

	assert.Equal(t, os.FileMode(0o500), modeOf(t, filepath.Join(jailRoot, "kernel", "vmlinux")),
		"the kernel is read once and must not be writable")
	assert.Equal(t, os.FileMode(0o600), modeOf(t, filepath.Join(jailRoot, "drives", "rootfs.ext4")))

	launchedUID := ownerUID(t, filepath.Join(jailRoot, "drives", "rootfs.ext4"))
	assert.Equal(t, 3000, launchedUID, "the staged disk belongs to the instance's uid, not to root")
	assert.Equal(t, launchedUID, ownerUID(t, filepath.Join(jailRoot, "snapshot", "vm.mem")))
	assert.Equal(t, os.FileMode(0o700), modeOf(t, filepath.Join(jailRoot, "snapshot")),
		"only the instance may traverse the directory that holds its checkpoint")

	assert.Equal(t,
		filepath.Join(launcher.JailRoot(req.InstanceID), "root", "run", "firecracker", req.InstanceID+".sock"),
		launched.APISocket, "the caller dials the socket through the host path")

	assert.DirExists(t, filepath.Join(launcher.cgroupRoot, "sbxtest", req.InstanceID),
		"the fake jailer must have created the cgroup the teardown is asserted on")

	require.Len(t, fake.loads, 1)
	assert.Equal(t, jailedStatePath, fake.loads[0].state, "load must name the path inside the jail")
	assert.Equal(t, jailedMemPath, fake.loads[0].mem)
	assert.Equal(t, jailedStatePath, launched.StatePath)
	assert.Equal(t, jailedMemPath, launched.MemPath)

	require.Len(t, fake.resumes, 1, "a loaded checkpoint is resumed explicitly")
	assert.Empty(t, fake.boots, "a restore configures no boot source")
}

// TestJailedLauncherColdBootsInsideTheJail pins the template path: a cold launch
// stages only the kernel and the disk, asks Firecracker to boot them at their chroot
// paths, and reports no checkpoint images.
func TestJailedLauncherColdBootsInsideTheJail(t *testing.T) {
	requireJailHost(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	kernel, rootfs, _, _ := jailedSources(t)
	req := LaunchRequest{
		InstanceID: "sbx-template",
		Mode:       ModeCold,
		Kernel:     kernel,
		Rootfs:     rootfs,
		VCPUs:      2,
		MemoryMB:   256,
	}
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = launched.Stop(context.Background()) })

	jailRoot := filepath.Join(launcher.JailRoot(req.InstanceID), "root")
	assert.FileExists(t, filepath.Join(jailRoot, "kernel", "vmlinux"))
	assert.FileExists(t, filepath.Join(jailRoot, "drives", "rootfs.ext4"))
	assert.NoFileExists(t, filepath.Join(jailRoot, "snapshot", "vm.state"),
		"a cold boot has no checkpoint to stage")

	require.Len(t, fake.boots, 1)
	assert.Equal(t, 2, fake.boots[0].VCPUs)
	assert.Equal(t, 256, fake.boots[0].MemoryMB)
	assert.Empty(t, fake.loads, "a cold boot loads no checkpoint")
	assert.Empty(t, fake.resumes)
	assert.Empty(t, launched.StatePath, "a cold boot has no checkpoint images to report")
	assert.Empty(t, launched.MemPath)
}

// TestJailedLauncherJailerArgv pins the jailer invocation. Every argument here has
// a failure behind it: without --exec-file the jailer has nothing to run, without
// --cgroup-version 2 it writes cgroup v1 files into a v2 hierarchy, without
// --parent-cgroup the cgroup path is a jailer default Stop could not predict, and a
// second --id (or one after --) makes Firecracker exit 153.
func TestJailedLauncherJailerArgv(t *testing.T) {
	launcher, _ := jailedTestLauncher(t)
	launcher.MemoryMaxBytes = 1073741824
	launcher.PidsMax = 64
	launcher.ParentCgroup = "sbxtest"

	req := needJailedRestoreRequest(t, launcher)
	req.JailUID = 3004
	req.JailGID = 3005
	req.NetNS = "sbx-1a2b3c"

	j, err := launcher.newJailed(req)
	require.NoError(t, err)

	want := []string{
		"--id", "sbx-1a2b3c",
		"--exec-file", launcher.FirecrackerPath,
		"--uid", "3004",
		"--gid", "3005",
		"--chroot-base-dir", launcher.ChrootBaseDir,
		"--cgroup-version", "2",
		"--parent-cgroup", "sbxtest",
		"--cgroup", "memory.max=1073741824",
		"--cgroup", "pids.max=64",
		"--netns", "/var/run/netns/sbx-1a2b3c",
		"--",
		"--api-sock", "/run/firecracker/sbx-1a2b3c.sock",
		"--level", "Info",
		"--log-path", "/firecracker.log",
	}
	assert.Equal(t, want, j.jailerArgv())

	// The jailer sends --id to Firecracker itself; a second one is exit 153.
	assert.Equal(t, 1, strings.Count(strings.Join(j.jailerArgv(), " "), "--id "),
		"the instance id must reach Firecracker only through the jailer")
}

// TestJailedLauncherGivesAnUnnamedGIDTheInstanceUID pins the group default: a
// jailer needs a non-root gid, and the instance's own uid is a gid that belongs to
// nothing else.
func TestJailedLauncherGivesAnUnnamedGIDTheInstanceUID(t *testing.T) {
	launcher, _ := jailedTestLauncher(t)
	req := needJailedRestoreRequest(t, launcher)
	req.JailGID = 0

	j, err := launcher.newJailed(req)
	require.NoError(t, err)
	assert.Equal(t, j.uid, j.gid)
}

// TestJailedLauncherRefusesAnUnusableRequest pins the requests the launcher
// declines before it leases a uid or creates anything. Each refusal is a caller
// mistake that would otherwise become a VMM started with the wrong files, as root,
// or outside the node's uid range.
func TestJailedLauncherRefusesAnUnusableRequest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*JailedLauncher, *LaunchRequest)
		want   string
	}{
		{name: "empty mode", mutate: func(_ *JailedLauncher, r *LaunchRequest) { r.Mode = "" }, want: "launch mode is empty"},
		{name: "unknown mode", mutate: func(_ *JailedLauncher, r *LaunchRequest) { r.Mode = "thaw" }, want: `unknown launch mode "thaw"`},
		{name: "escaping instance ID", mutate: func(_ *JailedLauncher, r *LaunchRequest) { r.InstanceID = "../escape" }, want: "path separator"},
		{name: "no chroot base", mutate: func(l *JailedLauncher, _ *LaunchRequest) { l.ChrootBaseDir = "" }, want: "no chroot base directory"},
		{name: "no uid allocator", mutate: func(l *JailedLauncher, _ *LaunchRequest) { l.UIDs = nil }, want: "no uid allocator"},
		{name: "no rootfs", mutate: func(_ *JailedLauncher, r *LaunchRequest) { r.Rootfs = "" }, want: "needs a disk"},
		{name: "no kernel", mutate: func(_ *JailedLauncher, r *LaunchRequest) { r.Kernel = "" }, want: "needs the kernel"},
		{name: "no state image", mutate: func(_ *JailedLauncher, r *LaunchRequest) { r.State = "" }, want: "both checkpoint images"},
		{name: "no memory image", mutate: func(_ *JailedLauncher, r *LaunchRequest) { r.Mem = "" }, want: "both checkpoint images"},
		{name: "cold without a size", mutate: func(_ *JailedLauncher, r *LaunchRequest) { r.Mode = ModeCold; r.State, r.Mem = "", "" }, want: "needs a size"},
		{name: "uid outside the range", mutate: func(_ *JailedLauncher, r *LaunchRequest) { r.JailUID = 4000 }, want: "outside the configured range"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			launcher, fake := jailedTestLauncher(t)
			fake.install(t)
			alloc := launcher.UIDs
			req := needJailedRestoreRequest(t, launcher)
			tt.mutate(&launcher, &req)

			_, err := launcher.Start(context.Background(), req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Empty(t, fake.waited, "a refused request must not start a process")
			assert.False(t, alloc.Leased(3000), "a refused request must not lease a uid")
		})
	}
}

// TestJailedLauncherFailsOnAPathRuleItsOwn pins that the instance ID is checked with
// the node's one rule for "this string can name a file", so the sentinel a caller
// matches is the shared one.
func TestJailedLauncherFailsOnAPathRuleItsOwn(t *testing.T) {
	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)
	req.InstanceID = "../escape"

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.ErrorIs(t, err, scruntime.ErrTaskIDPathSeparator)
}

// TestJailedLauncherStopReleasesEverythingItHeld pins the teardown: the process, the
// console socket, the API socket, the cgroup and the jail directory are gone, the
// uid is free again, and a second stop succeeds. The uid assertions are the ones
// that matter most — a lease kept after cleanup would exhaust the range, and a uid
// released before cleanup would let a second instance join a live one.
func TestJailedLauncherStopReleasesEverythingItHeld(t *testing.T) {
	requireJailHost(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	launcher.ConsoleSocketDir = filepath.Join(fake.dir, "console")

	first := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, first.InstanceID)
	launched, err := launcher.Start(context.Background(), first)
	require.NoError(t, err)

	second := first
	second.InstanceID = "sbx-4d5e6f"
	launcher.JailerPath = fake.jailerScript(t, launcher, second.InstanceID)
	other, err := launcher.Start(context.Background(), second)
	require.NoError(t, err)

	firstUID := ownerUID(t, filepath.Join(launcher.JailRoot(first.InstanceID), "root", "drives", "rootfs.ext4"))
	secondUID := ownerUID(t, filepath.Join(launcher.JailRoot(second.InstanceID), "root", "drives", "rootfs.ext4"))
	assert.NotEqual(t, firstUID, secondUID, "two live instances must not share a uid")

	pid := pidFromFake(t, fake, first.InstanceID)
	require.NoError(t, launched.Stop(context.Background()))

	assert.False(t, processAlive(pid), "the VMM must be gone after a stop")
	assert.NoFileExists(t, launched.APISocket)
	assert.NoFileExists(t, filepath.Join(launcher.ConsoleSocketDir, first.InstanceID+".console.sock"))
	assert.NoDirExists(t, launcher.JailRoot(first.InstanceID), "the jail directory must not outlive the instance")
	assert.NoDirExists(t, filepath.Join(launcher.cgroupRoot, "sbxtest", first.InstanceID))
	assert.False(t, launcher.UIDs.Leased(firstUID), "a stopped instance's uid must be free again")

	require.NoError(t, launched.Stop(context.Background()), "a retried stop must succeed")

	// The freed uid is the one the next launch takes, which is what keeps the range
	// from leaking on a node that starts and stops instances all day.
	launcher.JailerPath = fake.jailerScript(t, launcher, first.InstanceID)
	relaunched, err := launcher.Start(context.Background(), first)
	require.NoError(t, err, "the same id may be launched again after its teardown")
	assert.Equal(t, firstUID,
		ownerUID(t, filepath.Join(launcher.JailRoot(first.InstanceID), "root", "drives", "rootfs.ext4")),
		"cleanup finished, so the uid may be reused")

	// A stale handle must not touch the instance that now owns the id.
	require.NoError(t, launched.Stop(context.Background()))
	assert.FileExists(t, relaunched.APISocket, "a retried stop must not remove a newer instance's socket")
	assert.Equal(t, secondUID, ownerUID(t, filepath.Join(launcher.JailRoot(second.InstanceID), "root", "drives", "rootfs.ext4")))

	require.NoError(t, other.Stop(context.Background()))
	require.NoError(t, relaunched.Stop(context.Background()))
}

// TestJailedLauncherFailedStartLeavesNothingBehind pins the contract's hardest
// clause: Start returns no handle for a launch that failed, so the launcher itself
// has to end the process, remove the socket, cgroup and jail, and release the uid —
// and the next launch under the same ID must not inherit the failure.
func TestJailedLauncherFailedStartLeavesNothingBehind(t *testing.T) {
	requireJailHost(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	fake.loadErr = assert.AnError

	req := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	launched, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load the checkpoint into the jail")
	assert.Nil(t, launched.Stop, "a failed launch must not hand back a stop for a process it already killed")

	pid := pidFromFake(t, fake, req.InstanceID)
	assert.False(t, processAlive(pid), "the VMM of a failed launch must be gone")
	assert.NoFileExists(t, filepath.Join(launcher.JailRoot(req.InstanceID), "root", "run", "firecracker", req.InstanceID+".sock"))
	assert.NoDirExists(t, launcher.JailRoot(req.InstanceID))
	assert.NoDirExists(t, filepath.Join(launcher.cgroupRoot, "sbxtest", req.InstanceID))
	assert.False(t, launcher.UIDs.Leased(3000), "a failed launch must give its uid back")

	// The same ID launches cleanly afterwards.
	fake.loadErr = nil
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)
	retried, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, retried.Stop(context.Background()))
}

// TestJailedLauncherRefusesToLaunchOverALiveID pins the check that makes a jail
// directory safe to remove: a jail whose VMM is still running is not a leftover, and
// launching over it would delete a live instance's chroot.
func TestJailedLauncherRefusesToLaunchOverALiveID(t *testing.T) {
	requireJailHost(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = launched.Stop(context.Background()) })

	_, err = launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is already running")

	// The running instance is untouched: its socket and its jail are still there.
	assert.FileExists(t, launched.APISocket)
	assert.DirExists(t, launcher.JailRoot(req.InstanceID))
}

// TestJailedLauncherStopsAnInstanceWhoseVMMAlreadyExited pins the retry/restart
// path: a VMM that died on its own leaves a socket, a cgroup and a jail behind, and
// a stop after that is a cleanup, not an error.
func TestJailedLauncherStopsAnInstanceWhoseVMMAlreadyExited(t *testing.T) {
	requireJailHost(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)

	pid := pidFromFake(t, fake, req.InstanceID)
	proc, err := os.FindProcess(pid)
	require.NoError(t, err)
	require.NoError(t, proc.Kill())
	waitForProcessGone(t, pid)

	require.NoError(t, launched.Stop(context.Background()))
	assert.NoFileExists(t, launched.APISocket)
	assert.NoDirExists(t, launcher.JailRoot(req.InstanceID))
	assert.NoDirExists(t, filepath.Join(launcher.cgroupRoot, "sbxtest", req.InstanceID))
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestJailedLauncherRefusesAPathTooLongForASocket pins the one configuration mistake
// a jail cannot work around: the socket's host path is inside the chroot, so a chroot
// base whose paths exceed a unix socket address makes every launch fail at connect()
// with an error that says nothing useful. The launcher says so before it leases a uid.
func TestJailedLauncherRefusesAPathTooLongForASocket(t *testing.T) {
	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	launcher.ChrootBaseDir = filepath.Join(t.TempDir(), strings.Repeat("d", 80))

	_, err := launcher.Start(context.Background(), needJailedRestoreRequest(t, launcher))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than the 107 a unix socket address holds")
	assert.False(t, launcher.UIDs.Leased(3000), "a refused launch must not cost a uid")
	assert.Empty(t, fake.waited)
}

// TestNetnsPath pins how a network namespace name becomes the path the jailer joins.
func TestNetnsPath(t *testing.T) {
	assert.Empty(t, netnsPath(""))
	assert.Equal(t, "/var/run/netns/sbx-1", netnsPath("sbx-1"))
	assert.Equal(t, "/run/netns/custom", netnsPath("/run/netns/custom"))
}

// pidFromFake is the pid the fake jailer ran under for one instance.
func pidFromFake(t *testing.T, fake *fakeJail, instanceID string) int {
	t.Helper()

	body, err := os.ReadFile(fake.pidPath(instanceID))
	require.NoError(t, err)

	var pid int
	_, err = fmt.Sscanf(strings.TrimSpace(string(body)), "%d", &pid)
	require.NoError(t, err)
	return pid
}

// modeOf is a file's permission bits.
func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Mode().Perm()
}
