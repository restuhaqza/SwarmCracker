package main

// The tests in this file drive the whole migration flow against fakes, and the
// spool for real: the manager's side of the move is a sequence of calls and a
// hand-off of bytes, so the two things worth pinning are the order of the calls
// (which hop happened before which) and what every failure leaves behind.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/broker"
	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// migrationTestArtifactID is a well-formed artifact ID.
const migrationTestArtifactID = "mig-0123456789abcdef"

// migrationTestStart is the instant the happy path's clock starts at. A scripted
// replacement has to be created at or after the migration started, so the two come
// from the same installed fiction rather than from the wall clock.
var migrationTestStart = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

// migrationCallLog is one ordered log of both fakes, because the point of the
// flow is the order across them: export before file, file before stage, stage
// before prepare, update after prepare.
type migrationCallLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *migrationCallLog) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, fmt.Sprintf(format, args...))
}

func (l *migrationCallLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// migrationUpdate is one recorded service update: the patch and the precondition,
// so the test can assert the pinned constraint and the revision it was pinned
// against.
type migrationUpdate struct {
	ref         string
	constraints []string
	ifMatch     string
}

// fakeMigrationClient is the control plane the coordinator reads and writes.
type fakeMigrationClient struct {
	log *migrationCallLog

	mu       sync.Mutex
	task     v1.Task
	service  v1.Service
	node     v1.Node
	nodes    []v1.Node
	nodesErr error

	taskErr    error
	serviceErr error
	nodeErr    error
	tasksErr   error
	updateErr  error

	// taskSteps scripts ListServiceTasks: each call consumes one entry, and the
	// last entry repeats. Nil serves serviceTasks unchanged.
	taskSteps [][]v1.Task
	stepIndex int

	updates []migrationUpdate
	// updateErrs is a queue of scripted results: each service update consumes one
	// entry, falling back to updateErr when the queue is empty. A scripted error
	// means the update was refused: nothing is written.
	updateErrs []error
	// updateCommitErrs is the queue for an update whose answer was lost: the patch
	// is applied and the revision incremented, and then the error is returned. It is
	// what a committed-but-unacknowledged UpdateService looks like from here.
	updateCommitErrs []error
	// revision is the fake's service revision; an update increments it.
	revision int
}

func (f *fakeMigrationClient) GetTask(_ context.Context, ref string) (v1.Task, error) {
	f.log.add("task:%s", ref)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.taskErr != nil {
		return v1.Task{}, f.taskErr
	}
	return f.task, nil
}

func (f *fakeMigrationClient) GetService(_ context.Context, ref string) (v1.Service, error) {
	f.log.add("service:%s", ref)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.serviceErr != nil {
		return v1.Service{}, f.serviceErr
	}
	return f.snapshotService(), nil
}

func (f *fakeMigrationClient) ListServiceTasks(_ context.Context, ref string) ([]v1.Task, error) {
	f.log.add("tasks:%s", ref)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tasksErr != nil {
		return nil, f.tasksErr
	}
	if len(f.taskSteps) == 0 {
		return nil, nil
	}
	index := f.stepIndex
	if index >= len(f.taskSteps) {
		index = len(f.taskSteps) - 1
	}
	f.stepIndex++
	return f.taskSteps[index], nil
}

func (f *fakeMigrationClient) GetNode(_ context.Context, ref string) (v1.Node, error) {
	f.log.add("node:%s", ref)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nodeErr != nil {
		return v1.Node{}, f.nodeErr
	}
	return f.node, nil
}

func (f *fakeMigrationClient) ListNodes(_ context.Context) ([]v1.Node, error) {
	f.log.add("nodes")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nodesErr != nil {
		return nil, f.nodesErr
	}
	return append([]v1.Node(nil), f.nodes...), nil
}

func (f *fakeMigrationClient) UpdateService(_ context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Service, error) {
	f.log.add("update:%s", ref)
	f.mu.Lock()
	defer f.mu.Unlock()

	constraints, err := placementConstraintsInPatch(patch)
	if err != nil {
		return v1.Service{}, err
	}
	f.updates = append(f.updates, migrationUpdate{ref: ref, constraints: constraints, ifMatch: opts.IfMatch})

	err = f.updateErr
	if len(f.updateErrs) > 0 {
		err = f.updateErrs[0]
		f.updateErrs = f.updateErrs[1:]
	}
	if err != nil {
		return v1.Service{}, err
	}

	if len(constraints) == 0 {
		f.service.Spec.Placement = nil
	} else {
		f.service.Spec.Placement = &v1.Placement{Constraints: append([]string(nil), constraints...)}
	}
	f.revision++
	f.service.Revision = strconv.Itoa(f.revision)

	// The update is committed; only the answer is lost.
	if len(f.updateCommitErrs) > 0 {
		commitErr := f.updateCommitErrs[0]
		f.updateCommitErrs = f.updateCommitErrs[1:]
		if commitErr != nil {
			return v1.Service{}, commitErr
		}
	}
	return f.snapshotService(), nil
}

// snapshotService copies the service so a caller cannot mutate the fake's state.
func (f *fakeMigrationClient) snapshotService() v1.Service {
	service := f.service
	if service.Spec.Placement != nil {
		placement := *service.Spec.Placement
		placement.Constraints = append([]string(nil), service.Spec.Placement.Constraints...)
		service.Spec.Placement = &placement
	}
	return service
}

// placementConstraintsInPatch reads the constraints a merge patch names. It is
// deliberately strict: the test fails on a patch that does not carry the one
// subtree the flow is allowed to write.
func placementConstraintsInPatch(patch []byte) ([]string, error) {
	var decoded struct {
		Spec struct {
			Placement *struct {
				Constraints []string `json:"constraints"`
			} `json:"placement"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(patch, &decoded); err != nil {
		return nil, fmt.Errorf("the service patch is not JSON: %w", err)
	}
	if decoded.Spec.Placement == nil {
		return nil, errors.New("the service patch names no placement")
	}
	return decoded.Spec.Placement.Constraints, nil
}

// fakeMigrationBroker is the broker manager's migration transport.
type fakeMigrationBroker struct {
	log *migrationCallLog

	mu       sync.Mutex
	artifact migration.Artifact
	payloads map[string][]byte

	exportErr    error
	fileErr      error
	stageErrName string
	stageErr     error
	discardErr   error
	prepareErr   error
	unprepareErr error
	reannealErr  error

	// exportGate, when non-nil, holds Export until it is closed: the concurrency
	// test uses it to keep one migration inside the flow.
	exportGate chan struct{}
	entered    chan struct{}
	enterOnce  sync.Once

	// fileHook, when non-nil, runs on the first File call: the cancellation tests
	// use it to interrupt a transfer halfway through, from inside the relay.
	fileHook func(ctx context.Context, w io.Writer) error
	fileOnce sync.Once

	discards   []string
	prepares   []string
	unprepares []string
	reanneals  []string
}

func (f *fakeMigrationBroker) Export(ctx context.Context, sourceNode string, task v1.Task) (broker.MigrationExportDocument, error) {
	f.log.add("export:%s:%s", sourceNode, task.ID)
	if f.entered != nil {
		f.enterOnce.Do(func() { close(f.entered) })
	}
	if f.exportGate != nil {
		select {
		case <-f.exportGate:
		case <-ctx.Done():
			return broker.MigrationExportDocument{}, ctx.Err()
		}
	}
	if f.exportErr != nil {
		return broker.MigrationExportDocument{}, f.exportErr
	}
	return broker.MigrationExportDocument{
		SchemaVersion: v1.SchemaVersion,
		Kind:          "MigrationArtifact",
		Artifact:      f.artifact,
		Dir:           "/var/lib/migration/" + f.artifact.ID,
	}, nil
}

func (f *fakeMigrationBroker) File(
	ctx context.Context, nodeID string, _ v1.Task, _ migration.Artifact, name string, w io.Writer,
) (int64, error) {
	f.log.add("file:%s:%s", nodeID, name)

	var hookErr error
	if f.fileHook != nil {
		f.fileOnce.Do(func() { hookErr = f.fileHook(ctx, w) })
		if hookErr != nil {
			return 0, hookErr
		}
	}
	if f.fileErr != nil {
		return 0, f.fileErr
	}
	f.mu.Lock()
	payload, ok := f.payloads[name]
	f.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("fake broker has no payload for %q", name)
	}
	written, err := w.Write(payload)
	return int64(written), err
}

func (f *fakeMigrationBroker) Stage(
	_ context.Context, nodeID string, _ v1.Task, artifact migration.Artifact,
	name string, size int64, sha256Hex string, r io.Reader,
) error {
	f.log.add("stage:%s:%s", nodeID, name)
	if f.stageErr != nil && f.stageErrName == name {
		return f.stageErr
	}

	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(body)) != size {
		return fmt.Errorf("staged %q as %d bytes: want %d", name, len(body), size)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != sha256Hex {
		return fmt.Errorf("staged %q with SHA-256 %s: want %s", name, got, sha256Hex)
	}
	if f.payloads != nil && string(body) != string(f.payloads[name]) {
		return fmt.Errorf("staged %q with bytes the source never served", name)
	}
	return nil
}

func (f *fakeMigrationBroker) Discard(_ context.Context, nodeID string, _ v1.Task, artifact migration.Artifact) error {
	f.log.add("discard:%s:%s", nodeID, artifact.ID)
	f.mu.Lock()
	f.discards = append(f.discards, artifact.ID)
	f.mu.Unlock()
	return f.discardErr
}

func (f *fakeMigrationBroker) Prepare(_ context.Context, nodeID, serviceID, artifactID string) error {
	f.log.add("prepare:%s:%s:%s", nodeID, serviceID, artifactID)
	f.mu.Lock()
	f.prepares = append(f.prepares, serviceID+"/"+artifactID)
	f.mu.Unlock()
	return f.prepareErr
}

func (f *fakeMigrationBroker) Unprepare(_ context.Context, nodeID, serviceID, artifactID string) error {
	f.log.add("unprepare:%s:%s:%s", nodeID, serviceID, artifactID)
	f.mu.Lock()
	f.unprepares = append(f.unprepares, serviceID)
	f.mu.Unlock()
	return f.unprepareErr
}

// Reanneal records one peer's re-anneal, in call order with everything else: the
// point of the call is that it happens after the move and on the right nodes.
func (f *fakeMigrationBroker) Reanneal(_ context.Context, nodeID, ip, mac string) error {
	f.log.add("reanneal:%s:%s:%s", nodeID, ip, mac)
	f.mu.Lock()
	f.reanneals = append(f.reanneals, nodeID+" "+ip+" "+mac)
	f.mu.Unlock()
	return f.reannealErr
}

// migrationHarness is one coordinator plus the two fakes and the real spool.
type migrationHarness struct {
	coordinator *migrationCoordinator
	client      *fakeMigrationClient
	broker      *fakeMigrationBroker
	spool       *migration.Store
	log         *migrationCallLog
	artifact    migration.Artifact
}

// migrationPayloads is the three files the fake artifact describes, with bytes
// that differ per file so a mixed-up relay fails its digest check.
func migrationPayloads() map[string][]byte {
	return map[string][]byte{
		"state.bin":  []byte("state: the VM's device model and memory map\n"),
		"memory.bin": []byte("memory: the guest's pages, standing in for 512 MiB\n"),
		"disk.ext4":  []byte("disk: the guest's filesystem, standing in for 1 GiB\n"),
	}
}

// newMigrationTestArtifact describes the payloads as a valid artifact for task
// taskID on node nodeID in service serviceID.
func newMigrationTestArtifact(t *testing.T, taskID, serviceID, nodeID string, payloads map[string][]byte) migration.Artifact {
	t.Helper()

	file := func(name string) migration.File {
		body := payloads[name]
		sum := sha256.Sum256(body)
		return migration.File{Name: name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
	}

	created := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	return migration.Artifact{
		SchemaVersion:   v1.SchemaVersion,
		Kind:            migration.ArtifactKind,
		ID:              migrationTestArtifactID,
		SnapshotID:      "snap-0123456789abcdef",
		SourceTaskID:    taskID,
		RequestedTaskID: taskID,
		SourceNodeID:    nodeID,
		ServiceID:       serviceID,
		CreatedAt:       created,
		SnapshotAt:      created.Add(-time.Minute),
		TapName:         scruntime.TapName(taskID, 0),
		VsockPath:       filepath.Join("/var/run/firecracker", taskID+".vsock"),
		DiskPath:        filepath.Join("/var/lib/firecracker/rootfs/tasks", taskID+".ext4"),
		StateFile:       file("state.bin"),
		MemoryFile:      file("memory.bin"),
		DiskFile:        file("disk.ext4"),
	}
}

// freshTaskAt is a creation instant just after now: the verify only accepts a
// replacement created at or after the migration started, so a scripted task is dated
// slightly ahead of the harness that builds it rather than racing the real clock.
func freshTaskAt() string {
	return time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
}

// newMigrationHarness builds the flow with short deadlines and a service whose
// task runs on node-a with one replica and one existing constraint.
func newMigrationHarness(t *testing.T) *migrationHarness {
	t.Helper()

	spool, err := migration.NewStore(migration.StoreConfig{Root: filepath.Join(t.TempDir(), "spool")})
	require.NoError(t, err)

	callLog := &migrationCallLog{}
	payloads := migrationPayloads()
	artifact := newMigrationTestArtifact(t, "web-1", "svc-1", "node-a", payloads)

	replicas := int64(1)
	client := &fakeMigrationClient{
		log: callLog,
		task: v1.Task{
			ID: "web-1", ServiceID: "svc-1", ServiceName: "web", NodeID: "node-a",
			State: "running", DesiredState: "running",
		},
		service: v1.Service{
			ID: "svc-1", Name: "web", Revision: "7",
			Spec: v1.ServiceSpec{
				Mode:      "replicated",
				Replicas:  &replicas,
				Placement: &v1.Placement{Constraints: []string{"node.labels.zone==a"}},
			},
		},
		node:     v1.Node{ID: "node-b", State: "ready", Availability: "active"},
		revision: 7,
	}
	// The default script: the old task is still there, then it is stopped (still
	// listed), then a replacement reports running on the target.
	client.taskSteps = [][]v1.Task{
		{{ID: "web-1", ServiceID: "svc-1", NodeID: "node-a", State: "running"}},
		{{ID: "web-1", ServiceID: "svc-1", NodeID: "node-a", State: "shutdown"}},
		{{
			ID: "web-2", ServiceID: "svc-1", NodeID: "node-b", State: "running",
			CreatedAt: freshTaskAt(),
		}},
	}

	nodes := &fakeMigrationBroker{log: callLog, artifact: artifact, payloads: payloads}

	// self is empty, which is the gate's "no verdict" case: these tests are about the
	// move, and the leadership gate has its own. A test that wants a verdict sets
	// self and the node list.
	coordinator := newMigrationCoordinator(client, nodes, spool, "")
	coordinator.now = time.Now
	coordinator.overallTimeout = 5 * time.Second
	coordinator.verifyTimeout = 2 * time.Second
	coordinator.cleanupTimeout = 2 * time.Second
	coordinator.pollInterval = time.Millisecond

	return &migrationHarness{
		coordinator: coordinator, client: client, broker: nodes,
		spool: spool, log: callLog, artifact: artifact,
	}
}

func TestMigrateHappyPath(t *testing.T) {
	h := newMigrationHarness(t)
	h.coordinator.now = (&steppingClock{
		at:   migrationTestStart,
		step: 10 * time.Millisecond,
	}).Now
	// The replacement is created after this migration started, which is what makes it
	// the replacement rather than an earlier attempt's task.
	h.client.taskSteps[2] = []v1.Task{{
		ID: "web-2", ServiceID: "svc-1", NodeID: "node-b", State: "running",
		CreatedAt: migrationTestStart.Add(5 * time.Second).Format(time.RFC3339Nano),
	}}

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)

	// The call order is the contract: the target is only prepared once its bytes
	// arrived, the service is only replaced once the directive is recorded, and
	// the artifact is only discarded once a replacement is running.
	assert.Equal(t, []string{
		"task:web-1",
		"service:svc-1",
		"node:node-b",
		"export:node-a:web-1",
		"file:node-a:state.bin",
		"stage:node-b:state.bin",
		"file:node-a:memory.bin",
		"stage:node-b:memory.bin",
		"file:node-a:disk.ext4",
		"stage:node-b:disk.ext4",
		"prepare:node-b:svc-1:" + migrationTestArtifactID,
		"update:svc-1",
		"tasks:svc-1",
		"tasks:svc-1",
		"tasks:svc-1",
		"service:svc-1",
		"update:svc-1",
		"discard:node-a:" + migrationTestArtifactID,
	}, h.log.snapshot())

	require.Len(t, h.client.updates, 2)
	assert.Equal(t,
		[]string{"node.labels.zone==a", "node.id==node-b"},
		h.client.updates[0].constraints)
	assert.Equal(t, "7", h.client.updates[0].ifMatch,
		"the pin is applied against the revision validate read")
	assert.Equal(t, []string{"node.labels.zone==a"}, h.client.updates[1].constraints,
		"cleanup restores the original constraints")
	assert.Equal(t, "8", h.client.updates[1].ifMatch,
		"cleanup reads the revision the pin produced")

	assert.Equal(t, []string{migrationTestArtifactID}, h.broker.discards)
	assert.Empty(t, h.broker.unprepares, "a replacement that ran consumed the directive")
	assert.Empty(t, h.broker.prepares[1:], "one migration records one directive")

	assert.Equal(t, v1.KindMigration, doc.Kind)
	assert.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
	assert.Equal(t, migrationTestArtifactID, doc.ID, "the migration is its artifact")
	assert.Equal(t, "web-1", doc.TaskID)
	assert.Equal(t, "svc-1", doc.ServiceID)
	assert.Equal(t, "node-a", doc.SourceNode)
	assert.Equal(t, "node-b", doc.TargetNode)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)
	assert.Greater(t, doc.DowntimeMs, int64(0), "downtime is measured, not assumed")

	var names []string
	for _, phase := range doc.Phases {
		names = append(names, phase.Name)
		assert.Equal(t, v1.MigrationPhaseOK, phase.Result, phase.Name)
		assert.NotEmpty(t, phase.StartedAt, phase.Name)
		assert.NotEmpty(t, phase.FinishedAt, phase.Name)
	}
	assert.Equal(t, []string{
		v1.MigrationPhaseValidate, v1.MigrationPhaseExport, v1.MigrationPhaseRelay,
		v1.MigrationPhasePrepare, v1.MigrationPhaseReplace, v1.MigrationPhaseVerify,
		v1.MigrationPhaseCleanup,
	}, names)

	requireSpoolEmpty(t, h)
}

func TestMigrateRefusesTwoReplicasBeforeAnyNodeCall(t *testing.T) {
	h := newMigrationHarness(t)
	replicas := int64(2)
	h.client.service.Spec.Replicas = &replicas

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, output.CodeInvalid, typed.Code)
	assert.Equal(t, "replicas", typed.Details["reason"])
	assert.Equal(t, []string{"task:web-1", "service:svc-1"}, h.log.snapshot(),
		"a two-replica service is refused before the target is resolved")
	requireSpoolEmpty(t, h)
}

func TestMigrateRefusesTheCurrentNode(t *testing.T) {
	h := newMigrationHarness(t)

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-a")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, output.CodeInvalid, typed.Code)
	assert.Equal(t, "node-a", typed.Details["nodeId"])
	assert.Equal(t, []string{"task:web-1", "service:svc-1"}, h.log.snapshot())
}

func TestMigrateRefusesATaskThatIsNotRunning(t *testing.T) {
	h := newMigrationHarness(t)
	h.client.task.State = "failed"

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, output.CodeInvalid, typed.Code)
	assert.Equal(t, []string{"task:web-1"}, h.log.snapshot())
}

func TestMigrateRefusesATargetThatIsNotSchedulable(t *testing.T) {
	h := newMigrationHarness(t)
	h.client.node.State = "down"

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, "node_not_schedulable", typed.Details["reason"])
	assert.Empty(t, h.broker.prepares)
	assert.Empty(t, h.client.updates)
}

func TestMigrateExportFailureDiscardsNothingItNeverHad(t *testing.T) {
	h := newMigrationHarness(t)
	h.broker.exportErr = output.NewError(output.CodeUnavailable, "the source node is unreachable")

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, output.CodeUnavailable, typed.Code)
	assert.Equal(t, v1.MigrationPhaseExport, typed.Details["phase"])

	assert.Empty(t, h.client.updates, "nothing is replaced when the export fails")
	assert.Empty(t, h.broker.prepares, "no directive is recorded")
	assert.Empty(t, h.broker.discards, "no artifact was produced, so there is none to discard")
	requireSpoolEmpty(t, h)

	// The in-flight slot is released: a later migration of the same service runs.
	h.broker.exportErr = nil
	_, err = h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)
}

func TestMigrateRefusesAnArtifactForAnotherTask(t *testing.T) {
	h := newMigrationHarness(t)
	h.broker.artifact.RequestedTaskID = "other-task"

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, v1.MigrationPhaseExport, typed.Details["phase"])
	assert.Equal(t, "artifact_mismatch", typed.Details["reason"])
	assert.Empty(t, h.broker.discards, "an artifact describing another task is not this migration's to delete")
	assert.Empty(t, h.client.updates)
}

func TestMigrateStageFailureDiscardsTheArtifact(t *testing.T) {
	h := newMigrationHarness(t)
	h.broker.stageErrName = "memory.bin"
	h.broker.stageErr = output.NewError(output.CodeUnavailable, "the target node refused the file")

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, v1.MigrationPhaseRelay, typed.Details["phase"])

	assert.Empty(t, h.client.updates, "the service is never replaced")
	assert.Empty(t, h.broker.prepares, "the directive is recorded only after every file arrived")
	assert.Equal(t, []string{"svc-1"}, h.broker.unprepares,
		"the removal runs anyway: a lost acknowledge may have left a directive behind")
	assert.Equal(t, []string{migrationTestArtifactID}, h.broker.discards,
		"the artifact the export produced is discarded")
	requireSpoolEmpty(t, h)
}

func TestMigrateRejectedUpdateAbandonsTheMigration(t *testing.T) {
	h := newMigrationHarness(t)
	h.client.updateErr = output.NewError(output.CodeConflict, "the service changed while the artifact was in transit")

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, output.CodeConflict, typed.Code)
	assert.Equal(t, v1.MigrationPhaseReplace, typed.Details["phase"])

	require.Len(t, h.client.updates, 1, "the rejected update is the only one: the placement never changed")
	assert.Equal(t, []string{"node.labels.zone==a", "node.id==node-b"}, h.client.updates[0].constraints)
	assert.Equal(t, []string{"svc-1"}, h.broker.unprepares, "the directive is removed with the artifact")
	assert.Equal(t, []string{migrationTestArtifactID}, h.broker.discards)
	requireSpoolEmpty(t, h)
}

// TestMigrateRefusesToRunOnAFollower is ruling I1(a): two managers can migrate the
// same service, and both would prepare the target, pin the placement and unpin it
// again - so only the raft leader acts, and a follower says where to retry.
func TestMigrateRefusesToRunOnAFollower(t *testing.T) {
	h := newMigrationHarness(t)
	h.coordinator.self = "node-a"
	h.client.nodes = []v1.Node{
		{ID: "node-a", Hostname: "node-a", Manager: &v1.NodeManager{Leader: false}},
		{ID: "node-b", Hostname: "node-b", Manager: &v1.NodeManager{Leader: true}},
	}

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, output.CodeUnavailable, typed.Code, "a follower refuses the call")
	assert.Equal(t, "not_leader", typed.Details["reason"])
	assert.Equal(t, "node-b", typed.Details["leader"])
	assert.Equal(t, v1.MigrationPhaseValidate, typed.Details["phase"])
	assert.Contains(t, typed.Message, "retry", "the answer says what to do next")

	assert.Equal(t, []string{"nodes"}, h.log.snapshot(), "a follower touches nothing else")
	assert.Zero(t, h.coordinator.migrationCount(), "a refused call holds no in-flight slot")
}

// TestMigrateRunsOnTheLeader pins the other half of the gate: the leader is not
// refused, and the node list is the first thing the call reads.
func TestMigrateRunsOnTheLeader(t *testing.T) {
	h := newMigrationHarness(t)
	h.coordinator.self = "node-a"
	h.client.nodes = []v1.Node{
		{ID: "node-a", Hostname: "node-a", Manager: &v1.NodeManager{Leader: true}},
		{ID: "node-b", Hostname: "node-b", Manager: &v1.NodeManager{Leader: false}},
	}

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)

	calls := h.log.snapshot()
	require.NotEmpty(t, calls)
	assert.Equal(t, "nodes", calls[0], "the leadership gate runs before anything is read or written")
	assert.False(t, h.coordinator.gateNotice, "a verdict is not a failed-open note")
}

// TestMigrateFailsOpenWithoutALeaderVerdict pins the ruling's second half: a cluster
// that cannot say who the leader is must not stop moving services. Each case is one
// reason there is no verdict, and each still runs the migration.
func TestMigrateFailsOpenWithoutALeaderVerdict(t *testing.T) {
	cases := []struct {
		name     string
		self     string
		nodes    []v1.Node
		nodesErr error
	}{
		{
			name: "no node reports itself as the leader",
			self: "node-a",
			nodes: []v1.Node{
				{ID: "node-a", Hostname: "node-a"},
				{ID: "node-b", Hostname: "node-b"},
			},
		},
		{
			name:     "the node list cannot be read",
			self:     "node-a",
			nodesErr: output.NewError(output.CodeUnavailable, "the control socket is unreachable"),
		},
		{
			name:  "the leader has no hostname",
			self:  "node-a",
			nodes: []v1.Node{{ID: "node-b", Manager: &v1.NodeManager{Leader: true}}},
		},
		{
			name:  "this daemon does not know its own hostname",
			self:  "",
			nodes: []v1.Node{{ID: "node-b", Hostname: "node-b", Manager: &v1.NodeManager{Leader: true}}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newMigrationHarness(t)
			h.coordinator.self = tc.self
			h.client.nodes = tc.nodes
			h.client.nodesErr = tc.nodesErr

			doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
			require.NoError(t, err, "no verdict means the migration runs")
			assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)
			assert.True(t, h.coordinator.gateNotice, "the failed-open decision is noted once")
		})
	}
}

// TestMigrateKeepsAMoveWhoseUpdateAnswerWasLost is ruling I2: SwarmKit commits the
// update and the answer travels back separately, so a failed call can still have
// pinned the service. Abandoning would restore the placement under a starting
// replacement, delete the target's directive and discard the artifact.
func TestMigrateKeepsAMoveWhoseUpdateAnswerWasLost(t *testing.T) {
	h := newMigrationHarness(t)
	// The pin is written, then the answer is lost.
	h.client.updateCommitErrs = []error{output.NewError(output.CodeTimeout, "the update's answer was lost")}

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)

	require.Len(t, h.client.updates, 2, "the pin, then the restore: the landed pin is not repeated")
	assert.Equal(t, []string{"node.labels.zone==a", "node.id==node-b"}, h.client.updates[0].constraints)
	assert.Equal(t, []string{"node.labels.zone==a"}, h.client.updates[1].constraints,
		"the placement is restored against the revision the landed pin produced")

	assert.Empty(t, h.broker.unprepares, "the replacement consumed the directive")
	assert.Equal(t, []string{migrationTestArtifactID}, h.broker.discards, "the moved artifact is discarded on the source")
	requireSpoolEmpty(t, h)
}

// TestMigrateRemovesADirectiveWhosePrepareAnswerWasLost is ruling I3: the node records
// the directive before it answers, so a prepare that timed out may have landed. The
// removal is artifact-scoped, so it can only remove this migration's own directive.
func TestMigrateRemovesADirectiveWhosePrepareAnswerWasLost(t *testing.T) {
	h := newMigrationHarness(t)
	h.broker.prepareErr = output.NewError(output.CodeTimeout, "the target did not answer the prepare")

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, v1.MigrationPhasePrepare, typed.Details["phase"])
	assert.Contains(t, h.log.snapshot(), "unprepare:node-b:svc-1:"+migrationTestArtifactID,
		"the removal names the artifact it owns")
	assert.Equal(t, []string{migrationTestArtifactID}, h.broker.discards)
	assert.Empty(t, h.client.updates, "the service is never replaced")
	requireSpoolEmpty(t, h)
}

// TestMigrateLeavesANewerDirectiveInPlace pins what the artifact in the removal buys:
// when another migration prepared the service after this one, the target answers 409
// and this call's cleanup leaves that directive alone without turning its own failure
// into the answer.
func TestMigrateLeavesANewerDirectiveInPlace(t *testing.T) {
	h := newMigrationHarness(t)
	h.broker.stageErrName = "memory.bin"
	h.broker.stageErr = output.NewError(output.CodeUnavailable, "the target node refused the file")
	h.broker.unprepareErr = output.NewError(output.CodeConflict,
		"this node waits for a newer migration's artifact for this service: the directive names another artifact").
		WithDetails(map[string]any{"reason": "directive_replaced", "serviceId": "svc-1"})

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, output.CodeUnavailable, typed.Code, "the relay's failure is still the answer")
	assert.Equal(t, v1.MigrationPhaseRelay, typed.Details["phase"])
	assert.Equal(t, []string{migrationTestArtifactID}, h.broker.discards,
		"this migration's artifact still goes")
}

// TestMigrateCancelledRequestAbortsTheRelay is ruling I5: a caller that goes away
// mid-transfer must not leave a spool entry, a directive or an artifact behind. The
// cancellation is injected inside the relay, halfway through a file.
func TestMigrateCancelledRequestAbortsTheRelay(t *testing.T) {
	h := newMigrationHarness(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.broker.fileHook = func(hookCtx context.Context, w io.Writer) error {
		_, _ = io.WriteString(w, "state: half of the VM's state, and then the caller left")
		cancel()
		<-hookCtx.Done()
		return hookCtx.Err()
	}

	_, err := h.coordinator.Migrate(ctx, "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, output.CodeTimeout, typed.Code, "a cancelled call is a timeout, not a 500")
	assert.Equal(t, v1.MigrationPhaseRelay, typed.Details["phase"])

	assert.Equal(t, []string{"svc-1"}, h.broker.unprepares, "nothing is left for the service's next task to restore")
	assert.Equal(t, []string{migrationTestArtifactID}, h.broker.discards, "the source's artifact is discarded")
	assert.Empty(t, h.client.updates, "the service is never replaced")
	requireSpoolEmpty(t, h, "a half-transferred file is not published")
	assert.Zero(t, h.coordinator.migrationCount(), "the slot is released")
}

// TestMigrateOverallDeadlineAbortsTheRelay is the other half of ruling I5: the call's
// own bound expiring mid-transfer cleans up exactly like a cancellation, and the
// untyped transport error the relay sees is classified as a timeout - with its text,
// which names a node's path, kept out of the answer (ruling M7).
func TestMigrateOverallDeadlineAbortsTheRelay(t *testing.T) {
	h := newMigrationHarness(t)
	h.coordinator.overallTimeout = 50 * time.Millisecond
	h.broker.fileHook = func(hookCtx context.Context, w io.Writer) error {
		_, _ = io.WriteString(w, "state: half of the VM's state, and then the bound expired")
		<-hookCtx.Done()
		return fmt.Errorf("read /var/lib/firecracker/artifacts/state.bin: %w", hookCtx.Err())
	}

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, output.CodeTimeout, typed.Code, "an untyped deadline is a timeout, not an internal error")
	assert.Equal(t, "phase_timeout", typed.Details["reason"])
	assert.NotContains(t, typed.Message, "/var/lib", "the answer never carries a node-local path")

	assert.Equal(t, []string{"svc-1"}, h.broker.unprepares)
	assert.Equal(t, []string{migrationTestArtifactID}, h.broker.discards)
	assert.Empty(t, h.client.updates)
	requireSpoolEmpty(t, h)
}

// TestMigrateCleanupMessageCarriesNoNodePath pins the other half of ruling M7: the
// cleanup's best-effort failure is summarised by classification in the report, and the
// text - which names the node's own directories - stays in the log.
func TestMigrateCleanupMessageCarriesNoNodePath(t *testing.T) {
	h := newMigrationHarness(t)
	h.broker.discardErr = output.NewError(output.CodeUnavailable,
		"delete /var/lib/firecracker/artifacts/"+migrationTestArtifactID+"/state.bin: permission denied").
		WithDetails(map[string]any{"reason": "artifact_unremovable"})

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)

	var cleanup v1.MigrationPhase
	for _, phase := range doc.Phases {
		assert.NotContains(t, phase.Message, "/var/lib", "a report never carries a node-local path (%s)", phase.Name)
		if phase.Name == v1.MigrationPhaseCleanup {
			cleanup = phase
		}
	}
	require.Equal(t, v1.MigrationPhaseCleanup, cleanup.Name)
	assert.Contains(t, cleanup.Message, "discard the artifact on the source")
	assert.Contains(t, cleanup.Message, "unavailable (artifact_unremovable)",
		"the summary is the classification, not the text")
}

func TestMigrateVerifyTimeoutKeepsTheRetryAndRestoresPlacement(t *testing.T) {
	h := newMigrationHarness(t)
	h.coordinator.verifyTimeout = 20 * time.Millisecond
	h.coordinator.pollInterval = time.Millisecond
	// The old task never disappears.
	h.client.taskSteps = [][]v1.Task{
		{{ID: "web-1", ServiceID: "svc-1", NodeID: "node-a", State: "running"}},
	}

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)

	assert.Equal(t, v1.MigrationOutcomePending, doc.Outcome)
	assert.Zero(t, doc.DowntimeMs, "an unmeasured downtime is not zero seconds")

	var verify v1.MigrationPhase
	for _, phase := range doc.Phases {
		if phase.Name == v1.MigrationPhaseVerify {
			verify = phase
		}
	}
	require.Equal(t, v1.MigrationPhaseVerify, verify.Name)
	assert.Equal(t, v1.MigrationPhasePending, verify.Result)
	assert.Equal(t, reasonOldTaskRunning, verify.Message)

	// The placement is restored, but the artifact and the directive stay: the
	// replaced service's retry restores from them.
	require.Len(t, h.client.updates, 2)
	assert.Equal(t, []string{"node.labels.zone==a"}, h.client.updates[1].constraints)
	assert.Empty(t, h.broker.discards)
	assert.Empty(t, h.broker.unprepares)
	requireSpoolEmpty(t, h, "the spool holds nothing once every file has crossed")
}

func TestMigrateVerifyReportsAReplacementThatNeverRuns(t *testing.T) {
	h := newMigrationHarness(t)
	h.coordinator.verifyTimeout = 20 * time.Millisecond
	h.coordinator.pollInterval = time.Millisecond
	h.client.taskSteps = [][]v1.Task{
		{},
	}

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)

	assert.Equal(t, v1.MigrationOutcomePending, doc.Outcome)
	for _, phase := range doc.Phases {
		if phase.Name == v1.MigrationPhaseVerify {
			assert.Equal(t, reasonReplacementNotRunning, phase.Message)
		}
	}
}

func TestMigrateVerifyFailureKeepsTheRetryAndReportsThePhase(t *testing.T) {
	h := newMigrationHarness(t)
	h.client.taskSteps = [][]v1.Task{
		{{
			ID: "web-2", ServiceID: "svc-1", NodeID: "node-b", State: "failed",
			// The failure the update caused: created after the update returned.
			CreatedAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
		}},
	}

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)

	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, v1.MigrationPhaseVerify, typed.Details["phase"])
	assert.Equal(t, "replacement_failed", typed.Details["reason"])

	require.Len(t, h.client.updates, 2, "the placement is restored")
	assert.Empty(t, h.broker.discards, "the artifact is the retry's")
	assert.Empty(t, h.broker.unprepares, "the directive is the retry's")
}

// TestMigrateVerifyIgnoresAnEarlierFailedTask pins the filter a retried migration
// needs: a failed task the service already had on the target is not this call's
// failure, so the verify keeps waiting instead of refusing a retry.
func TestMigrateVerifyIgnoresAnEarlierFailedTask(t *testing.T) {
	h := newMigrationHarness(t)
	h.coordinator.verifyTimeout = 20 * time.Millisecond
	h.coordinator.pollInterval = time.Millisecond
	h.client.taskSteps = [][]v1.Task{
		{{
			ID: "web-0", ServiceID: "svc-1", NodeID: "node-b", State: "failed",
			CreatedAt: "2026-01-01T00:00:00Z",
		}},
	}

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)
	assert.Equal(t, v1.MigrationOutcomePending, doc.Outcome)
}

func TestMigrateAllowsOneMigrationPerService(t *testing.T) {
	h := newMigrationHarness(t)
	h.broker.exportGate = make(chan struct{})
	h.broker.entered = make(chan struct{})

	done := make(chan error, 1)
	go func() {
		_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
		done <- err
	}()

	select {
	case <-h.broker.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the first migration never reached the export")
	}

	_, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.Error(t, err)
	typed := requireTypedMigrationError(t, err)
	assert.Equal(t, output.CodeConflict, typed.Code)
	assert.Equal(t, "migration_in_flight", typed.Details["reason"])
	assert.Equal(t, v1.MigrationPhaseValidate, typed.Details["phase"])

	close(h.broker.exportGate)
	require.NoError(t, <-done)

	assert.Zero(t, h.coordinator.migrationCount(), "the slot is released on exit")
}

func TestMigrateCleanupFailureDoesNotFailAMovedMigration(t *testing.T) {
	h := newMigrationHarness(t)
	h.broker.discardErr = output.NewError(output.CodeUnavailable, "the source node is unreachable")

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)

	var cleanup v1.MigrationPhase
	for _, phase := range doc.Phases {
		if phase.Name == v1.MigrationPhaseCleanup {
			cleanup = phase
		}
	}
	assert.Equal(t, v1.MigrationPhaseOK, cleanup.Result)
	assert.Contains(t, cleanup.Message, "discard the artifact",
		"a cleanup that could not finish is reported, not turned into an error")
}

// TestMigrateReannealsEveryPeerButTheTarget pins the L2 half of a move: after the
// guest runs on the target, every other node is told to forget it - the source
// included, because its bridge learned the MAC too - and the target is not, because
// it is the node that just learned the guest.
func TestMigrateReannealsEveryPeerButTheTarget(t *testing.T) {
	h := newMigrationHarness(t)
	h.client.task.Addresses = []string{"10.0.1.7/24"}
	h.client.nodes = []v1.Node{
		{ID: "node-a", Hostname: "worker-a"},
		{ID: "node-b", Hostname: "worker-b"},
		{ID: "node-c", Hostname: "worker-c"},
	}

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)

	// The MAC is the guest's, derived from the VM's own identity - the artifact's
	// source task - not from the host-side TAP device.
	guestMAC := scruntime.GuestMAC("web-1", 0)
	assert.Equal(t, []string{
		"node-a 10.0.1.7 " + guestMAC,
		"node-c 10.0.1.7 " + guestMAC,
	}, h.broker.reanneals, "every node except the target is re-annealed")

	calls := h.log.snapshot()
	at := indexOfCall(calls, "reanneal:node-a:10.0.1.7:"+guestMAC)
	require.NotEqual(t, -1, at, "the source is re-annealed: %v", calls)
	assert.Less(t, indexOfCall(calls, "nodes"), at, "the peer list is read before the first peer")
	assert.Greater(t, at, indexOfCall(calls, "update:svc-1"),
		"the peers are only told once the replacement is the service's task")
	assert.Less(t, at, indexOfCall(calls, "discard:node-a:"+migrationTestArtifactID),
		"the peers are re-annealed while the source still holds the artifact")
}

// TestMigrateReannealFailureKeepsTheMovedOutcome pins the best-effort rule: a peer
// that cannot forget the guest is logged, and the migration still reports moved,
// because the guest is up on its target and reachability recovers on the next ARP.
func TestMigrateReannealFailureKeepsTheMovedOutcome(t *testing.T) {
	h := newMigrationHarness(t)
	h.client.task.Addresses = []string{"10.0.1.7/24"}
	h.client.nodes = []v1.Node{{ID: "node-a"}, {ID: "node-b"}, {ID: "node-c"}}
	h.broker.reannealErr = output.NewError(output.CodeUnavailable,
		"this node could not forget the moved guest").
		WithDetails(map[string]any{"reason": "reanneal_failed"})

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)
	assert.Len(t, h.broker.reanneals, 2, "a failing peer does not stop the next one")

	var cleanup v1.MigrationPhase
	for _, phase := range doc.Phases {
		if phase.Name == v1.MigrationPhaseCleanup {
			cleanup = phase
		}
	}
	assert.Equal(t, v1.MigrationPhaseOK, cleanup.Result)
	assert.NotContains(t, cleanup.Message, "re-anneal",
		"the report is about the move; a peer's stale entry is not a cleanup step that failed")
}

// TestMigrateSkipsTheReannealWithoutAGuestAddress pins the one case a re-anneal
// cannot happen: a task the cluster reports no address for has no guest a peer
// could have cached, so the node list is not even read.
func TestMigrateSkipsTheReannealWithoutAGuestAddress(t *testing.T) {
	h := newMigrationHarness(t)
	h.client.nodes = []v1.Node{{ID: "node-a"}, {ID: "node-b"}}

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)
	assert.Empty(t, h.broker.reanneals)
	assert.Equal(t, -1, indexOfCall(h.log.snapshot(), "nodes"),
		"a task with no address needs no peer list")
}

// TestGuestAddress pins what a re-anneal sends as the guest's address: the task's
// first allocated address without its prefix length, and nothing at all for a task
// the cluster cannot say an address for.
func TestGuestAddress(t *testing.T) {
	cases := []struct {
		name      string
		addresses []string
		want      string
	}{
		{name: "prefix stripped", addresses: []string{"10.0.1.7/24"}, want: "10.0.1.7"},
		{name: "bare address", addresses: []string{"10.0.1.7"}, want: "10.0.1.7"},
		{name: "first attachment wins", addresses: []string{"10.0.1.7/24", "10.0.2.7/24"}, want: "10.0.1.7"},
		{name: "no address", addresses: nil, want: ""},
		{name: "not an address", addresses: []string{"not-an-ip"}, want: ""},
		{name: "empty", addresses: []string{""}, want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, guestAddress(v1.Task{Addresses: tc.addresses}))
		})
	}
}

// indexOfCall returns the position of one logged call, or -1.
func indexOfCall(calls []string, want string) int {
	for i, call := range calls {
		if call == want {
			return i
		}
	}
	return -1
}

// TestRestorePlacementRetriesOnceOnAConflict pins the cleanup's one retry: the
// revision read a moment earlier can be stale, and a conflict is re-read rather
// than reported.
func TestRestorePlacementRetriesOnceOnAConflict(t *testing.T) {
	h := newMigrationHarness(t)
	h.client.updateErrs = []error{
		nil, // the pin
		output.NewError(output.CodeConflict, "the service changed between the read and the update"),
		nil, // the retry after a fresh read
	}

	doc, err := h.coordinator.Migrate(context.Background(), "web-1", "node-b")
	require.NoError(t, err)
	assert.Equal(t, v1.MigrationOutcomeMoved, doc.Outcome)

	require.Len(t, h.client.updates, 3, "the pin, the conflicted restore and its retry")
	assert.Equal(t, []string{"node.labels.zone==a"}, h.client.updates[2].constraints)
}

// TestRestorePlacementLeavesAPlacementSomeoneElseChanged pins the ownership rule:
// cleanup only undoes the constraint this migration wrote. A service whose
// placement is someone else's decision keeps it.
func TestRestorePlacementLeavesAPlacementSomeoneElseChanged(t *testing.T) {
	h := newMigrationHarness(t)
	h.client.service.Spec.Placement = &v1.Placement{Constraints: []string{"node.id==node-c"}}

	plan := migrationPlan{service: v1.Service{ID: "svc-1"}, constraints: []string{"node.labels.zone==a"}}
	state := &migrationState{pinned: []string{"node.labels.zone==a", "node.id==node-b"}}

	require.NoError(t, h.coordinator.restorePlacement(context.Background(), plan, state))
	assert.Empty(t, h.client.updates, "another writer's placement is not this migration's to undo")
}

// TestMigrationSpoolSweeperReclaimsAnAbandonedEntry pins the one backstop a crash
// leaves to: an entry nobody is holding, older than the TTL, is removed by the
// sweep the manager runs at startup.
func TestMigrationSpoolSweeperReclaimsAnAbandonedEntry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	spool, err := migration.NewStore(migration.StoreConfig{Root: root, TTL: 5 * time.Millisecond})
	require.NoError(t, err)

	dir := filepath.Join(root, migrationTestArtifactID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.bin"), []byte("abandoned"), 0o600))
	time.Sleep(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	startMigrationSpoolSweeper(ctx, spool)

	require.Eventually(t, func() bool {
		_, statErr := os.Stat(dir)
		return errors.Is(statErr, os.ErrNotExist)
	}, 2*time.Second, 5*time.Millisecond, "the startup sweep must reclaim an expired entry")
}

// TestMigrationSpoolSweeperWithoutASpoolIsANoop pins that a manager with no spool
// (a relative state directory, say) starts no goroutine and does not panic.
func TestMigrationSpoolSweeperWithoutASpoolIsANoop(t *testing.T) {
	startMigrationSpoolSweeper(context.Background(), nil)
}

// TestObserveReplacement pins the readings the verify makes of one task list: whether
// the old task still runs, which task is a replacement, and which failure belongs to
// this migration. The stale cases are ruling M6: a running task on the target that
// predates this migration is not the replacement.
func TestObserveReplacement(t *testing.T) {
	since := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	later := since.Add(time.Second).Format(time.RFC3339Nano)
	earlier := since.Add(-time.Hour).Format(time.RFC3339Nano)

	old := v1.Task{ID: "web-1", ServiceID: "svc-1", NodeID: "node-a", State: "running"}
	running := v1.Task{ID: "web-2", ServiceID: "svc-1", NodeID: "node-b", State: "running", CreatedAt: later}

	t.Run("old task still running", func(t *testing.T) {
		observed := observeReplacement([]v1.Task{old, running}, "svc-1", old.ID, "node-b", since)
		assert.False(t, observed.oldGone, "a running old task has not stopped")
		require.NotNil(t, observed.replacement)
		assert.Equal(t, running.ID, observed.replacement.ID)
	})

	t.Run("old task stopped but still listed", func(t *testing.T) {
		stopped := old
		stopped.State = "shutdown"
		observed := observeReplacement([]v1.Task{stopped, running}, "svc-1", old.ID, "node-b", since)
		assert.True(t, observed.oldGone, "a stopped task no longer runs the workload")
	})

	t.Run("a replacement has to be running and on the target", func(t *testing.T) {
		starting := v1.Task{ID: "web-2", ServiceID: "svc-1", NodeID: "node-b", State: "starting", CreatedAt: later}
		elsewhere := v1.Task{ID: "web-3", ServiceID: "svc-1", NodeID: "node-c", State: "running", CreatedAt: later}
		observed := observeReplacement([]v1.Task{starting, elsewhere}, "svc-1", old.ID, "node-b", since)
		assert.Nil(t, observed.replacement)
	})

	t.Run("a running task that predates the migration is not the replacement", func(t *testing.T) {
		stale := v1.Task{ID: "web-0", ServiceID: "svc-1", NodeID: "node-b", State: "running", CreatedAt: earlier}
		observed := observeReplacement([]v1.Task{stale}, "svc-1", old.ID, "node-b", since)
		assert.Nil(t, observed.replacement, "an earlier attempt's task is not this call's replacement")
	})

	t.Run("an undated running task is not the replacement", func(t *testing.T) {
		undated := v1.Task{ID: "web-2", ServiceID: "svc-1", NodeID: "node-b", State: "running"}
		observed := observeReplacement([]v1.Task{undated}, "svc-1", old.ID, "node-b", since)
		assert.Nil(t, observed.replacement, "a task this call cannot date is never believed")
	})

	t.Run("another service's task on the target is not the replacement", func(t *testing.T) {
		other := v1.Task{ID: "web-9", ServiceID: "svc-2", NodeID: "node-b", State: "running", CreatedAt: later}
		observed := observeReplacement([]v1.Task{other}, "svc-1", old.ID, "node-b", since)
		assert.Nil(t, observed.replacement)
	})

	t.Run("a failure before the migration is another migration's", func(t *testing.T) {
		stale := v1.Task{ID: "web-0", ServiceID: "svc-1", NodeID: "node-b", State: "failed", CreatedAt: earlier}
		observed := observeReplacement([]v1.Task{stale}, "svc-1", old.ID, "node-b", since)
		assert.False(t, observed.failed)
	})

	t.Run("a failure after the migration is this migration's", func(t *testing.T) {
		fresh := v1.Task{ID: "web-2", ServiceID: "svc-1", NodeID: "node-b", State: "failed", CreatedAt: later}
		observed := observeReplacement([]v1.Task{fresh}, "svc-1", old.ID, "node-b", since)
		assert.True(t, observed.failed)
	})

	t.Run("an undated failure is not believed", func(t *testing.T) {
		undated := v1.Task{ID: "web-2", ServiceID: "svc-1", NodeID: "node-b", State: "failed"}
		observed := observeReplacement([]v1.Task{undated}, "svc-1", old.ID, "node-b", since)
		assert.False(t, observed.failed)
	})
}

// steppingClock advances by a fixed step on every read, so a test can prove a
// duration was measured rather than assert a wall-clock race.
type steppingClock struct {
	mu   sync.Mutex
	at   time.Time
	step time.Duration
}

func (c *steppingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	at := c.at
	c.at = c.at.Add(c.step)
	return at
}

// migrationCount reads the in-flight map. The coordinator is safe for concurrent
// use, so the test takes the same lock.
func (c *migrationCoordinator) migrationCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inFlight)
}

// requireTypedMigrationError unwraps the typed error every failure path answers.
func requireTypedMigrationError(t *testing.T, err error) *output.Error {
	t.Helper()
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	return typed
}

// requireSpoolEmpty asserts the spool holds no entry, which is every path's
// promise: the spool is a hand-off buffer, never a store.
func requireSpoolEmpty(t *testing.T, h *migrationHarness, message ...string) {
	t.Helper()

	entries, err := os.ReadDir(h.spool.Root())
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	require.NoError(t, err)
	if len(entries) != 0 {
		t.Fatalf("the spool still holds %v: %v", entries, message)
	}
}
