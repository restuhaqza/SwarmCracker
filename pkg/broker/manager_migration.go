package broker

// This file owns the manager side of a live migration's file transfer: the four
// calls that move one artifact from the node that produced it to the node that
// will restore it, over the existing mTLS relay.
//
//	export  source -> manager   a dump, described as an artifact
//	file    source -> manager   one file of that artifact, streamed
//	stage   manager -> target   the same file, streamed
//	discard either node         the artifact, and the dump behind it
//
// There is no public route for any of this, and no new port or certificate: the
// manager initiates every transfer, so the artifact never has to be reachable
// from outside the cluster. The manager also never holds a whole file: File hands
// its caller 32 KiB at a time, Stage reads its source 32 KiB at a time, and the
// caller in between is the manager's spool (pkg/migration.Store), which writes to
// and reads from disk.
//
// # Node-scoped, not task-scoped
//
// The relay addresses a node, not a task. A migration's target has no task yet -
// SwarmKit creates it when the service is replaced - so there is nothing to
// resolve, and the artifact operations the node serves are artifact-scoped
// anyway. routeForNode is the seam, and the task ID in the URL is a placeholder
// that only export reads: export names the task to dump, which is a real task on
// the source.
//
// # What the manager verifies
//
// A node's answer is not trusted to be the artifact that was asked for. Export
// validates the manifest it receives, and each transfer checks the bytes against
// that manifest on both hops: the size the node announces and the digest it
// promises are the manifest's, or the stream is refused, and the digest the
// manager computes must be too. The target repeats the check on arrival, so a
// corrupt spool cannot become a corrupt guest.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// migrationRelayChunkBytes is the buffer one relayed file uses, in either
// direction. It is a constant rather than io.Copy's default because it is the
// bound this transport promises: a 64 GiB memory image crosses the manager in
// 32 KiB pieces, whatever the size of the image.
const migrationRelayChunkBytes = 32 << 10

// DefaultMigrationTimeout bounds one migration call - an export, a file read, a
// stage or a discard - when ManagerConfig names no deadline. It is six times the
// snapshot deadline on purpose: a transfer is bounded by the link, not by the
// disk, and an artifact may carry 64 GiB, which needs only ~36 MB/s to cross in
// this window. The deadline is still a bound: it exists so a node that stops
// answering mid-stream cannot pin a migration open for ever, and a caller that
// knows its artifacts are smaller can lower it.
const DefaultMigrationTimeout = 30 * time.Minute

// Export asks the source node to dump one task and returns the artifact it
// describes. The task keeps running: the node resumes the VM before it answers,
// so the transfer that follows is not inside the migration's downtime.
//
// sourceNode is the node the task runs on, named explicitly because the caller is
// about to move the task off it. A task that names a different node is refused
// before any request: exporting through the wrong node would answer 404 from a
// node that has never seen the task, which reads like a missing task rather than
// a wiring mistake.
func (m *Manager) Export(ctx context.Context, sourceNode string, task v1.Task) (MigrationExportDocument, error) {
	var none MigrationExportDocument

	if task.ID == "" {
		return none, output.NewError(output.CodeInvalid, "task ID must not be empty")
	}
	if task.NodeID != "" && task.NodeID != sourceNode {
		return none, output.NewErrorf(output.CodeInvalid,
			"task %q runs on node %q: export it through that node, not through %q",
			task.ID, task.NodeID, sourceNode).
			WithDetails(map[string]any{"nodeId": task.NodeID})
	}

	query := make(url.Values, 2)
	if task.ServiceID != "" {
		query.Set("service", task.ServiceID)
	}
	// The manifest records the node the dump was taken on, and the node cannot
	// recover that from the snapshot.
	query.Set("node", sourceNode)

	route, err := m.routeForNode(ctx, sourceNode, task.ID, MigrationExportOperation)
	if err != nil {
		return none, err
	}
	resp, err := m.doNodeRequest(ctx, route, http.MethodPost, query, nil, -1, "", m.migrationTimeout, "migration export")
	if err != nil {
		return none, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return none, decodeNodeError(resp.Body, resp.StatusCode, route.Node.ID, "migration export")
	}

	doc, err := decodeNodeDocument(resp.Body, route.Node.ID, "migration export",
		func(doc MigrationExportDocument) bool {
			return doc.Kind == migrationExportKind && doc.Artifact.ID != ""
		})
	if err != nil {
		return none, err
	}
	// The manifest is what every later transfer is checked against, and the
	// artifact it describes is the target's instructions for recreating the VM.
	// A manifest that does not validate is refused here, where the migration can
	// still be abandoned, rather than on the target inside a restore.
	if err := doc.Artifact.Validate(); err != nil {
		return none, unreadableArtifact(route.Node.ID, "does not validate")
	}
	if doc.Artifact.SourceNodeID != sourceNode {
		return none, unreadableArtifact(route.Node.ID, "names another source node")
	}

	return doc, nil
}

// File streams one file of an artifact from the node that holds it into w, and
// returns how many bytes it wrote. The manifest bounds the stream in both
// directions: the node must announce the manifest's length and digest, and the
// bytes must hash to that digest, or the transfer is refused.
//
// The node is named rather than resolved from task because the source task may be
// gone by the time its artifact is read; task supplies only the placeholder
// segment of the route.
//
// A failed transfer has already written some bytes to w, so the caller owns
// whatever w holds: the spool's writer discards them with Abort, and that is what
// keeps a refused file from ever looking like a complete one.
func (m *Manager) File(
	ctx context.Context, nodeID string, task v1.Task,
	artifact migration.Artifact, name string, w io.Writer,
) (int64, error) {
	entry, err := artifactEntry(artifact, name)
	if err != nil {
		return 0, err
	}
	if w == nil {
		return 0, output.NewError(output.CodeInternal, "a destination writer is required to relay an artifact file")
	}

	route, err := m.routeForNode(ctx, nodeID, task.ID, MigrationFileOperation)
	if err != nil {
		return 0, err
	}
	query := url.Values{"artifactId": {artifact.ID}, "name": {name}}

	resp, err := m.doNodeRequest(ctx, route, http.MethodGet, query, nil, -1, "", m.migrationTimeout, "migration file")
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, decodeNodeError(resp.Body, resp.StatusCode, route.Node.ID, "migration file")
	}

	// The node announces what it is serving before it serves it. Either answer
	// that is not the manifest's means these are not the bytes the artifact
	// describes, so the stream is refused before it is read.
	if got := resp.Header.Get(checksumHeader); got != "" && got != entry.SHA256 {
		return 0, streamMismatch(route.Node.ID, entry.Name, "checksum_mismatch",
			"node %q serves file %q with SHA-256 %s: the artifact declares %s",
			route.Node.ID, entry.Name, got, entry.SHA256)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != entry.Size {
		return 0, streamMismatch(route.Node.ID, entry.Name, "size_mismatch",
			"node %q serves file %q as %d bytes: the artifact describes %d",
			route.Node.ID, entry.Name, resp.ContentLength, entry.Size)
	}

	written, err := copyArtifactStream(w, resp.Body, entry, route.Node.ID)
	if err != nil {
		var typed *output.Error
		if errors.As(err, &typed) {
			return written, typed
		}
		return written, relayTransportError(ctx, route.Node.ID, err)
	}
	return written, nil
}

// Stage streams one file of an artifact to the node that will restore it. The
// node verifies the bytes against the query it is given and publishes them
// atomically, so a stage either lands complete or not at all; a second stage of
// the same bytes succeeds, which is what makes a retried migration safe.
//
// size and sha256 are the manifest's values for name, passed explicitly because
// they are the query the node checks the body against. They are compared with the
// artifact here rather than trusted: a caller that passed a size the manifest does
// not describe would either be refused by the node or, worse, stage bytes a
// restore would later reject.
//
// Stage reads r and does not close it: the reader is the caller's, and a spooled
// file stays open for the caller to close after the transfer.
func (m *Manager) Stage(
	ctx context.Context, nodeID string, task v1.Task, artifact migration.Artifact,
	name string, size int64, sha256Hex string, r io.Reader,
) error {
	entry, err := artifactEntry(artifact, name)
	if err != nil {
		return err
	}
	if size != entry.Size {
		return output.NewErrorf(output.CodeInvalid,
			"file %q is described as %d bytes: the stage declares %d", name, entry.Size, size).
			WithDetails(map[string]any{"parameter": "size", "size": entry.Size})
	}
	if sha256Hex != entry.SHA256 {
		return output.NewErrorf(output.CodeInvalid,
			"file %q is described with SHA-256 %s: the stage declares %s", name, entry.SHA256, sha256Hex).
			WithDetails(map[string]any{"parameter": "sha256", "sha256": entry.SHA256})
	}
	if r == nil {
		return output.NewErrorf(output.CodeInternal, "a source reader is required to stage file %q", name)
	}

	route, err := m.routeForNode(ctx, nodeID, task.ID, MigrationStageOperation)
	if err != nil {
		return err
	}
	query := url.Values{
		"artifactId": {artifact.ID},
		"name":       {name},
		"size":       {strconv.FormatInt(size, 10)},
		"sha256":     {sha256Hex},
	}

	// The request owns the body it is given and closes it when it is done; r
	// belongs to the caller, who opened it (the spool's reader) and closes it, so
	// the transport is handed a body it cannot close.
	resp, err := m.doNodeRequest(ctx, route, http.MethodPut, query, io.NopCloser(r), size, octetStream,
		m.migrationTimeout, "migration stage")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNoContent:
		// The node echoes the digest it verified; a node that answers 204 without
		// it still verified the body against the query above, so this is a check
		// of the answer, not of the transfer.
		if got := resp.Header.Get(checksumHeader); got != "" && got != sha256Hex {
			return streamMismatch(route.Node.ID, name, "checksum_mismatch",
				"node %q staged file %q as SHA-256 %s: the stage declared %s",
				route.Node.ID, name, got, sha256Hex)
		}
		return nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// A 2xx that is not 204 did not stage: only the 204 contract is a
		// success here.
		return output.NewErrorf(output.CodeInternal,
			"node %q returned status %d for a migration stage", route.Node.ID, resp.StatusCode).
			WithDetails(map[string]any{"nodeId": route.Node.ID})
	default:
		return decodeNodeError(resp.Body, resp.StatusCode, route.Node.ID, "migration stage")
	}
}

// Discard removes one artifact - and the snapshot behind it - from the node that
// holds it. It is idempotent on the node, because a success path, a rollback and
// an abandoned migration may all run it, and it is keyed by the artifact rather
// than by the task, because the task that exported it may already be gone.
func (m *Manager) Discard(ctx context.Context, nodeID string, task v1.Task, artifact migration.Artifact) error {
	if !migration.ValidArtifactID(artifact.ID) {
		return invalidArtifactID("id", artifact.ID)
	}

	route, err := m.routeForNode(ctx, nodeID, task.ID, MigrationDiscardOperation)
	if err != nil {
		return err
	}

	resp, err := m.doNodeRequest(ctx, route, http.MethodDelete,
		url.Values{"artifactId": {artifact.ID}}, nil, -1, "",
		m.migrationTimeout, "migration discard")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return output.NewErrorf(output.CodeInternal,
			"node %q returned status %d for a migration discard", route.Node.ID, resp.StatusCode).
			WithDetails(map[string]any{"nodeId": route.Node.ID})
	}
	return decodeNodeError(resp.Body, resp.StatusCode, route.Node.ID, "migration discard")
}

// artifactEntry resolves the manifest entry a transfer names. The name is checked
// against the manifest rather than passed through: a name the artifact does not
// describe is not a file this migration may move, whichever node asks.
func artifactEntry(artifact migration.Artifact, name string) (migration.File, error) {
	if !migration.ValidArtifactID(artifact.ID) {
		return migration.File{}, invalidArtifactID("id", artifact.ID)
	}
	if err := migration.ValidFileName(name); err != nil {
		return migration.File{}, invalidArtifactFileName(name)
	}
	entry, found := artifactFile(artifact, name)
	if !found {
		return migration.File{}, unknownArtifactFile(artifact, name)
	}
	if err := entry.Validate(); err != nil {
		return migration.File{}, output.NewErrorf(output.CodeInvalid,
			"the artifact's description of file %q is not usable: %v", name, err).
			WithDetails(map[string]any{"parameter": "artifactId", "reason": "invalid_file"})
	}
	return entry, nil
}

// copyArtifactStream relays one file body into w, verifying it as it goes. The
// declared size bounds the read, so a node that serves more than the manifest
// describes is refused rather than relayed; the digest is computed from the bytes
// that were written to w, so it describes exactly what the next hop will read.
//
// The buffer is fixed: the bytes go through it and away, which is what makes a
// multi-gigabyte file a constant amount of memory rather than a slice.
func copyArtifactStream(w io.Writer, src io.Reader, entry migration.File, nodeID string) (int64, error) {
	digest := sha256.New()
	buf := make([]byte, migrationRelayChunkBytes)

	written, err := io.CopyBuffer(
		io.MultiWriter(w, digest),
		io.LimitReader(src, entry.Size),
		buf,
	)
	if err != nil {
		return written, err
	}
	if written != entry.Size {
		return written, output.NewErrorf(output.CodeConflict,
			"file %q arrived as %d bytes: the artifact describes %d", entry.Name, written, entry.Size).
			WithDetails(map[string]any{"nodeId": nodeID, "reason": "size_mismatch", "name": entry.Name, "size": entry.Size})
	}

	// A body longer than the manifest is refused without being written: the
	// caller's writer must never receive more than the file the artifact
	// describes, so the extra byte is read here and dropped rather than relayed.
	var spare [1]byte
	switch n, probeErr := src.Read(spare[:]); {
	case n > 0:
		return written, output.NewErrorf(output.CodeConflict,
			"file %q streamed past the %d bytes the artifact describes", entry.Name, entry.Size).
			WithDetails(map[string]any{"nodeId": nodeID, "reason": "size_mismatch", "name": entry.Name, "size": entry.Size})
	case probeErr != nil && !errors.Is(probeErr, io.EOF):
		return written, probeErr
	}

	if sum := hex.EncodeToString(digest.Sum(nil)); sum != entry.SHA256 {
		return written, output.NewErrorf(output.CodeConflict,
			"file %q arrived with SHA-256 %s: the artifact declares %s", entry.Name, sum, entry.SHA256).
			WithDetails(map[string]any{"nodeId": nodeID, "reason": "checksum_mismatch", "name": entry.Name, "sha256": entry.SHA256})
	}
	return written, nil
}

// streamMismatch builds the typed conflict for bytes that are not the ones the
// artifact describes.
func streamMismatch(nodeID, name, reason, format string, args ...any) *output.Error {
	return output.NewErrorf(output.CodeConflict, format, args...).
		WithDetails(map[string]any{"nodeId": nodeID, "name": name, "reason": reason})
}

// relayTransportError maps a failed read from a node to a stable failure. The
// underlying error can name an internal address, so it never reaches the message.
func relayTransportError(ctx context.Context, nodeID string, err error) *output.Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return output.NewErrorf(output.CodeTimeout, "node %q migration transfer timed out", nodeID).
			WithDetails(map[string]any{"nodeId": nodeID})
	}
	return output.NewErrorf(codeUnavailable, "node %q migration transfer failed", nodeID).
		WithDetails(map[string]any{"nodeId": nodeID})
}

// unreadableArtifact is the typed internal failure for a node that answers an
// export with a manifest this build cannot use. The validation error itself names
// node-local paths, so it stays on the node; the answer names the node and the
// reason.
func unreadableArtifact(nodeID, problem string) *output.Error {
	return output.NewErrorf(output.CodeInternal,
		"node %q returned a migration artifact that %s", nodeID, problem).
		WithDetails(map[string]any{"nodeId": nodeID, "reason": "artifact_invalid"})
}
