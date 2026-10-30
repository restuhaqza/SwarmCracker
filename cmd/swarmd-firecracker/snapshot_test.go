package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/apihttp"
	"github.com/restuhaqza/swarmcracker/pkg/broker"
	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/test/testhelpers"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
)

// TestBrokerRuntimeFrom_SnapshotDefaults pins the experimental surface off by
// default with the shared default directory.
func TestBrokerRuntimeFrom_SnapshotDefaults(t *testing.T) {
	cfg := runBrokerFlags(t)

	require.False(t, cfg.snapshotsEnabled, "snapshots are experimental and disabled by default")
	require.Equal(t, config.DefaultSnapshotDir, cfg.snapshotDir)
}

// enabledSnapshotRuntime assembles the broker runtime the way run.go does for a
// node whose snapshot surface is on: the daemon's own flag derivation, then the
// store newSnapshotManager builds. The daemon hands that one instance to both
// consumers, so a test that builds it here serves the same store startBroker gets.
//
// taskAlive is cleared because the daemon's probe asks the process table for a
// Firecracker PID, and a test has no VMM: the stat-only behaviour is what the
// other broker tests exercise.
func enabledSnapshotRuntime(t *testing.T, snapshotDir, socketDir, logDir string) brokerRuntime {
	t.Helper()

	cfg := runBrokerFlags(t, "--snapshots-enable", "--snapshot-dir", snapshotDir)
	cfg.listenAddr = "127.0.0.1:0"
	cfg.socketDir = socketDir
	cfg.logDir = logDir
	cfg.taskAlive = nil
	cfg.snapshotManager = newSnapshotManager(context.Background(), cfg)
	return cfg
}

// TestNewSnapshotManagerBuildsWithoutTheFlag pins the binding addendum: the node's
// snapshot store is built for every daemon, not only under --snapshots-enable,
// because stopping a task restored from a migration needs the manager that started
// its VM. The public snapshot surface stays behind the flag.
func TestNewSnapshotManagerBuildsWithoutTheFlag(t *testing.T) {
	snapshotDir := t.TempDir()
	cfg := runBrokerFlags(t, "--snapshot-dir", snapshotDir)
	require.False(t, cfg.snapshotsEnabled)
	require.Nil(t, cfg.snapshotManager, "the flag derivation does not construct the store")

	require.NotNil(t, newSnapshotManager(context.Background(), cfg),
		"a restored VM must be stoppable on a node that never enabled the experimental API")

	info, err := os.Stat(snapshotDir)
	require.NoError(t, err, "the store is rooted at --snapshot-dir")
	require.True(t, info.IsDir())

	_, _, store, active := snapshotHandlers(cfg)
	require.False(t, active, "the public snapshot surface stays behind its flag")
	require.Nil(t, store)
}

// TestNewSnapshotManagerReportsAFailedStore pins the degraded case: a directory
// that cannot be created leaves the node without a store, and the failure is
// returned as nil rather than stopping the daemon.
func TestNewSnapshotManagerReportsAFailedStore(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(notADir, nil, 0o600))

	cfg := runBrokerFlags(t, "--snapshot-dir", filepath.Join(notADir, "snapshots"))
	require.Nil(t, newSnapshotManager(context.Background(), cfg))
}

func TestBrokerRuntimeFrom_SnapshotFlags(t *testing.T) {
	cfg := runBrokerFlags(t, "--snapshots-enable", "--snapshot-dir", "/srv/snapshots")

	require.True(t, cfg.snapshotsEnabled)
	require.Equal(t, "/srv/snapshots", cfg.snapshotDir)
}

func TestAPIRuntimeFrom_SnapshotsEnabled(t *testing.T) {
	require.False(t, runAPIFlags(t).snapshotsEnabled)
	require.True(t, runAPIFlags(t, "--snapshots-enable").snapshotsEnabled)
}

// TestAPIRuntimeFrom_SnapshotTimeout pins the flag that bounds one relayed
// snapshot request: five minutes by default, because create copies the whole task
// disk, and an explicit duration wins.
func TestAPIRuntimeFrom_SnapshotTimeout(t *testing.T) {
	require.Equal(t, broker.DefaultSnapshotTimeout, runAPIFlags(t).snapshotTimeout)
	require.Equal(t, 5*time.Minute, runAPIFlags(t).snapshotTimeout,
		"a create copies a multi-GB disk; seconds would time it out")
	require.Equal(t, 90*time.Second, runAPIFlags(t, "--snapshot-timeout", "90s").snapshotTimeout)
}

// TestStartBroker_DisabledSnapshotsIs501 proves the snapshots operation is
// registered in disabled mode and refuses with the capability detail, not the
// server's generic unregistered-operation answer.
func TestStartBroker_DisabledSnapshotsIs501(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir, logDir: t.TempDir()}
	srv, snapshotsActive, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()
	require.False(t, snapshotsActive, "a disabled capability must not be reported active")

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	resp, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/snapshots")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotImplemented, resp.StatusCode)

	var doc v1.ErrorDocument
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, v1.KindError, doc.Kind)
	require.Equal(t, "snapshots", doc.Error.Details["capability"])
	require.Equal(t, "snapshots_disabled", doc.Error.Details["reason"])
}

// TestStartBroker_RestoredOperationIsRegistered proves the daemon registers the
// restored-VM stop operation next to snapshots: a disabled capability answers the
// typed 501 rather than the server's unregistered-operation answer, and a wrong
// method is 405.
func TestStartBroker_RestoredOperationIsRegistered(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir, logDir: t.TempDir()}
	srv, _, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	req, err := http.NewRequest(http.MethodDelete, "https://"+srv.Addr()+"/v1/tasks/task-1/restored", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotImplemented, resp.StatusCode)
	var doc v1.ErrorDocument
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, "snapshots", doc.Error.Details["capability"])

	respGet, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/restored")
	require.NoError(t, err)
	defer respGet.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, respGet.StatusCode)
	require.Contains(t, respGet.Header.Get("Allow"), http.MethodDelete)
}

// TestStartBroker_RestoredSharesTheSnapshotStore proves the enabled daemon hands
// both operations the same store: the enabled node's restored route reaches the
// real manager, which tracks no VM yet and answers no_restored_vm (404), instead
// of the disabled 501.
func TestStartBroker_RestoredSharesTheSnapshotStore(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	cfg := enabledSnapshotRuntime(t, t.TempDir(), socketDir, t.TempDir())
	srv, active, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()
	require.True(t, active)

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	req, err := http.NewRequest(http.MethodDelete, "https://"+srv.Addr()+"/v1/tasks/task-1/restored", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	var doc v1.ErrorDocument
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, "no_restored_vm", doc.Error.Details["reason"])
}

// TestStartBroker_EnabledSnapshotsListsMetadata proves the enabled daemon builds
// a real store rooted at --snapshot-dir: the list reads pkg/snapshot's metadata
// file and never publishes the memory or state path.
func TestStartBroker_EnabledSnapshotsListsMetadata(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	snapshotDir := t.TempDir()
	writeSnapshotMetadata(t, snapshotDir, `{
  "id": "snap-abc123",
  "task_id": "task-1",
  "service_id": "svc-1",
  "node_id": "node-1",
  "created_at": "2026-10-24T12:00:00Z",
  "memory_path": "/var/lib/firecracker/snapshots/snap-abc123/vm.mem",
  "state_path": "/var/lib/firecracker/snapshots/snap-abc123/vm.state",
  "size_bytes": 4096,
  "vcpu_count": 2,
  "memory_mb": 512,
  "rootfs_path": "/var/lib/firecracker/rootfs/task-1",
  "checksum": "deadbeef"
}`)

	cfg := enabledSnapshotRuntime(t, snapshotDir, socketDir, t.TempDir())
	srv, snapshotsActive, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	defer stop()
	require.True(t, snapshotsActive, "a usable store must report the capability active")

	client := brokerTestClient(clientCert, pool)
	defer client.CloseIdleConnections()

	resp, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/snapshots")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var doc v1.SnapshotList
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Len(t, doc.Items, 1)
	require.Equal(t, "snap-abc123", doc.Items[0].ID)
	require.Equal(t, "task-1", doc.Items[0].TaskID)
	require.Equal(t, "/var/lib/firecracker/rootfs/task-1", doc.Items[0].RootfsPath)

	body, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NotContains(t, string(body), "vm.mem")
	require.NotContains(t, string(body), "vm.state")
}

// writeSnapshotMetadata seeds one pkg/snapshot directory the way the snapshot
// engine writes it.
func writeSnapshotMetadata(t *testing.T, snapshotDir, metadata string) {
	t.Helper()

	dir := filepath.Join(snapshotDir, "snap-abc123")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(metadata), 0o600))
}

// TestStartAPI_AdvertisesSnapshotsWhenEnabled pins the daemon wiring: the API
// sets the capability and hands it the same broker manager.
func TestStartAPI_AdvertisesSnapshotsWhenEnabled(t *testing.T) {
	stateDir := testhelpers.ShortTempDir(t)
	seedNodeCertificates(t, stateDir)

	sock := filepath.Join(stateDir, "api.sock")
	cfg := apiRuntime{
		enabled:          true,
		socket:           sock,
		tokenFile:        filepath.Join(stateDir, "api", "tokens.json"),
		certDir:          filepath.Join(stateDir, "api"),
		auditFile:        filepath.Join(stateDir, "api", "audit.jsonl"),
		auditRetention:   720 * time.Hour,
		snapshotsEnabled: true,
	}

	stop, err := startAPI(context.Background(), cfg, stateDir, "node-1", filepath.Join(stateDir, "swarm.sock"))
	require.NoError(t, err)
	defer stop()

	resp := testhelpers.WaitForGet(t, testhelpers.UnixHTTPClient(sock), "http://unix/v1/version", apihttp.MediaType, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var doc v1.VersionInfo
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Contains(t, doc.Capabilities, "snapshots")
	require.Contains(t, doc.Capabilities, "exec", "the shared broker transport is still advertised")
}

// TestStartAPI_OmitsSnapshotsWhenStoreFails pins I5: a snapshot store that could
// not be constructed must not be advertised. The broker reports the effective
// state, and the API runtime forwards it instead of echoing the flag.
func TestStartAPI_OmitsSnapshotsWhenStoreFails(t *testing.T) {
	// A regular file where a directory should be makes MkdirAll fail with
	// ENOTDIR, so snapshot.NewManager returns an error.
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(notADir, nil, 0o600))
	badSnapshotDir := filepath.Join(notADir, "snapshots")

	brokerStateDir := t.TempDir()
	_, _ = seedBrokerCertificates(t, brokerStateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	// The daemon builds the store on every run, and its failure is what leaves the
	// capability off: the runtime is assembled the same way run.go assembles it.
	cfg := enabledSnapshotRuntime(t, badSnapshotDir, socketDir, t.TempDir())
	require.Nil(t, cfg.snapshotManager,
		"a snapshot directory that cannot be created leaves the daemon without a store")

	_, snapshotsActive, stopBroker, err := startBroker(context.Background(), cfg, brokerStateDir)
	require.NoError(t, err, "the broker must still start when the snapshot store fails")
	defer stopBroker()
	require.False(t, snapshotsActive, "a store that failed to build must not report active")

	apiStateDir := testhelpers.ShortTempDir(t)
	seedNodeCertificates(t, apiStateDir)
	sock := filepath.Join(apiStateDir, "api.sock")

	stopAPI, err := startAPI(context.Background(), apiRuntime{
		enabled:          true,
		socket:           sock,
		tokenFile:        filepath.Join(apiStateDir, "api", "tokens.json"),
		certDir:          filepath.Join(apiStateDir, "api"),
		auditFile:        filepath.Join(apiStateDir, "api", "audit.jsonl"),
		auditRetention:   720 * time.Hour,
		snapshotsEnabled: snapshotsActive,
	}, apiStateDir, "node-1", filepath.Join(apiStateDir, "swarm.sock"))
	require.NoError(t, err)
	defer stopAPI()

	resp := testhelpers.WaitForGet(t, testhelpers.UnixHTTPClient(sock), "http://unix/v1/version", apihttp.MediaType, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var doc v1.VersionInfo
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.NotContains(t, doc.Capabilities, "snapshots",
		"the version document must not promise a store that does not exist")
	require.Contains(t, doc.Capabilities, "exec", "the rest of the broker surface still works")
}
