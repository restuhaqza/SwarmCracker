package v1

// This file owns the public live-migration document: the request that asks a
// manager to move one task's VM to another node, and the report of what the
// manager did.
//
// It is one document, not a list and not a status surface: the call is
// synchronous, and what it returns is the whole story of one migration. There is
// deliberately no migration ID of its own - the migration is the artifact the
// source exported, so `id` is that artifact's ID - and no retention: the report
// is not stored, it is the answer to the request that ran it.

const (
	// KindMigration is the document kind of one migration report.
	KindMigration = "Migration"
)

// The phases of a migration, in the order they run. They are the report's
// vocabulary: a caller reads the list to see how far the move got, and the
// failing phase is named in the error document's details.phase.
const (
	MigrationPhaseValidate = "validate"
	MigrationPhaseExport   = "export"
	MigrationPhaseRelay    = "relay"
	MigrationPhasePrepare  = "prepare"
	MigrationPhaseReplace  = "replace"
	MigrationPhaseVerify   = "verify"
	MigrationPhaseCleanup  = "cleanup"
)

// The result of one phase.
const (
	// MigrationPhaseOK is a phase that finished its work.
	MigrationPhaseOK = "ok"
	// MigrationPhaseError is a phase that failed. The request then answers a
	// typed error document.
	MigrationPhaseError = "error"
	// MigrationPhasePending is the verify phase that ran out of time. The
	// migration is neither finished nor abandoned: the service was replaced, the
	// artifact and the directive are kept, and SwarmKit's retry restores from
	// them until the directive expires.
	MigrationPhasePending = "pending"
)

// The outcome of a whole migration.
const (
	// MigrationOutcomeMoved is a replacement that reported running on the target.
	MigrationOutcomeMoved = "moved"
	// MigrationOutcomePending is a service that was replaced but had not
	// reported a running replacement on the target before the verify deadline.
	MigrationOutcomePending = "pending"
)

// MigrationRequest is the body of POST /v1/migrations: the task to move and the
// node to move it to.
//
// The task is an exact task ID: the manager reads it with a GetTask by that ID
// and resolves nothing, so a shortened ID is a 404. The node is a reference the
// manager does resolve - an exact ID, an exact hostname, or a unique ID prefix of
// at least four characters. Nothing here names the source: the task's own node is
// the source, which is what keeps a caller from exporting through a node that
// never ran the task.
type MigrationRequest struct {
	TaskID     string `json:"taskId"`
	TargetNode string `json:"targetNode"`
}

// MigrationPhase is one step of the migration report. StartedAt and FinishedAt
// are RFC3339Nano instants in UTC; FinishedAt is empty on a phase that was
// still running when the report was written (a pending verify keeps it empty).
type MigrationPhase struct {
	Name       string `json:"name"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Result     string `json:"result"`
	// Message explains a non-ok result, or names a best-effort cleanup step that
	// did not complete. It is a step name plus the failure's stable code and reason,
	// never the failure's text: that text is written by this node or a peer and
	// names node-local paths, which this document may not carry.
	Message string `json:"message,omitempty"`
}

// Migration is the document a migration answers: what moved, where it moved
// from and to, the phases it ran, and how the move ended.
//
// ID is the artifact the source exported, because a migration has no identity
// beyond the artifact that carries the VM's bytes: the pair (task, artifact) is
// the move. DowntimeMs is measured from the instant the old task was observed
// gone - or, when it was never observed gone, from the instant the service
// update returned - to the first instant a replacement reported running on the
// target. It is zero for an outcome that never saw a running replacement, because
// an unmeasured downtime is not zero seconds.
type Migration struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	ID            string `json:"id"`

	TaskID    string `json:"taskId"`
	ServiceID string `json:"serviceId,omitempty"`
	// SourceNode is the node the task ran on; TargetNode is the node it moved to.
	SourceNode string `json:"sourceNode"`
	TargetNode string `json:"targetNode"`

	Phases     []MigrationPhase `json:"phases"`
	DowntimeMs int64            `json:"downtimeMs"`
	Outcome    string           `json:"outcome"`
}
