package swarmkit

// This file owns the first step of a task start on a node that was told, before the
// task existed, that the next task of a service restores a staged migration
// artifact.
//
// A live migration here is a SwarmKit replacement: the manager stages an artifact on
// the target and records a pending-restore directive for the service, then SwarmKit
// stops the old task and creates the new one on the target. The task start is the
// only place that knows both halves — which service is being replaced and which
// task ID SwarmKit chose — so the restore lives there, before the boot:
//
//	1. ask the directive store for this service, consuming it,
//	2. read and validate the manifest it staged,
//	3. derive the guest's new address and gateway from the task's own attachment,
//	4. restore, with the new task's console log and API socket,
//	5. on success drop the staged bytes and continue the start as a running task,
//	6. on failure put the directive back and fail the task, so the retry restores the
//	   same artifact rather than booting a second, unrelated VM.
//
// No directive is the ordinary case and it is the hot path: one store lookup, and
// then exactly the boot this executor always performed. Nothing below runs for a task
// whose service has nothing prepared.
//
// The restore itself is pkg/migration's (Restore), which is a leaf: it starts nothing,
// runs nothing and touches no interface. Everything it needs is wired here, from
// packages that may: pkg/snapshot starts and stops Firecracker, pkg/network owns the
// TAP lifecycle, and this package knows the task's own network attachment.

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// PendingRestoreStore is the node's store of pending-restore directives, as a task
// start uses it. *runtime.PendingRestores is the production implementation, and it is
// the same instance the node broker's prepare route writes to: a directive the manager
// prepared is only visible to the task that claims it if both read one directory, and
// the filesystem is what makes that true without any reconciliation.
type PendingRestoreStore interface {
	// Take claims and removes the directive waiting for serviceID. found is false
	// when nothing was prepared for it, and also when the directive is past its TTL —
	// in which case the record is still returned, so the caller can log which
	// directive it ignored.
	Take(serviceID, root string) (scruntime.PendingRestore, bool, error)
	// Put records a directive, keeping the CreatedAt it carries. A restore that
	// consumed one and then failed puts the same record back, so SwarmKit's retry
	// restores rather than booting: the TTL is measured from the prepare, not from
	// the put-back.
	Put(record scruntime.PendingRestore) error
}

// ArtifactRestorer starts the VM a staged artifact describes as a task's VM, and stops
// what it started.
//
// consolePath is the task's console log (<logDir>/<taskID>.log): the log belongs to
// the new task, not to the source one whose VM is inside the artifact, because it is
// what this node's `vm logs` reads for the task it now runs. An empty path leaves the
// console detached.
//
// StopRestored is part of the interface because a restore that fails after the load
// must take its process down, and only the restorer holds it.
type ArtifactRestorer interface {
	RestoreArtifact(ctx context.Context, info *snapshot.SnapshotInfo, socketPath, consolePath string) error
	StopRestored(taskID string) error
}

// MigrationRestoreConfig is what a task start needs to restore a staged artifact
// instead of booting a fresh VM. A nil config is a node that restores nothing.
type MigrationRestoreConfig struct {
	// Pending is the directive store the manager prepared into.
	Pending PendingRestoreStore
	// Root is this node's migration root. A directive names <Root>/<artifactID>, and
	// the store checks the directory against this root again when the directive is
	// consumed.
	Root string
	// Restorer loads the artifact's snapshot as the task's VM.
	Restorer ArtifactRestorer
	// Agent dials the guest agent behind the artifact's vsock muxer. Nil selects
	// migration.ProductionDialer, which is the node's real guest client; a test
	// substitutes a dialer that answers a fake guest.
	Agent migration.AgentDialer
	// Tap is the TAP device lifecycle the restore drives. The zero value selects
	// pkg/network's real device operations, which is what the daemon gets; a test
	// substitutes effects that touch no interface.
	Tap TapEffects
}

// TapEffects are the three TAP operations a restore performs, in the order it performs
// them: create the device down, bring it up once the guest has its new address, and
// remove it if the restore fails.
//
// The device is created under the recorded name and left down because the guest resumes
// with the source node's address still configured: an interface that is up first would
// put that address on this node's bridge.
type TapEffects struct {
	// Create adds the device and attaches it to the bridge, leaving it down.
	Create func(name, bridge string) error
	// Up brings an existing device up.
	Up func(name string) error
	// Delete removes a device, treating one that is already gone as success.
	Delete func(name string) error
}

// NewArtifactRestorer adapts the node's snapshot manager to ArtifactRestorer: the
// manager starts the Firecracker process, so it is also the thing that can stop it
// again, and the restored task's stop path is keyed by that same manager.
//
// A nil manager answers a nil restorer, so the daemon's wiring can pass "the snapshot
// store could not be built" through without a typed nil that would panic when called.
func NewArtifactRestorer(manager *snapshot.Manager) ArtifactRestorer {
	if manager == nil {
		return nil
	}
	return snapshotArtifactRestorer{manager: manager}
}

// snapshotArtifactRestorer is the production ArtifactRestorer.
type snapshotArtifactRestorer struct {
	manager *snapshot.Manager
}

// RestoreArtifact loads the snapshot with the task's own console log path, so `vm
// logs` on the moved task reads the VM the node runs now.
func (r snapshotArtifactRestorer) RestoreArtifact(
	ctx context.Context, info *snapshot.SnapshotInfo, socketPath, consolePath string,
) error {
	return r.manager.RestoreFromSnapshotWithLog(ctx, info, socketPath, consolePath)
}

// StopRestored stops the process this restorer started for a task.
func (r snapshotArtifactRestorer) StopRestored(taskID string) error {
	return r.manager.StopRestored(taskID)
}

// The production restorer is pkg/snapshot's manager behind an adapter, and the record
// store a restore writes is the node's restored-instance registry — the same store the
// executor's teardown resolves the task through. The assertions fail the build if either
// signature drifts, which is the only way the two packages can disagree without a test
// noticing at wiring time.
var (
	_ ArtifactRestorer                = snapshotArtifactRestorer{}
	_ migration.RestoredInstanceStore = (*scruntime.RestoredRegistry)(nil)
)

// startFromArtifact restores the VM a pending-restore directive describes instead of
// booting a fresh one. restored is true when this task now runs a restored VM, and the
// caller must not boot.
//
// The prepared task is read once under the lock and everything below works from that
// copy: the restore can take seconds, and holding c.mu for it would stall the teardown
// SwarmKit may be sending at the same time.
func (c *Controller) startFromArtifact(ctx context.Context) (restored bool, err error) {
	cfg := c.migration
	if cfg == nil || cfg.Pending == nil {
		return false, nil
	}

	c.mu.Lock()
	prepared, started, task := c.prepared, c.started, c.internalTask
	c.mu.Unlock()

	// Only a prepared task that has not started may claim a directive: consuming one
	// for a task that cannot start would restore an artifact for nobody.
	if !prepared || started || task == nil {
		return false, nil
	}
	// A directive is keyed by service, and a task that belongs to no service is not a
	// replica of anything a migration can have replaced.
	serviceID := task.ServiceID
	if serviceID == "" {
		return false, nil
	}

	directive, found, err := cfg.Pending.Take(serviceID, cfg.Root)
	switch {
	case err != nil:
		// An unusable directive is not this task's to restore — the store put it back
		// for a human to read — so the task boots. One warning is the whole report.
		c.logger.Warn().Err(err).Str("service_id", serviceID).
			Msg("A pending-restore directive for this service cannot be used; starting a fresh VM")
		return false, nil
	case !found:
		if !directive.CreatedAt.IsZero() {
			// The store consumed and deleted a directive past its TTL. Booting cold is
			// the documented outcome for an abandoned migration, and an operator has to
			// be able to see that it happened.
			c.logger.Warn().
				Str("service_id", serviceID).
				Str("artifact_id", directive.ArtifactID).
				Time("created_at", directive.CreatedAt).
				Str("ttl", scruntime.DefaultPendingRestoreTTL.String()).
				Msg("Ignoring a pending-restore directive older than its TTL; starting a fresh VM")
		}
		return false, nil
	}

	if err := c.restoreArtifact(ctx, cfg, task, directive); err != nil {
		// The directive goes back before the error is returned: a retry of this task
		// must restore the artifact again, not boot a second VM beside the one the
		// source node is still running.
		return false, c.keepDirective(cfg, directive, err)
	}
	return true, nil
}

// restoreArtifact performs the restore a directive describes, and drops the staged
// bytes once the guest is running under this task.
func (c *Controller) restoreArtifact(
	ctx context.Context, cfg *MigrationRestoreConfig, task *types.Task, directive scruntime.PendingRestore,
) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("task %q: restore artifact %s: %s", task.ID, directive.ArtifactID, fmt.Sprintf(format, args...))
	}

	// The restore needs the node's bridge, socket directory and console log directory.
	// Only a controller built without one — never the executor's — has none, and it
	// must be refused rather than panic halfway through a load.
	if c.config == nil {
		return fail("this controller has no node configuration, so the restored VM has no bridge, socket or log directory")
	}
	if cfg.Restorer == nil {
		return fail("this node has no artifact restorer, so the staged VM cannot be loaded")
	}

	artifact, err := migration.ReadArtifact(directive.Dir)
	if err != nil {
		return fmt.Errorf("task %q: restore artifact %s: %w", task.ID, directive.ArtifactID, err)
	}
	// The directive names the artifact by ID and the directory is derived from it, so
	// a manifest that names another artifact is a staging directory that was replaced
	// underneath the directive: restoring it would start a VM nobody asked this task to
	// run.
	if artifact.ID != directive.ArtifactID {
		return fail("the staged manifest describes artifact %q, not %q", artifact.ID, directive.ArtifactID)
	}
	if artifact.ServiceID != "" && artifact.ServiceID != task.ServiceID {
		return fail("the staged artifact was exported from service %q, not %q", artifact.ServiceID, task.ServiceID)
	}

	guestCIDR, gateway, err := restoredGuestAddress(task, c.config.BridgeIP)
	if err != nil {
		return fmt.Errorf("task %q: restore artifact %s: %w", task.ID, directive.ArtifactID, err)
	}

	instance, err := migration.Restore(ctx, migration.RestoreInput{
		Artifact:  artifact,
		Dir:       directive.Dir,
		TaskID:    task.ID,
		GuestCIDR: guestCIDR,
		Gateway:   gateway,
		// The TAP keeps the source task's name — the state file resolves it — but the
		// bridge is this node's, and the socket is the new task's.
		Bridge:    c.config.BridgeName,
		SocketDir: c.config.SocketDir,
	}, c.restoreDeps(cfg))
	if err != nil {
		return fmt.Errorf("task %q: %w", task.ID, err)
	}

	// A restore that succeeded but left its sidecar unwritten would leave `vm status`
	// describing a task with no network, so the metadata is published exactly as a boot
	// would have published it. It is best-effort there and here.
	c.writeVMMetadata(task)

	// The bytes were this node's only until the guest ran: a copy left behind would
	// shadow the source's cleanup and hold a memory image nothing will restore.
	if removeErr := os.RemoveAll(directive.Dir); removeErr != nil {
		c.logger.Warn().Err(removeErr).Str("dir", directive.Dir).
			Msg("The restored task's staged artifact directory could not be removed")
	}

	// This executor never booted the VM, so it never counted it as started. Wait reads
	// this to answer the VM's end without moving the counters a boot would have moved.
	c.markInheritedVM(instance.SocketPath)

	c.logger.Info().
		Str("artifact_id", artifact.ID).
		Str("vm_id", instance.VMID).
		Str("socket", instance.SocketPath).
		Str("tap", instance.TapName).
		Str("guest_cidr", guestCIDR).
		Str("gateway", gateway).
		Msg("Started the task from a migration artifact")
	return nil
}

// keepDirective puts a consumed directive back with the instant it was prepared at, and
// returns the failure that made it necessary. A put-back that fails is reported
// alongside the cause: the restore failure is what the caller acts on, and the missing
// directive is what would make the retry boot cold instead.
func (c *Controller) keepDirective(
	cfg *MigrationRestoreConfig, directive scruntime.PendingRestore, cause error,
) error {
	if putErr := cfg.Pending.Put(directive); putErr != nil {
		return errors.Join(cause, fmt.Errorf(
			"the pending-restore directive for service %q could not be put back, so a retry starts a fresh VM instead of restoring artifact %s: %w",
			directive.ServiceID, directive.ArtifactID, putErr))
	}
	c.logger.Warn().
		Str("service_id", directive.ServiceID).
		Str("artifact_id", directive.ArtifactID).
		Time("created_at", directive.CreatedAt).
		Msg("Restoring the migrated task failed; the directive is kept so the retry restores the same artifact")
	return cause
}

// restoreDeps wires the restore's effects: the caller's restorer with this node's
// console log, the TAP lifecycle that leaves the device down until the guest has its
// new address, the guest agent behind the artifact's muxer, and the record store a
// teardown resolves the task through.
//
// Every effect comes from the config when it names one and from this node's packages
// otherwise, so production and a test drive the same restore with the same order.
func (c *Controller) restoreDeps(cfg *MigrationRestoreConfig) migration.RestoreDeps {
	tap := cfg.Tap
	if tap.Create == nil {
		tap.Create = network.CreateTAPDeviceDown
	}
	if tap.Up == nil {
		tap.Up = network.BringTAPUp
	}
	if tap.Delete == nil {
		tap.Delete = network.RemoveTAPDevice
	}

	agent := cfg.Agent
	if agent == nil {
		agent = migration.ProductionDialer
	}

	return migration.RestoreDeps{
		Snapshot:  consoleRestorer{restorer: cfg.Restorer, logDir: c.config.LogDir},
		TapCreate: tap.Create,
		TapUp:     tap.Up,
		TapDelete: tap.Delete,
		Agent:     agent,
		Registry:  c.restoredRecords(),
	}
}

// restoredRecords is the node's restored-instance store as a restore writes it. It is
// the store the executor was given for teardown: a record only one of the two paths
// could see would make a restored VM unresolvable by the other. It answers nil when the
// store is absent or cannot hold a record, which the restore reports as a missing
// effect before it creates anything.
func (c *Controller) restoredRecords() migration.RestoredInstanceStore {
	store, ok := c.restored.(migration.RestoredInstanceStore)
	if !ok {
		return nil
	}
	return store
}

// consoleRestorer adapts the node's ArtifactRestorer to what pkg/migration drives: a
// SnapshotRestorer that hands every load the console log of the task the VM runs as,
// and the stopper its undo needs.
type consoleRestorer struct {
	restorer ArtifactRestorer
	logDir   string
}

// RestoreFromSnapshot loads the snapshot with the console log path derived from the
// task the restore is for: the new task, not the source one inside the artifact.
func (r consoleRestorer) RestoreFromSnapshot(
	ctx context.Context, info *snapshot.SnapshotInfo, socketPath string,
) error {
	if info == nil {
		return errors.New("snapshot info is nil: want the artifact's state, memory and disk to load")
	}
	return r.restorer.RestoreArtifact(ctx, info, socketPath, vmConsolePath(r.logDir, info.TaskID))
}

// StopRestored stops the VM the restorer started for a task.
func (r consoleRestorer) StopRestored(taskID string) error {
	return r.restorer.StopRestored(taskID)
}

var (
	_ migration.SnapshotRestorer  = consoleRestorer{}
	_ migration.RestoredVMStopper = consoleRestorer{}
)

// vmConsolePath is the console log of a task: the same <logDir>/<taskID>.log a booted
// VM writes, which is the file `vm logs` reads. An empty log directory asks for no log.
func vmConsolePath(logDir, taskID string) string {
	if strings.TrimSpace(logDir) == "" || strings.TrimSpace(taskID) == "" {
		return ""
	}
	return filepath.Join(logDir, taskID+".log")
}

// restoredGuestAddress is the address and gateway a restored task's guest is given: the
// same two values the boot path would have put on the kernel command line
// (taskTranslatorImpl.networkBootArgs), read from the same attachment.
//
// Only the first attachment is read. A restored guest has one interface, eth0, which is
// the one interface this product gives a VM and the one the restore re-addresses, and
// the boot path hands eth0 the first attachment's first address. Taking an address from
// another attachment would put the guest on a network under a name it never used.
func restoredGuestAddress(task *types.Task, bridgeIP string) (cidr, gateway string, err error) {
	if task == nil || len(task.Networks) == 0 {
		return "", "", errors.New("the task has no network attachment: a restored guest must be given the address its task was allocated")
	}

	attachment := task.Networks[0]
	if len(attachment.Addresses) == 0 {
		return "", "", fmt.Errorf(
			"the task's first network attachment (%s) has no address: a restored guest must be given the address its task was allocated",
			networkLabel(attachment))
	}

	cidr, err = guestAddressPrefix(attachment.Addresses[0], attachment.Network.Spec.Subnet)
	if err != nil {
		return "", "", err
	}

	gateway = guestGateway(attachment.Network.Spec.Gateway, bridgeIP)
	if gateway == "" {
		return "", "", fmt.Errorf(
			"no gateway for the restored guest on network %s: set the network's gateway or the node's bridge address (--bridge-ip)",
			networkLabel(attachment))
	}
	return cidr, gateway, nil
}

// guestAddressPrefix turns one attachment address into the CIDR the guest interface is
// given. An address that carries its prefix length is used as it is; a bare address
// takes the prefix of the network's subnet, and /24 when the network names none — the
// same default the boot path's netmask falls back to.
func guestAddressPrefix(address, subnet string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("the task's network attachment names an empty address")
	}

	if strings.Contains(address, "/") {
		prefix, err := netip.ParsePrefix(address)
		if err != nil {
			return "", fmt.Errorf("task address %q is not an address prefix: want an IPv4 address and prefix length, for example 192.168.127.37/24: %w", address, err)
		}
		if !prefix.Addr().Is4() {
			return "", fmt.Errorf("task address %q is not IPv4: the guest interface this product gives a task is IPv4", address)
		}
		return prefix.String(), nil
	}

	ip, err := netip.ParseAddr(address)
	if err != nil {
		return "", fmt.Errorf("task address %q is not an IP address: want the address the task's network allocated: %w", address, err)
	}
	if !ip.Is4() {
		return "", fmt.Errorf("task address %q is not IPv4: the guest interface this product gives a task is IPv4", address)
	}

	bits := 24
	if trimmed := strings.TrimSpace(subnet); trimmed != "" {
		if prefix, prefixErr := netip.ParsePrefix(trimmed); prefixErr == nil && prefix.Addr().Is4() {
			bits = prefix.Bits()
		}
	}
	return netip.PrefixFrom(ip, bits).String(), nil
}

// guestGateway is the address the guest's default route points at: the network's own
// gateway when it has one — a user-defined network's address on the bridge the task
// joins — and otherwise the node's bridge address, without its prefix length. The boot
// path resolves the same two the same way, so a moved guest keeps the route it had.
func guestGateway(networkGateway, bridgeIP string) string {
	gateway := strings.TrimSpace(networkGateway)
	if gateway == "" {
		gateway = strings.TrimSpace(bridgeIP)
	}
	if i := strings.Index(gateway, "/"); i > 0 {
		gateway = gateway[:i]
	}
	return gateway
}

// networkLabel names an attachment in an error: the network's name when it has one,
// else the driver, which is enough to tell "the default bridge" from "a user network".
func networkLabel(attachment types.NetworkAttachment) string {
	if name := strings.TrimSpace(attachment.Network.Spec.Name); name != "" {
		return name
	}
	if driver := strings.TrimSpace(attachment.Network.Spec.Driver); driver != "" {
		return driver + " network"
	}
	return "unnamed network"
}
