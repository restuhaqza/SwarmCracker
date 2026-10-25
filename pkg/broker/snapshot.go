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
// The handler never publishes the snapshot's memory or state path, or its
// socket path. pkg/snapshot errors carry absolute paths, so they are classified
// through the package's sentinel errors and mapped to fixed messages here: only
// the classification crosses the wire. The public layer repeats the same rule
// for the same reason. The checksum is deliberately different: it is a document
// field on the create and restore responses, because it is the integrity value
// a caller may verify and it names no path.
//
// Restore is experimental. It starts a Firecracker process outside SwarmKit's
// task lifecycle, so a restored VM may outlive the task it came from; the
// manager's TrackRestoredProcess is the only bookkeeping pkg/snapshot keeps. A
// task that is still running is refused (409 restore_task_running): its rootfs
// is in use, and restoring a second VM over that file would corrupt both.
//
// Every restore for one task reuses <socketDir>/<taskID>-restored.sock, and
// pkg/snapshot unlinks that path before it starts the new process. Three guards
// protect the VM that owns the rootfs and socket:
//
//   - A task that is still running is refused before any store call: 409 conflict
//     with details.reason "restore_task_running".
//   - Restores for one task are serialized inside this handler, so two requests
//     in the same daemon cannot race for the same socket.
//   - Before restoring, the handler probes the restored socket with
//     runtime.IsVMSocketAlive. A live Firecracker API socket means a previous
//     restore is still running; replacing it would orphan that process, so the
//     request is 409 conflict with details.reason "restore_already_running". A
//     stale socket file (process gone) is not alive and still proceeds:
//     pkg/snapshot removes it. Nothing is probed when neither a handler socket
//     directory nor the task's own socket directory is known: that restore would
//     write a CWD-relative socket, so it is 503 unavailable with
//     details.reason "socket_dir_unconfigured".
//
// Residual risk: a process started by a previous daemon lifetime is only caught
// while its API socket answers. A crash before the socket is created, or a
// restored VM whose socket was removed out of band, can still leave an untracked
// Firecracker process behind. That residual risk is documented in the public
// OpenAPI description as well.
//
// A nil store keeps the operation registered but answers a typed 501: the public
// layer already gates snapshots on the capability, so this is defence in depth.
// A typed-nil store counts as nil. The method is checked first, so a method the
// operation does not implement is 405 with Allow even while snapshots are
// disabled; only GET, POST and DELETE can answer 501.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
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

	// maxSnapshotScopeBytes bounds one service/node query value. Real IDs are far
	// shorter; the cap only stops a caller from pushing a path-sized value through.
	maxSnapshotScopeBytes = 256

	// restoreSocketProbeTimeout bounds the liveness probe of a restored VM's API
	// socket. A live Firecracker answers a machine-config request in microseconds;
	// the timeout only covers a wedged process.
	restoreSocketProbeTimeout = 500 * time.Millisecond
)

// SnapshotStore is the narrow view of *snapshot.Manager the handler needs. Tests
// substitute their own so no test ever reaches the real restore path, which
// shells out to firecracker.
type SnapshotStore interface {
	CreateSnapshot(ctx context.Context, taskID, socketPath string, opts snapshot.CreateOptions) (*snapshot.SnapshotInfo, error)
	ListSnapshots(filter snapshot.SnapshotFilter) ([]*snapshot.SnapshotInfo, error)
	RestoreFromSnapshot(ctx context.Context, info *snapshot.SnapshotInfo, socketPath string) error
	DeleteSnapshot(ctx context.Context, snapshotID string) error
	CleanupOldSnapshots(ctx context.Context, maxAge time.Duration) (int, int64, error)
}

// SnapshotHandlerConfig configures the snapshots operation.
type SnapshotHandlerConfig struct {
	// Store is the node-local snapshot store. Nil keeps the route registered but
	// answers 501 not_implemented with details.capability "snapshots".
	Store SnapshotStore
	// SocketDir is the directory for restored VM sockets. Restore writes
	// <SocketDir>/<taskID>-restored.sock; empty falls back to the directory of the
	// task's own socket, so a running task still restores beside itself.
	SocketDir string
	// MaxAge enables opportunistic retention: after a successful create the
	// handler removes snapshots older than MaxAge. Zero disables the cleanup.
	MaxAge time.Duration
	// RootfsDir is the executor's rootfs directory. A Firecracker snapshot
	// records the VM's block-device path but not the disk contents, so the
	// handler records <RootfsDir>/tasks/<taskID>.ext4 as the snapshot's rootfs
	// when the caller does not name one. Restore then fails with a precise
	// snapshot_incomplete instead of starting a VMM that cannot open its disk.
	// Empty leaves the field unset.
	RootfsDir string
}

// snapshotHandler holds the per-task restore lock. The lock lives in the handler
// instance, so two requests that reach the same daemon share it.
type snapshotHandler struct {
	cfg SnapshotHandlerConfig

	mu        sync.Mutex
	restoring map[string]struct{}
}

// NewSnapshotHandler builds the snapshots operation. A nil store keeps the route
// and answers 501 not_implemented with details.capability "snapshots". A typed
// nil (for example a (*snapshot.Manager)(nil) stored in the interface) is the
// same as nil: the store implements nothing, and calling it would panic.
func NewSnapshotHandler(cfg SnapshotHandlerConfig) Handler {
	if snapshotStoreDisabled(cfg.Store) {
		cfg.Store = nil
	}
	h := &snapshotHandler{cfg: cfg, restoring: make(map[string]struct{})}
	return h.serve
}

// snapshotStoreDisabled reports whether store implements nothing. A typed-nil
// interface value is not == nil, so the interface comparison alone would let a
// nil *snapshot.Manager reach a method call and panic; the kind check covers it.
func snapshotStoreDisabled(store SnapshotStore) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// serve dispatches one snapshot request by method and query. The method check
// runs before the store gate: a method the operation does not implement is 405
// with Allow even when snapshots are disabled, and 501 is reserved for the
// methods the operation really serves.
func (h *snapshotHandler) serve(ctx context.Context, task Task, w http.ResponseWriter, r *http.Request) {
	if !snapshotMethodAllowed(r.Method) {
		w.Header().Set("Allow", operationSpecs[snapshotOperation].allow())
		writeTypedError(w, http.StatusMethodNotAllowed, output.NewErrorf(output.CodeInvalid,
			"method %s is not allowed on a snapshot", r.Method).
			WithDetails(map[string]any{"reason": "method_not_allowed"}))
		return
	}
	if snapshotStoreDisabled(h.cfg.Store) {
		writeSnapshotDisabled(w)
		return
	}
	if err := validateSnapshotQuery(r); err != nil {
		writeTypedError(w, http.StatusBadRequest, err)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.serveList(task, w)
	case http.MethodPost:
		if r.URL.Query().Has("restore") {
			h.serveRestore(ctx, task, r.URL.Query().Get("restore"), w)
			return
		}
		h.serveCreate(ctx, task, w, r)
	case http.MethodDelete:
		h.serveDelete(ctx, task, r.URL.Query().Get("id"), w)
	}
}

// snapshotMethodAllowed reports whether the handler serves method. The method
// set lives in operationSpecs so the server's 405 check and the handler cannot
// disagree about it.
func snapshotMethodAllowed(method string) bool {
	return operationSpecs[snapshotOperation].allows(method)
}

// validateSnapshotQuery enforces the small query grammar: `restore` belongs to a
// POST and `id` to a DELETE. `service` and `node` are internal create scope the
// manager sends, so they are accepted only on a create, once each, and must be
// opaque values that could never look like a path. Anything else would be
// silently ignored, which would turn a typo into the wrong operation.
func validateSnapshotQuery(r *http.Request) *output.Error {
	query := r.URL.Query()
	if err := validateSnapshotQueryKeys(query); err != nil {
		return err
	}

	switch r.Method {
	case http.MethodGet:
		return validateSnapshotListQuery(query)
	case http.MethodPost:
		return validateSnapshotCreateQuery(query)
	case http.MethodDelete:
		return validateSnapshotDeleteQuery(query)
	}
	return nil
}

// validateSnapshotQueryKeys rejects an unknown key and a repeated key, which
// would otherwise be silently ignored.
func validateSnapshotQueryKeys(query url.Values) *output.Error {
	for key, values := range query {
		switch key {
		case "id", "restore", "service", "node":
		default:
			return invalidSnapshotParameter(key, "is not a snapshot query parameter")
		}
		if len(values) != 1 {
			return invalidSnapshotParameter(key, "must be set exactly once")
		}
	}
	return nil
}

// validateSnapshotListQuery accepts no parameter at all.
func validateSnapshotListQuery(query url.Values) *output.Error {
	return rejectSnapshotQueryKeys(query, []string{"restore", "id", "service", "node"}, "is not accepted on a list")
}

// validateSnapshotCreateQuery accepts the internal service/node scope on a create
// and only the snapshot ID on a restore.
func validateSnapshotCreateQuery(query url.Values) *output.Error {
	if query.Has("id") {
		return invalidSnapshotParameter("id", "is not accepted on a create")
	}
	if query.Has("restore") {
		if err := rejectSnapshotQueryKeys(query, []string{"service", "node"}, "is not accepted on a restore"); err != nil {
			return err
		}
		if !v1.ValidSnapshotID(query.Get("restore")) {
			return invalidSnapshotParameter("restore", "must be a snapshot ID")
		}
		return nil
	}
	for _, key := range []string{"service", "node"} {
		if query.Has(key) && !validSnapshotScope(query.Get(key)) {
			return invalidSnapshotParameter(key, "must be an opaque ID, not a path")
		}
	}
	return nil
}

// validateSnapshotDeleteQuery requires one well-formed id and nothing else.
func validateSnapshotDeleteQuery(query url.Values) *output.Error {
	if err := rejectSnapshotQueryKeys(query, []string{"restore", "service", "node"}, "is not accepted on a delete"); err != nil {
		return err
	}
	if !query.Has("id") || query.Get("id") == "" {
		return invalidSnapshotParameter("id", "is required to delete a snapshot")
	}
	if !v1.ValidSnapshotID(query.Get("id")) {
		return invalidSnapshotParameter("id", "must be a snapshot ID")
	}
	return nil
}

// rejectSnapshotQueryKeys returns the first present key's typed 400.
func rejectSnapshotQueryKeys(query url.Values, keys []string, message string) *output.Error {
	for _, key := range keys {
		if query.Has(key) {
			return invalidSnapshotParameter(key, message)
		}
	}
	return nil
}

// validSnapshotScope reports whether a service or node value can be carried
// safely. The node treats the value as an opaque string recorded in metadata, so
// the only rule is that it cannot look like a path or carry a control byte.
func validSnapshotScope(value string) bool {
	if value == "" || len(value) > maxSnapshotScopeBytes {
		return false
	}
	if value == "." || value == ".." {
		return false
	}
	return !strings.ContainsAny(value, "/\\\x00\n\r")
}

// invalidSnapshotParameter is the typed 400 for a bad query value.
func invalidSnapshotParameter(parameter, message string) *output.Error {
	return output.NewErrorf(output.CodeInvalid, "snapshot parameter %q %s", parameter, message).
		WithDetails(map[string]any{"parameter": parameter})
}

// serveList answers the task's snapshots in ascending (createdAt, id) order.
// pkg/snapshot sorts newest first; the list contract is oldest first, so the
// handler sorts. The task filter is applied here as well as in the store so a
// store that ignores its filter cannot serve another task's snapshots.
func (h *snapshotHandler) serveList(task Task, w http.ResponseWriter) {
	infos, err := h.cfg.Store.ListSnapshots(snapshot.SnapshotFilter{TaskID: task.ID})
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

// serveCreate snapshots the task's running VM on its own socket. The manager
// sends the task's service and node IDs so the store can group snapshots for
// retention; the node records them as opaque metadata.
func (h *snapshotHandler) serveCreate(ctx context.Context, task Task, w http.ResponseWriter, r *http.Request) {
	if !taskRunning(task) {
		writeSnapshotTaskNotRunning(w, task.ID)
		return
	}

	request, status, err := decodeSnapshotCreateRequest(w, r)
	if err != nil {
		writeTypedError(w, status, err)
		return
	}

	rootfsPath := request.RootfsPath
	if rootfsPath == "" && h.cfg.RootfsDir != "" {
		// The snapshot's memory image references this per-task disk, and the
		// executor removes it when the task is cleaned up. Recording it makes
		// restore's preflight honest instead of failing inside Firecracker.
		rootfsPath = filepath.Join(h.cfg.RootfsDir, "tasks", task.ID+".ext4")
	}
	info, createErr := h.cfg.Store.CreateSnapshot(ctx, task.ID, task.SocketPath, snapshot.CreateOptions{
		ServiceID:  r.URL.Query().Get("service"),
		NodeID:     r.URL.Query().Get("node"),
		VCPUCount:  request.VCPUs,
		MemoryMB:   request.MemoryMB,
		RootfsPath: rootfsPath,
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

	// Retention is opportunistic: the snapshot exists, so a cleanup failure must
	// not turn a successful create into an error. The store keeps its files and a
	// later create retries the cleanup.
	if h.cfg.MaxAge > 0 {
		_, _, _ = h.cfg.Store.CleanupOldSnapshots(ctx, h.cfg.MaxAge)
	}

	writeSnapshotDocument(w, http.StatusCreated, v1.NewSnapshotDocument(snapshotInfoToV1(info)))
}

// serveRestore restores one of the task's snapshots into a fresh Firecracker
// process on <SocketDir>/<taskID>-restored.sock. It requires a task whose own VM
// is gone: a running task's rootfs is still in use, and pkg/snapshot restores the
// same file, so a second Firecracker on one rootfs.ext4 would corrupt both. An
// exited task is the normal case — the snapshot's main use is bringing a
// finished VM back. Restores for one task are serialized, and a restored VM that
// is still answering on that socket is never replaced: pkg/snapshot would unlink
// the socket and orphan the process.
func (h *snapshotHandler) serveRestore(ctx context.Context, task Task, snapshotID string, w http.ResponseWriter) {
	if task.Running {
		writeTypedError(w, http.StatusConflict, output.NewErrorf(output.CodeConflict,
			"task %q is still running; restoring its snapshot would share the live rootfs", task.ID).
			WithDetails(map[string]any{"reason": "restore_task_running"}))
		return
	}
	if !h.acquireRestore(task.ID) {
		writeTypedError(w, http.StatusConflict, output.NewErrorf(output.CodeConflict,
			"a restore for task %q is already in progress", task.ID).
			WithDetails(map[string]any{"reason": "restore_in_progress"}))
		return
	}
	defer h.releaseRestore(task.ID)

	socketPath, configured := h.restoredSocketPath(task)
	if !configured {
		writeTypedError(w, http.StatusServiceUnavailable, output.NewErrorf(output.CodeUnavailable,
			"this node has no snapshot socket directory to restore task %q", task.ID).
			WithDetails(map[string]any{"reason": "socket_dir_unconfigured"}))
		return
	}

	info, found, err := findTaskSnapshot(h.cfg.Store, task.ID, snapshotID)
	if err != nil {
		writeSnapshotStoreError(w, "list", err)
		return
	}
	if !found {
		writeSnapshotNotFound(w)
		return
	}

	// A live API socket belongs to a restored VM that is still running. Reusing
	// the path would unlink that socket and leave its process untracked, so the
	// request conflicts instead. A stale file is not alive and still proceeds.
	if runtime.IsVMSocketAlive(socketPath, restoreSocketProbeTimeout) {
		writeTypedError(w, http.StatusConflict, output.NewErrorf(output.CodeConflict,
			"a restored VM for task %q is already running on this node", task.ID).
			WithDetails(map[string]any{"reason": "restore_already_running"}))
		return
	}

	// The snapshot records the VM's vsock device with its host-side path
	// <socketDir>/<taskID>.vsock. A VMM that crashed leaves that file behind,
	// and the restored Firecracker cannot bind it again (EADDRINUSE), so clear
	// it first. The task is not running here (the guard above returned), so no
	// live backend owns the path.
	vsockPath := filepath.Join(filepath.Dir(socketPath), task.ID+".vsock")
	if err := os.Remove(vsockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeTypedError(w, http.StatusServiceUnavailable, output.NewErrorf(output.CodeUnavailable,
			"cannot clear the stale vsock path for task %q", task.ID).
			WithDetails(map[string]any{"reason": "vsock_path_busy"}))
		return
	}

	if err := h.cfg.Store.RestoreFromSnapshot(ctx, info, socketPath); err != nil {
		writeSnapshotStoreError(w, "restore", err)
		return
	}

	writeSnapshotDocument(w, http.StatusOK, v1.NewSnapshotDocument(snapshotInfoToV1(info)))
}

// restoredSocketPath is the restored VM's API socket. The configured directory is
// independent of the task's own socket, so a finished task can still restore. The
// boolean is false when neither is known: a CWD-relative socket would make the
// restored VM depend on the daemon's working directory.
func (h *snapshotHandler) restoredSocketPath(task Task) (string, bool) {
	dir := h.cfg.SocketDir
	if dir == "" {
		if task.SocketPath == "" {
			return "", false
		}
		dir = filepath.Dir(task.SocketPath)
	}
	return filepath.Join(dir, task.ID+"-restored.sock"), true
}

// acquireRestore reserves the task's restore slot. False means another restore
// for the same task is still running.
func (h *snapshotHandler) acquireRestore(taskID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, busy := h.restoring[taskID]; busy {
		return false
	}
	h.restoring[taskID] = struct{}{}
	return true
}

// releaseRestore frees the task's restore slot. It always runs, including when
// the store fails, so a failed restore does not wedge the task.
func (h *snapshotHandler) releaseRestore(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.restoring, taskID)
}

// serveDelete removes one of the task's snapshots.
func (h *snapshotHandler) serveDelete(ctx context.Context, task Task, snapshotID string, w http.ResponseWriter) {
	_, found, err := findTaskSnapshot(h.cfg.Store, task.ID, snapshotID)
	if err != nil {
		writeSnapshotStoreError(w, "list", err)
		return
	}
	if !found {
		writeSnapshotNotFound(w)
		return
	}

	if err := h.cfg.Store.DeleteSnapshot(ctx, snapshotID); err != nil {
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

// taskRunning reports whether the task has a live Firecracker API socket.
// Snapshotting needs one; restoring needs the opposite, because the task's own
// rootfs must no longer be in use.
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
// unvalidated value would persist a traversal path. Redundant separators, a
// trailing slash and . segments are not traversal: they name the same absolute
// directory and are accepted, exactly like the public layer's rule.
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

// mapSnapshotStoreError classifies a store failure through pkg/snapshot's
// sentinels without ever repeating its text: pkg/snapshot errors name absolute
// paths, sockets and checksums.
func mapSnapshotStoreError(action string, err error) (int, *output.Error) {
	var netErr net.Error
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) {
		return http.StatusGatewayTimeout, output.NewErrorf(output.CodeTimeout,
			"the snapshot %s timed out", action).
			WithDetails(map[string]any{"reason": "snapshot_timeout"})
	}

	switch {
	case errors.Is(err, snapshot.ErrNotFound):
		return http.StatusNotFound, output.NewError(output.CodeNotFound,
			"the snapshot is not available on this node").
			WithDetails(map[string]any{"reason": "snapshot_not_found"})
	case errors.Is(err, snapshot.ErrSocketMissing):
		// The task's Firecracker socket vanished between the source snapshot of
		// tasks and this call: the task looked running to SwarmKit but has no VM
		// left to snapshot. Distinct from the create precheck's task_not_running,
		// which is a 404 about the task; this is a 409 about the node's view.
		return http.StatusConflict, output.NewError(output.CodeConflict,
			"the task's VM is no longer running on this node").
			WithDetails(map[string]any{"reason": "vm_socket_missing"})
	case errors.Is(err, snapshot.ErrSnapshotIncomplete):
		return http.StatusConflict, output.NewError(output.CodeConflict,
			"the snapshot is incomplete on this node").
			WithDetails(map[string]any{"reason": "snapshot_incomplete"})
	case errors.Is(err, snapshot.ErrChecksumMismatch):
		return http.StatusInternalServerError, output.NewError(output.CodeInternal,
			"the snapshot failed its integrity check").
			WithDetails(map[string]any{"reason": "checksum_mismatch"})
	default:
		return http.StatusServiceUnavailable, output.NewErrorf(output.CodeUnavailable,
			"the snapshot %s failed on this node", action).
			WithDetails(map[string]any{"reason": "snapshot_store_error"})
	}
}
