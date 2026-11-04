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
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
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

	// owned records every "give this path to this uid" the launcher asked for, and
	// ownErr is what that request answers.
	owned  []jailOwnership
	ownErr error

	argvPath string
}

// jailOwnership is one staged path and the uid and gid it was handed to.
type jailOwnership struct {
	path string
	uid  int
	gid  int
}

// uidOf is the uid the launcher asked for the staged file that ends in suffix, or 0
// when it asked for none.
func (f *fakeJail) uidOf(suffix string) int {
	for _, own := range f.owned {
		if strings.HasSuffix(own.path, suffix) {
			return own.uid
		}
	}
	return 0
}

// install replaces the launcher's seams for one test and restores them afterwards.
func (f *fakeJail) install(t *testing.T) {
	t.Helper()

	previousWait := jailedWaitForAPI
	previousLoad := jailedLoadCheckpoint
	previousResume := jailedResumeGuest
	previousBoot := jailedBootGuest
	previousOwn := jailOwn
	t.Cleanup(func() {
		jailedWaitForAPI = previousWait
		jailedLoadCheckpoint = previousLoad
		jailedResumeGuest = previousResume
		jailedBootGuest = previousBoot
		jailOwn = previousOwn
	})

	jailOwn = func(path string, uid, gid int) error {
		f.owned = append(f.owned, jailOwnership{path: path, uid: uid, gid: gid})
		return f.ownErr
	}

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

// requireJailHost skips a test that needs root: placing files inside a chroot as
// another uid, which is what a real staging does and what a seam cannot stand in for
// when the assertion is about the filesystem itself.
func requireJailHost(t *testing.T) {
	t.Helper()

	requireJailPlatform(t)
	if os.Geteuid() != 0 {
		t.Skip("building a chroot for another uid needs root")
	}
}

// requireJailPlatform skips a test on a platform without the jailer. Nothing else is
// skipped: the launcher's ownership step is a seam, so staging, the jailer argv, a
// fake jailer process and the whole teardown run for any user — which is what CI is.
func requireJailPlatform(t *testing.T) {
	t.Helper()

	if goruntime.GOOS != "linux" {
		t.Skip("the jailer is Linux-only")
	}
}

// TestJailedLauncherStagesTheCheckpointInsideTheJail pins what a restore puts in the
// chroot and what it tells Firecracker: the four files at the checkpoint's stable
// paths, owned by the jail uid, the API socket at its host path, and /snapshot/load
// naming the chroot paths — never the host paths the caller passed.
func TestJailedLauncherStagesTheCheckpointInsideTheJail(t *testing.T) {
	requireJailPlatform(t)

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

	assert.Equal(t, 3000, fake.uidOf(filepath.Join("drives", "rootfs.ext4")),
		"the staged disk is handed to the instance's uid, not to root")
	assert.Equal(t, 3000, fake.uidOf(filepath.Join("snapshot", "vm.mem")))
	assert.Equal(t, 3000, fake.uidOf(filepath.Join("kernel", "vmlinux")))
	assert.Equal(t, 3000, fake.uidOf(filepath.Join("snapshot")),
		"the directory holding the checkpoint is the instance's too")
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
	requireJailPlatform(t)

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
		{name: "no firecracker binary", mutate: func(l *JailedLauncher, _ *LaunchRequest) {
			l.FirecrackerPath = filepath.Join(t.TempDir(), "missing")
		}, want: "firecracker binary not found"},
		{name: "no jailer binary", mutate: func(l *JailedLauncher, _ *LaunchRequest) {
			l.JailerPath = filepath.Join(t.TempDir(), "missing")
		}, want: "jailer binary not found"},
		{name: "console socket path too long", mutate: func(l *JailedLauncher, _ *LaunchRequest) {
			l.ConsoleSocketDir = filepath.Join(t.TempDir(), strings.Repeat("c", 90))
		}, want: "a unix socket address holds"},
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
	requireJailPlatform(t)

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

	firstUID := fake.uidOf(filepath.Join(first.InstanceID, "root", "drives", "rootfs.ext4"))
	secondUID := fake.uidOf(filepath.Join(second.InstanceID, "root", "drives", "rootfs.ext4"))
	require.NotZero(t, firstUID)
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
	assert.Equal(t, firstUID, fake.uidOf(filepath.Join(first.InstanceID, "root", "drives", "rootfs.ext4")),
		"cleanup finished, so the uid may be reused")

	// A stale handle must not touch the instance that now owns the id.
	require.NoError(t, launched.Stop(context.Background()))
	assert.FileExists(t, relaunched.APISocket, "a retried stop must not remove a newer instance's socket")
	assert.Equal(t, secondUID, fake.uidOf(filepath.Join(second.InstanceID, "root", "drives", "rootfs.ext4")))

	require.NoError(t, other.Stop(context.Background()))
	require.NoError(t, relaunched.Stop(context.Background()))
}

// TestJailedLauncherFailedStartLeavesNothingBehind pins the contract's hardest
// clause: Start returns no handle for a launch that failed, so the launcher itself
// has to end the process, remove the socket, cgroup and jail, and release the uid —
// and the next launch under the same ID must not inherit the failure.
func TestJailedLauncherFailedStartLeavesNothingBehind(t *testing.T) {
	requireJailPlatform(t)

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
	requireJailPlatform(t)

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
	requireJailPlatform(t)

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

// apiCall is one request a fake Firecracker API received.
type apiCall struct {
	method string
	path   string
	body   map[string]interface{}
}

// fakeAPI is a Firecracker API socket a test can read. The real putFirecrackerAPI
// and patchFirecrackerAPI talk to it over a unix socket, so the payloads a jailed
// launch sends are pinned here without a VMM: what a checkpoint load tells
// Firecracker, in which order, and with which status handled as failure.
type fakeAPI struct {
	t      *testing.T
	socket string
	status int
	calls  []apiCall
}

// newFakeAPI starts a socket and returns it. It is closed when the test ends.
func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()

	dir := t.TempDir()
	api := &fakeAPI{t: t, socket: filepath.Join(dir, "api.sock"), status: http.StatusNoContent}

	listener, err := net.Listen("unix", api.socket)
	require.NoError(t, err)

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var decoded map[string]interface{}
		if len(body) > 0 {
			require.NoError(t, json.Unmarshal(body, &decoded))
		}
		api.calls = append(api.calls, apiCall{method: r.Method, path: r.URL.Path, body: decoded})
		w.WriteHeader(api.status)
	})}

	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})

	return api
}

// TestBootJailedGuestSendsTheTemplateConfiguration pins what a cold jailed boot asks
// Firecracker for. The paths are the checkpoint contract: a guest booted from
// /kernel/vmlinux and /drives/rootfs.ext4 is a guest whose snapshot restores in any
// other jail, which is the whole reason the paths are fixed.
func TestBootJailedGuestSendsTheTemplateConfiguration(t *testing.T) {
	api := newFakeAPI(t)

	require.NoError(t, bootJailedGuest(context.Background(), api.socket,
		LaunchRequest{VCPUs: 2, MemoryMB: 256}))

	require.Len(t, api.calls, 4, "a cold boot is boot source, drive, machine config, then start")
	assert.Equal(t, apiCall{method: http.MethodPut, path: "/boot-source", body: map[string]interface{}{
		"kernel_image_path": "/kernel/vmlinux",
		"boot_args":         jailedBootArgs,
	}}, api.calls[0])
	assert.Equal(t, apiCall{method: http.MethodPut, path: "/drives/rootfs", body: map[string]interface{}{
		"drive_id":       "rootfs",
		"path_on_host":   "/drives/rootfs.ext4",
		"is_root_device": true,
		"is_read_only":   false,
	}}, api.calls[1])
	assert.Equal(t, apiCall{method: http.MethodPut, path: "/machine-config", body: map[string]interface{}{
		"vcpu_count":        float64(2),
		"mem_size_mib":      float64(256),
		"smt":               false,
		"track_dirty_pages": false,
	}}, api.calls[2])
	assert.Equal(t, apiCall{method: http.MethodPut, path: "/actions", body: map[string]interface{}{
		"action_type": "InstanceStart",
	}}, api.calls[3])
}

// TestLoadJailedCheckpointLeavesTheGuestStopped pins the load: the chroot paths, a
// File memory backend named explicitly, and resume_vm=false. The false is the point
// of the two-call resume — a load that resumed the guest itself would leave no window
// for a per-instance drive or MMDS before the guest runs.
func TestLoadJailedCheckpointLeavesTheGuestStopped(t *testing.T) {
	api := newFakeAPI(t)

	require.NoError(t, loadJailedCheckpoint(context.Background(), api.socket,
		"/snapshot/vm.state", "/snapshot/vm.mem"))

	require.Len(t, api.calls, 1)
	assert.Equal(t, http.MethodPut, api.calls[0].method)
	assert.Equal(t, "/snapshot/load", api.calls[0].path)
	assert.Equal(t, map[string]interface{}{
		"snapshot_path": "/snapshot/vm.state",
		"mem_backend": map[string]interface{}{
			"backend_path": "/snapshot/vm.mem",
			"backend_type": "File",
		},
		"resume_vm": false,
	}, api.calls[0].body)
}

// TestResumeJailedGuestPatchesTheVMState pins the call that actually runs a loaded
// guest. InstanceStart is not it: Firecracker refuses that after a snapshot is loaded.
func TestResumeJailedGuestPatchesTheVMState(t *testing.T) {
	api := newFakeAPI(t)

	require.NoError(t, resumeJailedGuest(context.Background(), api.socket))

	require.Len(t, api.calls, 1)
	assert.Equal(t, apiCall{method: http.MethodPatch, path: "/vm",
		body: map[string]interface{}{"state": "Resumed"}}, api.calls[0])
}

// TestJailedAPIFailuresAreReported pins that a VMM answering anything but success
// fails the step that asked: a launch must not report an instance as running because
// a request was sent, only because it was accepted.
func TestJailedAPIFailuresAreReported(t *testing.T) {
	api := newFakeAPI(t)
	api.status = http.StatusBadRequest

	ctx := context.Background()
	require.Error(t, bootJailedGuest(ctx, api.socket, LaunchRequest{VCPUs: 1, MemoryMB: 128}))
	require.Error(t, loadJailedCheckpoint(ctx, api.socket, jailedStatePath, jailedMemPath))
	require.Error(t, resumeJailedGuest(ctx, api.socket))
}

// TestJailedLauncherRefusesToStageAMissingFile pins the first failure a caller can
// cause: the kernel, disk or a checkpoint image is gone. Each is refused before the
// VMM starts, and the launch leaves no jail and no uid behind.
func TestJailedLauncherRefusesToStageAMissingFile(t *testing.T) {
	tests := []struct {
		name   string
		remove func(*LaunchRequest)
		want   string
	}{
		{name: "kernel", remove: func(r *LaunchRequest) { r.Kernel = filepath.Join(t.TempDir(), "gone") }, want: "at /kernel/vmlinux"},
		{name: "disk", remove: func(r *LaunchRequest) { r.Rootfs = filepath.Join(t.TempDir(), "gone") }, want: "at /drives/rootfs.ext4"},
		{name: "state", remove: func(r *LaunchRequest) { r.State = filepath.Join(t.TempDir(), "gone") }, want: "at /snapshot/vm.state"},
		{name: "memory", remove: func(r *LaunchRequest) { r.Mem = filepath.Join(t.TempDir(), "gone") }, want: "at /snapshot/vm.mem"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireJailPlatform(t)

			launcher, fake := jailedTestLauncher(t)
			fake.install(t)
			req := needJailedRestoreRequest(t, launcher)
			launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)
			tt.remove(&req)

			_, err := launcher.Start(context.Background(), req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want, "the error names the chroot path the file was for")
			assert.Empty(t, fake.waited, "nothing may be spawned without the files the instance needs")
			assert.NoDirExists(t, launcher.JailRoot(req.InstanceID), "a failed staging must not leave a jail")
			assert.False(t, launcher.UIDs.Leased(3000), "a failed staging must give its uid back")
		})
	}
}

// TestJailedLauncherRefusesADirectoryAsAnImage pins the second shape of unusable
// input: the path exists, so it is not "missing", but it is not a file either. The
// copy refuses it rather than streaming a directory into the jail.
func TestJailedLauncherRefusesADirectoryAsAnImage(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)
	req.Rootfs = t.TempDir()
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestJailedInstanceRefusesAChrootItCannotBuild pins the staging directory failure.
// It is white-box on purpose: a chroot that cannot be created is not reachable
// through Start on a host where the launcher owns its base directory, and the step
// that fails is what a caller's error message has to name.
func TestJailedInstanceRefusesAChrootItCannotBuild(t *testing.T) {
	requireJailHost(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)

	j, err := launcher.newJailed(req)
	require.NoError(t, err)
	t.Cleanup(func() { j.lease.Release() })

	// A file where the jail's kernel directory has to go: MkdirAll cannot make a
	// directory under a non-directory, which is the "the chroot cannot be built"
	// failure in its cleanest form.
	require.NoError(t, os.MkdirAll(j.chroot, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(j.chroot, "kernel"), nil, 0o600))

	err = j.stage(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create /kernel in the jail")
}

// TestJailedLauncherRefusesAJailItCannotInspect pins the stale-jail check's other
// shape: the path exists but cannot even be inspected, so the launcher cannot tell a
// leftover from a live instance and refuses rather than guess.
func TestJailedLauncherRefusesAJailItCannotInspect(t *testing.T) {
	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)

	// A file where the jailer's per-binary directory goes: every jail path under it
	// answers ENOTDIR, which is not "no jail here".
	require.NoError(t, os.MkdirAll(launcher.ChrootBaseDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(launcher.ChrootBaseDir, launcher.execFileName()), nil, 0o600))

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inspect the jail")
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestJailedLauncherRefusesAJailWhosePIDFileIsUnreadable pins why an unparseable pid
// is an error and not "no pid, so the jail is stale": deleting this jail could delete a
// live instance's disk.
func TestJailedLauncherRefusesAJailWhosePIDFileIsUnreadable(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)

	pidFile := filepath.Join(launcher.JailRoot(req.InstanceID), "root", "firecracker.pid")
	require.NoError(t, os.MkdirAll(filepath.Dir(pidFile), 0o700))
	require.NoError(t, os.WriteFile(pidFile, []byte("not-a-pid\n"), 0o600))

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse the jail's pid file")
	assert.FileExists(t, pidFile, "a jail the launcher refuses must be left for an operator")
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestJailedLauncherReplacesAJailWhoseVMMIsGone pins the recovery path: a jail left by
// a VMM that died is a leftover, and the next launch under the same id takes it over
// instead of failing on the leftover's socket and /dev entries.
func TestJailedLauncherReplacesAJailWhoseVMMIsGone(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)

	// A pid no process has: this one, from a child that has already exited.
	dead := exec.Command("true")
	require.NoError(t, dead.Run())
	deadPID := dead.Process.Pid

	pidFile := filepath.Join(launcher.JailRoot(req.InstanceID), "root", "firecracker.pid")
	require.NoError(t, os.MkdirAll(filepath.Dir(pidFile), 0o700))
	require.NoError(t, os.WriteFile(pidFile, []byte(strconv.Itoa(deadPID)+"\n"), 0o600))
	stale := filepath.Join(launcher.JailRoot(req.InstanceID), "root", "stale-file")
	require.NoError(t, os.WriteFile(stale, []byte("left behind"), 0o600))

	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)
	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = launched.Stop(context.Background()) })

	assert.NoFileExists(t, stale, "the previous jail must be gone, not merged with this one")
}

// TestJailedLauncherRefusesACgroupThatStillHasProcesses pins the saw-off between "a
// leftover cgroup" and "a live instance's cgroup": only an empty one may be removed,
// because removing a populated one would move a running VMM.
func TestJailedLauncherRefusesACgroupThatStillHasProcesses(t *testing.T) {
	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)

	occupied := filepath.Join(launcher.cgroupRoot, launcher.ParentCgroup, req.InstanceID, "child")
	require.NoError(t, os.MkdirAll(occupied, 0o755))

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a previous instance left behind")
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestJailedLauncherReportsAnUnstartableJailer pins the spawn failure: the jailer is
// there, but the kernel cannot execute it. Nothing is left behind, because Start
// returns no handle for it.
func TestJailedLauncherReportsAnUnstartableJailer(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)

	broken := filepath.Join(fake.dir, "jailer-broken")
	require.NoError(t, os.WriteFile(broken, []byte("#!/nonexistent/interpreter\n"), 0o755))
	launcher.JailerPath = broken

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start jailer")
	assert.NoDirExists(t, launcher.JailRoot(req.InstanceID))
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestJailedLauncherReportsAnUnopenableConsole pins the console as a launch
// requirement: a caller that asked for logs and cannot have them is told, rather than
// getting an instance whose output nobody can read.
func TestJailedLauncherReportsAnUnopenableConsole(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)

	notADirectory := filepath.Join(fake.dir, "logs-is-a-file")
	require.NoError(t, os.WriteFile(notADirectory, nil, 0o600))
	launcher.LogDir = notADirectory

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "console")
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestJailedLauncherReportsAFailedResume pins the last step of a restore: the
// checkpoint loaded but the guest never ran, so the instance is not launched and its
// jail is torn down.
func TestJailedLauncherReportsAFailedResume(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	fake.resumeErr = assert.AnError

	req := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resume the guest")
	assert.NoDirExists(t, launcher.JailRoot(req.InstanceID))
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestJailedLauncherReportsAFailedColdBoot is the same for the template path.
func TestJailedLauncherReportsAFailedColdBoot(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	fake.bootErr = assert.AnError

	kernel, rootfs, _, _ := jailedSources(t)
	req := LaunchRequest{
		InstanceID: "sbx-template",
		Mode:       ModeCold,
		Kernel:     kernel,
		Rootfs:     rootfs,
		VCPUs:      1,
		MemoryMB:   128,
	}
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boot the instance in its jail")
	assert.NoDirExists(t, launcher.JailRoot(req.InstanceID))
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestJailedLauncherKeepsItsHandlesWhileTheVMWillNotDie pins the one teardown that
// must not clean up: if the process cannot be stopped, the socket, cgroup and jail are
// the only handles to a VM that is still running, and the uid must stay leased. The
// retry, once the process is gone, then finishes the job.
func TestJailedLauncherKeepsItsHandlesWhileTheVMWillNotDie(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	j, err := launcher.newJailed(req)
	require.NoError(t, err)
	require.NoError(t, j.prepare(context.Background()))
	require.NoError(t, j.spawn())
	require.NoError(t, jailedWaitForAPI(j.hostSocket, time.Second))

	pid := pidFromFake(t, fake, req.InstanceID)
	t.Cleanup(func() {
		proc, err := os.FindProcess(pid)
		if err == nil {
			_ = proc.Kill()
		}
		// A failed assertion here must not leave a jail on the host.
		_ = os.RemoveAll(j.jailRoot)
	})

	previous := signalProcess
	signalProcess = func(*os.Process, syscall.Signal) error { return syscall.EPERM }
	t.Cleanup(func() { signalProcess = previous })

	err = j.stop(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.EPERM)
	assert.DirExists(t, j.jailRoot, "a VM that is still running keeps its jail")
	assert.True(t, launcher.UIDs.Leased(j.uid), "and it keeps its uid")

	signalProcess = previous
	proc, err := os.FindProcess(pid)
	require.NoError(t, err)
	require.NoError(t, proc.Kill())
	waitForProcessGone(t, pid)

	require.NoError(t, j.stop(context.Background()), "the retry finishes the teardown")
	assert.NoDirExists(t, j.jailRoot)
	assert.False(t, launcher.UIDs.Leased(j.uid))
}

// TestJailedLauncherKeepsTheVMMsLog pins where a VMM that died inside the fence says
// why: its log is inside the jail, which the teardown deletes, so it is copied out
// first.
func TestJailedLauncherKeepsTheVMMsLog(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)

	vmmLog := filepath.Join(launcher.JailRoot(req.InstanceID), "root", "firecracker.log")
	require.NoError(t, os.WriteFile(vmmLog, []byte("guest panicked\n"), 0o600))

	require.NoError(t, launched.Stop(context.Background()))

	kept := filepath.Join(launcher.LogDir, req.InstanceID+".firecracker.log")
	body, err := os.ReadFile(kept)
	require.NoError(t, err, "the VMM log must survive the jail")
	assert.Contains(t, string(body), "guest panicked")
}

// TestJailedLauncherReportsASocketItCannotRemove pins the teardown's error path: the
// instance is gone and the uid is free (so the caller is not blocked on a uid), but the
// removal that failed is reported once, and a retry does not touch the paths again —
// they may belong to a newer instance by then.
func TestJailedLauncherReportsASocketItCannotRemove(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)

	// A directory where the socket is: os.Remove cannot take a non-empty one.
	require.NoError(t, os.Remove(launched.APISocket))
	require.NoError(t, os.MkdirAll(filepath.Join(launched.APISocket, "child"), 0o700))

	err = launched.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance socket")
	assert.NoDirExists(t, launcher.JailRoot(req.InstanceID), "the jail goes even when one removal fails")
	assert.False(t, launcher.UIDs.Leased(3000), "and the uid is free: nothing of the instance is running")

	require.NoError(t, launched.Stop(context.Background()), "a retried stop must not fail on work already done")
}

// TestJailedLauncherWaitsOutACgroupBeforeGivingUp pins the settle: a cgroup the kernel
// has not released yet becomes removable a moment later, and the teardown waits rather
// than leaking one directory per instance.
func TestJailedLauncherWaitsOutACgroupBeforeGivingUp(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)

	cgroupDir := filepath.Join(launcher.cgroupRoot, launcher.ParentCgroup, req.InstanceID)
	require.DirExists(t, cgroupDir)

	go func() {
		time.Sleep(60 * time.Millisecond)
		_ = os.Remove(cgroupDir)
	}()

	require.NoError(t, launched.Stop(context.Background()))
	assert.NoDirExists(t, cgroupDir)
}

// TestJailedLauncherReportsAnUncreatableConsoleSocket pins the console socket as part
// of the launch: if the socket cannot be created the guest would have no input, so the
// launch fails before the VMM starts and stages nothing.
func TestJailedLauncherReportsAnUncreatableConsoleSocket(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)

	// A file where the console socket directory has to go.
	inTheWay := filepath.Join(fake.dir, "console-is-a-file")
	require.NoError(t, os.WriteFile(inTheWay, nil, 0o600))
	launcher.ConsoleSocketDir = inTheWay
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create the instance console")
	assert.False(t, launcher.UIDs.Leased(3000), "nothing started, so the uid goes back")
}

// TestJailedLauncherReportsTheFailureThatStoppedIt pins fail's precedence: when a
// launch fails and its own teardown also fails, the caller must be told what stopped the
// launch, not what the cleanup tripped over. The teardown's complaint goes to the log.
func TestJailedLauncherReportsTheFailureThatStoppedIt(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	fake.loadErr = assert.AnError
	req := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	previous := signalProcess
	signalProcess = func(*os.Process, syscall.Signal) error { return syscall.EPERM }
	t.Cleanup(func() { signalProcess = previous })

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load the checkpoint into the jail",
		"the launch failure is what the caller sees")

	// The VMM the broken signal left behind is this test's to clean up.
	t.Cleanup(func() { _ = os.RemoveAll(launcher.JailRoot(req.InstanceID)) })
	pid := pidFromFake(t, fake, req.InstanceID)
	proc, err := os.FindProcess(pid)
	require.NoError(t, err)
	require.NoError(t, proc.Kill())
	waitForProcessGone(t, pid)

	// The uid stays leased: while the VMM might still be alive, handing its uid to a
	// second instance is the one thing the allocator must not do. A daemon restart
	// reclaims it through the process table, which is why that check exists.
	signalProcess = previous
	require.NoError(t, os.RemoveAll(launcher.JailRoot(req.InstanceID)))
	assert.True(t, launcher.UIDs.Leased(3000))
}

// TestJailedLauncherReportsACgroupItCannotRemove pins the other teardown error: a
// cgroup that still has something in it is reported after the settle window, while the
// instance itself is still taken down (the process is gone, so the uid is free).
func TestJailedLauncherReportsACgroupItCannotRemove(t *testing.T) {
	requireJailPlatform(t)

	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)
	launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

	launched, err := launcher.Start(context.Background(), req)
	require.NoError(t, err)

	cgroupDir := filepath.Join(launcher.cgroupRoot, launcher.ParentCgroup, req.InstanceID)
	require.NoError(t, os.MkdirAll(filepath.Join(cgroupDir, "child"), 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(cgroupDir) })

	err = launched.Stop(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance cgroup")
	assert.NoDirExists(t, launcher.JailRoot(req.InstanceID))
	assert.False(t, launcher.UIDs.Leased(3000))

	require.NoError(t, os.RemoveAll(cgroupDir))
	require.NoError(t, launched.Stop(context.Background()), "the retry has nothing left to do")
}

// TestJailedLauncherRefusesAFileItCannotHandOver pins the last step of staging as a
// failure a caller can see: a file the jail uid does not own is a file the VMM cannot
// open, so the launch stops instead of starting a VMM that will fail on its own disk.
// The ownership seam is what makes this reachable without root.
func TestJailedLauncherRefusesAFileItCannotHandOver(t *testing.T) {
	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	fake.ownErr = syscall.EPERM
	req := needJailedRestoreRequest(t, launcher)

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "give")
	assert.Contains(t, err.Error(), "to uid 3000")
	assert.Empty(t, fake.waited, "nothing may be spawned while the files are not the instance's")
	assert.NoDirExists(t, launcher.JailRoot(req.InstanceID))
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestJailedLauncherRefusesAReadFailureOnTheJailPIDFile pins the other way the pid file
// can be unusable: it is not a file. Reading it cannot answer "is this jail live", and
// the jail is left for an operator rather than deleted.
func TestJailedLauncherRefusesAReadFailureOnTheJailPIDFile(t *testing.T) {
	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)

	pidFile := filepath.Join(launcher.JailRoot(req.InstanceID), "root", "firecracker.pid")
	require.NoError(t, os.MkdirAll(pidFile, 0o700))

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read the jail's pid file")
	assert.DirExists(t, pidFile)
	assert.False(t, launcher.UIDs.Leased(3000))
}

// TestJailedLauncherClosesTheConsoleWhenTheJailerWillNotStart pins that the console
// socket does not outlive a launch that never produced a VMM: a socket nobody serves
// would answer attach with a guest that does not exist.
func TestJailedLauncherClosesTheConsoleWhenTheJailerWillNotStart(t *testing.T) {
	launcher, fake := jailedTestLauncher(t)
	fake.install(t)
	req := needJailedRestoreRequest(t, launcher)

	broken := filepath.Join(fake.dir, "jailer-broken")
	require.NoError(t, os.WriteFile(broken, []byte("#!/nonexistent/interpreter\n"), 0o755))
	launcher.JailerPath = broken
	launcher.ConsoleSocketDir = filepath.Join(fake.dir, "console")

	_, err := launcher.Start(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start jailer")
	assert.NoFileExists(t, filepath.Join(launcher.ConsoleSocketDir, req.InstanceID+".console.sock"))
}

// TestJailedLauncherLeavesNothingBehindWhenStagingFails is the invariant every failed
// launch owes its caller, and it is deliberately not gated on root: whichever staging
// step fails first is an environment detail, but "no process, socket, cgroup, jail or
// uid lease left behind" holds on every host. Without it, a production staging failure
// could leak a jail nobody has a handle to.
func TestJailedLauncherLeavesNothingBehindWhenStagingFails(t *testing.T) {
	tests := []struct {
		name string
		// fail breaks the launch, the way a caller or the host can.
		fail func(*fakeJail, *LaunchRequest)
	}{
		{
			name: "the kernel is gone",
			fail: func(_ *fakeJail, r *LaunchRequest) {
				r.Kernel = filepath.Join(t.TempDir(), "gone")
			},
		},
		{
			name: "an image is not a file",
			fail: func(_ *fakeJail, r *LaunchRequest) {
				r.Rootfs = t.TempDir()
			},
		},
		{
			name: "the file cannot be given to the jail uid",
			fail: func(f *fakeJail, _ *LaunchRequest) {
				f.ownErr = syscall.EPERM
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			launcher, fake := jailedTestLauncher(t)
			fake.install(t)
			req := needJailedRestoreRequest(t, launcher)
			tt.fail(fake, &req)
			launcher.JailerPath = fake.jailerScript(t, launcher, req.InstanceID)

			_, err := launcher.Start(context.Background(), req)
			require.Error(t, err, "a launch that cannot stage must not report an instance")

			assert.Empty(t, fake.waited, "no VMM may be spawned for a launch that never staged")
			assert.NoFileExists(t, fake.pidPath(req.InstanceID), "no process of this launch may exist")
			assert.NoDirExists(t, launcher.JailRoot(req.InstanceID), "the jail must be removed")
			assert.NoFileExists(t,
				filepath.Join(launcher.JailRoot(req.InstanceID), "root", "run", "firecracker", req.InstanceID+".sock"),
				"no API socket may be left behind")
			assert.NoDirExists(t, filepath.Join(launcher.cgroupRoot, launcher.ParentCgroup, req.InstanceID),
				"no cgroup may be left behind")
			assert.False(t, launcher.UIDs.Leased(3000), "and the uid must be free for the next instance")
		})
	}
}
