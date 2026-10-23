package v1

// AuditActor is who caused an audited write. Type is "token" for a bearer token,
// "local" for the trusted Unix socket (where the file mode is the credential),
// and "unknown" when a record was produced outside the request middleware and no
// identity is available.
type AuditActor struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// AuditActorUnknown is the actor type for a request that carried no identity.
const AuditActorUnknown = "unknown"

// AuditResource is the object an audited write touched. Kind is the document
// kind ("Service", "Config", …); ID and Name are optional so a failed create
// that never resolved a resource still records the attempt.
type AuditResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// AuditRecord is one append-only audit entry.
//
// Result is "ok" or "error". SourceIP is the TCP peer address, never a
// forwarded header; it is empty for a request over the Unix socket.
type AuditRecord struct {
	At        string        `json:"at"`
	RequestID string        `json:"requestId"`
	Actor     AuditActor    `json:"actor"`
	Action    string        `json:"action"`
	Resource  AuditResource `json:"resource"`
	Result    string        `json:"result"`
	// Precondition is how the write's If-Match was satisfied: "" when the
	// caller sent none, "revision" for an exact revision and "any" for
	// If-Match: "*". An unconditional write is visible in the log.
	Precondition string `json:"precondition,omitempty"`
	SourceIP     string `json:"sourceIp,omitempty"`
}

// AuditList is a paginated list of audit records.
type AuditList struct {
	SchemaVersion int           `json:"schemaVersion"`
	Kind          string        `json:"kind"`
	Items         []AuditRecord `json:"items"`
	Next          *string       `json:"next"`
}

// NewAuditList returns a list document with a non-nil items array.
func NewAuditList(items []AuditRecord) AuditList {
	if items == nil {
		items = []AuditRecord{}
	}
	return AuditList{
		SchemaVersion: SchemaVersion,
		Kind:          KindAuditList,
		Items:         items,
		Next:          nil,
	}
}
