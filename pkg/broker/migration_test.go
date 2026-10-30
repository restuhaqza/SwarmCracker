package broker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

// serveMigration drives one migration handler directly, with the task the server
// would have resolved. task resolution is the server's job and is exercised
// separately.
func serveMigration(
	t *testing.T, handler Handler, task Task, method, target string, body []byte,
) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		// httptest sets ContentLength from a *bytes.Reader; the handlers must see
		// the same value a real client would send.
		req.ContentLength = int64(len(body))
	}
	rec := httptest.NewRecorder()
	handler(context.Background(), task, rec, req)
	return rec
}

// decodeMigrationError decodes a handler's error answer.
func decodeMigrationError(t *testing.T, rec *httptest.ResponseRecorder) v1.ErrorDocument {
	t.Helper()

	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc), rec.Body.String())
	require.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
	require.Equal(t, v1.KindError, doc.Kind)
	return doc
}

// seedSnapshot publishes the directory pkg/snapshot would have published, so an
// export has real files to describe and its checksums can be checked against the
// bytes the artifact directory exposes. The state file's digest is the snapshot's
// checksum, exactly as snapshot.Manager records it.
func seedSnapshot(t *testing.T, root string) *snapshot.SnapshotInfo {
	t.Helper()

	dir := filepath.Join(root, "snap-0123456789abcdef")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	state := []byte("VMSTATE: paused at the dump instant\n")
	memory := []byte(strings.Repeat("m", 4096))
	disk := []byte(strings.Repeat("d", 8192))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vm.state"), state, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vm.mem"), memory, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rootfs.ext4"), disk, 0o600))

	sum := sha256.Sum256(state)
	return &snapshot.SnapshotInfo{
		ID:         "snap-0123456789abcdef",
		TaskID:     "task-1",
		ServiceID:  "svc-1",
		NodeID:     "node-1",
		CreatedAt:  time.Now().UTC().Add(-time.Minute),
		StatePath:  filepath.Join(dir, "vm.state"),
		MemoryPath: filepath.Join(dir, "vm.mem"),
		DiskPath:   filepath.Join(dir, "rootfs.ext4"),
		RootfsPath: "/var/lib/firecracker/rootfs/tasks/task-1.ext4",
		Checksum:   hex.EncodeToString(sum[:]),
	}
}

// migrationConfig is a fully configured node: one artifact root, which is not the
// snapshot root (they are separate flags on a real node), and the executor's rootfs
// directory. A node publishes, receives, serves and discards artifacts under that
// one root, so the tests model that rather than two roots the daemon never builds.
func migrationConfig(t *testing.T, store SnapshotStore) MigrationHandlerConfig {
	t.Helper()

	return MigrationHandlerConfig{
		Store:       store,
		ArtifactDir: t.TempDir(),
		RootfsDir:   "/var/lib/firecracker/rootfs",
	}
}

// migrationNode is one test node with its two roots apart: the snapshot store's
// directory, which pkg/snapshot owns, and the artifact root, which the broker
// publishes into. Keeping them separate is what proves the artifact's files are
// hard links and that the store's own housekeeping cannot reach them.
type migrationNode struct {
	cfg          MigrationHandlerConfig
	snapshotRoot string
	store        *fakeSnapshotStore
}

// newMigrationNode builds one node and seeds the snapshot its store would publish.
func newMigrationNode(t *testing.T) migrationNode {
	t.Helper()

	store := &fakeSnapshotStore{}
	node := migrationNode{cfg: migrationConfig(t, store), snapshotRoot: t.TempDir(), store: store}
	node.store.createInfo = seedSnapshot(t, node.snapshotRoot)
	return node
}

// export runs one successful export on the node.
func (n migrationNode) export(t *testing.T) MigrationExportDocument {
	t.Helper()

	rec := serveMigration(t, NewMigrationExportHandler(n.cfg), snapshotTask(), http.MethodPost,
		"/v1/tasks/task-1/migration/export?service=svc-1&node=node-1", nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var doc MigrationExportDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	return doc
}

// exportFixture runs one successful export and returns its document with the
// config it used, so a file or stage test starts from a real artifact.
func exportFixture(t *testing.T) (MigrationHandlerConfig, MigrationExportDocument) {
	t.Helper()

	node := newMigrationNode(t)
	return node.cfg, node.export(t)
}

// TestMigrationExportRejectsANonRunningTask pins the first check: a task whose VM
// is gone cannot be dumped, and the store must never be called for one.
func TestMigrationExportRejectsANonRunningTask(t *testing.T) {
	cases := []struct {
		name string
		task Task
	}{
		{
			name: "log only",
			task: Task{ID: "task-1", LogPath: "/var/run/swarmcracker/task-1.log"},
		},
		{
			name: "running flag but no socket",
			task: Task{ID: "task-1", Running: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeSnapshotStore{}
			cfg := migrationConfig(t, store)

			rec := serveMigration(t, NewMigrationExportHandler(cfg), tc.task, http.MethodPost,
				"/v1/tasks/task-1/migration/export?node=node-1", nil)

			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			doc := decodeMigrationError(t, rec)
			require.Equal(t, string(output.CodeNotFound), doc.Error.Code)
			require.Equal(t, "task_not_running", doc.Error.Details["reason"])
			require.Empty(t, store.gotTaskID, "a task that is not running must not be snapshotted")
		})
	}
}

// TestMigrationExportDescribesTheFilesItPublished pins the export contract end to
// end: the snapshot is taken on the task's own socket, the manifest's entries match
// the bytes behind the artifact directory, and the directory is named by the
// artifact, not by the snapshot.
func TestMigrationExportDescribesTheFilesItPublished(t *testing.T) {
	node := newMigrationNode(t)
	store := node.store

	rec := serveMigration(t, NewMigrationExportHandler(node.cfg), snapshotTask(), http.MethodPost,
		"/v1/tasks/task-1/migration/export?service=svc-1&node=node-1", nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, publicMediaType, rec.Header().Get("Content-Type"))

	var doc MigrationExportDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	artifact := doc.Artifact
	require.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
	require.Equal(t, migrationExportKind, doc.Kind)
	require.NoError(t, artifact.Validate(), "an exported manifest must be complete")
	require.Equal(t, "task-1", artifact.SourceTaskID)
	require.Equal(t, store.createInfo.ID, artifact.SnapshotID,
		"the manifest names the dump it was made from, so discard deletes exactly that one")
	require.Equal(t, "node-1", artifact.SourceNodeID)
	require.Equal(t, "svc-1", artifact.ServiceID)
	require.Equal(t, "/var/run/firecracker/task-1.vsock", artifact.VsockPath)
	require.Equal(t, "/var/lib/firecracker/rootfs/tasks/task-1.ext4", artifact.DiskPath)
	require.Equal(t, filepath.Join(node.cfg.ArtifactDir, artifact.ID), doc.Dir)
	require.True(t, doc.Linked, "the default install links the files rather than copying them")

	// The snapshot is taken on the resolved task's own socket, and the disk it
	// copies is the executor's per-task disk: the manifest's diskPath and the
	// files that travel must describe the same device.
	require.Equal(t, "task-1", store.gotTaskID)
	require.Equal(t, "/var/run/firecracker/task-1.sock", store.gotSocket)
	require.Equal(t, "/var/lib/firecracker/rootfs/tasks/task-1.ext4", store.gotCreate.RootfsPath)
	require.Equal(t, "svc-1", store.gotCreate.ServiceID)
	require.Equal(t, "node-1", store.gotCreate.NodeID)

	// Every entry the manifest describes is servable from the directory it names,
	// with the size and digest it promises: the checksums are of the bytes, not of
	// the paths.
	for _, file := range []migration.File{artifact.StateFile, artifact.MemoryFile, artifact.DiskFile} {
		body, err := os.ReadFile(filepath.Join(doc.Dir, file.Name))
		require.NoError(t, err, file.Name)
		require.Equal(t, file.Size, int64(len(body)), file.Name)
		sum := sha256.Sum256(body)
		require.Equal(t, file.SHA256, hex.EncodeToString(sum[:]), file.Name)
	}

	// The artifact is a real directory of its own, not a link into the store: the
	// store's retention sweeps a directory it owns, and a link would dangle the
	// moment a snapshot was cleaned up. It is private: the files it holds are a
	// guest's memory image.
	info, statErr := os.Lstat(doc.Dir)
	require.NoError(t, statErr)
	require.True(t, info.IsDir(), "the artifact directory must be a directory")
	require.Zero(t, info.Mode()&os.ModeSymlink, "the artifact directory must not be a link")
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "an artifact directory is the daemon's alone")

	// The manifest is node-local metadata inside the artifact directory: it
	// describes the files, it is never one of the files that move, and it is 0600
	// because it names the guest's disk, socket and TAP device.
	manifestPath := filepath.Join(doc.Dir, migrationArtifactDocumentName)
	manifestInfo, err := os.Stat(manifestPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), manifestInfo.Mode().Perm(), "the manifest is not world-readable")
	manifest, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	var stored migration.Artifact
	require.NoError(t, json.Unmarshal(manifest, &stored))
	require.Equal(t, artifact, stored)
	_, isServable := artifactFile(artifact, migrationArtifactDocumentName)
	require.False(t, isServable, "the manifest is not one of the artifact's files")

	// One copy of the bytes: each published file is the snapshot's own inode.
	for _, name := range artifactFileNames(artifact) {
		published, err := os.Stat(filepath.Join(doc.Dir, name))
		require.NoError(t, err, name)
		source, err := os.Stat(filepath.Join(node.snapshotRoot, store.createInfo.ID, name))
		require.NoError(t, err, name)
		require.True(t, os.SameFile(published, source), "%s must be a hard link to the snapshot's file", name)
	}

	// The store still holds a complete record of its own.
	require.FileExists(t, filepath.Join(node.snapshotRoot, "snap-0123456789abcdef", "vm.state"))
}

// TestMigrationExportRefusesBeforeTouchingTheTask pins the configuration and query
// rejections: each one answers a typed error and none of them snapshots anything.
func TestMigrationExportRefusesBeforeTouchingTheTask(t *testing.T) {
	cases := []struct {
		name       string
		store      SnapshotStore
		cfg        func(*MigrationHandlerConfig)
		method     string
		target     string
		body       []byte
		wantStatus int
		wantCode   output.Code
		wantDetail string
		// wantText must appear in the error message: a refusal that does not say
		// what the valid form is leaves the caller guessing.
		wantText string
		// reachesStore marks the one case whose failure comes from the store:
		// every other refusal must happen before it is called.
		reachesStore bool
	}{
		{
			name:       "no store",
			store:      nil,
			method:     http.MethodPost,
			target:     "/v1/tasks/task-1/migration/export?node=node-1",
			wantStatus: http.StatusNotImplemented,
			wantCode:   output.CodeNotImplemented,
			wantDetail: "migrations_disabled",
		},
		{
			name:  "no artifact directory",
			store: &fakeSnapshotStore{},
			cfg: func(c *MigrationHandlerConfig) {
				c.ArtifactDir = ""
			},
			method:     http.MethodPost,
			target:     "/v1/tasks/task-1/migration/export?node=node-1",
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   output.CodeUnavailable,
			wantDetail: "artifact_dir_unconfigured",
		},
		{
			name:  "no rootfs directory",
			store: &fakeSnapshotStore{},
			cfg: func(c *MigrationHandlerConfig) {
				c.RootfsDir = ""
			},
			method:     http.MethodPost,
			target:     "/v1/tasks/task-1/migration/export?node=node-1",
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   output.CodeUnavailable,
			wantDetail: "rootfs_dir_unconfigured",
		},
		{
			name:       "missing node",
			store:      &fakeSnapshotStore{},
			method:     http.MethodPost,
			target:     "/v1/tasks/task-1/migration/export",
			wantStatus: http.StatusBadRequest,
			wantCode:   output.CodeInvalid,
		},
		{
			name:       "path-like service",
			store:      &fakeSnapshotStore{},
			method:     http.MethodPost,
			target:     "/v1/tasks/task-1/migration/export?service=../etc&node=node-1",
			wantStatus: http.StatusBadRequest,
			wantCode:   output.CodeInvalid,
		},
		{
			name:       "unknown parameter",
			store:      &fakeSnapshotStore{},
			method:     http.MethodPost,
			target:     "/v1/tasks/task-1/migration/export?node=node-1&force=1",
			wantStatus: http.StatusBadRequest,
			wantCode:   output.CodeInvalid,
			wantText:   "want service or node",
		},
		{
			name:       "repeated parameter",
			store:      &fakeSnapshotStore{},
			method:     http.MethodPost,
			target:     "/v1/tasks/task-1/migration/export?node=node-1&node=node-2",
			wantStatus: http.StatusBadRequest,
			wantCode:   output.CodeInvalid,
		},
		{
			name:       "unexpected body",
			store:      &fakeSnapshotStore{},
			method:     http.MethodPost,
			target:     "/v1/tasks/task-1/migration/export?node=node-1",
			body:       []byte(`{"taskId":"task-1"}`),
			wantStatus: http.StatusBadRequest,
			wantCode:   output.CodeInvalid,
			wantDetail: "unexpected_body",
		},
		{
			name:       "wrong method",
			store:      &fakeSnapshotStore{},
			method:     http.MethodGet,
			target:     "/v1/tasks/task-1/migration/export?node=node-1",
			wantStatus: http.StatusMethodNotAllowed,
			wantCode:   output.CodeInvalid,
			wantDetail: "method_not_allowed",
		},
		{
			name:         "store socket vanished",
			store:        &fakeSnapshotStore{createErr: snapshot.ErrSocketMissing},
			method:       http.MethodPost,
			target:       "/v1/tasks/task-1/migration/export?node=node-1",
			wantStatus:   http.StatusConflict,
			wantCode:     output.CodeConflict,
			wantDetail:   "vm_socket_missing",
			reachesStore: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := MigrationHandlerConfig{
				Store:       tc.store,
				ArtifactDir: t.TempDir(),
				RootfsDir:   "/var/lib/firecracker/rootfs",
			}
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			store, _ := cfg.Store.(*fakeSnapshotStore)

			rec := serveMigration(t, NewMigrationExportHandler(cfg), snapshotTask(),
				tc.method, tc.target, tc.body)

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			doc := decodeMigrationError(t, rec)
			require.Equal(t, string(tc.wantCode), doc.Error.Code)
			if tc.wantDetail != "" {
				require.Equal(t, tc.wantDetail, doc.Error.Details["reason"])
			}
			if tc.wantText != "" {
				require.Contains(t, doc.Error.Message, tc.wantText)
			}
			if store != nil && !tc.reachesStore {
				require.Empty(t, store.gotTaskID, "a refused export must not reach the store")
			}
		})
	}
}

// TestMigrationArtifactOutlivesItsSnapshot pins what a real artifact directory
// buys: the snapshot store's own housekeeping — a restart, a leftover sweep, a
// delete — cannot reach the artifact, and a hard link keeps the bytes alive after
// the snapshot directory is gone. A symlink would dangle at that first delete.
func TestMigrationArtifactOutlivesItsSnapshot(t *testing.T) {
	node := newMigrationNode(t)
	doc := node.export(t)
	artifact := doc.Artifact

	// A store restart over the same directory sweeps leftovers; the artifact is
	// not one, because it is a directory of its own outside the snapshot root.
	store, err := snapshot.NewManager(snapshot.SnapshotConfig{
		Enabled:      true,
		SnapshotDir:  node.snapshotRoot,
		MaxSnapshots: 3,
		MaxAge:       168 * time.Hour,
	})
	require.NoError(t, err)

	for _, file := range artifactFiles(artifact) {
		require.FileExists(t, filepath.Join(node.cfg.ArtifactDir, artifact.ID, file.Name), file.Name)
	}

	// Deleting the snapshot removes the store's record. The artifact's files are
	// the same inodes, so they survive, and the transfer still completes.
	require.NoError(t, store.DeleteSnapshot(context.Background(), node.store.createInfo.ID))
	require.NoDirExists(t, filepath.Join(node.snapshotRoot, node.store.createInfo.ID))

	for _, file := range artifactFiles(artifact) {
		body, readErr := os.ReadFile(filepath.Join(node.cfg.ArtifactDir, artifact.ID, file.Name))
		require.NoError(t, readErr, file.Name)
		require.Equal(t, file.Size, int64(len(body)), file.Name)
		sum := sha256.Sum256(body)
		require.Equal(t, file.SHA256, hex.EncodeToString(sum[:]), file.Name)
	}

	rec := serveMigration(t, NewMigrationFileHandler(node.cfg), snapshotTask(), http.MethodGet,
		"/v1/tasks/task-1/migration/file?artifactId="+artifact.ID+"&name="+artifact.StateFile.Name, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, artifact.StateFile.SHA256, rec.Header().Get(checksumHeader))
}

// TestMigrationExportCopiesWhenItCannotLink pins the fallback: a snapshot root on
// another filesystem cannot be linked into the artifact root, so the export copies
// the bytes and says so instead of publishing a directory it cannot fill.
func TestMigrationExportCopiesWhenItCannotLink(t *testing.T) {
	linkArtifactFile = func(source, target string) error {
		return &os.LinkError{Op: "link", Old: source, New: target, Err: errors.New("cross-device link")}
	}
	t.Cleanup(func() { linkArtifactFile = os.Link })

	node := newMigrationNode(t)
	doc := node.export(t)

	require.False(t, doc.Linked, "an export that had to copy must say so")
	require.NotEqual(t, doc.Artifact.DiskFile, migration.File{})

	for _, file := range artifactFiles(doc.Artifact) {
		published, err := os.Stat(filepath.Join(doc.Dir, file.Name))
		require.NoError(t, err, file.Name)
		require.True(t, published.Mode().IsRegular(), file.Name)
		source, err := os.Stat(filepath.Join(node.snapshotRoot, node.store.createInfo.ID, file.Name))
		require.NoError(t, err, file.Name)
		require.False(t, os.SameFile(published, source), "%s must be a copy, not a link", file.Name)

		body, err := os.ReadFile(filepath.Join(doc.Dir, file.Name))
		require.NoError(t, err, file.Name)
		sum := sha256.Sum256(body)
		require.Equal(t, file.SHA256, hex.EncodeToString(sum[:]), file.Name)
		require.Equal(t, file.Size, int64(len(body)), file.Name)
	}

	// A copied artifact serves exactly like a linked one.
	rec := serveMigration(t, NewMigrationFileHandler(node.cfg), snapshotTask(), http.MethodGet,
		"/v1/tasks/task-1/migration/file?artifactId="+doc.Artifact.ID+"&name="+doc.Artifact.MemoryFile.Name, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, doc.Artifact.MemoryFile.Size, int64(rec.Body.Len()))
}

// TestMigrationFileServesTheDeclaredBytes pins the download contract: the bytes are
// the file, and the headers are the manifest's promises, so a caller can verify
// what it received without trusting the stream.
func TestMigrationFileServesTheDeclaredBytes(t *testing.T) {
	cfg, doc := exportFixture(t)
	handler := NewMigrationFileHandler(cfg)

	for _, file := range []migration.File{
		doc.Artifact.StateFile, doc.Artifact.MemoryFile, doc.Artifact.DiskFile,
	} {
		t.Run(file.Name, func(t *testing.T) {
			rec := serveMigration(t, handler, snapshotTask(), http.MethodGet,
				"/v1/tasks/task-1/migration/file?artifactId="+doc.Artifact.ID+"&name="+file.Name, nil)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, octetStream, rec.Header().Get("Content-Type"))
			require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			require.Equal(t, file.SHA256, rec.Header().Get(checksumHeader))
			require.Equal(t, strconv.FormatInt(file.Size, 10), rec.Header().Get("Content-Length"))
			require.Equal(t, file.Size, int64(rec.Body.Len()), "the body must be the size the manifest promises")

			sum := sha256.Sum256(rec.Body.Bytes())
			require.Equal(t, file.SHA256, hex.EncodeToString(sum[:]))
		})
	}
}

// TestMigrationFileRefusesWhatIsNotAnArtifactFile pins the whitelist: the artifact
// directory holds node-local metadata, and none of it may leave the node, while a
// name that is a path is refused before any join. The handler is given no task at
// all: this operation is artifact-scoped, so what may leave is decided by the
// manifest, not by the route's task segment.
func TestMigrationFileRefusesWhatIsNotAnArtifactFile(t *testing.T) {
	cfg, doc := exportFixture(t)
	handler := NewMigrationFileHandler(cfg)

	cases := []struct {
		name       string
		target     string
		wantStatus int
		wantReason string
	}{
		{
			name:       "manifest",
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=" + migrationArtifactDocumentName,
			wantStatus: http.StatusBadRequest,
			wantReason: "unknown_artifact_file",
		},
		{
			name:       "snapshot metadata",
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=metadata.json",
			wantStatus: http.StatusBadRequest,
			wantReason: "unknown_artifact_file",
		},
		{
			name:       "traversal name",
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=..%2Fvm.state",
			wantStatus: http.StatusBadRequest,
			wantReason: "invalid_file_name",
		},
		{
			name:       "subdirectory name",
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=sub%2Fvm.state",
			wantStatus: http.StatusBadRequest,
			wantReason: "invalid_file_name",
		},
		{
			name:       "dot dot name",
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=..",
			wantStatus: http.StatusBadRequest,
			wantReason: "invalid_file_name",
		},
		{
			name:       "traversal artifact",
			target:     "/v1/tasks/task-1/migration/file?artifactId=..%2F..%2Fetc&name=vm.state",
			wantStatus: http.StatusBadRequest,
			wantReason: "invalid_artifact_id",
		},
		{
			name:       "unknown artifact",
			target:     "/v1/tasks/task-1/migration/file?artifactId=mig-0000000000000000&name=vm.state",
			wantStatus: http.StatusNotFound,
			wantReason: "artifact_not_available",
		},
		{
			name:       "wrong method",
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=vm.state",
			wantStatus: http.StatusMethodNotAllowed,
			wantReason: "method_not_allowed",
		},
		{
			name:       "unknown parameter",
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=vm.state&size=1",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method := http.MethodGet
			if tc.wantStatus == http.StatusMethodNotAllowed {
				method = http.MethodPut
			}

			rec := serveMigration(t, handler, Task{}, method, tc.target, nil)

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			if tc.wantReason != "" {
				require.Equal(t, tc.wantReason, decodeMigrationError(t, rec).Error.Details["reason"])
			}
			if tc.wantStatus == http.StatusMethodNotAllowed {
				require.Equal(t, http.MethodGet, rec.Header().Get("Allow"))
			}
		})
	}
}

// TestMigrationFileRefusesASymlinkedEntry pins the open rule: the manifest names a
// file inside the artifact directory, so an entry that is a symbolic link is refused
// rather than followed. The link here points at a copy with the same size and digest,
// so only the refusal to follow it can stop the answer.
func TestMigrationFileRefusesASymlinkedEntry(t *testing.T) {
	cfg, doc := exportFixture(t)
	entry := doc.Artifact.MemoryFile

	// A byte-identical file outside the artifact: size and digest would both pass,
	// which is what makes the link the only thing left to refuse.
	elsewhere := filepath.Join(t.TempDir(), "vm.mem")
	body, err := os.ReadFile(filepath.Join(cfg.ArtifactDir, doc.Artifact.ID, entry.Name))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(elsewhere, body, 0o600))

	path := filepath.Join(cfg.ArtifactDir, doc.Artifact.ID, entry.Name)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(elsewhere, path))

	rec := serveMigration(t, NewMigrationFileHandler(cfg), Task{}, http.MethodGet,
		"/v1/tasks/task-1/migration/file?artifactId="+doc.Artifact.ID+"&name="+entry.Name, nil)

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "artifact_changed", decodeMigrationError(t, rec).Error.Details["reason"])
	require.NotEqual(t, entry.Size, int64(rec.Body.Len()), "a linked entry must not be served")
}

// TestMigrationFileRefusesAFileThatChangedAfterTheExport pins the size check: the
// digest the caller was promised describes the file at export time, so a file that
// no longer matches its description is a conflict rather than a stream the caller
// cannot verify.
func TestMigrationFileRefusesAFileThatChangedAfterTheExport(t *testing.T) {
	cfg, doc := exportFixture(t)

	path := filepath.Join(cfg.ArtifactDir, doc.Artifact.ID, doc.Artifact.MemoryFile.Name)
	require.NoError(t, os.Truncate(path, doc.Artifact.MemoryFile.Size-1))

	rec := serveMigration(t, NewMigrationFileHandler(cfg), snapshotTask(), http.MethodGet,
		"/v1/tasks/task-1/migration/file?artifactId="+doc.Artifact.ID+"&name="+doc.Artifact.MemoryFile.Name, nil)

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	doc2 := decodeMigrationError(t, rec)
	require.Equal(t, "artifact_changed", doc2.Error.Details["reason"])
}

// TestMigrationFileServesOnlyARealArtifactDirectory pins the resolution rule: an
// artifact is a real directory this node published. A symbolic link or a plain file
// under the artifact root answers the same 404 a missing artifact does, so nothing
// outside the root can answer for one.
func TestMigrationFileServesOnlyARealArtifactDirectory(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, cfg MigrationHandlerConfig)
	}{
		{
			name: "symbolic link",
			setup: func(t *testing.T, cfg MigrationHandlerConfig) {
				t.Helper()
				elsewhere := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(elsewhere, "vm.state"), []byte("x"), 0o600))
				require.NoError(t, os.Symlink(elsewhere, filepath.Join(cfg.ArtifactDir, "mig-0123456789abcdef")))
			},
		},
		{
			name: "regular file",
			setup: func(t *testing.T, cfg MigrationHandlerConfig) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(cfg.ArtifactDir, "mig-0123456789abcdef"),
					[]byte("not an artifact"), 0o600))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := MigrationHandlerConfig{ArtifactDir: t.TempDir()}
			tc.setup(t, cfg)

			rec := serveMigration(t, NewMigrationFileHandler(cfg), snapshotTask(), http.MethodGet,
				"/v1/tasks/task-1/migration/file?artifactId=mig-0123456789abcdef&name=vm.state", nil)

			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			require.Equal(t, "artifact_not_available", decodeMigrationError(t, rec).Error.Details["reason"])
		})
	}
}

// TestMigrationExportRefusesASnapshotItCannotDescribe pins the answer for a
// snapshot that is not a complete artifact: the failure is the node's, and the
// pkg/migration rejection (which names absolute paths) never reaches the wire. The
// dump it could not describe is discarded rather than left for retention.
func TestMigrationExportRefusesASnapshotItCannotDescribe(t *testing.T) {
	store := &fakeSnapshotStore{}
	cfg := migrationConfig(t, store)

	info := seedSnapshot(t, t.TempDir())
	info.DiskPath = "" // a snapshot with no disk copy cannot move a filesystem
	store.createInfo = info

	rec := serveMigration(t, NewMigrationExportHandler(cfg), snapshotTask(), http.MethodPost,
		"/v1/tasks/task-1/migration/export?node=node-1", nil)

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), cfg.ArtifactDir,
		"a pkg/migration rejection names paths; only the classification may leave the node")
	require.Equal(t, []string{info.ID}, store.deleted,
		"an export that cannot describe its dump must discard it")
}

// TestMigrationExportReportsAPublishFailure pins the last step: a node whose
// artifact root cannot hold the directory cannot publish the artifact, even though
// the snapshot itself was taken — and the dump it cannot publish is discarded.
func TestMigrationExportReportsAPublishFailure(t *testing.T) {
	store := &fakeSnapshotStore{}
	cfg := migrationConfig(t, store)
	store.createInfo = seedSnapshot(t, t.TempDir())

	// A regular file where the root should be: MkdirAll cannot create below it.
	blocked := filepath.Join(t.TempDir(), "blocked")
	require.NoError(t, os.WriteFile(blocked, []byte("x"), 0o600))
	cfg.ArtifactDir = filepath.Join(blocked, "root")

	rec := serveMigration(t, NewMigrationExportHandler(cfg), snapshotTask(), http.MethodPost,
		"/v1/tasks/task-1/migration/export?node=node-1", nil)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, string(output.CodeUnavailable), decodeMigrationError(t, rec).Error.Code)
	require.Equal(t, []string{store.createInfo.ID}, store.deleted,
		"an export that cannot publish must discard the dump it made")
	require.NoDirExists(t, filepath.Join(cfg.ArtifactDir, "mig-0123456789abcdef"))
}

// TestMigrationFileRefusesAManifestItCannotUse pins the two manifest failures: a
// manifest that does not validate is the node's problem (500), and a path that is
// not a readable file is unavailability (503). Neither is the caller's mistake.
func TestMigrationFileRefusesAManifestItCannotUse(t *testing.T) {
	t.Run("invalid manifest", func(t *testing.T) {
		cfg, doc := exportFixture(t)
		manifest := filepath.Join(cfg.ArtifactDir, doc.Artifact.ID, migrationArtifactDocumentName)
		require.NoError(t, os.WriteFile(manifest, []byte("{ not a manifest"), 0o600))

		rec := serveMigration(t, NewMigrationFileHandler(cfg), snapshotTask(), http.MethodGet,
			"/v1/tasks/task-1/migration/file?artifactId="+doc.Artifact.ID+"&name=vm.state", nil)

		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		require.Equal(t, "artifact_invalid", decodeMigrationError(t, rec).Error.Details["reason"])
	})

	t.Run("manifest is not a file", func(t *testing.T) {
		cfg, doc := exportFixture(t)
		manifest := filepath.Join(cfg.ArtifactDir, doc.Artifact.ID, migrationArtifactDocumentName)
		require.NoError(t, os.Remove(manifest))
		require.NoError(t, os.Mkdir(manifest, 0o755))

		rec := serveMigration(t, NewMigrationFileHandler(cfg), snapshotTask(), http.MethodGet,
			"/v1/tasks/task-1/migration/file?artifactId="+doc.Artifact.ID+"&name=vm.state", nil)

		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		require.Equal(t, "artifact_unreadable", decodeMigrationError(t, rec).Error.Details["reason"])
	})
}

// TestMigrationFileNeedsAnArtifactRoot pins the one configuration the file
// operation cannot work without.
func TestMigrationFileNeedsAnArtifactRoot(t *testing.T) {
	rec := serveMigration(t, NewMigrationFileHandler(MigrationHandlerConfig{}), snapshotTask(),
		http.MethodGet, "/v1/tasks/task-1/migration/file?artifactId=mig-0123456789abcdef&name=vm.state", nil)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, "artifact_dir_unconfigured", decodeMigrationError(t, rec).Error.Details["reason"])
}

// TestMigrationStagePublishesOnlyVerifiedBytes pins the upload contract: a matching
// body is published atomically, restaging it succeeds, and a body that does not
// match the declared size or digest leaves neither a file nor a partial file.
func TestMigrationStagePublishesOnlyVerifiedBytes(t *testing.T) {
	cfg := MigrationHandlerConfig{ArtifactDir: t.TempDir()}
	handler := NewMigrationStageHandler(cfg)

	const artifactID = "mig-0123456789abcdef"
	const name = "vm.state"
	body := []byte("VMSTATE: the bytes that arrive\n")
	size := int64(len(body))
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])

	target := func(size int64, sha string) string {
		return "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=" + name +
			"&size=" + strconv.FormatInt(size, 10) + "&sha256=" + sha
	}
	stagedPath := filepath.Join(cfg.ArtifactDir, artifactID, name)

	t.Run("accepts a matching file", func(t *testing.T) {
		rec := serveMigration(t, handler, Task{}, http.MethodPut, target(size, digest), body)

		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		require.Equal(t, digest, rec.Header().Get(checksumHeader))
		staged, err := os.ReadFile(stagedPath)
		require.NoError(t, err)
		require.Equal(t, body, staged)
		requireNoPartials(t, filepath.Dir(stagedPath))
		requireNoPartials(t, cfg.ArtifactDir)
	})

	t.Run("is idempotent", func(t *testing.T) {
		for attempt := 0; attempt < 2; attempt++ {
			rec := serveMigration(t, handler, Task{}, http.MethodPut, target(size, digest), body)
			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		}

		staged, err := os.ReadFile(stagedPath)
		require.NoError(t, err)
		require.Equal(t, body, staged)
		requireNoPartials(t, filepath.Dir(stagedPath))
	})

	t.Run("refuses a checksum mismatch without replacing the file", func(t *testing.T) {
		// The same length and a different body: only the digest can tell them
		// apart, so this is the case the check exists for.
		bad := bytes.ToUpper(body)

		rec := serveMigration(t, handler, Task{}, http.MethodPut,
			target(int64(len(bad)), digest), bad)

		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Equal(t, "checksum_mismatch", decodeMigrationError(t, rec).Error.Details["reason"])

		staged, err := os.ReadFile(stagedPath)
		require.NoError(t, err)
		require.Equal(t, body, staged, "a refused stage must not replace the file it could not verify")
		requireNoPartials(t, filepath.Dir(stagedPath))
	})

	t.Run("refuses a short body", func(t *testing.T) {
		rec := serveMigration(t, handler, Task{}, http.MethodPut,
			target(size+10, digest), body)

		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Equal(t, "size_mismatch", decodeMigrationError(t, rec).Error.Details["reason"])
		requireNoPartials(t, filepath.Dir(stagedPath))
	})

	t.Run("refuses a declared length above the size", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, target(size, digest), bytes.NewReader(append(body, 'x')))
		rec := httptest.NewRecorder()
		handler(context.Background(), Task{}, rec, req)

		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
		require.Equal(t, "payload_too_large", decodeMigrationError(t, rec).Error.Details["reason"])
		requireNoPartials(t, filepath.Dir(stagedPath))
	})

	t.Run("refuses a chunked body above the size", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, target(size, digest), bytes.NewReader(append(body, 'x')))
		// A chunked upload declares no length, so the cap has to hold while the
		// body is read.
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		handler(context.Background(), Task{}, rec, req)

		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
		require.Equal(t, "payload_too_large", decodeMigrationError(t, rec).Error.Details["reason"])
		requireNoPartials(t, filepath.Dir(stagedPath))
	})
}

// TestMigrationStageRefusesBadRequests pins the query grammar and the traversal
// rules: a bad request never creates a directory, let alone a file.
func TestMigrationStageRefusesBadRequests(t *testing.T) {
	const artifactID = "mig-0123456789abcdef"
	digest := strings.Repeat("a", sha256HexChars)

	cases := []struct {
		name       string
		noStaging  bool
		target     string
		wantStatus int
		reason     string
	}{
		{
			name:       "no staging directory",
			noStaging:  true,
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=vm.state&size=1&sha256=" + digest,
			wantStatus: http.StatusServiceUnavailable,
			reason:     "staging_dir_unconfigured",
		},
		{
			name:       "traversal name",
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=..%2Fpasswd&size=1&sha256=" + digest,
			wantStatus: http.StatusBadRequest,
			reason:     "invalid_file_name",
		},
		{
			name:       "dot dot name",
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=..&size=1&sha256=" + digest,
			wantStatus: http.StatusBadRequest,
			reason:     "invalid_file_name",
		},
		{
			name:       "subdirectory name",
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=sub%2Fvm.state&size=1&sha256=" + digest,
			wantStatus: http.StatusBadRequest,
			reason:     "invalid_file_name",
		},
		{
			name:       "manifest name",
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=" + migrationArtifactDocumentName + "&size=1&sha256=" + digest,
			wantStatus: http.StatusBadRequest,
			reason:     "reserved_file_name",
		},
		{
			name:       "traversal artifact",
			target:     "/v1/tasks/task-2/migration/stage?artifactId=..%2F..%2Fetc&name=vm.state&size=1&sha256=" + digest,
			wantStatus: http.StatusBadRequest,
			reason:     "invalid_artifact_id",
		},
		{
			name:       "zero size",
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=vm.state&size=0&sha256=" + digest,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "size above the ceiling",
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=vm.state&size=" + strconv.FormatInt(migration.MaxFileSize+1, 10) + "&sha256=" + digest,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "non-numeric size",
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=vm.state&size=many&sha256=" + digest,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "uppercase digest",
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=vm.state&size=1&sha256=" + strings.ToUpper(digest),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "short digest",
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=vm.state&size=1&sha256=abcd",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown parameter",
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=vm.state&size=1&sha256=" + digest + "&mode=0644",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := MigrationHandlerConfig{}
			if !tc.noStaging {
				cfg.ArtifactDir = t.TempDir()
			}

			rec := serveMigration(t, NewMigrationStageHandler(cfg), Task{},
				http.MethodPut, tc.target, []byte("x"))

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			if tc.reason != "" {
				require.Equal(t, tc.reason, decodeMigrationError(t, rec).Error.Details["reason"])
			}
			if cfg.ArtifactDir != "" {
				entries, err := os.ReadDir(cfg.ArtifactDir)
				require.NoError(t, err)
				require.Empty(t, entries, "a refused stage must not create anything")
			}
		})
	}
}

// TestMigrationStageValidatesBeforeCreatingTheDirectory pins the order M7 asked
// for: the parameters and the body are checked before the artifact directory
// exists, so a refused stage leaves no empty <artifactID>/ behind for a restore to
// mistake for a started transfer. The declaration is valid and the body is not.
func TestMigrationStageValidatesBeforeCreatingTheDirectory(t *testing.T) {
	root := t.TempDir()

	body := []byte("VMSTATE: not the bytes the request declares\n")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])

	rec := serveMigration(t, NewMigrationStageHandler(MigrationHandlerConfig{ArtifactDir: root}), Task{},
		http.MethodPut,
		"/v1/tasks/task-2/migration/stage?artifactId=mig-0123456789abcdef&name=vm.state"+
			"&size="+strconv.Itoa(len(body))+"&sha256="+digest,
		bytes.ToUpper(body))

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "checksum_mismatch", decodeMigrationError(t, rec).Error.Details["reason"])
	require.NoDirExists(t, filepath.Join(root, "mig-0123456789abcdef"),
		"a refused stage must not leave an artifact directory behind")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Empty(t, entries, "a refused stage must not leave anything behind")
}

// TestFileNameRuleIsTheManifestRule pins I4's single rule from the transport side:
// the broker holds no copy of it. Every name pkg/migration.ValidFileName rejects is a
// 400 from both routes that take a name, and every name it accepts gets past the name
// check (the stage accepts it, the file route answers about the artifact instead).
// The table is the one in pkg/migration; this test is what would fail if a second
// copy of the rule ever grew back here.
func TestFileNameRuleIsTheManifestRule(t *testing.T) {
	cfg := MigrationHandlerConfig{ArtifactDir: t.TempDir()}
	stageHandler := NewMigrationStageHandler(cfg)
	fileHandler := NewMigrationFileHandler(cfg)

	body := []byte("x")
	digest := sha256HexBytes(body)

	names := []string{
		"vm.state",
		"rootfs.ext4",
		"a name with a space",
		strings.Repeat("n", 255),
		"",
		".",
		"..",
		"snap-1/vm.state",
		`snap-1\vm.state`,
		"vm.state\n",
		"vm.state\r",
		"vm\x00.state",
		"vm\tstate",
		"vm\x7f.state",
		strings.Repeat("n", 256),
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			ruleErr := migration.ValidFileName(name)
			encoded := url.QueryEscape(name)

			rec := serveMigration(t, stageHandler, Task{}, http.MethodPut,
				"/v1/tasks/task-2/migration/stage?artifactId=mig-0123456789abcdef&name="+encoded+
					"&size=1&sha256="+digest, body)

			if ruleErr != nil {
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				require.Equal(t, "invalid_file_name", decodeMigrationError(t, rec).Error.Details["reason"])
			} else {
				require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
			}

			rec = serveMigration(t, fileHandler, Task{}, http.MethodGet,
				"/v1/tasks/task-1/migration/file?artifactId=mig-0123456789abcdef&name="+encoded, nil)
			if ruleErr != nil {
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				require.Equal(t, "invalid_file_name", decodeMigrationError(t, rec).Error.Details["reason"])
			} else {
				require.Equal(t, http.StatusNotFound, rec.Code,
					"a name the rule accepts must get past the name check: %s", rec.Body.String())
			}
		})
	}
}

// TestMigrationStageSpansChunks pins the digest over a body larger than the read
// buffer: io.Copy hands the hash one write per chunk, so a body of a few hundred
// kilobytes is the smallest case that would catch a digest computed per chunk
// instead of over the whole file.
func TestMigrationStageSpansChunks(t *testing.T) {
	root := t.TempDir()
	handler := NewMigrationStageHandler(MigrationHandlerConfig{ArtifactDir: root})

	// Well past io.Copy's 32 KiB buffer, and not a multiple of it.
	body := bytes.Repeat([]byte("chunked migration body\n"), 20000)
	require.Greater(t, len(body), 32<<10)
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])

	rec := serveMigration(t, handler, Task{}, http.MethodPut,
		"/v1/tasks/task-2/migration/stage?artifactId=mig-0123456789abcdef&name=vm.mem"+
			"&size="+strconv.Itoa(len(body))+"&sha256="+digest,
		body)

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	staged, err := os.ReadFile(filepath.Join(root, "mig-0123456789abcdef", "vm.mem"))
	require.NoError(t, err)
	require.Equal(t, len(body), len(staged))
	require.Equal(t, digest, sha256HexBytes(staged), "the digest must span every chunk of the body")
	requireNoPartials(t, root)
}

// TestMigrationStageIsConcurrencySafe pins what two writers and a reader may do at
// once: two stages of the same file publish the same verified bytes and neither
// leaves a partial, and a file of an exported artifact keeps serving while another
// file is being staged into that same artifact directory. Each stage writes its own
// temporary, so the only name either can publish is the one it verified.
func TestMigrationStageIsConcurrencySafe(t *testing.T) {
	cfg, doc := exportFixture(t)
	stageHandler := NewMigrationStageHandler(cfg)
	fileHandler := NewMigrationFileHandler(cfg)
	artifactID := doc.Artifact.ID

	stageTarget := func(name string, size int, sha string) string {
		return "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=" + name +
			"&size=" + strconv.Itoa(size) + "&sha256=" + sha
	}

	// Well past io.Copy's read buffer, so the two writers overlap in time.
	body := bytes.Repeat([]byte("concurrent stage\n"), 40000)
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])

	var wg sync.WaitGroup
	stageResults := make([]*httptest.ResponseRecorder, 2)
	readResults := make([]*httptest.ResponseRecorder, 0, 4)
	var readMu sync.Mutex

	for i := range stageResults {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			stageResults[i] = serveMigration(t, stageHandler, Task{}, http.MethodPut,
				stageTarget("chunk.a", len(body), digest), body)
		}(i)
	}

	// A reader of the file the export published, while the stages are writing into
	// the same directory: a read must never depend on a stage finishing.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 4 {
			rec := serveMigration(t, fileHandler, Task{}, http.MethodGet,
				"/v1/tasks/task-1/migration/file?artifactId="+artifactID+"&name="+doc.Artifact.StateFile.Name, nil)
			readMu.Lock()
			readResults = append(readResults, rec)
			readMu.Unlock()
		}
	}()

	wg.Wait()

	for i, rec := range stageResults {
		require.Equal(t, http.StatusNoContent, rec.Code, "stage %d: %s", i, rec.Body.String())
	}
	require.NotEmpty(t, readResults)
	for _, rec := range readResults {
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, doc.Artifact.StateFile.SHA256, sha256HexBytes(rec.Body.Bytes()),
			"a concurrent stage must not disturb a file the manifest describes")
	}

	staged, err := os.ReadFile(filepath.Join(cfg.ArtifactDir, artifactID, "chunk.a"))
	require.NoError(t, err)
	require.Equal(t, digest, sha256HexBytes(staged))
	requireNoPartials(t, cfg.ArtifactDir)
	requireNoPartials(t, filepath.Join(cfg.ArtifactDir, artifactID))
}

// TestMigrationStageRefusesAnotherMethod pins the method set the server enforces
// for the operation.
func TestMigrationStageRefusesAnotherMethod(t *testing.T) {
	rec := serveMigration(t, NewMigrationStageHandler(MigrationHandlerConfig{ArtifactDir: t.TempDir()}),
		Task{}, http.MethodPost,
		"/v1/tasks/task-2/migration/stage?artifactId=mig-0123456789abcdef&name=vm.state&size=1&sha256="+strings.Repeat("a", sha256HexChars), nil)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	require.Equal(t, http.MethodPut, rec.Header().Get("Allow"))
}

// TestMigrationDiscardRemovesTheArtifactAndItsSnapshot pins the delete path a
// rollback and a completed migration both need: the artifact directory goes, and the
// one snapshot the manifest names goes with it — not another dump of the same task,
// which is what the checksum-matching discovery used to risk.
func TestMigrationDiscardRemovesTheArtifactAndItsSnapshot(t *testing.T) {
	node := newMigrationNode(t)
	doc := node.export(t)

	// A second dump of the same task exists in the store: it is not this
	// artifact's, and discarding this artifact must leave it alone.
	other := *node.store.createInfo
	other.ID = "snap-fedcba9876543210"
	node.store.list = []*snapshot.SnapshotInfo{node.store.createInfo, &other}

	handler := NewMigrationDiscardHandler(node.cfg)
	target := "/v1/tasks/task-1/migration/discard?artifactId=" + doc.Artifact.ID

	rec := serveMigration(t, handler, Task{}, http.MethodDelete, target, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.NoDirExists(t, doc.Dir, "the artifact directory must be removed")
	require.Equal(t, []string{node.store.createInfo.ID}, node.store.deleted,
		"exactly the snapshot the manifest names must be removed")

	// Idempotent: the artifact is already gone, so this is a success.
	rec = serveMigration(t, handler, Task{}, http.MethodDelete, target, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Len(t, node.store.deleted, 1, "a second discard must not delete a snapshot twice")

	// An artifact this node never held is a success too: the caller's cleanup
	// already happened.
	rec = serveMigration(t, handler, Task{}, http.MethodDelete,
		"/v1/tasks/task-1/migration/discard?artifactId=mig-0000000000000000", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

// TestMigrationDiscardRemovesAStagedArtifact pins the target side: an artifact that
// arrived through stage is removed without a snapshot store, because there is no
// snapshot behind it, and without a manifest, because the manifest travels as a
// document.
func TestMigrationDiscardRemovesAStagedArtifact(t *testing.T) {
	cfg := MigrationHandlerConfig{ArtifactDir: t.TempDir()}

	const artifactID = "mig-0123456789abcdef"
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.ArtifactDir, artifactID), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.ArtifactDir, artifactID, "vm.state"),
		[]byte("VMSTATE: the bytes that arrived\n"), 0o600))

	rec := serveMigration(t, NewMigrationDiscardHandler(cfg), Task{}, http.MethodDelete,
		"/v1/tasks/task-2/migration/discard?artifactId="+artifactID, nil)

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.NoDirExists(t, filepath.Join(cfg.ArtifactDir, artifactID))
}

// TestMigrationDiscardTreatsAMissingSnapshotAsDeleted pins idempotence where it
// actually was broken: a snapshot the store no longer holds (retention ran first, or
// the previous discard removed it and then failed to remove the directory) means that
// half of the delete already happened, so the directory still goes and the answer is
// success.
func TestMigrationDiscardTreatsAMissingSnapshotAsDeleted(t *testing.T) {
	node := newMigrationNode(t)
	doc := node.export(t)
	node.store.deleteErr = snapshot.ErrNotFound

	rec := serveMigration(t, NewMigrationDiscardHandler(node.cfg), Task{}, http.MethodDelete,
		"/v1/tasks/task-1/migration/discard?artifactId="+doc.Artifact.ID, nil)

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.NoDirExists(t, doc.Dir, "an already-deleted snapshot must not keep the artifact on disk")
}

// TestMigrationDiscardReportsAFailedSnapshotDelete pins the order: the snapshot is
// removed before the artifact directory, so a failed delete leaves the artifact in
// place for a retry instead of leaking a snapshot nobody can name any more.
func TestMigrationDiscardReportsAFailedSnapshotDelete(t *testing.T) {
	node := newMigrationNode(t)
	doc := node.export(t)
	node.store.deleteErr = errors.New("device is busy")

	handler := NewMigrationDiscardHandler(node.cfg)
	target := "/v1/tasks/task-1/migration/discard?artifactId=" + doc.Artifact.ID

	rec := serveMigration(t, handler, Task{}, http.MethodDelete, target, nil)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, "snapshot_store_error", decodeMigrationError(t, rec).Error.Details["reason"])
	require.DirExists(t, doc.Dir, "a failed snapshot delete must leave the artifact for a retry")

	node.store.deleteErr = nil
	rec = serveMigration(t, handler, Task{}, http.MethodDelete, target, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.NoDirExists(t, doc.Dir)
}

// TestMigrationDiscardRefusesBadRequests pins the query grammar and the method set:
// a refused discard removes nothing.
func TestMigrationDiscardRefusesBadRequests(t *testing.T) {
	withRoots := func(cfg *MigrationHandlerConfig) {
		cfg.ArtifactDir = t.TempDir()
	}

	cases := []struct {
		name       string
		cfg        func(*MigrationHandlerConfig)
		method     string
		target     string
		body       []byte
		wantStatus int
		wantReason string
	}{
		{
			name:       "no directory",
			cfg:        func(cfg *MigrationHandlerConfig) { cfg.ArtifactDir = "" },
			method:     http.MethodDelete,
			target:     "/v1/tasks/task-1/migration/discard?artifactId=mig-0123456789abcdef",
			wantStatus: http.StatusServiceUnavailable,
			wantReason: "artifact_dir_unconfigured",
		},
		{
			name:       "missing artifact ID",
			cfg:        withRoots,
			method:     http.MethodDelete,
			target:     "/v1/tasks/task-1/migration/discard",
			wantStatus: http.StatusBadRequest,
			wantReason: "invalid_artifact_id",
		},
		{
			name:       "traversal artifact ID",
			cfg:        withRoots,
			method:     http.MethodDelete,
			target:     "/v1/tasks/task-1/migration/discard?artifactId=..%2F..%2Fetc",
			wantStatus: http.StatusBadRequest,
			wantReason: "invalid_artifact_id",
		},
		{
			name:       "repeated artifact ID",
			cfg:        withRoots,
			method:     http.MethodDelete,
			target:     "/v1/tasks/task-1/migration/discard?artifactId=mig-0123456789abcdef&artifactId=mig-0123456789abcdee",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown parameter",
			cfg:        withRoots,
			method:     http.MethodDelete,
			target:     "/v1/tasks/task-1/migration/discard?artifactId=mig-0123456789abcdef&force=1",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unexpected body",
			cfg:        withRoots,
			method:     http.MethodDelete,
			target:     "/v1/tasks/task-1/migration/discard?artifactId=mig-0123456789abcdef",
			body:       []byte(`{"artifactId":"mig-0123456789abcdef"}`),
			wantStatus: http.StatusBadRequest,
			wantReason: "unexpected_body",
		},
		{
			name:       "wrong method",
			cfg:        withRoots,
			method:     http.MethodGet,
			target:     "/v1/tasks/task-1/migration/discard?artifactId=mig-0123456789abcdef",
			wantStatus: http.StatusMethodNotAllowed,
			wantReason: "method_not_allowed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := MigrationHandlerConfig{Store: &fakeSnapshotStore{}}
			tc.cfg(&cfg)
			store, _ := cfg.Store.(*fakeSnapshotStore)

			rec := serveMigration(t, NewMigrationDiscardHandler(cfg), snapshotTask(), tc.method, tc.target, tc.body)

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			if tc.wantReason != "" {
				require.Equal(t, tc.wantReason, decodeMigrationError(t, rec).Error.Details["reason"])
			}
			require.Empty(t, store.deleted, "a refused discard must not delete a snapshot")
			if tc.wantStatus == http.StatusMethodNotAllowed {
				require.Equal(t, http.MethodDelete, rec.Header().Get("Allow"))
			}
		})
	}
}

// TestMigrationStageRefusesADirectoryThatIsNotOne pins the staging side of the
// resolution rule: the artifact directory stage writes into must be a real
// directory of its own. A link there would move a staged name outside the staging
// root, and a plain file cannot hold one.
func TestMigrationStageRefusesADirectoryThatIsNotOne(t *testing.T) {
	const artifactID = "mig-0123456789abcdef"
	// The body is valid on purpose: the directory is checked after the bytes are
	// verified, so this is what reaches the check.
	body := []byte("x")
	target := "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID +
		"&name=vm.state&size=1&sha256=" + sha256HexBytes(body)

	cases := []struct {
		name  string
		setup func(t *testing.T, stagingDir string) string
	}{
		{
			name: "symbolic link",
			setup: func(t *testing.T, stagingDir string) string {
				t.Helper()
				elsewhere := t.TempDir()
				require.NoError(t, os.Symlink(elsewhere, filepath.Join(stagingDir, artifactID)))
				return elsewhere
			},
		},
		{
			name: "regular file",
			setup: func(t *testing.T, stagingDir string) string {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(stagingDir, artifactID), []byte("x"), 0o600))
				return ""
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stagingDir := t.TempDir()
			elsewhere := tc.setup(t, stagingDir)

			rec := serveMigration(t, NewMigrationStageHandler(MigrationHandlerConfig{ArtifactDir: stagingDir}),
				Task{}, http.MethodPut, target, []byte("x"))

			require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			require.Equal(t, "artifact_dir_unsafe", decodeMigrationError(t, rec).Error.Details["reason"])
			if elsewhere != "" {
				entries, err := os.ReadDir(elsewhere)
				require.NoError(t, err)
				require.Empty(t, entries, "a refused stage must not write through the link")
			}
		})
	}
}

// TestServerRoutesMigrationOperations pins the wire shape the manager will call:
// a namespaced operation is two path segments, the registry enforces its method,
// and anything else under the namespace stays a malformed path.
func TestServerRoutesMigrationOperations(t *testing.T) {
	cfg, doc := exportFixture(t)
	srv := newRouteServer(t, &fakeTaskSource{task: snapshotTask(), found: true})
	require.NoError(t, srv.Handle(MigrationExportOperation, NewMigrationExportHandler(MigrationHandlerConfig{})))
	require.NoError(t, srv.Handle(MigrationFileOperation, NewMigrationFileHandler(cfg)))
	require.NoError(t, srv.Handle(MigrationStageOperation, NewMigrationStageHandler(MigrationHandlerConfig{ArtifactDir: t.TempDir()})))
	require.NoError(t, srv.Handle(MigrationDiscardOperation, NewMigrationDiscardHandler(
		MigrationHandlerConfig{ArtifactDir: t.TempDir()})))

	// The file operation is reached through the two-segment path, and answers from
	// the artifact the export published.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v1/tasks/task-1/migration/file?artifactId="+doc.Artifact.ID+"&name=vm.state", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, doc.Artifact.StateFile.SHA256, rec.Header().Get(checksumHeader))

	// The export operation is reached too; with no store it answers its typed 501
	// rather than the server's unregistered-operation answer.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/v1/tasks/task-1/migration/export?node=node-1", nil))
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
	require.Equal(t, "migrations", decodeMigrationError(t, rec).Error.Details["capability"])

	// The discard operation is reached, and answers its own contract: an artifact
	// this node does not hold is already discarded.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/v1/tasks/task-1/migration/discard?artifactId=mig-0123456789abcdef", nil))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	// A wrong method is 405 from the registry, with the operation's own method set.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v1/tasks/task-1/migration/export", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	require.Equal(t, http.MethodPost, rec.Header().Get("Allow"))

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/v1/tasks/task-1/migration/discard?artifactId=mig-0123456789abcdef", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	require.Equal(t, http.MethodDelete, rec.Header().Get("Allow"))

	// An unregistered action under the namespace is a malformed path, not a 501.
	for _, path := range []string{
		"/v1/tasks/task-1/migration/bogus",
		"/v1/tasks/task-1/migration/export/extra",
		"/v1/tasks/task-1/exec/extra",
	} {
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, path)
		require.Equal(t, string(codeNotFound), decodeMigrationError(t, rec).Error.Code, path)
	}
}

// TestServerServesArtifactOperationsForAGoneTask pins the routing ruling: migration
// file, stage and discard are artifact-scoped, so the server never resolves the task
// in the path for them. The task here does not exist on this node - which is the
// state of the source once SwarmKit has replaced the exporting task, and of the
// target before the new task has ever run - and all three operations still answer.
// Export is not artifact-scoped, because it snapshots the task it names, and it keeps
// the unknown-task 404.
func TestServerServesArtifactOperationsForAGoneTask(t *testing.T) {
	cfg, doc := exportFixture(t)
	src := &fakeTaskSource{found: false}
	srv := newRouteServer(t, src)
	require.NoError(t, srv.Handle(MigrationExportOperation, NewMigrationExportHandler(MigrationHandlerConfig{})))
	require.NoError(t, srv.Handle(MigrationFileOperation, NewMigrationFileHandler(cfg)))
	require.NoError(t, srv.Handle(MigrationStageOperation, NewMigrationStageHandler(cfg)))
	require.NoError(t, srv.Handle(MigrationDiscardOperation, NewMigrationDiscardHandler(cfg)))

	// file: the artifact the gone task exported is still servable.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v1/tasks/task-gone/migration/file?artifactId="+doc.Artifact.ID+"&name=vm.state", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, doc.Artifact.StateFile.SHA256, rec.Header().Get(checksumHeader))

	// stage: a file of an artifact for a task this node has never run.
	const stagedID = "mig-0123456789abcdef"
	body := []byte("VMSTATE: staged for a task this node has never run\n")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPut,
		"/v1/tasks/task-gone/migration/stage?artifactId="+stagedID+"&name=vm.state&size="+strconv.Itoa(len(body))+"&sha256="+digest,
		bytes.NewReader(body)))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.FileExists(t, filepath.Join(cfg.ArtifactDir, stagedID, "vm.state"))

	// discard: the artifact is the subject, so it goes even though the task does not exist.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/v1/tasks/task-gone/migration/discard?artifactId="+stagedID, nil))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.NoDirExists(t, filepath.Join(cfg.ArtifactDir, stagedID))

	require.Empty(t, src.gotID, "an artifact-scoped operation must not resolve the task in its path")

	// A wrong method is still 405 rather than the unknown-task 404: for these three
	// operations the task in the path is never the reason an answer is refused.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/v1/tasks/task-gone/migration/discard?artifactId="+stagedID, nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	require.Equal(t, http.MethodDelete, rec.Header().Get("Allow"))

	// export is task-scoped, and keeps the contract for every other operation.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/v1/tasks/task-gone/migration/export?node=node-1", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Equal(t, "task-gone", src.gotID, "export resolves the task it snapshots")

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v1/tasks/task-gone/logs", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// sha256HexBytes is the hex-encoded SHA-256 of a body, for assertions about bytes a
// handler wrote rather than bytes a fixture declared.
func sha256HexBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// requireNoPartials fails when a staging directory still holds a temporary file.
// A published file is renamed, so nothing named like a partial may remain.
func requireNoPartials(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".partial-"),
			"a staging directory must not keep the partial file %q", entry.Name())
	}
}

// TestArtifactFilesFollowsTheManifest pins the manifest's optional disk: a
// snapshot that shipped no disk copy describes none, and only the files an
// artifact describes are addressable.
func TestArtifactFilesFollowsTheManifest(t *testing.T) {
	_, doc := exportFixture(t)

	require.Equal(t, []string{"vm.state", "vm.mem", "rootfs.ext4"}, artifactFileNames(doc.Artifact))

	withoutDisk := doc.Artifact
	withoutDisk.DiskFile = migration.File{}
	require.Equal(t, []string{"vm.state", "vm.mem"}, artifactFileNames(withoutDisk))

	_, found := artifactFile(withoutDisk, doc.Artifact.DiskFile.Name)
	require.False(t, found, "a file the manifest does not describe is not addressable")
}

// artifactFileNames lists the names a manifest describes, in field order.
func artifactFileNames(artifact migration.Artifact) []string {
	files := artifactFiles(artifact)
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.Name)
	}
	return names
}
