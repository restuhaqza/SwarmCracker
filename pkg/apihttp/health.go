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
//
// The manager never dials a node directly. The fan-out goes through the same
// broker Manager that serves exec, logs and files, so there is one node
// transport and one cluster identity.
//
// No audit record is written: reading health mutates nothing and reveals only
// the probe command the service already declares.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

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

// tasksWithHealth copies the page and attaches the health of every task it can
// reach, returning the partials for the nodes it cannot. Results land at their
// task's index, so enrichment can never reorder a page.
func (s *Server) tasksWithHealth(ctx context.Context, tasks []v1.Task) ([]v1.Task, []v1.Partial) {
	enriched := make([]v1.Task, len(tasks))
	copy(enriched, tasks)
	if len(enriched) == 0 {
		return enriched, nil
	}

	errs := make([]error, len(enriched))
	var wg sync.WaitGroup
	sem := make(chan struct{}, healthFanoutLimit)

	for i := range enriched {
		if enriched[i].NodeID == "" {
			continue // an unplaced task has no node to ask
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			health, err := s.fetchTaskHealth(ctx, enriched[i])
			if err != nil {
				errs[i] = err
				return
			}
			enriched[i].Health = health
		}(i)
	}
	wg.Wait()

	return enriched, healthPartials(enriched, errs)
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
// hosting several tasks is reported once.
func healthPartials(tasks []v1.Task, errs []error) []v1.Partial {
	var partials []v1.Partial
	index := make(map[string]int, len(errs))

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
				Retryable: true,
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
