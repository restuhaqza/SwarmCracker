package broker

// This file owns the node-side artifact transport of a live migration: producing
// an artifact from a running task, serving one of its files, and staging a file
// a restore will use.
//
//	POST /v1/tasks/{taskID}/migration/export
//	GET  /v1/tasks/{taskID}/migration/file?artifactId=<id>&name=<file>
//	PUT  /v1/tasks/{taskID}/migration/stage?artifactId=<id>&name=<file>&size=<n>&sha256=<hex>
//
// The restore itself is not here: loading the artifact into a new task creates the
// TAP device, loads the snapshot and re-addresses the guest, and that has its own
// operation.
//
// # One artifact, one directory
//
// An artifact is addressed by its own ID, so the files that move live in
// <root>/<artifactID>/ on both sides of the move: the source publishes the
// directory, the target stages into it, and a name inside it is one bare file
// name the manifest describes. The directory is not part of the artifact: it is
// where a node keeps the bytes, and no other node may assume it.
//
// The source does not copy the bytes. pkg/snapshot's create already paused the
// task, dumped the state and the memory image, copied the disk and published the
// result as one directory, so export writes the manifest (artifact.json) into
// that directory and publishes <snapshotDir>/<artifactID> as a symbolic link to
// it. The consequences are deliberate:
//
//   - One copy of a multi-GB memory image, not two. A link costs a directory
//     entry, not the bytes it points at.
//   - The snapshot store keeps a complete, restorable record whose files are the
//     ones on the wire, so snapshot.Manager.DeleteSnapshot (or its retention
//     sweep) frees the artifact's bytes with the snapshot. Export removes links
//     whose target is already gone, so spent artifacts do not accumulate.
//   - A link that outlives its snapshot answers the same typed 404 a missing
//     artifact does, and a transfer that starts after cleanup fails as one
//     transfer rather than as a corrupt restore, because the target verifies
//     every checksum it is given.
//
// Export requires the node's snapshot store, so it is available where snapshots
// are (--snapshots-enable). Without a store the operation stays registered and
// answers a typed 501, exactly like the snapshots operation.
//
// # Trust
//
// Both file operations take an artifact ID and a file name from the request. The
// ID must be one safe path segment in the shape pkg/migration generates, and the
// name must be one bare file name; both are checked before any join, so neither
// can address anything outside the directory it names.
//
// On the source the manifest is the whitelist: the artifact directory also holds
// node-local metadata (the manifest itself, the snapshot's own metadata.json) and
// only the files the manifest describes may leave the node. The route is
// task-scoped, and it serves the artifact that task exported; an artifact
// belonging to another task is a conflict, not a file.
//
// On the target the name is not checked against a manifest, because the manifest
// travels as a document rather than as a file. Instead the reserved manifest name
// is refused outright, and a staged file exists only after its size and its
// SHA-256 matched the request: a restore either finds a complete artifact or
// none. Restaging the same bytes succeeds (the checksum matches again) and a
// differing body replaces nothing (the check happens before the rename).

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

	// maxArtifactFileBytes is the largest file one transfer accepts: 64 GiB. It
	// mirrors pkg/migration's ceiling, which is where the number is defined (a
	// manifest's entry cannot exceed it, so a declared size above it is either a
	// lie or a file no manifest could describe). The copy here is the second line
	// of defence: it bounds the read before a manifest is available.
	maxArtifactFileBytes int64 = 64 << 30

	// maxArtifactNameBytes is the kernel's NAME_MAX: a file name is one directory
	// entry, and a longer one cannot be created on the node that must stage it.
	maxArtifactNameBytes = 255

	// maxArtifactDocumentBytes bounds the manifest this node reads back. The
	// document is a page at most; the cap stops a file planted in the artifact
	// directory from making the handler buffer an arbitrary size.
	maxArtifactDocumentBytes = 1 << 20

	// sha256HexChars is the length of a hex-encoded SHA-256 digest.
	sha256HexChars = 64
)

// errManifestTooLarge marks a manifest longer than maxArtifactDocumentBytes. It
// is a distinct failure from an unreadable file: the file exists, and it is not a
// manifest this build wrote.
var errManifestTooLarge = errors.New("artifact manifest is too large")

// MigrationHandlerConfig configures the migration transport operations. The three
// operations share one configuration because they describe one artifact layout.
type MigrationHandlerConfig struct {
	// Store is the node-local snapshot store export snapshots through. Nil (or a
	// typed nil) keeps the operations registered and answers export a typed 501: a
	// node without a snapshot store cannot produce an artifact. file and stage do
	// not need it: they move bytes an export already produced.
	Store SnapshotStore
	// SnapshotDir is the root that holds artifacts on this node. Export publishes
	// <SnapshotDir>/<artifactID> there and file resolves an artifact from it. It is
	// the snapshot store's own directory, because the artifact's bytes are the
	// snapshot's files.
	SnapshotDir string
	// StagingDir is the root that holds artifacts received on this node: stage
	// writes <StagingDir>/<artifactID>/<name>, which is where the restore looks.
	StagingDir string
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
	// Dir is the directory that holds the files, <SnapshotDir>/<artifactID>. It is
	// not part of the artifact: a target stages into its own root, and the two
	// nodes need not agree on a path.
	Dir string `json:"dir"`
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

// migrationExportHandler serves POST /v1/tasks/{taskID}/migration/export.
type migrationExportHandler struct {
	cfg MigrationHandlerConfig
}

// serve exports the resolved task: snapshot it through the node's store, describe
// the result, and publish the directory the manifest names. The task keeps
// running: the store resumes the VM before it returns, so the transfer happens
// while the source still serves.
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
	if bodyErr := requireEmptyBody(w, r); bodyErr != nil {
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
		writeError(w, http.StatusInternalServerError, output.CodeInternal,
			"task %q could not be described as a migration artifact", task.ID)
		return
	}

	// The snapshot directory pkg/snapshot published holds the bytes; the artifact
	// directory named by the artifact's own ID points at it.
	snapshotDir := filepath.Dir(info.StatePath)
	if err := publishArtifact(h.cfg.SnapshotDir, snapshotDir, artifact); err != nil {
		writeError(w, http.StatusServiceUnavailable, output.CodeUnavailable,
			"task %q artifact could not be published on this node", task.ID)
		return
	}

	writeMigrationDocument(w, http.StatusCreated, MigrationExportDocument{
		SchemaVersion: v1.SchemaVersion,
		Kind:          migrationExportKind,
		Artifact:      artifact,
		Dir:           filepath.Join(h.cfg.SnapshotDir, artifact.ID),
	})
}

// configProblem reports why this node cannot export at all, or nil. The template
// values are checked once, before the task is touched: a node without a rootfs
// directory would pause a task only to fail after the disk copy.
func (h *migrationExportHandler) configProblem() (int, *output.Error) {
	if h.cfg.SnapshotDir == "" {
		return http.StatusServiceUnavailable, directoryNotConfigured("snapshot", "publish an artifact")
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
			return scope, invalidMigrationParameter(key, "is not a migration export parameter")
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

// publishArtifact makes the artifact addressable by its own ID: the manifest goes
// into the snapshot directory pkg/snapshot published, and <root>/<artifactID>
// becomes a link to that directory. The manifest is written first, so a reader
// never sees a link without the description of what it holds.
func publishArtifact(root, snapshotDir string, artifact migration.Artifact) error {
	pruneSpentArtifacts(root)

	if err := writeArtifactDocument(snapshotDir, artifact); err != nil {
		return err
	}
	link := filepath.Join(root, artifact.ID)
	if err := os.Symlink(snapshotDir, link); err != nil {
		return err
	}
	return nil
}

// writeArtifactDocument writes the manifest into dir with a temporary file and a
// rename, so a reader sees either no manifest or a complete one.
func writeArtifactDocument(dir string, artifact migration.Artifact) error {
	body, err := encodeDocument(artifact)
	if err != nil {
		return err
	}

	//nolint:gosec // G304: the directory is the snapshot this node just published.
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
	// The manifest is node-local metadata an operator may want to read; 0644 keeps
	// it inspectable while the bytes it describes stay as pkg/snapshot wrote them.
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, filepath.Join(dir, migrationArtifactDocumentName))
}

// pruneSpentArtifacts removes artifact links whose snapshot is gone, so an
// exported artifact does not outlive its bytes as a dangling directory entry.
// Only a link this package named (an artifact ID) whose target no longer resolves
// is removed: an error other than "not there" leaves the entry alone, and a link
// that still resolves is a live artifact. Best effort: a failed prune must not
// fail the export, exactly like the snapshot store's opportunistic retention.
func pruneSpentArtifacts(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 || !migration.ValidArtifactID(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		_ = os.Remove(path)
	}
}

// migrationFileHandler serves GET /v1/tasks/{taskID}/migration/file.
type migrationFileHandler struct {
	cfg MigrationHandlerConfig
}

// serve streams one file of an artifact this task exported. The manifest bounds
// the answer: its size is the Content-Length and its digest is the header, so the
// caller can verify what it received without trusting the stream.
func (h *migrationFileHandler) serve(_ context.Context, task Task, w http.ResponseWriter, r *http.Request) {
	if !operationSpecs[MigrationFileOperation].allows(r.Method) {
		writeOperationMethodNotAllowed(w, MigrationFileOperation, r.Method)
		return
	}
	if h.cfg.SnapshotDir == "" {
		writeTypedError(w, http.StatusServiceUnavailable, directoryNotConfigured("snapshot", "serve an artifact"))
		return
	}

	request, queryErr := parseArtifactFileQuery(r)
	if queryErr != nil {
		writeTypedError(w, http.StatusBadRequest, queryErr)
		return
	}

	dir := filepath.Join(h.cfg.SnapshotDir, request.artifactID)
	artifact, status, problem := readArtifactDocument(dir)
	if problem != nil {
		writeTypedError(w, status, problem)
		return
	}
	// The route is task-scoped: it serves the artifact that task exported, never
	// another task's, the same rule the snapshots operation applies to its list.
	if artifact.SourceTaskID != task.ID {
		writeTypedError(w, http.StatusConflict, output.NewErrorf(output.CodeConflict,
			"artifact %s belongs to task %q, not %q", artifact.ID, artifact.SourceTaskID, task.ID).
			WithDetails(map[string]any{"reason": "artifact_task_mismatch"}))
		return
	}

	entry, found := artifactFile(artifact, request.name)
	if !found {
		writeTypedError(w, http.StatusBadRequest, unknownArtifactFile(artifact, request.name))
		return
	}
	serveArtifactFile(w, dir, entry)
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
			return request, invalidMigrationParameter(key, "is not a migration file parameter")
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
	if !validArtifactFileName(request.name) {
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
func serveArtifactFile(w http.ResponseWriter, dir string, entry migration.File) {
	path := filepath.Join(dir, entry.Name)
	//nolint:gosec // G304: the directory is this node's artifact root and the name is one manifest entry.
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeTypedError(w, http.StatusNotFound, artifactNotAvailable())
			return
		}
		writeTypedError(w, http.StatusServiceUnavailable, artifactUnreadable())
		return
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		writeTypedError(w, http.StatusServiceUnavailable, artifactUnreadable())
		return
	}
	if !stat.Mode().IsRegular() || stat.Size() != entry.Size {
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

// serve stages one file a restore will use. The body is written to a temporary
// file, checked against the declared size and digest, and only then renamed into
// the artifact directory: a mismatch leaves nothing behind, and the operation is
// idempotent because the same bytes always pass the same check.
func (h *migrationStageHandler) serve(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
	if !operationSpecs[MigrationStageOperation].allows(r.Method) {
		writeOperationMethodNotAllowed(w, MigrationStageOperation, r.Method)
		return
	}
	if h.cfg.StagingDir == "" {
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

	dir := filepath.Join(h.cfg.StagingDir, request.artifactID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeTypedError(w, http.StatusServiceUnavailable,
			output.NewError(output.CodeUnavailable, "the staging directory is not writable on this node").
				WithDetails(map[string]any{"reason": "staging_unavailable"}))
		return
	}

	if status, problem := stageInto(w, r, dir, request); problem != nil {
		writeTypedError(w, status, problem)
		return
	}

	w.Header().Set(checksumHeader, request.sha256)
	w.WriteHeader(http.StatusNoContent)
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
			return request, invalidMigrationParameter(key, "is not a migration stage parameter")
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
	if !validArtifactFileName(request.name) {
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
	if size > maxArtifactFileBytes {
		return request, invalidMigrationParameter("size",
			"must be at most "+strconv.FormatInt(maxArtifactFileBytes, 10)+" bytes, the largest file a migration carries")
	}
	request.size = size

	request.sha256 = query.Get("sha256")
	if !validSHA256Hex(request.sha256) {
		return request, invalidMigrationParameter("sha256", "must be 64 lowercase hex characters")
	}
	return request, nil
}

// stageInto writes the body to a temporary file in dir and publishes it under its
// name once it matches the request. The temporary file is removed on every
// failure path, so a failed stage leaves the artifact directory as it was.
func stageInto(w http.ResponseWriter, r *http.Request, dir string, request artifactStageRequest) (int, *output.Error) {
	// A temporary name without the file's own name keeps the pattern inside
	// NAME_MAX whatever the caller sent, and keeps a concurrent stage of the same
	// file from writing the same partial file.
	tmp, err := os.CreateTemp(dir, ".partial-*")
	if err != nil {
		return http.StatusServiceUnavailable, output.NewError(output.CodeUnavailable,
			"the staging directory is not writable on this node").
			WithDetails(map[string]any{"reason": "staging_unavailable"})
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

	if err := os.Rename(tmpPath, filepath.Join(dir, request.name)); err != nil {
		return http.StatusServiceUnavailable, output.NewError(output.CodeUnavailable,
			"the staged file could not be published").
			WithDetails(map[string]any{"reason": "staging_unavailable"})
	}
	published = true
	return 0, nil
}

// validArtifactFileName reports whether name is one bare file name a caller may
// stage or read: no path separator, no "..", no control byte, and inside the
// kernel's name length. It is the rule pkg/migration applies to a manifest entry,
// plus the length limit staging needs, so a manifest this node wrote always
// passes it.
func validArtifactFileName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if len(name) > maxArtifactNameBytes {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsAny(name, "\x00\n\r") {
		return false
	}
	return name == filepath.Base(name)
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

// invalidArtifactFileName is the typed 400 for a name that is a path.
func invalidArtifactFileName(name string) *output.Error {
	return output.NewErrorf(output.CodeInvalid,
		"name %q is not a file name: want one bare name without a separator or a \"..\"", name).
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

// writeMigrationDocument writes one migration document with the internal broker
// media type.
func writeMigrationDocument(w http.ResponseWriter, status int, doc MigrationExportDocument) {
	body, err := encodeDocument(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, output.CodeInternal,
			"the migration document could not be encoded")
		return
	}

	w.Header().Set("Content-Type", publicMediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	//nolint:gosec // G705: the body is a JSON document served as the v1 media type.
	_, _ = w.Write(body)
}

// requireEmptyBody refuses a body on an operation that takes none. A handler that
// silently ignored one would accept a caller's mistake and act on a different
// request than the caller sent.
func requireEmptyBody(w http.ResponseWriter, r *http.Request) *output.Error {
	// A chunked body declares no length; a zero-byte cap is enough to tell an
	// empty body from a present one without buffering it.
	r.Body = http.MaxBytesReader(w, r.Body, 0)
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return output.NewError(output.CodeInvalid,
				"migration/export takes no body: the task is named in the path").
				WithDetails(map[string]any{"reason": "unexpected_body"})
		}
		return output.NewError(output.CodeInvalid, "the request body could not be read").
			WithDetails(map[string]any{"reason": "body_unreadable"})
	}
	return nil
}
