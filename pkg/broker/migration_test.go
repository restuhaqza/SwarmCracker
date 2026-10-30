package broker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// migrationConfig is a fully configured node: one snapshot root, one staging root,
// and the executor's rootfs directory.
func migrationConfig(t *testing.T, store SnapshotStore) MigrationHandlerConfig {
	t.Helper()

	return MigrationHandlerConfig{
		Store:       store,
		SnapshotDir: t.TempDir(),
		StagingDir:  t.TempDir(),
		RootfsDir:   "/var/lib/firecracker/rootfs",
	}
}

// exportFixture runs one successful export and returns its document with the
// config it used, so a file or stage test starts from a real artifact.
func exportFixture(t *testing.T) (MigrationHandlerConfig, MigrationExportDocument) {
	t.Helper()

	store := &fakeSnapshotStore{}
	cfg := migrationConfig(t, store)
	store.createInfo = seedSnapshot(t, cfg.SnapshotDir)

	rec := serveMigration(t, NewMigrationExportHandler(cfg), snapshotTask(), http.MethodPost,
		"/v1/tasks/task-1/migration/export?service=svc-1&node=node-1", nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var doc MigrationExportDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	return cfg, doc
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
	store := &fakeSnapshotStore{}
	cfg := migrationConfig(t, store)
	store.createInfo = seedSnapshot(t, cfg.SnapshotDir)

	rec := serveMigration(t, NewMigrationExportHandler(cfg), snapshotTask(), http.MethodPost,
		"/v1/tasks/task-1/migration/export?service=svc-1&node=node-1", nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, publicMediaType, rec.Header().Get("Content-Type"))

	var doc MigrationExportDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
	require.Equal(t, migrationExportKind, doc.Kind)

	artifact := doc.Artifact
	require.NoError(t, artifact.Validate(), "an exported manifest must be complete")
	require.Equal(t, "task-1", artifact.SourceTaskID)
	require.Equal(t, "node-1", artifact.SourceNodeID)
	require.Equal(t, "svc-1", artifact.ServiceID)
	require.Equal(t, "/var/run/firecracker/task-1.vsock", artifact.VsockPath)
	require.Equal(t, "/var/lib/firecracker/rootfs/tasks/task-1.ext4", artifact.DiskPath)
	require.Equal(t, filepath.Join(cfg.SnapshotDir, artifact.ID), doc.Dir)

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

	// The manifest is node-local metadata in the snapshot directory: written there
	// (so the snapshot store's directory is the only place the artifact lives) and
	// never one of the files that move.
	manifest, err := os.ReadFile(filepath.Join(filepath.Dir(store.createInfo.StatePath), migrationArtifactDocumentName))
	require.NoError(t, err)
	var stored migration.Artifact
	require.NoError(t, json.Unmarshal(manifest, &stored))
	require.Equal(t, artifact, stored)
	_, isServable := artifactFile(artifact, migrationArtifactDocumentName)
	require.False(t, isServable, "the manifest is not one of the artifact's files")

	// The snapshot store still holds a complete record: the artifact directory is a
	// link to it, so DeleteSnapshot frees the bytes the transfer reads.
	info, statErr := os.Stat(doc.Dir)
	require.NoError(t, statErr)
	require.True(t, info.IsDir(), "the artifact directory resolves to the snapshot directory")
	require.FileExists(t, filepath.Join(cfg.SnapshotDir, "snap-0123456789abcdef", "vm.state"))
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
			name:  "no snapshot directory",
			store: &fakeSnapshotStore{},
			cfg: func(c *MigrationHandlerConfig) {
				c.SnapshotDir = ""
			},
			method:     http.MethodPost,
			target:     "/v1/tasks/task-1/migration/export?node=node-1",
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   output.CodeUnavailable,
			wantDetail: "snapshot_dir_unconfigured",
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
				SnapshotDir: t.TempDir(),
				StagingDir:  t.TempDir(),
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
			if store != nil && !tc.reachesStore {
				require.Empty(t, store.gotTaskID, "a refused export must not reach the store")
			}
		})
	}
}

// TestMigrationExportPrunesSpentArtifacts pins the one piece of housekeeping the
// export owns: a link whose snapshot is gone is not an artifact, and leaving it
// behind would accumulate one dead entry per completed migration.
func TestMigrationExportPrunesSpentArtifacts(t *testing.T) {
	store := &fakeSnapshotStore{}
	cfg := migrationConfig(t, store)
	store.createInfo = seedSnapshot(t, cfg.SnapshotDir)

	spent := filepath.Join(cfg.SnapshotDir, "mig-0000000000000001")
	require.NoError(t, os.Symlink(filepath.Join(cfg.SnapshotDir, "snap-gone"), spent))

	rec := serveMigration(t, NewMigrationExportHandler(cfg), snapshotTask(), http.MethodPost,
		"/v1/tasks/task-1/migration/export?node=node-1", nil)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	_, err := os.Lstat(spent)
	require.ErrorIs(t, err, os.ErrNotExist, "a link whose snapshot is gone must be removed")

	var doc MigrationExportDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.FileExists(t, filepath.Join(doc.Dir, "vm.state"), "the exported artifact must survive the prune")
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
// name that is a path is refused before any join.
func TestMigrationFileRefusesWhatIsNotAnArtifactFile(t *testing.T) {
	cfg, doc := exportFixture(t)
	handler := NewMigrationFileHandler(cfg)

	cases := []struct {
		name       string
		task       Task
		target     string
		wantStatus int
		wantReason string
	}{
		{
			name:       "manifest",
			task:       snapshotTask(),
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=" + migrationArtifactDocumentName,
			wantStatus: http.StatusBadRequest,
			wantReason: "unknown_artifact_file",
		},
		{
			name:       "snapshot metadata",
			task:       snapshotTask(),
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=metadata.json",
			wantStatus: http.StatusBadRequest,
			wantReason: "unknown_artifact_file",
		},
		{
			name:       "traversal name",
			task:       snapshotTask(),
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=..%2Fvm.state",
			wantStatus: http.StatusBadRequest,
			wantReason: "invalid_file_name",
		},
		{
			name:       "subdirectory name",
			task:       snapshotTask(),
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=sub%2Fvm.state",
			wantStatus: http.StatusBadRequest,
			wantReason: "invalid_file_name",
		},
		{
			name:       "dot dot name",
			task:       snapshotTask(),
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=..",
			wantStatus: http.StatusBadRequest,
			wantReason: "invalid_file_name",
		},
		{
			name:       "traversal artifact",
			task:       snapshotTask(),
			target:     "/v1/tasks/task-1/migration/file?artifactId=..%2F..%2Fetc&name=vm.state",
			wantStatus: http.StatusBadRequest,
			wantReason: "invalid_artifact_id",
		},
		{
			name:       "unknown artifact",
			task:       snapshotTask(),
			target:     "/v1/tasks/task-1/migration/file?artifactId=mig-0000000000000000&name=vm.state",
			wantStatus: http.StatusNotFound,
			wantReason: "artifact_not_available",
		},
		{
			name:       "another task's artifact",
			task:       Task{ID: "task-2", SocketPath: "/var/run/firecracker/task-2.sock", Running: true},
			target:     "/v1/tasks/task-2/migration/file?artifactId=" + doc.Artifact.ID + "&name=vm.state",
			wantStatus: http.StatusConflict,
			wantReason: "artifact_task_mismatch",
		},
		{
			name:       "wrong method",
			task:       snapshotTask(),
			target:     "/v1/tasks/task-1/migration/file?artifactId=" + doc.Artifact.ID + "&name=vm.state",
			wantStatus: http.StatusMethodNotAllowed,
			wantReason: "method_not_allowed",
		},
		{
			name:       "unknown parameter",
			task:       snapshotTask(),
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

			rec := serveMigration(t, handler, tc.task, method, tc.target, nil)

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

// TestMigrationFileRefusesAFileThatChangedAfterTheExport pins the size check: the
// digest the caller was promised describes the file at export time, so a file that
// no longer matches its description is a conflict rather than a stream the caller
// cannot verify.
func TestMigrationFileRefusesAFileThatChangedAfterTheExport(t *testing.T) {
	cfg, doc := exportFixture(t)

	path := filepath.Join(cfg.SnapshotDir, doc.Artifact.ID, doc.Artifact.MemoryFile.Name)
	require.NoError(t, os.Truncate(path, doc.Artifact.MemoryFile.Size-1))

	rec := serveMigration(t, NewMigrationFileHandler(cfg), snapshotTask(), http.MethodGet,
		"/v1/tasks/task-1/migration/file?artifactId="+doc.Artifact.ID+"&name="+doc.Artifact.MemoryFile.Name, nil)

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	doc2 := decodeMigrationError(t, rec)
	require.Equal(t, "artifact_changed", doc2.Error.Details["reason"])

	// An artifact whose snapshot was cleaned up is gone, not corrupt: the same 404
	// a never-exported ID answers.
	require.NoError(t, os.RemoveAll(filepath.Join(cfg.SnapshotDir, "snap-0123456789abcdef")))
	rec = serveMigration(t, NewMigrationFileHandler(cfg), snapshotTask(), http.MethodGet,
		"/v1/tasks/task-1/migration/file?artifactId="+doc.Artifact.ID+"&name="+doc.Artifact.StateFile.Name, nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Equal(t, "artifact_not_available", decodeMigrationError(t, rec).Error.Details["reason"])
}

// TestMigrationExportRefusesASnapshotItCannotDescribe pins the answer for a
// snapshot that is not a complete artifact: the failure is the node's, and the
// pkg/migration rejection (which names absolute paths) never reaches the wire.
func TestMigrationExportRefusesASnapshotItCannotDescribe(t *testing.T) {
	store := &fakeSnapshotStore{}
	cfg := migrationConfig(t, store)

	info := seedSnapshot(t, cfg.SnapshotDir)
	info.DiskPath = "" // a snapshot with no disk copy cannot move a filesystem
	store.createInfo = info

	rec := serveMigration(t, NewMigrationExportHandler(cfg), snapshotTask(), http.MethodPost,
		"/v1/tasks/task-1/migration/export?node=node-1", nil)

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), cfg.SnapshotDir,
		"a pkg/migration rejection names paths; only the classification may leave the node")
}

// TestMigrationExportReportsAPublishFailure pins the last step: a node whose
// artifact root cannot hold the link cannot publish the artifact, even though the
// snapshot itself was taken.
func TestMigrationExportReportsAPublishFailure(t *testing.T) {
	store := &fakeSnapshotStore{}
	cfg := migrationConfig(t, store)
	store.createInfo = seedSnapshot(t, t.TempDir())
	cfg.SnapshotDir = filepath.Join(t.TempDir(), "missing-root")

	rec := serveMigration(t, NewMigrationExportHandler(cfg), snapshotTask(), http.MethodPost,
		"/v1/tasks/task-1/migration/export?node=node-1", nil)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, string(output.CodeUnavailable), decodeMigrationError(t, rec).Error.Code)
}

// TestMigrationFileRefusesAManifestItCannotUse pins the two manifest failures: a
// manifest that does not validate is the node's problem (500), and a path that is
// not a readable file is unavailability (503). Neither is the caller's mistake.
func TestMigrationFileRefusesAManifestItCannotUse(t *testing.T) {
	t.Run("invalid manifest", func(t *testing.T) {
		cfg, doc := exportFixture(t)
		manifest := filepath.Join(cfg.SnapshotDir, doc.Artifact.ID, migrationArtifactDocumentName)
		require.NoError(t, os.WriteFile(manifest, []byte("{ not a manifest"), 0o600))

		rec := serveMigration(t, NewMigrationFileHandler(cfg), snapshotTask(), http.MethodGet,
			"/v1/tasks/task-1/migration/file?artifactId="+doc.Artifact.ID+"&name=vm.state", nil)

		require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		require.Equal(t, "artifact_invalid", decodeMigrationError(t, rec).Error.Details["reason"])
	})

	t.Run("manifest is not a file", func(t *testing.T) {
		cfg, doc := exportFixture(t)
		manifest := filepath.Join(cfg.SnapshotDir, doc.Artifact.ID, migrationArtifactDocumentName)
		require.NoError(t, os.Remove(manifest))
		require.NoError(t, os.Mkdir(manifest, 0o755))

		rec := serveMigration(t, NewMigrationFileHandler(cfg), snapshotTask(), http.MethodGet,
			"/v1/tasks/task-1/migration/file?artifactId="+doc.Artifact.ID+"&name=vm.state", nil)

		require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
		require.Equal(t, "artifact_unreadable", decodeMigrationError(t, rec).Error.Details["reason"])
	})
}

// TestMigrationFileNeedsASnapshotRoot pins the one configuration the file
// operation cannot work without.
func TestMigrationFileNeedsASnapshotRoot(t *testing.T) {
	rec := serveMigration(t, NewMigrationFileHandler(MigrationHandlerConfig{}), snapshotTask(),
		http.MethodGet, "/v1/tasks/task-1/migration/file?artifactId=mig-0123456789abcdef&name=vm.state", nil)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Equal(t, "snapshot_dir_unconfigured", decodeMigrationError(t, rec).Error.Details["reason"])
}

// TestMigrationStagePublishesOnlyVerifiedBytes pins the upload contract: a matching
// body is published atomically, restaging it succeeds, and a body that does not
// match the declared size or digest leaves neither a file nor a partial file.
func TestMigrationStagePublishesOnlyVerifiedBytes(t *testing.T) {
	cfg := MigrationHandlerConfig{StagingDir: t.TempDir()}
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
	stagedPath := filepath.Join(cfg.StagingDir, artifactID, name)

	t.Run("accepts a matching file", func(t *testing.T) {
		rec := serveMigration(t, handler, Task{ID: "task-2"}, http.MethodPut, target(size, digest), body)

		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		require.Equal(t, digest, rec.Header().Get(checksumHeader))
		staged, err := os.ReadFile(stagedPath)
		require.NoError(t, err)
		require.Equal(t, body, staged)
		requireNoPartials(t, filepath.Dir(stagedPath))
	})

	t.Run("is idempotent", func(t *testing.T) {
		for attempt := 0; attempt < 2; attempt++ {
			rec := serveMigration(t, handler, Task{ID: "task-2"}, http.MethodPut, target(size, digest), body)
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

		rec := serveMigration(t, handler, Task{ID: "task-2"}, http.MethodPut,
			target(int64(len(bad)), digest), bad)

		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Equal(t, "checksum_mismatch", decodeMigrationError(t, rec).Error.Details["reason"])

		staged, err := os.ReadFile(stagedPath)
		require.NoError(t, err)
		require.Equal(t, body, staged, "a refused stage must not replace the file it could not verify")
		requireNoPartials(t, filepath.Dir(stagedPath))
	})

	t.Run("refuses a short body", func(t *testing.T) {
		rec := serveMigration(t, handler, Task{ID: "task-2"}, http.MethodPut,
			target(size+10, digest), body)

		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Equal(t, "size_mismatch", decodeMigrationError(t, rec).Error.Details["reason"])
		requireNoPartials(t, filepath.Dir(stagedPath))
	})

	t.Run("refuses a declared length above the size", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, target(size, digest), bytes.NewReader(append(body, 'x')))
		rec := httptest.NewRecorder()
		handler(context.Background(), Task{ID: "task-2"}, rec, req)

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
		handler(context.Background(), Task{ID: "task-2"}, rec, req)

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
			target:     "/v1/tasks/task-2/migration/stage?artifactId=" + artifactID + "&name=vm.state&size=" + strconv.FormatInt(maxArtifactFileBytes+1, 10) + "&sha256=" + digest,
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
				cfg.StagingDir = t.TempDir()
			}

			rec := serveMigration(t, NewMigrationStageHandler(cfg), Task{ID: "task-2"},
				http.MethodPut, tc.target, []byte("x"))

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			if tc.reason != "" {
				require.Equal(t, tc.reason, decodeMigrationError(t, rec).Error.Details["reason"])
			}
			if cfg.StagingDir != "" {
				entries, err := os.ReadDir(cfg.StagingDir)
				require.NoError(t, err)
				require.Empty(t, entries, "a refused stage must not create anything")
			}
		})
	}
}

// TestMigrationStageRefusesAnotherMethod pins the method set the server enforces
// for the operation.
func TestMigrationStageRefusesAnotherMethod(t *testing.T) {
	rec := serveMigration(t, NewMigrationStageHandler(MigrationHandlerConfig{StagingDir: t.TempDir()}),
		Task{ID: "task-2"}, http.MethodPost,
		"/v1/tasks/task-2/migration/stage?artifactId=mig-0123456789abcdef&name=vm.state&size=1&sha256="+strings.Repeat("a", sha256HexChars), nil)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	require.Equal(t, http.MethodPut, rec.Header().Get("Allow"))
}

// TestServerRoutesMigrationOperations pins the wire shape the manager will call:
// a namespaced operation is two path segments, the registry enforces its method,
// and anything else under the namespace stays a malformed path.
func TestServerRoutesMigrationOperations(t *testing.T) {
	cfg, doc := exportFixture(t)
	srv := newRouteServer(t, &fakeTaskSource{task: snapshotTask(), found: true})
	require.NoError(t, srv.Handle(MigrationExportOperation, NewMigrationExportHandler(MigrationHandlerConfig{})))
	require.NoError(t, srv.Handle(MigrationFileOperation, NewMigrationFileHandler(cfg)))
	require.NoError(t, srv.Handle(MigrationStageOperation, NewMigrationStageHandler(MigrationHandlerConfig{StagingDir: t.TempDir()})))

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

	// A wrong method is 405 from the registry, with the operation's own method set.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v1/tasks/task-1/migration/export", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
	require.Equal(t, http.MethodPost, rec.Header().Get("Allow"))

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
