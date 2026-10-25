package broker

// This file owns the node-side snapshots operation:
//
//	GET    /v1/tasks/{taskID}/snapshots
//	POST   /v1/tasks/{taskID}/snapshots
//	POST   /v1/tasks/{taskID}/snapshots?restore=<id>
//	DELETE /v1/tasks/{taskID}/snapshots?id=<id>
//
// A snapshot is node-local: pkg/snapshot writes <snapshotDir>/<id>/metadata.json
// and that directory is the source of truth. The node therefore answers from its
// own store, filtered to the task the manager resolved; there is no manager-side
// index and no SwarmKit record.
//
// The handler never publishes the snapshot's memory or state path, its socket
// path or its checksum. pkg/snapshot errors carry absolute paths, so they are
// mapped to fixed messages here: only the classification crosses the wire. The
// public layer repeats the same rule for the same reason.
//
// Restore is experimental. It starts a Firecracker process outside SwarmKit's
// task lifecycle, so a restored VM may outlive the task it came from; the
// manager's TrackRestoredProcess is the only bookkeeping pkg/snapshot keeps.
//
// A nil store keeps the operation registered but answers a typed 501: the public
// layer already gates snapshots on the capability, so this is defence in depth.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

const (
	// snapshotOperation is the broker operation name for the snapshot surface.
	snapshotOperation = "snapshots"

	// snapshotBodyLimit bounds one optional create body. The three fields are
	// short; the cap only stops a hostile caller from exhausting memory.
	snapshotBodyLimit = 64 << 10

	// defaultSnapshotTimeout bounds one manager-side snapshot request. Create and
	// restore copy a whole VM memory image, so this is well above a health probe;
	// ManagerConfig.SnapshotTimeout overrides it.
	defaultSnapshotTimeout = 30 * time.Second
)

// SnapshotStore is the narrow view of *snapshot.Manager the handler needs. Tests
// substitute their own so no test ever reaches the real restore path, which
// shells out to firecracker.
type SnapshotStore interface {
	CreateSnapshot(ctx context.Context, taskID, socketPath string, opts snapshot.CreateOptions) (*snapshot.SnapshotInfo, error)
	ListSnapshots(filter snapshot.SnapshotFilter) ([]*snapshot.SnapshotInfo, error)
	RestoreFromSnapshot(ctx context.Context, info *snapshot.SnapshotInfo, socketPath string) error
	DeleteSnapshot(ctx context.Context, snapshotID string) error
}

// NewSnapshotHandler builds the snapshots operation. A nil store keeps the route
// and answers 501 not_implemented with details.capability "snapshots".
func NewSnapshotHandler(store SnapshotStore) Handler {
	return func(ctx context.Context, task Task, w http.ResponseWriter, r *http.Request) {
		serveSnapshots(ctx, store, task, w, r)
	}
}

// serveSnapshots dispatches one snapshot request by method and query.
func serveSnapshots(ctx context.Context, store SnapshotStore, task Task, w http.ResponseWriter, r *http.Request) {
	if store == nil {
		writeSnapshotDisabled(w)
		return
	}
	if !snapshotMethodAllowed(r.Method) {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost+", "+http.MethodDelete)
		writeError(w, http.StatusMethodNotAllowed, output.CodeInvalid,
			"method %s is not allowed on a snapshot", r.Method)
		return
	}
	if err := validateSnapshotQuery(r); err != nil {
		writeTypedError(w, http.StatusBadRequest, err)
		return
	}

	switch r.Method {
	case http.MethodGet:
		serveSnapshotList(store, task, w)
	case http.MethodPost:
		if r.URL.Query().Has("restore") {
			serveSnapshotRestore(ctx, store, task, r.URL.Query().Get("restore"), w)
			return
		}
		serveSnapshotCreate(ctx, store, task, w, r)
	case http.MethodDelete:
		serveSnapshotDelete(ctx, store, task, r.URL.Query().Get("id"), w)
	}
}

// snapshotMethodAllowed reports whether the handler serves method.
func snapshotMethodAllowed(method string) bool {
	return method == http.MethodGet || method == http.MethodPost || method == http.MethodDelete
}

// validateSnapshotQuery enforces the small query grammar: `restore` belongs to a
// POST and `id` to a DELETE. Anything else would be silently ignored, which
// would turn a typo into the wrong operation.
func validateSnapshotQuery(r *http.Request) *output.Error {
	query := r.URL.Query()
	for key, values := range query {
		if key != "id" && key != "restore" {
			return invalidSnapshotParameter(key, "is not a snapshot query parameter")
		}
		if len(values) != 1 {
			return invalidSnapshotParameter(key, "must be set exactly once")
		}
	}

	switch r.Method {
	case http.MethodGet:
		if query.Has("restore") {
			return invalidSnapshotParameter("restore", "is not accepted on a list")
		}
		if query.Has("id") {
			return invalidSnapshotParameter("id", "is not accepted on a list")
		}
	case http.MethodPost:
		if query.Has("id") {
			return invalidSnapshotParameter("id", "is not accepted on a create")
		}
		if query.Has("restore") && !v1.ValidSnapshotID(query.Get("restore")) {
			return invalidSnapshotParameter("restore", "must be a snapshot ID")
		}
	case http.MethodDelete:
		if query.Has("restore") {
			return invalidSnapshotParameter("restore", "is not accepted on a delete")
		}
		if !query.Has("id") || query.Get("id") == "" {
			return invalidSnapshotParameter("id", "is required to delete a snapshot")
		}
		if !v1.ValidSnapshotID(query.Get("id")) {
			return invalidSnapshotParameter("id", "must be a snapshot ID")
		}
	}
	return nil
}

// invalidSnapshotParameter is the typed 400 for a bad query value.
func invalidSnapshotParameter(parameter, message string) *output.Error {
	return output.NewErrorf(output.CodeInvalid, "snapshot parameter %q %s", parameter, message).
		WithDetails(map[string]any{"parameter": parameter})
}

// serveSnapshotList answers the task's snapshots in ascending (createdAt, id)
// order. pkg/snapshot sorts newest first; the list contract is oldest first, so
// the handler sorts. The task filter is applied here as well as in the store so a
// store that ignores its filter cannot serve another task's snapshots.
func serveSnapshotList(store SnapshotStore, task Task, w http.ResponseWriter) {
	infos, err := store.ListSnapshots(snapshot.SnapshotFilter{TaskID: task.ID})
	if err != nil {
		writeSnapshotStoreError(w, "list", err)
		return
	}

	owned := make([]*snapshot.SnapshotInfo, 0, len(infos))
	for _, info := range infos {
		if info != nil && info.TaskID == task.ID {
			owned = append(owned, info)
		}
	}
	sort.SliceStable(owned, func(i, j int) bool {
		if !owned[i].CreatedAt.Equal(owned[j].CreatedAt) {
			return owned[i].CreatedAt.Before(owned[j].CreatedAt)
		}
		return owned[i].ID < owned[j].ID
	})

	writeSnapshotDocument(w, http.StatusOK, v1.NewSnapshotList(snapshotInfosToV1(owned)))
}

// serveSnapshotCreate snapshots the task's running VM on its own socket.
func serveSnapshotCreate(ctx context.Context, store SnapshotStore, task Task, w http.ResponseWriter, r *http.Request) {
	if !taskRunning(task) {
		writeSnapshotTaskNotRunning(w, task.ID)
		return
	}

	request, status, err := decodeSnapshotCreateRequest(w, r)
	if err != nil {
		writeTypedError(w, status, err)
		return
	}

	info, createErr := store.CreateSnapshot(ctx, task.ID, task.SocketPath, snapshot.CreateOptions{
		VCPUCount:  request.VCPUs,
		MemoryMB:   request.MemoryMB,
		RootfsPath: request.RootfsPath,
	})
	if createErr != nil {
		writeSnapshotStoreError(w, "create", createErr)
		return
	}
	if info == nil {
		writeError(w, http.StatusInternalServerError, output.CodeInternal,
			"task %q snapshot returned no metadata", task.ID)
		return
	}

	writeSnapshotDocument(w, http.StatusCreated, v1.NewSnapshotDocument(snapshotInfoToV1(info)))
}

// serveSnapshotRestore restores one of the task's snapshots into a fresh
// Firecracker process on <socketDir>/<taskID>-restored.sock.
func serveSnapshotRestore(ctx context.Context, store SnapshotStore, task Task, snapshotID string, w http.ResponseWriter) {
	if !taskRunning(task) {
		writeSnapshotTaskNotRunning(w, task.ID)
		return
	}

	info, found, err := findTaskSnapshot(store, task.ID, snapshotID)
	if err != nil {
		writeSnapshotStoreError(w, "list", err)
		return
	}
	if !found {
		writeSnapshotNotFound(w)
		return
	}

	socketPath := filepath.Join(filepath.Dir(task.SocketPath), task.ID+"-restored.sock")
	if err := store.RestoreFromSnapshot(ctx, info, socketPath); err != nil {
		writeSnapshotStoreError(w, "restore", err)
		return
	}

	writeSnapshotDocument(w, http.StatusOK, v1.NewSnapshotDocument(snapshotInfoToV1(info)))
}

// serveSnapshotDelete removes one of the task's snapshots.
func serveSnapshotDelete(ctx context.Context, store SnapshotStore, task Task, snapshotID string, w http.ResponseWriter) {
	_, found, err := findTaskSnapshot(store, task.ID, snapshotID)
	if err != nil {
		writeSnapshotStoreError(w, "list", err)
		return
	}
	if !found {
		writeSnapshotNotFound(w)
		return
	}

	if err := store.DeleteSnapshot(ctx, snapshotID); err != nil {
		writeSnapshotStoreError(w, "delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// findTaskSnapshot resolves an ID inside the task's own snapshot set. A snapshot
// that exists on the node but belongs to another task is not found: the route is
// task-scoped, and the store's directory is not a cross-task namespace.
func findTaskSnapshot(store SnapshotStore, taskID, snapshotID string) (*snapshot.SnapshotInfo, bool, error) {
	infos, err := store.ListSnapshots(snapshot.SnapshotFilter{TaskID: taskID})
	if err != nil {
		return nil, false, err
	}
	for _, info := range infos {
		if info != nil && info.ID == snapshotID && info.TaskID == taskID {
			return info, true, nil
		}
	}
	return nil, false, nil
}

// taskRunning reports whether the task has a live Firecracker API socket, which
// both create and restore need.
func taskRunning(task Task) bool {
	return task.Running && task.SocketPath != ""
}

// decodeSnapshotCreateRequest reads the optional strict create body. An empty
// body is valid: the node then records no explicit resources.
func decodeSnapshotCreateRequest(w http.ResponseWriter, r *http.Request) (v1.SnapshotCreateRequest, int, *output.Error) {
	var request v1.SnapshotCreateRequest

	r.Body = http.MaxBytesReader(w, r.Body, snapshotBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return request, http.StatusRequestEntityTooLarge, payloadTooLarge(snapshotBodyLimit)
		}
		return request, http.StatusBadRequest, output.NewErrorf(output.CodeInvalid,
			"read request body: %v", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return request, 0, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"invalid JSON body: the snapshot request has unknown or malformed fields").
			WithDetails(map[string]any{"reason": "invalid_body"})
	}
	if err := requireSingleJSONValue(decoder); err != nil {
		return request, http.StatusBadRequest, typedExecError(err)
	}
	if request.VCPUs < 0 || request.MemoryMB < 0 {
		return request, http.StatusBadRequest, output.NewError(output.CodeInvalid,
			"vcpus and memoryMB must be zero or positive").
			WithDetails(map[string]any{"reason": "invalid_resources"})
	}
	if request.RootfsPath != "" && !validRootfsPath(request.RootfsPath) {
		return request, http.StatusBadRequest, invalidSnapshotParameter("rootfsPath",
			"must be an absolute path without a .. segment")
	}
	return request, 0, nil
}

// validRootfsPath rejects a relative path and one that tries to leave its
// directory. The path is node-local data recorded in the snapshot metadata, so an
// unvalidated value would persist a traversal path.
func validRootfsPath(raw string) bool {
	if !filepath.IsAbs(raw) || strings.ContainsAny(raw, "\x00\n\r") {
		return false
	}
	for _, segment := range strings.Split(raw, "/") {
		if segment == ".." {
			return false
		}
	}
	return filepath.Clean(raw) != "/"
}

// snapshotInfoToV1 maps pkg/snapshot's record onto the wire DTO. The memory and
// state paths are deliberately not copied.
func snapshotInfoToV1(info *snapshot.SnapshotInfo) v1.Snapshot {
	out := v1.Snapshot{
		ID:         info.ID,
		TaskID:     info.TaskID,
		ServiceID:  info.ServiceID,
		NodeID:     info.NodeID,
		SizeBytes:  info.SizeBytes,
		MemoryMB:   info.MemoryMB,
		VCPUCount:  info.VCPUCount,
		RootfsPath: info.RootfsPath,
		Checksum:   info.Checksum,
	}
	if !info.CreatedAt.IsZero() {
		out.CreatedAt = info.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// snapshotInfosToV1 maps a list without ever returning nil.
func snapshotInfosToV1(infos []*snapshot.SnapshotInfo) []v1.Snapshot {
	out := make([]v1.Snapshot, 0, len(infos))
	for _, info := range infos {
		out = append(out, snapshotInfoToV1(info))
	}
	return out
}

// writeSnapshotDocument writes one snapshot document with the internal broker
// media type; the manager rewrites it into the public envelope for a caller.
func writeSnapshotDocument(w http.ResponseWriter, status int, value any) {
	body, err := encodeDocument(value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, output.CodeInternal,
			"snapshot document could not be encoded")
		return
	}

	w.Header().Set("Content-Type", publicMediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	//nolint:gosec // G705: the body is a JSON document served as the v1 media type.
	_, _ = w.Write(body)
}

// writeSnapshotDisabled is the 501 for a node with no snapshot store.
func writeSnapshotDisabled(w http.ResponseWriter) {
	writeTypedError(w, http.StatusNotImplemented, output.NewError(output.CodeNotImplemented,
		"snapshots are not enabled on this node").
		WithDetails(map[string]any{"capability": snapshotOperation, "reason": "snapshots_disabled"}))
}

// writeSnapshotTaskNotRunning is the 404 for a task whose VM has exited.
func writeSnapshotTaskNotRunning(w http.ResponseWriter, taskID string) {
	writeTypedError(w, http.StatusNotFound, output.NewErrorf(output.CodeNotFound,
		"task %q is not running", taskID).
		WithDetails(map[string]any{"reason": "task_not_running"}))
}

// writeSnapshotNotFound is the 404 for a snapshot the node does not hold for the
// task.
func writeSnapshotNotFound(w http.ResponseWriter) {
	writeTypedError(w, http.StatusNotFound, output.NewError(output.CodeNotFound,
		"the snapshot is not available on this node").
		WithDetails(map[string]any{"reason": "snapshot_not_found"}))
}

// writeSnapshotStoreError maps a pkg/snapshot failure onto a stable document.
func writeSnapshotStoreError(w http.ResponseWriter, action string, err error) {
	status, typed := mapSnapshotStoreError(action, err)
	writeTypedError(w, status, typed)
}

// mapSnapshotStoreError classifies a store failure without ever repeating its
// text: pkg/snapshot errors name absolute paths, sockets and checksums.
func mapSnapshotStoreError(action string, err error) (int, *output.Error) {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) {
		return http.StatusGatewayTimeout, output.NewErrorf(output.CodeTimeout,
			"the snapshot %s timed out", action).WithDetails(map[string]any{
			"reason":    "snapshot_timeout",
			"retryable": true,
		})
	}

	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "not found"), strings.Contains(message, "no such file"):
		return http.StatusNotFound, output.NewError(output.CodeNotFound,
			"the snapshot is not available on this node").
			WithDetails(map[string]any{"reason": "snapshot_not_found"})
	case strings.Contains(message, "firecracker"):
		// The binary is missing, the API socket never came up or a call was
		// rejected. The retryability of the error document already says whether
		// a retry can help, so no internal detail is added.
		return http.StatusServiceUnavailable, output.NewError(output.CodeUnavailable,
			"snapshots are unavailable on this node").
			WithDetails(map[string]any{"reason": "snapshots_unavailable"})
	case strings.Contains(message, "checksum"):
		return http.StatusInternalServerError, output.NewError(output.CodeInternal,
			"the snapshot failed its integrity check").
			WithDetails(map[string]any{"reason": "checksum_mismatch"})
	default:
		return http.StatusServiceUnavailable, output.NewErrorf(output.CodeUnavailable,
			"the snapshot %s failed on this node", action).
			WithDetails(map[string]any{"reason": "snapshot_store_error"})
	}
}
