// This file owns the node-local sandbox event stream: the bus a sandbox change is
// published on, and the subscription GET /v1/events adds to its select loop.
//
// # Why a sandbox event is not a cluster event
//
// Every other event this API streams comes from the cluster's watch (pkg/client,
// fed by SwarmKit): a service, a task, a node. A sandbox is deliberately not a
// SwarmKit object — it is created by a caller rather than scheduled, and its state
// is one node's record (pkg/sandbox) — so nothing watches it for the API. The bus
// here is that missing producer, and it is deliberately the smallest one that can
// work: a subscriber set and a publish that never blocks.
//
// # The rules a subscriber gets
//
//   - A change is published only after the operation succeeded, and what it
//     carries is the record as it was at that moment. A watcher therefore sees the
//     same document a caller who created the sandbox got.
//   - An event has no cursor. The cluster stream's cursor is a SwarmKit watch
//     index, and a sandbox change has no place in that log: reusing the field would
//     let a client resume the cluster stream from an index that means nothing, and
//     dropping the field would break the envelope. So a sandbox event is live-only:
//     it is delivered to whoever is watching when it happens, and it is not
//     replayed. `since` therefore never resumes across a sandbox event, which is
//     what the stream's own documentation says.
//   - Publishing never blocks and never fails the operation that produced it. A
//     watcher that cannot keep up with its buffer loses events rather than stalling
//     a create, and the record — which is on disk — remains the truth.
//
// # What is not published yet
//
// This bus is fed by the API, so it carries what the API did: a sandbox created
// (including each clone a fork makes), a create that failed, and a sandbox removed.
// A removal the reaper performs, or a reconcile pass that marks a lost sandbox
// failed, happens in the daemon's own loops and is not on this stream: publishing
// those needs the publisher to live in pkg/sandbox, the node's writer, which is the
// next step for this stream and is called out in the sandbox guide.
package apihttp

import (
	"sync"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox"
)

// sandboxEventBuffer is how many events one subscriber may fall behind by. It is
// small on purpose: an event is a notification, and a watcher that is more than a
// few events behind is better served by reading the sandboxes themselves than by
// replaying a stale queue.
const sandboxEventBuffer = 64

// SandboxEvents is the node's live stream of sandbox changes.
//
// It is safe for concurrent use by any number of publishers (the sandbox routes)
// and subscribers (the event streams). A subscription is a channel plus the
// function that ends it, and the two are the whole lifecycle: no cursor, no
// replay, no history.
type SandboxEvents struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]chan v1.Event
}

// NewSandboxEvents returns an empty bus. Every server gets one in New, so a
// sandbox route never has to check whether anyone is listening.
func NewSandboxEvents() *SandboxEvents {
	return &SandboxEvents{subs: make(map[int]chan v1.Event)}
}

// Subscribe returns a channel of the events published from now on, and the
// function that ends the subscription.
//
// The channel is closed by the cancel function, so a reader's range or its select
// sees the end of the subscription as a closed channel. Cancel is idempotent: a
// handler defers it and may also call it on the way out.
func (b *SandboxEvents) Subscribe() (<-chan v1.Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan v1.Event, sandboxEventBuffer)
	id := b.nextID
	b.nextID++
	b.subs[id] = ch

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if _, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(ch)
			}
		})
	}
	return ch, cancel
}

// Publish delivers one event to every current subscriber.
//
// It never blocks: a subscriber whose buffer is full loses this event rather than
// holding the operation that published it. That is the only choice that keeps a
// created sandbox from waiting on a watcher, and the loss is bounded because the
// record itself is the durable answer.
func (b *SandboxEvents) Publish(event v1.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, ch := range b.subs {
		select {
		case ch <- event:
		default:
		}
	}
}

// sandboxEvent builds the v1 document for one sandbox change, with the document it
// happened to.
//
// The shape is the cluster stream's, field for field (pkg/client.eventDocument):
// the type is the resource's kind, lowercased, and the past-tense action; the
// resource names the sandbox. The cursor is empty — see the file comment — and the
// object is always carried here, because only the stream knows whether the caller
// asked for it (include=object) and it strips it otherwise.
func sandboxEvent(typ string, id string, at time.Time, object v1.Sandbox) v1.Event {
	event := v1.NewEvent("", formatInstant(at), typ, v1.EventResource{Kind: v1.KindSandbox, ID: id}, "")
	event.Object = object
	return event
}

// publishSandboxEvent announces a sandbox this node's API created, forked, or left
// failed. The document and the timestamp are the record's own, so a watcher sees
// exactly what the caller who performed the operation saw.
func (s *Server) publishSandboxEvent(typ string, rec sandbox.Record) {
	at := rec.Updated
	if at.IsZero() {
		at = time.Now().UTC()
	}
	s.cfg.SandboxEvents.Publish(sandboxEvent(typ, rec.ID, at, sandboxDocument(rec)))
}

// publishSandboxRemoval announces a sandbox this node's API removed.
//
// It carries no object, deliberately: the record is deleted by the time this runs,
// and a sandbox's ID is its whole identity — it has no name and no labels — so an
// empty document would suggest a sandbox with nothing in it rather than one that is
// gone.
func (s *Server) publishSandboxRemoval(id string) {
	s.cfg.SandboxEvents.Publish(v1.NewEvent("", formatInstant(time.Now().UTC()),
		v1.EventTypeSandboxRemoved, v1.EventResource{Kind: v1.KindSandbox, ID: id}, ""))
}
