package broker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// migrationTestTask is the source task the relay's fixtures name. Its ID is also
// the VM identity the artifact describes, because the artifact's host-side names
// are derived from it.
const migrationTestTask = "task-1"

// migrationNodeReader answers the two nodes a relay test needs. Both are reachable
// through the one test broker: a Manager has a single broker port, so two node IDs
// on one address is the only way to model a source and a target without a second
// port the real cluster does not have.
type migrationNodeReader struct {
	nodes map[string]v1.Node
	err   error
}

func (r *migrationNodeReader) GetNode(_ context.Context, nodeID string) (v1.Node, error) {
	if r.err != nil {
		return v1.Node{}, r.err
	}
	node, ok := r.nodes[nodeID]
	if !ok {
		return v1.Node{}, errors.New("node " + nodeID + " is not in the cluster")
	}
	return node, nil
}

// newMigrationTestManager builds a manager that can address node-1 and node-2 on
// the test broker. migrationTimeout is the relay's own deadline; zero keeps the
// default.
func newMigrationTestManager(t *testing.T, pki testPKI, baseURL string, migrationTimeout time.Duration) *Manager {
	t.Helper()

	host, port := brokerTarget(t, baseURL)
	mgr, err := NewManager(ManagerConfig{
		Tasks: &stubTaskReader{task: v1.Task{
			ID: migrationTestTask, NodeID: "node-1", ServiceID: "svc-1",
		}},
		Nodes: &migrationNodeReader{nodes: map[string]v1.Node{
			"node-1": {ID: "node-1", Address: host},
			"node-2": {ID: "node-2", Address: host},
		}},
		Port:             port,
		Client:           pki.httpClient(),
		MigrationTimeout: migrationTimeout,
	})
	require.NoError(t, err)
	return mgr
}

// migrationArtifactFixture builds a manifest that passes Validate over the bodies
// a test relays. A nil disk leaves the optional disk entry out, the way a snapshot
// that shipped no disk copy does.
func migrationArtifactFixture(t *testing.T, state, memory, disk []byte) migration.Artifact {
	t.Helper()

	artifact := migration.Artifact{
		SchemaVersion:   v1.SchemaVersion,
		Kind:            migration.ArtifactKind,
		ID:              "mig-0123456789abcdef",
		SnapshotID:      "snap-0123456789abcdef",
		SourceTaskID:    migrationTestTask,
		RequestedTaskID: migrationTestTask,
		SourceNodeID:    "node-1",
		ServiceID:       "svc-1",
		CreatedAt:       time.Now().UTC(),
		SnapshotAt:      time.Now().UTC().Add(-time.Minute),
		TapName:         scruntime.TapName(migrationTestTask, 0),
		VsockPath:       "/var/run/firecracker/" + migrationTestTask + ".vsock",
		DiskPath:        "/var/lib/firecracker/rootfs/tasks/" + migrationTestTask + ".ext4",
		StateFile:       migrationTestFile("vm.state", state),
		MemoryFile:      migrationTestFile("vm.mem", memory),
	}
	if disk != nil {
		artifact.DiskFile = migrationTestFile("rootfs.ext4", disk)
	}
	require.NoError(t, artifact.Validate())
	return artifact
}

// migrationTestFile describes one body as a manifest entry.
func migrationTestFile(name string, body []byte) migration.File {
	sum := sha256.Sum256(body)
	return migration.File{Name: name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
}

// migrationTestBody returns deterministic bytes; pattern lets a test tell two
// bodies of the same size apart.
func migrationTestBody(size int, pattern byte) []byte {
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i) * 31
		body[i] += pattern
	}
	return body
}

// migrationTestEntry resolves one file of a fixture artifact.
func migrationTestEntry(t *testing.T, artifact migration.Artifact, name string) migration.File {
	t.Helper()

	entry, found := artifactFile(artifact, name)
	require.True(t, found, "the fixture must describe %q", name)
	return entry
}

// writeNodeError answers with one typed broker error, the document a real node
// sends.
func writeNodeError(t *testing.T, w http.ResponseWriter, status int, code output.Code, message string) {
	t.Helper()

	body, err := encodeDocument(output.NewError(code, message).Document())
	require.NoError(t, err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// TestManagerExportAsksTheSourceNode pins the request the manager makes: a POST on
// the task's export operation, addressed to the node the task runs on, carrying the
// scope the manifest records. The document the node answers is returned validated.
func TestManagerExportAsksTheSourceNode(t *testing.T) {
	pki := newTestPKI(t)
	artifact := migrationArtifactFixture(t, []byte("state"), migrationTestBody(64, 1), nil)

	type observed struct {
		method string
		path   string
		query  string
	}
	var got observed

	src := &fakeTaskSource{task: Task{ID: migrationTestTask, Running: true}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		MigrationExportOperation: func(_ context.Context, task Task, w http.ResponseWriter, r *http.Request) {
			got = observed{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}
			body, err := encodeDocument(MigrationExportDocument{
				SchemaVersion: v1.SchemaVersion,
				Kind:          migrationExportKind,
				Artifact:      artifact,
				Dir:           "/var/lib/firecracker/migrations/" + artifact.ID,
				Linked:        true,
			})
			require.NoError(t, err)
			w.Header().Set("Content-Type", publicMediaType)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(body)
		},
	})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	doc, err := mgr.Export(context.Background(), "node-1", v1.Task{
		ID: migrationTestTask, NodeID: "node-1", ServiceID: "svc-1",
	})
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/v1/tasks/"+migrationTestTask+"/migration/export", got.path)
	assert.Equal(t, "node=node-1&service=svc-1", got.query)
	assert.Equal(t, artifact, doc.Artifact)
}

// TestManagerExportRefusesAnotherNodesTask pins the wiring check: exporting
// through a node that is not the task's would answer 404 from a node that never
// saw the task, which reads like a missing task rather than a wrong node.
func TestManagerExportRefusesAnotherNodesTask(t *testing.T) {
	pki := newTestPKI(t)

	var called atomic.Bool
	src := &fakeTaskSource{task: Task{ID: migrationTestTask}, found: true}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		MigrationExportOperation: func(_ context.Context, _ Task, _ http.ResponseWriter, _ *http.Request) {
			called.Store(true)
		},
	})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	_, err := mgr.Export(context.Background(), "node-2", v1.Task{
		ID: migrationTestTask, NodeID: "node-1",
	})

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeInvalid, typed.Code)
	assert.Contains(t, typed.Message, "runs on node")
	assert.False(t, called.Load(), "a task on another node must not be exported through this one")
}

// TestManagerExportRefusesAnArtifactThatIsNotThisRequest pins the checks between
// decoding the node's answer and handing the artifact on. The manifest is the
// target's instructions for replacing this task in this service, so one describing
// another task, service or node is refused here - before anything is spooled -
// rather than being staged as this service's replacement.
func TestManagerExportRefusesAnArtifactThatIsNotThisRequest(t *testing.T) {
	pki := newTestPKI(t)
	artifact := migrationArtifactFixture(t, []byte("state"), migrationTestBody(64, 1), nil)

	cases := []struct {
		name string
		// kind overrides the document kind; empty is the export's own.
		kind string
		// mutate changes the artifact the node answers with.
		mutate func(*migration.Artifact)
		code   output.Code
		reason string
		field  string
		want   string
		got    string
		why    string
	}{
		{
			name:   "manifest does not validate",
			mutate: func(a *migration.Artifact) { a.TapName = "not-a-tap" },
			code:   output.CodeInternal,
			reason: "artifact_invalid",
			why:    "does not validate",
		},
		{
			name: "another source node",
			mutate: func(a *migration.Artifact) {
				a.SourceNodeID = "node-9"
			},
			code:   output.CodeInternal,
			reason: "artifact_mismatch",
			field:  "source node",
			want:   "node-1",
			got:    "node-9",
			why:    "names another source node",
		},
		{
			name: "another task",
			mutate: func(a *migration.Artifact) {
				// The requested task is the identity the manager's own request is
				// checked against, so an answer about another one is refused here
				// even though the manifest validates.
				a.RequestedTaskID = "task-2"
			},
			code:   output.CodeInternal,
			reason: "artifact_mismatch",
			field:  "task",
			want:   migrationTestTask,
			got:    "task-2",
			why:    "names another task",
		},
		{
			name: "another service",
			mutate: func(a *migration.Artifact) {
				a.ServiceID = "svc-2"
			},
			code:   output.CodeInternal,
			reason: "artifact_mismatch",
			field:  "service",
			want:   "svc-1",
			got:    "svc-2",
			why:    "names another service",
		},
		{
			name:   "another document kind",
			kind:   "SomethingElse",
			mutate: func(*migration.Artifact) {},
			code:   output.CodeInternal,
			why:    "unreadable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := MigrationExportDocument{
				SchemaVersion: v1.SchemaVersion,
				Kind:          migrationExportKind,
				Artifact:      artifact,
				Dir:           "/var/lib/firecracker/migrations/" + artifact.ID,
				Linked:        true,
			}
			tc.mutate(&doc.Artifact)
			if tc.kind != "" {
				doc.Kind = tc.kind
			}

			src := &fakeTaskSource{task: Task{ID: migrationTestTask}, found: true}
			_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
				MigrationExportOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
					body, err := encodeDocument(doc)
					require.NoError(t, err)
					w.Header().Set("Content-Type", publicMediaType)
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write(body)
				},
			})

			mgr := newMigrationTestManager(t, pki, baseURL, 0)
			got, err := mgr.Export(context.Background(), "node-1", v1.Task{
				ID: migrationTestTask, NodeID: "node-1", ServiceID: "svc-1",
			})

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, tc.code, typed.Code, typed.Message)
			assert.Equal(t, "node-1", typed.Details["nodeId"])
			assert.Contains(t, typed.Message, tc.why)

			if tc.reason != "" {
				assert.Equal(t, tc.reason, typed.Details["reason"], typed.Message)
			}
			if tc.field != "" {
				// A mismatch names both sides: the refusal is only actionable if
				// it says which two values disagree.
				assert.Equal(t, tc.field, typed.Details["field"])
				assert.Equal(t, tc.want, typed.Details["want"])
				assert.Equal(t, tc.got, typed.Details["got"])
				assert.Contains(t, typed.Message, tc.want, "the error names what was asked for")
				assert.Contains(t, typed.Message, tc.got, "and what the node answered")
			}
			assert.Equal(t, MigrationExportDocument{}, got,
				"a refused export hands the caller nothing to spool")
		})
	}
}

// TestManagerExportAcceptsTheSecondHopOfAChain pins the two identities apart. A
// task this node restored from an earlier migration runs a VM whose Firecracker
// state records the *source* task's TAP device, vsock muxer and disk, so the
// artifact it answers with names that source task while the export was requested
// about this node's task. Refusing that would fail every second hop (A→B, then
// B→C); the manager therefore checks the requested task, not the VM's identity.
func TestManagerExportAcceptsTheSecondHopOfAChain(t *testing.T) {
	pki := newTestPKI(t)
	artifact := migrationArtifactFixture(t, []byte("state"), migrationTestBody(64, 1), nil)

	// The VM this node restored: its devices are task-0's, and the task SwarmKit
	// started for it is migrationTestTask.
	const sourceVM = "task-0"
	artifact.SourceTaskID = sourceVM
	artifact.TapName = scruntime.TapName(sourceVM, 0)
	artifact.VsockPath = "/var/run/firecracker/" + sourceVM + ".vsock"
	artifact.DiskPath = "/var/lib/firecracker/rootfs/tasks/" + sourceVM + ".ext4"
	require.NoError(t, artifact.Validate(), "a re-export describes its own VM consistently")

	_, baseURL := startTestBroker(t, pki,
		&fakeTaskSource{task: Task{ID: migrationTestTask}, found: true},
		map[string]Handler{
			MigrationExportOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
				body, err := encodeDocument(MigrationExportDocument{
					SchemaVersion: v1.SchemaVersion,
					Kind:          migrationExportKind,
					Artifact:      artifact,
					Dir:           "/var/lib/firecracker/migrations/" + artifact.ID,
					Linked:        true,
				})
				require.NoError(t, err)
				w.Header().Set("Content-Type", publicMediaType)
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(body)
			},
		})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	doc, err := mgr.Export(context.Background(), "node-1", v1.Task{
		ID: migrationTestTask, NodeID: "node-1", ServiceID: "svc-1",
	})
	require.NoError(t, err, "the second hop of a chain is not another task's artifact")
	assert.Equal(t, migrationTestTask, doc.Artifact.RequestedTaskID,
		"the artifact answers the task the export named")
	assert.Equal(t, sourceVM, doc.Artifact.SourceTaskID,
		"the VM's identity still travels, because the target recreates its devices")
}

// TestManagerFileStreamsTheArtifactFile pins the happy path: a GET on the file
// operation, addressed to the node that holds the artifact, with the artifact and
// file named in the query, streamed into the caller's writer.
func TestManagerFileStreamsTheArtifactFile(t *testing.T) {
	pki := newTestPKI(t)
	body := migrationTestBody(1<<20, 2)
	artifact := migrationArtifactFixture(t, []byte("state"), body, nil)
	entry := migrationTestEntry(t, artifact, "vm.mem")

	var gotPath, gotQuery string
	src := &fakeTaskSource{}
	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		MigrationFileOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
			w.Header().Set("Content-Type", octetStream)
			w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
			w.Header().Set(checksumHeader, entry.SHA256)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		},
	})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	var got bytes.Buffer
	n, err := mgr.File(context.Background(), "node-1", v1.Task{ID: migrationTestTask}, artifact, "vm.mem", &got)
	require.NoError(t, err)

	assert.Equal(t, entry.Size, n)
	assert.Equal(t, body, got.Bytes())
	assert.Equal(t, "/v1/tasks/"+migrationTestTask+"/migration/file", gotPath)
	assert.Equal(t, "artifactId="+artifact.ID+"&name=vm.mem", gotQuery)
}

// TestManagerFileRefusesWhatIsNotTheArtifactFile pins the checks made before a
// stream is read: a name the manifest does not describe, and a node that announces
// a length or digest the manifest does not.
func TestManagerFileRefusesWhatIsNotTheArtifactFile(t *testing.T) {
	pki := newTestPKI(t)
	body := migrationTestBody(256, 3)
	artifact := migrationArtifactFixture(t, []byte("state"), body, nil)
	entry := migrationTestEntry(t, artifact, "vm.mem")

	t.Run("unknown name", func(t *testing.T) {
		var called atomic.Bool
		_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
			MigrationFileOperation: func(_ context.Context, _ Task, _ http.ResponseWriter, _ *http.Request) {
				called.Store(true)
			},
		})

		mgr := newMigrationTestManager(t, pki, baseURL, 0)
		_, err := mgr.File(context.Background(), "node-1", v1.Task{ID: migrationTestTask}, artifact, "rootfs.ext4", io.Discard)

		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeInvalid, typed.Code)
		assert.Equal(t, "unknown_artifact_file", typed.Details["reason"])
		assert.False(t, called.Load(), "a name the manifest does not describe is refused before any request")
	})

	cases := []struct {
		name          string
		status        int
		length        int64 // negative: chunked, no declared length
		checksum      string
		body          []byte
		wantCode      output.Code
		wantReason    string
		wantMessageIn string
		wantInDst     int64 // -1: the answer is refused before anything is written
	}{
		{
			name:          "the node promises another digest",
			status:        http.StatusOK,
			length:        entry.Size,
			checksum:      migrationTestFile("vm.mem", migrationTestBody(256, 4)).SHA256,
			body:          body,
			wantCode:      output.CodeConflict,
			wantReason:    "checksum_mismatch",
			wantMessageIn: "serves file",
			wantInDst:     0,
		},
		{
			name:          "the node promises another length",
			status:        http.StatusOK,
			length:        entry.Size - 1,
			checksum:      entry.SHA256,
			body:          body,
			wantCode:      output.CodeConflict,
			wantReason:    "size_mismatch",
			wantMessageIn: "the artifact describes",
			wantInDst:     0,
		},
		{
			name:          "the body is short",
			status:        http.StatusOK,
			length:        -1,
			checksum:      entry.SHA256,
			body:          body[:len(body)-1],
			wantCode:      output.CodeConflict,
			wantReason:    "size_mismatch",
			wantMessageIn: "arrived as",
			wantInDst:     entry.Size - 1,
		},
		{
			name:          "the body is long",
			status:        http.StatusOK,
			length:        -1,
			checksum:      entry.SHA256,
			body:          append(append([]byte{}, body...), 0xff),
			wantCode:      output.CodeConflict,
			wantReason:    "size_mismatch",
			wantMessageIn: "streamed past",
			// The extra byte is read and dropped: the caller's writer is never
			// handed more than the file the artifact describes.
			wantInDst: entry.Size,
		},
		{
			name:          "the body is not the digest",
			status:        http.StatusOK,
			length:        entry.Size,
			checksum:      entry.SHA256,
			body:          migrationTestBody(256, 5),
			wantCode:      output.CodeConflict,
			wantReason:    "checksum_mismatch",
			wantMessageIn: "arrived with SHA-256",
			wantInDst:     entry.Size,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
				MigrationFileOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
					if tc.length >= 0 {
						w.Header().Set("Content-Length", strconv.FormatInt(tc.length, 10))
					}
					if tc.checksum != "" {
						w.Header().Set(checksumHeader, tc.checksum)
					}
					w.WriteHeader(tc.status)
					if tc.length < 0 {
						// Flush the head before the body, so the answer is chunked
						// and its length is unknown: the case where only the bytes
						// themselves can reveal a mismatch.
						w.(http.Flusher).Flush()
					}
					_, _ = w.Write(tc.body)
				},
			})

			mgr := newMigrationTestManager(t, pki, baseURL, 0)
			var got bytes.Buffer
			_, err := mgr.File(context.Background(), "node-1", v1.Task{ID: migrationTestTask}, artifact, "vm.mem", &got)

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, tc.wantCode, typed.Code)
			assert.Equal(t, tc.wantReason, typed.Details["reason"], typed.Message)
			assert.Equal(t, "node-1", typed.Details["nodeId"])
			assert.Contains(t, typed.Message, tc.wantMessageIn)
			assert.Equal(t, tc.wantInDst, int64(got.Len()),
				"the destination receives exactly the bytes the answer is allowed to carry")
		})
	}
}

// TestManagerMigrationCallsRelayTypedNodeErrors pins the error contract on all
// four calls: a node's typed document keeps its code, message, retry hint and
// details, and a body that is not one becomes the stable unavailable answer.
func TestManagerMigrationCallsRelayTypedNodeErrors(t *testing.T) {
	pki := newTestPKI(t)
	artifact := migrationArtifactFixture(t, []byte("state"), migrationTestBody(64, 6), nil)
	entry := migrationTestEntry(t, artifact, "vm.mem")

	reads := map[string]func(*Manager) error{
		"export": func(mgr *Manager) error {
			_, err := mgr.Export(context.Background(), "node-1", v1.Task{ID: migrationTestTask, NodeID: "node-1"})
			return err
		},
		"file": func(mgr *Manager) error {
			_, err := mgr.File(context.Background(), "node-1", v1.Task{ID: migrationTestTask}, artifact, "vm.mem", io.Discard)
			return err
		},
		"stage": func(mgr *Manager) error {
			return mgr.Stage(context.Background(), "node-2", v1.Task{ID: "placeholder"},
				artifact, "vm.mem", entry.Size, entry.SHA256, bytes.NewReader(migrationTestBody(64, 6)))
		},
		"discard": func(mgr *Manager) error {
			return mgr.Discard(context.Background(), "node-1", v1.Task{ID: migrationTestTask}, artifact)
		},
	}

	cases := []struct {
		name        string
		status      int
		code        output.Code
		message     string
		document    bool
		wantCode    output.Code
		wantMessage string
	}{
		{
			name:        "a typed not found",
			status:      http.StatusNotFound,
			code:        output.CodeNotFound,
			message:     "the artifact is not available on this node",
			document:    true,
			wantCode:    output.CodeNotFound,
			wantMessage: "the artifact is not available on this node",
		},
		{
			name:        "a typed conflict",
			status:      http.StatusConflict,
			code:        output.CodeConflict,
			message:     "the staged file's SHA-256 differs",
			document:    true,
			wantCode:    output.CodeConflict,
			wantMessage: "the staged file's SHA-256 differs",
		},
		{
			name:        "a typed unavailable",
			status:      http.StatusServiceUnavailable,
			code:        output.CodeUnavailable,
			message:     "the artifact could not be read on this node",
			document:    true,
			wantCode:    output.CodeUnavailable,
			wantMessage: "the artifact could not be read on this node",
		},
		{
			name:        "a body that is not a typed error",
			status:      http.StatusInternalServerError,
			document:    false,
			wantCode:    codeUnavailable,
			wantMessage: "unavailable",
		},
	}

	for noun, call := range reads {
		for _, tc := range cases {
			t.Run(noun+" "+tc.name, func(t *testing.T) {
				reply := func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
					if tc.document {
						writeNodeError(t, w, tc.status, tc.code, tc.message)
						return
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, "boom")
				}
				_, baseURL := startTestBroker(t, pki, &fakeTaskSource{task: Task{ID: migrationTestTask}, found: true},
					map[string]Handler{
						MigrationExportOperation:  reply,
						MigrationFileOperation:    reply,
						MigrationStageOperation:   reply,
						MigrationDiscardOperation: reply,
					})

				err := call(newMigrationTestManager(t, pki, baseURL, 0))

				var typed *output.Error
				require.ErrorAs(t, err, &typed)
				assert.Equal(t, tc.wantCode, typed.Code, typed.Message)
				assert.Contains(t, typed.Message, tc.wantMessage)
				if tc.document {
					assert.Equal(t, tc.message, typed.Message, "a typed error crosses the relay verbatim")
				}
			})
		}
	}
}

// TestManagerStageSendsTheArtifactAndTheManifestQuery pins the upload: a PUT on
// the stage operation addressed to the target node, the file named in the query
// with its declared size and digest, and the bytes streamed as the body under the
// length the manifest gives.
func TestManagerStageSendsTheArtifactAndTheManifestQuery(t *testing.T) {
	pki := newTestPKI(t)
	body := migrationTestBody(4096, 7)
	artifact := migrationArtifactFixture(t, []byte("state"), body, nil)
	entry := migrationTestEntry(t, artifact, "vm.mem")

	type observed struct {
		method        string
		path          string
		query         string
		contentType   string
		contentLength int64
		digest        string
		checksum      string
	}
	var got observed

	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationStageOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			digest := sha256.New()
			n, err := io.Copy(digest, r.Body)
			require.NoError(t, err)
			got = observed{
				method:        r.Method,
				path:          r.URL.Path,
				query:         r.URL.RawQuery,
				contentType:   r.Header.Get("Content-Type"),
				contentLength: n,
				digest:        hex.EncodeToString(digest.Sum(nil)),
				checksum:      r.Header.Get(checksumHeader),
			}
			w.Header().Set(checksumHeader, r.URL.Query().Get("sha256"))
			w.WriteHeader(http.StatusNoContent)
		},
	})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	// The placeholder task is not the source task: the target has never run it.
	require.NoError(t, mgr.Stage(context.Background(), "node-2", v1.Task{ID: "next-task"},
		artifact, "vm.mem", entry.Size, entry.SHA256, bytes.NewReader(body)))

	assert.Equal(t, http.MethodPut, got.method)
	assert.Equal(t, "/v1/tasks/next-task/migration/stage", got.path)
	assert.Equal(t, entry.Size, got.contentLength)
	assert.Equal(t, entry.SHA256, got.digest)
	assert.Equal(t, octetStream, got.contentType)
	assert.Empty(t, got.checksum, "the manager sends the digest in the query, not a header")

	// The query is compared as a set: url.Values.Encode sorts keys, but pinning the
	// whole string would make a new parameter look like a broken stage.
	query := got.query
	for _, want := range []string{"artifactId=" + artifact.ID, "name=vm.mem",
		"size=" + strconv.FormatInt(entry.Size, 10), "sha256=" + entry.SHA256} {
		assert.Contains(t, query, want)
	}
}

// TestManagerChecksumsAreCaseInsensitive pins the digest comparison: a hex digest
// is the same value in either case, and a node that announces an upper-case one is
// describing the file the manifest does, not another file.
func TestManagerChecksumsAreCaseInsensitive(t *testing.T) {
	pki := newTestPKI(t)
	body := migrationTestBody(1024, 18)
	artifact := migrationArtifactFixture(t, []byte("state"), body, nil)
	entry := migrationTestEntry(t, artifact, "vm.mem")
	upper := strings.ToUpper(entry.SHA256)
	require.NotEqual(t, entry.SHA256, upper)

	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationFileOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
			w.Header().Set(checksumHeader, upper)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		},
		MigrationStageOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			// The node echoes the digest it verified, in the case it prefers.
			w.Header().Set(checksumHeader, upper)
			w.WriteHeader(http.StatusNoContent)
		},
	})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)

	var got bytes.Buffer
	n, err := mgr.File(context.Background(), "node-1", v1.Task{ID: migrationTestTask}, artifact, "vm.mem", &got)
	require.NoError(t, err, "an upper-case announcement is the same digest")
	assert.Equal(t, entry.Size, n)
	assert.Equal(t, body, got.Bytes())

	require.NoError(t, mgr.Stage(context.Background(), "node-2", v1.Task{ID: "next-task"},
		artifact, "vm.mem", entry.Size, entry.SHA256, bytes.NewReader(body)),
		"an upper-case echo is the digest the stage sent")
}

// sourceFailureReader serves its prefix and then fails: the reader a Stage is
// handed when the hop between the spool and the wire breaks after the transfer has
// started.
type sourceFailureReader struct {
	prefix []byte
	err    error
}

func (r *sourceFailureReader) Read(p []byte) (int, error) {
	if len(r.prefix) > 0 {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	return 0, r.err
}

// TestManagerStageReportsItsOwnSourceFailure pins who is blamed when a stage dies
// while reading its own bytes: the reader, not the target node. The spool checks a
// file's digest at EOF and a disk can refuse a read, and neither is a node that
// stopped answering - so the failure keeps the spool's own classification when it
// has one, and never becomes "node unavailable".
func TestManagerStageReportsItsOwnSourceFailure(t *testing.T) {
	pki := newTestPKI(t)
	body := migrationTestBody(256, 19)
	artifact := migrationArtifactFixture(t, []byte("state"), body, nil)
	entry := migrationTestEntry(t, artifact, "vm.mem")

	// The error the spool's reader returns at EOF when the bytes are not the
	// digest the manifest describes.
	spoolErr := output.NewErrorf(output.CodeConflict, "spool file %q has SHA-256 %s: the artifact declares %s",
		"vm.mem", strings.Repeat("0", 64), entry.SHA256).
		WithDetails(map[string]any{"reason": "checksum_mismatch", "name": "vm.mem"})

	// An error no layer classified: the message names a path the manager keeps to
	// itself, so it must not be relayed.
	rawErr := errors.New("read /var/lib/swarmcracker/migration-spool/mig-0123456789abcdef/vm.mem: input/output error")

	cases := []struct {
		name     string
		err      error
		code     output.Code
		reason   string
		wantSame bool
	}{
		{
			name:     "the spool refuses the file at EOF",
			err:      spoolErr,
			code:     output.CodeConflict,
			reason:   "checksum_mismatch",
			wantSame: true,
		},
		{
			name:   "an unclassified read error",
			err:    rawErr,
			code:   output.CodeInternal,
			reason: "source_unreadable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
				MigrationStageOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(http.StatusNoContent)
				},
			})

			mgr := newMigrationTestManager(t, pki, baseURL, 0)
			reader := &sourceFailureReader{prefix: body[:len(body)-1], err: tc.err}
			err := mgr.Stage(context.Background(), "node-2", v1.Task{ID: "next-task"},
				artifact, "vm.mem", entry.Size, entry.SHA256, reader)

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, tc.code, typed.Code, typed.Message)
			assert.Equal(t, tc.reason, typed.Details["reason"], typed.Message)

			// The point of the ruling: a source that failed is never reported as
			// the target node being unavailable.
			assert.NotEqual(t, codeUnavailable, typed.Code, "a failed source is not a node that is down")
			assert.NotEqual(t, output.CodeTimeout, typed.Code)
			assert.NotContains(t, typed.Message, "migration-spool", "the manager's paths stay off the wire")
			assert.NotContains(t, typed.Message, "/var/lib")
			assert.Equal(t, "vm.mem", typed.Details["name"])

			if tc.wantSame {
				assert.Same(t, spoolErr, typed, "a spool error keeps its own classification")
				assert.Nil(t, typed.Details["nodeId"],
					"a relayed classification is not rewritten with manager-side detail")
			} else {
				assert.Equal(t, "node-2", typed.Details["nodeId"],
					"the node is named for correlation, not blamed")
			}
		})
	}
}

// TestManagerStageRefusesParametersTheManifestDoesNotDescribe pins the redundant
// pair: size and digest are passed explicitly because they are the node's query,
// and they are checked against the manifest rather than trusted, so a caller
// cannot stage bytes under a description the artifact contradicts.
func TestManagerStageRefusesParametersTheManifestDoesNotDescribe(t *testing.T) {
	pki := newTestPKI(t)
	artifact := migrationArtifactFixture(t, []byte("state"), migrationTestBody(64, 8), nil)
	entry := migrationTestEntry(t, artifact, "vm.mem")

	cases := []struct {
		name string
		size int64
		sha  string
	}{
		{name: "another size", size: entry.Size - 1, sha: entry.SHA256},
		{name: "another digest", size: entry.Size, sha: migrationTestFile("vm.mem", []byte("other")).SHA256},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var called atomic.Bool
			_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
				MigrationStageOperation: func(_ context.Context, _ Task, _ http.ResponseWriter, _ *http.Request) {
					called.Store(true)
				},
			})

			mgr := newMigrationTestManager(t, pki, baseURL, 0)
			err := mgr.Stage(context.Background(), "node-2", v1.Task{ID: "next-task"},
				artifact, "vm.mem", tc.size, tc.sha, bytes.NewReader(migrationTestBody(64, 8)))

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, output.CodeInvalid, typed.Code)
			assert.False(t, called.Load(), "a contradictory description is refused before any request")
		})
	}
}

// TestManagerDiscardIsIdempotent pins the one call a success path, a rollback and
// an abandoned migration all make: it names the artifact, addresses the node that
// holds it, and answers nil for a 204 whether or not there was anything left to
// remove.
func TestManagerDiscardIsIdempotent(t *testing.T) {
	pki := newTestPKI(t)
	artifact := migrationArtifactFixture(t, []byte("state"), migrationTestBody(64, 9), nil)

	var calls atomic.Int32
	var gotPath, gotQuery, gotMethod string
	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationDiscardOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			gotPath, gotQuery, gotMethod = r.URL.Path, r.URL.RawQuery, r.Method
			w.WriteHeader(http.StatusNoContent)
		},
	})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	require.NoError(t, mgr.Discard(context.Background(), "node-2", v1.Task{ID: "next-task"}, artifact))
	require.NoError(t, mgr.Discard(context.Background(), "node-2", v1.Task{ID: "next-task"}, artifact))

	assert.Equal(t, int32(2), calls.Load())
	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/v1/tasks/next-task/migration/discard", gotPath)
	assert.Equal(t, "artifactId="+artifact.ID, gotQuery)
}

// TestManagerDiscardRefusesAMalformedID pins the only local check Discard makes:
// the ID addresses a directory on the node, so a malformed one never becomes a
// request.
func TestManagerDiscardRefusesAMalformedID(t *testing.T) {
	pki := newTestPKI(t)

	var called atomic.Bool
	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationDiscardOperation: func(_ context.Context, _ Task, _ http.ResponseWriter, _ *http.Request) {
			called.Store(true)
		},
	})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	err := mgr.Discard(context.Background(), "node-1", v1.Task{ID: migrationTestTask},
		migration.Artifact{ID: "../../etc"})

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeInvalid, typed.Code)
	assert.False(t, called.Load())
}

// TestManagerRouteForNodeAddressesANodeWithoutATask pins the seam the addendum
// asks for: a node and a placeholder task alone produce the one route shape every
// node operation has, so the target of a migration can be addressed before
// SwarmKit has created the task that will restore the artifact there.
func TestManagerRouteForNodeAddressesANodeWithoutATask(t *testing.T) {
	pki := newTestPKI(t)
	mgr := newMigrationTestManager(t, pki, "https://127.0.0.1:4245", 0)

	route, err := mgr.routeForNode(context.Background(), "node-2", "next-task", MigrationStageOperation)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:4245", route.URL.Host)
	assert.Equal(t, "/v1/tasks/next-task/migration/stage", route.URL.Path)
	assert.Equal(t, "node-2", route.Node.ID)
	assert.Empty(t, route.Task.ID, "a node-scoped route resolves no task")

	_, err = mgr.routeForNode(context.Background(), "node-2", "", MigrationStageOperation)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeInvalid, typed.Code)

	_, err = mgr.routeForNode(context.Background(), "", "next-task", MigrationStageOperation)
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeInvalid, typed.Code)

	_, err = mgr.routeForNode(context.Background(), "node-3", "next-task", MigrationStageOperation)
	require.Error(t, err)

	_, err = mgr.routeForNode(context.Background(), "node-2", "next-task", "Migration/Stage")
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeInvalid, typed.Code)
	assert.Contains(t, typed.Message, "unsupported broker operation")
}

// TestManagerMigrationCallsTimeOut pins the deadline every relay call inherits: a
// node that stops answering must not hold the manager's migration open forever. It
// is the migration deadline, not the snapshot one: a transfer is bounded by the
// link, and one number for both would cut a large artifact off.
func TestManagerMigrationCallsTimeOut(t *testing.T) {
	pki := newTestPKI(t)
	artifact := migrationArtifactFixture(t, []byte("state"), migrationTestBody(64, 10), nil)
	entry := migrationTestEntry(t, artifact, "vm.mem")

	stall := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}

	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{task: Task{ID: migrationTestTask}, found: true},
		map[string]Handler{
			MigrationExportOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) { stall(w, r) },
			MigrationFileOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
				w.WriteHeader(http.StatusOK)
				stall(w, r)
			},
			MigrationStageOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				stall(w, r)
			},
		})

	host, port := brokerTarget(t, baseURL)
	mgr, err := NewManager(ManagerConfig{
		Tasks: &stubTaskReader{task: v1.Task{ID: migrationTestTask, NodeID: "node-1", ServiceID: "svc-1"}},
		Nodes: &migrationNodeReader{nodes: map[string]v1.Node{
			"node-1": {ID: "node-1", Address: host},
		}},
		Port:             port,
		Client:           pki.httpClient(),
		MigrationTimeout: 30 * time.Millisecond,
		// The snapshot deadline is left at its default: a timeout here can only
		// come from the migration one.
		SnapshotTimeout: time.Minute,
	})
	require.NoError(t, err)

	_, err = mgr.Export(context.Background(), "node-1", v1.Task{ID: migrationTestTask, NodeID: "node-1"})
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeTimeout, typed.Code)

	_, err = mgr.File(context.Background(), "node-1", v1.Task{ID: migrationTestTask}, artifact, "vm.mem", io.Discard)
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeTimeout, typed.Code)

	// A stage whose own reader behaved keeps the transport's answer: the wrapper
	// that blames a failed source must not swallow a node that stopped answering.
	err = mgr.Stage(context.Background(), "node-1", v1.Task{ID: "next-task"},
		artifact, "vm.mem", entry.Size, entry.SHA256, bytes.NewReader(migrationTestBody(64, 10)))
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeTimeout, typed.Code, "a stalled node is still a timeout")
}

// TestManagerMigrationTimeoutHasItsOwnDefault pins the two deadlines: a Manager
// built without them gets both, and the migration one is the wider of the two
// because it bounds a transfer rather than a disk copy.
func TestManagerMigrationTimeoutHasItsOwnDefault(t *testing.T) {
	pki := newTestPKI(t)
	mgr := newMigrationTestManager(t, pki, "https://127.0.0.1:4245", 0)

	assert.Equal(t, DefaultMigrationTimeout, mgr.migrationTimeout)
	assert.Equal(t, 30*time.Minute, mgr.migrationTimeout)
	assert.Equal(t, DefaultSnapshotTimeout, mgr.snapshotTimeout)
	assert.Greater(t, mgr.migrationTimeout, mgr.snapshotTimeout)

	overridden, err := NewManager(ManagerConfig{
		Tasks:            &stubTaskReader{},
		Nodes:            &migrationNodeReader{},
		Client:           pki.httpClient(),
		MigrationTimeout: time.Minute,
	})
	require.NoError(t, err)
	assert.Equal(t, time.Minute, overridden.migrationTimeout)
}

// migrationPatternReader emits size deterministic bytes without ever holding them:
// the source side of the streaming proof.
type migrationPatternReader struct {
	remaining int64
	at        int64
}

func (r *migrationPatternReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = byte((r.at + i) * 13)
	}
	r.at += n
	r.remaining -= n
	return int(n), nil
}

// migrationPatternDigest is the SHA-256 of the bytes migrationPatternReader emits.
func migrationPatternDigest(t *testing.T, size int64) string {
	t.Helper()

	digest := sha256.New()
	_, err := io.Copy(digest, &migrationPatternReader{remaining: size})
	require.NoError(t, err)
	return hex.EncodeToString(digest.Sum(nil))
}

// bufferProbe is the streaming proof's instrument: it records how large each Write
// is, and once the transfer is half done it collects the heap and records what is
// still live. A relay that buffered the file would show a single huge Write and a
// live heap the size of the file; a relay that streams shows neither.
type bufferProbe struct {
	dst io.Writer

	total     int64
	writes    int
	peakWrite int

	sampleAt int64
	sampled  bool
	baseline uint64
	peakHeap uint64
}

func (p *bufferProbe) Write(b []byte) (int, error) {
	if len(b) > p.peakWrite {
		p.peakWrite = len(b)
	}
	p.writes++
	p.total += int64(len(b))

	if !p.sampled && p.total >= p.sampleAt {
		p.sampled = true
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		p.peakHeap = stats.HeapAlloc
	}

	return p.dst.Write(b)
}

// TestManagerRelayStreamsTenMiBThroughTheSpool is the streaming proof: a 10 MiB
// artifact file goes source node -> manager spool -> target node with a fixed
// buffer. It asserts the bound rather than the outcome - every write into the
// spool is at most one relay chunk, the transfer is many writes rather than one,
// and the live heap halfway through is far below the file's size - and then
// asserts the bytes arrived intact by digest on both ends.
func TestManagerRelayStreamsTenMiBThroughTheSpool(t *testing.T) {
	pki := newTestPKI(t)

	const size = int64(10 << 20)
	digest := migrationPatternDigest(t, size)
	artifact := migrationArtifactFixture(t, []byte("state"), []byte("memory"), nil)
	artifact.MemoryFile = migration.File{Name: "vm.mem", Size: size, SHA256: digest}
	require.NoError(t, artifact.Validate())

	var received atomic.Int64
	var receivedDigest atomic.Value
	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationFileOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.Header().Set(checksumHeader, digest)
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, &migrationPatternReader{remaining: size})
		},
		MigrationStageOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			// The target hashes what it is handed, in its own fixed buffer: its
			// side of the proof is that it never allocates the file either.
			hasher := sha256.New()
			n, err := io.CopyBuffer(hasher, r.Body, make([]byte, 32<<10))
			if !assert.NoError(t, err) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			received.Store(n)
			receivedDigest.Store(hex.EncodeToString(hasher.Sum(nil)))
			w.Header().Set(checksumHeader, r.URL.Query().Get("sha256"))
			w.WriteHeader(http.StatusNoContent)
		},
	})

	store, err := migration.NewStore(migration.StoreConfig{
		Root: filepath.Join(t.TempDir(), migration.SpoolDirName),
	})
	require.NoError(t, err)

	mgr := newMigrationTestManager(t, pki, baseURL, 0)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	// Source node -> spool.
	writer, err := store.Writer(artifact.ID, artifact.MemoryFile)
	require.NoError(t, err)

	probe := &bufferProbe{dst: writer, sampleAt: size / 2, baseline: before.HeapAlloc}
	written, err := mgr.File(context.Background(), "node-1", v1.Task{ID: migrationTestTask},
		artifact, "vm.mem", probe)
	require.NoError(t, err)
	require.Equal(t, size, written)
	require.NoError(t, writer.Close())

	assert.Equal(t, size, probe.total)
	assert.LessOrEqual(t, probe.peakWrite, migrationRelayChunkBytes,
		"a relayed file crosses the manager in fixed-size pieces")
	assert.GreaterOrEqual(t, probe.writes, int(size/migrationRelayChunkBytes),
		"the transfer is many pieces, not one buffered write")
	require.True(t, probe.sampled)

	const heapSlack = 5 << 20
	if probe.peakHeap > probe.baseline {
		assert.Less(t, probe.peakHeap-probe.baseline, uint64(heapSlack),
			"a 10 MiB file must not be resident in the manager's heap")
	}

	// Spool -> target node.
	reader, err := store.Open(artifact.ID, artifact.MemoryFile)
	require.NoError(t, err)
	require.NoError(t, mgr.Stage(context.Background(), "node-2", v1.Task{ID: "next-task"},
		artifact, "vm.mem", artifact.MemoryFile.Size, artifact.MemoryFile.SHA256, reader))
	require.NoError(t, reader.Close())

	assert.Equal(t, size, received.Load())
	assert.Equal(t, digest, receivedDigest.Load())

	// The spool holds the one file while it is in flight, and nothing after.
	entries, err := os.ReadDir(filepath.Join(store.Root(), artifact.ID))
	require.NoError(t, err)
	require.Len(t, entries, 1)

	require.NoError(t, store.Remove(artifact.ID))
	require.NoError(t, store.Remove(artifact.ID))

	removed, err := store.Sweep()
	require.NoError(t, err)
	assert.Zero(t, removed, "a finished migration leaves nothing to sweep")
}

// TestManagerMigrationCallsRefuseWhatTheyCannotDo pins the local refusals and the
// answers that are not the success contract: a missing writer or reader, a stage
// that answers 2xx without the 204 the operation promises, an echoed digest that
// is not the one sent, and a discard that answers 2xx without deleting.
func TestManagerMigrationCallsRefuseWhatTheyCannotDo(t *testing.T) {
	pki := newTestPKI(t)
	artifact := migrationArtifactFixture(t, []byte("state"), migrationTestBody(64, 15), nil)
	entry := migrationTestEntry(t, artifact, "vm.mem")

	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationStageOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			if r.URL.Query().Get("name") == "vm.state" {
				w.Header().Set(checksumHeader, migrationTestFile("vm.state", []byte("other")).SHA256)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusOK)
		},
		MigrationDiscardOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	})

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	ctx := context.Background()

	cases := []struct {
		name    string
		call    func() error
		want    output.Code
		message string
	}{
		{
			name: "file without a writer",
			call: func() error {
				_, err := mgr.File(ctx, "node-1", v1.Task{ID: migrationTestTask}, artifact, "vm.mem", nil)
				return err
			},
			want:    output.CodeInternal,
			message: "destination writer is required",
		},
		{
			name: "a manifest entry that is not usable",
			call: func() error {
				broken := artifact
				broken.StateFile.Size = 0
				_, err := mgr.File(ctx, "node-1", v1.Task{ID: migrationTestTask}, broken, "vm.state", io.Discard)
				return err
			},
			want:    output.CodeInvalid,
			message: "is not usable",
		},
		{
			name: "stage without a reader",
			call: func() error {
				return mgr.Stage(ctx, "node-2", v1.Task{ID: "next-task"},
					artifact, "vm.mem", entry.Size, entry.SHA256, nil)
			},
			want:    output.CodeInternal,
			message: "source reader is required",
		},
		{
			name: "stage answers 2xx without the 204",
			call: func() error {
				return mgr.Stage(ctx, "node-2", v1.Task{ID: "next-task"},
					artifact, "vm.mem", entry.Size, entry.SHA256, bytes.NewReader(migrationTestBody(64, 16)))
			},
			want:    output.CodeInternal,
			message: "returned status 200",
		},
		{
			name: "stage echoes another digest",
			call: func() error {
				state := migrationTestEntry(t, artifact, "vm.state")
				return mgr.Stage(ctx, "node-2", v1.Task{ID: "next-task"},
					artifact, "vm.state", state.Size, state.SHA256, bytes.NewReader([]byte("state")))
			},
			want:    output.CodeConflict,
			message: "staged file",
		},
		{
			name: "discard answers 2xx without deleting",
			call: func() error {
				return mgr.Discard(ctx, "node-1", v1.Task{ID: migrationTestTask}, artifact)
			},
			want:    output.CodeInternal,
			message: "returned status 200",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, tc.want, typed.Code, typed.Message)
			assert.Contains(t, typed.Message, tc.message)
		})
	}
}

// TestManagerFileMapsAMidStreamFailure pins the two failures that only happen once
// the answer has started: a connection that drops, and a stream that stalls past
// the deadline. Both are the node's, both name no internal address, and neither is
// a mismatch of the bytes.
func TestManagerFileMapsAMidStreamFailure(t *testing.T) {
	pki := newTestPKI(t)
	body := migrationTestBody(1<<20, 18)
	artifact := migrationArtifactFixture(t, []byte("state"), body, nil)
	entry := migrationTestEntry(t, artifact, "vm.mem")

	startStream := func(w http.ResponseWriter) bool {
		w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
		w.Header().Set(checksumHeader, entry.SHA256)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, err := w.Write(body[:len(body)/2])
		return assert.NoError(t, err)
	}

	cases := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		want    output.Code
	}{
		{
			name: "the connection drops",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				if !startStream(w) {
					return
				}
				hijacker, ok := w.(http.Hijacker)
				if !assert.True(t, ok) {
					return
				}
				conn, _, err := hijacker.Hijack()
				if !assert.NoError(t, err) {
					return
				}
				_ = conn.Close()
			},
			want: output.CodeUnavailable,
		},
		{
			name: "the stream stalls past the deadline",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !startStream(w) {
					return
				}
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
			},
			want: output.CodeTimeout,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
				MigrationFileOperation: func(_ context.Context, _ Task, w http.ResponseWriter, r *http.Request) {
					tc.handler(w, r)
				},
			})

			mgr := newMigrationTestManager(t, pki, baseURL, 50*time.Millisecond)
			_, err := mgr.File(context.Background(), "node-1", v1.Task{ID: migrationTestTask},
				artifact, "vm.mem", io.Discard)

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, tc.want, typed.Code, typed.Message)
			assert.Equal(t, "node-1", typed.Details["nodeId"])
			assert.NotContains(t, typed.Message, "127.0.0.1", "the message must not expose the internal broker address")
		})
	}
}

// TestManagerRelaysThroughTheNodesOwnMigrationHandlers is the two-sided contract:
// the manager's four calls run against the node's real handlers, so the path, the
// query names, the headers, the status codes and the checks that guard each end
// are the ones the cluster actually uses. A mismatch between the two sides - a
// renamed parameter, a different media type, an assumed status - fails here rather
// than on a migration.
func TestManagerRelaysThroughTheNodesOwnMigrationHandlers(t *testing.T) {
	pki := newTestPKI(t)

	snapshotRoot := t.TempDir()
	store := &fakeSnapshotStore{}
	node := migrationNode{cfg: migrationConfig(t, store), snapshotRoot: snapshotRoot, store: store}
	store.createInfo = seedSnapshot(t, snapshotRoot)
	original := *store.createInfo

	_, baseURL := startTestBroker(t, pki,
		&fakeTaskSource{task: snapshotTask(), found: true},
		map[string]Handler{
			MigrationExportOperation:  NewMigrationExportHandler(node.cfg),
			MigrationFileOperation:    NewMigrationFileHandler(node.cfg),
			MigrationStageOperation:   NewMigrationStageHandler(node.cfg),
			MigrationDiscardOperation: NewMigrationDiscardHandler(node.cfg),
		})

	spool, err := migration.NewStore(migration.StoreConfig{
		Root: filepath.Join(t.TempDir(), migration.SpoolDirName),
	})
	require.NoError(t, err)

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	ctx := context.Background()
	sourceTask := v1.Task{ID: migrationTestTask, NodeID: "node-1", ServiceID: "svc-1"}

	doc, err := mgr.Export(ctx, "node-1", sourceTask)
	require.NoError(t, err)
	artifact := doc.Artifact
	require.Equal(t, migrationTestTask, artifact.SourceTaskID)
	require.Equal(t, "svc-1", artifact.ServiceID)
	require.NotEmpty(t, artifact.SnapshotID)

	files := map[string]string{
		artifact.StateFile.Name:  original.StatePath,
		artifact.MemoryFile.Name: original.MemoryPath,
		artifact.DiskFile.Name:   original.DiskPath,
	}

	for name, source := range files {
		entry := migrationTestEntry(t, artifact, name)

		// Source node -> spool.
		writer, err := spool.Writer(artifact.ID, entry)
		require.NoError(t, err)
		written, err := mgr.File(ctx, "node-1", sourceTask, artifact, name, writer)
		require.NoError(t, err, name)
		require.Equal(t, entry.Size, written, name)
		require.NoError(t, writer.Close(), name)

		want, readErr := os.ReadFile(source)
		require.NoError(t, readErr)

		reader, err := spool.Open(artifact.ID, entry)
		require.NoError(t, err)
		got, err := io.ReadAll(reader)
		require.NoError(t, err, name)
		require.NoError(t, reader.Close())
		require.Equal(t, want, got, "the spooled copy of %s is the node's bytes", name)

		// Spool -> target node. The target is the same daemon here, because a
		// Manager has one broker port; what is being checked is that the two
		// operations agree, not which node answers.
		require.NoError(t, mgr.Stage(ctx, "node-2", v1.Task{ID: "next-task"},
			artifact, name, entry.Size, entry.SHA256, bytes.NewReader(got)), name)

		require.NoError(t, spool.Remove(artifact.ID), "the spool holds one file at a time")
	}

	// The target now holds the artifact, and a second stage of the same bytes is
	// the retry a rollback makes: it must succeed.
	entry := migrationTestEntry(t, artifact, artifact.StateFile.Name)
	state, err := os.ReadFile(original.StatePath)
	require.NoError(t, err)
	require.NoError(t, mgr.Stage(ctx, "node-2", v1.Task{ID: "next-task"},
		artifact, entry.Name, entry.Size, entry.SHA256, bytes.NewReader(state)))

	// Discard takes the artifact and the dump behind it, and is idempotent.
	require.NoError(t, mgr.Discard(ctx, "node-1", sourceTask, artifact))
	require.NoError(t, mgr.Discard(ctx, "node-1", sourceTask, artifact))

	assert.Contains(t, store.deleted, original.ID, "discard deletes the snapshot the artifact names")
	_, err = os.Stat(filepath.Join(node.cfg.ArtifactDir, artifact.ID))
	assert.True(t, errors.Is(err, os.ErrNotExist), "discard removes the artifact directory")
}

// TestManagerRelayRemovesTheSpoolEntryOnASourceMismatch pins the failure path of
// the composition: when the source serves bytes the manifest does not describe,
// the spool writer is aborted and the manager is left holding nothing - not a
// partial file and not an empty directory a restore could read.
func TestManagerRelayRemovesTheSpoolEntryOnASourceMismatch(t *testing.T) {
	pki := newTestPKI(t)
	body := migrationTestBody(4096, 11)
	artifact := migrationArtifactFixture(t, []byte("state"), body, nil)
	entry := migrationTestEntry(t, artifact, "vm.mem")

	_, baseURL := startTestBroker(t, pki, &fakeTaskSource{}, map[string]Handler{
		MigrationFileOperation: func(_ context.Context, _ Task, w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
			w.Header().Set(checksumHeader, entry.SHA256)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(migrationTestBody(4096, 12))
		},
	})

	store, err := migration.NewStore(migration.StoreConfig{
		Root: filepath.Join(t.TempDir(), migration.SpoolDirName),
	})
	require.NoError(t, err)

	mgr := newMigrationTestManager(t, pki, baseURL, 0)
	writer, err := store.Writer(artifact.ID, entry)
	require.NoError(t, err)

	_, err = mgr.File(context.Background(), "node-1", v1.Task{ID: migrationTestTask}, artifact, "vm.mem", writer)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeConflict, typed.Code)

	// The caller's undo: the bytes in the spool are not the ones the artifact
	// describes, so they are discarded rather than closed.
	require.NoError(t, writer.Abort())

	_, err = os.Stat(filepath.Join(store.Root(), artifact.ID))
	assert.True(t, errors.Is(err, os.ErrNotExist), "a failed fetch leaves no spool entry")
}
