package v1

// This file owns the checkpoint wire contract: the documents POST/GET
// /v1/sandboxes/{id}/checkpoints and the four node-scoped /v1/checkpoints routes serve.
//
// A checkpoint is immutable and outlives the sandbox it was taken from (RFC §5.4), so
// its document does not carry a state: there is nothing about a checkpoint that changes.
// What it carries is what a caller decides on — the sandbox it came from, the template
// and kernel it was captured on, the guest's own report of how its writers were stopped,
// and how much space it occupies.
//
// What stays on the node, as with a sandbox: the jail, the disk path, the state
// document's path, and the guest credential the checkpoint carries. A caller reads that
// a checkpoint exists and what it can resume; where its bytes are is the node's business.

// Kinds for the checkpoint documents, beside the sandbox ones.
const (
	KindCheckpoint        = "Checkpoint"
	KindCheckpointList    = "CheckpointList"
	KindCheckpointRemoval = "CheckpointRemoval"
)

// Checkpoint is one stored image of a sandbox.
type Checkpoint struct {
	// Name is the checkpoint's identity, unique on the node.
	Name string `json:"name"`
	// Sandbox is the sandbox it was taken from. It may no longer exist.
	Sandbox string `json:"sandbox"`
	// Template and Kernel are what it was captured on: a fork resumes exactly these.
	Template string `json:"template,omitempty"`
	Kernel   string `json:"kernel,omitempty"`
	// CreatedAt is when the images were taken.
	CreatedAt string `json:"createdAt,omitempty"`
	// VCPUs and MemoryMB are the sandbox's size: the memory image is exactly that
	// much memory.
	VCPUs    int `json:"vcpus,omitempty"`
	MemoryMB int `json:"memoryMB,omitempty"`
	// Egress is the allowlist a fork of this checkpoint installs.
	Egress []string `json:"egress,omitempty"`
	// QuiesceMode names the mechanism the guest used to stop its writers before the
	// images were taken, and QuiesceMounts are the filesystems it froze. An empty mode
	// means the checkpoint was taken without one, so its consistency is unknown.
	QuiesceMode   string   `json:"quiesceMode,omitempty"`
	QuiesceMounts []string `json:"quiesceMounts,omitempty"`
	// SizeBytes is what the checkpoint occupies: its state document, its memory
	// image's chunks and its disk copy.
	SizeBytes int64 `json:"sizeBytes,omitempty"`
	// AgentVersion and BootID are the guest the checkpoint resumes.
	AgentVersion string `json:"agentVersion,omitempty"`
	BootID       string `json:"bootId,omitempty"`
}

// CheckpointDocument is a single-resource Checkpoint document.
type CheckpointDocument struct {
	SchemaVersion int `json:"schemaVersion"`
	Kind          string
	Checkpoint
}

// NewCheckpointDocument wraps a Checkpoint in the v1 envelope.
func NewCheckpointDocument(c Checkpoint) CheckpointDocument {
	return CheckpointDocument{SchemaVersion: SchemaVersion, Kind: KindCheckpoint, Checkpoint: c}
}

// CheckpointList is a list of checkpoints, as one sandbox's or the whole node's.
type CheckpointList struct {
	SchemaVersion int          `json:"schemaVersion"`
	Kind          string       `json:"kind"`
	Items         []Checkpoint `json:"items"`
	Next          *string      `json:"next"`
	Warnings      []Warning    `json:"warnings,omitempty"`
	Partial       []Partial    `json:"partial,omitempty"`
}

// NewCheckpointList returns a list document with a non-nil items array.
func NewCheckpointList(items []Checkpoint) CheckpointList {
	if items == nil {
		items = []Checkpoint{}
	}
	return CheckpointList{SchemaVersion: SchemaVersion, Kind: KindCheckpointList, Items: items}
}

// CheckpointCreate is the body of POST /v1/sandboxes/{id}/checkpoints.
//
// Name is the checkpoint's identity and is required: a checkpoint is something a caller
// names so it can fork it later, and a minted one would only be reachable through a
// listing.
type CheckpointCreate struct {
	Name string `json:"name"`
}

// SandboxFork is the body of the two fork routes.
type SandboxFork struct {
	// Count is how many clones to create. Zero means one.
	Count int `json:"count,omitempty"`
	// Owner overrides the clones' owner. Empty inherits the checkpoint's.
	Owner string `json:"owner,omitempty"`
	// IdleTTLSeconds is the clones' idle TTL: zero means the node's default, -1 means
	// never reap them, and any other negative value is refused.
	IdleTTLSeconds int64 `json:"idleTtlSeconds,omitempty"`
	// IDs names the clones, in order. Empty mints them and the response carries them.
	IDs []string `json:"ids,omitempty"`
}

// CheckpointRemoval is the response to DELETE /v1/checkpoints/{name}: the checkpoint that
// was forgotten and what forgetting it actually freed.
type CheckpointRemoval struct {
	SchemaVersion int             `json:"schemaVersion"`
	Kind          string          `json:"kind"`
	Checkpoint    string          `json:"checkpoint"`
	Freed         CheckpointFreed `json:"freed"`
}

// CheckpointFreed says what a deletion returned to the node and what it had to keep.
//
// The distinction is the point: a checkpoint's memory image is stored as 4 MiB chunks
// shared with every other checkpoint of the same sandbox, so deleting one of two forks
// frees a few megabytes rather than the image — and a response that reported only success
// would leave an operator believing the disk came back.
type CheckpointFreed struct {
	// StateBytes and DiskBytes are the checkpoint's own files.
	StateBytes int64 `json:"stateBytes,omitempty"`
	DiskBytes  int64 `json:"diskBytes,omitempty"`
	// ChunkBytes and Chunks are the memory-image chunks nothing else referenced.
	ChunkBytes int64 `json:"chunkBytes,omitempty"`
	Chunks     int   `json:"chunks,omitempty"`
	// FreedBytes is what the node got back.
	FreedBytes int64 `json:"freedBytes"`
	// RetainedBytes is the memory image the remaining checkpoints still reference, and
	// Retained is how many remain. Shared chunks are counted once per referencing
	// checkpoint, so RetainedBytes is what they reference rather than a deduplicated
	// figure.
	RetainedBytes int64 `json:"retainedBytes"`
	Retained      int   `json:"retained,omitempty"`
}

// NewCheckpointRemoval wraps a removal in the v1 envelope.
func NewCheckpointRemoval(name string) CheckpointRemoval {
	return NewCheckpointRemovalWithFreed(name, CheckpointFreed{})
}

// NewCheckpointRemovalWithFreed wraps a removal and its freed/retained figures.
func NewCheckpointRemovalWithFreed(name string, freed CheckpointFreed) CheckpointRemoval {
	return CheckpointRemoval{
		SchemaVersion: SchemaVersion, Kind: KindCheckpointRemoval,
		Checkpoint: name, Freed: freed,
	}
}
