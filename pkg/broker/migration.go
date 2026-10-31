package broker

// This file owns the node-side artifact transport of a live migration: producing
// an artifact from a running task, serving one of its files, staging a file a
// restore will use, discarding an artifact when it is no longer needed, and
// recording the directive that tells the node to restore one.
//
//	POST   /v1/tasks/{taskID}/migration/export
//	GET    /v1/tasks/{taskID}/migration/file?artifactId=<id>&name=<file>
//	PUT    /v1/tasks/{taskID}/migration/stage?artifactId=<id>&name=<file>&size=<n>&sha256=<hex>
//	DELETE /v1/tasks/{taskID}/migration/discard?artifactId=<id>
//	POST   /v1/tasks/{taskID}/migration/prepare
//	DELETE /v1/tasks/{taskID}/migration/unprepare?serviceId=<id>&artifactId=<id>
//	POST   /v1/tasks/{taskID}/migration/reanneal
//
// The restore itself is not here: loading the artifact into a new task creates the
// TAP device, loads the snapshot and re-addresses the guest, and that has its own
// operation.
//
// # Artifact-scoped, not task-scoped
//
// An artifact is addressed by its own ID. The route still names a task - it is one
// path shape for every node operation - but only export uses it: export snapshots
// the task it names, so the server resolves that task and a task this node does not
// have is a 404. file, stage, discard, prepare, unprepare and reanneal never read
// the task, because the artifact (and the directive that restores it) outlives it:
// SwarmKit replaces the task as part of the move, and a prepare, a stage or a
// re-anneal on the target happens for a task the target has never run.
//
// # One artifact, one directory
//
// An artifact is addressed by its own ID, so the files that move live in
// <artifact root>/<artifactID>/ on both sides of the move: the source publishes the
// directory, the target stages into it, and a name inside it is one bare file name
// the manifest describes. The directory is not part of the artifact: it is where a
// node keeps the bytes, and no other node may assume it.
//
// The directory is real, and it holds the manifest (artifact.json) plus the
// artifact's files. The snapshot store keeps its own directory for the same bytes,
// and the two are joined by hard links: the artifact root sits on the same
// filesystem as the snapshot root in the default install (--migration-staging-dir
// beside --snapshot-dir), so publishing an artifact costs directory entries rather
// than a second copy of a multi-GB memory image. When the two roots are not on one
// filesystem the link is refused by the kernel and the file is copied instead, and
// the export document says which happened (Linked).
//
// The consequences are deliberate:
//
//   - The artifact is not a link to the snapshot. A symbolic link would dangle the
//     moment the store cleaned its snapshot up, and a symlinked directory has its
//     own semantics for rename and removal, which is the wrong shape for both the
//     transfer and the cleanup.
//   - The store's housekeeping cannot reach the artifact: an artifact directory is
//     not a snapshot name, so a manager restart's leftover sweep and the retention
//     policy ignore it, and deleting the snapshot leaves the artifact's inodes (and
//     therefore its bytes) alive until it is discarded.
//   - Discarding is therefore the only thing that frees an artifact's bytes, and
//     migration/discard is that operation: it removes the directory and the
//     snapshot behind it. Nothing waits for retention any more.
//
// Export requires the node's snapshot store, so it is available where snapshots
// are (--snapshots-enable). Without a store the operation stays registered and
// answers a typed 501, exactly like the snapshots operation. file, stage and
// discard keep working without one: they move and remove bytes an export already
// produced.
//
// # Trust
//
// Both file operations take an artifact ID and a file name from the request. The
// ID must be one safe path segment in the shape pkg/migration generates, and the
// name must be one bare file name by the one rule in pkg/migration.ValidFileName;
// both are checked before any join, so neither can address anything outside the
// directory it names. The directory itself must be a real directory: a symbolic
// link or a plain file under the artifact root answers the same 404 a missing
// artifact does, so nothing outside the root can answer for an artifact. A file
// inside it is opened without following a link, because a planted symlink would
// serve whatever it points at.
//
// On the source the manifest is the whitelist: the artifact directory also holds
// the manifest itself, and only the files it describes may leave the node. A name
// that is not one of them is refused, whichever task the route happens to name.
//
// On the target the name is not checked against a manifest, because the manifest
// travels as a document rather than as a file. Instead the reserved manifest name
// is refused outright, and a staged file exists only after its size and its
// SHA-256 matched the request: a restore either finds a complete artifact or
// none. Restaging the same bytes succeeds (the checksum matches again) and a
// differing body replaces nothing (the check happens before the rename).
//
// Discard is keyed by the artifact, not by the task: after a completed migration
// the task that exported the artifact is gone, and its artifact is still the thing
// to delete. It is idempotent, because a rollback and a success path may both run
// it: a missing directory and a snapshot the store no longer holds are both "already
// deleted", and the snapshot it deletes is the one the manifest names, so it can
// never remove another dump of the same task.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

const (
	// MigrationExportOperation snapshots a running task and describes the
	// artifact it produced. It is a POST because it produces one.
	MigrationExportOperation = "migration/export"
	// MigrationFileOperation streams one file of an artifact this node exported.
	MigrationFileOperation = "migration/file"
	// MigrationStageOperation stores one file of an artifact on the node that
	// will restore it.
	MigrationStageOperation = "migration/stage"
	// MigrationDiscardOperation removes an artifact and the snapshot it was made
	// from, so an artifact's bytes are freed as soon as nobody needs them
	// instead of waiting for the snapshot store's retention.
	MigrationDiscardOperation = "migration/discard"
	// MigrationPrepareOperation records the directive that tells this node to
	// restore an artifact into the next task of a service that starts here. It is
	// a POST because it produces one, and it names the task slot in the route
	// without reading it: the task it describes does not exist yet.
	MigrationPrepareOperation = "migration/prepare"
	// MigrationUnprepareOperation removes that directive again, so an abandoned
	// migration does not leave a service's next task restoring an artifact that has
	// already been discarded. It is a DELETE because it removes one, and it names the
	// task slot in the route without reading it for the same reason prepare does:
	// the task a migration was preparing for may never exist.
	//
	// It is artifact-scoped in the strict sense: the query names the artifact whose
	// directive the caller owns, and a directive naming another artifact is a 409
	// rather than a removal. Two managers can migrate one service, and the manager
	// that lost the race must not delete the winner's directive.
	MigrationUnprepareOperation = "migration/unprepare"
	// MigrationReannealOperation makes a node forget what it knew about a guest
	// that has moved: the neighbour entry for its address and the FDB entry for its
	// MAC on the overlay port, on the bridge its attachment's network is
	// materialised on. It is a POST because it performs an action on this node
	// rather than describing one, and it names the task slot in the route without
	// reading it: after a move the guest runs under another task, and every peer is
	// told about the guest, not about the task.
	//
	// It is best effort by contract: the guest is already up on its new node, and a
	// peer that kept a stale entry recovers on its own next ARP. A peer that cannot
	// run the commands answers an error the manager logs and moves on from.
	MigrationReannealOperation = "migration/reanneal"

	// migrationExportKind is the document kind migration/export answers. Like the
	// artifact itself it is an internal manager↔node document, so its kind lives
	// with the operation rather than in the public contract.
	migrationExportKind = "MigrationExport"

	// migrationArtifactDocumentName is the manifest's file name inside an artifact
	// directory. The source writes it; it is node-local metadata, never one of the
	// files that move, and the target refuses to stage a file under that name.
	migrationArtifactDocumentName = "artifact.json"

	// checksumHeader carries the SHA-256 of one transferred file in both
	// directions: file announces the digest it serves, stage echoes the digest it
	// verified.
	checksumHeader = "X-Checksum-Sha256"

	// artifactDirectoryMode is the mode of an artifact directory: only the daemon
	// reads one, and an artifact carries a guest's memory image, so the directory
	// is private (0700) as well as the files inside it.
	artifactDirectoryMode = 0o700

	// artifactFileMode is the mode of an artifact file this node copies. A memory
	// image may hold guest secrets, so a copy is never group- or world-readable;
	// a hard link keeps the mode pkg/snapshot already gave the file, and the
	// manifest is written 0600 for the same reason.
	artifactFileMode = 0o600

	// partialPrefix starts the name of a stage temporary in the migration root.
	// It carries the artifact ID so discard can remove what a crash mid-stage
	// left behind; it never ends in .json, so it is never mistaken for a record.
	partialPrefix = ".partial-"

	// maxArtifactDocumentBytes bounds the manifest this node reads back. The
	// document is a page at most; the cap stops a file planted in the artifact
	// directory from making the handler buffer an arbitrary size.
	maxArtifactDocumentBytes = 1 << 20

	// sha256HexChars is the length of a hex-encoded SHA-256 digest.
	sha256HexChars = 64

	// migrationPrepareDocumentBytes bounds the body migration/prepare accepts. The
	// document is two short strings; the cap stops a caller from making the handler
	// buffer an arbitrary body for a request that cannot be about anything larger.
	migrationPrepareDocumentBytes = 8 << 10

	// migrationPrepareMediaType is the media type of the prepare body. It is plain
	// JSON rather than the v1 document type: the directive is two fields, not a
	// versioned document the public contract describes.
	migrationPrepareMediaType = "application/json"

	// migrationReannealDocumentBytes bounds the body migration/reanneal accepts.
	// The document is two short strings; the cap stops a caller from making the
	// handler buffer an arbitrary body for a request that cannot be about anything
	// larger.
	migrationReannealDocumentBytes = 8 << 10
)

// linkArtifactFile is os.Link, and a test seam: a hard link is what publishing an
// artifact uses, and the only way to exercise the copy fallback hermetically is to
// make the kernel's answer injectable.
var linkArtifactFile = os.Link

// errManifestTooLarge marks a manifest longer than maxArtifactDocumentBytes. It
// is a distinct failure from an unreadable file: the file exists, and it is not a
// manifest this build wrote.
var errManifestTooLarge = errors.New("artifact manifest is too large")

// MigrationHandlerConfig configures the migration transport operations. The
// operations share one configuration because they describe one artifact layout.
type MigrationHandlerConfig struct {
	// Store is the node-local snapshot store export snapshots through, and the
	// one discard removes the snapshot from. Nil (or a typed nil) keeps the
	// operations registered and answers export a typed 501: a node without a
	// snapshot store cannot produce an artifact. file, stage and discard do not
	// need it: they move and remove bytes an export already produced.
	Store SnapshotStore
	// ArtifactDir is the node's migration root, the one directory a migration
	// uses in either direction: <ArtifactDir>/<artifactID> is a real directory
	// holding artifact.json and the artifact's files. Export publishes into it on
	// the source, stage writes into it on the target, file serves from it, and
	// discard removes it. It is one root rather than a published root plus a
	// received one because a node is both: the same cluster moves an artifact off
	// this node today and onto it tomorrow, and an artifact is addressed by its
	// own ID in either direction. It sits on the same filesystem as the snapshot
	// root, so a published file is a hard link to the snapshot's rather than a
	// copy.
	ArtifactDir string
	// RootfsDir is the executor's rootfs directory. A Firecracker snapshot records
	// the VM's block-device path but not the disk contents, so export records the
	// disk to copy: <RootfsDir>/tasks/<taskID>.ext4 for a task this node booted,
	// or the disk a restored task's record names (that VM's state file resolves
	// it, so its own per-task disk belongs to no VM). The same rule the snapshot
	// create operation applies. Empty refuses an export: an artifact without a disk
	// would restore a guest with no filesystem.
	RootfsDir string
	// Pending is the node's store of pending-restore directives: the prepare
	// operation records "the next task of this service to start here restores this
	// artifact" before SwarmKit has created that task, the executor consumes it once
	// at task start, and unprepare removes it when the migration is abandoned.
	// ArtifactDir is still needed beside it, because a directive names the staging
	// directory and the node derives that directory itself - <artifact root>/
	// <artifactID> - and refuses a prepare for an artifact it does not hold as a real
	// directory there. A nil store (a daemon that could not open it) answers prepare
	// and unprepare a typed 503: the node cannot keep the directive, so it must not
	// tell the manager it did.
	Pending *scruntime.PendingRestores
	// BridgeName is the node's L2 bridge, the device the re-anneal deletes a
	// guest's neighbour and FDB entries from. Empty keeps reanneal registered and
	// answering a typed 503: a node that was never told which bridge carries the
	// flat segment cannot forget anything about a guest on it.
	//
	// It is only the bridge of the flat segment. A task attached to a user-defined
	// network ran on that network's own bridge, so the re-anneal derives the bridge
	// from the attachment's network ID through NetworkByID and pkg/network's
	// AttachmentBridgeName, exactly as the restore path picks the bridge a restored
	// task's TAP joins. Deleting on this node's flat bridge instead would forget
	// nothing, because the guest was never on it.
	BridgeName string
	// Subnet is the node's flat L2 subnet (--subnet). AttachmentBridgeName uses it
	// to recognise a network carved from the flat segment - the ingress network is,
	// by design - and keep its attachments on BridgeName rather than on a second
	// gateway for the same range. Empty means the node was never given one.
	Subnet string
	// NetworkByID resolves a network ID to the network as this node knows it, so a
	// user-defined network's attachment is forgotten from the bridge that network
	// is materialised on here. The node answers nil for a network it has not
	// materialised (the flat segment, and a user network no local task uses), and
	// AttachmentBridgeName then answers BridgeName: a node with no bridge for that
	// network has no stale entry on it either.
	NetworkByID func(networkID string) (*types.Network, bool)
	// VXLANPeers is the overlay peer list this node floods to. After the re-anneal
	// deletes a moved guest's entries, the operation re-adds the all-zero flood
	// entry for each peer that lost it, so the next frame for that guest still
	// reaches every node. Empty means this node runs no overlay, and the re-anneal
	// is then only the two deletes.
	VXLANPeers []string
}

// MigrationExportDocument is the document migration/export answers: the manifest
// and the node-local directory that holds the files it describes.
type MigrationExportDocument struct {
	SchemaVersion int                `json:"schemaVersion"`
	Kind          string             `json:"kind"`
	Artifact      migration.Artifact `json:"artifact"`
	// Dir is the directory that holds the files, <ArtifactDir>/<artifactID>. It is
	// not part of the artifact: a target stages into its own root, and the two
	// nodes need not agree on a path.
	Dir string `json:"dir"`
	// Linked reports whether the artifact's files are hard links to the snapshot's
	// (true, the normal case) or copies (false: the two roots are on different
	// filesystems, so the export paid for the bytes twice). It is an observation
	// about this node, not part of the artifact, so it stays out of the manifest.
	Linked bool `json:"linked"`
}

// NewMigrationExportHandler builds the export operation.
func NewMigrationExportHandler(cfg MigrationHandlerConfig) Handler {
	if snapshotStoreDisabled(cfg.Store) {
		cfg.Store = nil
	}
	h := &migrationExportHandler{cfg: cfg}
	return h.serve
}

// NewMigrationFileHandler builds the file operation. It needs no store: it serves
// bytes an export already published.
func NewMigrationFileHandler(cfg MigrationHandlerConfig) Handler {
	h := &migrationFileHandler{cfg: cfg}
	return h.serve
}

// NewMigrationStageHandler builds the stage operation.
func NewMigrationStageHandler(cfg MigrationHandlerConfig) Handler {
	h := &migrationStageHandler{cfg: cfg}
	return h.serve
}

// NewMigrationDiscardHandler builds the discard operation. It needs no store to
// remove bytes; when one is configured it also deletes the snapshot behind them.
func NewMigrationDiscardHandler(cfg MigrationHandlerConfig) Handler {
	if snapshotStoreDisabled(cfg.Store) {
		cfg.Store = nil
	}
	h := &migrationDiscardHandler{cfg: cfg}
	return h.serve
}

// NewMigrationPrepareHandler builds the prepare operation. It needs the node's
// directive store and its migration root, and no snapshot store: it records where
// an already-staged artifact lives, and the restore that reads it is the executor's.
func NewMigrationPrepareHandler(cfg MigrationHandlerConfig) Handler {
	h := &migrationPrepareHandler{cfg: cfg}
	return h.serve
}

// NewMigrationUnprepareHandler builds the unprepare operation. It needs only the
// directive store: removing a directive names no artifact bytes and touches none.
func NewMigrationUnprepareHandler(cfg MigrationHandlerConfig) Handler {
	h := &migrationUnprepareHandler{cfg: cfg}
	return h.serve
}

// NewMigrationReannealHandler builds the re-anneal operation. It needs this node's
// bridge and, when it runs an overlay, its peer list: the commands it runs act on
// this node's own devices, and no request may name them.
func NewMigrationReannealHandler(cfg MigrationHandlerConfig) Handler {
	h := &migrationReannealHandler{cfg: cfg}
	return h.serve
}

// migrationExportHandler serves POST /v1/tasks/{taskID}/migration/export.
type migrationExportHandler struct {
	cfg MigrationHandlerConfig
}

// serve exports the resolved task: snapshot it through the node's store, describe
// the result, and publish a directory of its own holding the manifest and the
// files it describes. The task keeps running: the store resumes the VM before it
// returns, so the transfer happens while the source still serves.
//
// A failure after the store returned discards the dump it produced. The caller
// never receives an artifact ID it could discard itself, so an export that cannot
// finish must not leave bytes behind for the store's retention to find.
func (h *migrationExportHandler) serve(ctx context.Context, task Task, w http.ResponseWriter, r *http.Request) {
	if !operationSpecs[MigrationExportOperation].allows(r.Method) {
		writeOperationMethodNotAllowed(w, MigrationExportOperation, r.Method)
		return
	}
	if h.cfg.Store == nil {
		writeMigrationDisabled(w)
		return
	}
	if status, problem := h.configProblem(); problem != nil {
		writeTypedError(w, status, problem)
		return
	}

	scope, queryErr := parseExportQuery(r)
	if queryErr != nil {
		writeTypedError(w, http.StatusBadRequest, queryErr)
		return
	}
	if !taskRunning(task) {
		writeSnapshotTaskNotRunning(w, task.ID)
		return
	}
	if bodyErr := requireEmptyBody(w, r, MigrationExportOperation); bodyErr != nil {
		writeTypedError(w, http.StatusBadRequest, bodyErr)
		return
	}

	// The VM's identity decides three things: which disk to copy, and which task's
	// names the artifact writes for the TAP device and the vsock muxer. A task
	// this node booted answers with its own ID and its own per-task disk; a task
	// restored from a migration runs a VM whose Firecracker state names the source
	// task's devices and disk, so exporting it must describe that VM — anything
	// else would publish an artifact naming devices nobody owns.
	vmID, socketDir, diskPath := task.sourceVMIdentity(h.cfg.RootfsDir)

	info, err := h.cfg.Store.CreateSnapshot(ctx, task.ID, task.SocketPath, snapshot.CreateOptions{
		ServiceID:  scope.service,
		NodeID:     scope.node,
		RootfsPath: diskPath,
	})
	if err != nil {
		// pkg/snapshot failures name absolute paths and sockets; the shared
		// mapping keeps the classification and drops the text, exactly like the
		// snapshots operation.
		writeSnapshotStoreError(w, "export", err)
		return
	}
	if info == nil {
		writeError(w, http.StatusInternalServerError, output.CodeInternal,
			"task %q export returned no snapshot metadata", task.ID)
		return
	}

	// The store names the snapshot after the task it was asked about, which is the
	// right owner for the dump. The artifact describes the VM inside it, whose
	// sourceTaskId names the devices the state file resolves, so the snapshot is
	// described under the VM's ID. A copy keeps the store's metadata intact.
	described := *info
	described.TaskID = vmID

	artifact, err := migration.ArtifactFromSnapshot(&described, socketDir, 0)
	if err == nil {
		// Two identities travel. The artifact above describes the VM in the dump,
		// whose state file records the source task's devices; the export was
		// requested about task.ID, which is what the manager checks its own
		// request against. They differ when this node restored the VM from an
		// earlier migration, which is a chain's second hop and not a mistake.
		artifact, err = artifact.RequestedFor(task.ID)
	}
	if err != nil {
		// pkg/migration names the file it could not describe; only the
		// classification crosses the wire.
		discardSnapshot(ctx, h.cfg.Store, info.ID)
		writeError(w, http.StatusInternalServerError, output.CodeInternal,
			"task %q could not be described as a migration artifact", task.ID)
		return
	}

	sources := artifactFileSources(artifact, info)
	linked, err := publishArtifact(h.cfg.ArtifactDir, artifact, sources)
	if err != nil {
		discardSnapshot(ctx, h.cfg.Store, info.ID)
		writeError(w, http.StatusServiceUnavailable, output.CodeUnavailable,
			"task %q artifact could not be published on this node", task.ID)
		return
	}

	doc := MigrationExportDocument{
		SchemaVersion: v1.SchemaVersion,
		Kind:          migrationExportKind,
		Artifact:      artifact,
		Dir:           filepath.Join(h.cfg.ArtifactDir, artifact.ID),
		Linked:        linked,
	}
	body, encodeErr := encodeDocument(doc)
	if encodeErr != nil {
		// The artifact was published a moment ago and the caller will never learn
		// its ID, so the request cleans up after itself.
		_ = os.RemoveAll(doc.Dir)
		discardSnapshot(ctx, h.cfg.Store, info.ID)
		writeError(w, http.StatusInternalServerError, output.CodeInternal,
			"the migration document could not be encoded")
		return
	}

	writeDocument(w, http.StatusCreated, body)
}

// configProblem reports why this node cannot export at all, or nil. The template
// values are checked once, before the task is touched: a node without a rootfs
// directory would pause a task only to fail after the disk copy.
func (h *migrationExportHandler) configProblem() (int, *output.Error) {
	if h.cfg.ArtifactDir == "" {
		return http.StatusServiceUnavailable, directoryNotConfigured("artifact", "publish an artifact")
	}
	if h.cfg.RootfsDir == "" {
		return http.StatusServiceUnavailable, directoryNotConfigured("rootfs", "export a task disk")
	}
	return 0, nil
}

// migrationScope is the export's caller-supplied scope: the service whose single
// replica moves, and the node the snapshot was taken on. The node cannot recover
// either from the snapshot, and the manifest requires the node.
type migrationScope struct {
	service string
	node    string
}

// parseExportQuery enforces the export's query grammar: only service and node,
// once each, and node is required because the manifest records it.
func parseExportQuery(r *http.Request) (migrationScope, *output.Error) {
	var scope migrationScope

	query := r.URL.Query()
	for key, values := range query {
		switch key {
		case "service", "node":
		default:
			return scope, invalidMigrationParameter(key, "is not a migration export parameter: want service or node")
		}
		if len(values) != 1 {
			return scope, invalidMigrationParameter(key, "must be set exactly once")
		}
	}

	scope.service, scope.node = query.Get("service"), query.Get("node")
	for _, key := range []string{"service", "node"} {
		if value := query.Get(key); value != "" && !validSnapshotScope(value) {
			return scope, invalidMigrationParameter(key, "must be an opaque ID, not a path")
		}
	}
	if scope.node == "" {
		return scope, invalidMigrationParameter("node",
			"is required: the manifest records the node the snapshot was taken on")
	}
	return scope, nil
}

// artifactFileSource is one file the export publishes: the name the manifest gives
// it on the wire, and the snapshot-owned file it comes from.
type artifactFileSource struct {
	name string
	path string
}

// artifactFileSources pairs the manifest's entries with the snapshot files they
// describe, in field order. It takes the paths from the snapshot rather than
// joining the manifest's names under the snapshot directory: the manifest is what
// travels, and the export links the files it just described.
func artifactFileSources(artifact migration.Artifact, info *snapshot.SnapshotInfo) []artifactFileSource {
	sources := []artifactFileSource{
		{artifact.StateFile.Name, info.StatePath},
		{artifact.MemoryFile.Name, info.MemoryPath},
	}
	if artifact.DiskFile != (migration.File{}) {
		sources = append(sources, artifactFileSource{artifact.DiskFile.Name, info.DiskPath})
	}
	return sources
}

// publishArtifact makes the artifact a directory of its own: <root>/<artifactID>
// holding each described file (a hard link to the snapshot's, or a copy when the
// kernel refuses the link) and, last, the manifest. It reports whether the files
// are links.
//
// The directory is removed if any step fails, so a caller never sees an artifact
// directory without its complete manifest: a restore reads the manifest to decide
// whether an artifact is complete.
func publishArtifact(root string, artifact migration.Artifact, sources []artifactFileSource) (bool, error) {
	if err := os.MkdirAll(root, artifactDirectoryMode); err != nil {
		return false, err
	}
	dir := filepath.Join(root, artifact.ID)
	if err := os.Mkdir(dir, artifactDirectoryMode); err != nil {
		return false, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(dir)
		}
	}()

	linked := true
	for _, source := range sources {
		fileLinked, err := publishArtifactFile(source.path, filepath.Join(dir, source.name))
		if err != nil {
			return false, err
		}
		linked = linked && fileLinked
	}

	if err := writeArtifactDocument(dir, artifact); err != nil {
		return false, err
	}
	published = true
	return linked, nil
}

// publishArtifactFile puts one artifact file in place: a hard link when the kernel
// allows it, because the snapshot directory and the artifact root share a
// filesystem in the default install and a link costs one directory entry rather
// than a second copy of a multi-GB image, and a copy when it does not.
func publishArtifactFile(source, target string) (bool, error) {
	if err := linkArtifactFile(source, target); err == nil {
		return true, nil
	}
	if err := copyArtifactFile(source, target); err != nil {
		return false, err
	}
	return false, nil
}

// copyArtifactFile writes a private copy of source at target. It is the fallback
// path: slower and it doubles the bytes, but the artifact is the same one. The
// copy is 0600 because the bytes may be a guest's memory image.
func copyArtifactFile(source, target string) error {
	//nolint:gosec // G304: the path is a file of the snapshot this node just took.
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	//nolint:gosec // G304: target is this node's artifact root plus a manifest name.
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, artifactFileMode)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(target)
		return err
	}
	// Durable before it is addressable: the directory is published as one unit,
	// so a rename must never expose a name whose bytes are not on disk.
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(target)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
}

// writeArtifactDocument writes the manifest into dir with a temporary file and a
// rename, so a reader sees either no manifest or a complete one. The manifest is
// 0600 like the bytes it describes: it names the guest's disk, its vsock socket
// and its TAP device, and nothing outside the daemon has business reading it.
func writeArtifactDocument(dir string, artifact migration.Artifact) error {
	body, err := encodeDocument(artifact)
	if err != nil {
		return err
	}

	//nolint:gosec // G304: the directory is the artifact this node just published.
	tmp, err := os.CreateTemp(dir, "."+migrationArtifactDocumentName+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// The rename below consumes the temporary path; a failure leaves it behind
	// only until this deferred removal runs.
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp opens 0600; the chmod makes that mode explicit rather than a
	// property of the standard library.
	if err := os.Chmod(tmpPath, artifactFileMode); err != nil {
		return err
	}
	return os.Rename(tmpPath, filepath.Join(dir, migrationArtifactDocumentName))
}

// migrationFileHandler serves GET /v1/tasks/{taskID}/migration/file.
type migrationFileHandler struct {
	cfg MigrationHandlerConfig
}

// serve streams one file of an artifact this node published. The manifest bounds
// the answer: its size is the Content-Length and its digest is the header, so the
// caller can verify what it received without trusting the stream.
//
// The task is not read: this operation is artifact-scoped, and the task that
// exported the artifact may already have been replaced by the time the target
// pulls a file from it.
func (h *migrationFileHandler) serve(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
	if !operationSpecs[MigrationFileOperation].allows(r.Method) {
		writeOperationMethodNotAllowed(w, MigrationFileOperation, r.Method)
		return
	}
	if h.cfg.ArtifactDir == "" {
		writeTypedError(w, http.StatusServiceUnavailable, directoryNotConfigured("artifact", "serve an artifact"))
		return
	}

	request, queryErr := parseArtifactFileQuery(r)
	if queryErr != nil {
		writeTypedError(w, http.StatusBadRequest, queryErr)
		return
	}

	dir, status, problem := existingArtifactDir(h.cfg.ArtifactDir, request.artifactID)
	if problem != nil {
		writeTypedError(w, status, problem)
		return
	}
	artifact, status, problem := readArtifactDocument(dir)
	if problem != nil {
		writeTypedError(w, status, problem)
		return
	}

	entry, found := artifactFile(artifact, request.name)
	if !found {
		writeTypedError(w, http.StatusBadRequest, unknownArtifactFile(artifact, request.name))
		return
	}
	serveArtifactFile(w, dir, entry)
}

// existingArtifactDir resolves one artifact's own directory under root, and reports
// the typed answer when it is not one. Only a real directory is an artifact: a
// symbolic link, a plain file and a missing entry answer the same 404, because an
// artifact this node publishes is a directory that holds its files, and following a
// link would let an entry outside the root answer for an artifact.
func existingArtifactDir(root, artifactID string) (string, int, *output.Error) {
	dir := filepath.Join(root, artifactID)
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return dir, http.StatusNotFound, artifactNotAvailable()
	case err != nil:
		return dir, http.StatusServiceUnavailable, artifactUnreadable()
	case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return dir, http.StatusNotFound, artifactNotAvailable()
	}
	return dir, 0, nil
}

// artifactFileRequest is the file operation's query: which artifact, which file.
type artifactFileRequest struct {
	artifactID string
	name       string
}

// parseArtifactFileQuery enforces the file operation's query grammar: artifactId
// and name, once each, both required.
func parseArtifactFileQuery(r *http.Request) (artifactFileRequest, *output.Error) {
	var request artifactFileRequest

	query := r.URL.Query()
	for key, values := range query {
		switch key {
		case "artifactId", "name":
		default:
			return request, invalidMigrationParameter(key, "is not a migration file parameter: want artifactId and name")
		}
		if len(values) != 1 {
			return request, invalidMigrationParameter(key, "must be set exactly once")
		}
	}

	request.artifactID = query.Get("artifactId")
	if !migration.ValidArtifactID(request.artifactID) {
		return request, invalidArtifactID("artifactId", request.artifactID)
	}
	request.name = query.Get("name")
	if err := migration.ValidFileName(request.name); err != nil {
		return request, invalidArtifactFileName(request.name)
	}
	return request, nil
}

// readArtifactDocument reads and validates the manifest an artifact directory
// holds. A directory without one is not an artifact this node published, so it
// answers the same 404 a missing artifact does; a manifest that does not validate
// is this node's problem, not the caller's.
func readArtifactDocument(dir string) (migration.Artifact, int, *output.Error) {
	var artifact migration.Artifact

	body, err := readSmallFile(filepath.Join(dir, migrationArtifactDocumentName))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return artifact, http.StatusNotFound, artifactNotAvailable()
	case errors.Is(err, errManifestTooLarge):
		return artifact, http.StatusInternalServerError, artifactInvalid()
	case err != nil:
		return artifact, http.StatusServiceUnavailable, artifactUnreadable()
	}

	if err := json.Unmarshal(body, &artifact); err != nil {
		return artifact, http.StatusInternalServerError, artifactInvalid()
	}
	if err := artifact.Validate(); err != nil {
		// pkg/migration names the field and the absolute path it rejected; only the
		// classification crosses the wire.
		return artifact, http.StatusInternalServerError, artifactInvalid()
	}
	return artifact, 0, nil
}

// readSmallFile reads a file that must stay small, refusing a larger one rather
// than buffering it.
func readSmallFile(path string) ([]byte, error) {
	//nolint:gosec // G304: the path is this node's artifact root plus a fixed manifest name.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	body, err := io.ReadAll(io.LimitReader(f, maxArtifactDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxArtifactDocumentBytes {
		return nil, errManifestTooLarge
	}
	return body, nil
}

// artifactFile resolves one of the manifest's file entries by its wire name.
func artifactFile(artifact migration.Artifact, name string) (migration.File, bool) {
	for _, file := range artifactFiles(artifact) {
		if file.Name == name {
			return file, true
		}
	}
	return migration.File{}, false
}

// artifactFiles lists the manifest's file entries in field order. The disk copy is
// optional: a snapshot that shipped none describes none, and the restore refuses
// such an artifact on its own terms.
func artifactFiles(artifact migration.Artifact) []migration.File {
	files := []migration.File{artifact.StateFile, artifact.MemoryFile}
	if artifact.DiskFile != (migration.File{}) {
		files = append(files, artifact.DiskFile)
	}
	return files
}

// serveArtifactFile streams one manifest entry from dir. The file must be exactly
// the size the manifest describes: a file that changed after the export is a
// conflict, because the checksum the caller was promised no longer describes what
// it would receive.
//
// It is opened without following a symbolic link: the manifest names a file in this
// directory, and a link planted there would serve whatever it points at - another
// artifact, another task's disk, or a file outside the root entirely - under a
// checksum that describes neither.
func serveArtifactFile(w http.ResponseWriter, dir string, entry migration.File) {
	path := filepath.Join(dir, entry.Name)
	//nolint:gosec // G304: the directory is this node's artifact root and the name is one manifest entry.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			writeTypedError(w, http.StatusNotFound, artifactNotAvailable())
		case errors.Is(err, syscall.ELOOP):
			writeTypedError(w, http.StatusConflict, artifactFileNotRegular(entry.Name))
		default:
			writeTypedError(w, http.StatusServiceUnavailable, artifactUnreadable())
		}
		return
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		writeTypedError(w, http.StatusServiceUnavailable, artifactUnreadable())
		return
	}
	if !stat.Mode().IsRegular() {
		writeTypedError(w, http.StatusConflict, artifactFileNotRegular(entry.Name))
		return
	}
	if stat.Size() != entry.Size {
		writeTypedError(w, http.StatusConflict, output.NewErrorf(output.CodeConflict,
			"artifact file %q is %d bytes: the manifest describes %d", entry.Name, stat.Size(), entry.Size).
			WithDetails(map[string]any{"reason": "artifact_changed", "size": entry.Size}))
		return
	}

	w.Header().Set("Content-Type", octetStream)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
	w.Header().Set(checksumHeader, entry.SHA256)
	w.WriteHeader(http.StatusOK)
	// The target verifies the digest while it stages the file, so hashing here
	// would read every byte of a multi-GB image twice for an answer it re-checks
	// anyway. A failure after the header has started truncates the stream, which
	// the caller detects as a short read against the Content-Length above.
	_, _ = io.Copy(w, io.LimitReader(f, entry.Size))
}

// migrationStageHandler serves PUT /v1/tasks/{taskID}/migration/stage.
type migrationStageHandler struct {
	cfg MigrationHandlerConfig
}

// artifactStageRequest is the stage operation's query: which artifact, which file,
// how large it is and what its digest must be.
type artifactStageRequest struct {
	artifactID string
	name       string
	size       int64
	sha256     string
}

// serve stages one file a restore will use. The parameters and the body are
// validated before the artifact directory exists: the body goes to a temporary in
// the migration root, is checked against the declared size and digest, and only
// then is the artifact directory created and the name renamed into it. A refused
// stage therefore leaves nothing behind - not even an empty directory - and the
// operation is idempotent because the same bytes always pass the same check.
func (h *migrationStageHandler) serve(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
	if !operationSpecs[MigrationStageOperation].allows(r.Method) {
		writeOperationMethodNotAllowed(w, MigrationStageOperation, r.Method)
		return
	}
	if h.cfg.ArtifactDir == "" {
		writeTypedError(w, http.StatusServiceUnavailable, directoryNotConfigured("staging", "stage an artifact"))
		return
	}

	request, queryErr := parseStageQuery(r)
	if queryErr != nil {
		writeTypedError(w, http.StatusBadRequest, queryErr)
		return
	}
	// A declared length above the size the caller is staging is refused before the
	// body is read; a chunked body is bounded while it is read instead.
	if r.ContentLength > request.size {
		writeTypedError(w, http.StatusRequestEntityTooLarge, payloadTooLarge(request.size))
		return
	}

	if status, problem := stageArtifact(w, r, h.cfg.ArtifactDir, request); problem != nil {
		writeTypedError(w, status, problem)
		return
	}

	w.Header().Set(checksumHeader, request.sha256)
	w.WriteHeader(http.StatusNoContent)
}

// ensureArtifactDir creates the artifact directory a stage publishes into, and
// refuses one that exists as anything but a real directory: a symbolic link
// planted there would move a staged name outside the artifact root, and a file is
// not an artifact directory either.
func ensureArtifactDir(dir string) (int, *output.Error) {
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return http.StatusConflict, artifactDirectoryUnsafe()
		}
		return 0, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return http.StatusServiceUnavailable, stagingUnavailable()
	}
	if err := os.MkdirAll(dir, artifactDirectoryMode); err != nil {
		return http.StatusServiceUnavailable, stagingUnavailable()
	}
	return 0, nil
}

// parseStageQuery enforces the stage operation's query grammar: artifactId, name,
// size and sha256, once each, all required.
func parseStageQuery(r *http.Request) (artifactStageRequest, *output.Error) {
	var request artifactStageRequest

	query := r.URL.Query()
	for key, values := range query {
		switch key {
		case "artifactId", "name", "size", "sha256":
		default:
			return request, invalidMigrationParameter(key, "is not a migration stage parameter: want artifactId, name, size and sha256")
		}
		if len(values) != 1 {
			return request, invalidMigrationParameter(key, "must be set exactly once")
		}
	}

	request.artifactID = query.Get("artifactId")
	if !migration.ValidArtifactID(request.artifactID) {
		return request, invalidArtifactID("artifactId", request.artifactID)
	}
	request.name = query.Get("name")
	if err := migration.ValidFileName(request.name); err != nil {
		return request, invalidArtifactFileName(request.name)
	}
	if request.name == migrationArtifactDocumentName {
		return request, output.NewErrorf(output.CodeInvalid,
			"name %q is the node's own manifest name: the manifest travels as a document, not as a file",
			request.name).
			WithDetails(map[string]any{"parameter": "name", "reason": "reserved_file_name"})
	}

	size, err := strconv.ParseInt(query.Get("size"), 10, 64)
	if err != nil || size <= 0 {
		return request, invalidMigrationParameter("size",
			"must be the positive byte count of the file being staged")
	}
	if size > migration.MaxFileSize {
		return request, invalidMigrationParameter("size",
			"must be at most "+strconv.FormatInt(migration.MaxFileSize, 10)+" bytes, the largest file a migration carries")
	}
	request.size = size

	request.sha256 = query.Get("sha256")
	if !validSHA256Hex(request.sha256) {
		return request, invalidMigrationParameter("sha256", "must be 64 lowercase hex characters")
	}
	return request, nil
}

// stageArtifact writes the body to a temporary file in the migration root, and
// publishes it under its name inside the artifact directory once it matches the
// request. The artifact directory is created only after the bytes are known to be
// the ones asked for, and the temporary is removed on every failure path, so a
// refused stage leaves no artifact behind. The root itself may be created: it is
// this node's configuration rather than an artifact, and the temporary has to live
// on the same filesystem as the directory it will be renamed into.
func stageArtifact(
	w http.ResponseWriter, r *http.Request, root string, request artifactStageRequest,
) (int, *output.Error) {
	if err := os.MkdirAll(root, artifactDirectoryMode); err != nil {
		return http.StatusServiceUnavailable, stagingUnavailable()
	}
	// The temporary lives beside the artifact directory rather than inside it: the
	// directory is what a refused stage must not create. Its name carries the
	// artifact ID so discard can sweep what a crash mid-stage left behind; the
	// random suffix keeps a concurrent stage of the same file from sharing a
	// partial file.
	tmp, err := os.CreateTemp(root, partialPrefix+request.artifactID+"-*")
	if err != nil {
		return http.StatusServiceUnavailable, stagingUnavailable()
	}
	tmpPath := tmp.Name()

	published := false
	defer func() {
		if published {
			return
		}
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	// The declared size bounds the read: a longer body is refused rather than
	// buffered or written in full.
	r.Body = http.MaxBytesReader(w, r.Body, request.size)
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, digest), r.Body)
	if copyErr != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(copyErr, &tooLarge) {
			return http.StatusRequestEntityTooLarge, payloadTooLarge(request.size)
		}
		return http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"the staged file could not be read").
			WithDetails(map[string]any{"reason": "body_unreadable"})
	}
	if written != request.size {
		return http.StatusBadRequest, output.NewErrorf(output.CodeInvalid,
			"the staged file is %d bytes: the request declares %d", written, request.size).
			WithDetails(map[string]any{"reason": "size_mismatch", "size": request.size})
	}

	sum := hex.EncodeToString(digest.Sum(nil))
	if sum != request.sha256 {
		return http.StatusConflict, output.NewErrorf(output.CodeConflict,
			"the staged file's SHA-256 is %s: the request declares %s", sum, request.sha256).
			WithDetails(map[string]any{"reason": "checksum_mismatch", "sha256": request.sha256})
	}

	// Durable before it is addressable: a rename that outlives the data it names
	// would hand the restore a truncated file.
	if err := tmp.Sync(); err != nil {
		return http.StatusServiceUnavailable, output.NewError(output.CodeUnavailable,
			"the staged file could not be written").
			WithDetails(map[string]any{"reason": "staging_unavailable"})
	}
	if err := tmp.Close(); err != nil {
		return http.StatusServiceUnavailable, output.NewError(output.CodeUnavailable,
			"the staged file could not be written").
			WithDetails(map[string]any{"reason": "staging_unavailable"})
	}

	dir := filepath.Join(root, request.artifactID)
	if status, problem := ensureArtifactDir(dir); problem != nil {
		return status, problem
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, request.name)); err != nil {
		return http.StatusServiceUnavailable, output.NewError(output.CodeUnavailable,
			"the staged file could not be published").
			WithDetails(map[string]any{"reason": "staging_unavailable"})
	}
	published = true
	return 0, nil
}

// migrationDiscardHandler serves DELETE /v1/tasks/{taskID}/migration/discard.
type migrationDiscardHandler struct {
	cfg MigrationHandlerConfig
}

// artifactDiscardRequest is the discard operation's query: which artifact.
type artifactDiscardRequest struct {
	artifactID string
}

// serve discards one artifact: its directory on this node, and the snapshot its
// files were published from. The operation is idempotent, because a rollback and a
// completed migration may both run it, and it is keyed by the artifact rather than
// by the task, because the task that exported it may already be gone.
func (h *migrationDiscardHandler) serve(ctx context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
	if !operationSpecs[MigrationDiscardOperation].allows(r.Method) {
		writeOperationMethodNotAllowed(w, MigrationDiscardOperation, r.Method)
		return
	}
	if h.cfg.ArtifactDir == "" {
		writeTypedError(w, http.StatusServiceUnavailable, directoryNotConfigured("artifact", "discard an artifact"))
		return
	}

	request, queryErr := parseDiscardQuery(r)
	if queryErr != nil {
		writeTypedError(w, http.StatusBadRequest, queryErr)
		return
	}
	if bodyErr := requireEmptyBody(w, r, MigrationDiscardOperation); bodyErr != nil {
		writeTypedError(w, http.StatusBadRequest, bodyErr)
		return
	}

	if status, problem := discardArtifact(ctx, h.cfg, request.artifactID); problem != nil {
		writeTypedError(w, status, problem)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseDiscardQuery enforces the discard operation's query grammar: artifactId,
// once, required.
func parseDiscardQuery(r *http.Request) (artifactDiscardRequest, *output.Error) {
	var request artifactDiscardRequest

	query := r.URL.Query()
	for key, values := range query {
		switch key {
		case "artifactId":
		default:
			return request, invalidMigrationParameter(key, "is not a migration discard parameter: want artifactId")
		}
		if len(values) != 1 {
			return request, invalidMigrationParameter(key, "must be set exactly once")
		}
	}

	request.artifactID = query.Get("artifactId")
	if !migration.ValidArtifactID(request.artifactID) {
		return request, invalidArtifactID("artifactId", request.artifactID)
	}
	return request, nil
}

// discardArtifact removes one artifact and the snapshot behind it. An artifact this
// node does not hold is already discarded, so the operation succeeds: a rollback
// may run twice, and may run on a node where the export never finished.
//
// The snapshot is deleted before the directory, so a failed delete leaves the
// artifact in place for a retry: removing the directory first would make the
// snapshot unreachable-by-name and leak it until retention ran. A snapshot the
// store no longer has is not a failure - it is the half of the delete that already
// happened - so the directory still goes.
func discardArtifact(ctx context.Context, cfg MigrationHandlerConfig, artifactID string) (int, *output.Error) {
	// The manifest names the snapshot the files came from. An artifact without a
	// readable one - never published, already gone, or not a real directory of its
	// own - is still removed: it cannot be restored, and its bytes belong to no
	// snapshot this node can name.
	if cfg.Store != nil {
		if dir, _, problem := existingArtifactDir(cfg.ArtifactDir, artifactID); problem == nil {
			if artifact, _, readProblem := readArtifactDocument(dir); readProblem == nil {
				if status, problem := deleteArtifactSnapshot(ctx, cfg.Store, artifact.SnapshotID); problem != nil {
					return status, problem
				}
			}
		}
	}

	// Removing a path that is not there is a success, which is what makes the
	// operation idempotent.
	if err := os.RemoveAll(filepath.Join(cfg.ArtifactDir, artifactID)); err != nil {
		return http.StatusServiceUnavailable, output.NewError(output.CodeUnavailable,
			"the artifact could not be removed from this node").
			WithDetails(map[string]any{"reason": "artifact_unremovable"})
	}
	// A stage that crashed before publishing leaves a partial file beside the
	// artifact directory; its name carries the artifact ID precisely so this
	// discard can remove it. Failing to remove one is not worth failing the
	// discard: the artifact itself is gone.
	partials, _ := filepath.Glob(filepath.Join(cfg.ArtifactDir, partialPrefix+artifactID+"-*"))
	for _, partial := range partials {
		_ = os.Remove(partial)
	}
	return 0, nil
}

// deleteArtifactSnapshot deletes the dump the artifact was published from. The
// artifact names it, so exactly one record goes: a task that was dumped twice has
// two snapshots, and only the one this artifact was made from belongs to it.
//
// A snapshot the store no longer holds is already deleted. That is what makes
// discard idempotent in the presence of the store's own retention, and it must not
// turn a caller's cleanup into a 404: the caller asked for the artifact to be gone,
// and it is.
func deleteArtifactSnapshot(ctx context.Context, store SnapshotStore, snapshotID string) (int, *output.Error) {
	if snapshotID == "" {
		return 0, nil
	}
	err := store.DeleteSnapshot(ctx, snapshotID)
	if err == nil || errors.Is(err, snapshot.ErrNotFound) {
		return 0, nil
	}
	return mapSnapshotStoreError("delete", err)
}

// discardSnapshot removes the dump a failed export left behind. The caller never
// received an artifact ID, so nothing else can ever discard it. Best effort: the
// request already answers an error, and a failed cleanup must not replace it.
func discardSnapshot(ctx context.Context, store SnapshotStore, snapshotID string) {
	if snapshotID == "" || snapshotStoreDisabled(store) {
		return
	}
	_ = store.DeleteSnapshot(ctx, snapshotID)
}

// migrationPrepareHandler serves POST /v1/tasks/{taskID}/migration/prepare.
type migrationPrepareHandler struct {
	cfg MigrationHandlerConfig
}

// migrationPrepareRequest is the document prepare takes: the service whose next
// task on this node restores, and the artifact it restores. Nothing else - the
// directory is this node's to derive, <migration root>/<artifactID>, because each
// node has its own root and the manager does not run here.
type migrationPrepareRequest struct {
	ServiceID  string `json:"serviceId"`
	ArtifactID string `json:"artifactId"`
}

// serve records the directive that tells this node to restore an artifact into the
// next task of a service that starts here. The task does not exist yet - SwarmKit
// creates it when the service is replaced - which is why the directive is keyed by
// service and why the task segment of the route is never resolved.
//
// The request is validated before anything is written, and the directive is then
// written by the node's store, whose Put replaces an earlier directive for the same
// service: the manager retries a prepare it never saw the answer to, and the retry
// describes the same migration.
func (h *migrationPrepareHandler) serve(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
	if !operationSpecs[MigrationPrepareOperation].allows(r.Method) {
		writeOperationMethodNotAllowed(w, MigrationPrepareOperation, r.Method)
		return
	}
	if h.cfg.ArtifactDir == "" {
		writeTypedError(w, http.StatusServiceUnavailable, directoryNotConfigured("migration", "record a pending restore"))
		return
	}
	if h.cfg.Pending == nil {
		writeTypedError(w, http.StatusServiceUnavailable, pendingRestoreUnavailable())
		return
	}

	request, bodyErr := decodePrepareRequest(w, r)
	if bodyErr != nil {
		writeTypedError(w, http.StatusBadRequest, bodyErr)
		return
	}
	dir, status, problem := prepareDirProblem(h.cfg.ArtifactDir, request)
	if problem != nil {
		writeTypedError(w, status, problem)
		return
	}

	record := scruntime.PendingRestore{
		ServiceID:  request.ServiceID,
		ArtifactID: request.ArtifactID,
		Dir:        dir,
		// The node stamps the instant it recorded the directive, not the manager:
		// the TTL is about how long this node has been holding it, and a manager
		// whose clock is wrong must not be able to keep a directive alive.
		CreatedAt: time.Now().UTC(),
	}
	if err := h.cfg.Pending.Put(record); err != nil {
		// pkg/runtime names the directory and the file it could not write; the text
		// stays here, where a human can read why, and only the classification crosses
		// the wire.
		log.Error().
			Err(err).
			Str("serviceId", record.ServiceID).
			Str("artifactId", record.ArtifactID).
			Msg("A pending restore could not be recorded on this node")
		writeTypedError(w, http.StatusServiceUnavailable, pendingRestoreNotRecorded())
		return
	}

	// One line, and it says the retry semantics out loud: a second prepare for the
	// same service is a replace, not a queue of restores.
	log.Info().
		Str("serviceId", record.ServiceID).
		Str("artifactId", record.ArtifactID).
		Str("dir", record.Dir).
		Msg("Prepared a pending restore for the service: its next task here restores this artifact (replaces any earlier directive for the service)")

	w.WriteHeader(http.StatusNoContent)
}

// decodePrepareRequest reads the prepare document, bounded and strict. A field this
// build does not know is refused rather than ignored: a caller sending a directive
// this node would not honour - a directory it cannot know, say - must be told, not
// quietly answered 204.
func decodePrepareRequest(w http.ResponseWriter, r *http.Request) (migrationPrepareRequest, *output.Error) {
	var request migrationPrepareRequest

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, migrationPrepareDocumentBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return request, invalidPrepareBody("the request body is larger than a pending-restore directive")
		}
		return request, invalidPrepareBody("the request body is not a pending-restore directive: want serviceId and artifactId")
	}
	// The body is one document. A second value after it is a caller mistake that a
	// decoder reading only the first would silently ignore - and the ignored part
	// could be the fields the caller meant.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return request, invalidPrepareBody("the request body carries more than one document")
	}
	return request, nil
}

// prepareDirProblem validates a prepare and returns the staging directory this node
// derives for it. The service must be an ID shape and the artifact must be one
// pkg/migration generates; then the artifact must actually be staged here, as a real
// directory under this node's migration root. The directory is never taken from the
// request: only this node knows its root, and a directive that named a path the
// manager guessed would restore something else - or nothing - into the new task.
//
// existingArtifactDir is the rule for "a staged artifact this node holds", shared with
// file and discard: a symbolic link and a plain file answer 404 rather than being
// followed, so a staged path can never become a way out of the root.
func prepareDirProblem(root string, request migrationPrepareRequest) (string, int, *output.Error) {
	if err := scruntime.CheckTaskID(request.ServiceID); err != nil {
		return "", http.StatusBadRequest, invalidServiceID(request.ServiceID)
	}
	if !migration.ValidArtifactID(request.ArtifactID) {
		return "", http.StatusBadRequest, invalidArtifactID("artifactId", request.ArtifactID)
	}
	return existingArtifactDir(root, request.ArtifactID)
}

// migrationUnprepareHandler serves DELETE /v1/tasks/{taskID}/migration/unprepare.
type migrationUnprepareHandler struct {
	cfg MigrationHandlerConfig
}

// serve removes the directive prepared for a service, but only when it names the
// artifact the caller says it owns. It is the operation an abandoned migration needs:
// the manager has discarded the artifact, so a directive that still named it would
// make the service's next task here restore nothing.
//
// The artifact is part of the request because two managers can migrate one service
// (a leadership change mid-call, say). Each prepares its own directive, which
// replaces the other's; the one that then abandons must not delete the directive the
// winner is about to consume. So a mismatch is a 409 directive_replaced and the
// directive stays, rather than a 204 that silently removed someone else's restore.
//
// It is idempotent - a rollback may run twice, and may run on a node where the
// prepare never arrived - so a service with no directive is the same 204. The store
// removes a record without decoding it, so a corrupt file cannot wedge the cleanup.
func (h *migrationUnprepareHandler) serve(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
	if !operationSpecs[MigrationUnprepareOperation].allows(r.Method) {
		writeOperationMethodNotAllowed(w, MigrationUnprepareOperation, r.Method)
		return
	}
	if h.cfg.Pending == nil {
		writeTypedError(w, http.StatusServiceUnavailable, pendingRestoreUnavailable())
		return
	}

	request, queryErr := parseUnprepareQuery(r)
	if queryErr != nil {
		writeTypedError(w, http.StatusBadRequest, queryErr)
		return
	}
	if err := scruntime.CheckTaskID(request.serviceID); err != nil {
		writeTypedError(w, http.StatusBadRequest, invalidServiceID(request.serviceID))
		return
	}
	if !migration.ValidArtifactID(request.artifactID) {
		writeTypedError(w, http.StatusBadRequest, invalidArtifactID("artifactId", request.artifactID))
		return
	}
	if bodyErr := requireEmptyBody(w, r, MigrationUnprepareOperation); bodyErr != nil {
		writeTypedError(w, http.StatusBadRequest, bodyErr)
		return
	}

	if err := h.cfg.Pending.DeleteArtifact(request.serviceID, request.artifactID); err != nil {
		if errors.Is(err, scruntime.ErrDirectiveReplaced) {
			// The text stays here: it names the artifact the service now waits for,
			// which is another migration's business.
			log.Info().
				Err(err).
				Str("serviceId", request.serviceID).
				Str("artifactId", request.artifactID).
				Msg("A pending restore was left in place: a newer directive for this service names another artifact")
			writeTypedError(w, http.StatusConflict, directiveReplaced(request.serviceID))
			return
		}
		// pkg/runtime names the file it could not remove, and that is this node's
		// own path.
		log.Error().
			Err(err).
			Str("serviceId", request.serviceID).
			Str("artifactId", request.artifactID).
			Msg("A pending restore could not be removed on this node")
		writeTypedError(w, http.StatusServiceUnavailable, pendingRestoreNotCleared())
		return
	}

	log.Info().
		Str("serviceId", request.serviceID).
		Str("artifactId", request.artifactID).
		Msg("Removed the pending restore for the service: its next task here starts cold")

	w.WriteHeader(http.StatusNoContent)
}

// migrationReannealRequest is the document reanneal takes: which attachment of the
// guest is meant (its network ID), the guest's address on that attachment and the
// guest's MAC.
//
// The network ID is the one attachment fact a request has to carry, because the
// bridge is a node-local decision: a guest on a user-defined network lived on that
// network's bridge, not on the node's flat bridge, and only the receiving node can
// say which device that is (see MigrationHandlerConfig.NetworkByID). It is
// validated as an opaque ID and never read as a device name. Nothing here names a
// bridge, a TAP device or a peer: those are this node's own configuration, and a
// request that named them could make a node act on a link that is not its L2
// segment.
type migrationReannealRequest struct {
	NetworkID string `json:"networkId"`
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
}

// runNodeNetworkCommand runs one iproute2 command on this node and returns its
// combined output. It is a variable so a test can record the exact commands a
// re-anneal issues and script a node's answers, which is the only way to pin
// them without CAP_NET_ADMIN.
var runNodeNetworkCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// migrationReannealHandler serves POST /v1/tasks/{taskID}/migration/reanneal.
type migrationReannealHandler struct {
	cfg MigrationHandlerConfig
}

// serve makes this node forget what it knew about one guest that has moved.
//
// The route is artifact-scoped and the task in the path is never read: after a
// move the guest runs under another task, and every peer is told about the guest
// (its attachment, its address and MAC), not about the task that used to own it.
//
// The steps are ordered by what a failure costs. Deleting the neighbour entry
// means this node's next frame to the guest asks L2 again; deleting the FDB entry
// means the bridge stops sending the guest's frames to the old node's VTEP;
// re-adding the flood entry means the ARP that follows reaches every node,
// including the one the guest is now on. The neighbour and port-entry deletes are
// best effort for an entry that was never there - a peer that never learned the
// guest has nothing to forget - and the flood append is idempotent: an entry that
// is already there is not an error.
func (h *migrationReannealHandler) serve(ctx context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
	if !operationSpecs[MigrationReannealOperation].allows(r.Method) {
		writeOperationMethodNotAllowed(w, MigrationReannealOperation, r.Method)
		return
	}
	if h.cfg.BridgeName == "" {
		writeTypedError(w, http.StatusServiceUnavailable, reannealNotConfigured())
		return
	}

	request, bodyErr := decodeReannealRequest(w, r)
	if bodyErr != nil {
		writeTypedError(w, http.StatusBadRequest, bodyErr)
		return
	}

	// The device this node acts on is resolved once, from the attachment's network
	// and this node's own configuration: no part of it comes from the request.
	bridge := reannealBridge(h.cfg, request.NetworkID)

	if problem := reannealGuest(ctx, h.cfg, request, bridge); problem != nil {
		// A command that failed on this node is this node's internal problem, not a
		// "try again later": the same iproute2 call would fail the same way, so
		// nothing retries it and the manager logs the peer and moves on.
		writeTypedError(w, http.StatusInternalServerError, problem)
		return
	}

	log.Info().
		Str("networkId", request.NetworkID).
		Str("ip", request.IP).
		Str("mac", request.MAC).
		Str("bridge", bridge).
		Msg("Re-annealed a moved guest: this node forgot its neighbour and FDB entries and keeps flooding its overlay peers")

	w.WriteHeader(http.StatusNoContent)
}

// decodeReannealRequest reads the re-anneal document, bounded and strict. Every
// field is required: an address without a MAC would leave the stale FDB entry, a
// MAC without an address would leave the stale neighbour entry, and neither
// without a network ID would forget the wrong bridge.
func decodeReannealRequest(w http.ResponseWriter, r *http.Request) (migrationReannealRequest, *output.Error) {
	var request migrationReannealRequest

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, migrationReannealDocumentBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, invalidReannealBody("the request body is not a re-anneal document: want networkId, ip and mac")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return request, invalidReannealBody("the request body carries more than one document")
	}

	request.NetworkID = strings.TrimSpace(request.NetworkID)
	if !reannealValidNetworkID(request.NetworkID) {
		return request, output.NewErrorf(output.CodeInvalid,
			"networkId %q is not a network ID: want the attachment's network, as an opaque SwarmKit ID", request.NetworkID).
			WithDetails(map[string]any{"parameter": "networkId", "reason": "invalid_network_id"})
	}
	if net.ParseIP(strings.TrimSpace(request.IP)) == nil {
		return request, output.NewErrorf(output.CodeInvalid,
			"ip %q is not an IP address: want the guest address this node cached", request.IP).
			WithDetails(map[string]any{"parameter": "ip", "reason": "invalid_ip"})
	}
	hardware, err := net.ParseMAC(strings.TrimSpace(request.MAC))
	if err != nil || len(hardware) != 6 {
		return request, output.NewErrorf(output.CodeInvalid,
			"mac %q is not a 48-bit MAC address: want the guest's address, as aa:bb:cc:dd:ee:ff", request.MAC).
			WithDetails(map[string]any{"parameter": "mac", "reason": "invalid_mac"})
	}

	request.IP = strings.TrimSpace(request.IP)
	request.MAC = hardware.String()
	return request, nil
}

// reannealValidNetworkID reports whether a network ID is an opaque SwarmKit ID: a
// non-empty value that is not a path, carries no whitespace or control character
// and is bounded, so it is safe to log, compare and answer a request with - and so
// it cannot be mistaken for a device name or a path by anything downstream.
func reannealValidNetworkID(value string) bool {
	if value == "" || len(value) > maxSnapshotScopeBytes {
		return false
	}
	if value == "." || value == ".." {
		return false
	}
	return !strings.ContainsAny(value, "/\\\x00\n\r \t")
}

// reannealBridge is the bridge this node carries one attachment on: the bridge the
// network is materialised on here, derived by the same call the restore path makes
// for the TAP a restored task joins (pkg/network's AttachmentBridgeName).
//
// The lookup answers nil for a network this node has not materialised, and for a
// network carved from the flat segment, and AttachmentBridgeName then answers this
// node's own bridge: that is exactly right for both, because neither was ever on
// another device.
func reannealBridge(cfg MigrationHandlerConfig, networkID string) string {
	var attached *types.Network
	if networkID != "" && cfg.NetworkByID != nil {
		attached, _ = cfg.NetworkByID(networkID)
	}
	return network.AttachmentBridgeName(attached, types.NetworkConfig{
		BridgeName: cfg.BridgeName,
		Subnet:     cfg.Subnet,
	})
}

// reannealGuest runs this node's steps. The two deletes that matter tolerate an
// absent entry, because forgetting something twice is still forgetting it; any
// other failure is reported, because a node that cannot say the stale entry is
// gone has not re-annealed and the manager must be able to log which peer did not.
func reannealGuest(
	ctx context.Context, cfg MigrationHandlerConfig, request migrationReannealRequest, bridge string,
) *output.Error {
	// The guest's frames stopped following the MAC when the port entry pointed at
	// the old node's VTEP, so the entry to delete is the one *of the overlay port*:
	// a bridge FDB entry belongs to the port that learned it, and naming the bridge
	// device addresses the bridge's own (self) entry, which is not the one that
	// carries the guest.
	vxlan := scruntime.VXLANInterfaceName(bridge)

	if !reannealForget(ctx, "ip", "neigh", "del", request.IP, "dev", bridge) {
		return reannealFailed()
	}
	if !reannealForgetOverlay(ctx, request.MAC, vxlan) {
		return reannealFailed()
	}
	// The bridge's own entry is kept as a best-effort extra: a guest whose MAC the
	// bridge learned locally (a TAP on this node, a frame the bridge flooded to its
	// own ports) leaves one there too. It is not the entry that black-holes the
	// guest, so a failure here is logged and the re-anneal carries on.
	reannealForgetExtra(ctx, "bridge", "fdb", "del", request.MAC, "dev", bridge)

	if len(cfg.VXLANPeers) == 0 {
		return nil
	}
	// The flood entry maps the all-zero MAC to every peer's VTEP: it is what makes
	// an ARP for a guest nobody has learned yet reach the node that now holds it.
	// 'append' allows one destination per peer under the same key, and a repeated
	// append of an entry that exists is answered as a duplicate, which is success.
	for _, peer := range cfg.VXLANPeers {
		output, err := runNodeNetworkCommand(ctx, "bridge", "fdb", "append",
			"00:00:00:00:00:00", "dev", vxlan, "dst", peer, "self", "permanent")
		if err == nil || reannealEntryExists(string(output)) {
			continue
		}
		log.Warn().
			Err(err).
			Str("vxlan", vxlan).
			Str("peer", peer).
			Str("output", strings.TrimSpace(string(output))).
			Msg("Re-anneal could not re-add the overlay flood entry for a peer")
		return reannealFailed()
	}
	return nil
}

// reannealForget runs one delete and reports whether the entry is gone: a
// successful delete, or iproute2's answer for an entry that was not there. Any
// other failure is this node's and stops the re-anneal.
func reannealForget(ctx context.Context, name string, args ...string) bool {
	output, err := runNodeNetworkCommand(ctx, name, args...)
	if err == nil || reannealEntryAbsent(string(output)) {
		return true
	}
	log.Warn().
		Err(err).
		Str("command", name+" "+strings.Join(args, " ")).
		Str("output", strings.TrimSpace(string(output))).
		Msg("Re-anneal could not remove a stale entry")
	return false
}

// reannealForgetOverlay deletes the guest's MAC on the overlay port. It answers
// the two absent-entry results reannealForget does, and one more: a node that runs
// no overlay has no overlay interface at all, and a node with no overlay port
// holds no entry of that port to forget.
func reannealForgetOverlay(ctx context.Context, mac, vxlan string) bool {
	output, err := runNodeNetworkCommand(ctx, "bridge", "fdb", "del", mac, "dev", vxlan)
	if err == nil || reannealEntryAbsent(string(output)) {
		return true
	}
	if reannealOverlayAbsent(string(output), vxlan) {
		log.Debug().
			Str("vxlan", vxlan).
			Msg("Re-anneal found no overlay interface on this node, so there is no port entry to forget")
		return true
	}
	log.Warn().
		Err(err).
		Str("command", "bridge fdb del "+mac+" dev "+vxlan).
		Str("output", strings.TrimSpace(string(output))).
		Msg("Re-anneal could not remove the guest's overlay FDB entry")
	return false
}

// reannealForgetExtra runs the delete whose failure the caller never reports.
func reannealForgetExtra(ctx context.Context, name string, args ...string) {
	output, err := runNodeNetworkCommand(ctx, name, args...)
	if err == nil || reannealEntryAbsent(string(output)) {
		return
	}
	log.Debug().
		Err(err).
		Str("command", name+" "+strings.Join(args, " ")).
		Str("output", strings.TrimSpace(string(output))).
		Msg("Re-anneal left the bridge's own entry alone; it is not the entry that carried the guest")
}

// reannealEntryAbsent reports whether a failed delete failed because there was
// nothing to delete. iproute2's two answers for that are "RTNETLINK answers: No
// such file or directory" (a neighbour or FDB entry that does not exist) and
// "RTNETLINK answers: No such process" (a neighbour already gone). Those two, and
// only those two: "not found" and "does not exist" also appear in answers about
// devices and sysctls, and a node that read one of those as success would report
// an entry gone that it never touched.
func reannealEntryAbsent(output string) bool {
	lowered := strings.ToLower(output)
	return strings.Contains(lowered, "no such file or directory") ||
		strings.Contains(lowered, "no such process")
}

// reannealOverlayAbsent reports whether the overlay interface a delete names is not
// on this node at all. This is not an absent entry and is never read as one: it is
// the answer a cluster without an overlay gives for the device, and it only ever
// excuses the overlay delete, which is the only command that names that device.
func reannealOverlayAbsent(output, vxlan string) bool {
	lowered := strings.ToLower(output)
	return strings.Contains(lowered, "cannot find device") &&
		strings.Contains(lowered, strings.ToLower(vxlan))
}

// reannealEntryExists reports whether adding a flood entry found it already
// there, which is the one answer that makes the append idempotent.
func reannealEntryExists(output string) bool {
	return strings.Contains(strings.ToLower(output), "exists")
}

// reannealNotConfigured is the typed 503 for a node that was never given the
// bridge its guests live on: it has nothing to forget them from.
func reannealNotConfigured() *output.Error {
	return output.NewError(output.CodeUnavailable,
		"this node has no bridge configured, so it cannot re-anneal a moved guest").
		WithDetails(map[string]any{"reason": "bridge_unconfigured"})
}

// reannealFailed is the typed 500 for a node whose iproute2 command failed for a
// reason other than the entry already being gone: this node's own networking is
// what is wrong, so it is internal, not "try again later". The command's own
// output stays in the log: it names this node's devices.
func reannealFailed() *output.Error {
	return output.NewError(output.CodeInternal,
		"this node could not forget the moved guest").
		WithDetails(map[string]any{"reason": "reanneal_failed"})
}

// invalidReannealBody is the typed 400 for a body that is not the re-anneal
// document.
func invalidReannealBody(problem string) *output.Error {
	return output.NewError(output.CodeInvalid, problem).
		WithDetails(map[string]any{"reason": "body_unreadable"})
}

// unprepareRequest is the unprepare query: the service whose directive is removed and
// the artifact the caller owns.
type unprepareRequest struct {
	serviceID  string
	artifactID string
}

// parseUnprepareQuery enforces the unprepare query grammar: serviceId and artifactId,
// once each, and nothing else. The pair is in the query rather than the body because
// the operation removes a record and sends no document.
func parseUnprepareQuery(r *http.Request) (unprepareRequest, *output.Error) {
	var request unprepareRequest

	query := r.URL.Query()
	for key, values := range query {
		switch key {
		case "serviceId", "artifactId":
		default:
			return request, invalidMigrationParameter(key, "is not a migration unprepare parameter: want serviceId and artifactId")
		}
		if len(values) != 1 {
			return request, invalidMigrationParameter(key, "must be set exactly once")
		}
	}
	request.serviceID, request.artifactID = query.Get("serviceId"), query.Get("artifactId")
	return request, nil
}

// directiveReplaced is the typed 409 for a directive another migration owns. It is a
// conflict rather than a success because the caller asked to remove a restore it does
// not own: the node still holds one, and pretending otherwise would hide the winner.
func directiveReplaced(serviceID string) *output.Error {
	return output.NewError(output.CodeConflict,
		"this node waits for a newer migration's artifact for this service: the directive names another artifact").
		WithDetails(map[string]any{"reason": "directive_replaced", "serviceId": serviceID})
}

// invalidServiceID is the typed 400 for a service ID that is not one safe path
// element. SwarmKit gives services and tasks the same shape, so the rule is the
// task ID rule the node already applies to every ID that becomes a file name.
func invalidServiceID(value string) *output.Error {
	return output.NewErrorf(output.CodeInvalid,
		"serviceId %q is not a SwarmKit service ID: want one safe path segment, at most %d characters, with no separator, dot or whitespace",
		value, scruntime.MaxIDLength).
		WithDetails(map[string]any{"parameter": "serviceId", "reason": "invalid_service_id"})
}

// pendingRestoreNotCleared is the typed 503 for a directive the store could not
// remove. The caller asked for the migration to be abandoned, and a node that cannot
// say the directive is gone must not answer as though it were: the next task of the
// service would then restore an artifact nobody kept.
func pendingRestoreNotCleared() *output.Error {
	return output.NewError(output.CodeUnavailable,
		"the pending restore could not be removed on this node").
		WithDetails(map[string]any{"reason": "pending_restore_not_cleared"})
}

// invalidPrepareBody is the typed 400 for a body that is not the prepare document.
func invalidPrepareBody(problem string) *output.Error {
	return output.NewError(output.CodeInvalid, problem).
		WithDetails(map[string]any{"reason": "body_unreadable"})
}

// pendingRestoreUnavailable is the typed 503 for a node whose directive store was
// never opened. Recording a prepare it cannot keep would tell the manager a restore
// is arranged when nothing is.
func pendingRestoreUnavailable() *output.Error {
	return output.NewError(output.CodeUnavailable,
		"this node cannot keep a pending-restore directive").
		WithDetails(map[string]any{"reason": "pending_restore_unavailable"})
}

// pendingRestoreNotRecorded is the typed 503 for a directive the store refused to
// write: the node's state directory would not take it, so the restore is not
// arranged and the manager must not proceed.
func pendingRestoreNotRecorded() *output.Error {
	return output.NewError(output.CodeUnavailable,
		"the pending restore could not be recorded on this node").
		WithDetails(map[string]any{"reason": "pending_restore_unrecorded"})
}

// validSHA256Hex reports whether value is a hex-encoded SHA-256 digest in the
// shape pkg/migration writes: 64 lowercase hex characters, no "sha256:" prefix.
func validSHA256Hex(value string) bool {
	if len(value) != sha256HexChars {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// invalidArtifactID is the typed 400 for an ID that is not one safe path segment.
func invalidArtifactID(parameter, value string) *output.Error {
	return output.NewErrorf(output.CodeInvalid,
		"%s %q is not a migration artifact ID: want %q followed by 16 lowercase hex characters",
		parameter, value, "mig-").
		WithDetails(map[string]any{"parameter": parameter, "reason": "invalid_artifact_id"})
}

// invalidArtifactFileName is the typed 400 for a name that is not one bare file
// name. The rule itself is pkg/migration.ValidFileName: the manifest that describes
// a file and the route that stages or serves one must agree, so the message here
// states the rule rather than repeating the check.
func invalidArtifactFileName(name string) *output.Error {
	return output.NewErrorf(output.CodeInvalid,
		"name %q is not one bare file name: want a name with no separator, no \".\" or \"..\", no control character, and at most 255 bytes", name).
		WithDetails(map[string]any{"parameter": "name", "reason": "invalid_file_name"})
}

// unknownArtifactFile is the typed 400 for a name the manifest does not describe.
// Only the names it does describe are listed: nothing else in the directory is a
// file that may travel.
func unknownArtifactFile(artifact migration.Artifact, name string) *output.Error {
	files := artifactFiles(artifact)
	names := make([]string, 0, len(files))
	quoted := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.Name)
		quoted = append(quoted, strconv.Quote(file.Name))
	}
	return output.NewErrorf(output.CodeInvalid,
		"name %q is not a file of artifact %s: want one of %s", name, artifact.ID, strings.Join(quoted, ", ")).
		WithDetails(map[string]any{
			"parameter": "name",
			"reason":    "unknown_artifact_file",
			"files":     names,
		})
}

// invalidMigrationParameter is the typed 400 for a bad query value.
func invalidMigrationParameter(parameter, message string) *output.Error {
	return output.NewErrorf(output.CodeInvalid, "migration parameter %q %s", parameter, message).
		WithDetails(map[string]any{"parameter": parameter})
}

// directoryNotConfigured is the typed 503 for a node that was never given the
// directory an operation needs.
func directoryNotConfigured(directory, action string) *output.Error {
	return output.NewErrorf(output.CodeUnavailable,
		"this node has no %s directory configured, so it cannot %s", directory, action).
		WithDetails(map[string]any{"reason": directory + "_dir_unconfigured"})
}

// artifactNotAvailable is the typed 404 for an artifact this node does not hold:
// never exported here, already cleaned up, or published by an earlier daemon
// lifetime whose snapshot is gone.
func artifactNotAvailable() *output.Error {
	return output.NewError(output.CodeNotFound,
		"the artifact is not available on this node").
		WithDetails(map[string]any{"reason": "artifact_not_available"})
}

// artifactFileNotRegular is the typed 409 for an entry that is not the plain file
// the manifest describes: a directory, a device, or a symbolic link, which is
// opened without being followed.
func artifactFileNotRegular(name string) *output.Error {
	return output.NewErrorf(output.CodeConflict,
		"artifact file %q is not a regular file: the manifest describes one", name).
		WithDetails(map[string]any{"reason": "artifact_changed", "name": name})
}

// artifactUnreadable is the typed 503 for an artifact directory this daemon cannot
// read: the files are the node's, so a failure is the node's, not the caller's.
func artifactUnreadable() *output.Error {
	return output.NewError(output.CodeUnavailable,
		"the artifact could not be read on this node").
		WithDetails(map[string]any{"reason": "artifact_unreadable"})
}

// artifactInvalid is the typed 500 for a manifest that does not validate.
func artifactInvalid() *output.Error {
	return output.NewError(output.CodeInternal,
		"the artifact's manifest is not valid on this node").
		WithDetails(map[string]any{"reason": "artifact_invalid"})
}

// artifactDirectoryUnsafe is the typed 409 for a path under the staging root that
// is not a real directory: a link there would move a staged name outside the root.
func artifactDirectoryUnsafe() *output.Error {
	return output.NewError(output.CodeConflict,
		"the artifact directory on this node is not a directory of its own").
		WithDetails(map[string]any{"reason": "artifact_dir_unsafe"})
}

// stagingUnavailable is the typed 503 for a staging root this node cannot write.
func stagingUnavailable() *output.Error {
	return output.NewError(output.CodeUnavailable,
		"the staging directory is not writable on this node").
		WithDetails(map[string]any{"reason": "staging_unavailable"})
}

// writeMigrationDisabled is the 501 for a node with no snapshot store.
func writeMigrationDisabled(w http.ResponseWriter) {
	writeTypedError(w, http.StatusNotImplemented, output.NewError(output.CodeNotImplemented,
		"migrations are not enabled on this node").
		WithDetails(map[string]any{"capability": "migrations", "reason": "migrations_disabled"}))
}

// writeOperationMethodNotAllowed is the 405 for a method the named operation does
// not implement. It reads the method set from operationSpecs so the answer cannot
// disagree with the server's.
func writeOperationMethodNotAllowed(w http.ResponseWriter, operation, method string) {
	w.Header().Set("Allow", operationSpecs[operation].allow())
	writeTypedError(w, http.StatusMethodNotAllowed, output.NewErrorf(output.CodeInvalid,
		"method %s is not allowed on operation %q", method, operation).
		WithDetails(map[string]any{"reason": "method_not_allowed"}))
}

// writeDocument writes one already-encoded internal document with the broker media
// type. Encoding before the body is written is what lets a handler publish an
// artifact and its answer as one unit: a document that cannot be encoded is a
// failure it can still undo.
func writeDocument(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", publicMediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	//nolint:gosec // G705: the body is a JSON document served as the v1 media type.
	_, _ = w.Write(body)
}

// requireEmptyBody refuses a body on an operation that takes none. A handler that
// silently ignored one would accept a caller's mistake and act on a different
// request than the caller sent.
func requireEmptyBody(w http.ResponseWriter, r *http.Request, operation string) *output.Error {
	// A chunked body declares no length; a zero-byte cap is enough to tell an
	// empty body from a present one without buffering it.
	r.Body = http.MaxBytesReader(w, r.Body, 0)
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return output.NewErrorf(output.CodeInvalid,
				"%s takes no body: what it acts on is named in the path and the query", operation).
				WithDetails(map[string]any{"reason": "unexpected_body"})
		}
		return output.NewError(output.CodeInvalid, "the request body could not be read").
			WithDetails(map[string]any{"reason": "body_unreadable"})
	}
	return nil
}
