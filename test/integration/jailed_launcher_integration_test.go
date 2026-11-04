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
	t.Logf("boot: %s to a console socket, guest kernel %q, uptime %.2fs", booted.Round(time.Millisecond), kernelRelease, beforeUptime)

	srcUID := jailUID(t, launcher, srcID)
	require.GreaterOrEqual(t, srcUID, jailedTestUIDBase, "the source VM runs as a uid from the configured range")
	require.Less(t, srcUID, jailedTestUIDBase+7, "and one with room for the restore's uid above it")
	assertJailCgroup(t, launcher, srcID)

	// Snapshot through the API, naming the chroot paths: this is what a jail-aware
	// CreateSnapshot will do for real (SBX-022), and the point here is that the
	// checkpoint it writes records paths that another jail can honour.
	tSnap := time.Now()
	requireStatus(t, src.APISocket, http.MethodPatch, "/vm", map[string]string{"state": "Paused"}, http.StatusNoContent)
	requireStatus(t, src.APISocket, http.MethodPut, "/snapshot/create", map[string]string{
		"snapshot_type": "Full",
		"snapshot_path": "/snapshot/vm.state",
		"mem_file_path": "/snapshot/vm.mem",
	}, http.StatusNoContent)
	snapTook := time.Since(tSnap)

	srcJail := filepath.Join(launcher.JailRoot(srcID), "root")
	statePath := filepath.Join(srcJail, "snapshot", "vm.state")
	memPath := filepath.Join(srcJail, "snapshot", "vm.mem")
	liveDisk := filepath.Join(srcJail, "drives", "rootfs.ext4")
	stateInfo, err := os.Stat(statePath)
	require.NoError(t, err)
	memInfo, err := os.Stat(memPath)
	require.NoError(t, err)
	require.Equal(t, int64(512<<20), memInfo.Size(), "a 512 MiB guest writes a 512 MiB memory image")
	t.Logf("snapshot: %s, vm.state %d B, vm.mem %d B", snapTook.Round(time.Millisecond), stateInfo.Size(), memInfo.Size())

	// Keep the checkpoint and the live disk after the jail is removed. Hard links cost
	// nothing and survive the teardown; a different filesystem falls back to a copy.
	savedState := hardlink(t, statePath, filepath.Join(scratch, "vm.state"))
	savedMem := hardlink(t, memPath, filepath.Join(scratch, "vm.mem"))
	savedDisk := hardlink(t, liveDisk, filepath.Join(scratch, "vm.rootfs.ext4"))

	// Staging is a copy, so the guest wrote to the disk inside its jail and not to the
	// file this test booted from. That is exactly why a jail-aware CreateSnapshot has
	// to read the chroot's disk (SBX-022) rather than the path the caller passed.
	diskAfter, err := os.Stat(disk)
	require.NoError(t, err)
	assert.True(t, diskBefore.ModTime().Equal(diskAfter.ModTime()),
		"the guest must not write the caller's disk: the jail runs on its own copy")

	srcPID := jailPID(t, launcher, srcID)
	tStop := time.Now()
	require.NoError(t, src.Stop(ctx))
	stopped := time.Since(tStop)
	assertJailGone(t, launcher, srcID, srcPID, src.APISocket)
	assert.False(t, uids.Leased(srcUID), "a stopped instance's uid must be free again")
	t.Logf("stop: %s, source jail, socket and cgroup removed", stopped.Round(time.Millisecond))

	// The restore runs as a different uid, named explicitly: a checkpoint restored into
	// the uid that produced it would not prove the jail moved with the state. Naming it
	// is the launcher's to reserve — reserving it here as well would refuse the launch.
	restoreUID := srcUID + 1

	restID := "rest"
	tRestore := time.Now()
	restored, err := launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: restID,
		Mode:       snapshot.ModeRestore,
		Kernel:     kernel,
		Rootfs:     savedDisk,
		State:      savedState,
		Mem:        savedMem,
		JailUID:    restoreUID,
	})
	require.NoError(t, err, "a checkpoint taken in one jail must load in another")
	t.Cleanup(func() { _ = restored.Stop(context.Background()) })
	restoredTook := time.Since(tRestore)

	assert.Equal(t, "/snapshot/vm.state", restored.StatePath, "the caller is told the path /snapshot/load was given")
	assert.Equal(t, "/snapshot/vm.mem", restored.MemPath)
	assert.Equal(t, "Running", apiState(t, restored.APISocket), "a resumed guest is running, not merely loaded")

	// The proof that matters: the guest answers, and it is the same boot continuing.
	restGuest := attachGuest(t, restored, launcher, restID)
	token := fmt.Sprintf("JAILED-RESTORE-%d", time.Now().UnixNano())
	assert.Contains(t, restGuest.run("echo "+token), token, "a command must run in the restored guest")
	afterUptime := restGuest.uptime(t)
	restoredConsole := restGuest.transcript()
	assert.NotContains(t, restoredConsole, "Linux version",
		"the restored console must carry no kernel boot banner: the guest resumed rather than booted")
	assert.GreaterOrEqual(t, afterUptime, beforeUptime,
		"guest uptime must continue across a restore (it was %.2fs before the snapshot)", beforeUptime)
	t.Logf("restore: %s to a resumed guest as uid %d; uptime %.2fs -> %.2fs",
		restoredTook.Round(time.Millisecond), restoreUID, beforeUptime, afterUptime)

	// The uid comes from the process itself, not from the launcher's bookkeeping.
	restoredUID := jailUID(t, launcher, restID)
	assert.Equal(t, restoreUID, restoredUID, "the restored VMM runs as the uid it was asked for")
	assert.NotEqual(t, srcUID, restoredUID, "the two instances never share a uid")

	restPID := jailPID(t, launcher, restID)
	tRestStop := time.Now()
	require.NoError(t, restored.Stop(ctx))
	t.Logf("stop: %s, restored jail, socket and cgroup removed", time.Since(tRestStop).Round(time.Millisecond))
	assertJailGone(t, launcher, restID, restPID, restored.APISocket)
	assert.False(t, uids.Leased(restoreUID), "the restored instance's uid is free after its teardown")

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

// hardlink materialises src at dst without copying it, falling back to a copy when the
// two are on different filesystems. The link survives the jail's removal, which is what
// keeps a checkpoint usable after its source instance is stopped.
func hardlink(t *testing.T, src, dst string) string {
	t.Helper()

	if err := os.Link(src, dst); err != nil {
		require.NoError(t, copySparse(src, dst), "keep %s after the jail is removed", src)
	}
	require.FileExists(t, dst)
	return dst
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
	assert.NoDirExists(t, filepath.Join("/sys/fs/cgroup", launcher.ParentCgroup),
		"the parent cgroup the jailer created for it must not be left behind either")
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
