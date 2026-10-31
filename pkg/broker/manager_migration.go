package broker

// This file owns the manager side of a live migration's file transfer: the calls
// that move one artifact from the node that produced it to the node that will
// restore it, over the existing mTLS relay, and the directive that tells the target
// to restore it.
//
//	export  source -> manager   a dump, described as an artifact
//	file    source -> manager   one file of that artifact, streamed
//	stage   manager -> target   the same file, streamed
//	discard either node         the artifact, and the dump behind it
//	prepare manager -> target   the target's directive: the next task of a service
//	                            that starts there restores that artifact
//	unprepare manager -> target the directive again, when the migration is abandoned
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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// migrationRelayChunkBytes is the buffer one relayed file uses, in either
// direction. It is a constant rather than io.Copy's default because it is the
// bound this transport promises: a 64 GiB memory image crosses the manager in
// 32 KiB pieces, whatever the size of the image.
const migrationRelayChunkBytes = 32 << 10

// migrationDirectiveRouteTask is the task segment of the directive routes (prepare
// and unprepare). The route shape is the one every node operation has, and both are
// artifact-scoped, so the node never resolves this segment: the task that will restore
// the artifact does not exist yet - SwarmKit creates it when the service is replaced -
// and a migration that is abandoned may never have created one. This placeholder is
// not an ID anyone looks up.
const migrationDirectiveRouteTask = "pending-restore"

// DefaultMigrationTimeout bounds one migration call - an export, a file read, a
// stage or a discard - when ManagerConfig names no deadline. It is six times the
// snapshot deadline on purpose: a transfer is bounded by the link, not by the
// disk, and an artifact may carry 64 GiB, which needs only ~36 MB/s to cross in
// this window. The deadline is still a bound: it exists so a node that stops
// answering mid-stream cannot pin a migration open for ever, and a caller that
// knows its artifacts are smaller can lower it.
//
// It is the transfer deadline pkg/migration names, not a second opinion about it:
// the manager's spool must outlive a transfer (migration.DefaultSpoolTTL is
// derived from it), so the one number lives where the two are ordered.
const DefaultMigrationTimeout = migration.DefaultTransferDeadline

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
	// The manifest is the target's instructions for replacing *this* task in
	// *this* service. A node that answers for another task - or names another
	// service - would have its artifact staged as this service's replacement, so
	// the answer is refused here, before the caller spools a byte.
	if err := artifactDescribesRequest(doc.Artifact, task, sourceNode, route.Node.ID); err != nil {
		return none, err
	}

	return doc, nil
}

// artifactDescribesRequest checks the manifest's own identity against the task the
// export named: the requested task, the source node and the service whose single
// replica moves.
//
// It compares RequestedTaskID, not SourceTaskID. SourceTaskID is the VM's identity
// - it names the TAP device, the vsock muxer and the disk the target recreates -
// and a VM restored from an earlier migration keeps it while running under a new
// task ID. Comparing it would refuse a chain's second hop (A→B, then B→C), where
// the export names B and the VM still describes A.
func artifactDescribesRequest(artifact migration.Artifact, task v1.Task, sourceNode, nodeID string) error {
	if got := artifact.SourceNodeID; got != sourceNode {
		return artifactNamesAnother(nodeID, "source node", sourceNode, got)
	}
	if got := artifact.RequestedTaskID; got != task.ID {
		return artifactNamesAnother(nodeID, "task", task.ID, got)
	}
	if got := artifact.ServiceID; got != task.ServiceID {
		return artifactNamesAnother(nodeID, "service", task.ServiceID, got)
	}
	return nil
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
	// describes, so the stream is refused before it is read. Digest comparison is
	// case-insensitive: hex is hex in either case.
	if got := resp.Header.Get(checksumHeader); got != "" && !strings.EqualFold(got, entry.SHA256) {
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
	//
	// The body is wrapped so a reader that fails on the way out can be blamed on
	// the source: the transport reports a broken request, and without the wrapper
	// that reads like a node that stopped answering.
	source := &stageSource{r: r, want: size}
	resp, err := m.doNodeRequest(ctx, route, http.MethodPut, query, io.NopCloser(source), size, octetStream,
		m.migrationTimeout, "migration stage")
	if err != nil {
		return stageSourceFailure(source, name, route.Node.ID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNoContent:
		// The node echoes the digest it verified; a node that answers 204 without
		// it still verified the body against the query above, so this is a check
		// of the answer, not of the transfer.
		if got := resp.Header.Get(checksumHeader); got != "" && !strings.EqualFold(got, sha256Hex) {
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

// Prepare records the target node's pending-restore directive: the next task of
// serviceID to start there restores artifactID, whose files the manager has already
// staged in that node's own migration root. It is called before SwarmKit creates the
// task, which is why nothing here names one and why the route carries a placeholder
// instead.
//
// The directory is not in the request: each node has its own migration root, so only
// the node can say where the artifact's files are. It derives
// <migration root>/<artifactID> and answers a typed 404 when that is not a real
// directory it holds - a prepare for an artifact that was never staged here is not a
// restore the manager may believe is arranged.
//
// The checks here are the ones that need no node: the IDs must be the shapes the node
// accepts. A rejected prepare is a request the node refuses, so the manager never
// reports a restore as arranged.
func (m *Manager) Prepare(ctx context.Context, nodeID, serviceID, artifactID string) error {
	if err := scruntime.CheckTaskID(serviceID); err != nil {
		return invalidServiceID(serviceID)
	}
	if !migration.ValidArtifactID(artifactID) {
		return invalidArtifactID("artifactId", artifactID)
	}

	body, err := json.Marshal(migrationPrepareRequest{
		ServiceID:  serviceID,
		ArtifactID: artifactID,
	})
	if err != nil {
		return output.NewErrorf(output.CodeInternal,
			"the pending-restore directive for service %q could not be encoded", serviceID).
			WithDetails(map[string]any{"serviceId": serviceID, "reason": "directive_unencodable"})
	}

	route, err := m.routeForNode(ctx, nodeID, migrationDirectiveRouteTask, MigrationPrepareOperation)
	if err != nil {
		return err
	}

	resp, err := m.doNodeRequest(ctx, route, http.MethodPost, nil, bytes.NewReader(body),
		int64(len(body)), migrationPrepareMediaType, m.migrationTimeout, "migration prepare")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNoContent:
		return nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// Only the 204 contract is a recorded directive: a 2xx that is not one did
		// not record anything here.
		return output.NewErrorf(output.CodeInternal,
			"node %q returned status %d for a migration prepare", route.Node.ID, resp.StatusCode).
			WithDetails(map[string]any{"nodeId": route.Node.ID})
	default:
		return decodeNodeError(resp.Body, resp.StatusCode, route.Node.ID, "migration prepare")
	}
}

// Unprepare removes the target node's pending-restore directive for a service, but
// only while it names artifactID. It is how an abandoned migration cleans up after
// itself: the artifact is being discarded, so a directive that still named it would
// leave the service's next task here trying to restore something that is gone.
//
// The artifact is part of the call because two managers can migrate one service: the
// node keeps one directive per service, the later prepare replaces the earlier one,
// and the manager that abandons afterwards must not delete the directive the winner
// is about to consume. A node that answers 409 directive_replaced still holds another
// migration's directive, and the caller learns that rather than being told the
// service is clear.
//
// The node answers 204 whether or not it held a directive - a rollback may run twice,
// and may run where the prepare never arrived - so a retried cleanup succeeds.
func (m *Manager) Unprepare(ctx context.Context, nodeID, serviceID, artifactID string) error {
	if err := scruntime.CheckTaskID(serviceID); err != nil {
		return invalidServiceID(serviceID)
	}
	if !migration.ValidArtifactID(artifactID) {
		return invalidArtifactID("artifactId", artifactID)
	}

	route, err := m.routeForNode(ctx, nodeID, migrationDirectiveRouteTask, MigrationUnprepareOperation)
	if err != nil {
		return err
	}

	resp, err := m.doNodeRequest(ctx, route, http.MethodDelete,
		url.Values{"serviceId": {serviceID}, "artifactId": {artifactID}}, nil, -1, "",
		m.migrationTimeout, "migration unprepare")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// The operation promises a 204: a 2xx that is not one did not confirm the
		// removal, and the caller must not believe the directive is gone.
		return output.NewErrorf(output.CodeInternal,
			"node %q returned status %d for a migration unprepare", route.Node.ID, resp.StatusCode).
			WithDetails(map[string]any{"nodeId": route.Node.ID})
	}
	return decodeNodeError(resp.Body, resp.StatusCode, route.Node.ID, "migration unprepare")
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

// artifactNamesAnother is the typed internal failure for a node that answers with a
// manifest describing something other than what the export asked about. Both values
// travel in the message and in the details: the point of the refusal is which two
// sides disagree.
func artifactNamesAnother(nodeID, field, want, got string) *output.Error {
	return output.NewErrorf(output.CodeInternal,
		"node %q returned a migration artifact that names another %s: %q, but the export named %q",
		nodeID, field, got, want).
		WithDetails(map[string]any{
			"nodeId": nodeID, "reason": "artifact_mismatch",
			"field": field, "want": want, "got": got,
		})
}

// stageSource is the reader a Stage hands the transport. It remembers the first
// failure of the caller's reader, because the transport can only report it as a
// broken request - which is indistinguishable from a node that stopped answering.
//
// Reads happen on the transport's writing goroutine, so the record is guarded: the
// caller reads it after Do returns.
type stageSource struct {
	r    io.Reader
	want int64
	read int64

	mu  sync.Mutex
	err error
}

// Read passes the next slice through and records what the source did.
func (s *stageSource) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)

	s.mu.Lock()
	s.read += int64(n)
	if err != nil && s.err == nil {
		switch {
		case !errors.Is(err, io.EOF):
			s.err = err
		case s.read < s.want:
			// A source shorter than the manifest is this side's failure too: the
			// transport reports it as a body shorter than its length, which would
			// otherwise read like a node that hung up.
			s.err = io.ErrUnexpectedEOF
		}
	}
	s.mu.Unlock()

	return n, err
}

// failure returns the source's own failure, or nil when the reader behaved.
func (s *stageSource) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// stageSourceFailure answers a stage whose request failed. A source that failed is
// the manager's failure and keeps its own classification; anything else is the
// transport's, and the transport's mapping is what the caller already knows.
func stageSourceFailure(source *stageSource, name, nodeID string, err error) error {
	if srcErr := source.failure(); srcErr != nil {
		return unreadableSource(name, nodeID, srcErr)
	}
	return err
}

// unreadableSource is the typed failure for a stage whose own bytes could not be
// read. A classified failure (the spool refusing a file whose digest is not the
// manifest's) crosses verbatim, so "the spool is wrong" and "the node is down"
// never read the same way. An unclassified one is internal, not unavailable: the
// caller handed the transport a reader that failed, and answering with the node's
// code would blame the wrong end of the link. It names the file and the node for
// correlation only - the error text can name a path on the manager, so it stays
// here.
func unreadableSource(name, nodeID string, err error) error {
	var typed *output.Error
	if errors.As(err, &typed) {
		return typed
	}
	return output.NewErrorf(output.CodeInternal,
		"file %q could not be read to stage it: node %q was never sent the file", name, nodeID).
		WithDetails(map[string]any{"nodeId": nodeID, "name": name, "reason": "source_unreadable"})
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
