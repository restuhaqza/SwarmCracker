//go:build integration

package integration

// SBX-084–086 on a real host: the guest operations that did not exist, exercised
// from inside a running sandbox and compared with what the guest itself says.
//
// What this file proves, and only this: `info` and `net` answer with the values the
// guest's own commands report (kernel, machine, hostname, vCPUs, memory, addresses,
// routes, resolver); `stat` and `list` describe a real tree and refuse a path that
// leaves the file root, by traversal and through a link; `tar` streams a directory
// whose extraction on the host is byte-identical to the guest's tree, and a file
// that disappears under the stream ends it with a typed failure and no complete
// archive; `activity` changes while the sandbox is running something and says so
// again when it is idle; and an agent built before these operations refuses each of
// them with a message that names the operation and the capabilities it does have.
//
// Prerequisites: Linux, root, /dev/kvm, firecracker and jailer in PATH, iproute2,
// nftables, and a bootable guest image that starts the guest agent at boot:
//
//	SWARMCRACKER_TEST_KERNEL=/var/tmp/m3-netns/assets/vmlinux \
//	SWARMCRACKER_TEST_ROOTFS=/var/tmp/m3-netns/assets/guest-rootfs.ext4 \
//	SWARMCRACKER_TEST_OLD_AGENT=/var/tmp/sbx084-old-agent/sc-agent \
//	go test -tags=integration -run GuestOps -v ./test/integration/
//
// SWARMCRACKER_TEST_OLD_AGENT is an sc-agent built from the revision before these
// operations existed; the second test is skipped without it, because the artifact
// has to be built from that revision. SWARMCRACKER_TEST_KEEP=1 keeps the scratch
// directory after a failure.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/image"
)

const (
	// The sandbox IDs this file names. They are its own, so a run of it cannot
	// collide with another test's namespace or wipe its devices.
	guestOpsSandbox    = "gom-ops"
	guestOpsOldSandbox = "gom-old"

	// guestOpsBigFile is the size of the file written from the host: large enough
	// to span several frames and several buffered writes, so an archive of it
	// exercises the chunking rather than a single frame.
	guestOpsBigFile = 4 << 20
)

// TestIntegration_GuestOps_ReportTheGuest is the whole slice on one sandbox: every
// operation is asked through the agent and compared with the guest's own answer.
func TestIntegration_GuestOps_ReportTheGuest(t *testing.T) {
	requireSandboxHost(t)

	f := newNetnsFixture(t)
	installGuestOpsImage(t, f, "")
	s := f.bootSandboxWith(t, guestOpsSandbox, nil)

	checkInfo(t, s)
	checkNet(t, s)
	checkFiles(t, s)
	checkTar(t, s)
	checkTarWhileAFileDisappears(t, s)
	checkActivity(t, s)

	// The sandbox is stopped and its namespace and TAP removed by the fixture's
	// cleanup; nothing here has to undo what the operations did, because every one
	// of them is a read.
}

// installGuestOpsImage gives the fixture a copy of the guest image with this
// build's agent in it — and with the /workspace a sandbox template ships. An empty
// agentPath installs the agent this build embeds; otherwise it installs that
// binary, which is how the old-agent test gets a guest that predates the
// operations.
func installGuestOpsImage(t *testing.T, f *netnsFixture, agentPath string) {
	t.Helper()

	prepared := filepath.Join(f.scratch, "guest-rootfs.ext4")
	require.NoError(t, copySparse(f.rootfs, prepared))

	var body []byte
	if agentPath == "" {
		body = image.AgentBinary()
	} else {
		var err error
		body, err = os.ReadFile(agentPath)
		require.NoError(t, err, "reading the old agent to install")
	}

	editGuestImage(t, prepared, func(root string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, "sbin/sc-agent"), body, 0o755))
		// A template's workspace mount point. The operations are confined to it, so
		// a guest image without it answers not_found — which the file operations
		// test covers separately.
		require.NoError(t, os.MkdirAll(filepath.Join(root, "workspace"), 0o755))
	})
	f.rootfs = prepared
}

// checkInfo compares every field of the report with the command that reads the
// same fact inside the guest.
func checkInfo(t *testing.T, s *sandbox) {
	t.Helper()

	info, err := s.guest.Info(context.Background())
	require.NoError(t, err, "asking the guest about itself")
	t.Logf("[%s] info\n%s", s.id, prettyJSON(t, info))

	release := strings.TrimSpace(s.mustRun(t, "cat /proc/sys/kernel/osrelease"))
	t.Logf("[%s] guest: cat /proc/sys/kernel/osrelease -> %q (report: %q)", s.id, release, info.KernelRelease)
	assert.Equal(t, release, info.KernelRelease)

	machine := strings.TrimSpace(s.mustRun(t, "uname -m"))
	t.Logf("[%s] guest: uname -m -> %q (report: %q)", s.id, machine, info.Machine)
	assert.Equal(t, machine, info.Machine)

	hostname := strings.TrimSpace(s.mustRun(t, "hostname"))
	t.Logf("[%s] guest: hostname -> %q (report: %q)", s.id, hostname, info.Hostname)
	assert.Equal(t, hostname, info.Hostname)

	machineID := strings.TrimSpace(s.mustRun(t, "cat /etc/machine-id 2>/dev/null || true"))
	t.Logf("[%s] guest: cat /etc/machine-id -> %q (report: %q)", s.id, machineID, info.MachineID)
	assert.Equal(t, machineID, info.MachineID)

	cpus := strings.TrimSpace(s.mustRun(t, "nproc"))
	t.Logf("[%s] guest: nproc -> %q (report: %d)", s.id, cpus, info.CPUs)
	assert.Equal(t, cpus, strconv.Itoa(info.CPUs))

	memTotal := strings.Fields(s.mustRun(t, "head -1 /proc/meminfo"))
	require.GreaterOrEqual(t, len(memTotal), 2)
	t.Logf("[%s] guest: head -1 /proc/meminfo -> %q kB (report: %d bytes)", s.id, memTotal[1], info.MemoryTotalBytes)
	assert.Equal(t, memTotal[1]+" kB", fmt.Sprintf("%d kB", info.MemoryTotalBytes/1024))

	uptime := strings.TrimSpace(s.mustRun(t, `cut -d' ' -f1 /proc/uptime`))
	guestUptime, err := strconv.ParseFloat(uptime, 64)
	require.NoError(t, err)
	t.Logf("[%s] guest: /proc/uptime -> %s s (report: %.2f s)", s.id, uptime, info.UptimeSeconds)
	assert.Less(t, math.Abs(guestUptime-info.UptimeSeconds), 10.0,
		"the two reads are moments apart, so the guest's uptime has moved on")

	// The toolchain probe is a fixed list, so the guest's own answer for that list
	// is what the report must carry.
	tools := strings.TrimSpace(s.mustRun(t, `for t in sh bash tar gzip curl wget python3 node npm go git jq; do `+
		`for d in /usr/local/sbin /usr/local/bin /usr/sbin /usr/bin /sbin /bin; do `+
		`if [ -e "$d/$t" ]; then echo "$t"; break; fi; done; done | sort | tr '\n' ' '`))
	t.Logf("[%s] guest: fixed toolchain probe -> [%s] (report: [%s])", s.id, tools, strings.Join(info.Tools, " "))
	assert.Equal(t, strings.Fields(tools), info.Tools)

	// os-release is optional in a template; the report says what the guest has.
	osRelease := s.mustRun(t, `[ -f /etc/os-release ] && echo present || echo absent`)
	if strings.TrimSpace(osRelease) == "present" {
		pretty := strings.TrimSpace(s.mustRun(t, `. /etc/os-release; echo "${PRETTY_NAME:-}"`))
		t.Logf("[%s] guest: /etc/os-release PRETTY_NAME -> %q (report: %q)", s.id, pretty, info.OSName)
		assert.Equal(t, pretty, info.OSName)
	} else {
		t.Logf("[%s] guest has no /etc/os-release (report: os_name=%q os_id=%q)", s.id, info.OSName, info.OSID)
		assert.Empty(t, info.OSName)
		assert.Empty(t, info.OSID)
	}

	assert.Equal(t, agent.Version, info.Protocol)
	assert.NotEmpty(t, info.BootID)
	// The launch's own hello was taken before the guest applied its identity, and
	// applying it rotates the boot identity, so the two legitimately differ; what
	// the report must describe is the guest this session is talking to.
	t.Logf("[%s] boot id: handshake at launch %q, this session %q, report %q",
		s.id, s.launched.Hello.BootID, s.guest.Hello().BootID, info.BootID)
	assert.Equal(t, s.guest.Hello().BootID, info.BootID,
		"the report describes the guest this session is talking to")
}

// checkNet compares the report with the guest's own kernel view, and with the
// identity the node allocated for this sandbox.
func checkNet(t *testing.T, s *sandbox) {
	t.Helper()

	report, err := s.guest.Net(context.Background())
	require.NoError(t, err, "asking the guest about its network")
	t.Logf("[%s] net\n%s", s.id, prettyJSON(t, report))

	guestAddr := strings.TrimSpace(s.mustRun(t,
		`ip -4 addr show dev eth0 | sed -n 's/.*inet \([0-9.]*\/[0-9]*\).*/\1/p'`))
	guestMAC := strings.TrimSpace(s.mustRun(t, "cat /sys/class/net/eth0/address"))
	guestMTU := strings.TrimSpace(s.mustRun(t, "cat /sys/class/net/eth0/mtu"))
	t.Logf("[%s] guest: ip -4 addr show dev eth0 -> %s, MAC %s, MTU %s", s.id, guestAddr, guestMAC, guestMTU)

	eth0 := findInterface(report.Interfaces, agent.DefaultIdentityInterface)
	require.NotNil(t, eth0, "the guest reports the interface its identity configured")
	assert.Contains(t, eth0.Addresses, guestAddr, "the address the guest holds is the address the report carries")
	assert.Equal(t, guestMAC, eth0.MAC)
	assert.Equal(t, guestMTU, strconv.Itoa(eth0.MTU))
	assert.True(t, eth0.Up)

	// The node allocated this address and MAC for the sandbox; the guest wears them
	// because its identity said so, and the report must describe what it wears.
	assert.Equal(t, s.net.MAC, guestMAC)
	assert.Equal(t, s.net.GuestIP.String()+fmt.Sprintf("/%d", s.net.CIDR.Bits()), guestAddr)

	lo := findInterface(report.Interfaces, "lo")
	require.NotNil(t, lo, "a sandbox's loopback is its own and is reported")
	assert.True(t, lo.Loopback)
	assert.Contains(t, lo.Addresses, "127.0.0.1/8")

	guestDefault := strings.TrimSpace(s.mustRun(t,
		`ip -4 route | sed -n 's/^default via \([0-9.]*\) dev \([a-z0-9]*\).*/\1 \2/p'`))
	t.Logf("[%s] guest: ip -4 route default -> %q", s.id, guestDefault)
	require.NotEmpty(t, guestDefault, "the sandbox has a default route")
	wantGateway, wantDevice, _ := strings.Cut(guestDefault, " ")

	defaultRoute := findDefaultRoute(report.Routes, "ipv4")
	require.NotNil(t, defaultRoute, "the report carries the default route the guest uses")
	assert.Equal(t, wantGateway, defaultRoute.Gateway)
	assert.Equal(t, wantDevice, defaultRoute.Device)
	assert.Equal(t, s.net.Gateway.String(), defaultRoute.Gateway)

	guestResolvers := strings.Fields(strings.Join(strings.Split(
		strings.TrimSpace(s.mustRun(t, `sed -n 's/^nameserver //p' /etc/resolv.conf`)), "\n"), " "))
	t.Logf("[%s] guest: /etc/resolv.conf nameservers -> %v (report: %v)", s.id, guestResolvers, report.Resolvers)
	assert.Equal(t, guestResolvers, report.Resolvers)
	assert.Equal(t, s.net.DNS(), report.Resolvers)

	// And the report is not a snapshot of one moment: it reads the kernel's tables,
	// so an address the guest takes now is in the next report.
	s.mustRun(t, "ip addr add 10.99.0.9/32 dev lo 2>/dev/null || true")
	t.Cleanup(func() { _, _ = s.run(t, "ip addr del 10.99.0.9/32 dev lo 2>/dev/null || true") })
	after, err := s.guest.Net(context.Background())
	require.NoError(t, err)
	lo = findInterface(after.Interfaces, "lo")
	require.NotNil(t, lo)
	assert.Contains(t, lo.Addresses, "10.99.0.9/32", "an address added inside the guest appears in the next report")
}

// checkFiles exercises stat and list on a real tree, and the refusals that make
// the file root a boundary rather than a convention.
func checkFiles(t *testing.T, s *sandbox) {
	t.Helper()
	ctx := context.Background()

	s.mustRun(t, "mkdir -p /workspace/proj/sub")
	s.mustRun(t, `printf 'alpha' > /workspace/proj/a.txt && chmod 640 /workspace/proj/a.txt`)
	s.mustRun(t, `printf 'deep' > /workspace/proj/sub/deep.txt`)
	s.mustRun(t, "ln -sf a.txt /workspace/proj/link.txt")
	// A link out of the workspace: naming it is allowed, walking through it is not.
	s.mustRun(t, "ln -sfn /etc /workspace/escape")

	entry, err := s.guest.Stat(ctx, "proj/a.txt")
	require.NoError(t, err)
	guestSize := strings.TrimSpace(s.mustRun(t, "stat -c %s /workspace/proj/a.txt"))
	guestMode := strings.TrimSpace(s.mustRun(t, `ls -l /workspace/proj/a.txt | cut -d' ' -f1`))
	guestOwner := strings.TrimSpace(s.mustRun(t, "stat -c '%u %g' /workspace/proj/a.txt"))
	t.Logf("[%s] stat proj/a.txt -> %s", s.id, prettyJSON(t, entry))
	t.Logf("[%s] guest: stat -c %%s -> %s, ls -l -> %s, stat -c '%%u %%g' -> %s", s.id, guestSize, guestMode, guestOwner)
	assert.Equal(t, guestSize, strconv.FormatInt(entry.Size, 10))
	assert.Equal(t, guestMode, entry.Mode)
	assert.Equal(t, guestOwner, fmt.Sprintf("%d %d", entry.UID, entry.GID))
	assert.Equal(t, "file", entry.Type)

	// The mode of a link and of a directory reads the way ls writes them, which is
	// what makes the report comparable with the tools a caller already has.
	for _, tc := range []struct{ path, kind string }{
		{"proj/link.txt", "symlink"},
		{"proj/sub", "dir"},
	} {
		described, err := s.guest.Stat(ctx, tc.path)
		require.NoError(t, err)
		guest := strings.TrimSpace(s.mustRun(t, `ls -ld /workspace/`+tc.path+` | cut -d' ' -f1`))
		t.Logf("[%s] stat %s -> mode %s, type %s (guest: ls -ld -> %s)", s.id, tc.path, described.Mode, described.Type, guest)
		assert.Equal(t, guest, described.Mode)
		assert.Equal(t, tc.kind, described.Type)
	}

	link, err := s.guest.Stat(ctx, "proj/link.txt")
	require.NoError(t, err)
	t.Logf("[%s] stat proj/link.txt -> %s", s.id, prettyJSON(t, link))
	assert.Equal(t, "symlink", link.Type)
	assert.Equal(t, "a.txt", link.Target)

	listing, err := s.guest.List(ctx, "proj")
	require.NoError(t, err)
	guestNames := strings.Fields(s.mustRun(t, "ls -A /workspace/proj | sort"))
	names := make([]string, 0, len(listing.Entries))
	for _, item := range listing.Entries {
		names = append(names, item.Name)
	}
	t.Logf("[%s] list proj -> %v (guest: ls -A -> %v)", s.id, names, guestNames)
	assert.Equal(t, guestNames, names)
	assert.False(t, listing.Truncated)

	// The root itself lists, and its entries include the directory just made.
	top, err := s.guest.List(ctx, "")
	require.NoError(t, err)
	topNames := make([]string, 0, len(top.Entries))
	for _, item := range top.Entries {
		topNames = append(topNames, item.Name)
	}
	assert.Contains(t, topNames, "proj")
	assert.Contains(t, topNames, "escape")

	// The refusals. Each is logged verbatim, because the message is the part an
	// operator reads.
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"stat of a traversal", func() error {
			_, err := s.guest.Stat(ctx, "../../etc/passwd")
			return err
		}},
		{"list of a traversal", func() error {
			_, err := s.guest.List(ctx, "../..")
			return err
		}},
		{"list through a link that leaves the root", func() error {
			_, err := s.guest.List(ctx, "escape/etc")
			return err
		}},
		{"tar through a link that leaves the root", func() error {
			var sink bytes.Buffer
			_, err := s.guest.Tar(ctx, "escape/etc", &sink)
			return err
		}},
	} {
		err := tc.call()
		require.Error(t, err, tc.name)
		t.Logf("[%s] %s was refused: %v (code %s)", s.id, tc.name, err, agent.CodeOf(err))
		assert.Equal(t, agent.CodeUnsafePath, agent.CodeOf(err), tc.name)
	}

	// The contrast that makes the rule precise: the link itself is describable,
	// because naming a target is not reading it.
	escaping, err := s.guest.Stat(ctx, "escape")
	require.NoError(t, err)
	t.Logf("[%s] stat escape -> %s", s.id, prettyJSON(t, escaping))
	assert.Equal(t, "symlink", escaping.Type)
	assert.Equal(t, "/etc", escaping.Target)

	_, err = s.guest.Stat(ctx, "proj/absent.txt")
	require.Error(t, err)
	t.Logf("[%s] stat of a missing file: %v (code %s)", s.id, err, agent.CodeOf(err))
	assert.Equal(t, agent.CodeNotFound, agent.CodeOf(err))
}

// checkTar streams a directory, extracts it on the host with the host's own tar,
// and proves the two trees hold the same files with the same bytes.
func checkTar(t *testing.T, s *sandbox) {
	t.Helper()
	ctx := context.Background()

	// A file written from the host, so the test knows its exact bytes; it is larger
	// than a frame, so the archive spans several.
	big := deterministicBytes(guestOpsBigFile)
	require.NoError(t, s.guest.CopyTo(ctx, "/workspace/proj/big.bin", bytes.NewReader(big), 0o644))

	scratch := t.TempDir()
	archivePath := filepath.Join(scratch, "proj.tar")
	archive, err := os.Create(archivePath)
	require.NoError(t, err)
	summary, err := s.guest.Tar(ctx, "proj", archive)
	require.NoError(t, err, "streaming the workspace")
	require.NoError(t, archive.Close())
	t.Logf("[%s] tar proj -> %s", s.id, prettyJSON(t, summary))

	info, err := os.Stat(archivePath)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), summary.Bytes, "the summary counts the bytes that were streamed")

	// Extract with the host's tar, which is not this code: if the stream is not a
	// tar archive, this is where it shows.
	extract := filepath.Join(scratch, "extract")
	require.NoError(t, os.MkdirAll(extract, 0o755))
	hostEvidence(t, "tar -xf (host)", "tar", "-xf", archivePath, "-C", extract)

	// The guest's own view of its tree, from the guest's own find and sha256sum.
	guestTree := strings.TrimSpace(s.mustRun(t, `cd /workspace && find proj -type f -exec sha256sum {} + | sort`))
	// The host's view of what came out of the archive.
	hostTree := hostFindHashes(t, extract)

	t.Logf("[%s] guest: cd /workspace && find proj -type f -exec sha256sum {} +\n%s", s.id, indent(guestTree))
	t.Logf("[host] find in the extracted tree\n%s", indent(hostTree))
	assert.Equal(t, guestTree, hostTree, "the extracted tree is the guest's tree, file for file")

	// The bytes of the host-written file survive the round trip exactly.
	extracted, err := os.ReadFile(filepath.Join(extract, "proj", "big.bin"))
	require.NoError(t, err)
	assert.Equal(t, sha256Hex(big), sha256Hex(extracted))
	assert.Equal(t, int64(len(big)), int64(len(extracted)))

	// The link is archived as a link, and its target is the guest's target.
	linkTarget, err := os.Readlink(filepath.Join(extract, "proj", "link.txt"))
	require.NoError(t, err)
	guestLinkTarget := strings.TrimSpace(s.mustRun(t, "readlink /workspace/proj/link.txt"))
	t.Logf("[%s] guest: readlink proj/link.txt -> %q (extracted link -> %q)", s.id, guestLinkTarget, linkTarget)
	assert.Equal(t, guestLinkTarget, linkTarget)

	// No name in the archive leaves the directory it was extracted into.
	names := hostEvidence(t, "tar -tf (host)", "tar", "-tf", archivePath)
	for _, name := range strings.Fields(names) {
		assert.False(t, strings.HasPrefix(name, "/"), "an archive name must be relative: %q", name)
		assert.NotContains(t, name, "..", "an archive name must stay inside the extraction root: %q", name)
	}
}

// checkTarWhileAFileDisappears is the boundary case this file chose: a file the
// walk has not reached yet is deleted while the archive streams. The stream must
// end with a typed failure, must not carry a summary, and must leave the session
// usable — a caller that ignores the failure still holds a stream that is visibly
// not an archive.
func checkTarWhileAFileDisappears(t *testing.T, s *sandbox) {
	t.Helper()
	ctx := context.Background()

	s.mustRun(t, "mkdir -p /workspace/van")
	s.mustRun(t, `printf 'first' > /workspace/van/a.txt`)
	s.mustRun(t, `printf 'last' > /workspace/van/zzz.txt`)
	require.NoError(t, s.guest.CopyTo(ctx, "/workspace/van/big.bin",
		bytes.NewReader(deterministicBytes(guestOpsBigFile)), 0o644))

	// A second connection, ready before the stream starts: the deletion must not
	// pay for a dial while the archive is in flight.
	deleter := dialGuest(t, s.launched)
	defer func() { _ = deleter.Close() }()

	var once sync.Once
	slow := &slowWriter{
		threshold: 256 << 10,
		delay:     3 * time.Millisecond,
		onFire: func() {
			once.Do(func() {
				t.Logf("[%s] deleting /workspace/van/zzz.txt while the archive is streaming", s.id)
				_, err := deleter.ExecStatus(ctx, []string{"/bin/rm", "/workspace/van/zzz.txt"},
					agent.ExecOptions{})
				if err != nil {
					t.Logf("[%s] the deletion failed: %v", s.id, err)
				}
			})
		},
	}

	summary, err := s.guest.Tar(ctx, "van", slow)
	require.Error(t, err, "a file the walk cannot open fails the archive")
	t.Logf("[%s] tar van while zzz.txt vanished: %v (code %s)", s.id, err, agent.CodeOf(err))
	assert.Equal(t, agent.CodeNotFound, agent.CodeOf(err))
	t.Logf("[%s] the fragment received is %d bytes and no summary came back (%+v)", s.id, slow.written(), summary)

	// The archive was aborted, not closed: it does not carry tar's end-of-archive
	// marker, and the host's own tar refuses it.
	fragmentPath := filepath.Join(t.TempDir(), "van.partial.tar")
	require.NoError(t, os.WriteFile(fragmentPath, slow.bytes(), 0o644))
	out, tarErr := exec.Command("tar", "-tf", fragmentPath).CombinedOutput()
	t.Logf("[host] tar -tf of the aborted stream: %v\n%s", tarErr, indent(string(out)))
	assert.Error(t, tarErr, "an aborted stream must not read as a complete archive")
	assert.False(t, hasEndOfArchiveMarker(slow.bytes()),
		"the stream stopped without tar's two zero blocks, which is why it cannot be extracted")

	// The error frame was the stream's terminator, so the session is still at a
	// frame boundary and the next operation works.
	entry, err := s.guest.Stat(ctx, "van/a.txt")
	require.NoError(t, err, "the session is usable after an aborted archive")
	assert.Equal(t, "a.txt", entry.Name)
}

// checkActivity proves the report follows the sandbox instead of standing still:
// while a command runs it says so, and once it has finished it says the sandbox is
// idle.
func checkActivity(t *testing.T, s *sandbox) {
	t.Helper()
	ctx := context.Background()

	// A long command on one connection, observed from another: the agent serves
	// each connection in its own goroutine, so this is the case the operation
	// exists for.
	worker := dialGuest(t, s.launched)
	defer func() { _ = worker.Close() }()

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = worker.ExecStatus(ctx, []string{"/bin/sh", "-c", "sleep 3"}, agent.ExecOptions{})
	}()

	var busy agent.Activity
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		busy, err = s.guest.Activity(ctx)
		require.NoError(t, err)
		if len(busy.Inflight) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Logf("[%s] activity while a command runs\n%s", s.id, prettyJSON(t, busy))
	require.NotEmpty(t, busy.Inflight, "a sandbox running a command has something in flight")
	assert.Equal(t, "exec", busy.Inflight[0].Op)
	assert.Greater(t, busy.Inflight[0].Seconds, 0.0)

	<-finished
	time.Sleep(100 * time.Millisecond)

	idle, err := s.guest.Activity(ctx)
	require.NoError(t, err)
	t.Logf("[%s] activity once the command has finished\n%s", s.id, prettyJSON(t, idle))
	assert.Empty(t, idle.Inflight, "nothing is running, so nothing is in flight")
	assert.Equal(t, "exec", idle.LastOp, "the last thing the sandbox did was run the command")
	assert.Greater(t, idle.IdleSeconds, 0.0)
	assert.Greater(t, idle.UptimeSeconds, busy.UptimeSeconds, "uptime advances between the two reads")

	guestProcesses, err := strconv.Atoi(strings.TrimSpace(s.mustRun(t, `ls -d /proc/[0-9]* | wc -l`)))
	require.NoError(t, err)
	t.Logf("[%s] processes: report %d, guest `ls -d /proc/[0-9]* | wc -l` %d", s.id, idle.Processes, guestProcesses)
	// The count is the guest's own at a moment moments before the guest's own
	// command to count them, and that command is itself a shell, an ls and a wc, so
	// the two numbers are compared with the margin those processes are.
	assert.InDelta(t, guestProcesses, idle.Processes, 5,
		"the count is the guest's own, read a moment before the guest counted it itself")
	assert.Greater(t, idle.Processes, 0)
}

// TestIntegration_GuestOps_AnOlderAgentRefusesThem is the compatibility half: an
// agent built before these operations refuses each of them, naming the operation
// and the capabilities it does advertise, instead of answering something narrower
// or doing nothing at all.
func TestIntegration_GuestOps_AnOlderAgentRefusesThem(t *testing.T) {
	requireSandboxHost(t)

	oldAgent := os.Getenv("SWARMCRACKER_TEST_OLD_AGENT")
	if oldAgent == "" {
		t.Skip("SWARMCRACKER_TEST_OLD_AGENT is unset: the artifact must be built from the revision before these operations")
	}
	ctx := context.Background()

	f := newNetnsFixture(t)
	installGuestOpsImage(t, f, oldAgent)
	s := f.bootSandboxWith(t, guestOpsOldSandbox, nil)

	// What the old agent says about itself: protocol 2, with the operations that
	// existed when it was built.
	hello := s.guest.Hello()
	t.Logf("[%s] old agent hello: protocol %s, caps %v, version %q, build %q",
		s.id, hello.Version, hello.Caps, hello.AgentVersion, hello.BuildID)
	assert.Contains(t, hello.Versions, agent.Version, "the old agent speaks the same protocol version")
	assert.NotContains(t, hello.Caps, agent.CapInfo)
	assert.NotContains(t, hello.Caps, agent.CapTar)

	// The host's side of the refusal: the client refuses before writing a frame.
	for name, call := range map[string]func() error{
		agent.CapInfo:     func() error { _, err := s.guest.Info(ctx); return err },
		agent.CapNet:      func() error { _, err := s.guest.Net(ctx); return err },
		agent.CapStat:     func() error { _, err := s.guest.Stat(ctx, "x"); return err },
		agent.CapList:     func() error { _, err := s.guest.List(ctx, "x"); return err },
		agent.CapTar:      func() error { _, err := s.guest.Tar(ctx, "x", &bytes.Buffer{}); return err },
		agent.CapActivity: func() error { _, err := s.guest.Activity(ctx); return err },
	} {
		err := call()
		require.Error(t, err, name)
		t.Logf("[%s] host refused %q: %v (code %s)", s.id, name, err, agent.CodeOf(err))
		assert.Equal(t, agent.CodeUnsupportedOp, agent.CodeOf(err), name)
		assert.Contains(t, err.Error(), name, "the refusal names the operation")
	}

	// The guest's side of the same refusal, asked directly on the wire so the
	// *agent* answers: a host that skipped its own check would still be refused,
	// with the operation and the capabilities named.
	for _, op := range []string{agent.CapInfo, agent.CapNet, agent.CapStat, agent.CapList, agent.CapTar, agent.CapActivity} {
		msg := rawRequest(t, s.launched.VsockPath, agent.Request{Op: op, ID: "raw-" + op, Version: agent.Version})
		t.Logf("[%s] old agent answered a raw %q request: type %q code %q message %q",
			s.id, op, msg.Type, msg.ErrorCode, msg.Error)
		assert.Equal(t, "error", msg.Type)
		assert.Equal(t, agent.CodeUnsupportedOp, msg.ErrorCode)
		assert.Contains(t, msg.Error, op, "the guest's refusal names the operation")
		assert.Contains(t, msg.Error, "caps:", "the guest's refusal names the capabilities it does have")
	}
}

// rawRequest sends one request frame straight to the guest agent, bypassing the
// host client's own capability check, and returns the frame it answers with.
func rawRequest(t *testing.T, muxerPath string, req agent.Request) agent.Message {
	t.Helper()

	conn, err := net.Dial("unix", muxerPath)
	require.NoError(t, err, "dialling the vsock muxer")
	defer func() { _ = conn.Close() }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))

	// Firecracker's muxer handshake for a host-initiated connection.
	_, err = fmt.Fprintf(conn, "CONNECT %d\n", agent.DefaultPort)
	require.NoError(t, err)
	line, err := readMuxerLine(conn)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "OK "), "muxer answered %q", line)

	require.NoError(t, agent.WriteFrame(conn, req))
	var msg agent.Message
	_, err = agent.ReadFrame(conn, &msg)
	require.NoError(t, err)
	return msg
}

// readMuxerLine reads the muxer's "OK <port>" reply.
func readMuxerLine(conn net.Conn) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for len(line) < 64 {
		if _, err := conn.Read(buf); err != nil {
			return "", err
		}
		if buf[0] == '\n' {
			return strings.TrimRight(string(line), "\r"), nil
		}
		line = append(line, buf[0])
	}
	return "", fmt.Errorf("muxer reply exceeded 64 bytes")
}

// slowWriter is a tar sink that fires once a threshold of bytes has arrived and
// then slows every chunk down, so a test has a window in which the guest's walk
// has not reached the end of the tree yet.
type slowWriter struct {
	threshold int
	delay     time.Duration
	onFire    func()

	mu      sync.Mutex
	buf     bytes.Buffer
	firedAt int
}

func (w *slowWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buf.Write(p)
	fire := w.firedAt == 0 && w.buf.Len() > w.threshold
	if fire {
		w.firedAt = w.buf.Len()
	}
	w.mu.Unlock()

	if fire && w.onFire != nil {
		w.onFire()
	}
	time.Sleep(w.delay)
	return n, err
}

func (w *slowWriter) written() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Len()
}

func (w *slowWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

// deterministicBytes returns n bytes of a pattern only sha256 of which the test
// keeps, so a comparison is exact without holding the pattern in memory twice.
func deterministicBytes(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*7 + i/251)
	}
	return out
}

// sha256Hex is the digest of b in hex.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hostFindHashes runs the host's own find and sha256sum over dir, giving the same
// text the guest's find does: one "<hash>  <path>" line per file, sorted.
func hostFindHashes(t *testing.T, dir string) string {
	t.Helper()

	cmd := exec.Command("sh", "-c", "find . -type f -exec sha256sum {} + | sed 's|  \\./|  |' | sort")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "hashing the extracted tree: %s", out)
	return strings.TrimSpace(string(out))
}

// findInterface returns the named interface, or nil.
func findInterface(interfaces []agent.NetInterface, name string) *agent.NetInterface {
	for i := range interfaces {
		if interfaces[i].Name == name {
			return &interfaces[i]
		}
	}
	return nil
}

// findDefaultRoute returns the family's default route, or nil.
func findDefaultRoute(routes []agent.NetRoute, family string) *agent.NetRoute {
	for i := range routes {
		if routes[i].Family != family {
			continue
		}
		if routes[i].Destination == "0.0.0.0/0" || routes[i].Destination == "::/0" {
			return &routes[i]
		}
	}
	return nil
}

// hasEndOfArchiveMarker reports whether data ends with tar's end of archive: two
// 512-byte blocks of zeros. A stream that fails partway has none, which is what
// makes it fail to extract rather than extract short.
func hasEndOfArchiveMarker(data []byte) bool {
	if len(data) < 1024 {
		return false
	}
	for _, b := range data[len(data)-1024:] {
		if b != 0 {
			return false
		}
	}
	return true
}

// prettyJSON renders a report for the log, field by field.
func prettyJSON(t *testing.T, value any) string {
	t.Helper()

	data, err := json.MarshalIndent(value, "  ", "  ")
	require.NoError(t, err)
	return string(data)
}
