package v1

// Event is one change from the manager's Watch stream.
type Event struct {
	SchemaVersion int           `json:"schemaVersion"`
	Kind          string        `json:"kind"`
	Cursor        string        `json:"cursor"`
	At            string        `json:"at,omitempty"`
	Type          string        `json:"type"`
	Resource      EventResource `json:"resource"`
	Revision      string        `json:"revision,omitempty"`
	Changed       []string      `json:"changed,omitempty"`
	Actor         *AuditActor   `json:"actor,omitempty"`
	Object        any           `json:"object,omitempty"`

	// Labels is the resource's labels, used for ?label= filtering. It is not
	// part of the wire document; the resource itself carries them.
	Labels map[string]string `json:"-"`
}

// The sandbox event types, emitted by the node that serves the sandboxes rather
// than by the cluster watch (pkg/apihttp/sandbox_events.go).
//
// They are spelled exactly like every other event type — the resource's kind,
// lowercased, then the past-tense action — so one `types=sandbox` filter and one
// consumer rule cover both streams. A fork's clones need no type of their own: each
// is a sandbox that was created.
const (
	// EventTypeSandboxCreated is a sandbox this node's API created and reached
	// ready, and each clone a fork made.
	EventTypeSandboxCreated = "sandbox.created"
	// EventTypeSandboxFailed is a create that left a failed record behind. The
	// caller's own error says why; this is how a watcher learns it happened.
	EventTypeSandboxFailed = "sandbox.failed"
	// EventTypeSandboxRemoved is a sandbox an API call ended.
	EventTypeSandboxRemoved = "sandbox.removed"
)

// EventResource identifies the object an event changed.
type EventResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// NewEvent wraps one change in the v1 envelope.
func NewEvent(cursor, at, typ string, resource EventResource, revision string) Event {
	return Event{
		SchemaVersion: SchemaVersion,
		Kind:          KindEvent,
		Cursor:        cursor,
		At:            at,
		Type:          typ,
		Resource:      resource,
		Revision:      revision,
	}
}

// EventOptions selects the stream the client opens.
type EventOptions struct {
	Types []string
	Since string
	// IncludeObject embeds the mapped resource in each event.
	IncludeObject bool
	// IncludeConfigValues is the configs:read-values grant. Without it a
	// config event never carries data.
	IncludeConfigValues bool
}

// ReadyFrame is the NDJSON stream's first line. SSE uses an `event: ready`
// frame instead, but both tell the client the stream is live.
type ReadyFrame struct {
	Type   string `json:"type"`
	Cursor string `json:"cursor,omitempty"`
}
