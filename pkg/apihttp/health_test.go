package apihttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// taskAt builds a task with a deterministic createdAt/id pair for pagination.
func taskAt(id, nodeID, createdAt string) v1.Task {
	return v1.Task{ID: id, NodeID: nodeID, NodeName: nodeID, CreatedAt: createdAt}
}

func healthyTaskHealth(status string) *v1.TaskHealth {
	return &v1.TaskHealth{Status: status, CheckedAt: "2026-10-24T12:00:00Z"}
}

// getTasksWithHealth performs an authenticated task list read.
func getTasksWithHealth(t *testing.T, api *execTestAPI, target string) *httptest.ResponseRecorder {
	t.Helper()
	return api.do(t, http.MethodGet, target, withToken(api.admin))
}

func decodeTaskList(t *testing.T, rec *httptest.ResponseRecorder) v1.TaskList {
	t.Helper()

	var doc v1.TaskList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc), rec.Body.String())
	require.Equal(t, v1.KindTaskList, doc.Kind)
	return doc
}

// TestListTasksWithoutHealthIsUnchanged proves an absent health parameter keeps
// the phase-1 document byte for byte: no partial array and no health key.
func TestListTasksWithoutHealthIsUnchanged(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.tasks = []v1.Task{
		taskAt("task-1", "node-1", "2026-10-24T10:00:00Z"),
	}
	api.broker.healthFn = func(context.Context, v1.Task) (*v1.TaskHealth, error) {
		return healthyTaskHealth("healthy"), nil
	}

	absent := getTasksWithHealth(t, api, "/v1/tasks")
	explicitFalse := getTasksWithHealth(t, api, "/v1/tasks?health=false")

	require.Equal(t, http.StatusOK, absent.Code)
	require.Equal(t, http.StatusOK, explicitFalse.Code)
	require.Equal(t, absent.Body.String(), explicitFalse.Body.String(),
		"health=false must be the same document as an absent parameter")
	require.NotContains(t, absent.Body.String(), `"partial"`)
	require.NotContains(t, absent.Body.String(), `"health"`)
	require.Zero(t, api.broker.healthCalls, "health is only fetched when asked for")
}

func TestListTasksHealthInvalidIs400(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	for _, raw := range []string{"maybe", "", "1", "yes"} {
		rec := getTasksWithHealth(t, api, "/v1/tasks?health="+raw)

		require.Equal(t, http.StatusBadRequest, rec.Code, raw)
		doc := decodeErrorBody(t, rec)
		require.Equal(t, string(output.CodeInvalid), doc.Error.Code, raw)
		require.Equal(t, "health", doc.Error.Details["parameter"], raw)
		require.Zero(t, api.broker.healthCalls)
	}
}

// TestListTasksHealthWithoutBrokerIs501 proves the capability rule: without a
// broker there is no way to read a node, so the request is refused up front
// instead of answering an empty health surface.
func TestListTasksHealthWithoutBrokerIs501(t *testing.T) {
	fc := &fakeClient{tasks: []v1.Task{taskAt("task-1", "node-1", "2026-10-24T10:00:00Z")}}
	api := newTestAPI(t, fc)

	rec := api.do(t, http.MethodGet, "/v1/tasks?health=true", withToken(api.token))

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, string(output.CodeNotImplemented), doc.Error.Code)
	require.Equal(t, "health", doc.Error.Details["capability"])
}

// TestListTasksHealthAllSucceed proves the happy path: every task on a page is
// enriched, the document stays a normal TaskList and no partial is emitted.
func TestListTasksHealthAllSucceed(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.tasks = []v1.Task{
		taskAt("task-1", "node-1", "2026-10-24T10:00:00Z"),
		taskAt("task-2", "node-2", "2026-10-24T10:01:00Z"),
	}
	api.broker.healthFn = func(_ context.Context, task v1.Task) (*v1.TaskHealth, error) {
		return healthyTaskHealth(task.ID), nil
	}

	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, MediaType, rec.Header().Get("Content-Type"))
	doc := decodeTaskList(t, rec)
	require.Len(t, doc.Items, 2)
	require.Empty(t, doc.Partial)
	for i, want := range []string{"task-1", "task-2"} {
		require.NotNil(t, doc.Items[i].Health)
		require.Equal(t, want, doc.Items[i].Health.Status)
	}
	require.Equal(t, 2, api.broker.healthCalls)
}

// TestListTasksHealthOneNodeFailsIs207 proves the partial contract: one
// unreachable node yields 207 with every readable task included and exactly one
// partial per failed node, even when it hosts several tasks.
func TestListTasksHealthOneNodeFailsIs207(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.tasks = []v1.Task{
		taskAt("task-1", "node-1", "2026-10-24T10:00:00Z"),
		taskAt("task-2", "node-2", "2026-10-24T10:01:00Z"),
		taskAt("task-3", "node-1", "2026-10-24T10:02:00Z"),
	}
	api.broker.healthFn = func(_ context.Context, task v1.Task) (*v1.TaskHealth, error) {
		if task.NodeID == "node-1" {
			return nil, output.NewError(output.CodeUnreachable, "node broker is unreachable")
		}
		return healthyTaskHealth("healthy"), nil
	}

	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")

	require.Equal(t, http.StatusMultiStatus, rec.Code)
	require.Equal(t, MediaType, rec.Header().Get("Content-Type"))

	doc := decodeTaskList(t, rec)
	require.Len(t, doc.Items, 3, "a 207 still carries the full page")
	require.Equal(t, []string{"task-1", "task-2", "task-3"}, []string{
		doc.Items[0].ID, doc.Items[1].ID, doc.Items[2].ID,
	})
	require.Nil(t, doc.Items[0].Health)
	require.NotNil(t, doc.Items[1].Health)
	require.Nil(t, doc.Items[2].Health)

	require.Len(t, doc.Partial, 1, "repeated failures of one node are deduped")
	partial := doc.Partial[0]
	require.Equal(t, "node-1", partial.NodeID)
	require.Equal(t, "node-1", partial.NodeName)
	require.Equal(t, string(output.CodeUnavailable), partial.Error.Code)
	require.True(t, partial.Error.Retryable)
	require.Equal(t, rec.Header().Get(requestIDHeader), partial.Error.RequestID,
		"a partial error is a complete Error, including the requestId")
	require.Equal(t, "node-1", partial.Error.Details["nodeId"],
		"a partial error carries typed details")
	require.Contains(t, partial.Error.Message, "node-1")
	require.NotContains(t, partial.Error.Message, "127.0.0.1",
		"a partial must not expose an internal address")
}

func TestListTasksHealthMultipleNodeFailures(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.tasks = []v1.Task{
		taskAt("task-1", "node-1", "2026-10-24T10:00:00Z"),
		taskAt("task-2", "node-2", "2026-10-24T10:01:00Z"),
		taskAt("task-3", "node-3", "2026-10-24T10:02:00Z"),
	}
	api.broker.healthFn = func(_ context.Context, task v1.Task) (*v1.TaskHealth, error) {
		switch task.NodeID {
		case "node-1", "node-3":
			return nil, errors.New("node offline")
		default:
			return healthyTaskHealth("healthy"), nil
		}
	}

	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")

	require.Equal(t, http.StatusMultiStatus, rec.Code)
	doc := decodeTaskList(t, rec)
	require.Len(t, doc.Items, 3, "every readable task is still included")
	require.Len(t, doc.Partial, 2)
	require.Equal(t, "node-1", doc.Partial[0].NodeID)
	require.Equal(t, "node-3", doc.Partial[1].NodeID, "partials keep page order")
	require.NotNil(t, doc.Items[1].Health)
	require.Nil(t, doc.Items[0].Health)
	require.Nil(t, doc.Items[2].Health)
}

// TestListTasksHealthOmittedWhenNotPublished proves a task that publishes no
// health keeps the key absent instead of emitting an empty object.
func TestListTasksHealthOmittedWhenNotPublished(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.tasks = []v1.Task{taskAt("task-1", "node-1", "2026-10-24T10:00:00Z")}
	api.broker.healthFn = func(context.Context, v1.Task) (*v1.TaskHealth, error) {
		return nil, nil
	}

	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), `"health"`,
		"a task without a healthcheck must omit the field, not emit {}")
	doc := decodeTaskList(t, rec)
	require.Nil(t, doc.Items[0].Health)
}

// TestListTasksHealthSkipsTasksWithoutNode proves an unplaced task is not sent
// to the broker: there is no node to ask.
func TestListTasksHealthSkipsTasksWithoutNode(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.tasks = []v1.Task{
		{ID: "task-pending", CreatedAt: "2026-10-24T09:00:00Z"},
		taskAt("task-1", "node-1", "2026-10-24T10:00:00Z"),
	}
	api.broker.healthFn = func(context.Context, v1.Task) (*v1.TaskHealth, error) {
		return healthyTaskHealth("healthy"), nil
	}

	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, api.broker.healthCalls)
	doc := decodeTaskList(t, rec)
	require.Len(t, doc.Items, 2)
	require.Nil(t, doc.Items[0].Health)
	require.NotNil(t, doc.Items[1].Health)
}

// TestListTasksHealthPreservesPagination proves health is fetched for the
// resolved page only and never reorders the list.
func TestListTasksHealthPreservesPagination(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.tasks = []v1.Task{
		taskAt("task-1", "node-1", "2026-10-24T10:00:00Z"),
		taskAt("task-2", "node-1", "2026-10-24T10:01:00Z"),
		taskAt("task-3", "node-1", "2026-10-24T10:02:00Z"),
	}
	api.broker.healthFn = func(_ context.Context, task v1.Task) (*v1.TaskHealth, error) {
		return healthyTaskHealth(task.ID), nil
	}

	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true&limit=2")

	require.Equal(t, http.StatusOK, rec.Code)
	doc := decodeTaskList(t, rec)
	require.Len(t, doc.Items, 2)
	require.Equal(t, "task-1", doc.Items[0].ID)
	require.Equal(t, "task-2", doc.Items[1].ID)
	require.NotNil(t, doc.Next, "the page cursor must survive enrichment")
	require.Equal(t, 2, api.broker.healthCalls, "health is fetched for the page, not the stream")
}

// TestListTasksHealthFanoutIsBounded proves the fan-out is concurrent but
// bounded: a page cannot open one connection per task.
func TestListTasksHealthFanoutIsBounded(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	const total = 25
	for i := range total {
		api.client.tasks = append(api.client.tasks,
			taskAt(fmt.Sprintf("task-%02d", i), "node-1", fmt.Sprintf("2026-10-24T10:%02d:00Z", i)))
	}

	var inFlight, peak int32
	api.broker.healthFn = func(_ context.Context, task v1.Task) (*v1.TaskHealth, error) {
		current := atomic.AddInt32(&inFlight, 1)
		for {
			seen := atomic.LoadInt32(&peak)
			if current <= seen || atomic.CompareAndSwapInt32(&peak, seen, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return healthyTaskHealth(task.ID), nil
	}

	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")

	require.Equal(t, http.StatusOK, rec.Code)
	doc := decodeTaskList(t, rec)
	require.Len(t, doc.Items, total)
	require.Equal(t, total, api.broker.healthCalls)
	require.LessOrEqual(t, peak, int32(healthFanoutLimit),
		"the fan-out must not exceed its concurrency bound")
	require.Greater(t, peak, int32(1), "health fetches must run concurrently")
}

// TestGetTaskHealthSucceeds proves the single-task read is enriched in place.
func TestGetTaskHealthSucceeds(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.task = v1.Task{ID: "task-1", NodeID: "node-1"}
	api.broker.healthFn = func(context.Context, v1.Task) (*v1.TaskHealth, error) {
		return &v1.TaskHealth{
			Status:        "failing",
			FailingStreak: 2,
			Output:        "connection refused",
			CheckedAt:     "2026-10-24T12:00:00Z",
		}, nil
	}

	rec := api.do(t, http.MethodGet, "/v1/tasks/task-1?health=true", withToken(api.admin))

	require.Equal(t, http.StatusOK, rec.Code)
	var doc v1.TaskDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.NotNil(t, doc.Health)
	require.Equal(t, "failing", doc.Health.Status)
	require.Equal(t, 2, doc.Health.FailingStreak)
	require.Equal(t, 1, api.broker.healthCalls)
}

// TestGetTaskHealthOmitted proves an unpublished healthcheck leaves the key out.
func TestGetTaskHealthOmitted(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.task = v1.Task{ID: "task-1", NodeID: "node-1"}
	api.broker.healthFn = func(context.Context, v1.Task) (*v1.TaskHealth, error) {
		return nil, nil
	}

	rec := api.do(t, http.MethodGet, "/v1/tasks/task-1?health=true", withToken(api.admin))

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), `"health"`)
}

// TestGetTaskHealthBrokerFailureIs503 proves a single-task read that cannot
// reach its node is unavailable, never a partial document.
func TestGetTaskHealthBrokerFailureIs503(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.task = v1.Task{ID: "task-1", NodeID: "node-1"}
	api.broker.healthFn = func(context.Context, v1.Task) (*v1.TaskHealth, error) {
		return nil, output.NewError(output.CodeUnreachable, "node 10.0.0.5:4245 is unreachable")
	}

	rec := api.do(t, http.MethodGet, "/v1/tasks/task-1?health=true", withToken(api.admin))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	doc := decodeErrorBody(t, rec)
	require.Equal(t, string(output.CodeUnavailable), doc.Error.Code)
	require.Equal(t, "node-1", doc.Error.Details["nodeId"],
		"the single-task 503 names the node, like a list partial")
	require.Nil(t, doc.Error.Details["partial"])
	require.NotContains(t, rec.Body.String(), "10.0.0.5",
		"the public error must not expose an internal address")
}

func TestGetTaskHealthInvalidIs400(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)

	rec := api.do(t, http.MethodGet, "/v1/tasks/task-1?health=maybe", withToken(api.admin))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	doc := decodeErrorBody(t, rec)
	require.Equal(t, "health", doc.Error.Details["parameter"])
	require.Zero(t, api.broker.healthCalls)
}

func TestGetTaskHealthWithoutBrokerIs501(t *testing.T) {
	fc := &fakeClient{task: v1.Task{ID: "task-1", NodeID: "node-1"}}
	api := newTestAPI(t, fc)

	rec := api.do(t, http.MethodGet, "/v1/tasks/task-1?health=true", withToken(api.token))

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	var doc v1.ErrorDocument
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Equal(t, "health", doc.Error.Details["capability"])
}

// TestGetTaskHealthSkipsTasksWithoutNode proves an unplaced task is answered
// from the manager alone.
func TestGetTaskHealthSkipsTasksWithoutNode(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.task = v1.Task{ID: "task-pending"}

	rec := api.do(t, http.MethodGet, "/v1/tasks/task-pending?health=true", withToken(api.admin))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, api.broker.healthCalls)
}

// TestGetTaskHealthSeesTheRequestID proves the broker receives the request's own
// context, so the manager can carry the public request ID to the node.
func TestGetTaskHealthSeesTheRequestID(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.task = v1.Task{ID: "task-1", NodeID: "node-1"}

	var gotID string
	api.broker.healthFn = func(ctx context.Context, _ v1.Task) (*v1.TaskHealth, error) {
		gotID = RequestIDFromContext(ctx)
		return nil, nil
	}

	rec := api.do(t, http.MethodGet, "/v1/tasks/task-1?health=true", withToken(api.admin))

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, gotID)
	require.Equal(t, rec.Header().Get(requestIDHeader), gotID)
}

// TestGetTaskHealthDeadlineIsBounded proves a slow node cannot hold a single-task
// read open beyond the per-task deadline.
func TestGetTaskHealthDeadlineIsBounded(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.task = v1.Task{ID: "task-1", NodeID: "node-1"}

	var deadline time.Time
	api.broker.healthFn = func(ctx context.Context, _ v1.Task) (*v1.TaskHealth, error) {
		deadline, _ = ctx.Deadline()
		return nil, nil
	}

	started := time.Now()
	rec := api.do(t, http.MethodGet, "/v1/tasks/task-1?health=true", withToken(api.admin))

	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, deadline.IsZero(), "the broker call must carry a deadline")
	require.WithinDuration(t, started.Add(healthPerTaskTimeout), deadline, time.Second)
}

// TestListTasksHealthPageBudgetStopsASlowPage proves the page has its own
// wall-clock budget: a node that never answers is bounded by it, not by its
// per-task deadline times the number of waves, and the page still answers 207
// with the failed node as a partial.
func TestListTasksHealthPageBudgetStopsASlowPage(t *testing.T) {
	const budget = 100 * time.Millisecond

	api := newExecTestAPIWithConfig(t, nil, nil, func(cfg *Config) {
		cfg.HealthPageBudget = budget
	})
	api.client.tasks = []v1.Task{
		taskAt("task-1", "node-slow", "2026-10-24T10:00:00Z"),
		taskAt("task-2", "node-slow", "2026-10-24T10:01:00Z"),
	}
	api.broker.healthFn = func(ctx context.Context, _ v1.Task) (*v1.TaskHealth, error) {
		<-ctx.Done() // a node that never answers
		return nil, ctx.Err()
	}

	started := time.Now()
	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")
	elapsed := time.Since(started)

	require.Equal(t, http.StatusMultiStatus, rec.Code)
	require.Less(t, elapsed, healthPerTaskTimeout,
		"the page budget, not the per-task deadline, must end the fan-out")
	require.Less(t, elapsed, time.Second)

	doc := decodeTaskList(t, rec)
	require.Len(t, doc.Items, 2, "a 207 still carries the full page")
	require.Len(t, doc.Partial, 1, "one partial per failed node")
	require.Equal(t, "node-slow", doc.Partial[0].NodeID)
	require.Equal(t, string(output.CodeUnavailable), doc.Partial[0].Error.Code)
	require.True(t, doc.Partial[0].Error.Retryable)
	for _, item := range doc.Items {
		require.Nil(t, item.Health)
	}
}

// TestListTasksHealthPageBudgetKeepsAnswers proves a mixed page keeps the health
// a readable task returned while the slow node becomes a partial when the page
// budget expires.
func TestListTasksHealthPageBudgetKeepsAnswers(t *testing.T) {
	const budget = 100 * time.Millisecond

	api := newExecTestAPIWithConfig(t, nil, nil, func(cfg *Config) {
		cfg.HealthPageBudget = budget
	})
	api.client.tasks = []v1.Task{
		taskAt("task-1", "node-fast", "2026-10-24T10:00:00Z"),
		taskAt("task-2", "node-slow", "2026-10-24T10:01:00Z"),
	}
	api.broker.healthFn = func(ctx context.Context, task v1.Task) (*v1.TaskHealth, error) {
		if task.NodeID == "node-slow" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return healthyTaskHealth("healthy"), nil
	}

	started := time.Now()
	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")
	require.Less(t, time.Since(started), time.Second)

	require.Equal(t, http.StatusMultiStatus, rec.Code)
	doc := decodeTaskList(t, rec)
	require.Len(t, doc.Items, 2)
	require.NotNil(t, doc.Items[0].Health, "a task that answered keeps its health")
	require.Equal(t, "healthy", doc.Items[0].Health.Status)
	require.Nil(t, doc.Items[1].Health)
	require.Len(t, doc.Partial, 1)
	require.Equal(t, "node-slow", doc.Partial[0].NodeID)
}

// TestListTasksHealthPageBudgetDoesNotWaitForAStuckBroker proves the handler
// returns when the budget expires even if the broker ignores its context: the
// worker deposits its result into the buffered channel and exits on its own.
func TestListTasksHealthPageBudgetDoesNotWaitForAStuckBroker(t *testing.T) {
	const budget = 50 * time.Millisecond

	api := newExecTestAPIWithConfig(t, nil, nil, func(cfg *Config) {
		cfg.HealthPageBudget = budget
	})
	api.client.tasks = []v1.Task{taskAt("task-1", "node-1", "2026-10-24T10:00:00Z")}

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	api.broker.healthFn = func(context.Context, v1.Task) (*v1.TaskHealth, error) {
		<-release // misbehaves: does not observe cancellation
		return healthyTaskHealth("late"), nil
	}

	started := time.Now()
	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")
	require.Less(t, time.Since(started), time.Second,
		"the handler must not wait for a broker that ignores the budget")

	require.Equal(t, http.StatusMultiStatus, rec.Code)
	doc := decodeTaskList(t, rec)
	require.Len(t, doc.Partial, 1)
	require.Equal(t, "node-1", doc.Partial[0].NodeID)
}

// TestListTasksHealthOrderingIsStable is a smoke test that enrichment cannot
// reorder a page even when the answers arrive out of order.
func TestListTasksHealthOrderingIsStable(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.tasks = []v1.Task{
		taskAt("task-1", "node-1", "2026-10-24T10:00:00Z"),
		taskAt("task-2", "node-2", "2026-10-24T10:01:00Z"),
		taskAt("task-3", "node-3", "2026-10-24T10:02:00Z"),
	}

	var mu sync.Mutex
	var calls int
	api.broker.healthFn = func(_ context.Context, task v1.Task) (*v1.TaskHealth, error) {
		mu.Lock()
		calls++
		slow := calls == 1
		mu.Unlock()
		if slow {
			time.Sleep(10 * time.Millisecond)
		}
		return healthyTaskHealth(task.ID), nil
	}

	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")

	require.Equal(t, http.StatusOK, rec.Code)
	doc := decodeTaskList(t, rec)
	require.Equal(t, []string{"task-1", "task-2", "task-3"}, []string{
		doc.Items[0].ID, doc.Items[1].ID, doc.Items[2].ID,
	})
	for _, item := range doc.Items {
		require.NotNil(t, item.Health)
		require.Equal(t, item.ID, item.Health.Status)
	}
}

// TestListTasksHealthPartialMessageIsStable pins the wording an SDK sees: it
// names the node, never the transport detail.
func TestListTasksHealthPartialMessageIsStable(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.tasks = []v1.Task{taskAt("task-1", "node-1", "2026-10-24T10:00:00Z")}
	api.broker.healthFn = func(context.Context, v1.Task) (*v1.TaskHealth, error) {
		return nil, errors.New("dial tcp 10.0.0.5:4245: connect: connection refused")
	}

	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")

	require.Equal(t, http.StatusMultiStatus, rec.Code)
	doc := decodeTaskList(t, rec)
	require.Len(t, doc.Partial, 1)
	require.NotContains(t, doc.Partial[0].Error.Message, "10.0.0.5",
		"the stable message must not be replaced by a transport error")
	require.NotContains(t, rec.Body.String(), "connect: connection refused")
	require.True(t, strings.HasPrefix(doc.Partial[0].Error.Message, "health is unavailable"),
		"the partial message is fixed text: %q", doc.Partial[0].Error.Message)
}

// captureHealthLogs redirects the package logger for one test and returns the
// buffer. The fan-out logs one line per recovered panic, and nothing else in
// this package logs, so every line here is that line.
func captureHealthLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = previous })
	return &buf
}

// TestListTasksHealthPanicIsContained proves a panicking health read cannot take
// the manager down. The fan-out goroutine is not a handler goroutine, so net/http
// contains nothing here: the recover has to live inside the goroutine. The
// recovered fetch must still settle its task, because the collector waits for
// exactly `pending` results — without one it would hold the page open until the
// budget expired.
func TestListTasksHealthPanicIsContained(t *testing.T) {
	api := newExecTestAPI(t, nil, nil)
	api.client.tasks = []v1.Task{
		taskAt("task-1", "node-1", "2026-10-24T10:00:00Z"),
		taskAt("task-2", "node-2", "2026-10-24T10:01:00Z"),
	}
	logs := captureHealthLogs(t)
	api.broker.healthFn = func(_ context.Context, task v1.Task) (*v1.TaskHealth, error) {
		if task.ID == "task-2" {
			panic("broker assertion blew up")
		}
		return healthyTaskHealth("healthy"), nil
	}

	started := time.Now()
	rec := getTasksWithHealth(t, api, "/v1/tasks?health=true")
	elapsed := time.Since(started)

	require.Less(t, elapsed, time.Second,
		"a recovered panic must settle its task instead of waiting out the page budget")
	require.Equal(t, http.StatusMultiStatus, rec.Code)

	doc := decodeTaskList(t, rec)
	require.Len(t, doc.Items, 2, "the whole page is still returned")
	require.NotNil(t, doc.Items[0].Health, "a healthy node keeps its health")
	require.Equal(t, "healthy", doc.Items[0].Health.Status)
	require.Nil(t, doc.Items[1].Health)

	require.Len(t, doc.Partial, 1, "the panicking node is a partial, like a failed fetch")
	partial := doc.Partial[0]
	require.Equal(t, "node-2", partial.NodeID)
	require.Equal(t, string(output.CodeUnavailable), partial.Error.Code)
	require.True(t, partial.Error.Retryable)
	require.Equal(t, "node-2", partial.Error.Details["nodeId"])
	require.NotContains(t, rec.Body.String(), "broker assertion blew up",
		"the panic value belongs in the log, never on the wire")

	logged := logs.String()
	require.Contains(t, logged, "task-2", "the log names the task that panicked")
	require.Contains(t, logged, "node-2", "the log names its node")
	require.Contains(t, logged, "broker assertion blew up", "the log carries the panic value")
	require.Contains(t, logged, "goroutine",
		"the log carries the recovered panic's stack: %q", logged)
	// A frame from the panic site, not the `created by ... tasksWithHealth`
	// trailer, which every goroutine of the fan-out would satisfy. Go 1.27
	// prints the panic frame as `panic(...)`, not `runtime.gopanic`, so the
	// fetch frame is the stable proof.
	require.Contains(t, logged, "fetchTaskHealth",
		"the stack's frames lead through the fetch that panicked: %q", logged)
}

// TestListTasksHealthPanicInAQueuedFetchIsContained proves the same recover also
// covers a fetch that panicked after it had queued for a worker slot. The defer
// is installed at the top of the goroutine, before the semaphore select, so there
// is no point between "goroutine started" and "result deposited" that escapes it.
//
// Which goroutine wins one of the first ten slots is the scheduler's choice, so
// queueing cannot be forced from outside the fan-out: a panicking fetch that
// merely sits late in the page still ran in the first wave in 82 of 100 runs
// here (199 of 200 in the review's probe). Instead the fetch proves the premise
// itself — it only panics once every slot was already held — and the test retries
// until one attempt genuinely queues, failing outright if none does. The
// assertions below therefore only ever run against a panic behind the semaphore.
func TestListTasksHealthPanicInAQueuedFetchIsContained(t *testing.T) {
	const (
		budget = 2 * time.Second
		// A panicking fetch that wins a first-wave slot is discarded as an
		// attempt that proves nothing. Measured on this host: 1 to 10 attempts
		// before one queues, so 100 is a wide margin against a loaded machine
		// (and a run that never queues fails in well under the test timeout).
		attempts = 100
	)

	logs := captureHealthLogs(t)

	var (
		rec     *httptest.ResponseRecorder
		doc     v1.TaskList
		broker  *fakeBroker
		elapsed time.Duration
		tries   int
	)

	for ; tries < attempts && rec == nil; tries++ {
		api := newExecTestAPIWithConfig(t, nil, nil, func(cfg *Config) {
			cfg.HealthPageBudget = budget
		})
		for i := range healthFanoutLimit {
			api.client.tasks = append(api.client.tasks,
				taskAt(fmt.Sprintf("task-%02d", i), "node-1", fmt.Sprintf("2026-10-24T10:%02d:00Z", i)))
		}
		api.client.tasks = append(api.client.tasks,
			taskAt("task-panic", "node-2", "2026-10-24T11:00:00Z"))

		// allInFlight closes only once every slot is held, and a slot can only
		// free after `release`, which happens after that close — so a panicking
		// fetch that observes the close ran behind the semaphore.
		allInFlight := make(chan struct{})
		var holding sync.WaitGroup
		holding.Add(healthFanoutLimit)
		released := make(chan struct{})
		var once sync.Once
		release := func() { once.Do(func() { close(released) }) }
		go func() {
			holding.Wait()
			close(allInFlight)
			release()
		}()

		var queued atomic.Bool
		api.broker.healthFn = func(_ context.Context, task v1.Task) (*v1.TaskHealth, error) {
			if task.ID == "task-panic" {
				select {
				case <-allInFlight:
					queued.Store(true)
					panic("panicked after queueing for a worker slot")
				default:
					// A free slot: this goroutine was not behind the semaphore,
					// so answering normally is the only honest outcome. The
					// test discards the attempt and tries the ordering again.
					return healthyTaskHealth("healthy"), nil
				}
			}
			holding.Done()
			<-released
			return healthyTaskHealth("healthy"), nil
		}

		started := time.Now()
		attempt := getTasksWithHealth(t, api, "/v1/tasks?health=true")
		elapsed = time.Since(started)
		release()
		if queued.Load() {
			rec = attempt
			doc = decodeTaskList(t, attempt)
			broker = api.broker
		}
	}

	require.NotNil(t, rec, "the panicking fetch never queued on the fan-out "+
		"semaphore in %d attempts, so this test would prove nothing beyond a panic "+
		"in a fetch", tries)

	require.Less(t, elapsed, budget,
		"a panic behind the fan-out limit still settles its task")

	require.Equal(t, http.StatusMultiStatus, rec.Code)
	require.Len(t, doc.Items, healthFanoutLimit+1)
	for i := range doc.Items {
		if doc.Items[i].ID == "task-panic" {
			require.Nil(t, doc.Items[i].Health, "the panicking fetch kept no health")
			continue
		}
		require.NotNil(t, doc.Items[i].Health, "the slot holders keep their health")
	}
	require.Len(t, doc.Partial, 1)
	require.Equal(t, "node-2", doc.Partial[0].NodeID)
	broker.healthMu.Lock()
	healthCalls := broker.healthCalls
	broker.healthMu.Unlock()
	require.Equal(t, healthFanoutLimit+1, healthCalls,
		"every placed task is still fetched")
	require.Contains(t, logs.String(), "task-panic")
}
