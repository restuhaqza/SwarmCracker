package sandbox

// The node's live stream of sandbox changes, and what one change is.
//
// # Why the bus lives here
//
// Every other event this API streams comes from the cluster's watch (pkg/client, fed by
// SwarmKit): a service, a task, a node. A sandbox is deliberately not a SwarmKit object —
// it is created by a caller rather than scheduled, and its state is one node's record — so
// nothing watches it for the API, and its changes have two origins: a call this node's API
// served, and this node's own loops (the reaper taking an idle sandbox back, a reconcile
// pass finding a sandbox's VMM gone). Both are the writer's news, and this package is the
// writer, so the bus is here and pkg/apihttp is a consumer of it like the loops are: it
// subscribes, and it renders what it reads as the v1 documents its event stream writes.
//
// # What a change is, and what it deliberately is not
//
// A Change is one fact about one sandbox: what happened, to which sandbox, when, and — for
// everything except a removal — the record as it was at that moment. It is not a wire
// document: the event envelope, its type names, and the decision to strip the object
// unless the caller asked for it (include=object) are the API's contract, and keeping them
// out of here is what lets the loops publish without knowing it.
//
// # The rules a subscriber gets
//
//   - A change is published only after the operation succeeded, and what it carries is the
//     record as it was at that moment. A watcher therefore sees the same sandbox a caller
//     who created it got.
//   - A change has no cursor. The cluster stream's cursor is a SwarmKit watch index, and a
//     sandbox change has no place in that log: reusing the field would let a client resume
//     the cluster stream from an index that means nothing, and dropping the field would
//     break the envelope. So the API's stream is live-only — a change is delivered to
//     whoever is watching when it happens and is never replayed — and `since` never
//     resumes across one.
//   - Publishing never blocks and never fails the operation that produced it. A watcher
//     that cannot keep up with its buffer loses changes rather than stalling a create, and
//     the record — which is on disk — remains the truth.

import (
	"sync"
	"time"
)

// changeBuffer is how many changes one subscriber may fall behind by. It is small on
// purpose: a change is a notification, and a watcher that is more than a few behind is
// better served by reading the sandboxes themselves than by replaying a stale queue.
const changeBuffer = 64

// ChangeKind is what happened to a sandbox. It is the node's own vocabulary rather than the
// API's event type: the type name is the API's, and it is derived from this.
type ChangeKind string

const (
	// ChangeCreated is a sandbox that was created and reached ready, including each clone
	// a fork made.
	ChangeCreated ChangeKind = "created"
	// ChangeFailed is a sandbox left failed: a create the node could not make usable, or a
	// record a reconcile pass found behind a VMM that is gone. The record's Reason says
	// why, so a watcher learns the cause and not only that something went wrong.
	ChangeFailed ChangeKind = "failed"
	// ChangeRemoved is a sandbox that was removed and holds nothing any more. It carries
	// no record, deliberately: the record is deleted by the time the change is published,
	// and a sandbox's ID is its whole identity — it has no name and no labels — so an
	// empty record would suggest a sandbox with nothing in it rather than one that is gone.
	ChangeRemoved ChangeKind = "removed"
)

// Change is one sandbox change: what happened, to which sandbox, and when.
//
// One publisher per change is the rule, not a coincidence: the API announces what the API
// did, and a loop announces what the loop did, so nothing is published twice and nothing is
// published by a layer that only guessed. The consequence is an obligation on the next
// author: a new way to create, fail or remove a sandbox owes a publish at its own call site.
// Today the API is the only creator and this package's reaper and reconcile pass are the
// only loops, so nothing is missing — but a second creator added without one would change a
// sandbox silently on every stream.
type Change struct {
	Kind ChangeKind
	// ID is the sandbox that changed. It is set for every kind, because a removal has
	// nothing else to carry.
	ID string
	// At is when it happened, on the node's clock.
	At time.Time
	// Record is the sandbox as it was at that moment, for the kinds that have one. It is
	// nil for a removal.
	Record *Record
}

// RecordChange builds the change for a sandbox that still exists, timestamped by the record
// itself: the record's own Updated is what a caller who performed the operation saw, and a
// record with no clock of its own falls back to now (which is the node's clock, passed in
// rather than read here so the loops and the API agree and a test can inject it).
//
// It is exported because both publishers — this package's loops and pkg/apihttp's routes —
// must stamp a change the same way; two rules for one timestamp would be two answers to
// "when did this happen".
func RecordChange(kind ChangeKind, rec Record, now time.Time) Change {
	at := rec.Updated
	if at.IsZero() {
		at = now.UTC()
	}
	return Change{Kind: kind, ID: rec.ID, At: at, Record: &rec}
}

// RemovalChange builds the change for a sandbox that is gone.
func RemovalChange(id string, now time.Time) Change {
	return Change{Kind: ChangeRemoved, ID: id, At: now.UTC()}
}

// Events is the node's live stream of sandbox changes.
//
// It is safe for concurrent use by any number of publishers (the API routes, the reaper,
// the reconcile pass) and subscribers (the event streams). A subscription is a channel plus
// the function that ends it, and the two are the whole lifecycle: no cursor, no replay, no
// history.
type Events struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]chan Change
}

// NewEvents returns an empty bus. The daemon builds one and hands it to both the manager
// (whose loops publish) and the API server (which subscribes), so a change a loop makes
// reaches the same stream a change a route makes does.
func NewEvents() *Events {
	return &Events{subs: make(map[int]chan Change)}
}

// Subscribe returns a channel of the changes published from now on, and the function that
// ends the subscription.
//
// The channel is closed by the cancel function, so a reader's range or its select sees the
// end of the subscription as a closed channel. Cancel is idempotent: a handler defers it and
// may also call it on the way out.
func (b *Events) Subscribe() (<-chan Change, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Change, changeBuffer)
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

// Publish delivers one change to every current subscriber.
//
// It never blocks: a subscriber whose buffer is full loses this change rather than holding
// the operation that published it. That is the only choice that keeps a created sandbox
// from waiting on a watcher, and the loss is bounded because the record itself is the
// durable answer.
func (b *Events) Publish(change Change) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, ch := range b.subs {
		select {
		case ch <- change:
		default:
		}
	}
}
