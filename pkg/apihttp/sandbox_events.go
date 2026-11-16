package apihttp

// This file owns the API's half of the sandbox event stream: the changes this node's
// sandbox routes publish, and the rendering of any change — theirs or a loop's — as the v1
// document the event stream writes.
//
// The bus itself is not here. It lives in pkg/sandbox (pkg/sandbox/events.go), because a
// sandbox change has two origins and only one of them is this package: the reaper taking an
// idle sandbox back and the reconcile pass finding a VMM gone are the node's own loops, and
// they publish from where they run. This package is a consumer of that bus like they are —
// it subscribes (events.go) and it publishes the changes its own routes produce — so there
// is one bus and two kinds of publisher rather than one bus per layer.
//
// # Why a sandbox event is not a cluster event
//
// Every other event this API streams comes from the cluster's watch (pkg/client, fed by
// SwarmKit): a service, a task, a node. A sandbox is deliberately not a SwarmKit object —
// it is created by a caller rather than scheduled, and its state is one node's record — so
// nothing watches it for the API, and this stream's local source is the node's own bus.
//
// # The rules a subscriber gets
//
// They are the bus's (see pkg/sandbox/events.go) and this file keeps them: a change is
// published only after the operation succeeded and carries the record as it was at that
// moment; an event has no cursor, because the cluster stream's cursor is a SwarmKit watch
// index that a sandbox change has no place in — so the stream is live-only and `since`
// never resumes across one; and publishing never blocks the operation that produced it.
//
// # What is published, and by whom
//
// A create — including each clone a fork makes — a create that left a failed record, and
// every removal, whether the API performed it or the reaper did. A reconcile pass that
// marks a sandbox failed also publishes, because a watcher that only saw the API's changes
// would otherwise have to poll to learn that a sandbox died.

import (
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox"
)

// publishSandboxEvent announces a sandbox change this node's API made: a sandbox it created
// (including each clone a fork made) or one it left failed. The change's timestamp is the
// record's own, so a watcher sees exactly what the caller who performed the operation saw.
func (s *Server) publishSandboxEvent(kind sandbox.ChangeKind, rec sandbox.Record) {
	s.cfg.SandboxEvents.Publish(sandbox.RecordChange(kind, rec, time.Now()))
}

// publishSandboxRemoval announces a sandbox this node's API removed.
func (s *Server) publishSandboxRemoval(id string) {
	s.cfg.SandboxEvents.Publish(sandbox.RemovalChange(id, time.Now()))
}

// sandboxChangeEvent renders one change from the node's bus as the v1 document this stream
// writes.
//
// The type is the change's kind spelled the way every event type is — the resource's kind,
// lowercased, then the past-tense action — so `sandbox.created`, `sandbox.failed` and
// `sandbox.removed` are one rule rather than three strings, and a kind added to the bus
// cannot silently borrow another's name. The cursor is empty (see the file comment), and
// the object is always set when there is a record: only the stream knows whether the caller
// asked for it (include=object), and it strips it otherwise.
func sandboxChangeEvent(change sandbox.Change) v1.Event {
	event := v1.NewEvent("", formatInstant(change.At), sandboxEventType(change.Kind),
		v1.EventResource{Kind: v1.KindSandbox, ID: change.ID}, "")
	if change.Record != nil {
		event.Object = sandboxDocument(*change.Record)
	}
	return event
}

// sandboxEventType is the wire name of one change kind: the sandbox resource's kind,
// lowercased, then the change's own name. A test pins each of them to the v1 constant the
// contract documents, so a renamed constant cannot drift from the stream silently.
func sandboxEventType(kind sandbox.ChangeKind) string {
	return "sandbox." + string(kind)
}
