// This file owns the Watch stream: SwarmKit's change feed mapped into the v1
// Event document. The same method serves the local control socket and a remote
// manager (the remote path uses NDJSON over HTTPS).
package client

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// watchBuffer is the per-subscriber buffer. A consumer that falls this far
// behind is disconnected; resuming from its last cursor is the client's job.
const watchBuffer = 1024

// WatchEvents opens the manager's change stream. An initial failure is returned;
// a failure after the stream started is delivered on the error channel.
func (c *Client) WatchEvents(ctx context.Context, opts v1.EventOptions) (<-chan v1.Event, <-chan error, error) {
	if c.remote != nil {
		return c.remote.watchEvents(ctx, opts)
	}
	if c.watch == nil {
		return nil, nil, output.NewError(output.CodeInternal, "watch: the local client has no watch service")
	}

	req, err := watchRequest(opts)
	if err != nil {
		return nil, nil, err
	}
	stream, err := c.watch.Watch(ctx, req)
	if err != nil {
		return nil, nil, watchError(err)
	}

	// SwarmKit reports a compacted resume cursor on the first Recv, not on the
	// Watch call. Prime one message before returning so the HTTP layer can still
	// answer 410 instead of a 200 stream with an error frame.
	first, err := receiveWatch(stream)
	if err != nil {
		return nil, nil, watchError(err)
	}

	events := make(chan v1.Event, watchBuffer)
	errs := make(chan error, 1)

	emit := func(message *api.WatchMessage) bool {
		cursor := cursorFor(message)
		for _, event := range message.Events {
			converted, ok := eventFromWatch(event, cursor, opts)
			if !ok {
				continue
			}
			select {
			case events <- converted:
			case <-ctx.Done():
				return false
			}
		}
		return true
	}

	go func() {
		defer close(events)
		defer close(errs)

		if !emit(first) {
			return
		}
		for {
			message, err := stream.Recv()
			if err != nil {
				if err != io.EOF && ctx.Err() == nil {
					errs <- watchError(err)
				}
				return
			}
			if !emit(message) {
				return
			}
		}
	}()
	return events, errs, nil
}

// watchReceiver is the one method the event loop needs from the gRPC stream, so
// the first-message error path is testable without a SwarmKit server.
type watchReceiver interface {
	Recv() (*api.WatchMessage, error)
}

func receiveWatch(stream watchReceiver) (*api.WatchMessage, error) {
	return stream.Recv()
}

// cursorFor is the batch boundary the client may safely resume from. The version
// on a WatchMessage covers the whole batch, so using it as the id of every event
// would let a reconnect in the middle of a batch skip the rest. The previous
// version re-delivers the batch (at-least-once) instead of losing it.
func cursorFor(message *api.WatchMessage) string {
	if message == nil || message.Version == nil {
		return ""
	}
	if message.Version.Index == 0 {
		return "0"
	}
	return strconv.FormatUint(message.Version.Index-1, 10)
}

// watchRequest maps the public type list and cursor onto SwarmKit's watch
// entries. An empty type list means every kind this gateway maps.
func watchRequest(opts v1.EventOptions) (*api.WatchRequest, error) {
	types := opts.Types
	if len(types) == 0 {
		types = []string{"service", "task", "node", "network", "config", "secret"}
	}

	entries := make([]*api.WatchRequest_WatchEntry, 0, len(types))
	for _, kind := range types {
		if !knownEventType(kind) {
			return nil, output.NewErrorf(output.CodeInvalid, "unsupported event type %q", kind).
				WithDetails(map[string]any{"fields": map[string]any{"types": kind}})
		}
		entries = append(entries, &api.WatchRequest_WatchEntry{
			Kind:   kind,
			Action: api.WatchActionKindCreate | api.WatchActionKindUpdate | api.WatchActionKindRemove,
		})
	}

	req := &api.WatchRequest{Entries: entries}
	if opts.Since != "" {
		index, err := strconv.ParseUint(opts.Since, 10, 64)
		if err != nil {
			return nil, output.NewErrorf(output.CodeInvalid, "invalid event cursor %q", opts.Since).
				WithDetails(map[string]any{"cursor": opts.Since})
		}
		if index > 0 {
			req.ResumeFrom = &api.Version{Index: index}
		}
	}
	return req, nil
}

// knownEventType is the closed list of Watch kinds this gateway maps to v1.
func knownEventType(kind string) bool {
	switch kind {
	case "service", "task", "node", "network", "config", "secret":
		return true
	default:
		return false
	}
}

// eventFromWatch maps one SwarmKit Watch event. A kind the v1 surface does not
// model is skipped rather than emitted as an untyped object.
func eventFromWatch(event *api.WatchMessage_Event, cursor string, opts v1.EventOptions) (v1.Event, bool) {
	if event == nil || event.Object == nil {
		return v1.Event{}, false
	}
	action := watchAction(event.Action)
	if action == "" {
		return v1.Event{}, false
	}

	switch obj := event.Object.GetObject().(type) {
	case *api.Object_Service:
		return watchServiceEvent(obj.Service, cursor, action, opts)
	case *api.Object_Task:
		return watchTaskEvent(obj.Task, cursor, action, opts)
	case *api.Object_Node:
		return watchNodeEvent(obj.Node, cursor, action, opts)
	case *api.Object_Network:
		return watchNetworkEvent(obj.Network, cursor, action, opts)
	case *api.Object_Config:
		return watchConfigEvent(obj.Config, cursor, action, opts)
	case *api.Object_Secret:
		return watchSecretEvent(obj.Secret, cursor, action, opts)
	default:
		return v1.Event{}, false
	}
}

func watchServiceEvent(service *api.Service, cursor, action string, opts v1.EventOptions) (v1.Event, bool) {
	if service == nil {
		return v1.Event{}, false
	}
	mapped := serviceFromSwarmkit(service, taskCounts{})
	out := eventDocument(cursor, action, v1.KindService, service.ID, mapped.Name,
		mapped.Revision, service.Meta, mapped.Labels)
	if opts.IncludeObject {
		out.Object = mapped
	}
	return out, true
}

func watchTaskEvent(task *api.Task, cursor, action string, opts v1.EventOptions) (v1.Event, bool) {
	if task == nil {
		return v1.Event{}, false
	}
	mapped := taskFromSwarmkit(task, nil)
	labels := task.Annotations.Labels
	if len(labels) == 0 {
		labels = task.ServiceAnnotations.Labels
	}
	out := eventDocument(cursor, action, v1.KindTask, task.ID, task.Annotations.Name,
		mapped.Revision, task.Meta, labels)
	if opts.IncludeObject {
		out.Object = mapped
	}
	return out, true
}

func watchNodeEvent(node *api.Node, cursor, action string, opts v1.EventOptions) (v1.Event, bool) {
	if node == nil {
		return v1.Event{}, false
	}
	mapped := nodeFromSwarmkit(node)
	out := eventDocument(cursor, action, v1.KindNode, node.ID, mapped.Hostname,
		mapped.Revision, node.Meta, mapped.Labels)
	if opts.IncludeObject {
		out.Object = mapped
	}
	return out, true
}

func watchNetworkEvent(network *api.Network, cursor, action string, opts v1.EventOptions) (v1.Event, bool) {
	if network == nil {
		return v1.Event{}, false
	}
	mapped := networkFromSwarmkit(network)
	out := eventDocument(cursor, action, v1.KindNetwork, network.ID, mapped.Name,
		mapped.Revision, network.Meta, mapped.Labels)
	if opts.IncludeObject {
		out.Object = mapped
	}
	return out, true
}

func watchConfigEvent(config *api.Config, cursor, action string, opts v1.EventOptions) (v1.Event, bool) {
	if config == nil {
		return v1.Event{}, false
	}
	mapped := configFromSwarmkit(config)
	if !opts.IncludeConfigValues {
		mapped.Data = nil
	}
	out := eventDocument(cursor, action, v1.KindConfig, config.ID, mapped.Name,
		mapped.Revision, config.Meta, mapped.Labels)
	if opts.IncludeObject {
		out.Object = mapped
	}
	return out, true
}

func watchSecretEvent(secret *api.Secret, cursor, action string, opts v1.EventOptions) (v1.Event, bool) {
	if secret == nil {
		return v1.Event{}, false
	}
	mapped := secretFromSwarmkit(secret)
	out := eventDocument(cursor, action, v1.KindSecret, secret.ID, mapped.Name,
		mapped.Revision, secret.Meta, mapped.Labels)
	if opts.IncludeObject {
		out.Object = mapped
	}
	return out, true
}

// eventDocument builds the common event fields. The event type is
// `<resource>.<created|updated|removed>`.
func eventDocument(cursor, action, kind, id, name, revision string, meta api.Meta, labels map[string]string) v1.Event {
	at := timestamp(meta.UpdatedAt)
	if at == "" {
		at = timestamp(meta.CreatedAt)
	}
	return v1.Event{
		SchemaVersion: v1.SchemaVersion,
		Kind:          v1.KindEvent,
		Cursor:        cursor,
		At:            at,
		Type:          strings.ToLower(kind) + "." + action,
		Resource:      v1.EventResource{Kind: kind, ID: id, Name: name},
		Revision:      revision,
		Labels:        labels,
	}
}

// watchAction maps SwarmKit's bitmap action to the contract's past tense.
func watchAction(action api.WatchActionKind) string {
	switch action {
	case api.WatchActionKindCreate:
		return "created"
	case api.WatchActionKindUpdate:
		return "updated"
	case api.WatchActionKindRemove:
		return "removed"
	default:
		return ""
	}
}

// watchError maps an expired watch cursor to the contract's 410. SwarmKit's
// raft layer reports compaction as a plain error, so the text is the only
// portable signal.
func watchError(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	if status.Code(err) == codes.OutOfRange || strings.Contains(lower, "compact") {
		return output.NewError(output.CodeCursorExpired,
			"the event cursor is older than the manager's retained log").
			WithDetails(map[string]any{"action": "relist"})
	}
	return err
}
