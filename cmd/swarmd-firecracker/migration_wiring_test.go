package main

// The daemon's wiring for a restored task: the directive store it opens once, the
// migration root and the snapshot manager it hands the executor.
//
// The two things asserted here are the ones the executor's start depends on and cannot
// see: that the store the broker writes is the store the executor reads (one directory,
// not two instances over different ones), and that a state directory is what makes
// directives survive a daemon restart.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/swarmkit"
)

// prepareRequestFor is the prepare body a manager sends: the service, the artifact
// the directive names, and the artifact's manifest - the document the target writes
// as artifact.json for the restore it is promising.
func prepareRequestFor(t *testing.T, serviceID, artifactID string) string {
	t.Helper()

	artifact := newMigrationTestArtifact(t, "task-1", serviceID, "node-1", migrationPayloads())
	artifact.ID = artifactID
	require.NoError(t, artifact.Validate())

	body, err := json.Marshal(struct {
		ServiceID  string             `json:"serviceId"`
		ArtifactID string             `json:"artifactId"`
		Artifact   migration.Artifact `json:"artifact"`
	}{serviceID, artifactID, artifact})
	require.NoError(t, err)
	return string(body)
}

// TestPendingRestoresLiveUnderTheStateDirectory pins where the directive lives: beside
// the restored-instance records, under the directory the daemon already owns, so a
// restart finds the directives it wrote and a line of cleanup is one directory.
func TestPendingRestoresLiveUnderTheStateDirectory(t *testing.T) {
	stateDir := t.TempDir()

	store, err := pendingRestores(stateDir)
	require.NoError(t, err)

	staged := filepath.Join(stateDir, "migrations", "mig-0123456789abcdef")
	require.NoError(t, os.MkdirAll(staged, 0o755))
	require.NoError(t, store.Put(scruntime.PendingRestore{
		ServiceID:  "service-1",
		ArtifactID: "mig-0123456789abcdef",
		Dir:        staged,
		CreatedAt:  time.Now().UTC(),
	}))

	assert.FileExists(t, filepath.Join(stateDir, scruntime.PendingRestoreDirName, "service-1.json"))

	// A second open of the same directory finds it: that is what makes the broker's
	// writer and the executor's reader the same store.
	reopened, err := pendingRestores(stateDir)
	require.NoError(t, err)
	record, found, err := reopened.Take("service-1", filepath.Join(stateDir, "migrations"))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "mig-0123456789abcdef", record.ArtifactID)
}

// TestPendingRestoresNeedsAStateDirectory pins the refusal: without --state-dir there is
// nowhere to keep a directive, and a node that cannot keep one must not answer a prepare
// as though it had.
func TestPendingRestoresNeedsAStateDirectory(t *testing.T) {
	_, err := pendingRestores(" ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state directory")
}

// TestOpenDaemonStateOpensBothStores pins the one call the daemon makes at startup: the
// restored-instance records and the pending directives are both opened, under the same
// state directory, before anything can arrive.
func TestOpenDaemonStateOpensBothStores(t *testing.T) {
	stateDir := t.TempDir()

	var got string
	var restored *scruntime.RestoredRegistry
	var pending *scruntime.PendingRestores
	var err error

	app := cli.NewApp()
	app.Flags = newAppFlags()
	app.Action = func(c *cli.Context) error {
		got, restored, pending, err = openDaemonState(c)
		return nil
	}
	require.NoError(t, app.Run([]string{"swarmd-firecracker", "--state-dir", stateDir}))
	require.NoError(t, err)
	require.Equal(t, stateDir, got)
	require.NotNil(t, restored)
	require.NotNil(t, pending)

	assert.DirExists(t, filepath.Join(stateDir, scruntime.RestoredInstanceDirName))
	assert.DirExists(t, filepath.Join(stateDir, scruntime.PendingRestoreDirName))
}

// TestStartBrokerUsesTheDaemonsDirectiveStore pins the sharing the executor depends on:
// the broker is handed the store the daemon opened, and a prepare lands in that store
// rather than in one the broker opened for itself. A second store over a different
// directory would accept the prepare and leave the executor's store empty, so the
// service's next task would boot cold after a successful migration.
func TestStartBrokerUsesTheDaemonsDirectiveStore(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)

	// The artifact root the prepare route checks the directory against.
	artifactRoot := t.TempDir()
	artifactID := "mig-0123456789abcdef"
	require.NoError(t, os.MkdirAll(filepath.Join(artifactRoot, artifactID), 0o755))

	// Deliberately not the daemon's state directory: if the broker opened its own
	// store, the directive would land here instead of in the handed-in store.
	pendingDir := filepath.Join(t.TempDir(), scruntime.PendingRestoreDirName)
	handedIn, err := scruntime.NewPendingRestores(pendingDir)
	require.NoError(t, err)

	cfg := brokerRuntime{
		listenAddr:          "127.0.0.1:0",
		socketDir:           t.TempDir(),
		logDir:              t.TempDir(),
		migrationStagingDir: artifactRoot,
		pending:             handedIn,
	}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	body := prepareRequestFor(t, "service-1", artifactID)
	req, err := http.NewRequest(http.MethodPost,
		"https://"+srv.Addr()+"/v1/tasks/migrating/migration/prepare", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	assert.FileExists(t, filepath.Join(pendingDir, "service-1.json"),
		"the directive must land in the store the executor was given")
	assert.NoFileExists(t, filepath.Join(stateDir, scruntime.PendingRestoreDirName, "service-1.json"),
		"the broker must not open a store of its own beside the daemon's")
	assert.FileExists(t, filepath.Join(artifactRoot, artifactID, "artifact.json"),
		"the manifest the restored task reads must be in the directory the directive names")
}

// TestWireMigrationRestoresDegradesToBooting pins the two wiring states a node can be in:
// no snapshot manager (the store could not be built) or no store, both of which leave the
// executor booting every task. Consuming a directive for an artifact this node cannot load
// would be worse than booting: the migration would fail and the directive would be spent.
func TestWireMigrationRestoresDegradesToBooting(t *testing.T) {
	exec := &swarmkit.Executor{}
	pending, err := scruntime.NewPendingRestores(filepath.Join(t.TempDir(), scruntime.PendingRestoreDirName))
	require.NoError(t, err)

	require.NotPanics(t, func() {
		wireMigrationRestores(exec, pending, brokerRuntime{migrationStagingDir: t.TempDir()}, nil)
		wireMigrationRestores(exec, nil, brokerRuntime{migrationStagingDir: t.TempDir()}, &snapshot.Manager{})
		wireMigrationRestores(nil, pending, brokerRuntime{migrationStagingDir: t.TempDir()}, &snapshot.Manager{})
	})
}

// TestMigrationPrepareOperationIsRegistered pins that the daemon's broker answers the
// prepare route at all: the executor's start path consumes what this route writes, and a
// route missing from the daemon is a migration that can never be restored.
func TestMigrationPrepareOperationIsRegistered(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)

	cfg := brokerRuntime{
		listenAddr:          "127.0.0.1:0",
		socketDir:           t.TempDir(),
		logDir:              t.TempDir(),
		migrationStagingDir: t.TempDir(),
	}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	// An unknown artifact is a typed refusal, not the server's unregistered-operation
	// answer: the route exists and validates what it was sent.
	body := prepareRequestFor(t, "service-1", "mig-0123456789abcdef")
	req, err := http.NewRequest(http.MethodPost,
		"https://"+srv.Addr()+"/v1/tasks/migrating/migration/prepare", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.NotEqual(t, http.StatusNotImplemented, resp.StatusCode)
	var doc v1.ErrorDocument
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	assert.Equal(t, v1.KindError, doc.Kind, "a missing artifact is a typed error, not a routing miss")
	assert.Equal(t, string(output.CodeNotFound), string(doc.Error.Code),
		"the route was reached and refused the artifact it does not hold")
}
