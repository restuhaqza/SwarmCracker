package snapshot

// The instance record a launch publishes (SBX-025), held to its rules without a VMM:
// what the caller's identity plus the launcher's facts add up to, that a launcher
// overwrites the fields it owns rather than trusting the caller's, that nothing is
// guessed, and that a record which cannot be written fails the launch instead of
// running a VM nothing can resolve.

import (
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// fakeRegistry is a registry a unit test can hold: the writes a launch made and the
// ids a teardown deleted. Get answers like the real store — a record whose VMM is gone
// is not returned — because the teardown asks that question first and a fake that
// answered differently would test a rule the launcher does not have.
type fakeRegistry struct {
	records map[string]vminstance.Instance
	puts    []vminstance.Instance
	deletes []string
	putErr  error
	// putErrOn makes the putErr refuse the Nth write (1-based) instead of every
	// one, which is how a test tells a launch that could not be recorded from one
	// whose record could not be marked ready.
	putErrOn int
	// deleteErr makes the delete fail, which is a teardown that could not finish.
	deleteErr error
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{records: map[string]vminstance.Instance{}}
}

func (f *fakeRegistry) Put(inst vminstance.Instance) error {
	if f.putErr != nil && (f.putErrOn == 0 || f.putErrOn == len(f.puts)+1) {
		return f.putErr
	}
	f.puts = append(f.puts, inst)
	f.records[inst.ID] = inst
	return nil
}

func (f *fakeRegistry) Get(id string) (vminstance.Instance, bool, error) {
	inst, found := f.records[id]
	if !found || !processAlive(inst.PID) {
		return vminstance.Instance{}, false, nil
	}
	return inst, true, nil
}

func (f *fakeRegistry) Peek(id string) (vminstance.Instance, bool, error) {
	inst, found := f.records[id]
	return inst, found, nil
}

func (f *fakeRegistry) Delete(id string) error {
	if _, found := f.records[id]; !found {
		return nil
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.records, id)
	f.deletes = append(f.deletes, id)
	return nil
}

// TestLaunchRecordCompletesTheCallersIdentity pins who owns which field: the caller's
// half is recorded as given, the launcher's half is written by the launcher, and the
// namespace is the launcher's answer because the launcher is what puts the VMM in one.
func TestLaunchRecordCompletesTheCallersIdentity(t *testing.T) {
	booted := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	req := LaunchRequest{
		InstanceID: "sbx-1a2b3c",
		NetNS:      "sbx-1a2b3c",
		Record: vminstance.Instance{
			Kind:    vminstance.KindSandbox,
			OwnerID: "user-7",
			NodeID:  "node-a",
			TAP:     "tap-sbx1a2b3c",
			GuestIP: "10.42.0.7",
			MAC:     "02:00:0a:2a:00:07",
			// Fields the launcher owns: whatever the caller put there must be
			// replaced, because it cannot know them.
			PID:       1,
			APISocket: "/caller/guessed.sock",
			VsockPath: "/caller/guessed.vsock",
			Console:   "/caller/guessed.log",
			JailUID:   4242,
			Ready:     true,
		},
	}

	record, err := launchRecord(req, launchFacts{
		pid:       5150,
		apiSocket: "/jail/root/run/firecracker/sbx-1a2b3c.sock",
		vsockPath: "/jail/root/run/firecracker/agent.vsock",
		console:   "/logs/sbx-1a2b3c.log",
		jailUID:   1000,
		netns:     "/var/run/netns/sbx-1a2b3c",
	}, booted)

	require.NoError(t, err)
	assert.Equal(t, "sbx-1a2b3c", record.ID, "an empty record ID is the instance being launched")
	assert.Equal(t, vminstance.KindSandbox, record.Kind)
	assert.Equal(t, "user-7", record.OwnerID)
	assert.Equal(t, "node-a", record.NodeID)
	assert.Equal(t, "tap-sbx1a2b3c", record.TAP, "the caller's network identity is recorded as given")
	assert.Equal(t, "10.42.0.7", record.GuestIP)
	assert.Equal(t, "02:00:0a:2a:00:07", record.MAC)
	assert.Equal(t, 5150, record.PID)
	assert.Equal(t, "/jail/root/run/firecracker/sbx-1a2b3c.sock", record.APISocket)
	assert.Equal(t, "/jail/root/run/firecracker/agent.vsock", record.VsockPath)
	assert.Equal(t, "/logs/sbx-1a2b3c.log", record.Console)
	assert.Equal(t, 1000, record.JailUID)
	assert.Equal(t, "/var/run/netns/sbx-1a2b3c", record.NetNS,
		"the namespace is the one the launcher put the VMM in, not the string the caller passed")
	assert.Equal(t, booted, record.Booted)
	assert.False(t, record.Ready, "an instance is recorded before its guest has answered")
}

// TestLaunchRecordLeavesWhatNobodyKnowsEmpty pins the rule that a record is not a place
// to be helpful: a field the caller did not fill and the launcher cannot observe stays
// empty, because a reader reads an empty field as "this instance has none".
func TestLaunchRecordLeavesWhatNobodyKnowsEmpty(t *testing.T) {
	req := LaunchRequest{
		InstanceID: "sbx-1a2b3c",
		Record:     vminstance.Instance{Kind: vminstance.KindSandbox},
	}

	record, err := launchRecord(req, launchFacts{pid: 4242}, time.Now().UTC())

	require.NoError(t, err)
	assert.Empty(t, record.TAP, "a cold boot with no NIC has no TAP to name")
	assert.Empty(t, record.GuestIP)
	assert.Empty(t, record.MAC)
	assert.Empty(t, record.NetNS, "a launcher with no jail has no namespace of its own to report")
	assert.Empty(t, record.Console)
	assert.Equal(t, 0, record.JailUID, "an unjailed instance runs as the launcher's own user")
}

// TestLaunchRecordRefusesADifferentName pins the one contradiction a record cannot
// carry: a name that is not the instance being launched. A reader resolves an ID to
// an instance, so a record that answers to another name is a lookup that finds
// nothing — and writing it would hide the caller's mistake.
func TestLaunchRecordRefusesADifferentName(t *testing.T) {
	req := LaunchRequest{
		InstanceID: "sbx-1a2b3c",
		Record:     vminstance.Instance{ID: "sbx-other", Kind: vminstance.KindSandbox},
	}

	_, err := launchRecord(req, launchFacts{pid: 4242}, time.Now().UTC())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "sbx-other")
	assert.Contains(t, err.Error(), "sbx-1a2b3c")
}

// TestPublishRecordFailsWhenTheRegistryRefusesIt pins the fail-closed rule the VMM
// managers already have: a node that keeps a registry does not run a VM it cannot
// record, because nothing else knows the paths it chose.
func TestPublishRecordFailsWhenTheRegistryRefusesIt(t *testing.T) {
	registry := newFakeRegistry()
	registry.putErr = errors.New("state directory is full")

	err := publishRecord(registry, vminstance.Instance{ID: "sbx-1a2b3c"})

	require.Error(t, err)
	assert.ErrorIs(t, err, registry.putErr)
	assert.Contains(t, err.Error(), "state directory is full")
}

// TestReleaseRecordKeepsTheRecordWhenTheTeardownCannotFinish pins the registry's rule
// at the launcher's door: a teardown that could not free what the record names, or could
// not delete it, leaves the record in place, because the record is the only handle the
// next attempt has — and a stop that dropped it would leave the netns and TAP device
// with nobody to free them.
func TestReleaseRecordKeepsTheRecordWhenTheTeardownCannotFinish(t *testing.T) {
	registry := newFakeRegistry()
	registry.deleteErr = errors.New("read-only file system")

	// A pid no process has: the teardown's first question is whether the VM is still
	// running, and a record whose VMM is alive is refused there rather than deleted.
	dead := exec.Command("true")
	require.NoError(t, dead.Run())

	record := vminstance.Instance{
		ID:        "sbx-1a2b3c",
		Kind:      vminstance.KindSandbox,
		PID:       dead.Process.Pid,
		APISocket: "/run/firecracker/sbx-1a2b3c.sock",
		Booted:    time.Now().UTC(),
	}
	require.NoError(t, registry.Put(record))

	releaseRecord(registry, record.ID)

	assert.Contains(t, registry.records, record.ID, "the record is the handle a retry needs")
	assert.Empty(t, registry.deletes)
}

// TestPublishAndMarkRecordWithoutARegistryAreNoOps pins that a launcher which keeps no
// registry (a dev run, a unit test) is not a special case at its call sites.
func TestPublishAndMarkRecordWithoutARegistryAreNoOps(t *testing.T) {
	record := vminstance.Instance{ID: "sbx-1a2b3c"}

	require.NoError(t, publishRecord(nil, record))
	markRecordReady(nil, record)
	releaseRecord(nil, record.ID)
}
