package main

// The command's tests are about the decisions it makes before a request: a
// reference that does not resolve, a task that is not running, a service it
// cannot pin, and a target that is the source. Each of those is refused locally,
// with no POST; a move that posts prints the report the manager answered, and a
// refusal from the manager crosses as its own typed error.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMigrateClient is the client surface `vm migrate` reads and calls.
type fakeMigrateClient struct {
	nodes    []v1.Node
	tasks    []v1.Task
	services map[string]v1.Service
	doc      v1.Migration
	err      error

	// calls records what the command did, in order, so a test can pin that a
	// refusal never posted.
	calls     []string
	gotTask   string
	gotTarget string
}

func (f *fakeMigrateClient) ListNodes(context.Context) ([]v1.Node, error) {
	f.calls = append(f.calls, "nodes")
	return f.nodes, nil
}

func (f *fakeMigrateClient) ListTasks(context.Context) ([]v1.Task, error) {
	f.calls = append(f.calls, "tasks")
	return f.tasks, nil
}

func (f *fakeMigrateClient) GetService(_ context.Context, ref string) (v1.Service, error) {
	f.calls = append(f.calls, "service:"+ref)
	if service, ok := f.services[ref]; ok {
		return service, nil
	}
	return v1.Service{ID: ref, Spec: v1.ServiceSpec{Mode: "replicated", Replicas: int64Ptr(1)}}, nil
}

func (f *fakeMigrateClient) Migrate(_ context.Context, taskID, targetNode string) (v1.Migration, error) {
	f.calls = append(f.calls, "migrate:"+taskID+":"+targetNode)
	f.gotTask = taskID
	f.gotTarget = targetNode
	return f.doc, f.err
}

// int64Ptr is a one-value replica count.
func int64Ptr(value int64) *int64 { return &value }

// migrationTestDoc is the report a successful move answers.
func migrationTestDoc() v1.Migration {
	return v1.Migration{
		SchemaVersion: v1.SchemaVersion,
		Kind:          v1.KindMigration,
		ID:            "mig-0123456789abcdef",
		TaskID:        "web-1",
		ServiceID:     "svc-1",
		SourceNode:    "node-a",
		TargetNode:    "node-b",
		Phases: []v1.MigrationPhase{
			{
				Name: v1.MigrationPhaseValidate, Result: v1.MigrationPhaseOK,
				StartedAt:  "2026-10-31T12:00:00Z",
				FinishedAt: "2026-10-31T12:00:00.010Z",
			},
			{
				Name: v1.MigrationPhaseExport, Result: v1.MigrationPhaseOK,
				StartedAt:  "2026-10-31T12:00:00.010Z",
				FinishedAt: "2026-10-31T12:00:01.210Z",
			},
			{
				Name: v1.MigrationPhaseVerify, Result: v1.MigrationPhasePending,
				StartedAt: "2026-10-31T12:00:01.210Z",
				Message:   "replacement_not_running",
			},
		},
		DowntimeMs: 1234,
		Outcome:    v1.MigrationOutcomeMoved,
	}
}

// migrationTestClient is one running single-replica service on node-a, with node-b
// as a valid target.
func migrationTestClient() *fakeMigrateClient {
	return &fakeMigrateClient{
		nodes: []v1.Node{
			{ID: "aaaa1111", Hostname: "worker-a", State: "ready", Availability: "active"},
			{ID: "bbbb2222", Hostname: "worker-b", State: "ready", Availability: "active"},
		},
		tasks: []v1.Task{
			{ID: "web-1", ServiceID: "svc-1", NodeID: "aaaa1111", State: "running"},
			{ID: "api-9", ServiceID: "svc-2", NodeID: "bbbb2222", State: "failed"},
		},
		services: map[string]v1.Service{
			"svc-1": {
				ID: "svc-1", Name: "web",
				Spec: v1.ServiceSpec{Mode: "replicated", Replicas: int64Ptr(1)},
			},
		},
		doc: migrationTestDoc(),
	}
}

// TestMigratePrintsTheReport pins the success path's output: one line per phase
// with its duration, the measured downtime, and the outcome last.
func TestMigratePrintsTheReport(t *testing.T) {
	c := migrationTestClient()
	var out bytes.Buffer

	require.NoError(t, migrateTaskWithClient(context.Background(), c, &out, "web-1", "worker-b"))

	assert.Equal(t, []string{"nodes", "tasks", "service:svc-1", "migrate:web-1:bbbb2222"}, c.calls,
		"the node is resolved, then the task, then its service, then one request")

	report := out.String()
	assert.Contains(t, report, "Migration mig-0123456789abcdef")
	assert.Contains(t, report, "web-1")
	assert.Contains(t, report, "node-a")
	assert.Contains(t, report, "node-b", "the report names the target the manager reported")
	assert.Contains(t, report, "validate")
	assert.Contains(t, report, "10ms", "a phase prints its own duration")
	assert.Contains(t, report, "1.2s", "an export that took 1.2 seconds says so")
	assert.Contains(t, report, "replacement_not_running", "a non-ok phase prints its message")
	assert.Contains(t, report, "downtime  1.234s", "the measured downtime keeps its milliseconds")
	assert.Contains(t, report, "outcome   moved")
	assert.Regexp(t, `(?s)outcome\s+moved\s*$`, report,
		"the outcome is the last line: it is the answer")
}

// TestMigrateResolvesAPrefixAndAHostname pins both reference rules: a unique task
// prefix and a node hostname resolve locally, and the request carries the full
// IDs the manager resolves without guessing.
func TestMigrateResolvesAPrefixAndAHostname(t *testing.T) {
	c := migrationTestClient()
	var out bytes.Buffer

	require.NoError(t, migrateTaskWithClient(context.Background(), c, &out, "web-", "worker-b"))
	assert.Equal(t, "web-1", c.gotTask)
	assert.Equal(t, "bbbb2222", c.gotTarget)

	// A unique ID prefix resolves the same way.
	c = migrationTestClient()
	require.NoError(t, migrateTaskWithClient(context.Background(), c, &out, "web-1", "bbbb"))
	assert.Equal(t, "bbbb2222", c.gotTarget)
}

// TestMigrateRefusesAnAmbiguousTargetPrefix pins the refusal a typo gets: the
// candidates are named, and no request is sent.
func TestMigrateRefusesAnAmbiguousTargetPrefix(t *testing.T) {
	c := migrationTestClient()
	c.nodes = []v1.Node{
		{ID: "aaaa1111", Hostname: "worker-a"},
		{ID: "aaaa2222", Hostname: "worker-b"},
	}
	var out bytes.Buffer

	err := migrateTaskWithClient(context.Background(), c, &out, "web-1", "aaaa")
	typed := migrationTypedError(t, err)
	assert.Equal(t, output.CodeAmbiguous, typed.Code)
	assert.Contains(t, typed.Message, "aaaa1111", "the candidates are named")
	assert.Contains(t, typed.Message, "aaaa2222")
	assert.NotContains(t, c.calls, "migrate:web-1:aaaa", "an ambiguous target never posts")
}

// TestMigrateRefusesWhatCannotMove pins the three local refusals: a task that is
// not running, a service with more than one replica, and a target the task already
// runs on. Each is a request whose answer is already known, so none is posted.
func TestMigrateRefusesWhatCannotMove(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fakeMigrateClient)
		task   string
		target string
		reason string
	}{
		{
			name: "a task that is not running",
			mutate: func(c *fakeMigrateClient) {
				c.tasks[0].State = "shutdown"
			},
			task: "web-1", target: "worker-b", reason: "task_not_running",
		},
		{
			name: "a service with two replicas",
			mutate: func(c *fakeMigrateClient) {
				c.services["svc-1"] = v1.Service{
					ID: "svc-1", Spec: v1.ServiceSpec{Mode: "replicated", Replicas: int64Ptr(2)},
				}
			},
			task: "web-1", target: "worker-b", reason: "replicas",
		},
		{
			name: "a global service",
			mutate: func(c *fakeMigrateClient) {
				c.services["svc-1"] = v1.Service{ID: "svc-1", Spec: v1.ServiceSpec{Mode: "global"}}
			},
			task: "web-1", target: "worker-b", reason: "service_mode",
		},
		{
			name:   "the node the task already runs on",
			mutate: func(*fakeMigrateClient) {},
			task:   "web-1", target: "worker-a", reason: "target_is_source",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := migrationTestClient()
			tc.mutate(c)
			var out bytes.Buffer

			err := migrateTaskWithClient(context.Background(), c, &out, tc.task, tc.target)
			typed := migrationTypedError(t, err)
			assert.Equal(t, output.CodeInvalid, typed.Code)
			assert.Equal(t, tc.reason, typed.Details["reason"])
			for _, call := range c.calls {
				assert.NotContains(t, call, "migrate:", "a refusal never posts")
			}
			assert.Empty(t, out.String(), "a refusal prints nothing as a report")
		})
	}
}

// TestMigrateRefusesATaskWithNoService pins the one shape a task can have that no
// migration can replace: a task outside a service has no service to pin.
func TestMigrateRefusesATaskWithNoService(t *testing.T) {
	c := migrationTestClient()
	c.tasks[0].ServiceID = ""

	err := migrateTaskWithClient(context.Background(), c, &bytes.Buffer{}, "web-1", "worker-b")
	typed := migrationTypedError(t, err)
	assert.Equal(t, output.CodeInvalid, typed.Code)
	assert.Equal(t, "task_without_service", typed.Details["reason"])
}

// TestMigrateRefusesAnUnknownReference pins both miss paths: an unknown task and an
// unknown node name are typed not-found answers that name the valid form.
func TestMigrateRefusesAnUnknownReference(t *testing.T) {
	c := migrationTestClient()

	err := migrateTaskWithClient(context.Background(), c, &bytes.Buffer{}, "nope-1234", "worker-b")
	typed := migrationTypedError(t, err)
	assert.Equal(t, output.CodeNotFound, typed.Code)
	assert.Contains(t, typed.Message, "task")

	c = migrationTestClient()
	err = migrateTaskWithClient(context.Background(), c, &bytes.Buffer{}, "web-1", "worker-z")
	typed = migrationTypedError(t, err)
	assert.Equal(t, output.CodeNotFound, typed.Code)
	assert.Contains(t, typed.Message, "aaaa1111", "a miss names the nodes the cluster has")
}

// TestMigrateSurfacesTheManagerRefusalWithItsCode pins the failure contract: the
// manager's typed document is the command's error, message and code unchanged, so
// main prints it and exits with the code it names.
func TestMigrateSurfacesTheManagerRefusalWithItsCode(t *testing.T) {
	cases := []struct {
		name     string
		err      *output.Error
		exitCode int
	}{
		{
			name: "a migration already in flight",
			err: output.NewError(output.CodeConflict, "a migration of service \"svc-1\" is already in flight").
				WithDetails(map[string]any{"reason": "migration_in_flight"}),
			exitCode: 4,
		},
		{
			name: "a follower refusing the move",
			err: output.NewError(output.CodeUnavailable,
				`node "manager-1" is the swarm leader, not this manager: retry the migration there`).
				WithDetails(map[string]any{"reason": "not_leader", "leader": "manager-1"}),
			exitCode: 6,
		},
		{
			name:     "a manager with migrations disabled",
			err:      output.NewError(output.CodeNotImplemented, "live migration is not enabled on this manager"),
			exitCode: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := migrationTestClient()
			c.err = tc.err

			err := migrateTaskWithClient(context.Background(), c, &bytes.Buffer{}, "web-1", "worker-b")
			require.Error(t, err)
			assert.Equal(t, tc.err.Message, err.Error(), "the server's message is printed as it is")
			assert.Equal(t, tc.exitCode, cliExitCode(err), "the process exits with the error's code")
		})
	}
}

// TestMigrateCommandRefusesBeforeAClient pins the flag rules: a missing --to, a
// non-positive --timeout and a missing --host are all answered without dialling
// anything.
func TestMigrateCommandRefusesBeforeAClient(t *testing.T) {
	resetAPIFlags(t)

	cases := []struct {
		name string
		args []string
		host string
		want string
	}{
		{name: "no target", args: []string{"web-1"}, host: "https://manager:4244", want: "--to"},
		{name: "zero timeout", args: []string{"web-1", "--to", "worker-b", "--timeout", "0s"}, host: "https://manager:4244", want: "--timeout"},
		{name: "no host", args: []string{"web-1", "--to", "worker-b"}, host: "", want: "--host"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiHost = tc.host
			cmd := newVMMigrateCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tc.args)

			err := cmd.Execute()
			typed := migrationTypedError(t, err)
			assert.Equal(t, output.CodeUsage, typed.Code)
			assert.Contains(t, typed.Message, tc.want)
		})
	}
}

// TestMigrateCommandPostsThroughTheAPI pins the wiring end to end: the command
// resolves both references over the manager API and posts one migration, and the
// report it prints is the document the manager answered.
func TestMigrateCommandPostsThroughTheAPI(t *testing.T) {
	resetAPIFlags(t)

	var posted bool
	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/nodes":
			writeAPITestDocument(w, http.StatusOK, v1.NewNodeList([]v1.Node{
				{ID: "bbbb2222", Hostname: "worker-b", State: "ready", Availability: "active"},
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks":
			writeAPITestDocument(w, http.StatusOK, v1.NewTaskList([]v1.Task{
				{ID: "web-1", ServiceID: "svc-1", NodeID: "aaaa1111", State: "running"},
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/services/svc-1":
			writeAPITestDocument(w, http.StatusOK, v1.NewServiceDocument(v1.Service{
				ID: "svc-1", Spec: v1.ServiceSpec{Mode: "replicated", Replicas: int64Ptr(1)},
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/migrations":
			posted = true
			writeAPITestDocument(w, http.StatusCreated, migrationTestDoc())
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	apiHost, apiCA = endpoint, caFile

	cmd := newVMMigrateCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"web-1", "--to", "bbbb", "--timeout", "30s"})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "outcome   moved")
	assert.True(t, posted, "the command posted the move")
}

// migrationTypedError requires err to be one *output.Error and returns it.
func migrationTypedError(t *testing.T, err error) *output.Error {
	t.Helper()

	var typed *output.Error
	require.Truef(t, errors.As(err, &typed), "want *output.Error, got %T: %v", err, err)
	return typed
}

// TestMigrationDowntime pins the one number a report must not invent: an
// unmeasured downtime is not zero seconds.
func TestMigrationDowntime(t *testing.T) {
	assert.Equal(t, "not measured", migrationDowntime(0))
	assert.Equal(t, "1.234s", migrationDowntime(1234))
	assert.Equal(t, "12ms", migrationDowntime(12))
}

// TestPhaseDuration pins the pending phase: a phase the report never finished
// prints no duration rather than zero.
func TestPhaseDuration(t *testing.T) {
	assert.Equal(t, "-", phaseDuration(v1.MigrationPhase{Name: "verify", StartedAt: "2026-10-31T12:00:00Z"}))
	assert.Equal(t, "-", phaseDuration(v1.MigrationPhase{}))
	assert.Equal(t, "1.5s", phaseDuration(v1.MigrationPhase{
		StartedAt:  "2026-10-31T12:00:00Z",
		FinishedAt: "2026-10-31T12:00:01.500Z",
	}))
	assert.Equal(t, "-", phaseDuration(v1.MigrationPhase{
		StartedAt:  "2026-10-31T12:00:01Z",
		FinishedAt: "2026-10-31T12:00:00Z",
	}), "a report whose instants run backwards has no duration")
}

// TestMigrateTimeoutFlagIsUsed pins the flag's one effect: the command's own
// deadline, which the client reads to size its request.
func TestMigrateTimeoutFlagIsUsed(t *testing.T) {
	cmd := newVMMigrateCommand()
	require.NoError(t, cmd.ParseFlags([]string{"--timeout", "90s"}))
	value, err := cmd.Flags().GetDuration("timeout")
	require.NoError(t, err)
	assert.Equal(t, 90*time.Second, value)
	assert.Equal(t, defaultMigrateTimeout, 5*time.Minute)
}
