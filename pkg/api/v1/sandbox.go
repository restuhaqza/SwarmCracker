package v1

// This file owns the sandbox wire contract: the documents the sandbox control plane
// (pkg/sandbox, served by the node's sandboxd) serves, and the request bodies a
// caller posts to it.
//
// A sandbox is not a task and has no SwarmKit identity: it is a microVM booted from
// a sealed template, owned by nobody but its creator, with its own network namespace
// and its own per-boot credential. So its documents carry the sandbox's own nouns —
// the template it came from, the state it is in, when it was last used — and not a
// service, a slot or a desired state.
//
// What is deliberately absent is host layout: the record a node keeps names the
// sandbox's jail, its disk and the host copy of its agent token, and none of those
// cross this contract. A caller reads where a sandbox is on the network (its address
// and MAC) and what it may reach; where its files are is the node's business.
//
// State is one of created, ready, failed or stopped. ready is the only state in
// which a sandbox is usable, and it means the guest agent answered its hello — not
// that a VMM started. failed carries reason, which says what went wrong.

// Sandbox is one agent sandbox.
type Sandbox struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Template string `json:"template,omitempty"`
	Owner    string `json:"owner,omitempty"`

	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	// ReadyAt is when the guest agent answered. It is absent until it does.
	ReadyAt string `json:"readyAt,omitempty"`
	// LastActiveAt is when the node last saw the sandbox used. It is what the idle
	// TTL is measured from.
	LastActiveAt string `json:"lastActiveAt,omitempty"`
	// IdleTTLSeconds is how long the sandbox may sit unused before the node takes it
	// back. Zero means it is never reaped.
	IdleTTLSeconds int64 `json:"idleTtlSeconds,omitempty"`
	// MaxTTLSeconds is how long the sandbox may exist at all, from its creation,
	// whatever it does in between: an idle TTL is pushed back by use, this one is
	// not. Zero means the node imposes no maximum. MaxExpiresAt is that deadline as
	// an instant, absent when there is none.
	MaxTTLSeconds int64  `json:"maxTtlSeconds,omitempty"`
	MaxExpiresAt  string `json:"maxExpiresAt,omitempty"`

	// Reason says why a sandbox is failed. It is absent in every other state.
	Reason string `json:"reason,omitempty"`

	// GuestIP and MAC are the sandbox's address and link address on its own /30.
	GuestIP string `json:"guestIp,omitempty"`
	MAC     string `json:"mac,omitempty"`
	// Egress is what the sandbox may reach, as requested: "host:port"
	// destinations, or empty for egress none.
	Egress []string `json:"egress,omitempty"`

	// DiskCopy is how the sandbox's own disk was materialized from the template:
	// reflink, sparse or full.
	DiskCopy string `json:"diskCopy,omitempty"`
	// Ports are the guest ports this node serves to its host through a preview proxy:
	// each one an address on the host, the guest port behind it, and when the exposure
	// ends. The token that opens one is not here — it is returned once, by the call that
	// exposed the port.
	Ports []SandboxPort `json:"ports,omitempty"`
	// AgentVersion and BootID are what the guest answered when it proved itself
	// ready: the agent build it runs and this boot's identity.
	AgentVersion string `json:"agentVersion,omitempty"`
	BootID       string `json:"bootId,omitempty"`
}

// SandboxPort is one guest port reached from the host through this node's preview proxy.
//
// The address is on the host's loopback, which is what makes a preview a preview: it is
// reachable from the machine the sandbox runs on and not from the network. The guest's own
// port is kept, so a service that prints its own URL prints something still true here, and
// the address is derived from the sandbox, so two sandboxes can expose the same guest port
// without meeting.
type SandboxPort struct {
	GuestPort uint16 `json:"guestPort"`
	// Address is the host address the preview listens on, "address:port".
	Address string `json:"address"`
	// URL is the address as a URL without the token.
	URL string `json:"url"`
	// ExpiresAt is when the exposure stops serving, or empty for one that never does.
	// Once it has, the listener answers 410 gone until the port is un-exposed or the
	// sandbox is removed.
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// SandboxPortRequest is the body of POST /v1/sandboxes/{id}/ports: a request to expose one
// guest port on the host.
//
// GuestPort is required. TTLSeconds is how long the exposure is served for: zero means the
// node's default, and -1 means it never expires.
type SandboxPortRequest struct {
	GuestPort  uint16 `json:"guestPort"`
	TTLSeconds int64  `json:"ttlSeconds,omitempty"`
}

// SandboxPortExposure is the response to POST /v1/sandboxes/{id}/ports: the exposure, and
// the token that opens it.
//
// Token and its URL are present exactly once, in this response, and never again: the node
// keeps only the digest of the token, so an exposure whose URL a caller lost cannot be
// recovered by reading the sandbox — the port has to be exposed again.
type SandboxPortExposure struct {
	SchemaVersion int         `json:"schemaVersion"`
	Kind          string      `json:"kind"`
	Sandbox       string      `json:"sandbox"`
	Port          SandboxPort `json:"port"`
	Token         string      `json:"token"`
	// Authenticated is the URL a caller opens to use the exposure: the port's own URL
	// with the token in it.
	Authenticated string `json:"authenticatedUrl"`
}

// NewSandboxPortExposure wraps an exposure in the v1 envelope.
func NewSandboxPortExposure(sandboxID string, port SandboxPort, token, authenticatedURL string) SandboxPortExposure {
	return SandboxPortExposure{
		SchemaVersion: SchemaVersion,
		Kind:          KindSandboxPort,
		Sandbox:       sandboxID,
		Port:          port,
		Token:         token,
		Authenticated: authenticatedURL,
	}
}

// SandboxDocument is a single-resource Sandbox document.
type SandboxDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Sandbox
}

// NewSandboxDocument wraps a Sandbox in the v1 envelope.
func NewSandboxDocument(s Sandbox) SandboxDocument {
	return SandboxDocument{SchemaVersion: SchemaVersion, Kind: KindSandbox, Sandbox: s}
}

// SandboxList is a list of sandboxes.
type SandboxList struct {
	SchemaVersion int       `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	Items         []Sandbox `json:"items"`
	// Checkpoint names the checkpoint the sandboxes in this list were forked from. It
	// is set by the fork routes, which either take a checkpoint themselves (and mint
	// its name) or are given one: a caller that cannot see the name cannot free the
	// image the fork left behind.
	Checkpoint string    `json:"checkpoint,omitempty"`
	Next       *string   `json:"next"`
	Warnings   []Warning `json:"warnings,omitempty"`
	Partial    []Partial `json:"partial,omitempty"`
}

// NewSandboxList returns a list document with a non-nil items array.
func NewSandboxList(items []Sandbox) SandboxList {
	if items == nil {
		items = []Sandbox{}
	}
	return SandboxList{
		SchemaVersion: SchemaVersion,
		Kind:          KindSandboxList,
		Items:         items,
		Next:          nil,
	}
}

// SandboxCreate is the body of POST /v1/sandboxes.
//
// Template is required and is resolved through the template store, where its pin is
// checked: a template built by other host software is refused, with
// template_pin_mismatch, before a disk is copied. ID is minted when empty. Egress is
// the allowlist; empty means the sandbox reaches nothing, which is the default a
// sandbox should have. IdleTTLSeconds is how long the sandbox may idle before the
// node's reaper takes it back; zero means the node's default. MaxTTLSeconds is how
// long the sandbox may exist at all, whatever it does in between; zero means the
// node's ceiling, and a request longer than that ceiling is 400 "max_ttl_too_long"
// rather than silently shortened.
type SandboxCreate struct {
	Template       string   `json:"template"`
	ID             string   `json:"id,omitempty"`
	Owner          string   `json:"owner,omitempty"`
	Egress         []string `json:"egress,omitempty"`
	IdleTTLSeconds int64    `json:"idleTtlSeconds,omitempty"`
	MaxTTLSeconds  int64    `json:"maxTtlSeconds,omitempty"`
	VCPUs          int      `json:"vcpus,omitempty"`
	MemoryMB       int      `json:"memoryMB,omitempty"`
}

// SandboxFreed is what removing one sandbox took back. Every field is a resource the
// teardown actually freed, so "everything is gone" is a list rather than a promise:
// the process, the socket, the cgroup, the jail, the network namespace (and the TAP
// device and policy inside it), the disk, the host's copy of the agent token, the
// node's instance record and the API tokens scoped to the sandbox.
type SandboxFreed struct {
	ProcessStopped bool `json:"processStopped"`
	SocketRemoved  bool `json:"socketRemoved"`
	CgroupRemoved  bool `json:"cgroupRemoved"`
	JailRemoved    bool `json:"jailRemoved"`
	NetNSRemoved   bool `json:"netnsRemoved"`
	TAPRemoved     bool `json:"tapRemoved"`
	DiskRemoved    bool `json:"diskRemoved"`
	// PreviewsClosed is how many preview listeners were closed: the host sockets that
	// served an exposed guest port. They are freed before the namespace they relay into.
	PreviewsClosed         int  `json:"previewsClosed"`
	AgentCredentialRemoved bool `json:"agentCredentialRemoved"`
	InstanceRecordRemoved  bool `json:"instanceRecordRemoved"`
	RecordRemoved          bool `json:"recordRemoved"`
	APITokensRevoked       int  `json:"apiTokensRevoked"`
}

// SandboxRemoval is the response to DELETE /v1/sandboxes/{id}: the sandbox that was
// removed and what removing it freed.
type SandboxRemoval struct {
	SchemaVersion int          `json:"schemaVersion"`
	Kind          string       `json:"kind"`
	Sandbox       string       `json:"sandbox"`
	Freed         SandboxFreed `json:"freed"`
}

// NewSandboxRemoval wraps a removal in the v1 envelope.
func NewSandboxRemoval(id string, freed SandboxFreed) SandboxRemoval {
	return SandboxRemoval{SchemaVersion: SchemaVersion, Kind: KindSandboxRemoval, Sandbox: id, Freed: freed}
}

// SandboxTokenRequest is the body of POST /v1/sandboxes/{id}/tokens: a request for a
// token scoped to one sandbox.
//
// The token the server returns carries exactly one sandbox scope — read and exec —
// and names this one sandbox, so it can run commands in that sandbox and read it,
// and cannot touch another sandbox, create one, remove one or issue a token.
type SandboxTokenRequest struct {
	// Name is the token's name, for the operator's listing. Empty is "sandbox
	// <id>", which is what an operator would call it.
	Name string `json:"name,omitempty"`
	// ExpiresAt is an RFC3339 instant. Empty means the token never expires.
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// SandboxToken is a token scoped to one sandbox, as returned at creation time:
// Secret is present exactly once, in this response, and never again.
type SandboxToken struct {
	SchemaVersion int      `json:"schemaVersion"`
	Kind          string   `json:"kind"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Sandbox       string   `json:"sandbox"`
	Scopes        []string `json:"scopes"`
	Secret        string   `json:"token"`
	ExpiresAt     string   `json:"expiresAt,omitempty"`
}

// NewSandboxToken wraps a sandbox-scoped token in the v1 envelope.
func NewSandboxToken(id, name, sandbox, secret, expiresAt string, scopes []string) SandboxToken {
	return SandboxToken{
		SchemaVersion: SchemaVersion,
		Kind:          KindSandboxToken,
		ID:            id,
		Name:          name,
		Sandbox:       sandbox,
		Scopes:        scopes,
		Secret:        secret,
		ExpiresAt:     expiresAt,
	}
}
