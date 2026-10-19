// Package v1 contains the stable wire types for the SwarmCracker public API.
//
// The types in this package are deliberately independent of SwarmKit: they are
// the contract, not a reflection of generated protobuf code. Mapping to and from
// SwarmKit lives in pkg/client.
package v1

// SchemaVersion is the document schema major. It only moves on a breaking
// payload change; additive fields do not change it.
const SchemaVersion = 1

// Document kinds. Kinds are open within v1: clients must tolerate unknown
// values, so adding a kind is additive.
const (
	KindService      = "Service"
	KindServiceList  = "ServiceList"
	KindTask         = "Task"
	KindTaskList     = "TaskList"
	KindNode         = "Node"
	KindNodeList     = "NodeList"
	KindNetwork      = "Network"
	KindNetworkList  = "NetworkList"
	KindConfig       = "Config"
	KindConfigList   = "ConfigList"
	KindSecret       = "Secret"
	KindSecretList   = "SecretList"
	KindSnapshot     = "Snapshot"
	KindSnapshotList = "SnapshotList"
	KindCluster      = "Cluster"
	KindEvent        = "Event"
	KindAuditEntry   = "AuditEntry"
	KindAuditList    = "AuditList"
	KindVersionInfo  = "VersionInfo"
	KindError        = "Error"
)

// Warning is a non-fatal notice carried in a document's warnings array. It is
// not a document kind.
type Warning struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Error is the payload of an Error document and of partial[].error.
type Error struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"requestId,omitempty"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

// ErrorDocument is the single document written for a failure.
type ErrorDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Error         Error  `json:"error"`
}

// NewErrorDocument wraps an error payload in the v1 envelope.
func NewErrorDocument(err Error) ErrorDocument {
	return ErrorDocument{
		SchemaVersion: SchemaVersion,
		Kind:          KindError,
		Error:         err,
	}
}

// Partial describes a node that could not answer an aggregate read.
type Partial struct {
	NodeID   string `json:"nodeId"`
	NodeName string `json:"nodeName,omitempty"`
	Error    Error  `json:"error"`
}
