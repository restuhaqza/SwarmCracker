package v1

// Node is a single node (a cluster member) inside a NodeList.
//
// The fields are the phase-0 read model: identity, the role and lifecycle the
// node spec declares, the address the manager observes, and the capacity and
// manager status the source reports. Manager is present only on nodes that
// carry manager status, so workers omit the key rather than sending null.
type Node struct {
	ID           string            `json:"id"`
	Hostname     string            `json:"hostname,omitempty"`
	Revision     string            `json:"revision"`
	CreatedAt    string            `json:"createdAt,omitempty"`
	UpdatedAt    string            `json:"updatedAt,omitempty"`
	Role         string            `json:"role"`
	Membership   string            `json:"membership"`
	Availability string            `json:"availability"`
	State        string            `json:"state"`
	Address      string            `json:"address,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	Resources    *NodeResources    `json:"resources,omitempty"`
	Manager      *NodeManager      `json:"manager,omitempty"`
	AgentVersion string            `json:"agentVersion,omitempty"`
}

// NodeResources is the node's advertised capacity. CPUs is a JSON number so
// fractional vCPU counts survive; MemoryBytes is an integer.
type NodeResources struct {
	CPUs        float64 `json:"cpus,omitempty"`
	MemoryBytes int64   `json:"memoryBytes,omitempty"`
}

// NodeManager is the manager status of a node. It is emitted only when the
// source has manager status for the node; leader and reachable are always
// present, address and raft id are optional.
type NodeManager struct {
	Leader    bool   `json:"leader"`
	Reachable bool   `json:"reachable"`
	Address   string `json:"address,omitempty"`
	RaftID    uint64 `json:"raftId,omitempty"`
}

// NodeDocument is a single-resource Node document.
type NodeDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Node
}

// NewNodeDocument wraps a Node in the v1 envelope.
func NewNodeDocument(node Node) NodeDocument {
	return NodeDocument{SchemaVersion: SchemaVersion, Kind: KindNode, Node: node}
}

// NodeList is a paginated list of nodes.
type NodeList struct {
	SchemaVersion int       `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	Items         []Node    `json:"items"`
	Next          *string   `json:"next"`
	Warnings      []Warning `json:"warnings,omitempty"`
	Partial       []Partial `json:"partial,omitempty"`
}

// NewNodeList returns a list document with a non-nil items array.
func NewNodeList(items []Node) NodeList {
	if items == nil {
		items = []Node{}
	}
	return NodeList{
		SchemaVersion: SchemaVersion,
		Kind:          KindNodeList,
		Items:         items,
		Next:          nil,
	}
}
