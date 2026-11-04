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

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
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
	})
	require.NoError(t, err, "a jailed cold boot must start")
	t.Cleanup(func() { _ = src.Stop(context.Background()) })
	booted := time.Since(tBoot)

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
		})
		require.NoError(t, startErr, "a checkpoint taken in one jail must load in another")
		t.Cleanup(func() { _ = restored.Stop(context.Background()) })

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
