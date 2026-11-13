package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The admission tests. Two things are under test and they are different questions: what
// the node refuses (the limit matrix), and what the refusal leaves behind (nothing). The
// second is the one a limit is worth having for — a refusal that had already copied a
// disk would be a limit that only reports the waste.

// TestCreateIsRefusedBeforeItAllocatesAnything is the promise admission makes: a sandbox
// the node will not admit leaves no record, no namespace, no disk and no VM behind.
func TestCreateIsRefusedBeforeItAllocatesAnything(t *testing.T) {
	f := newFixtureWith(t, func(cfg *Config) {
		cfg.Quota.MaxPerOwner = 1
	})
	ctx := context.Background()

	first, err := f.manager.Create(ctx, CreateRequest{Template: "base", ID: "sbx-1", Owner: "ci"})
	require.NoError(t, err)
	require.Equal(t, StateReady, first.State)

	refused, err := f.manager.Create(ctx, CreateRequest{Template: "base", ID: "sbx-2", Owner: "ci"})
	require.Error(t, err, "the owner already holds the one sandbox this node allows it")
	require.Empty(t, refused, "a refused create returns no sandbox to reason about")

	var quota *QuotaError
	require.ErrorAs(t, err, &quota)
	assert.ErrorIs(t, err, ErrQuota)
	assert.Equal(t, QuotaResourceOwnerSandboxes, quota.Resource)
	assert.Equal(t, int64(1), quota.Limit)
	assert.Equal(t, int64(1), quota.Used, "the refusal reports what the owner held, not what it asked for")
	assert.Equal(t, int64(1), quota.Requested)
	assert.Equal(t, DefaultQuotaRetryAfter, quota.RetryAfter, "the refusal carries the node's retry hint")
	assert.Contains(t, quota.Error(), "ci", "the refusal names the owner it is about")

	// Nothing was allocated for the refused sandbox: no record to read, no namespace to
	// detach, no VM to stop and no disk to free. This is the whole point of checking
	// before allocating rather than after.
	_, found, err := f.store.Get("sbx-2")
	require.NoError(t, err)
	assert.False(t, found, "a refused create writes no record")
	assert.Equal(t, []string{"sbx-1"}, f.network.attached, "a refused create attaches no namespace")
	assert.Len(t, f.launcher.requests, 1, "a refused create launches no VM")
	_, statErr := os.Stat(f.manager.diskPath("sbx-2"))
	assert.True(t, os.IsNotExist(statErr), "a refused create materializes no disk")

	records, _, err := f.store.List()
	require.NoError(t, err)
	assert.Len(t, records, 1)
	disks, err := filepath.Glob(filepath.Join(f.rootfsDir, DirName, "*.ext4"))
	require.NoError(t, err)
	assert.Equal(t, []string{f.manager.diskPath("sbx-1")}, disks, "one sandbox, one disk")
}

// TestNodeCapacityIsCheckedOnItsOwn holds the other half of the limit matrix: the node's
// own capacity refuses a create even when the owner is far from its share, and it
// refuses it for a caller whose owner has nothing at all.
func TestNodeCapacityIsCheckedOnItsOwn(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		// The fixture's sandboxes are 256 MiB each, so 512 MiB admits exactly two and
		// the third is what does not fit.
		f := newFixtureWith(t, func(cfg *Config) {
			cfg.Quota.MemoryMB = 512
		})
		ctx := context.Background()

		_, err := f.manager.Create(ctx, CreateRequest{Template: "base", ID: "sbx-1", Owner: "a"})
		require.NoError(t, err)
		_, err = f.manager.Create(ctx, CreateRequest{Template: "base", ID: "sbx-2", Owner: "a"})
		require.NoError(t, err, "a create that fits the limit exactly is admitted")

		// A different owner: the per-owner limit is the default ten, so what refuses this
		// create is the node's memory and nothing else.
		_, err = f.manager.Create(ctx, CreateRequest{Template: "base", ID: "sbx-3", Owner: "b"})
		require.Error(t, err)

		var quota *QuotaError
		require.ErrorAs(t, err, &quota)
		assert.Equal(t, QuotaResourceNodeMemoryMB, quota.Resource)
		assert.Equal(t, int64(512), quota.Limit)
		assert.Equal(t, int64(512), quota.Used, "both running sandboxes' memory is counted")
		assert.Equal(t, int64(256), quota.Requested, "the refusal says how much this sandbox asked for")
		assert.Contains(t, quota.Error(), "512 MiB")

		_, found, err := f.store.Get("sbx-3")
		require.NoError(t, err)
		assert.False(t, found)
		assert.Equal(t, []string{"sbx-1", "sbx-2"}, f.network.attached)
	})

	t.Run("disk", func(t *testing.T) {
		// The fixture's template artifact is 8 MiB, and a per-instance disk is a copy of
		// it, so an 8 MiB budget admits exactly one sandbox.
		f := newFixtureWith(t, func(cfg *Config) {
			cfg.Quota.DiskMB = 8
		})
		ctx := context.Background()

		_, err := f.manager.Create(ctx, CreateRequest{Template: "base", ID: "sbx-1", Owner: "a"})
		require.NoError(t, err)

		_, err = f.manager.Create(ctx, CreateRequest{Template: "base", ID: "sbx-2", Owner: "b"})
		require.Error(t, err)

		var quota *QuotaError
		require.ErrorAs(t, err, &quota)
		assert.Equal(t, QuotaResourceNodeDiskMB, quota.Resource)
		assert.Equal(t, int64(8), quota.Limit)
		assert.Equal(t, int64(8), quota.Used, "the disk of the sandbox already made is counted")
		assert.Equal(t, int64(8), quota.Requested, "a create reserves the size of the image it copies")
	})
}

// TestQuotaCountsOnlyLiveSandboxes pins what the ledger is: a sandbox holds memory and a
// disk while it exists or is being built, and a failed or stopped one holds nothing. A
// node that counted the dead would refuse work it has room for.
func TestQuotaCountsOnlyLiveSandboxes(t *testing.T) {
	f := newFixtureWith(t, func(cfg *Config) {
		cfg.Quota.MaxPerOwner = 1
	})
	ctx := context.Background()

	dead := func(id string, state State) Record {
		now := f.now
		return Record{
			ID: id, State: state, Owner: "ci", Template: "base@1.0.0",
			Created: now, Updated: now, LastActive: now,
			Reason: "test", MemoryMB: 256,
		}
	}
	stale := dead("sbx-failed", StateFailed)
	stale.Reason = "the template would not boot"
	require.NoError(t, f.store.Put(stale))
	require.NoError(t, f.store.Put(dead("sbx-stopped", StateStopped)))

	_, err := f.manager.Create(ctx, CreateRequest{Template: "base", ID: "sbx-1", Owner: "ci"})
	require.NoError(t, err, "a failed sandbox and a stopped one hold nothing to count")

	// A record in state created is a create in flight, and it is holding what it has
	// taken: it counts, or two creates racing would each see room.
	require.NoError(t, f.store.Put(dead("sbx-building", StateCreated)))
	_, err = f.manager.Create(ctx, CreateRequest{Template: "base", ID: "sbx-2", Owner: "ci"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrQuota)
}

// TestQuotaGivesUnnamedCreatesOneBucket pins the owner rule: a caller that names no owner
// shares one bucket with every other unnamed create. A limit a caller can leave by
// leaving out a field is not a limit.
func TestQuotaGivesUnnamedCreatesOneBucket(t *testing.T) {
	f := newFixtureWith(t, func(cfg *Config) {
		cfg.Quota.MaxPerOwner = 1
	})
	ctx := context.Background()

	_, err := f.manager.Create(ctx, CreateRequest{Template: "base", ID: "sbx-1"})
	require.NoError(t, err)

	_, err = f.manager.Create(ctx, CreateRequest{Template: "base", ID: "sbx-2"})
	require.Error(t, err)
	var quota *QuotaError
	require.ErrorAs(t, err, &quota)
	assert.Equal(t, QuotaResourceOwnerSandboxes, quota.Resource)
	assert.Empty(t, quota.Owner, "the unnamed owner's bucket is reported as what it is")
	assert.Contains(t, quota.Error(), "unnamed owner")
}

// TestCreateIsRefusedWhenTheLedgerCannotBeRead is the fail-closed rule: a node that
// cannot say what its sandboxes hold must not admit one against a number it does not
// have.
func TestCreateIsRefusedWhenTheLedgerCannotBeRead(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, os.RemoveAll(f.store.Dir()))

	_, err := f.manager.Create(context.Background(), CreateRequest{Template: "base", ID: "sbx-1", Owner: "ci"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrQuota, "a ledger that cannot be read is a failure, not a policy decision")
	assert.Contains(t, err.Error(), "already has")
	assert.Empty(t, f.network.attached)
	assert.Empty(t, f.launcher.requests)
}

// TestQuotaDefaultsAndRefusals pins where the numbers come from: a node that configures
// nothing gets the defaults, and a configuration that cannot be meant stops the node
// starting rather than being read as "no limit".
func TestQuotaDefaultsAndRefusals(t *testing.T) {
	t.Run("a node that configures nothing gets the defaults", func(t *testing.T) {
		manager, err := New(testNodeConfig(t))
		require.NoError(t, err)
		assert.Equal(t, DefaultQuota(), manager.cfg.Quota)
	})

	t.Run("one limit configured leaves the others at their defaults", func(t *testing.T) {
		cfg := testNodeConfig(t)
		cfg.Quota.MemoryMB = 1024
		manager, err := New(cfg)
		require.NoError(t, err)
		assert.Equal(t, 1024, manager.cfg.Quota.MemoryMB)
		assert.Equal(t, DefaultMaxSandboxesPerOwner, manager.cfg.Quota.MaxPerOwner)
		assert.Equal(t, DefaultNodeDiskMB, manager.cfg.Quota.DiskMB)
		assert.Equal(t, DefaultQuotaRetryAfter, manager.cfg.Quota.RetryAfter)
	})

	for _, tc := range []struct {
		name  string
		tweak func(*Config)
		named string
	}{
		{"a negative per-owner limit", func(c *Config) { c.Quota.MaxPerOwner = -1 }, "max sandboxes per owner"},
		{"a negative memory limit", func(c *Config) { c.Quota.MemoryMB = -1 }, "node memory"},
		{"a negative disk limit", func(c *Config) { c.Quota.DiskMB = -1 }, "node disk"},
		{"a negative retry hint", func(c *Config) { c.Quota.RetryAfter = -time.Second }, "retry-after"},
	} {
		t.Run(tc.name+" stops the node", func(t *testing.T) {
			cfg := testNodeConfig(t)
			tc.tweak(&cfg)
			_, err := New(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.named, "the refusal names the field an operator has to fix")
		})
	}
}

// testNodeConfig is the least a manager needs: the paths and seams New checks before it
// looks at the limits, so a test about a limit is not also a test about a disk.
func testNodeConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Store:      newTestStore(t),
		GoldenDir:  "/var/lib/firecracker/golden",
		KernelPath: "/usr/share/firecracker/vmlinux",
		RootfsDir:  "/var/lib/firecracker/rootfs",
		Launcher:   &fakeLauncher{},
		Network:    &fakeNetwork{},
	}
}

// TestMibCeilRoundsUp pins the reporting rule the two disk figures are rendered with: a
// refusal must not claim figures that add up to less than the limit it refused on.
func TestMibCeilRoundsUp(t *testing.T) {
	assert.Equal(t, int64(0), mibCeil(0))
	assert.Equal(t, int64(1), mibCeil(1))
	assert.Equal(t, int64(1), mibCeil(1<<20))
	assert.Equal(t, int64(2), mibCeil((1<<20)+1))
	assert.Equal(t, int64(9), mibCeil(9<<20))
}

// TestQuotaResourceNamesAreStable pins the three names that cross the API in
// details.resource. They are part of the contract a client matches on, so renaming one is
// a wire change and must not happen by tidying a constant.
func TestQuotaResourceNamesAreStable(t *testing.T) {
	assert.Equal(t, "sandboxes_per_owner", QuotaResourceOwnerSandboxes)
	assert.Equal(t, "node_memory_mb", QuotaResourceNodeMemoryMB)
	assert.Equal(t, "node_disk_mb", QuotaResourceNodeDiskMB)
	for _, name := range []string{QuotaResourceOwnerSandboxes, QuotaResourceNodeMemoryMB, QuotaResourceNodeDiskMB} {
		assert.True(t, strings.TrimSpace(name) != "")
	}
}

// TestAdmissionDoesNotCountTheCreateItIsChecking pins that the ledger is read before the
// record is written: a create cannot count itself, or the first sandbox on a node with a
// limit of one would be refused by its own reservation.
func TestAdmissionDoesNotCountTheCreateItIsChecking(t *testing.T) {
	f := newFixtureWith(t, func(cfg *Config) {
		cfg.Quota.MaxPerOwner = 1
		cfg.Quota.MemoryMB = 256
	})
	_, err := f.manager.Create(context.Background(), CreateRequest{Template: "base", ID: "sbx-1", Owner: "ci"})
	require.NoError(t, err, "the create that exactly fills the node is admitted")
	assert.NotErrorIs(t, err, ErrQuota)
}

// TestQuotaErrorIsAnErrQuota pins the sentinel relationship callers rely on:
// errors.Is(err, ErrQuota) is true through the typed error, so a caller that only needs
// "the node is full" does not have to know the type.
func TestQuotaErrorIsAnErrQuota(t *testing.T) {
	err := error(&QuotaError{Resource: QuotaResourceNodeMemoryMB, Limit: 512, Used: 512, Requested: 256})
	assert.ErrorIs(t, err, ErrQuota)
	require.True(t, errors.Is(err, ErrQuota))
	assert.False(t, errors.Is(err, ErrNotFound))
}
