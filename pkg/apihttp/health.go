package apihttp

// This file owns the health capability of the task reads: ?health=true enriches
// a task document with the live healthcheck state the node publishes, for one
// task or for every task on a page.
//
// Health is best-effort enrichment, so its failure contract differs from a plain
// read:
//   - A page that loses one node stays a normal TaskList document, answered 207
//     with a `partial` array. The readable tasks are still returned, and health
//     is simply absent for the tasks whose node could not be read.
//   - A single-task read has no partial to point at, so an unreachable node is
//     503 unavailable.
//   - A task that publishes no healthcheck keeps the key off the wire: an empty
//     object would claim an observation that does not exist.
//   - The whole fan-out of one page shares a wall-clock budget, not only a
//     per-task deadline: a large page is many waves of fetches, and a response
//     must not be held open for all of them. Tasks still unanswered when the
//     budget expires become node partials, exactly like a failed fetch.
//
// The manager never dials a node directly. The fan-out goes through the same
// broker Manager that serves exec, logs and files, so there is one node
// transport and one cluster identity.
//
// No audit record is written: reading health mutates nothing and reveals only
// the probe command the service already declares.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/rs/zerolog/log"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// healthCapability is both the query parameter and the advertised capability
	// name. One string keeps the two from drifting.
	healthCapability = "health"
	// healthFanoutLimit bounds the concurrent node fetches of one page. Health is
	// enrichment: a large page must not open one connection per task.
	healthFanoutLimit = 10
	// healthPerTaskTimeout bounds one task's fetch, so a node that stops
	// answering cannot hold a page open.
	healthPerTaskTimeout = 2 * time.Second
	// healthPageBudget is the default wall-clock budget of one page's whole
	// health fan-out. The per-task deadline bounds a single fetch; this bounds
	// the response. At the default limit a page is several waves of fetches, so
	// without it a slow cluster could hold a response for tens of seconds.
	// Config.HealthPageBudget overrides it.
	healthPageBudget = 5 * time.Second
)

// healthRequested reports whether the caller asked for health enrichment. An
// absent parameter means no; a present value must be exactly `true` or `false`,
// which is how OpenAPI serializes a query boolean. Anything else is a typed 400
// rather than silently unenriched data.
func healthRequested(r *http.Request) (bool, error) {
	values := r.URL.Query()
	if !values.Has(healthCapability) {
		return false, nil
	}

	switch values.Get(healthCapability) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, output.NewError(output.CodeInvalid,
			"health must be true or false").
			WithDetails(map[string]any{"parameter": healthCapability})
	}
}

// healthFetch is one task's completed (or abandoned) health read.
type healthFetch struct {
	index  int
	health *v1.TaskHealth
	err    error
}

// errHealthPanicked settles the task whose fetch panicked. Only its non-nil-ness
// reaches the page: a partial's message is fixed text, so the panic value never
// travels further than the log.
var errHealthPanicked = errors.New("health fetch panicked")

// tasksWithHealth copies the page and attaches the health of every task it can
// reach, returning the partials for the nodes it cannot. Results land at their
// task's index, so enrichment can never reorder a page.
//
// The fan-out runs under one page budget. Every per-task context derives from it,
// so an expiring page cancels the fetches still in flight, and the tasks that had
// not answered become partials instead of holding the response. The handler never
// waits past the budget: a worker that has not finished deposits its result into a
// buffered channel nobody reads and exits on the cancelled context.
func (s *Server) tasksWithHealth(ctx context.Context, tasks []v1.Task) ([]v1.Task, []v1.Partial) {
	enriched := make([]v1.Task, len(tasks))
	copy(enriched, tasks)
	if len(enriched) == 0 {
		return enriched, nil
	}

	pageCtx, cancel := context.WithTimeout(ctx, s.cfg.HealthPageBudget)
	defer cancel()

	// Buffered to the page size, so a worker can always deposit its result and
	// exit even after the handler stopped collecting.
	results := make(chan healthFetch, len(enriched))
	sem := make(chan struct{}, healthFanoutLimit)

	pending := 0
	for i := range enriched {
		if enriched[i].NodeID == "" {
			continue // an unplaced task has no node to ask
		}
		pending++
		go func(i int) {
			// A panic in this goroutine is fatal to the whole manager: it is
			// not a handler goroutine, so net/http contains nothing here.
			// Recover at the top, before the slot select, so a panic anywhere
			// in the fetch — including one that happens after queueing for a
			// worker slot — costs one node's health instead of the daemon.
			//
			// The recovered fetch still deposits a result: the collector waits
			// for exactly `pending` results, so a panic that settled nothing
			// would hold the page open until the budget expired.
			defer func() {
				if r := recover(); r != nil {
					log.Error().
						Str("task_id", enriched[i].ID).
						Str("node_id", enriched[i].NodeID).
						Interface("panic", r).
						Str("stack", string(debug.Stack())).
						Msg("health fan-out: fetch panicked and was recovered; its node becomes a partial")
					// Last statement of the goroutine: a second deposit would
					// over-count `collected`, so collection could end early and
					// leave a task with neither health nor a partial.
					results <- healthFetch{index: i, err: errHealthPanicked}
				}
			}()

			// Waiting for a worker slot counts against the page budget: a
			// queue of slow fetches must not outlive it.
			select {
			case sem <- struct{}{}:
			case <-pageCtx.Done():
				results <- healthFetch{index: i, err: pageCtx.Err()}
				return
			}
			defer func() { <-sem }()

			health, err := s.fetchTaskHealth(pageCtx, enriched[i])
			results <- healthFetch{index: i, health: health, err: err}
		}(i)
	}

	errs := make([]error, len(enriched))
	settled := make([]bool, len(enriched))
	apply := func(fetch healthFetch) {
		settled[fetch.index] = true
		if fetch.err != nil {
			errs[fetch.index] = fetch.err
			return
		}
		enriched[fetch.index].Health = fetch.health
	}

collect:
	for collected := 0; collected < pending; {
		select {
		case fetch := <-results:
			collected++
			apply(fetch)
		case <-pageCtx.Done():
			// Take what already answered so a completed fetch is never
			// downgraded to a partial, then stop waiting for the rest.
			for {
				select {
				case fetch := <-results:
					collected++
					apply(fetch)
				default:
					break collect
				}
			}
		}
	}

	for i := range enriched {
		if !settled[i] && enriched[i].NodeID != "" {
			errs[i] = pageCtx.Err()
		}
	}

	return enriched, healthPartials(ctx, enriched, errs)
}

// taskWithHealth attaches one task's health. A broker failure is returned to the
// caller, which answers 503: a single-task read has no partial to point at.
func (s *Server) taskWithHealth(ctx context.Context, task v1.Task) (v1.Task, error) {
	if task.NodeID == "" {
		return task, nil
	}

	health, err := s.fetchTaskHealth(ctx, task)
	if err != nil {
		return task, err
	}
	task.Health = health
	return task, nil
}

// fetchTaskHealth performs one health read under the per-task deadline.
func (s *Server) fetchTaskHealth(ctx context.Context, task v1.Task) (*v1.TaskHealth, error) {
	callCtx, cancel := context.WithTimeout(ctx, healthPerTaskTimeout)
	defer cancel()
	return s.cfg.Broker.Health(callCtx, task)
}

// healthPartials returns one partial per failed node, in page order, so a node
// hosting several tasks is reported once. Each partial's error is a complete
// Error object: the page's requestId, retryability and typed details travel with
// it, exactly as they would on an ErrorDocument.
func healthPartials(ctx context.Context, tasks []v1.Task, errs []error) []v1.Partial {
	var partials []v1.Partial
	index := make(map[string]int, len(errs))
	requestID := RequestIDFromContext(ctx)

	for i, err := range errs {
		if err == nil {
			continue
		}
		nodeID := tasks[i].NodeID
		if at, dup := index[nodeID]; dup {
			// A later task on the same node may carry the name the first one
			// lacked; filling it in keeps one partial usable.
			if partials[at].NodeName == "" {
				partials[at].NodeName = tasks[i].NodeName
			}
			continue
		}

		index[nodeID] = len(partials)
		partials = append(partials, v1.Partial{
			NodeID:   nodeID,
			NodeName: tasks[i].NodeName,
			Error: v1.Error{
				Code:      string(output.CodeUnavailable),
				Message:   healthPartialMessage(tasks[i]),
				RequestID: requestID,
				Retryable: true,
				Details:   map[string]any{"nodeId": nodeID},
			},
		})
	}
	return partials
}

// healthPartialMessage is fixed text naming the node. A partial never repeats
// the transport error, which can carry an internal address.
func healthPartialMessage(task v1.Task) string {
	name := task.NodeName
	if name == "" {
		name = task.NodeID
	}
	return fmt.Sprintf("health is unavailable for node %q", name)
}
