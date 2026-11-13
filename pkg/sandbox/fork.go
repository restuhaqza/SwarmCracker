package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
	"github.com/restuhaqza/swarmcracker/pkg/golden"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

// Checkpointing a sandbox, and forking it.
//
// The two operations are one idea seen twice. A checkpoint is a consistent image of a
// running sandbox — its memory, its device state, its disk and the credential its guest
// still requires — and a fork is a new sandbox resumed from that image with an identity
// of its own. Everything a fork is made of already exists in this package: the network
// attach, the disk copy, the jailed launch, the readiness rule and the failure path are
// the ones `create` uses, and a fork that fails is freed the way a failed create is.
//
// What is genuinely new is the consistency step. A checkpoint is two images taken at one
// instant, and a guest that keeps writing between them produces a pair that disagree, so
// the guest is asked to stop its writers first (the agent's `quiesce` op) and released
// after. The report of that freeze is recorded with the checkpoint: a checkpoint whose
// consistency is unknown is visible as such rather than assumed.
//
// # Why a fork does not share its parent's disk
//
// A forked guest resumes with its parent's memory, page cache included, and then writes
// to its disk. If that disk were the parent's — or the checkpoint's — the clone's writes
// would land in the parent's filesystem, and the parent's would appear in the clone's.
// So every clone gets a disk of its own, copied from the checkpoint's copy while the
// guest is not running (the launcher stages that copy into the clone's jail, so the
// checkpoint's file is only ever read).

// Snapshotter creates the Firecracker images a checkpoint is made of, and removes the
// working copy it leaves behind. *snapshot.Manager is the production value; a test
// hands back an equivalent without a KVM.
type Snapshotter interface {
	CreateSnapshot(ctx context.Context, instanceID, socketPath string, opts snapshot.CreateOptions) (*snapshot.SnapshotInfo, error)
	DeleteSnapshot(ctx context.Context, snapshotID string) error
}

// checkpointsAvailable refuses a checkpoint or fork on a node that has no checkpoint
// store: half of one is worse than none, because it leaves images nothing can free.
func (m *Manager) checkpointsAvailable() error {
	if m.cfg.Checkpoints == nil || m.cfg.CheckpointBlobs == nil || m.cfg.Snapshots == nil {
		return fmt.Errorf("%w: this node keeps no sandbox checkpoints", ErrNotImplemented)
	}
	return nil
}

// ErrNotImplemented means the node was configured without the piece an operation needs.
var ErrNotImplemented = errors.New("sandbox: not implemented on this node")

// Checkpoint takes a consistent checkpoint of a ready sandbox and returns its record.
//
// The order is the guarantee:
//
//  1. The guest is asked to stop writing (quiesce), and is released however this
//     returns. A checkpoint taken without the freeze is one whose images can disagree,
//     so a guest that will not freeze fails the checkpoint rather than degrading it.
//  2. The images are taken while the guest is frozen and, inside the same window, the
//     VM is paused by the snapshot manager — the memory image and the disk copy are
//     therefore the same instant.
//  3. The memory image is stored in the D11 format (4 MiB content-addressed chunks and
//     a manifest carrying this node's pin), the state document and the disk copy are
//     moved into the checkpoint's own directory, and the guest's agent token is copied
//     beside them: a resumed guest still requires it.
//  4. The record is written last, so a checkpoint that failed half-way is not one a
//     fork can find.
func (m *Manager) Checkpoint(ctx context.Context, id, name string) (CheckpointRecord, error) {
	m.slots <- struct{}{}
	defer func() { <-m.slots }()

	if err := m.checkpointsAvailable(); err != nil {
		return CheckpointRecord{}, err
	}
	rec, err := m.ready(id)
	if err != nil {
		return CheckpointRecord{}, err
	}
	name, err = m.allocateCheckpointName(name)
	if err != nil {
		return CheckpointRecord{}, err
	}

	inst, found, err := m.cfg.Instances.Peek(rec.ID)
	if err != nil {
		return CheckpointRecord{}, fmt.Errorf("sandbox %s: read its instance record: %w", rec.ID, err)
	}
	if !found || inst.APISocket == "" {
		return CheckpointRecord{}, fmt.Errorf("%w: sandbox %s has no running VMM to checkpoint", ErrNotReady, rec.ID)
	}

	pin, err := golden.NodePin(ctx, m.cfg.FirecrackerBinary, rec.Kernel)
	if err != nil {
		return CheckpointRecord{}, fmt.Errorf("sandbox %s: read this node's pin: %w", rec.ID, err)
	}

	// Freeze before the images are taken, and release on every path out: a guest left
	// frozen has no writers and no way to get them back.
	frozen, err := m.quiesce(ctx, rec, agent.QuiesceFreeze)
	if err != nil {
		return CheckpointRecord{}, fmt.Errorf("sandbox %s: stop its writers before a checkpoint: %w", rec.ID, err)
	}
	defer m.release(ctx, rec)

	info, err := m.cfg.Snapshots.CreateSnapshot(ctx, rec.ID, inst.APISocket, snapshot.CreateOptions{
		JailRoot:   rec.JailRoot,
		RootfsPath: rec.Disk,
		VCPUCount:  rec.VCPUs,
		MemoryMB:   rec.MemoryMB,
		Metadata:   map[string]string{"sandbox": rec.ID, "checkpoint": name},
	})
	if err != nil {
		return CheckpointRecord{}, fmt.Errorf("sandbox %s: take its checkpoint: %w", rec.ID, err)
	}

	chk, err := m.publishCheckpoint(ctx, rec, name, frozen, pin, info)
	if err != nil {
		// The snapshot's own copy is the snapshot manager's to free until a checkpoint
		// record names it; a checkpoint that could not be published owns nothing.
		_ = m.cfg.Snapshots.DeleteSnapshot(ctx, info.ID)
		return CheckpointRecord{}, err
	}
	log.Info().
		Str("sandbox_id", rec.ID).
		Str("checkpoint", chk.Name).
		Str("quiesce", chk.Quiesce.Mode).
		Int64("size_bytes", chk.SizeBytes).
		Msg("Sandbox checkpoint is published")
	return chk, nil
}

// publishCheckpoint turns a snapshot manager's working copy into a checkpoint: the
// state document and the disk move into the checkpoint's own directory, the memory
// image becomes D11 chunks, and the record that names all of it is written last.
func (m *Manager) publishCheckpoint(
	ctx context.Context, rec Record, name string, frozen agent.QuiesceReport,
	pin checkpoint.Pin, info *snapshot.SnapshotInfo,
) (CheckpointRecord, error) {
	dir := m.cfg.Checkpoints.Path(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return CheckpointRecord{}, fmt.Errorf("checkpoint %q: create %s: %w", name, dir, err)
	}
	// A checkpoint that fails to publish leaves nothing behind: no directory, no manifest
	// and no chunk. It is not enough to remove the directory — the memory image is written
	// to the chunk store before the record exists, so a failure after that point would
	// leave chunks with no record to free them, and no delete a caller could run.
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(dir)
			m.discardCheckpointImages(ctx, name)
		}
	}()

	statePath := filepath.Join(dir, "vm.state")
	if err := moveFile(info.StatePath, statePath); err != nil {
		return CheckpointRecord{}, fmt.Errorf("checkpoint %q: keep its state document: %w", name, err)
	}
	diskPath := filepath.Join(dir, "disk.ext4")
	if err := moveFile(info.DiskPath, diskPath); err != nil {
		return CheckpointRecord{}, fmt.Errorf("checkpoint %q: keep its disk copy: %w", name, err)
	}

	// The memory image, in the format D11 fixes: 4 MiB content-addressed chunks with a
	// manifest of their digests and this node's pins.
	state, err := os.Open(statePath)
	if err != nil {
		return CheckpointRecord{}, fmt.Errorf("checkpoint %q: open its state document: %w", name, err)
	}
	defer func() { _ = state.Close() }()
	memory, err := os.Open(info.MemoryPath)
	if err != nil {
		return CheckpointRecord{}, fmt.Errorf("checkpoint %q: open its memory image: %w", name, err)
	}
	manifest, err := checkpoint.Write(ctx, m.cfg.CheckpointBlobs, name, pin, state, memory)
	_ = memory.Close()
	if err != nil {
		return CheckpointRecord{}, fmt.Errorf("checkpoint %q: store its memory image: %w", name, err)
	}

	// The guest's credential goes into the record last, after the images: everything
	// before this point is idempotent to redo, and a failure here is cleaned up by the
	// deferred discard like any other.
	token, found, err := agent.ReadTokenFile(rec.AgentTokenFile)
	if err != nil {
		return CheckpointRecord{}, fmt.Errorf("checkpoint %q: read the guest's agent token: %w", name, err)
	}
	if !found {
		return CheckpointRecord{}, fmt.Errorf("checkpoint %q: the sandbox has no agent token to carry", name)
	}

	chk := CheckpointRecord{
		Name:         name,
		Sandbox:      rec.ID,
		Template:     rec.Template,
		Kernel:       rec.Kernel,
		VCPUs:        rec.VCPUs,
		MemoryMB:     rec.MemoryMB,
		Egress:       append([]string(nil), rec.Egress...),
		Owner:        rec.Owner,
		Created:      m.now().UTC(),
		AgentVersion: rec.AgentVersion,
		BootID:       rec.BootID,
		Quiesce:      QuiesceOutcome{Mode: frozen.Mode, Mounts: frozen.Mounts},
		Mem:          manifest,
		State:        statePath,
		Disk:         diskPath,
		Token:        token,
	}
	if info, err := os.Stat(diskPath); err == nil {
		chk.SizeBytes = manifest.TotalSize + info.Size()
		if state, err := os.Stat(statePath); err == nil {
			chk.SizeBytes += state.Size()
		}
	}
	if err := m.cfg.Checkpoints.Put(chk); err != nil {
		return CheckpointRecord{}, err
	}
	published = true

	// The working copy's memory file is now the chunk store's; the rest of its
	// directory was moved. Removing the snapshot's own record is what keeps one
	// checkpoint from being two things in two indexes.
	if err := m.cfg.Snapshots.DeleteSnapshot(ctx, info.ID); err != nil {
		log.Warn().Err(err).Str("snapshot", info.ID).
			Msg("Checkpoint published, but its working copy could not be removed")
	}
	return chk, nil
}

// Checkpoints returns this node's checkpoints, newest last, and the names of the
// records that could not be read. A sandbox ID narrows the list to checkpoints taken
// from that sandbox.
func (m *Manager) Checkpoints(sandboxID string) ([]CheckpointRecord, []string, error) {
	if m.cfg.Checkpoints == nil {
		return nil, nil, fmt.Errorf("%w: this node keeps no sandbox checkpoints", ErrNotImplemented)
	}
	records, skipped, err := m.cfg.Checkpoints.List()
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(sandboxID) == "" {
		return records, skipped, nil
	}
	out := records[:0]
	for _, rec := range records {
		if rec.Sandbox == sandboxID {
			out = append(out, rec)
		}
	}
	return out, skipped, nil
}

// ReconcileCheckpoints reclaims the memory-image chunks no checkpoint names, and reports
// what it removed.
//
// It is what the daemon runs once before it serves anything. Deleting a checkpoint
// reclaims its own chunks, but a chunk can be orphaned without a delete ever happening: a
// checkpoint writes its chunks and then publishes the manifest that names them, so a
// process killed in between — a crash, a reboot, a power loss — leaves 4 MiB blocks that
// no manifest references and no record names, and nothing would ever trigger their
// reclamation. The manager's one-at-a-time slot protects against a race, not against a
// death; this pass is what closes that.
//
// It expects the node's single daemon to be the only writer, which is the same assumption
// every store in this package makes: a pass that ran while another process was mid-checkpoint
// would reclaim the chunks it had written but not yet named.
func (m *Manager) ReconcileCheckpoints(ctx context.Context) (checkpoint.PruneReport, error) {
	m.slots <- struct{}{}
	defer func() { <-m.slots }()

	if m.cfg.CheckpointBlobs == nil {
		return checkpoint.PruneReport{}, fmt.Errorf("%w: this node keeps no sandbox checkpoints", ErrNotImplemented)
	}
	report, err := m.cfg.CheckpointBlobs.Prune(ctx, 0)
	if err != nil {
		return report, fmt.Errorf("reclaim the checkpoint chunks nothing names: %w", err)
	}
	if report.Blobs != 0 {
		log.Info().
			Int("chunks", report.Blobs).
			Int64("bytes", report.Bytes).
			Msg("Reclaimed checkpoint chunks no checkpoint names")
	}
	return report, nil
}

// CheckpointByName returns one checkpoint of this node, or says it is not there. It is
// the read a fork route resolves its input with, and it does not care whether the
// sandbox the checkpoint was taken from still exists: a checkpoint is an image.
func (m *Manager) CheckpointByName(name string) (CheckpointRecord, error) {
	if m.cfg.Checkpoints == nil {
		return CheckpointRecord{}, fmt.Errorf("%w: this node keeps no sandbox checkpoints", ErrNotImplemented)
	}
	return m.checkpoint(name)
}

// CheckpointDeletion is what forgetting a checkpoint freed, and what it had to keep
// because other checkpoints still reference it.
//
// A caller is told both numbers rather than a bare "deleted": a checkpoint's memory image
// is shared chunk by chunk with every other checkpoint of the same sandbox, so freeing one
// does not return its size to the node, and a response that did not say so would leave an
// operator believing a gigabyte came back when a few kilobytes did.
type CheckpointDeletion struct {
	// Name is the checkpoint that was forgotten.
	Name string
	// StateBytes and DiskBytes are the checkpoint's own files: its state document and
	// its disk copy, which nothing else references.
	StateBytes int64
	DiskBytes  int64
	// ChunkBytes and Chunks are the memory image's chunks this deletion removed, because
	// no remaining checkpoint named them.
	ChunkBytes int64
	Chunks     int
	// FreedBytes is what the node actually got back.
	FreedBytes int64
	// RetainedBytes is the memory image the remaining checkpoints still reference, and
	// Retained is how many checkpoints remain. The figure is the sum of their manifests'
	// sizes, so it counts a chunk two checkpoints share twice: it is what they reference,
	// not a deduplicated disk figure.
	RetainedBytes int64
	Retained      int
}

// DeleteCheckpoint forgets one checkpoint: its record, its state document, its disk copy,
// its manifest, and the memory-image chunks nothing else references.
//
// # Why the chunks are reclaimed here and not by a sweep alone
//
// A chunk is shared: two checkpoints of one sandbox are almost the same 4 MiB chunks, and
// an image is hundreds of megabytes. So no checkpoint may free a chunk another one names,
// and a delete that only removed its own record would leave the node holding the whole
// memory image with nothing that could name it — the leak this call exists to prevent. It
// therefore marks by what remains: it removes the manifest first, then asks the store to
// reclaim the chunks no manifest references any more (pkg/checkpoint's Prune, with no
// minimum age: a chunk that is unreferenced after this deletion is unreferenced).
//
// A checkpoint is taken and forked under the manager's one-at-a-time slot, so holding it
// here is what keeps a reclaim from racing a checkpoint whose chunks are written but not
// yet named by a manifest.
func (m *Manager) DeleteCheckpoint(ctx context.Context, name string) (CheckpointDeletion, error) {
	m.slots <- struct{}{}
	defer func() { <-m.slots }()

	if m.cfg.Checkpoints == nil {
		return CheckpointDeletion{}, fmt.Errorf("%w: this node keeps no sandbox checkpoints", ErrNotImplemented)
	}
	rec, err := m.checkpoint(name)
	if err != nil {
		return CheckpointDeletion{}, err
	}

	released := CheckpointDeletion{Name: name}
	for _, file := range []struct {
		path string
		into *int64
	}{{rec.State, &released.StateBytes}, {rec.Disk, &released.DiskBytes}} {
		if info, err := os.Stat(file.path); err == nil {
			*file.into = info.Size()
		}
	}

	// The record goes first: a checkpoint that is half-forgotten (files gone, record
	// there) must not be one a fork can find, and the record is what a fork resolves.
	if err := m.cfg.Checkpoints.Delete(name); err != nil {
		return CheckpointDeletion{}, err
	}
	if err := m.cfg.CheckpointBlobs.Delete(ctx, name); err != nil {
		return released, fmt.Errorf("checkpoint %q: forget its manifest: %w", name, err)
	}

	// Reclaim what that left behind. A chunk another checkpoint names is kept, which is
	// what makes deleting one of two forks cheap rather than destructive.
	if report, err := m.cfg.CheckpointBlobs.Prune(ctx, 0); err != nil {
		return released, fmt.Errorf("checkpoint %q: reclaim the chunks it left: %w", name, err)
	} else {
		released.ChunkBytes, released.Chunks = report.Bytes, report.Blobs
	}

	released.Retained, released.RetainedBytes, err = m.retainedImages(ctx)
	if err != nil {
		return released, err
	}
	released.FreedBytes = released.StateBytes + released.DiskBytes + released.ChunkBytes
	log.Info().
		Str("checkpoint", name).
		Str("sandbox_id", rec.Sandbox).
		Int64("freed_bytes", released.FreedBytes).
		Int64("retained_bytes", released.RetainedBytes).
		Int("retained", released.Retained).
		Msg("Sandbox checkpoint is forgotten")
	return released, nil
}

// discardCheckpointImages removes what a failed checkpoint wrote to the chunk store: its
// manifest, and then the chunks nothing else names.
//
// It is the same mark-and-sweep a delete runs, for the same reason: a chunk is shared, so
// a checkpoint may only free the ones no surviving manifest references. A failure here is
// logged rather than returned — the caller is already returning the failure that brought it
// here — and the daemon's start pass is the second net.
func (m *Manager) discardCheckpointImages(ctx context.Context, name string) {
	if m.cfg.CheckpointBlobs == nil {
		return
	}
	if err := m.cfg.CheckpointBlobs.Delete(ctx, name); err != nil {
		log.Warn().Err(err).Str("checkpoint", name).
			Msg("Could not remove the manifest of a checkpoint that failed to publish")
		return
	}
	if report, err := m.cfg.CheckpointBlobs.Prune(ctx, 0); err != nil {
		log.Warn().Err(err).Str("checkpoint", name).Int("chunks", report.Blobs).Int64("bytes", report.Bytes).
			Msg("Could not reclaim the chunks of a checkpoint that failed to publish")
	}
}

// retainedImages reports how many checkpoints remain and how much memory image they
// reference, so a delete can say what it did not free.
func (m *Manager) retainedImages(ctx context.Context) (int, int64, error) {
	ids, err := m.cfg.CheckpointBlobs.List(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list the checkpoints whose images remain: %w", err)
	}
	var total int64
	for _, id := range ids {
		manifest, err := m.cfg.CheckpointBlobs.Manifest(ctx, id)
		if err != nil {
			return len(ids), total, fmt.Errorf("read the manifest of %s: %w", id, err)
		}
		total += manifest.TotalSize
	}
	return len(ids), total, nil
}

// ForkRequest is what a caller asks for: the checkpoint to fork, and how many.
type ForkRequest struct {
	// Checkpoint names the checkpoint to resume. It may have been taken from a
	// sandbox that no longer exists: a checkpoint is an image, not a child of a
	// record.
	Checkpoint string
	// Count is how many clones to create. Zero means one.
	Count int
	// Owner overrides the clones' owner. Empty inherits the checkpoint's.
	Owner string
	// TTL is the clones' idle TTL. Zero means the node's default; a negative value
	// means never reap them.
	TTL time.Duration
	// IDs names the clones, in order. Empty mints them. A caller that supplies them
	// must supply `Count` of them, because a fork that named some of its clones and
	// minted others would be two rules in one call.
	IDs []string
}

// ForkFromSandbox takes a fresh checkpoint of a running sandbox and forks it. It is the
// one-step form of `checkpoint` then `fork`, for a caller that wants clones of what is
// running now rather than of an image it named earlier; the checkpoint it takes is
// recorded under a minted name and is visible in a listing like any other.
func (m *Manager) ForkFromSandbox(ctx context.Context, sandboxID string, req ForkRequest) ([]Record, error) {
	chk, err := m.Checkpoint(ctx, sandboxID, "")
	if err != nil {
		return nil, err
	}
	req.Checkpoint = chk.Name
	return m.Fork(ctx, req)
}

// Fork creates new sandboxes from a checkpoint, and returns their records once each
// clone's guest agent has answered.
//
// The clones are made one at a time, and each is made the way `create` makes a sandbox:
// its own network namespace and policy (the checkpoint's allowlist), its own copy of the
// checkpoint's disk, its own per-boot identity, its own jail and cgroup. The parent — and
// the checkpoint — are read, never written: a fork cannot disturb what it came from.
//
// A clone that fails is freed and reported; the clones before it stand. That is the same
// rule `create` follows, and it is what lets a caller ask for three and use the two it
// got rather than losing them to the third.
func (m *Manager) Fork(ctx context.Context, req ForkRequest) ([]Record, error) {
	m.slots <- struct{}{}
	defer func() { <-m.slots }()

	if err := m.checkpointsAvailable(); err != nil {
		return nil, err
	}
	chk, err := m.checkpoint(req.Checkpoint)
	if err != nil {
		return nil, err
	}

	count := req.Count
	if count == 0 {
		count = 1
	}
	if count < 0 {
		return nil, fmt.Errorf("%w: a fork of %d sandboxes", ErrInvalid, count)
	}
	if len(req.IDs) != 0 && len(req.IDs) != count {
		return nil, fmt.Errorf("%w: %d ids for %d clones", ErrInvalid, len(req.IDs), count)
	}

	// The pin is checked before a single byte is materialized: an image captured on
	// another Firecracker or another kernel would resume into garbage, and the node
	// refuses it rather than finding out inside a guest.
	pin, err := golden.NodePin(ctx, m.cfg.FirecrackerBinary, chk.Kernel)
	if err != nil {
		return nil, fmt.Errorf("checkpoint %q: read this node's pin: %w", chk.Name, err)
	}
	if err := chk.Mem.VerifyPin(pin); err != nil {
		return nil, fmt.Errorf("checkpoint %q: %w", chk.Name, err)
	}

	staged, err := m.stageCheckpointImages(ctx, chk)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(staged.dir) }()

	owner := strings.TrimSpace(req.Owner)
	if owner == "" {
		owner = chk.Owner
	}

	forks := make([]Record, 0, count)
	for i := 0; i < count; i++ {
		requested := ""
		if len(req.IDs) != 0 {
			requested = req.IDs[i]
		}
		rec, err := m.forkOne(ctx, chk, staged, requested, owner, req.TTL)
		if err != nil {
			return forks, err
		}
		forks = append(forks, rec)
	}
	return forks, nil
}

// stagedImages is a checkpoint's state document and memory image as files, which is what
// a launch needs: the chunk store is the durable form, and a VMM reads a file.
//
// Materializing is per fork call rather than per clone: the launcher copies the files
// into each clone's jail, so one materialized pair serves every clone in the call.
// SBX-071's on-demand loader is what removes the copy altogether, and until then this is
// the honest cost of resuming from a chunked image.
type stagedImages struct {
	dir   string
	state string
	mem   string
}

// stageCheckpointImages materializes a checkpoint's images under this node's rootfs
// directory, so the copy a launch stages into a jail is on the same filesystem.
func (m *Manager) stageCheckpointImages(ctx context.Context, chk CheckpointRecord) (stagedImages, error) {
	root := filepath.Join(m.cfg.RootfsDir, DirName)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return stagedImages{}, fmt.Errorf("checkpoint %q: stage its images: %w", chk.Name, err)
	}
	dir, err := os.MkdirTemp(root, ".fork-images-")
	if err != nil {
		return stagedImages{}, fmt.Errorf("checkpoint %q: stage its images: %w", chk.Name, err)
	}
	staged := stagedImages{dir: dir, state: filepath.Join(dir, "vm.state"), mem: filepath.Join(dir, "vm.mem")}

	state, err := os.Open(chk.State)
	if err != nil {
		_ = os.RemoveAll(dir)
		return stagedImages{}, fmt.Errorf("checkpoint %q: read its state document: %w", chk.Name, err)
	}
	defer func() { _ = state.Close() }()

	// Read verifies the state document against the manifest by consuming it, so the
	// staged copy is written from a rewound handle: a staged state file at EOF would be
	// an empty file the VMM then fails to load.
	image, err := checkpoint.Read(ctx, m.cfg.CheckpointBlobs, chk.Mem, state)
	if err != nil {
		_ = os.RemoveAll(dir)
		return stagedImages{}, fmt.Errorf("checkpoint %q: %w", chk.Name, err)
	}
	defer func() { _ = image.Close() }()
	if _, err := state.Seek(0, io.SeekStart); err != nil {
		_ = os.RemoveAll(dir)
		return stagedImages{}, fmt.Errorf("checkpoint %q: rewind its state document: %w", chk.Name, err)
	}

	if err := copyStreamTo(staged.state, state); err != nil {
		_ = os.RemoveAll(dir)
		return stagedImages{}, fmt.Errorf("checkpoint %q: stage its state document: %w", chk.Name, err)
	}
	if err := copyStreamTo(staged.mem, image); err != nil {
		_ = os.RemoveAll(dir)
		return stagedImages{}, fmt.Errorf("checkpoint %q: stage its memory image: %w", chk.Name, err)
	}
	return staged, nil
}

// forkOne is one clone, built the way a create builds a sandbox: record, network, disk,
// launch, ready — with the images and the credential the checkpoint supplies.
func (m *Manager) forkOne(
	ctx context.Context, chk CheckpointRecord, staged stagedImages, requested, owner string, ttl time.Duration,
) (Record, error) {
	id, err := m.allocateID(requested)
	if err != nil {
		return Record{}, err
	}
	rec := m.newForkRecord(id, chk, owner, ttl, m.size(chk.VCPUs, chk.MemoryMB))

	// The record exists before anything is allocated under it, exactly as in create:
	// a fork that dies half-way leaves a record whose teardown knows what to free.
	if err := m.cfg.Store.Put(rec); err != nil {
		return Record{}, err
	}
	if err := m.buildFork(ctx, &rec, chk, staged); err != nil {
		return m.fail(ctx, &rec, err)
	}
	return rec, nil
}

// newForkRecord is the record a clone starts from: the checkpoint's template, kernel,
// allowlist and size, its own identity, and the checkpoint it came from.
func (m *Manager) newForkRecord(id string, chk CheckpointRecord, owner string, ttl time.Duration, size sandboxSize) Record {
	now := m.now().UTC()

	if ttl < 0 {
		ttl = 0
	}
	if ttl == 0 {
		ttl = m.cfg.DefaultTTL
	}

	return Record{
		ID:             id,
		State:          StateCreated,
		Template:       chk.Template,
		Owner:          owner,
		Created:        now,
		Updated:        now,
		LastActive:     now,
		IdleTTLSeconds: int64(ttl / time.Second),
		Egress:         append([]string(nil), chk.Egress...),
		Kernel:         chk.Kernel,
		VCPUs:          size.vcpus,
		MemoryMB:       size.memoryMB,
		ForkedFrom:     chk.Name,
	}
}

// buildFork gives a clone its own network and disk, launches it from the checkpoint, and
// records it ready once its guest agent has answered.
//
// The disk is the clone's own copy of the checkpoint's copy. The launcher stages a copy
// into the jail anyway; the host copy is what the record names and what a teardown frees,
// so the checkpoint's file is never the thing a sandbox owns.
func (m *Manager) buildFork(ctx context.Context, rec *Record, chk CheckpointRecord, staged stagedImages) error {
	network, err := m.cfg.Network.Attach(ctx, rec.ID, rec.Egress)
	if err != nil {
		return err
	}
	a := allocated{network: network, token: chk.Token}
	rec.NetNS, rec.TAP = network.NetNS, network.Tap
	rec.GuestIP, rec.MAC = network.GuestIP.String(), network.MAC
	if err := m.save(rec); err != nil {
		return err
	}

	disk := m.diskPath(rec.ID)
	mode, err := snapshot.CopyDisk(ctx, chk.Disk, disk)
	if err != nil {
		return fmt.Errorf("sandbox %s: give the clone its own disk from checkpoint %s: %w", rec.ID, chk.Name, err)
	}
	a.disk = disk
	rec.Disk, rec.DiskCopy = disk, string(mode)
	if err := m.save(rec); err != nil {
		return err
	}

	launched, err := m.cfg.Launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: rec.ID,
		Mode:       snapshot.ModeFork,
		Kernel:     chk.Kernel,
		Rootfs:     disk,
		State:      staged.state,
		Mem:        staged.mem,
		VCPUs:      rec.VCPUs,
		MemoryMB:   rec.MemoryMB,
		NetNS:      network.NetNS,
		VsockPath:  m.chrootVsockPath(rec.ID),
		AgentToken: chk.Token,
		Net: []snapshot.NetworkInterface{{
			IfaceID:  ifaceID,
			HostTap:  network.Tap,
			GuestMAC: network.MAC,
		}},
		Identity: m.identity(rec.ID, network, chk.Token),
		Record:   vminstanceRecord(rec, a, ""),
	})
	if err != nil {
		return fmt.Errorf("sandbox %s: resume checkpoint %s: %w", rec.ID, chk.Name, err)
	}

	// The clone's guest released the freeze its image carried when its post-restore hook
	// ran, and this is the same release asked for directly: "ready" has to mean the
	// clone's writers run again, not that they are merely able to. It is idempotent, and
	// a guest that does not implement quiesce — an older template, which cannot have
	// been frozen — is not a failure.
	if _, err := m.quiesce(ctx, *rec, agent.QuiesceThaw); err != nil {
		log.Warn().Err(err).Str("sandbox_id", rec.ID).
			Msg("Could not confirm that the clone's writers were released after its resume")
	}
	return m.markReady(rec, a, launched)
}

// checkpoint resolves a checkpoint by name, or says it is not there.
func (m *Manager) checkpoint(name string) (CheckpointRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return CheckpointRecord{}, fmt.Errorf("%w: a checkpoint is required", ErrInvalid)
	}
	chk, found, err := m.cfg.Checkpoints.Get(name)
	if err != nil {
		return CheckpointRecord{}, err
	}
	if !found {
		return CheckpointRecord{}, fmt.Errorf("%w: checkpoint %s", ErrNotFound, name)
	}
	return chk, nil
}

// allocateCheckpointName settles a checkpoint's name: the caller's, or a minted one, and
// never one that is already a checkpoint.
func (m *Manager) allocateCheckpointName(requested string) (string, error) {
	name := strings.TrimSpace(requested)
	if name == "" {
		minted, err := m.mintID()
		if err != nil {
			return "", err
		}
		name = "c-" + minted
	}
	if err := checkID(name); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, taken, err := m.cfg.Checkpoints.Get(name); err != nil {
		return "", err
	} else if taken {
		return "", fmt.Errorf("%w: checkpoint %s", ErrExists, name)
	}
	return name, nil
}

// askGuestToQuiesce is how the node asks a sandbox's guest to stop or release its
// writers. Production dials the guest's own muxer and speaks the agent protocol; it is
// a seam because that muxer is a vsock device a unit test cannot build, and the
// decision the caller makes from the answer — refuse the checkpoint, or take it and
// release the guest again — is what has to be tested.
var askGuestToQuiesce = func(ctx context.Context, rec Record, action string) (agent.QuiesceReport, error) {
	client, err := agent.Dial(ctx, rec.VsockPath, agent.DefaultPort)
	if err != nil {
		return agent.QuiesceReport{}, fmt.Errorf("reach its guest agent: %w", err)
	}
	defer func() { _ = client.Close() }()
	return client.Quiesce(ctx, action)
}

// quiesce asks a sandbox's guest to stop (or release) its writers.
func (m *Manager) quiesce(ctx context.Context, rec Record, action string) (agent.QuiesceReport, error) {
	return askGuestToQuiesce(ctx, rec, action)
}

// release thaws a guest this call froze. It runs on every path out of a checkpoint, and
// its failure is logged rather than returned: the checkpoint's own outcome is what the
// caller asked for, and a guest whose writers stayed blocked is a condition the node must
// report, not one the caller can act on.
func (m *Manager) release(ctx context.Context, rec Record) {
	// The guest may be mid-resume; give the release its own budget.
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	report, err := m.quiesce(releaseCtx, rec, agent.QuiesceThaw)
	if err != nil {
		log.Warn().Err(err).Str("sandbox_id", rec.ID).
			Msg("Could not release the writers of a sandbox that was checkpointed; it is still frozen")
		return
	}
	log.Info().Str("sandbox_id", rec.ID).Str("mounts", strings.Join(report.Mounts, ",")).
		Msg("Sandbox writers released after a checkpoint")
}

// moveFile moves one file, falling back to a copy when the two paths are on different
// filesystems (the snapshot manager's directory and the checkpoint directory normally
// are not, but a node may be configured so).
func moveFile(from, to string) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	if err := copyStreamTo(to, src); err != nil {
		return err
	}
	return os.Remove(from)
}

// copyStreamTo writes a stream to path with the mode a checkpoint file has: readable by
// the daemon alone.
func copyStreamTo(path string, src io.Reader) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, src); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
