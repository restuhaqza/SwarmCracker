package netpolicy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// execRecorder is the Exec seam a unit test holds: it records every argv a
// manager would have run, and answers from reply. A nil reply answers success
// with no output, which is what most probe commands want.
type execRecorder struct {
	mu      sync.Mutex
	calls   [][]string
	replied []string
	reply   func(argv []string) ([]byte, error)
}

func (r *execRecorder) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	argv := append([]string{name}, args...)
	r.mu.Lock()
	r.calls = append(r.calls, argv)
	r.mu.Unlock()

	if r.reply == nil {
		return nil, nil
	}
	out, err := r.reply(argv)
	if err != nil {
		r.replied = append(r.replied, strings.Join(argv, " "))
	}
	return out, err
}

// ran reports every command whose argv contains every fragment.
func (r *execRecorder) ran(fragments ...string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, argv := range r.calls {
		joined := strings.Join(argv, " ")
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(joined, fragment) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, joined)
		}
	}
	return out
}

// sequence is every command in the order it ran.
func (r *execRecorder) sequence() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.calls))
	for _, argv := range r.calls {
		out = append(out, strings.Join(argv, " "))
	}
	return out
}

// indexOf is where a command sits in a recorded sequence, so a test can assert an
// order rather than only the presence of each step.
func indexOf(sequence []string, command string) int {
	for i, ran := range sequence {
		if ran == command {
			return i
		}
	}
	return -1
}

// testBinaries is a LookPath that finds both tools wherever they are, so a test
// exercises the manager rather than this machine's PATH.
func testBinaries(t *testing.T) func(string) (string, error) {
	t.Helper()
	return func(name string) (string, error) {
		switch name {
		case "ip":
			return "/usr/bin/ip", nil
		case "nft":
			return "/usr/sbin/nft", nil
		default:
			return exec.LookPath(name)
		}
	}
}

// newTestManager returns a manager whose commands are recorded and whose node
// already uses nothing, so an allocation is a function of the pool alone.
func newTestManager(t *testing.T, recorder *execRecorder) *Manager {
	t.Helper()
	m, err := New(Config{
		Exec:      recorder.exec,
		LookPath:  testBinaries(t),
		Occupancy: func(context.Context) ([]netip.Prefix, error) { return nil, nil },
	})
	require.NoError(t, err)
	return m
}

// TestAttachConfiguresTheNamespaceThenThePolicy holds the order to account. The
// policy has to be the last thing an attach does: a sandbox described as
// allocated while it is still open is the failure this package exists to prevent,
// and a policy applied before the addresses would filter a namespace whose
// gateway does not exist yet.
func TestAttachConfiguresTheNamespaceThenThePolicy(t *testing.T) {
	recorder := &execRecorder{reply: func(argv []string) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "link show") {
			return nil, errors.New("Device \"tap0\" does not exist.")
		}
		return nil, nil
	}}
	m := newTestManager(t, recorder)

	n, err := m.Attach(context.Background(), "alpha")
	require.NoError(t, err)

	assert.Equal(t, "sbx-alpha", n.Name)
	assert.Equal(t, "/var/run/netns/sbx-alpha", n.NetNS)
	assert.Equal(t, "tap0", n.Tap)
	assert.Equal(t, "10.63.0.1", n.Gateway.String())
	assert.Equal(t, "10.63.0.2", n.GuestIP.String())
	assert.Equal(t, "10.63.0.0/30", n.CIDR.String())

	sequence := recorder.sequence()
	require.NotEmpty(t, sequence)

	assert.Equal(t, "/usr/bin/ip netns list", sequence[0], "the node's namespaces are read before one is created")

	want := []string{
		"tuntap add dev tap0 mode tap",
		"addr replace 10.63.0.1/30 dev tap0",
		"link set tap0 up",
	}
	positions := make([]int, 0, len(want))
	for _, fragment := range want {
		ran := recorder.ran(fragment)
		require.Len(t, ran, 1, "expected exactly one %q, got %v", fragment, ran)
		positions = append(positions, indexOf(sequence, ran[0]))
	}
	assert.Less(t, positions[0], positions[1], "the device exists before it is addressed")
	assert.Less(t, positions[1], positions[2], "it is addressed before it is raised")

	policyRun := recorder.ran("nft", "-f")
	require.Len(t, policyRun, 1, "one policy application, got %v", policyRun)
	assert.Equal(t, len(sequence)-1, indexOf(sequence, policyRun[0]), "the policy is applied last")

	for _, argv := range recorder.sequence() {
		if strings.Contains(argv, "tap0") || strings.Contains(argv, "lo up") {
			assert.Contains(t, argv, "netns exec sbx-alpha",
				"every device this package configures is a device of the sandbox's namespace")
		}
	}
	assert.Contains(t, policyRun[0], "netns exec sbx-alpha",
		"the policy is installed in the namespace it filters, not on the node")
}

// TestAttachReusesWhatIsAlreadyThere is the retried attach: a namespace and a
// device that a previous attempt left are this sandbox's, and recreating either
// is impossible (`ip netns add` and `ip tuntap add` both refuse a name that
// exists). Only the address, the device's state and the policy are re-applied.
func TestAttachReusesWhatIsAlreadyThere(t *testing.T) {
	recorder := &execRecorder{reply: func(argv []string) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "netns list"):
			return []byte("sbx-alpha\n"), nil
		default:
			return nil, nil // every probe succeeds: the device is already there
		}
	}}
	m := newTestManager(t, recorder)

	_, err := m.Attach(context.Background(), "alpha")
	require.NoError(t, err)

	assert.Empty(t, recorder.ran("netns add"), "an existing namespace is the sandbox's own")
	assert.Empty(t, recorder.ran("tuntap add"), "an existing device is the sandbox's own")
	assert.Len(t, recorder.ran("addr replace"), 1, "the address is still asserted")
	assert.Len(t, recorder.ran("nft", "-f"), 1, "and the policy is still applied")
}

// TestAttachKeepsASandboxsAddressAndMACStable: two attaches of one sandbox are one
// identity. A guest is wearing the address, so an attach that moved it would leave
// the guest answering on an address its own network does not have.
func TestAttachKeepsASandboxAddressAndMACStable(t *testing.T) {
	first := &execRecorder{}
	m := newTestManager(t, first)
	before, err := m.Attach(context.Background(), "alpha")
	require.NoError(t, err)

	// The second attach meets the namespace the first one created, as a retried
	// attach on the node does.
	second := &execRecorder{reply: func(argv []string) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "netns list") {
			return []byte("sbx-alpha\n"), nil
		}
		return nil, nil
	}}
	m.exec = second.exec
	after, err := m.Attach(context.Background(), "alpha")
	require.NoError(t, err)

	assert.Equal(t, before.CIDR, after.CIDR)
	assert.Equal(t, before.GuestIP, after.GuestIP)
	assert.Equal(t, before.MAC, after.MAC)
	assert.Equal(t, before.NetNS, after.NetNS)
	assert.Empty(t, second.ran("netns add"), "the namespace was already this sandbox's")
}

// TestAttachLeavesNothingBehindWhenThePolicyFails: a sandbox that cannot be
// denied must not be left half-created, and its address must go back to the pool
// so the next attach starts where this one did.
func TestAttachLeavesNothingBehindWhenThePolicyFails(t *testing.T) {
	recorder := &execRecorder{reply: func(argv []string) ([]byte, error) {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "nft") {
			return []byte("nft: command not found"), errors.New("exit status 127")
		}
		return nil, nil
	}}
	m := newTestManager(t, recorder)

	_, err := m.Attach(context.Background(), "alpha")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "default-deny policy")

	assert.Len(t, recorder.ran("netns delete sbx-alpha"), 1, "the namespace this call created is removed again")

	next, _, err := m.reserve(context.Background(), "beta")
	require.NoError(t, err)
	assert.Equal(t, "10.63.0.0/30", next.String(), "the address the failed attach held is back in the pool")
}

// TestAFailedRetryKeepsAnAttachedSandboxsAddress is the other half of that rule, and
// the reason it exists: a guest is wearing its address, so a retry that fails must
// give back nothing but what it took. An attach that released the address here would
// hand the sandbox's /30 to the next sandbox and leave this guest answering on an
// address its own network no longer has.
func TestAFailedRetryKeepsAnAttachedSandboxsAddress(t *testing.T) {
	// The first attach succeeds and takes 10.63.0.0/30.
	first := &execRecorder{reply: func(argv []string) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "netns list") {
			return []byte("sbx-alpha\n"), nil
		}
		return nil, nil
	}}
	m := newTestManager(t, first)
	before, err := m.Attach(context.Background(), "alpha")
	require.NoError(t, err)
	require.Equal(t, "10.63.0.0/30", before.CIDR.String())

	// The retry meets the attached sandbox and fails at the policy.
	failing := &execRecorder{reply: func(argv []string) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "netns list"):
			return []byte("sbx-alpha\n"), nil
		case strings.Contains(joined, "nft"):
			return nil, errors.New("nft failed")
		default:
			return nil, nil
		}
	}}
	m.exec = failing.exec

	after, err := m.Attach(context.Background(), "alpha")
	require.Error(t, err)
	assert.False(t, after.CIDR.IsValid(), "a failed attach returns no network")
	assert.Empty(t, failing.ran("netns delete"), "and removes nothing it did not create")

	// The address is still this sandbox's, and the next sandbox gets a different one.
	held, took, err := m.reserve(context.Background(), "alpha")
	require.NoError(t, err)
	assert.Equal(t, before.CIDR, held)
	assert.False(t, took, "the retry took nothing, so it gives nothing back")

	other, _, err := m.reserve(context.Background(), "beta")
	require.NoError(t, err)
	assert.Equal(t, "10.63.0.4/30", other.String(), "the next sandbox does not get the address this one wears")
}

// TestAttachLeavesAForeignNamespaceAlone: a namespace that was already there is
// not this call's to remove. An attach that removed it would tear down a sandbox
// that is still running around a guest.
func TestAttachLeavesAForeignNamespaceAlone(t *testing.T) {
	recorder := &execRecorder{reply: func(argv []string) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "netns list"):
			return []byte("sbx-alpha\n"), nil
		case strings.Contains(joined, "nft"):
			return nil, errors.New("nft failed")
		default:
			return nil, nil
		}
	}}
	m := newTestManager(t, recorder)

	_, err := m.Attach(context.Background(), "alpha")
	require.Error(t, err)
	assert.Empty(t, recorder.ran("netns delete"), "a namespace this call did not create is not removed")
}

// TestAttachRefusesAnIDThatCannotNameANamespace: the ID becomes the namespace name
// and the record key, so the node's own rule for "this string can name a file"
// applies before anything is created.
func TestAttachRefusesAnIDThatCannotNameANamespace(t *testing.T) {
	recorder := &execRecorder{}
	m := newTestManager(t, recorder)

	for _, id := range []string{"", "a/b", "a.b", "a b"} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			_, err := m.Attach(context.Background(), id)
			require.Error(t, err)
		})
	}
	assert.Empty(t, recorder.sequence(), "nothing was created for a refused ID")
}

// TestDetachIsIdempotent: a sandbox whose namespace is gone, and one that never
// had one, are both success. The release is what a teardown retries after a crash.
func TestDetachIsIdempotent(t *testing.T) {
	m := newTestManager(t, &execRecorder{})

	require.NoError(t, m.Detach(context.Background(), "never-attached"))
	require.NoError(t, m.Detach(context.Background(), "never-attached"))
	require.Error(t, m.Detach(context.Background(), "a.b"))
}

// TestIdentityIsTheAllocation: what the guest is told to wear and what the node
// allocated are the same addresses, which is the whole point of building the
// identity here instead of at each call site.
func TestIdentityIsTheAllocation(t *testing.T) {
	recorder := &execRecorder{}
	m := newTestManager(t, recorder)
	n, err := m.Attach(context.Background(), "clone-1")
	require.NoError(t, err)

	id := n.Identity()
	assert.Equal(t, GuestInterface, id.Interface)
	assert.Equal(t, n.MAC, id.MAC)
	assert.Equal(t, n.GuestIP.String()+"/30", id.Address)
	assert.Equal(t, n.Gateway.String(), id.Gateway)
	assert.Equal(t, []string{n.Gateway.String()}, id.DNS)
	require.NoError(t, id.Validate(), "the identity the allocation builds is one a guest can apply")

	iface, tap, mac := n.Interface()
	assert.Equal(t, GuestInterface, iface)
	assert.Equal(t, TapName, tap)
	assert.Equal(t, n.MAC, mac)
}

// TestMACIsLocallyAdministeredUniqueAndStable: the link address has to be one a
// guest may wear (unicast, locally administered), different for two sandboxes, and
// the same for one sandbox across restarts.
func TestMACIsLocallyAdministeredUniqueAndStable(t *testing.T) {
	seen := make(map[string]string)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("sbx-%d", i)
		mac := macFor(id)
		assert.Equal(t, mac, macFor(id), "the same sandbox keeps its link address")

		first, err := strconv.ParseUint(mac[:2], 16, 8)
		require.NoError(t, err, "mac %s does not start with an octet", mac)
		assert.Zero(t, first&0x01, "mac %s is a group address: an interface cannot carry it", mac)
		assert.NotZero(t, first&0x02, "mac %s is globally administered, and this node has no OUI to wear", mac)

		other, ok := seen[mac]
		assert.False(t, ok, "mac %s is shared by %s and %s", mac, other, id)
		seen[mac] = id
	}
}
