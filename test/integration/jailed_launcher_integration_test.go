//go:build integration

package integration

// A jailed round-trip on a real KVM host: boot a VM inside a jailer chroot through
// JailedLauncher, snapshot it, stop it, and restore the checkpoint into a jail owned
// by a *different* uid.
//
// What this test is for is the thing unit tests cannot say: that the chroot paths a
// checkpoint records load in a jail the checkpoint was not taken in, that the restored
// guest is alive rather than merely answered 204, and that a stop leaves no process,
// socket, cgroup or jail directory behind. The guest is reached through the console
// socket the launcher creates, so "alive" means a command ran in it and /proc/uptime
// continued across the restore.
//
// The guest has to carry the agent, because since SBX-024 a launch is ready only once
// that agent answers `hello` (pkg/snapshot/readiness.go). This test installs the
// node's own agent into its private copy of the image, so the image it is handed only
// has to boot a systemd guest; a disk without an agent is what the readiness test
// below uses as its negative case.
//
// Prerequisites: Linux, root, /dev/kvm, firecracker and jailer in PATH, and a bootable
// kernel and rootfs. Point the last two at anything that boots to a root shell on
// ttyS0:
//
//	SWARMCRACKER_TEST_KERNEL=/var/tmp/sbx000-rerun/assets/vmlinux \
//	SWARMCRACKER_TEST_ROOTFS=/var/tmp/sbx000-rerun/assets/rootfs.ext4 \
//	go test -tags=integration -run JailedLauncher -v ./test/integration/
//
// SWARMCRACKER_TEST_SCRATCH moves the working directory (default /var/tmp, because the
// memory image is 512 MiB). SWARMCRACKER_TEST_KEEP=1 keeps it after a failure, which is
// where the jail's logs and Firecracker's own log are worth reading.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// jailedTestUIDBase is where this test takes jail uids from. It sits in the range the
// RFC reserves for sandboxes, so it cannot collide with a real user account on a node
// that follows the same convention.
const jailedTestUIDBase = 200100

// TestIntegration_JailedLauncherBootSnapshotRestoreAcrossUIDs walks the whole path a
// sandbox template will take: a cold boot inside the fence, a chroot-relative
// checkpoint, and a restore into a fresh jail under another uid.
func TestIntegration_JailedLauncherBootSnapshotRestoreAcrossUIDs(t *testing.T) {
	requireJailedHost(t)

	ctx := context.Background()
	scratch := jailedScratch(t)
	kernel := testAsset(t, "SWARMCRACKER_TEST_KERNEL", "/usr/share/firecracker/vmlinux")
	rootfs := testAsset(t, "SWARMCRACKER_TEST_ROOTFS",
		"/var/lib/firecracker/rootfs/rootfs.ext4", "/tmp/firecracker-rootfs.ext4")

	firecrackerBin := lookPath(t, "firecracker")
	jailerBin := lookPath(t, "jailer")

	uids := newTestUIDAllocator(t)

	// The source VM boots the caller's own copy of the disk: the guest writes to the
	// copy inside its jail, never to the file this test was handed.
	disk := filepath.Join(scratch, "rootfs.ext4")
	require.NoError(t, copySparse(rootfs, disk))
	installGuestAgent(t, disk)
	diskBefore, err := os.Stat(disk)
	require.NoError(t, err)

	launcher := snapshot.JailedLauncher{
		ChrootBaseDir:    filepath.Join(scratch, "jails"),
		FirecrackerPath:  firecrackerBin,
		JailerPath:       jailerBin,
		ParentCgroup:     "swarmcracker-test",
		MemoryMaxBytes:   1024 << 20, // the guest's 512 MiB plus the VMM's own footprint
		PidsMax:          64,
		LogDir:           filepath.Join(scratch, "logs"),
		ConsoleSocketDir: filepath.Join(scratch, "console"),
		UIDs:             uids,
	}
	srcID := "src"

	tBoot := time.Now()
	src, err := launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: srcID,
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     disk,
		VCPUs:      2,
		MemoryMB:   512,
		VsockPath:  jailedAgentMuxer,
	})
	require.NoError(t, err, "a jailed cold boot must start")
	t.Cleanup(func() { _ = src.Stop(context.Background()) })
	booted := time.Since(tBoot)

	// The launch is over only because the guest's agent answered: the hello is the
	// evidence, and the muxer is the host path the launcher dialled to get it.
	assert.Equal(t, agent.Version, src.Hello.Version, "the launch returns the guest's own handshake")
	assert.NotEmpty(t, src.Hello.BootID, "and the boot identity that comes with it")
	assert.Equal(t, filepath.Join(launcher.JailRoot(srcID), "root", "run", "firecracker", "agent.vsock"), src.VsockPath,
		"the reported muxer is the request's path resolved inside the jail")

	srcGuest := attachGuest(t, src, launcher, srcID)
	srcGuest.awaitPrompt()
	assert.Equal(t, "Running", apiState(t, src.APISocket), "a cold boot leaves the guest running")

	kernelRelease := srcGuest.run("uname -r")
	beforeUptime := srcGuest.uptime(t)
	t.Logf("boot: %s to a console socket, guest kernel %q, uptime %.2fs, disk copied by %s",
		booted.Round(time.Millisecond), kernelRelease, beforeUptime, src.DiskCopy)

	srcUID := jailUID(t, launcher, srcID)
	require.GreaterOrEqual(t, srcUID, jailedTestUIDBase, "the source VM runs as a uid from the configured range")
	require.Less(t, srcUID, jailedTestUIDBase+7, "and one with room for the restore's uid above it")
	assertJailCgroup(t, launcher, srcID)

	// The snapshot goes through the manager rather than through the API by hand,
	// because what has to hold is CreateSnapshot's behaviour (SBX-022): it names the
	// jail's paths, so the checkpoint records names another jail can honour, and it
	// copies the disk the guest has been writing — the chroot's — rather than the
	// pre-boot file this test staged from.
	mgr, err := snapshot.NewManager(snapshot.SnapshotConfig{SnapshotDir: filepath.Join(scratch, "snapshots")})
	require.NoError(t, err)

	liveDisk := filepath.Join(launcher.JailRoot(srcID), "root", "drives", "rootfs.ext4")
	liveBefore, err := os.Stat(liveDisk)
	require.NoError(t, err)

	tSnap := time.Now()
	info, err := mgr.CreateSnapshot(ctx, srcID, src.APISocket, snapshot.CreateOptions{
		ServiceID:  "jailed",
		NodeID:     "the-root",
		VCPUCount:  2,
		MemoryMB:   512,
		RootfsPath: disk,
		JailRoot:   launcher.JailRoot(srcID),
	})
	require.NoError(t, err, "a jailed VM's snapshot must be creatable through the manager")
	snapTook := time.Since(tSnap)

	assert.True(t, info.Jailed, "the snapshot records that its paths belong to a jail")
	assert.Equal(t, "/drives/rootfs.ext4", info.RootfsPath,
		"the checkpoint records the chroot path, which is what makes it portable")

	stateInfo, err := os.Stat(info.StatePath)
	require.NoError(t, err)
	memInfo, err := os.Stat(info.MemoryPath)
	require.NoError(t, err)
	diskCopy, err := os.Stat(info.DiskPath)
	require.NoError(t, err)
	require.Equal(t, int64(512<<20), memInfo.Size(), "a 512 MiB guest writes a 512 MiB memory image")
	assert.Equal(t, liveBefore.Size(), diskCopy.Size(), "the snapshot owns a copy of the live disk")
	assert.False(t, os.SameFile(liveBefore, diskCopy), "the snapshot's disk is its own file, not the jail's")
	t.Logf("snapshot: %s, vm.state %d B, vm.mem %d B, disk %d B",
		snapTook.Round(time.Millisecond), stateInfo.Size(), memInfo.Size(), diskCopy.Size())

	// The same checkpoint offered to the direct restore is refused, and refused before
	// anything starts: a VMM outside a chroot cannot open the disk the checkpoint
	// names. This is the fail-closed half of the pair the jailed restores below are
	// the other half of.
	directSocket := filepath.Join(scratch, "direct.sock")
	directErr := mgr.RestoreFromSnapshot(ctx, info, directSocket)
	require.Error(t, directErr, "a jailed checkpoint must not restore outside a jail")
	assert.ErrorIs(t, directErr, snapshot.ErrJailedSnapshot, "the refusal is typed")
	assert.Contains(t, directErr.Error(), "jailed launcher", "and it names the way out")
	assert.NoFileExists(t, directSocket, "nothing may be started for a refused restore")
	t.Logf("direct restore of the jailed checkpoint refused with: %v", directErr)

	// Staging is a copy, so the guest wrote to the disk inside its jail and not to the
	// file this test was handed.
	diskAfter, err := os.Stat(disk)
	require.NoError(t, err)
	assert.True(t, diskBefore.ModTime().Equal(diskAfter.ModTime()),
		"the guest must not write the caller's disk: the jail runs on its own copy")

	srcPID := jailPID(t, launcher, srcID)
	tStop := time.Now()
	require.NoError(t, src.Stop(ctx))
	stopped := time.Since(tStop)
	assertJailGone(t, launcher, srcID, srcPID, src.APISocket)
	assertParentCgroupGone(t, launcher)
	assert.False(t, uids.Leased(srcUID), "a stopped instance's uid must be free again")
	t.Logf("stop: %s, source jail, socket and cgroup removed", stopped.Round(time.Millisecond))

	// Two restores, each as a different uid, both from the snapshot's own disk copy:
	// one restore proves the checkpoint loads outside the jail it was taken in, two
	// prove each instance ends up with a backing file of its own (SBX-023). Both run
	// at once, because an inode number reused after a teardown is the same number and
	// not the same file: the two disks can only be compared while both exist.
	restores := []struct {
		id  string
		uid int
	}{{id: "rest-a", uid: srcUID + 1}, {id: "rest-b", uid: srcUID + 2}}

	type restoredInstance struct {
		id       string
		uid      int
		launched snapshot.Launched
		disk     os.FileInfo
		took     time.Duration
	}

	instances := make([]restoredInstance, 0, len(restores))
	for _, run := range restores {
		tRestore := time.Now()
		restored, startErr := launcher.Start(ctx, snapshot.LaunchRequest{
			InstanceID: run.id,
			Mode:       snapshot.ModeRestore,
			Kernel:     kernel,
			Rootfs:     info.DiskPath,
			State:      info.StatePath,
			Mem:        info.MemoryPath,
			JailUID:    run.uid,
			VsockPath:  jailedAgentMuxer,
		})
		require.NoError(t, startErr, "a checkpoint taken in one jail must load in another")
		t.Cleanup(func() { _ = restored.Stop(context.Background()) })

		assert.Equal(t, agent.Version, restored.Hello.Version,
			"a restore is ready because the agent it resumed answered inside the new jail")

		assert.Equal(t, "/snapshot/vm.state", restored.StatePath, "the caller is told the path /snapshot/load was given")
		assert.Equal(t, "/snapshot/vm.mem", restored.MemPath)
		assert.Equal(t, "Running", apiState(t, restored.APISocket), "a resumed guest is running, not merely loaded")

		assert.NotEqual(t, snapshot.CopyNone, restored.DiskCopy,
			"the launcher reports how the instance got its own copy of the disk")

		restoredDisk, statErr := os.Stat(filepath.Join(launcher.JailRoot(run.id), "root", "drives", "rootfs.ext4"))
		require.NoError(t, statErr)
		assert.False(t, os.SameFile(restoredDisk, diskCopy),
			"a restored instance must write its own disk, never the snapshot's copy")

		instances = append(instances, restoredInstance{
			id: run.id, uid: run.uid, launched: restored, disk: restoredDisk, took: time.Since(tRestore),
		})
	}

	require.Len(t, instances, 2)
	assert.False(t, os.SameFile(instances[0].disk, instances[1].disk),
		"two instances must never share one backing file, even though both came from one snapshot")
	assert.Equal(t, instances[0].disk.Size(), instances[1].disk.Size())

	for _, instance := range instances {
		restGuest := attachGuest(t, instance.launched, launcher, instance.id)
		token := fmt.Sprintf("JAILED-RESTORE-%s-%d", instance.id, time.Now().UnixNano())
		assert.Contains(t, restGuest.run("echo "+token), token, "a command must run in the restored guest")
		afterUptime := restGuest.uptime(t)
		assert.NotContains(t, restGuest.transcript(), "Linux version",
			"the restored console must carry no kernel boot banner: the guest resumed rather than booted")
		assert.GreaterOrEqual(t, afterUptime, beforeUptime,
			"guest uptime must continue across a restore (it was %.2fs before the snapshot)", beforeUptime)

		restoredUID := jailUID(t, launcher, instance.id)
		assert.Equal(t, instance.uid, restoredUID, "the restored VMM runs as the uid it was asked for")
		assert.NotEqual(t, srcUID, restoredUID, "the source and a restored instance never share a uid")

		t.Logf("restore %s: %s as uid %d, disk %d B inode %d copied by %s; uptime %.2fs -> %.2fs",
			instance.id, instance.took.Round(time.Millisecond), instance.uid,
			instance.disk.Size(), inodeOf(instance.disk), instance.launched.DiskCopy,
			beforeUptime, afterUptime)
	}

	// The checkpoint they restored from still holds what it recorded: the same bytes,
	// under the same name, after two instances have been built from it.
	diskCopyAfter, err := os.Stat(info.DiskPath)
	require.NoError(t, err)
	assert.True(t, diskCopy.ModTime().Equal(diskCopyAfter.ModTime()),
		"restoring a snapshot must not write the disk copy it restores from")
	assert.Equal(t, "/drives/rootfs.ext4", info.RootfsPath, "the recorded path never changes")
	assert.True(t, os.SameFile(diskCopy, diskCopyAfter), "and neither must it be replaced")

	for _, instance := range instances {
		restPID := jailPID(t, launcher, instance.id)
		tRestStop := time.Now()
		require.NoError(t, instance.launched.Stop(ctx))
		assertJailGone(t, launcher, instance.id, restPID, instance.launched.APISocket)
		assert.False(t, uids.Leased(instance.uid), "a restored instance's uid is free after its teardown")
		t.Logf("stop %s: %s, jail, socket and cgroup removed",
			instance.id, time.Since(tRestStop).Round(time.Millisecond))
	}
	assertParentCgroupGone(t, launcher)

	// And the range is reused in order: the next launch takes the lowest free uid.
	nextLease, err := uids.Allocate()
	require.NoError(t, err)
	assert.Equal(t, srcUID, nextLease.UID())
	nextLease.Release()
}

// guest is one VM's serial console, reached through the console socket the launcher
// created. Output is read from the console log the launcher mirrors, so a command's
// output survives the connection.
type guest struct {
	t       *testing.T
	conn    net.Conn
	logPath string
}

// attachGuest dials an instance's console socket and proves the guest prompt is
// reachable.
func attachGuest(t *testing.T, launched snapshot.Launched, launcher snapshot.JailedLauncher, instanceID string) *guest {
	t.Helper()

	socketPath := filepath.Join(launcher.ConsoleSocketDir, instanceID+".console.sock")
	require.FileExists(t, socketPath, "the launcher must create the console socket the caller attaches to")

	var conn net.Conn
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, err, "dial the guest console")

	g := &guest{t: t, conn: conn, logPath: launched.ConsolePath}
	t.Cleanup(func() { _ = conn.Close() })
	return g
}

// awaitPrompt waits for the guest's shell prompt, so the first command is typed into a
// shell rather than into a getty that has not started.
func (g *guest) awaitPrompt() {
	g.t.Helper()

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(g.transcript(), "# ") || strings.Contains(g.transcript(), "$ ") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	g.t.Fatalf("the guest never reached a shell prompt; console tail:\n%s", tail(g.transcript(), 2000))
}

// transcript is everything the guest has written so far.
func (g *guest) transcript() string {
	body, err := os.ReadFile(g.logPath)
	if err != nil {
		return ""
	}
	return string(body)
}

// run types a command into the guest and returns its output.
//
// The tty echoes what is typed, so a closing marker would match the echoed command
// line rather than the command's output. The closing marker is therefore sent as
// __EN""D_x__, which the shell concatenates back to __END_x__ on output while the echo
// of the typed line never contains the literal marker.
func (g *guest) run(command string) string {
	g.t.Helper()

	marker := fmt.Sprintf("%d", time.Now().UnixNano())
	begin := "__BEGIN_" + marker + "__"
	end := "__END_" + marker + "__"
	typedEnd := end[:4] + `""` + end[4:]

	_, err := g.conn.Write([]byte(fmt.Sprintf("echo %s; %s; echo %s\n", begin, command, typedEnd)))
	require.NoError(g.t, err, "write to the guest console")

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		text := g.transcript()
		if strings.Contains(text, end) {
			start := strings.LastIndex(text, begin)
			stop := strings.LastIndex(text, end)
			require.Less(g.t, start, stop, "the command's output must follow its begin marker")
			return cleanConsole(text[start+len(begin) : stop])
		}
		time.Sleep(20 * time.Millisecond)
	}
	g.t.Fatalf("the guest never answered %q; console tail:\n%s", command, tail(g.transcript(), 2000))
	return ""
}

// uptime reads the guest's uptime in seconds.
func (g *guest) uptime(t *testing.T) float64 {
	t.Helper()

	out := g.run(`cut -d' ' -f1 /proc/uptime`)
	seconds, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	require.NoError(t, err, "guest uptime was %q", out)
	return seconds
}

// requireJailedHost skips when this machine cannot run a jailed VM at all.
func requireJailedHost(t *testing.T) {
	t.Helper()

	if runtime.GOOS != "linux" {
		t.Skip("the jailer is Linux-only")
	}
	if os.Geteuid() != 0 {
		t.Skip("jailed VMs need root (chown, chroot, /dev/net/tun)")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("KVM not available")
	}
	for _, binary := range []string{"firecracker", "jailer"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s not found in PATH", binary)
		}
	}
}

// jailedScratch makes the directory this test owns. It is not t.TempDir(): the
// checkpoint images are hundreds of megabytes and /tmp is usually a tmpfs, so the
// scratch goes on disk where /var/tmp lives.
func jailedScratch(t *testing.T) string {
	t.Helper()

	base := os.Getenv("SWARMCRACKER_TEST_SCRATCH")
	if base == "" {
		base = "/var/tmp"
	}
	// The name is short on purpose: the instance API socket lives inside the chroot,
	// so its host path is <scratch>/jails/firecracker/<id>/root/run/firecracker/<id>.sock
	// and must fit in the 108 bytes a unix socket address holds.
	dir, err := os.MkdirTemp(base, "sbxj-")
	if err != nil {
		dir = t.TempDir()
	}
	if os.Getenv("SWARMCRACKER_TEST_KEEP") == "" {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	} else {
		t.Logf("scratch kept at %s", dir)
	}
	return dir
}

// testAsset finds a host file the test needs, from the environment first.
func testAsset(t *testing.T, env string, defaults ...string) string {
	t.Helper()

	if path := os.Getenv(env); path != "" {
		require.FileExists(t, path, "%s names a missing file", env)
		return path
	}
	for _, path := range defaults {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	t.Skipf("no asset for %s: set it to a bootable image", env)
	return ""
}

func lookPath(t *testing.T, name string) string {
	t.Helper()

	path, err := exec.LookPath(name)
	require.NoError(t, err)
	return path
}

func newTestUIDAllocator(t *testing.T) *jailer.UIDAllocator {
	t.Helper()

	uids, err := jailer.NewUIDAllocator(jailedTestUIDBase, 8)
	require.NoError(t, err)
	return uids
}

// copySparse copies a disk image, keeping its holes.
func copySparse(src, dst string) error {
	out, err := exec.Command("cp", "--sparse=always", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp %s: %w: %s", src, err, out)
	}
	return nil
}

// inodeOf reports the inode a file has on its filesystem, so a log line can name the
// file two instances were proved not to share.
func inodeOf(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Ino
	}
	return 0
}

// jailPID reads the pid of the VMM one instance's jail holds.
func jailPID(t *testing.T, launcher snapshot.JailedLauncher, instanceID string) int {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(launcher.JailRoot(instanceID), "root", "firecracker.pid"))
	require.NoError(t, err, "the jailer writes the VMM's pid inside the jail")
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	require.NoError(t, err)
	return pid
}

// jailUID reads the uid the VMM of one instance actually runs as.
func jailUID(t *testing.T, launcher snapshot.JailedLauncher, instanceID string) int {
	t.Helper()

	return echoUID(t, jailPID(t, launcher, instanceID))
}

// echoUID reads a process's real uid from procfs. It is the process's own answer, not
// the uid a launcher was asked for.
func echoUID(t *testing.T, pid int) int {
	t.Helper()

	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	require.NoError(t, err)

	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
		require.NotEmpty(t, fields)
		uid, err := strconv.Atoi(fields[0])
		require.NoError(t, err)
		return uid
	}
	t.Fatalf("no Uid line for pid %d", pid)
	return 0
}

// assertJailCgroup proves the cgroup limits reached the instance's own cgroup.
func assertJailCgroup(t *testing.T, launcher snapshot.JailedLauncher, instanceID string) {
	t.Helper()

	dir := filepath.Join("/sys/fs/cgroup", launcher.ParentCgroup, instanceID)
	body, err := os.ReadFile(filepath.Join(dir, "memory.max"))
	require.NoError(t, err, "the jailer must have created %s", dir)
	assert.Equal(t, strconv.FormatInt(launcher.MemoryMaxBytes, 10), strings.TrimSpace(string(body)))

	pids, err := os.ReadFile(filepath.Join(dir, "pids.max"))
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(launcher.PidsMax), strings.TrimSpace(string(pids)))
}

// assertJailGone proves a stop left nothing: no process, no socket, no cgroup and no
// jail directory.
func assertJailGone(t *testing.T, launcher snapshot.JailedLauncher, instanceID string, pid int, socket string) {
	t.Helper()

	assert.False(t, processRunning(pid), "the VMM must be gone after a stop")
	assert.NoFileExists(t, socket, "the instance socket must be removed")
	assert.NoFileExists(t, filepath.Join(launcher.ConsoleSocketDir, instanceID+".console.sock"))
	assert.NoDirExists(t, launcher.JailRoot(instanceID), "the jail directory must not outlive the instance")
	assert.NoDirExists(t, filepath.Join("/sys/fs/cgroup", launcher.ParentCgroup, instanceID),
		"the instance cgroup must be removed")
}

// assertParentCgroupGone proves the last teardown removed the cgroup the jailer
// creates the instances' own under. It is checked on its own because the parent is
// shared: it belongs to no instance and outlives every instance but the last.
func assertParentCgroupGone(t *testing.T, launcher snapshot.JailedLauncher) {
	t.Helper()

	assert.NoDirExists(t, filepath.Join("/sys/fs/cgroup", launcher.ParentCgroup),
		"the parent cgroup the jailer created must not be left behind once no instance is running")
}

// processRunning reports whether pid is a live (not zombie) process.
func processRunning(pid int) bool {
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(body[bytes.LastIndexByte(body, ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z"
}

// apiState reads the VMM's own idea of what its guest is doing.
func apiState(t *testing.T, socketPath string) string {
	t.Helper()

	status, body := fcAPI(t, socketPath, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, status, "GET / must describe the instance")

	var info struct {
		State string `json:"state"`
	}
	require.NoError(t, json.Unmarshal(body, &info))
	return info.State
}

// requireStatus makes one API call and pins its status.
func requireStatus(t *testing.T, socketPath, method, path string, payload interface{}, want int) {
	t.Helper()

	status, body := fcAPI(t, socketPath, method, path, payload)
	require.Equal(t, want, status, "%s %s answered %s", method, path, body)
}

// fcAPI performs one Firecracker API call over the instance's unix socket.
func fcAPI(t *testing.T, socketPath, method, path string, payload interface{}) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		reader = bytes.NewReader(body)
	}

	request, err := http.NewRequest(method, "http://localhost"+path, reader)
	require.NoError(t, err)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			// Firecracker caps how many API connections it accepts, and a pooled
			// connection it never reuses would count against that cap.
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
	}
	defer client.CloseIdleConnections()

	response, err := client.Do(request)
	require.NoError(t, err, "%s %s", method, path)
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, body
}

// cleanConsole removes the carriage returns and blank lines a tty adds.
func cleanConsole(raw string) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// tail is the last n bytes of s, for a failure message that fits.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// jailedAgentMuxer is where the guest agent answers inside an instance's jail. A cold
// boot configures its vsock device at this path, so a checkpoint of that instance
// records it and a restore of the checkpoint names the same string; the launcher
// resolves it to the host path <jail root>/root/run/firecracker/agent.vsock, which is
// what readiness dials.
const jailedAgentMuxer = "/run/firecracker/agent.vsock"

// TestIntegration_JailedLauncherReadiness holds the SBX-024 criterion to a real guest
// on a real KVM host, in both directions.
//
// The positive half is that a cold boot and a restore are ready because the guest's own
// agent answered, and that the answer is what the launcher reports — not the 204 the
// checkpoint load returned.
//
// The negative half is the one that matters: a guest whose agent never answers fails
// the launch with a typed error and leaves nothing behind, even though the jailer
// started, the disk staged, the checkpoint loaded and the guest resumed. Three guests
// produce it, because they are three different reasons an agent can be silent: an image
// with no agent at all, a guest whose agent requires a token this launch did not carry,
// and a checkpoint taken while the guest's agent was stopped — the last one proving the
// point exactly, since its checkpoint loads and its guest resumes.
func TestIntegration_JailedLauncherReadiness(t *testing.T) {
	requireJailedHost(t)

	ctx := context.Background()
	scratch := jailedScratch(t)
	kernel := testAsset(t, "SWARMCRACKER_TEST_KERNEL", "/usr/share/firecracker/vmlinux")
	rootfs := testAsset(t, "SWARMCRACKER_TEST_ROOTFS",
		"/var/lib/firecracker/rootfs/rootfs.ext4", "/tmp/firecracker-rootfs.ext4")

	firecrackerBin := lookPath(t, "firecracker")
	jailerBin := lookPath(t, "jailer")
	uids := newTestUIDAllocator(t)

	launcher := snapshot.JailedLauncher{
		ChrootBaseDir:    filepath.Join(scratch, "jails"),
		FirecrackerPath:  firecrackerBin,
		JailerPath:       jailerBin,
		ParentCgroup:     "swarmcracker-test",
		MemoryMaxBytes:   1024 << 20,
		PidsMax:          64,
		LogDir:           filepath.Join(scratch, "logs"),
		ConsoleSocketDir: filepath.Join(scratch, "console"),
		UIDs:             uids,
		ReadyTimeout:     60 * time.Second, // a cold boot of a full OS image is the slow one
	}
	// A launch that is going to fail spends its whole window waiting, so the negative
	// cases use a shorter one: long enough that a guest which is merely slow is not the
	// reason they fail, short enough to keep the test quick.
	impatient := launcher
	impatient.ReadyTimeout = 8 * time.Second

	// Three images from the one the runner provided: with the agent, with the agent and
	// a token to require, and without an agent at all.
	withAgent := filepath.Join(scratch, "agent.ext4")
	require.NoError(t, copySparse(rootfs, withAgent))
	installGuestAgent(t, withAgent)

	token := "7d4a1c9e2b6f8031a5c7e9b1d3f5a7c9"
	withToken := filepath.Join(scratch, "token.ext4")
	require.NoError(t, copySparse(rootfs, withToken))
	installGuestAgent(t, withToken)
	editGuestImage(t, withToken, func(root string) {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/swarmcracker"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, agent.TokenPath), []byte(token+"\n"), 0o400))
	})

	bare := filepath.Join(scratch, "bare.ext4")
	require.NoError(t, copySparse(rootfs, bare))

	// Positive: the agent answers, and the launch reports what it answered.
	readyID := "ready"
	tReady := time.Now()
	ready, err := launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: readyID,
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     withAgent,
		VCPUs:      2,
		MemoryMB:   512,
		VsockPath:  jailedAgentMuxer,
	})
	require.NoError(t, err, "a guest with an agent must launch")
	readyTook := time.Since(tReady)
	readyGuest := attachGuest(t, ready, launcher, readyID)
	readyGuest.awaitPrompt()
	t.Logf("cold boot ready in %s: guest agent %s protocol %s boot %s, muxer %s",
		readyTook.Round(time.Millisecond), ready.Hello.AgentVersion, ready.Hello.Version, ready.Hello.BootID, ready.VsockPath)
	assert.Equal(t, agent.Version, ready.Hello.Version)
	assert.NotEmpty(t, ready.Hello.BootID)
	assert.Equal(t, filepath.Join(launcher.JailRoot(readyID), "root", "run", "firecracker", "agent.vsock"), ready.VsockPath)
	assert.Contains(t, readyGuest.run("echo ready-guest"), "ready-guest", "and the guest behind the hello runs")
	readyPID := jailPID(t, launcher, readyID)
	require.NoError(t, ready.Stop(ctx))
	assertJailGone(t, launcher, readyID, readyPID, ready.APISocket)
	assert.False(t, uids.Leased(jailedTestUIDBase), "a stopped instance's uid is free again")

	// Positive, with a token: the same guest, this time requiring one. The launch is
	// handed the token and gets through the handshake; the same image without it is the
	// next case.
	tokenID := "token-ok"
	tokenOK, err := launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: tokenID,
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     withToken,
		VCPUs:      2,
		MemoryMB:   512,
		VsockPath:  jailedAgentMuxer,
		AgentToken: token,
	})
	require.NoError(t, err, "a guest that requires a token must launch when the launch carries it")
	t.Cleanup(func() { _ = tokenOK.Stop(context.Background()) })
	assert.Equal(t, agent.Version, tokenOK.Hello.Version, "the authenticated handshake is still the guest's own")
	require.NoError(t, tokenOK.Stop(ctx))
	assert.False(t, uids.Leased(jailedTestUIDBase))

	// Negative 1: the guest has no agent. Every VMM-side step succeeds; the launch fails
	// because nothing answers, and nothing is left behind.
	before := firecrackerPIDs()
	_, err = impatient.Start(ctx, snapshot.LaunchRequest{
		InstanceID: "no-agent",
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     bare,
		VCPUs:      2,
		MemoryMB:   512,
		VsockPath:  jailedAgentMuxer,
	})
	require.Error(t, err, "a guest with no agent must not be reported as launched")
	assert.ErrorIs(t, err, snapshot.ErrAgentNotReady, "and the failure is typed")
	assertNoInstanceLeft(t, launcher, uids, "no-agent", before)
	t.Logf("a guest with no agent failed as: %v", err)

	// Negative 2: the guest requires a token and the launch carries a wrong one, so the
	// agent refuses the handshake. It is the same image as the positive token case,
	// which is what makes this the token's doing rather than the image's; the refusal is
	// typed, so a caller can tell a mis-provisioned token from a guest that is simply
	// not up.
	before = firecrackerPIDs()
	_, err = impatient.Start(ctx, snapshot.LaunchRequest{
		InstanceID: "token-wrong",
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     withToken,
		VCPUs:      2,
		MemoryMB:   512,
		VsockPath:  jailedAgentMuxer,
		AgentToken: "0000000000000000000000000000000000000000000000000000000000000000",
	})
	require.Error(t, err, "a guest that requires a token must not launch without the right one")
	assert.ErrorIs(t, err, snapshot.ErrAgentNotReady)
	assert.ErrorIs(t, err, agent.ErrTokenRejected, "and the guest's own refusal is reachable through it")
	assertNoInstanceLeft(t, launcher, uids, "token-wrong", before)
	t.Logf("a token-requiring guest with the wrong token failed as: %v", err)

	// Negative 3: the same guest with no token at all. A guest that requires one closes
	// the connection without a frame (pkg/agent), so this is not distinguishable from a
	// guest with no agent by the error type — which is what the message says.
	before = firecrackerPIDs()
	_, err = impatient.Start(ctx, snapshot.LaunchRequest{
		InstanceID: "token-missing",
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     withToken,
		VCPUs:      2,
		MemoryMB:   512,
		VsockPath:  jailedAgentMuxer,
	})
	require.Error(t, err, "a guest that requires a token must not launch without one")
	assert.ErrorIs(t, err, snapshot.ErrAgentNotReady)
	assert.Contains(t, err.Error(), "token", "the failure names the token as the likely reason")
	assertNoInstanceLeft(t, launcher, uids, "token-missing", before)
	t.Logf("a token-requiring guest with no token failed as: %v", err)

	// Negative 4: the guest resumed, its agent is not there. The checkpoint is taken
	// from a guest whose agent was stopped, so loading it back is a load that returns
	// 204 and a guest that answers nothing: exactly the case the old criterion called
	// ready.
	stoppedID := "agent-stopped"
	stopped, err := launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: stoppedID,
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     withAgent,
		VCPUs:      2,
		MemoryMB:   512,
		VsockPath:  jailedAgentMuxer,
	})
	require.NoError(t, err)
	stoppedGuest := attachGuest(t, stopped, launcher, stoppedID)
	stoppedGuest.awaitPrompt()
	stoppedGuest.run("systemctl stop sc-agent")
	assert.NotEqual(t, "active", stoppedGuest.run("systemctl is-active sc-agent"),
		"the checkpoint must be taken of a guest whose agent is stopped")

	mgr, err := snapshot.NewManager(snapshot.SnapshotConfig{SnapshotDir: filepath.Join(scratch, "snapshots")})
	require.NoError(t, err)
	silent, err := mgr.CreateSnapshot(ctx, stoppedID, stopped.APISocket, snapshot.CreateOptions{
		ServiceID:  "readiness",
		NodeID:     "the-root",
		VCPUCount:  2,
		MemoryMB:   512,
		RootfsPath: withAgent,
		JailRoot:   launcher.JailRoot(stoppedID),
	})
	require.NoError(t, err, "a checkpoint of a guest without a running agent is still a checkpoint")
	require.NoError(t, stopped.Stop(ctx))

	before = firecrackerPIDs()
	_, err = impatient.Start(ctx, snapshot.LaunchRequest{
		InstanceID: "silent-restore",
		Mode:       snapshot.ModeRestore,
		Kernel:     kernel,
		Rootfs:     silent.DiskPath,
		State:      silent.StatePath,
		Mem:        silent.MemoryPath,
		VsockPath:  jailedAgentMuxer,
	})
	require.Error(t, err, "a checkpoint whose guest agent is stopped must not restore as running")
	assert.ErrorIs(t, err, snapshot.ErrAgentNotReady)
	assertNoInstanceLeft(t, launcher, uids, "silent-restore", before)
	t.Logf("a restored checkpoint whose agent was stopped failed as: %v", err)
}

// assertNoInstanceLeft proves a failed launch left nothing: no process started under
// the instance, no jail, no cgroup, and no uid still leased.
func assertNoInstanceLeft(t *testing.T, launcher snapshot.JailedLauncher, uids *jailer.UIDAllocator, instanceID string, before map[int]bool) {
	t.Helper()

	// The kill is asynchronous with respect to the call that issued it.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(newFirecrackerPIDs(before)) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.Empty(t, newFirecrackerPIDs(before), "no VMM of a failed launch may survive it")
	assert.NoDirExists(t, launcher.JailRoot(instanceID), "and no jail")
	assert.NoDirExists(t, filepath.Join("/sys/fs/cgroup", launcher.ParentCgroup, instanceID), "and no cgroup")
	assert.False(t, uids.Leased(jailedTestUIDBase), "and no leased uid")
}

// firecrackerPIDs is the set of VMM processes on the host right now. The host may be
// running other people's VMs, so a comparison against this is what answers "did this
// launch leave one behind", never an absolute count.
func firecrackerPIDs() map[int]bool {
	pids := map[int]bool{}
	out, err := exec.Command("pgrep", "-x", "firecracker").Output()
	if err != nil && len(out) == 0 {
		return pids
	}
	for _, field := range strings.Fields(string(out)) {
		if pid, convErr := strconv.Atoi(field); convErr == nil {
			pids[pid] = true
		}
	}
	return pids
}

// newFirecrackerPIDs are the VMM processes that were not running before.
func newFirecrackerPIDs(before map[int]bool) []int {
	var added []int
	for pid := range firecrackerPIDs() {
		if !before[pid] {
			added = append(added, pid)
		}
	}
	return added
}

// installGuestAgent gives a copy of the test image the node's own guest agent, enabled
// at boot under systemd. It is what a template build does for its image (pkg/golden
// owns the production installer); this test does it here because the readiness check
// needs a guest that answers, and the image the runner hands over only has to boot.
func installGuestAgent(t *testing.T, imagePath string) {
	t.Helper()

	editGuestImage(t, imagePath, func(root string) {
		if _, err := os.Stat(filepath.Join(root, "etc/systemd/system")); err != nil {
			t.Fatalf("the test image has no systemd at /etc/systemd/system: this test enables the agent as a systemd unit")
		}
		require.NoError(t, os.WriteFile(filepath.Join(root, "sbin/sc-agent"), image.AgentBinary(), 0o755))
		unit := "[Unit]\n" +
			"Description=SwarmCracker guest agent\n" +
			"After=local-fs.target\n" +
			"Wants=local-fs.target\n\n" +
			"[Service]\n" +
			"Type=simple\n" +
			"ExecStart=/sbin/sc-agent -port 52\n" +
			"Restart=always\n" +
			"RestartSec=1\n\n" +
			"[Install]\n" +
			"WantedBy=multi-user.target\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, "etc/systemd/system/sc-agent.service"), []byte(unit), 0o644))
		wants := filepath.Join(root, "etc/systemd/system/multi-user.target.wants")
		require.NoError(t, os.MkdirAll(wants, 0o755))
		_ = os.Remove(filepath.Join(wants, "sc-agent.service"))
		require.NoError(t, os.Symlink("/etc/systemd/system/sc-agent.service", filepath.Join(wants, "sc-agent.service")))
	})
}

// editGuestImage mounts a test image, runs edit with the root of the mounted
// filesystem, unmounts it, and returns.
//
// The unmount is part of the contract, not a tidy-up at the end of the test: a writer
// that puts bytes into the image *underneath* this mount — `agent.InjectToken`, which
// goes through debugfs — writes a state the mounted filesystem's own journal knows
// nothing about, and the guest that later boots the image replays that journal and
// undoes it. A caller that stamps an image has to see it flushed before the next
// writer touches it. The image is the test's own copy: a guest's first boot writes
// state into it, so nothing is ever mounted to write the runner's original.
func editGuestImage(t *testing.T, imagePath string, edit func(root string)) {
	t.Helper()

	mount, err := os.MkdirTemp("", "sc-guest-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(mount) })

	if out, err := exec.Command("mount", "-o", "loop", imagePath, mount).CombinedOutput(); err != nil {
		t.Fatalf("mount %s: %v: %s", imagePath, err, out)
	}
	t.Cleanup(func() {
		// A no-op on the normal path, where the unmount below already ran.
		if out, err := exec.Command("umount", mount).CombinedOutput(); err != nil {
			t.Logf("umount %s: %v: %s", mount, err, out)
		}
	})

	edit(mount)
	require.NoError(t, exec.Command("sync").Run())

	out, err := exec.Command("umount", mount).CombinedOutput()
	require.NoError(t, err, "umount %s: %s", mount, out)
}

// TestIntegration_JailedLauncherInstanceRecord is SBX-025 on a real host: the record a
// launch publishes, read back from the node's registry, and checked field by field
// against the host's own answers — /proc for the process and the uid it runs as, `ss`
// for the two sockets the record names, `ip`/`netns` for the namespace the VMM is in.
// A record is only worth writing if a reader can act on it, and acting on it means
// dialling the socket it names, so the comparison is the point.
//
// The fields this instance has no answer for are asserted empty rather than filled with
// a plausible value: a cold boot here configures no NIC, so there is no TAP device, no
// guest address and no MAC to record, and a guessed one would send an operator to an
// address nobody answers on.
func TestIntegration_JailedLauncherInstanceRecord(t *testing.T) {
	requireJailedHost(t)

	ctx := context.Background()
	scratch := jailedScratch(t)
	kernel := testAsset(t, "SWARMCRACKER_TEST_KERNEL", "/usr/share/firecracker/vmlinux")
	rootfs := testAsset(t, "SWARMCRACKER_TEST_ROOTFS",
		"/var/lib/firecracker/rootfs/rootfs.ext4", "/tmp/firecracker-rootfs.ext4")

	firecrackerBin := lookPath(t, "firecracker")
	jailerBin := lookPath(t, "jailer")
	uids := newTestUIDAllocator(t)

	stateDir := filepath.Join(scratch, "state")
	store, err := vminstance.NewStore(vminstance.Config{Dir: vminstance.Dir(stateDir)})
	require.NoError(t, err)

	disk := filepath.Join(scratch, "rootfs.ext4")
	require.NoError(t, copySparse(rootfs, disk))
	installGuestAgent(t, disk)

	launcher := snapshot.JailedLauncher{
		ChrootBaseDir:    filepath.Join(scratch, "jails"),
		FirecrackerPath:  firecrackerBin,
		JailerPath:       jailerBin,
		ParentCgroup:     "swarmcracker-test",
		MemoryMaxBytes:   1024 << 20,
		PidsMax:          64,
		LogDir:           filepath.Join(scratch, "logs"),
		ConsoleSocketDir: filepath.Join(scratch, "console"),
		UIDs:             uids,
		Registry:         store,
	}

	// The identity the caller knows when it asks for the instance. The launcher fills in
	// the rest and overwrites what the caller cannot know.
	identity := vminstance.Instance{
		Kind:    vminstance.KindSandbox,
		OwnerID: "user-l2",
		NodeID:  "the-root",
	}

	// The namespace is the one host-side fact the launcher itself puts the VMM into, so
	// it is the one the record has to agree with /proc about.
	srcNetNS := "sc-l2-src"
	addNetNS(t, srcNetNS)

	srcID := "rec-src"
	src, err := launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: srcID,
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     disk,
		VCPUs:      2,
		MemoryMB:   512,
		VsockPath:  jailedAgentMuxer,
		NetNS:      srcNetNS,
		Record:     identity,
	})
	require.NoError(t, err, "a cold boot with a registry must start")
	t.Cleanup(func() { _ = src.Stop(context.Background()) })
	assertRecordMatchesTheHost(t, store, launcher, src, srcID, srcNetNS)

	mgr, err := snapshot.NewManager(snapshot.SnapshotConfig{SnapshotDir: filepath.Join(scratch, "snapshots")})
	require.NoError(t, err)
	info, err := mgr.CreateSnapshot(ctx, srcID, src.APISocket, snapshot.CreateOptions{
		ServiceID:  "record",
		NodeID:     "the-root",
		VCPUCount:  2,
		MemoryMB:   512,
		RootfsPath: disk,
		JailRoot:   launcher.JailRoot(srcID),
	})
	require.NoError(t, err)
	require.NoError(t, src.Stop(ctx))

	// The record goes with the instance it describes, and so does the namespace it
	// named: a record is the handle to the resources a stop frees, so a stop that
	// finished deletes it and a stale record is never left for a reader to trip over.
	assertNoRecord(t, store, stateDir, srcID)
	assert.NoDirExists(t, filepath.Join("/var/run/netns", srcNetNS),
		"the record named the namespace, so the teardown is what removes it")

	// A restored instance is a second instance: its own jail, its own uid, its own
	// namespace, and its own record — which is what a reader resolves to what it is
	// actually running.
	restNetNS := "sc-l2-rest"
	addNetNS(t, restNetNS)
	restID := "rec-rest"
	restored, err := launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: restID,
		Mode:       snapshot.ModeRestore,
		Kernel:     kernel,
		Rootfs:     info.DiskPath,
		State:      info.StatePath,
		Mem:        info.MemoryPath,
		VsockPath:  jailedAgentMuxer,
		NetNS:      restNetNS,
		Record:     identity,
	})
	require.NoError(t, err, "a restore with a registry must start")
	assertRecordMatchesTheHost(t, store, launcher, restored, restID, restNetNS)

	require.NoError(t, restored.Stop(ctx))
	assertNoRecord(t, store, stateDir, restID)
	assert.NoDirExists(t, filepath.Join("/var/run/netns", restNetNS))
}

// assertRecordMatchesTheHost reads one instance's record back from the registry and
// holds every field the launcher claims to the host's own answer for it.
func assertRecordMatchesTheHost(t *testing.T, store *vminstance.Store, launcher snapshot.JailedLauncher, launched snapshot.Launched, instanceID, netns string) {
	t.Helper()

	record, found, err := store.Get(instanceID)
	require.NoError(t, err)
	require.True(t, found, "a running instance must have a record: %s", instanceID)
	require.True(t, record.Ready, "the guest answered, so the record says the instance may be used")

	// /proc: the record names the VMM, and the VMM is the process the jailer wrote into
	// the jail — not the script, not the launcher.
	pid := jailPID(t, launcher, instanceID)
	assert.Equal(t, pid, record.PID, "the record names the process whose death means the instance is gone")
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	require.NoError(t, err)
	assert.Equal(t, "firecracker", strings.TrimSpace(string(comm)), "and that process is the VMM itself")

	// /proc again: the uid the VMM actually runs as, which is the uid the record must
	// hand to the next teardown so it never frees a live instance's resources.
	assert.NotZero(t, record.JailUID, "a jailed instance runs as a uid of its own")
	assert.Equal(t, echoUID(t, pid), record.JailUID)

	// The two sockets, from both sides. On the host, the record names a path a reader
	// dials, and that path is a socket. Inside the instance's own namespace and chroot,
	// `ss -xl` reports the same socket at the path the VMM was configured with — the two
	// differ by the jail root, which is the launcher's own mapping — and it has to be
	// asked there, because a unix socket belongs to the netns that created it.
	instanceRoot := filepath.Join(launcher.JailRoot(instanceID), "root")
	listening := unixSocketsInUse(t, netns)
	for _, hostPath := range []string{record.APISocket, record.VsockPath} {
		info, err := os.Stat(hostPath)
		require.NoError(t, err, "the record names a socket a reader will dial: %s", hostPath)
		assert.Equal(t, os.ModeSocket, info.Mode()&os.ModeSocket, "%s must be a unix socket", hostPath)

		inside, err := filepath.Rel(instanceRoot, hostPath)
		require.NoError(t, err, "the record's paths live inside the instance's jail")
		assert.Contains(t, listening, "/"+inside,
			"the socket the host reaches at %s is the one the instance has at /%s", hostPath, inside)
	}
	assert.Equal(t, launched.VsockPath, record.VsockPath)
	assert.Equal(t, launched.APISocket, record.APISocket)

	// The console: the file the record names is the one the launcher created.
	assert.Equal(t, launched.ConsolePath, record.Console)
	assert.FileExists(t, record.Console)

	// /proc and ip: the namespace the VMM is in is the one the record names. The two are
	// compared by their namespace inode: /var/run/netns/<name> is a bind mount of the
	// namespace `ip netns add` created, not a symlink, so the number is what both sides
	// can be asked for.
	namedNS, err := os.Stat(filepath.Join("/var/run/netns", netns))
	require.NoError(t, err, "the test's namespace must exist")
	fromProc, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", pid))
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("net:[%d]", inodeOf(namedNS)), fromProc,
		"the VMM runs in the namespace the record names")
	assert.Equal(t, filepath.Join("/var/run/netns", netns), record.NetNS)

	// What this instance has no answer for: it boots no NIC, so the record leaves the
	// network identity empty rather than guessing one.
	assert.Empty(t, record.TAP)
	assert.Empty(t, record.GuestIP)
	assert.Empty(t, record.MAC)

	// And the caller's own half of the identity is recorded as given.
	assert.Equal(t, vminstance.KindSandbox, record.Kind)
	assert.Equal(t, "user-l2", record.OwnerID)
	assert.Equal(t, "the-root", record.NodeID)
	assert.False(t, record.Booted.IsZero())

	t.Logf("record %s: pid %d (comm %s, uid %d) api %s vsock %s netns %s console %s ready %v",
		record.ID, record.PID, strings.TrimSpace(string(comm)), record.JailUID,
		record.APISocket, record.VsockPath, record.NetNS, record.Console, record.Ready)
}

// assertNoRecord proves the registry has nothing left for an instance: neither through
// a lookup, nor as a file a later reader could find.
func assertNoRecord(t *testing.T, store *vminstance.Store, stateDir, instanceID string) {
	t.Helper()

	_, found, err := store.Get(instanceID)
	require.NoError(t, err)
	assert.False(t, found, "a stopped instance must not resolve")
	_, found, err = store.Peek(instanceID)
	require.NoError(t, err)
	assert.False(t, found, "and must not be left behind for a teardown to trip over")
	assert.NoFileExists(t, filepath.Join(vminstance.Dir(stateDir), instanceID+".json"))
}

// unixSocketsInUse is what `ss -xl` reports inside one instance's network namespace:
// every listening unix socket the kernel has there, which is the kernel's own answer
// about the sockets a record names. The namespace matters because a unix socket belongs
// to the netns that created it, so the host's own `ss` cannot see the instance's.
func unixSocketsInUse(t *testing.T, netns string) string {
	t.Helper()

	argv := []string{"ss", "-xl"}
	if netns != "" {
		argv = append([]string{"ip", "netns", "exec", netns}, argv...)
	}
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	require.NoError(t, err, "%s", strings.Join(argv, " "))
	return string(out)
}

// addNetNS creates a network namespace for one instance to run in, and removes it
// after the test unless a teardown already did.
func addNetNS(t *testing.T, name string) {
	t.Helper()

	if out, err := exec.Command("ip", "netns", "add", name).CombinedOutput(); err != nil {
		t.Fatalf("ip netns add %s: %v: %s", name, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", name).Run() })
}
