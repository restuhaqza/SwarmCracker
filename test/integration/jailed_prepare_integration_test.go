//go:build integration

package integration

// SBX-083 and SBX-042 on a real host: a jailed VM is booted with one identity, its
// checkpoint is taken, and two clones of that checkpoint come up wearing two
// *different* identities — because the guest applied them from the inside.
//
// This is the pair of claims no unit test can make. Firecracker 1.15.1 refuses to
// rewrite a guest's MAC, its addresses or its metadata configuration once an image
// is loaded, so the only place a clone's identity can be applied is inside the
// guest; and the only channel that stays writable across a load is MMDS. The
// evidence below is therefore read back from inside each guest over its console —
// the MAC the interface carries, the addresses and routes it has, the resolver it
// consults, the machine id and SSH host keys it names itself with, the token it
// authenticates with, the clock it holds — and from the host-side protocol, where a
// hello with the checkpoint's token is refused and a hello with the clone's token
// is accepted.
//
// Prerequisites: Linux, root, /dev/kvm, firecracker and jailer in PATH, and a
// bootable kernel and rootfs that boots to a systemd guest:
//
//	SWARMCRACKER_TEST_KERNEL=/var/tmp/sbx000-rerun/assets/vmlinux \
//	SWARMCRACKER_TEST_ROOTFS=/var/tmp/sbx000-rerun/assets/rootfs.ext4 \
//	go test -tags=integration -run Prepare -v ./test/integration/
//
// Two optional inputs:
//
//	SWARMCRACKER_TEST_OLD_AGENT=<path to an sc-agent built before the prepare op>
//	    runs the "an agent that cannot prepare fails the launch" test. It is skipped
//	    without it, because the artifact has to be built from an older revision.
//	SWARMCRACKER_TEST_KEEP=1 keeps the scratch directory after a failure.

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

// The identities this test gives its instances. A clone's MAC, address and host name
// are its own: the source's are what would otherwise be carried into every clone.
const (
	prepareSourceID = "sbx-src"
	prepareCloneB   = "sbx-clb"
	prepareCloneC   = "sbx-clc"

	// prepareBootMAC is the MAC the VMM presents at boot: the template's, which a
	// checkpoint then freezes. The identity's MAC replaces it from inside the guest,
	// because Firecracker refuses to change it from here once an image is loaded.
	prepareBootMAC = "06:00:ac:10:00:01"

	prepareSourceMAC = "06:00:ac:10:00:0a"
	prepareCloneBMAC = "06:00:ac:10:00:0b"
	prepareCloneCMAC = "06:00:ac:10:00:0c"

	prepareSourceAddr = "10.99.0.10/24"
	prepareCloneBAddr = "10.99.0.11/24"
	prepareCloneCAddr = "10.99.0.12/24"

	// prepareGateway is the host's address on the bridge the instances share, and
	// the resolver they are told to use. Both are the test's own scaffolding: the
	// point is that the guest ends up using what the identity says.
	prepareGateway = "10.99.0.1"
)

// TestIntegration_Prepare_GivesEachCloneItsOwnIdentity is the whole M-A P1 story on
// one host: boot a jailed VM as identity A, checkpoint it, and restore two clones
// that each come up as a machine of their own — verified inside the guest.
func TestIntegration_Prepare_GivesEachCloneItsOwnIdentity(t *testing.T) {
	requireJailedHost(t)
	ctx := context.Background()

	scratch := jailedScratch(t)
	kernel := testAsset(t, "SWARMCRACKER_TEST_KERNEL", "/usr/share/firecracker/vmlinux")
	rootfs := testAsset(t, "SWARMCRACKER_TEST_ROOTFS",
		"/var/lib/firecracker/rootfs/rootfs.ext4", "/tmp/firecracker-rootfs.ext4")

	network := newPrepareNetwork(t)
	launcher, _ := newPrepareLauncher(t, scratch)

	// The source VM's disk carries a per-boot token: without one the guest agent
	// serves unauthenticated, and the token half of the identity channel would have
	// nothing to rotate. It is the token every clone of the checkpoint starts with,
	// because a checkpoint freezes its disk.
	disk := filepath.Join(scratch, "rootfs.ext4")
	require.NoError(t, copySparse(rootfs, disk))
	installGuestAgent(t, disk)
	sourceToken := provisionToken(t, disk)

	sourceIdentity := prepareIdentity(t, "source", prepareSourceID, prepareSourceMAC, prepareSourceAddr)
	tBoot := time.Now()
	source, err := launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: prepareSourceID,
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     disk,
		VCPUs:      2,
		MemoryMB:   512,
		VsockPath:  jailedAgentMuxer,
		AgentToken: sourceToken,
		Net:        []snapshot.NetworkInterface{network.tapFor(t, "s", prepareBootMAC)},
		Identity:   &sourceIdentity,
	})
	require.NoError(t, err, "a cold boot whose identity the guest cannot apply must fail the launch")
	t.Cleanup(func() { _ = source.Stop(context.Background()) })
	t.Logf("source boot + prepare: %s to a running guest", time.Since(tBoot).Round(time.Millisecond))

	// The hook's own report is the guest's answer, and it is part of the launch.
	require.NotNil(t, source.Prepared, "a launch that carries an identity reports what the guest adopted")
	assertPrepared(t, "source", sourceIdentity, *source.Prepared)
	assert.NotEqual(t, source.Hello.BootID, source.Prepared.BootID,
		"the guest reports a boot identity of its own after the hook")

	sourceGuest := attachGuest(t, source, launcher, prepareSourceID)
	sourceGuest.awaitPrompt()
	sourceFacts := guestIdentityFacts(t, sourceGuest)
	t.Logf("source guest reports:\n%s", formatFacts(sourceFacts))
	assertGuestIdentity(t, "source", sourceFacts, sourceIdentity)
	assert.Equal(t, sourceToken, sourceFacts["TOK"],
		"the source keeps the token it was booted with; its identity named none")
	assertResolvable(t, "source", sourceFacts)

	// The checkpoint. It is taken through the manager so the chroot paths and the
	// live disk are what Firecracker writes and copies (SBX-022), which is what makes
	// it loadable in another jail.
	mgr, err := snapshot.NewManager(snapshot.SnapshotConfig{SnapshotDir: filepath.Join(scratch, "snapshots")})
	require.NoError(t, err)
	info, err := mgr.CreateSnapshot(ctx, prepareSourceID, source.APISocket, snapshot.CreateOptions{
		ServiceID:  "sbx-083",
		NodeID:     "the-root",
		VCPUCount:  2,
		MemoryMB:   512,
		RootfsPath: disk,
		JailRoot:   launcher.JailRoot(prepareSourceID),
	})
	require.NoError(t, err, "a jailed VM's checkpoint must be creatable through the manager")
	require.True(t, info.Jailed)

	srcPID := jailPID(t, launcher, prepareSourceID)
	require.NoError(t, source.Stop(ctx))
	assertJailGone(t, launcher, prepareSourceID, srcPID, source.APISocket)

	// Two clones of the one checkpoint, each with an identity of its own and a token
	// of its own. They restore one at a time, so the assertions read a single guest.
	clones := []struct {
		id        string
		mac       string
		address   string
		hostname  string
		token     string
		machineID string
		entropy   string
	}{
		{
			id: prepareCloneB, mac: prepareCloneBMAC, address: prepareCloneBAddr,
			hostname: preparHostname(prepareCloneB), token: mustToken(t),
			machineID: preparMachineID(t), entropy: preparEntropy(t),
		},
		{
			id: prepareCloneC, mac: prepareCloneCMAC, address: prepareCloneCAddr,
			hostname: preparHostname(prepareCloneC), token: mustToken(t),
			machineID: preparMachineID(t), entropy: preparEntropy(t),
		},
	}

	facts := make(map[string]map[string]string, len(clones))
	prepared := make([]agent.PrepareReport, 0, len(clones))
	for _, clone := range clones {
		identity := agent.Identity{
			Interface:     agent.DefaultIdentityInterface,
			MAC:           clone.mac,
			Address:       clone.address,
			Gateway:       prepareGateway,
			DNS:           []string{prepareGateway},
			Hostname:      clone.hostname,
			MachineID:     clone.machineID,
			Token:         clone.token,
			Entropy:       clone.entropy,
			ClockUnixNano: time.Now().UnixNano(),
		}

		tClone := time.Now()
		restored, err := launcher.Start(ctx, snapshot.LaunchRequest{
			InstanceID: clone.id,
			Mode:       snapshot.ModeFork,
			Kernel:     kernel,
			Rootfs:     info.DiskPath,
			State:      info.StatePath,
			Mem:        info.MemoryPath,
			VCPUs:      2,
			MemoryMB:   512,
			VsockPath:  jailedAgentMuxer,
			// The checkpoint's token: this is what the guest was restored with, and
			// the hook is what replaces it.
			AgentToken: sourceToken,
			// The load ignores the boot MAC: a restored NIC keeps the MAC the
			// checkpoint froze, so the clone comes up wearing the source's and the hook
			// is what gives it its own.
			Net:        []snapshot.NetworkInterface{network.tapFor(t, cloneTags[clone.id], prepareSourceMAC)},
			Identity:   &identity,
		})
		require.NoError(t, err, "clone %s must adopt the identity it was given", clone.id)
		t.Cleanup(func() { _ = restored.Stop(context.Background()) })
		t.Logf("clone %s: %s to a resumed guest that adopted %s",
			clone.id, time.Since(tClone).Round(time.Millisecond), clone.address)

		require.NotNil(t, restored.Prepared)
		assertPrepared(t, clone.id, identity, *restored.Prepared)
		prepared = append(prepared, *restored.Prepared)

		// The token the agent accepts after resume is the clone's own: the one the
		// checkpoint carried is refused, and the one the hook installed works. This is
		// the host-side half of "the guest received a token after resume".
		_, errOld := agent.DialWithToken(ctx, restored.VsockPath, agent.DefaultPort, sourceToken)
		assert.ErrorIs(t, errOld, agent.ErrTokenRejected,
			"the checkpoint's token must not authenticate to clone %s", clone.id)
		session, errNew := agent.DialWithToken(ctx, restored.VsockPath, agent.DefaultPort, clone.token)
		require.NoError(t, errNew, "the token the hook installed must authenticate to clone %s", clone.id)
		t.Cleanup(func() { _ = session.Close() })

		// No awaitPrompt: a resumed guest prints nothing on its console, because the
		// shell it resumes with is the one the source was already logged into. The
		// command below is what proves the console is live.
		cloneGuest := attachGuest(t, restored, launcher, clone.id)
		cloneFacts := guestIdentityFacts(t, cloneGuest)
		facts[clone.id] = cloneFacts
		t.Logf("clone %s reports:\n%s", clone.id, formatFacts(cloneFacts))

		assertGuestIdentity(t, clone.id, cloneFacts, identity)
		assertResolvable(t, clone.id, cloneFacts)
		assert.Equal(t, clone.token, cloneFacts["TOK"],
			"the guest's own token file holds the token the identity carried")

		// Idempotence, on the real guest: the hook runs again with the identity the
		// node already published, and nothing but the boot identity changes.
		again, err := session.Prepare(ctx)
		require.NoError(t, err, "prepare is idempotent")
		assert.Equal(t, []string{agent.PartBootID}, again.Changed,
			"a second prepare of the same identity changes nothing but the boot identity")
		assert.Equal(t, clone.machineID, again.MachineID)
	}

	// The two clones are two machines: nothing that names a machine is shared.
	assert.NotEqual(t, facts[prepareCloneB]["MAC"], facts[prepareCloneC]["MAC"])
	assert.NotEqual(t, facts[prepareCloneB]["MID"], facts[prepareCloneC]["MID"])
	assert.NotEqual(t, facts[prepareCloneB]["TOK"], facts[prepareCloneC]["TOK"])
	assert.NotEqual(t, facts[prepareCloneB]["SSHKEY"], facts[prepareCloneC]["SSHKEY"])
	assert.NotEqual(t, facts[prepareCloneB]["ADDR"], facts[prepareCloneC]["ADDR"])

	// And neither of them is the source: this is the identity leak D10 exists to
	// close.
	assert.NotEqual(t, sourceFacts["MAC"], facts[prepareCloneB]["MAC"])
	assert.NotEqual(t, sourceFacts["MAC"], facts[prepareCloneC]["MAC"])
	assert.NotEqual(t, sourceFacts["MID"], facts[prepareCloneB]["MID"])
	assert.NotEqual(t, sourceFacts["MID"], facts[prepareCloneC]["MID"])
	assert.NotEqual(t, sourceFacts["SSHKEY"], facts[prepareCloneB]["SSHKEY"],
		"a clone must not present the SSH host key of the machine its checkpoint came from")
	assert.NotEqual(t, sourceFacts["SSHKEY"], facts[prepareCloneC]["SSHKEY"])
	assert.Equal(t, sourceIdentity.MAC, sourceFacts["MAC"])
	assert.NotContains(t, facts[prepareCloneB]["ADDR"], "10.99.0.10",
		"the source's address must be gone from a clone")

	t.Logf("source and clones carried %d identities through one checkpoint",
		len(prepared)+1)
}

// TestIntegration_Prepare_FailsTheLaunchWhenTheGuestCannotApply proves the hook is
// not a formality: a guest that cannot carry the identity out fails the launch, and
// the failure is a typed error that names what went wrong. The instance is torn
// down rather than handed over wearing a stale identity.
func TestIntegration_Prepare_FailsTheLaunchWhenTheGuestCannotApply(t *testing.T) {
	requireJailedHost(t)
	ctx := context.Background()

	scratch := jailedScratch(t)
	kernel := testAsset(t, "SWARMCRACKER_TEST_KERNEL", "/usr/share/firecracker/vmlinux")
	rootfs := testAsset(t, "SWARMCRACKER_TEST_ROOTFS",
		"/var/lib/firecracker/rootfs/rootfs.ext4", "/tmp/firecracker-rootfs.ext4")

	network := newPrepareNetwork(t)
	launcher, _ := newPrepareLauncher(t, scratch)

	disk := filepath.Join(scratch, "rootfs.ext4")
	require.NoError(t, copySparse(rootfs, disk))
	installGuestAgent(t, disk)
	token := provisionToken(t, disk)

	// A gateway that is not on the instance's network: the guest's kernel refuses
	// `default via` for it, so the applier fails after it has already applied the MAC
	// and the address. It is the shape of a misallocated identity — something the node
	// cannot detect from outside, which is exactly why the hook has to report.
	identity := prepareIdentity(t, "failing", prepareCloneB, prepareCloneBMAC, prepareCloneBAddr)
	identity.Gateway = "10.99.9.254"

	id := prepareCloneB
	_, err := launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: id,
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     disk,
		VCPUs:      2,
		MemoryMB:   512,
		VsockPath:  jailedAgentMuxer,
		AgentToken: token,
		Net:        []snapshot.NetworkInterface{network.tapFor(t, "b", prepareCloneBMAC)},
		Identity:   &identity,
	})
	require.Error(t, err, "a hook that cannot take must fail the launch")
	assert.ErrorIs(t, err, snapshot.ErrPrepareFailed)
	assert.Contains(t, err.Error(), "prepare")
	t.Logf("launch with an unreachable gateway failed with: %v", err)

	assertLaunchLeftNothing(t, launcher, id)
}

// TestIntegration_Prepare_RefusesAnAgentThatCannotPrepare is the test SBX-042 asks
// for, on a real guest: the template's agent speaks protocol v2 but does not
// advertise the hook. The launch must fail with a version error the operator can
// read, rather than returning an instance that still wears the identity of the
// machine its checkpoint came from.
//
// It needs an agent built before the operation existed:
//
//	SWARMCRACKER_TEST_OLD_AGENT=/path/to/old/sc-agent
func TestIntegration_Prepare_RefusesAnAgentThatCannotPrepare(t *testing.T) {
	requireJailedHost(t)

	oldAgent := os.Getenv("SWARMCRACKER_TEST_OLD_AGENT")
	if oldAgent == "" {
		t.Skip("set SWARMCRACKER_TEST_OLD_AGENT to an sc-agent built before the prepare op")
	}
	info, err := os.Stat(oldAgent)
	require.NoError(t, err, "SWARMCRACKER_TEST_OLD_AGENT must name a file")
	require.NotZero(t, info.Size())

	ctx := context.Background()
	scratch := jailedScratch(t)
	kernel := testAsset(t, "SWARMCRACKER_TEST_KERNEL", "/usr/share/firecracker/vmlinux")
	rootfs := testAsset(t, "SWARMCRACKER_TEST_ROOTFS",
		"/var/lib/firecracker/rootfs/rootfs.ext4", "/tmp/firecracker-rootfs.ext4")

	network := newPrepareNetwork(t)
	launcher, _ := newPrepareLauncher(t, scratch)

	disk := filepath.Join(scratch, "rootfs.ext4")
	require.NoError(t, copySparse(rootfs, disk))
	// The unit and its enablement are the same as every other test's; only the binary is
	// the older build, which is the whole point: a template whose agent predates the
	// hook.
	installGuestAgent(t, disk)
	installGuestAgentFrom(t, disk, oldAgent)
	token := provisionToken(t, disk)

	identity := prepareIdentity(t, "oldagent", prepareCloneB, prepareCloneBMAC, prepareCloneBAddr)
	_, err = launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: prepareCloneB,
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     disk,
		VCPUs:      2,
		MemoryMB:   512,
		VsockPath:  jailedAgentMuxer,
		AgentToken: token,
		Net:        []snapshot.NetworkInterface{network.tapFor(t, "b", prepareCloneBMAC)},
		Identity:   &identity,
	})
	require.Error(t, err, "an agent that cannot prepare must fail the launch, not skip the hook")
	assert.ErrorIs(t, err, snapshot.ErrPrepareUnsupported)
	assert.Contains(t, err.Error(), "protocol "+agent.Version, "the refusal names the protocol the agent speaks")
	assert.Contains(t, err.Error(), "does not include \"prepare\"", "and the operation it is missing")
	assert.Contains(t, err.Error(), "predates the post-restore hook")
	t.Logf("launch on an agent without the hook failed with: %v", err)

	assertLaunchLeftNothing(t, launcher, prepareCloneB)
}

// cloneTags names each instance's tap in one or two characters, because an interface
// name is limited to 15 characters.
var cloneTags = map[string]string{prepareSourceID: "s", prepareCloneB: "b", prepareCloneC: "c"}

// prepareNetwork is the test's own network scaffolding: one bridge with a host
// address, a tap per instance attached to it, and a resolver that answers one name.
//
// It is scaffolding, not the product's network policy: M3 owns per-sandbox
// namespaces, bridges and filtering. What it provides here is enough for a guest to
// reach the node's resolver and for the node to reach the guest's console — and for
// the assertions to be about the identity the guest adopted rather than about
// isolation it does not have yet.
type prepareNetwork struct {
	bridge string
	taps   map[string]string
}

func newPrepareNetwork(t *testing.T) *prepareNetwork {
	t.Helper()

	// A short name: an interface name is at most 15 characters (IFNAMSIZ), and the
	// bridge plus a per-instance suffix has to fit inside that.
	bridge := fmt.Sprintf("sxb%d", time.Now().UnixNano()%100_000)
	n := &prepareNetwork{bridge: bridge, taps: map[string]string{}}

	require.NoError(t, runIP("link", "add", bridge, "type", "bridge"))
	require.NoError(t, runIP("addr", "add", prepareGateway+"/24", "dev", bridge))
	require.NoError(t, runIP("link", "set", bridge, "up"))
	t.Cleanup(func() { _ = runIP("link", "del", bridge) })

	return n
}

// tapFor gives one instance its own tap on the shared bridge and returns the
// interface description a launch is told: the device the VMM opens, and the MAC it
// presents at boot. The caller owns the device (identity.go) — this is the caller.
//
// A tap per instance is what lets two clones be asserted separately: the bridge keeps
// them apart at L2 because their MACs differ, and neither needs the other's device.
// tag is one or two characters, because an interface name may only be 15 long.
func (n *prepareNetwork) tapFor(t *testing.T, tag, mac string) snapshot.NetworkInterface {
	t.Helper()

	tap, ok := n.taps[tag]
	if !ok {
		tap = n.bridge + "-" + tag
		n.taps[tag] = tap
		require.NoError(t, runIP("tuntap", "add", "dev", tap, "mode", "tap"))
		t.Cleanup(func() { _ = runIP("link", "del", tap) })
		require.NoError(t, runIP("link", "set", tap, "master", n.bridge))
		require.NoError(t, runIP("link", "set", tap, "up"))
	}
	return snapshot.NetworkInterface{IfaceID: agent.DefaultIdentityInterface, HostTap: tap, GuestMAC: mac}
}

// runIP runs one iproute2 command from the host side of the scaffolding.
func runIP(args ...string) error {
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// newPrepareLauncher is a jailed launcher configured for one test, with a uid range
// of its own.
func newPrepareLauncher(t *testing.T, scratch string) (snapshot.JailedLauncher, *jailer.UIDAllocator) {
	t.Helper()

	uids := newTestUIDAllocator(t)
	launcher := snapshot.JailedLauncher{
		ChrootBaseDir:    filepath.Join(scratch, "jails"),
		FirecrackerPath:  lookPath(t, "firecracker"),
		JailerPath:       lookPath(t, "jailer"),
		ParentCgroup:     "swarmcracker-test",
		MemoryMaxBytes:   1024 << 20,
		PidsMax:          64,
		LogDir:           filepath.Join(scratch, "logs"),
		ConsoleSocketDir: filepath.Join(scratch, "console"),
		UIDs:             uids,
		// A cold boot of this image reaches its agent in a few seconds; the hook adds
		// the metadata read and the interface work on top.
		ReadyTimeout:   90 * time.Second,
		PrepareTimeout: 45 * time.Second,
	}
	return launcher, uids
}

// installGuestAgentFrom installs a specific agent binary into the image, so the test
// can boot a guest whose agent predates the operation under test.
func installGuestAgentFrom(t *testing.T, imagePath, binaryPath string) {
	t.Helper()

	body, err := os.ReadFile(binaryPath)
	require.NoError(t, err)
	editGuestImage(t, imagePath, func(root string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, "sbin/sc-agent"), body, 0o755))
	})
}

// migrateToken provisions a per-boot token into the image and returns it. It is the
// token the guest booted from this disk requires, and one every clone of the
// checkpoint starts with.
func provisionToken(t *testing.T, disk string) string {
	t.Helper()

	token, err := agent.NewToken()
	require.NoError(t, err)
	require.NoError(t, agent.InjectToken(context.Background(), disk, token))
	return token
}

// prepareIdentity builds the identity the node publishes for one instance: a
// complete one, with fresh entropy and the clock of this host.
func prepareIdentity(t *testing.T, _ string, id, mac, address string) agent.Identity {
	t.Helper()

	return agent.Identity{
		Interface:     agent.DefaultIdentityInterface,
		MAC:           mac,
		Address:       address,
		Gateway:       prepareGateway,
		DNS:           []string{prepareGateway},
		Hostname:      preparHostname(id),
		MachineID:     preparMachineID(t),
		Entropy:       preparEntropy(t),
		ClockUnixNano: time.Now().UnixNano(),
	}
}

func preparHostname(id string) string { return "sbx-" + strings.TrimPrefix(id, "sbx-") }

// preparMachineID mints a machine id the way a node would: 16 random bytes, hex
// encoded into the 32 characters the file holds.
func preparMachineID(t *testing.T) string {
	t.Helper()

	var b [16]byte
	_, err := randomReadHost(b[:])
	require.NoError(t, err)
	return hex.EncodeToString(b[:])
}

// preparEntropy mints the fresh entropy a clone needs, because its kernel's pool is
// a copy of its source's.
func preparEntropy(t *testing.T) string {
	t.Helper()

	var b [32]byte
	_, err := randomReadHost(b[:])
	require.NoError(t, err)
	return hex.EncodeToString(b[:])
}

func mustToken(t *testing.T) string {
	t.Helper()
	token, err := agent.NewToken()
	require.NoError(t, err)
	return token
}

// guestIdentityFacts reads the guest's own view of its identity in one console round
// trip: the MAC the interface carries, its addresses, its routes, the resolver it
// consults, the names it calls itself and the token it authenticates with.
func guestIdentityFacts(t *testing.T, g *guest) map[string]string {
	t.Helper()

	script := strings.Join([]string{
		`echo "MAC=$(cat /sys/class/net/eth0/address)"`,
		`echo "ADDR=$(ip -br addr show eth0 | tr -s ' ')"`,
		`echo "ROUTE=$(ip route | tr '\n' '|')"`,
		`echo "RESOLV=$(grep -h nameserver /etc/resolv.conf | tr '\n' ',')"`,
		`echo "SEARCH=$(grep -h '^search' /etc/resolv.conf | tr '\n' ',')"`,
		`echo "HOST=$(hostname)"`,
		`echo "MID=$(cat /etc/machine-id)"`,
		`echo "TOK=$(cat /etc/swarmcracker/agent-token)"`,
		`echo "CLOCK=$(date -u +%s)"`,
		`echo "SSHKEY=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub | cut -d' ' -f2)"`,
		`echo "MMDS=$(curl -s -m 3 -o /dev/null -w '%{http_code}' 169.254.169.254/identity || echo none)"`,
	}, "; ")

	// The console output arrives with \n endings: the helper that reads it strips
	// every \r, so a line is a line and a fact is one line long.
	out := g.run(script)
	facts := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || !factKeys[key] {
			continue
		}
		facts[key] = value
	}
	require.NotEmpty(t, facts["MAC"], "the guest must report its own MAC:\n%s", out)
	return facts
}

// factKeys is what guestIdentityFacts reads, so a console line that is not a fact
// (a prompt, an echoed command) cannot become one.
var factKeys = map[string]bool{
	"MAC": true, "ADDR": true, "ROUTE": true, "RESOLV": true, "SEARCH": true,
	"HOST": true, "MID": true, "TOK": true, "CLOCK": true, "SSHKEY": true,
	"MMDS": true,
}

func formatFacts(facts map[string]string) string {
	keys := []string{"MAC", "ADDR", "ROUTE", "RESOLV", "SEARCH", "HOST", "MID", "TOK", "CLOCK", "SSHKEY", "MMDS"}
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "  %s=%s\n", key, facts[key])
	}
	return strings.TrimRight(b.String(), "\n")
}

// assertGuestIdentity holds the guest to the identity it was given, read from inside
// the guest. Every one of these is a claim the host cannot make from outside: the
// interface's MAC cannot be set from here after a load, and neither can the
// addresses, the routes or the resolver.
func assertGuestIdentity(t *testing.T, who string, facts map[string]string, id agent.Identity) {
	t.Helper()

	assert.Equal(t, id.MAC, strings.ToLower(facts["MAC"]), "%s: the interface carries the identity's MAC", who)
	assert.Contains(t, facts["ADDR"], id.Address, "%s: the interface carries the identity's address", who)
	assert.Contains(t, facts["ROUTE"], "default via "+id.Gateway,
		"%s: the default route uses the identity's gateway", who)
	assert.Contains(t, facts["RESOLV"], "nameserver "+id.DNS[0],
		"%s: the resolver is the identity's", who)
	assert.Equal(t, id.Hostname, facts["HOST"], "%s: the host name is the identity's", who)
	assert.Equal(t, id.MachineID, facts["MID"], "%s: the machine id is the identity's", who)
	assert.NotEmpty(t, facts["SSHKEY"], "%s: the guest has SSH host keys of its own", who)

	// The clock: a resumed guest's wall clock is the checkpoint's, and the identity is
	// what moves it to the node's time. A minute of tolerance is generous for a VM
	// under load and far tighter than the drift a stale checkpoint clock shows.
	clock := parseUnix(t, facts["CLOCK"])
	assert.InDelta(t, time.Now().Unix(), clock, 60, "%s: the guest's clock is the node's time", who)

	// The metadata service answers inside the guest. A bare read has no session token,
	// so version 2 answers 401 — which is the service itself replying, and that is what
	// "reachable" means here. The hook is what reads it with a token.
	assert.Contains(t, []string{"200", "401", "404"}, facts["MMDS"],
		"%s: the metadata service must answer from inside the guest, got %q", who, facts["MMDS"])
}

// assertResolvable states what the resolver evidence is: the guest consults the
// resolver the identity named, and it carries a route that reaches it.
//
// What this does *not* prove is a name resolving end to end. That needs the guest's
// packets to reach the node — a per-sandbox bridge, on-link address and firewall —
// which is the network policy of §5.5 (M3), not the identity channel this change owns.
// Measured on this lab node, the guest-to-host path on a test bridge is intermittent
// (the node runs br_netfilter with a default-deny firewall), so asserting a resolved
// name here would be asserting M3's work, not this one's.
func assertResolvable(t *testing.T, who string, facts map[string]string) {
	t.Helper()

	assert.Contains(t, facts["RESOLV"], "nameserver "+prepareGateway,
		"%s: the resolver is the identity's", who)
	assert.Contains(t, facts["ROUTE"], "169.254.169.254",
		"%s: the metadata route survives the identity being applied", who)
}

// assertPrepared holds the guest's report to the identity the launch published.
func assertPrepared(t *testing.T, who string, id agent.Identity, report agent.PrepareReport) {
	t.Helper()

	assert.Equal(t, id.MAC, report.MAC, "%s: the guest reports the identity's MAC", who)
	assert.Contains(t, report.Addresses, id.Address, "%s: the guest reports the identity's address", who)
	assert.Equal(t, id.MachineID, report.MachineID, "%s: the guest reports the identity's machine id", who)
	assert.Equal(t, id.Hostname, report.Hostname)
	assert.NotEmpty(t, report.BootID, "%s: the clone reports a boot identity of its own", who)
	assert.Contains(t, report.Changed, agent.PartMAC, "%s: the MAC was one of the parts applied", who)
	assert.Contains(t, report.Changed, agent.PartAddress, "%s: and so was the address", who)
	assert.Contains(t, report.Changed, agent.PartBootID)
}

// assertLaunchLeftNothing proves a launch that failed took everything it created with
// it: no jail, no cgroup and no console socket. A failed launch hands the caller no
// handle, so nothing else could clean up after it.
func assertLaunchLeftNothing(t *testing.T, launcher snapshot.JailedLauncher, id string) {
	t.Helper()

	assert.NoDirExists(t, launcher.JailRoot(id), "a failed launch leaves no jail behind")
	assert.NoDirExists(t, filepath.Join("/sys/fs/cgroup", launcher.ParentCgroup, id),
		"a failed launch leaves no cgroup behind")
	assert.NoFileExists(t, filepath.Join(launcher.ConsoleSocketDir, id+".console.sock"))
}

func parseUnix(t *testing.T, value string) int64 {
	t.Helper()

	var seconds int64
	_, err := fmt.Sscan(strings.TrimSpace(value), &seconds)
	require.NoError(t, err, "the guest must report its clock as a unix timestamp, got %q", value)
	return seconds
}

// randomReadHost is the host's entropy source for the identities this test mints.
func randomReadHost(b []byte) (int, error) {
	f, err := os.Open("/dev/urandom")
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	return f.Read(b)
}
