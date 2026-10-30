package broker

// This file owns the node-side artifact transport of a live migration: producing
// an artifact from a running task, serving one of its files, staging a file a
// restore will use, and discarding an artifact when it is no longer needed.
//
//	POST   /v1/tasks/{taskID}/migration/export
//	GET    /v1/tasks/{taskID}/migration/file?artifactId=<id>&name=<file>
//	PUT    /v1/tasks/{taskID}/migration/stage?artifactId=<id>&name=<file>&size=<n>&sha256=<hex>
//	DELETE /v1/tasks/{taskID}/migration/discard?artifactId=<id>
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
// have is a 404. file, stage and discard never read the task, because the artifact
// outlives it: SwarmKit replaces the task as part of the move, and a stage on the
// target happens for a task the target has never run.
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
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
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

	// maxArtifactDocumentBytes bounds the manifest this node reads back. The
	// document is a page at most; the cap stops a file planted in the artifact
	// directory from making the handler buffer an arbitrary size.
	maxArtifactDocumentBytes = 1 << 20

	// sha256HexChars is the length of a hex-encoded SHA-256 digest.
	sha256HexChars = 64
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
	// the VM's block-device path but not the disk contents, so export records
	// <RootfsDir>/tasks/<taskID>.ext4 as the task disk to copy, the same rule the
	// snapshot create operation applies. Empty refuses an export: an artifact
	// without a disk would restore a guest with no filesystem.
	RootfsDir string
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

	info, err := h.cfg.Store.CreateSnapshot(ctx, task.ID, task.SocketPath, snapshot.CreateOptions{
		ServiceID:  scope.service,
		NodeID:     scope.node,
		RootfsPath: filepath.Join(h.cfg.RootfsDir, "tasks", task.ID+".ext4"),
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

	artifact, err := migration.ArtifactFromSnapshot(info, filepath.Dir(task.SocketPath), 0)
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
	// directory is what a refused stage must not create. A random name keeps a
	// concurrent stage of the same file from writing the same partial file.
	tmp, err := os.CreateTemp(root, ".partial-*")
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
				"%s takes no body: the artifact is named in the path and the query", operation).
				WithDetails(map[string]any{"reason": "unexpected_body"})
		}
		return output.NewError(output.CodeInvalid, "the request body could not be read").
			WithDetails(map[string]any{"reason": "body_unreadable"})
	}
	return nil
}
