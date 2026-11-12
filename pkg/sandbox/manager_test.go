package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/golden"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox/netpolicy"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// The fakes. Each holds the order it was called in rather than only the fact, because
// a create's correctness is the order of its steps: a disk copied before the network
// exists is the same sandbox, but a VM launched before its disk or its token exists is
// not.

type fakeLauncher struct {
	requests []snapshot.LaunchRequest
	launched snapshot.Launched
	jailRoot string
	err      error
	// onStart runs inside Start, before the launch is answered. A test uses it to
	// look at the world at the moment the VM is asked to start, which is the moment
	// the resources it is about to use have to be recorded.
	onStart func()
}

func (f *fakeLauncher) Start(_ context.Context, req snapshot.LaunchRequest) (snapshot.Launched, error) {
	f.requests = append(f.requests, req)
	if f.onStart != nil {
		f.onStart()
	}
	if f.err != nil {
		return snapshot.Launched{}, f.err
	}
	return f.launched, nil
}

func (f *fakeLauncher) JailRoot(id string) string {
	if f.jailRoot == "" {
		return ""
	}
	return filepath.Join(f.jailRoot, id)
}

type fakeNetwork struct {
	network   netpolicy.Network
	attached  []string
	detached  []string
	attachErr error
	detachErr error
}

func (f *fakeNetwork) Attach(_ context.Context, id string, _ []string) (netpolicy.Network, error) {
	f.attached = append(f.attached, id)
	if f.attachErr != nil {
		return netpolicy.Network{}, f.attachErr
	}
	return f.network, nil
}

func (f *fakeNetwork) Detach(_ context.Context, id string) error {
	f.detached = append(f.detached, id)
	return f.detachErr
}

// fakeInstances is the node's instance registry as a map. It keeps the registry's
// contract where it matters to a teardown: Get refuses a record whose process is gone,
// Peek returns it, and the two disagree exactly when the VM is dead.
type fakeInstances struct {
	records map[string]vminstance.Instance
	alive   func(pid int) bool
	readErr error
}

func (f *fakeInstances) Put(inst vminstance.Instance) error {
	if f.records == nil {
		f.records = map[string]vminstance.Instance{}
	}
	f.records[inst.ID] = inst
	return nil
}

func (f *fakeInstances) Get(id string) (vminstance.Instance, bool, error) {
	if f.readErr != nil {
		return vminstance.Instance{}, false, f.readErr
	}
	inst, found := f.records[id]
	if !found {
		return vminstance.Instance{}, false, nil
	}
	if f.alive != nil && !f.alive(inst.PID) {
		return vminstance.Instance{}, false, nil
	}
	return inst, true, nil
}

func (f *fakeInstances) Peek(id string) (vminstance.Instance, bool, error) {
	inst, found := f.records[id]
	return inst, found, nil
}

func (f *fakeInstances) Delete(id string) error {
	delete(f.records, id)
	return nil
}

// fixture is one node's worth of paths and fakes: a template store with a real ext4
// artifact, a fake kernel and Firecracker binary (so the pin check has real inputs to
// check), and the seams a create goes through.
type fixture struct {
	manager   *Manager
	store     *Store
	launcher  *fakeLauncher
	network   *fakeNetwork
	instances *fakeInstances
	rootfsDir string
	now       time.Time
	clock     *testClock
}

// testClock is the node's clock as a fixture: a test that needs time to pass advances it
// instead of sleeping, and one that compares two timestamps has a value to compare with.
// It is safe for concurrent use because the reaper's loop reads it from its own
// goroutine while a test advances it.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	// The pin check hashes a kernel and reads a Firecracker version, so both have to
	// exist: a fake binary that prints a version and a file whose digest the template
	// records.
	tmp := t.TempDir()
	kernel := filepath.Join(tmp, "vmlinux")
	require.NoError(t, os.WriteFile(kernel, []byte("a kernel image"), 0o600))
	sum := sha256.Sum256([]byte("a kernel image"))

	firecracker := filepath.Join(tmp, "firecracker")
	require.NoError(t, os.WriteFile(firecracker, []byte("#!/bin/sh\necho 'Firecracker vTEST'\n"), 0o700))

	// The artifact is a real ext4 image: the token is injected into it with debugfs,
	// which is the step that makes a sandbox's guest require the credential, and a
	// file that is not a filesystem cannot hold one.
	goldenDir := filepath.Join(tmp, "golden")
	require.NoError(t, os.MkdirAll(goldenDir, 0o755))
	artifact := filepath.Join(goldenDir, "golden-base-1.0.0.ext4")
	require.NoError(t, mke2fs(t, artifact))

	metadata := map[string]any{
		"apiVersion":    "swarmcracker.io/v1alpha1",
		"kind":          "GoldenImage",
		"name":          "base",
		"version":       "1.0.0",
		"artifact":      filepath.Base(artifact),
		"kernelProfile": "guest-runtime-6.1",
		"pin":           map[string]string{"firecracker": "vTEST", "kernel": hex.EncodeToString(sum[:])},
		"agent": map[string]any{
			"agentProtocol": agent.Version,
			"agentVersion":  "test-agent",
			"agentCaps":     golden.RequiredAgentCaps,
		},
	}
	data, err := json.Marshal(metadata)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(goldenDir, "golden-base-1.0.0.json"), data, 0o600))

	store := newTestStore(t)
	instances := &fakeInstances{records: map[string]vminstance.Instance{}}
	prefix, err := netip.ParsePrefix("10.63.0.0/30")
	require.NoError(t, err)
	network := &fakeNetwork{network: netpolicy.Network{
		ID: "sbx-1", Name: "sbx-sbx-1", NetNS: "/var/run/netns/sbx-sbx-1", Tap: "tap0",
		CIDR: prefix, Gateway: netip.MustParseAddr("10.63.0.1"),
		GuestIP: netip.MustParseAddr("10.63.0.2"), MAC: "02:00:00:00:00:01",
	}}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	clock := &testClock{t: now}
	launcher := &fakeLauncher{
		launched: snapshot.Launched{
			APISocket: filepath.Join(tmp, "run", "sbx-1.sock"),
			VsockPath: filepath.Join(tmp, "run", "sbx-1.vsock"),
			Hello:     agent.Hello{AgentVersion: "test-agent", BootID: "boot-test"},
		},
		jailRoot: filepath.Join(tmp, "jailer"),
	}

	manager, err := New(Config{
		Store:             store,
		GoldenDir:         goldenDir,
		KernelPath:        kernel,
		KernelProfiles:    map[string]string{}, // no built-in mapping: the fallback kernel is the fixture's
		FirecrackerBinary: firecracker,
		RootfsDir:         filepath.Join(tmp, "rootfs"),
		Launcher:          launcher,
		Network:           network,
		Instances:         instances,
		VCPUs:             1,
		MemoryMB:          256,
		Now:               clock.Now,
	})
	require.NoError(t, err)

	return &fixture{
		manager: manager, store: store, launcher: launcher, network: network,
		instances: instances, rootfsDir: filepath.Join(tmp, "rootfs"), now: now, clock: clock,
	}
}

// mke2fs writes the smallest ext4 image the token injector can hold a file in.
func mke2fs(t *testing.T, path string) error {
	t.Helper()
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("mke2fs is not installed (e2fsprogs): the token-in-image path cannot be exercised")
	}
	out, err := exec.Command("mke2fs", "-q", "-t", "ext4", "-F", path, "8M").CombinedOutput()
	if err != nil {
		t.Fatalf("mke2fs %s: %v: %s", path, err, out)
	}
	return nil
}

// TestCreateBuildsTheSandboxInTheOrderItPromises holds the create to the sequence its
// doc comment states: template resolved and pinned, record written, network attached,
// disk materialized, token written into the disk, VM launched with an identity, record
// ready with what the guest answered.
func TestCreateBuildsTheSandboxInTheOrderItPromises(t *testing.T) {
	f := newFixture(t)

	rec, err := f.manager.Create(context.Background(), CreateRequest{
		Template: "base", ID: "sbx-1", Owner: "test", TTL: time.Minute,
	})
	require.NoError(t, err)

	assert.Equal(t, StateReady, rec.State)
	assert.Equal(t, "base@1.0.0", rec.Template)
	assert.Equal(t, "boot-test", rec.BootID)
	assert.Equal(t, "test-agent", rec.AgentVersion)
	require.False(t, rec.ReadyAt.IsZero(), "ready is a measurement, so it has a timestamp")

	require.Len(t, f.launcher.requests, 1)
	req := f.launcher.requests[0]

	assert.Equal(t, "sbx-1", req.InstanceID)
	assert.Equal(t, snapshot.ModeCold, req.Mode)
	assert.Contains(t, req.Kernel, "vmlinux")
	assert.Equal(t, f.manager.diskPath("sbx-1"), req.Rootfs)
	assert.Equal(t, "/run/firecracker/sbx-1.vsock", req.VsockPath, "a jailed launch is told the path the instance sees")
	assert.Equal(t, "/var/run/netns/sbx-sbx-1", req.NetNS)
	assert.Equal(t, []string{"sbx-1"}, f.network.attached, "the network is attached before the VM is launched")
	require.Len(t, req.Net, 1)
	assert.Equal(t, "tap0", req.Net[0].HostTap)
	assert.Equal(t, "eth0", req.Net[0].IfaceID)

	// The identity makes the guest a machine of its own, and the token makes it this
	// boot's: both are what the launch carries, and the guest adopts the first.
	require.NotNil(t, req.Identity)
	assert.Equal(t, "sandbox-sbx-1", req.Identity.Hostname)
	assert.Equal(t, "10.63.0.2/30", req.Identity.Address)
	assert.Equal(t, "10.63.0.1", req.Identity.Gateway)
	assert.Equal(t, req.AgentToken, req.Identity.Token)
	assert.NotEmpty(t, req.Identity.MachineID, "two clones must never share a machine id")
	assert.NotEmpty(t, req.Identity.Entropy, "a restored guest's entropy pool is a copy")

	// The instance record the launcher publishes names the sandbox's own resources.
	assert.Equal(t, vminstance.KindSandbox, req.Record.Kind)
	assert.Equal(t, "sbx-1", req.Record.ID)
	assert.Equal(t, "tap0", req.Record.TAP)
	assert.Equal(t, f.manager.diskPath("sbx-1"), req.Record.Disk)

	// The disk is the sandbox's own, and the token is inside it: the guest requires
	// the credential from its first hello because the file is already there.
	disk := f.manager.diskPath("sbx-1")
	info, statErr := os.Stat(disk)
	require.NoError(t, statErr, "the per-instance disk must exist")
	assert.Greater(t, info.Size(), int64(0), "the disk is the template's image, copied")

	injected, err := agent.ReadTokenFromRootfs(context.Background(), disk)
	require.NoError(t, err)
	assert.Equal(t, req.AgentToken, injected, "the disk carries this boot's token")

	// The host's copy sits beside the muxer and is the sandbox's credential for exec.
	hostToken, found, err := agent.ReadTokenFile(agent.TokenFilePath(f.launcher.launched.VsockPath))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, req.AgentToken, hostToken)
	assert.Equal(t, agent.TokenFilePath(f.launcher.launched.VsockPath), rec.AgentTokenFile)

	// The disk copy is reported, because a filesystem without reflink is otherwise
	// invisible.
	assert.NotEmpty(t, rec.DiskCopy)
}

// TestCreateRecordsEachResourceBeforeItIsUsed holds the create to the rule that makes
// a crashed create recoverable: every resource the node allocates is in the record
// before the next step runs, so a teardown started from that record frees what is
// really there — the namespace, the disk and the VM.
func TestCreateRecordsEachResourceBeforeItIsUsed(t *testing.T) {
	f := newFixture(t)

	var atLaunch Record
	var found bool
	var err error
	f.launcher.onStart = func() {
		atLaunch, found, err = f.store.Get("sbx-1")
	}

	rec, createErr := f.manager.Create(context.Background(), CreateRequest{Template: "base", ID: "sbx-1"})
	require.NoError(t, createErr)
	require.NoError(t, err)
	require.True(t, found, "the record must exist before the VM is launched")

	assert.Equal(t, StateCreated, atLaunch.State, "a sandbox whose guest has not answered yet is created, not ready")
	assert.Equal(t, f.manager.diskPath("sbx-1"), atLaunch.Disk, "the disk is recorded before the VM that boots from it")
	assert.Equal(t, f.network.network.NetNS, atLaunch.NetNS, "the namespace is recorded before the VM that runs in it")
	assert.Equal(t, "10.63.0.2", atLaunch.GuestIP)
	assert.Equal(t, rec.ID, atLaunch.ID)

	// And the record the create returns is the ready one, with what the guest said,
	// while the record the VM was launched from was still being built.
	assert.Equal(t, StateReady, rec.State)
	assert.False(t, rec.ReadyAt.IsZero(), "the ready record says when the guest answered")
	assert.True(t, atLaunch.ReadyAt.IsZero(), "the record the VM was launched from was not ready yet")
}

// TestCreateFailureLeavesAFailedRecordAndNothingElse holds the failure path to its
// promise: the sandbox is visible as failed with the reason, and everything the
// attempt allocated is freed.
func TestCreateFailureLeavesAFailedRecordAndNothingElse(t *testing.T) {
	f := newFixture(t)
	f.launcher.err = fmt.Errorf("the guest never answered hello")

	_, err := f.manager.Create(context.Background(), CreateRequest{Template: "base", ID: "sbx-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never answered")

	stored, found, storeErr := f.store.Get("sbx-1")
	require.NoError(t, storeErr)
	require.True(t, found, "a failed create keeps its record: it is the answer to what happened")
	assert.Equal(t, StateFailed, stored.State)
	assert.Contains(t, stored.Reason, "never answered hello")
	assert.Empty(t, stored.Disk, "a failed sandbox names no resources it no longer holds")
	assert.Empty(t, stored.JailRoot)
	assert.Equal(t, []string{"sbx-1"}, f.network.detached, "the failed attempt's network is detached")

	// The disk the attempt copied is gone.
	_, statErr := os.Stat(f.manager.diskPath("sbx-1"))
	assert.True(t, os.IsNotExist(statErr), "the failed attempt's disk is freed")

	// The sandbox that remains is not usable, and exec says why.
	_, execErr := f.manager.Exec(context.Background(), "sbx-1", []string{"true"}, ExecOptions{})
	require.Error(t, execErr)
	assert.ErrorIs(t, execErr, ErrNotReady)
	assert.Contains(t, execErr.Error(), "never answered hello")
}

// TestCreateRefusesATemplateThisNodeWasNotPinnedTo pins the gate the whole template
// path exists for: a template built by other host software is refused before a disk
// or a namespace is created.
func TestCreateRefusesATemplateThisNodeWasNotPinnedTo(t *testing.T) {
	f := newFixture(t)

	// Move the host's Firecracker under the template's pin.
	otherFirecracker := filepath.Join(t.TempDir(), "firecracker")
	require.NoError(t, os.WriteFile(otherFirecracker, []byte("#!/bin/sh\necho 'Firecracker vOTHER'\n"), 0o700))
	f.manager.cfg.FirecrackerBinary = otherFirecracker

	_, err := f.manager.Create(context.Background(), CreateRequest{Template: "base", ID: "sbx-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), golden.CodeTemplatePinMismatch)

	// Nothing was allocated: the refusal happens before the first step that touches
	// the node.
	assert.Empty(t, f.network.attached)
	assert.Empty(t, f.launcher.requests)
	_, found, storeErr := f.store.Get("sbx-1")
	require.NoError(t, storeErr)
	assert.False(t, found, "a refused template leaves no record")
}

// TestRemoveStopsARunningSandboxAndFreesEverything holds rm to its promise against a
// real process: the VMM is stopped, and the socket, cgroup, jail, namespace, disk,
// token and records are gone.
func TestRemoveStopsARunningSandboxAndFreesEverything(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// A process that is really running stands in for the VMM, so the teardown's stop
	// step is exercised against the kernel rather than against a mock.
	proc := exec.Command("sleep", "60")
	require.NoError(t, proc.Start())
	t.Cleanup(func() { _ = proc.Process.Kill() })

	tmp := t.TempDir()
	disk := filepath.Join(tmp, "sbx-1.ext4")
	require.NoError(t, os.WriteFile(disk, []byte("disk"), 0o600))
	tokenFile := filepath.Join(tmp, "sbx-1.agent-token")
	require.NoError(t, agent.WriteTokenFile(tokenFile, "0123456789abcdef0123456789abcdef"))
	jailRoot := filepath.Join(tmp, "jail")
	require.NoError(t, os.MkdirAll(filepath.Join(jailRoot, "root"), 0o700))
	cgroup := f.manager.cgroupPath("sbx-1")

	rec := testRecord("sbx-1")
	rec.State, rec.Disk, rec.AgentTokenFile, rec.JailRoot = StateReady, disk, tokenFile, jailRoot
	rec.NetNS, rec.TAP = "/var/run/netns/sbx-sbx-1", "tap0"
	require.NoError(t, f.store.Put(rec))
	require.NoError(t, f.instances.Put(vminstance.Instance{
		ID: "sbx-1", Kind: vminstance.KindSandbox, PID: proc.Process.Pid,
		APISocket: filepath.Join(tmp, "sbx-1.sock"), NetNS: rec.NetNS, TAP: "tap0", Disk: disk,
	}))
	f.instances.alive = vminstance.ProcessAlive

	freed, err := f.manager.Remove(ctx, "sbx-1")
	require.NoError(t, err)

	assert.True(t, freed.Process, "the running VMM is stopped")
	assert.Equal(t, proc.Process.Pid, freed.PID)
	assert.False(t, vminstance.ProcessAlive(proc.Process.Pid), "the process is gone, not merely signalled")
	assert.Equal(t, jailRoot, freed.JailRoot)
	assert.Equal(t, disk, freed.Disk)
	assert.Equal(t, tokenFile, freed.TokenFile)
	assert.True(t, freed.Record)
	assert.Equal(t, []string{"sbx-1"}, f.network.detached)

	for _, path := range []string{disk, tokenFile, jailRoot} {
		_, statErr := os.Stat(path)
		assert.True(t, os.IsNotExist(statErr), "%s must be gone", path)
	}
	_, found, statErr := f.store.Get("sbx-1")
	require.NoError(t, statErr)
	assert.False(t, found, "the record is deleted last")

	// An empty cgroup directory is not there in this fixture (the real jailer creates
	// it), so the report says so rather than claiming it removed one.
	assert.Empty(t, freed.Cgroup)
	assert.NotEmpty(t, cgroup, "the cgroup name is the jailer's layout, not the fixture's")
}

// TestRemoveLeavesALiveSandboxAloneWhenItCannotBeStopped is the half of rm that
// matters most: a VM that cannot be stopped keeps its resources, because freeing them
// under a running guest is not a teardown.
func TestRemoveLeavesALiveSandboxAloneWhenItCannotBeStopped(t *testing.T) {
	f := newFixture(t)

	rec := testRecord("sbx-1")
	rec.Disk = filepath.Join(t.TempDir(), "sbx-1.ext4")
	require.NoError(t, os.WriteFile(rec.Disk, []byte("disk"), 0o600))
	require.NoError(t, f.store.Put(rec))

	// A VMM that will not stop is the one case where a teardown must do nothing at
	// all, so the seam reports the refusal a root process cannot be made to give.
	original := stopVMM
	stopVMM = func(int, time.Duration) error { return fmt.Errorf("process is wedged") }
	t.Cleanup(func() { stopVMM = original })

	require.NoError(t, f.instances.Put(vminstance.Instance{ID: "sbx-1", Kind: vminstance.KindSandbox, PID: 4242}))
	f.instances.alive = func(int) bool { return true }

	_, err := f.manager.Remove(context.Background(), "sbx-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop its VMM")

	// Nothing was freed: the record, the disk and the namespace are still the
	// running VM's.
	_, found, storeErr := f.store.Get("sbx-1")
	require.NoError(t, storeErr)
	assert.True(t, found)
	_, statErr := os.Stat(rec.Disk)
	assert.NoError(t, statErr)
	assert.Empty(t, f.network.detached)
}

// TestReapTakesOnlyWhatIsIdle checks the reaper's one decision, on both sides of it.
func TestReapTakesOnlyWhatIsIdle(t *testing.T) {
	f := newFixture(t)

	idle := testRecord("idle")
	idle.LastActive, idle.IdleTTLSeconds = f.now.Add(-5*time.Minute), 60
	require.NoError(t, f.store.Put(idle))

	fresh := testRecord("fresh")
	fresh.LastActive, fresh.IdleTTLSeconds = f.now, 60
	require.NoError(t, f.store.Put(fresh))

	never := testRecord("never")
	never.LastActive = f.now.Add(-24 * time.Hour)
	require.NoError(t, f.store.Put(never))

	reaped, err := f.manager.Reap(context.Background(), f.now)
	require.NoError(t, err)
	require.Len(t, reaped, 1)
	assert.Equal(t, "idle", reaped[0].ID)
	assert.Equal(t, 5*time.Minute, reaped[0].IdleFor)
	assert.NoError(t, reaped[0].Err)

	_, found, err := f.store.Get("idle")
	require.NoError(t, err)
	assert.False(t, found, "a reaped sandbox's record is gone")

	for _, id := range []string{"fresh", "never"} {
		_, still, err := f.store.Get(id)
		require.NoError(t, err)
		assert.True(t, still, "%s must survive", id)
	}
}

// TestListMarksASandboxWhoseVMMIsGone is the reconciliation that keeps a record
// honest: ready means a live VM, and a record that says ready after its VMM exited is
// corrected by the node's writer rather than left for a reader to discover.
func TestListMarksASandboxWhoseVMMIsGone(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.store.Put(testRecord("gone")))

	records, skipped, err := f.manager.List()
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, records, 1)
	assert.Equal(t, StateFailed, records[0].State)
	assert.Contains(t, records[0].Reason, "VMM is gone")
}

// TestExecRefusesASandboxThatIsNotReadyOrMissing pins the two refusals a caller meets
// before any dial happens.
func TestExecRefusesASandboxThatIsNotReadyOrMissing(t *testing.T) {
	f := newFixture(t)

	_, err := f.manager.Exec(context.Background(), "nope", []string{"true"}, ExecOptions{})
	assert.ErrorIs(t, err, ErrNotFound)

	created := testRecord("created")
	created.State = StateCreated
	require.NoError(t, f.store.Put(created))
	_, err = f.manager.Exec(context.Background(), "created", []string{"true"}, ExecOptions{})
	assert.ErrorIs(t, err, ErrNotReady)
}

// TestNewRefusesANodeThatIsMissingAPiece pins that every piece a sandbox needs is
// checked where the manager is built, rather than half-way through creating one.
func TestNewRefusesANodeThatIsMissingAPiece(t *testing.T) {
	store := newTestStore(t)
	base := func() Config {
		return Config{
			Store:      store,
			GoldenDir:  "/var/lib/firecracker/golden",
			KernelPath: "/usr/share/firecracker/vmlinux",
			RootfsDir:  "/var/lib/firecracker/rootfs",
			Launcher:   &fakeLauncher{},
			Network:    &fakeNetwork{},
		}
	}

	if _, err := New(base()); err != nil {
		t.Fatalf("a complete configuration must build: %v", err)
	}

	cases := []struct {
		name  string
		mutil func(*Config)
	}{
		{"no store", func(c *Config) { c.Store = nil }},
		{"no template directory", func(c *Config) { c.GoldenDir = "" }},
		{"no kernel", func(c *Config) { c.KernelPath = "" }},
		{"no rootfs directory", func(c *Config) { c.RootfsDir = "" }},
		{"no launcher", func(c *Config) { c.Launcher = nil }},
		{"no network policy", func(c *Config) { c.Network = nil }},
		{"negative default TTL", func(c *Config) { c.DefaultTTL = -time.Second }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutil(&cfg)
			_, err := New(cfg)
			assert.Error(t, err)
		})
	}

	// Defaults fill in where a caller said nothing, and the manager says so.
	m, err := New(base())
	require.NoError(t, err)
	assert.Equal(t, 1, m.cfg.VCPUs)
	assert.Equal(t, 512, m.cfg.MemoryMB)
	assert.Equal(t, defaultParentCgroup, m.cfg.ParentCgroup)
	assert.NotNil(t, m.Store())
}

// TestCreateRefusalsAreTheOnesACallerCanActOn pins the cheap refusals: no template, an
// id that is already a sandbox, an id that cannot name one.
func TestCreateRefusalsAreTheOnesACallerCanActOn(t *testing.T) {
	f := newFixture(t)

	_, err := f.manager.Create(context.Background(), CreateRequest{})
	assert.ErrorIs(t, err, ErrInvalid)

	require.NoError(t, f.store.Put(testRecord("taken")))
	_, err = f.manager.Create(context.Background(), CreateRequest{Template: "base", ID: "taken"})
	assert.ErrorIs(t, err, ErrExists)

	_, err = f.manager.Create(context.Background(), CreateRequest{Template: "base", ID: "a/b"})
	assert.ErrorIs(t, err, ErrInvalid)

	// An id the caller did not choose is minted, and it is one a sandbox can be called.
	rec, err := f.manager.Create(context.Background(), CreateRequest{Template: "base"})
	require.NoError(t, err)
	require.Len(t, rec.ID, 12)
	require.NoError(t, checkID(rec.ID))

	// A template store with nothing in it says so.
	_, err = f.manager.Create(context.Background(), CreateRequest{Template: "absent"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestExecReportsAGuestItCannotReach covers the dial that fails: a sandbox can be ready
// and still have no guest on the other end of its muxer (a VMM that just exited), and
// that is an error rather than an empty result.
func TestExecReportsAGuestItCannotReach(t *testing.T) {
	f := newFixture(t)
	rec := testRecord("sbx-1")
	rec.VsockPath = filepath.Join(t.TempDir(), "no-such.vsock")
	require.NoError(t, f.store.Put(rec))

	_, err := f.manager.Exec(context.Background(), "sbx-1", []string{"true"}, ExecOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reach its guest agent")

	// The pre-flight refuses an empty command before it dials.
	_, err = f.manager.Exec(context.Background(), "sbx-1", nil, ExecOptions{})
	assert.ErrorIs(t, err, ErrInvalid)
}

// TestServeExecRefusesWhatTheApiMustReportAsTyped pins the pre-flight of the streaming
// path: a sandbox that is not there, and one that cannot run anything, are answered
// before the handler is handed the request, so the API can write a typed document.
func TestServeExecRefusesWhatTheApiMustReportAsTyped(t *testing.T) {
	f := newFixture(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/nope/exec", nil)

	err := f.manager.ServeExec(context.Background(), "nope", recorder, request)
	assert.ErrorIs(t, err, ErrNotFound)

	rec := testRecord("sbx-1")
	rec.State, rec.Reason = StateFailed, "the guest never answered"
	require.NoError(t, f.store.Put(rec))
	err = f.manager.ServeExec(context.Background(), "sbx-1", recorder, request)
	assert.ErrorIs(t, err, ErrNotReady)
	assert.Contains(t, err.Error(), "the guest never answered")
}

// TestRemoveAndListRefuseWhatIsNotThere pins the two lookups a caller can get wrong.
func TestRemoveAndListRefuseWhatIsNotThere(t *testing.T) {
	f := newFixture(t)

	_, err := f.manager.Remove(context.Background(), "nope")
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = f.manager.Get("nope")
	assert.ErrorIs(t, err, ErrNotFound)

	// A listing reports a record file it cannot read instead of failing.
	require.NoError(t, os.WriteFile(filepath.Join(f.store.Dir(), "broken.json"), []byte("{"), 0o600))
	_, skipped, err := f.manager.List()
	require.NoError(t, err)
	assert.Len(t, skipped, 1)
}

// TestReapEveryStopsWithItsContext pins the daemon's shape of the reaper: it returns
// when the context ends, and it refuses a non-positive interval rather than spinning.
func TestReapEveryStopsWithItsContext(t *testing.T) {
	f := newFixture(t)

	assert.Error(t, f.manager.ReapEvery(context.Background(), 0))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NoError(t, f.manager.ReapEvery(ctx, time.Millisecond))
}

// TestTerminalHasOneState pins that stopped is the only end of the road.
func TestTerminalHasOneState(t *testing.T) {
	assert.False(t, StateCreated.Terminal())
	assert.False(t, StateReady.Terminal())
	assert.False(t, StateFailed.Terminal())
	assert.True(t, StateStopped.Terminal())
}

// TestCreateReportsANetworkItCouldNotBuild pins the first step's failure: the sandbox
// is visible as failed with the reason, and nothing was allocated behind it.
func TestCreateReportsANetworkItCouldNotBuild(t *testing.T) {
	f := newFixture(t)
	f.network.attachErr = fmt.Errorf("nftables is not installed")

	rec, err := f.manager.Create(context.Background(), CreateRequest{Template: "base", ID: "sbx-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nftables")
	assert.Equal(t, StateFailed, rec.State)
	assert.Empty(t, f.launcher.requests, "no VM is launched without a network to launch it in")
	assert.Empty(t, rec.Disk, "no disk is materialized behind a failed network")
}

// TestRemoveReportsANetworkItCouldNotDetach pins the other side: a teardown that cannot
// free the namespace says so, keeps the record as the handle for the next attempt, and
// does not report a namespace as removed.
func TestRemoveReportsANetworkItCouldNotDetach(t *testing.T) {
	f := newFixture(t)
	rec := testRecord("sbx-1")
	rec.NetNS, rec.TAP = "/var/run/netns/sbx-sbx-1", "tap0"
	require.NoError(t, f.store.Put(rec))

	f.network.detachErr = fmt.Errorf("the namespace is busy")

	_, err := f.manager.Remove(context.Background(), "sbx-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detach its network")

	_, found, storeErr := f.store.Get("sbx-1")
	require.NoError(t, storeErr)
	assert.True(t, found, "the record stays: it is the handle the next rm uses")
}

// TestDiskAndTokenStepsReportWhatWentWrong covers the two steps between the network and
// the VM: a disk that cannot be copied, and a disk the token cannot be written into. Both
// are refusals a caller can act on, and both must leave the sandbox failed rather than
// launch a VM that is missing one of them.
func TestDiskAndTokenStepsReportWhatWentWrong(t *testing.T) {
	f := newFixture(t)

	_, err := f.manager.materializeDisk(context.Background(),
		&golden.Artifact{Ref: "base@1", Path: filepath.Join(t.TempDir(), "absent.ext4")},
		filepath.Join(t.TempDir(), "disk.ext4"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "materialize the disk")

	// A disk that is not a filesystem cannot hold a token: debugfs refuses it, and the
	// create must report that rather than boot a guest with no credential.
	notAFilesystem := filepath.Join(t.TempDir(), "not-ext4")
	require.NoError(t, os.WriteFile(notAFilesystem, []byte("this is not a filesystem"), 0o600))
	_, err = f.manager.provisionToken(context.Background(), notAFilesystem)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent token")
}

// TestKernelForRefusesATemplateWithNoKernelHere pins the pin's other half: the kernel a
// template would boot must resolve on this node, or the template cannot be used.
func TestKernelForRefusesATemplateWithNoKernelHere(t *testing.T) {
	f := newFixture(t)

	_, err := f.manager.kernelFor(nil)
	assert.Error(t, err)

	f.manager.cfg.KernelPath = ""
	_, err = f.manager.kernelFor(&golden.Artifact{
		Ref:      "base@1",
		Metadata: &golden.ArtifactMetadata{KernelProfile: "unmapped"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmapped")
}

// TestListKeepsARecordWhenTheRegistryCannotBeRead pins the reconciliation's fail-safe: a
// registry that cannot be read is not evidence that a sandbox died, so its record is not
// downgraded on a guess.
func TestListKeepsARecordWhenTheRegistryCannotBeRead(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.store.Put(testRecord("sbx-1")))
	f.instances.readErr = fmt.Errorf("the registry directory is unreadable")

	records, _, err := f.manager.List()
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, StateReady, records[0].State, "an unreadable registry is not a dead VM")
}

// TestCreateTakesTheSizeAndTheIdleTtlTheCallerNamed pins where a create's numbers come
// from: what the caller asked for reaches the launch, and a TTL is one value with one
// meaning — zero on disk is "never reap this".
func TestCreateTakesTheSizeAndTheIdleTtlTheCallerNamed(t *testing.T) {
	f := newFixture(t)

	rec, err := f.manager.Create(context.Background(),
		CreateRequest{Template: "base", VCPUs: 2, MemoryMB: 768, TTL: 90 * time.Second})
	require.NoError(t, err)
	require.Len(t, f.launcher.requests, 1)
	assert.Equal(t, 2, f.launcher.requests[0].VCPUs)
	assert.Equal(t, 768, f.launcher.requests[0].MemoryMB)
	assert.Equal(t, int64(90), rec.IdleTTLSeconds)

	never, err := f.manager.Create(context.Background(), CreateRequest{Template: "base", TTL: -1})
	require.NoError(t, err)
	assert.Zero(t, never.IdleTTLSeconds, "a negative TTL is stored as zero: the record has one value for one meaning")

	// Whitespace around an id the caller named is not part of the identity: " sbx1" is
	// the sandbox "sbx1", which is the name a later exec and rm resolve too.
	padded, err := f.manager.Create(context.Background(), CreateRequest{Template: "base", ID: " padded"})
	require.NoError(t, err)
	assert.Equal(t, "padded", padded.ID)
	_, err = f.manager.Get(" padded")
	assert.NoError(t, err, "the same padded name reaches the sandbox it named")
}

// TestAFailedCreateKeepsWhatItCouldNotFree pins why a failed record keeps its paths: when
// the free that follows a failed create cannot finish — a VMM that will not stop — the
// record is the handle the next teardown uses, and clearing it would leave a disk and a
// jail nothing names.
func TestAFailedCreateKeepsWhatItCouldNotFree(t *testing.T) {
	f := newFixture(t)

	require.NoError(t, f.instances.Put(vminstance.Instance{
		ID: "sbx-1", Kind: vminstance.KindSandbox, PID: 4242,
	}))
	f.instances.alive = func(int) bool { return true }
	original := stopVMM
	stopVMM = func(int, time.Duration) error { return fmt.Errorf("process is wedged") }
	t.Cleanup(func() { stopVMM = original })

	f.launcher.err = fmt.Errorf("the guest agent never answered")

	rec, err := f.manager.Create(context.Background(), CreateRequest{Template: "base", ID: "sbx-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the guest agent never answered")
	assert.Contains(t, err.Error(), "process is wedged", "a free that did not finish is reported with the failure")

	assert.Equal(t, StateFailed, rec.State)
	assert.NotEmpty(t, rec.Disk, "the disk nothing could free is still named by the record")
	_, statErr := os.Stat(rec.Disk)
	assert.NoError(t, statErr, "and it is really still there")

	stored, err := f.manager.Get("sbx-1")
	require.NoError(t, err)
	assert.Equal(t, StateFailed, stored.State)
	assert.NotEmpty(t, stored.Disk)
}

// TestListTrustsAReadySandboxWhenTheNodeKeepsNoRegistry pins the reconciliation's scope: a
// node that keeps no instance registry has no evidence a sandbox died, so it does not
// rewrite a readable record.
func TestListTrustsAReadySandboxWhenTheNodeKeepsNoRegistry(t *testing.T) {
	f := newFixture(t)
	f.manager.cfg.Instances = nil
	require.NoError(t, f.store.Put(testRecord("sbx-1")))

	records, _, err := f.manager.List()
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, StateReady, records[0].State)
}

// TestCreateNamesTheDiskDirectoryItCannotCreate pins the failure an operator acts on: a
// rootfs directory that is not a directory is named, not reported as a VM that would not
// boot.
func TestCreateNamesTheDiskDirectoryItCannotCreate(t *testing.T) {
	f := newFixture(t)

	file := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	f.manager.cfg.RootfsDir = file

	_, err := f.manager.Create(context.Background(), CreateRequest{Template: "base", ID: "sbx-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create the disk directory")
}
