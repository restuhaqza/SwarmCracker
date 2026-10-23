package v1

// MergePatchMediaType is the request media type of PATCH /v1/services/{ref}.
// The body is an RFC 7386 JSON merge patch over the writable service subtree.
const MergePatchMediaType = "application/merge-patch+json"

// ServiceWrite is the body of POST /v1/services.
//
// It is deliberately a separate type from Service: the write shape is the only
// one that carries RegistryAuth, and no read path can marshal it. Keeping the
// two apart enforces "registry credentials are write-only" by construction
// instead of by remembering to redact a response.
type ServiceWrite struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Spec   ServiceSpecWrite  `json:"spec"`
}

// ServiceSpecWrite is the writable half of a service spec. The read
// ServiceSpec also carries a mode the API refuses to change after create, so the
// write type keeps its own field set rather than embedding it.
type ServiceSpecWrite struct {
	Mode      string        `json:"mode"`
	Replicas  *int64        `json:"replicas,omitempty"`
	Task      TaskSpecWrite `json:"task"`
	Update    *UpdateConfig `json:"update,omitempty"`
	Rollback  *UpdateConfig `json:"rollback,omitempty"`
	Placement *Placement    `json:"placement,omitempty"`
}

// TaskSpecWrite embeds the read task shape so the JSON fields stay flat, then
// adds the write-only credential.
//
// Never use this type in a response. TaskSpec, which every read document uses,
// has no RegistryAuth field at all.
type TaskSpecWrite struct {
	TaskSpec
	// RegistryAuth is the base64 X-Registry-Auth payload SwarmKit forwards to
	// the node when it pulls Image. It is never echoed back.
	RegistryAuth string `json:"registryAuth,omitempty"`
}

// WriteOptions carries the HTTP preconditions and modifiers of one write. It is
// transport-neutral: the HTTP layer fills it from headers and query parameters,
// pkg/client evaluates it against the current revision.
type WriteOptions struct {
	// IfMatch is the caller's If-Match header value, as received. "" means the
	// caller sent none (no precondition), "*" matches any current revision and a
	// quoted or bare revision must equal the current one. A weak W/"…" validator
	// never matches.
	IfMatch string
	// IfNoneMatch is the caller's If-None-Match header value on create. Only "*"
	// is meaningful: it marks the request create-only. "" means no header.
	IfNoneMatch string
	// IgnoreMissing turns a delete of an absent resource into a success.
	IgnoreMissing bool
}
