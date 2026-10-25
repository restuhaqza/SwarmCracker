package v1

import "regexp"

// This file owns the experimental snapshot wire contract: the Snapshot resource,
// its document and list envelopes, and the optional create request.
//
// A snapshot lives on the node that ran the task, next to the Firecracker files
// it names. Only the rootfs path crosses the wire: the memory and state files are
// node-local implementation details, and publishing them would leak the node's
// filesystem layout. Every snapshot ID is `snap-` plus hex characters.
//
// MemoryMB is deliberately camel-cased as `memoryMB`: the public contract names
// it that way even though pkg/snapshot's internal field is MemoryMB.

// Snapshot is one VM snapshot. Every field except ID is optional: an older node
// that recorded less metadata still produces a valid document.
type Snapshot struct {
	ID         string `json:"id"`
	TaskID     string `json:"taskId,omitempty"`
	ServiceID  string `json:"serviceId,omitempty"`
	NodeID     string `json:"nodeId,omitempty"`
	CreatedAt  string `json:"createdAt,omitempty"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	MemoryMB   int    `json:"memoryMB,omitempty"`
	VCPUCount  int    `json:"vcpuCount,omitempty"`
	RootfsPath string `json:"rootfsPath,omitempty"`
	Checksum   string `json:"checksum,omitempty"`
}

// SnapshotDocument is a single-resource Snapshot document.
type SnapshotDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Snapshot
}

// NewSnapshotDocument wraps a Snapshot in the v1 envelope.
func NewSnapshotDocument(snapshot Snapshot) SnapshotDocument {
	return SnapshotDocument{
		SchemaVersion: SchemaVersion,
		Kind:          KindSnapshot,
		Snapshot:      snapshot,
	}
}

// SnapshotList is a paginated list of snapshots.
type SnapshotList struct {
	SchemaVersion int        `json:"schemaVersion"`
	Kind          string     `json:"kind"`
	Items         []Snapshot `json:"items"`
	Next          *string    `json:"next"`
	Partial       []Partial  `json:"partial,omitempty"`
}

// NewSnapshotList returns a list document with a non-nil items array.
func NewSnapshotList(items []Snapshot) SnapshotList {
	if items == nil {
		items = []Snapshot{}
	}
	return SnapshotList{
		SchemaVersion: SchemaVersion,
		Kind:          KindSnapshotList,
		Items:         items,
		Next:          nil,
	}
}

// SnapshotCreateRequest is the optional body of POST /v1/tasks/{ref}/snapshots.
// Every field is optional: the node falls back to the values it recorded for the
// running VM. Unknown fields are rejected by the request decoder.
type SnapshotCreateRequest struct {
	VCPUs      int    `json:"vcpus,omitempty"`
	MemoryMB   int    `json:"memoryMB,omitempty"`
	RootfsPath string `json:"rootfsPath,omitempty"`
}

// snapshotIDPattern is the shape of a snapshot id: `snap-` plus alphanumerics.
// pkg/snapshot generates hex, but the rule that matters on the wire is that an
// id is one safe path segment, so no separator or dot can address a directory
// outside the snapshot root.
var snapshotIDPattern = regexp.MustCompile(`^snap-[A-Za-z0-9]+$`)

// ValidSnapshotID reports whether id is a well-formed snapshot id. Both the node
// handler and the public route validate before an id reaches a store or a URL.
func ValidSnapshotID(id string) bool {
	return snapshotIDPattern.MatchString(id)
}
