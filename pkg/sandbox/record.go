// Package sandbox is the node's control plane for agent sandboxes: one record per
// sandbox, the store that record lives in, the state machine it moves through, and
// the three operations a caller has — create, exec and remove — plus the reaper
// that takes an idle sandbox back.
//
// # What a sandbox is
//
// A sandbox is one microVM booted from a sealed template (pkg/golden), with its own
// disk, its own network namespace and policy (pkg/sandbox/netpolicy) and its own
// per-boot guest-agent token. It is not a SwarmKit task: nothing schedules it,
// nothing reconciles it, and its identity is not a task ID. That is why it needs a
// record of its own rather than the task-shaped derivation the service path uses.
//
// # Where the record lives
//
// A record is one JSON file, <stateDir>/sandboxes/<id>.json, and that file is the
// only state: nothing is cached in memory. The write is the registry's own
// (pkg/vminstance, RFC D2): a unique temporary beside the record, fsync, rename,
// fsync of the directory, so a reader sees either the old record or the new one and
// a crash leaves either none or a complete one. The node's daemon is the only
// writer, which is what makes a lock unnecessary — two writes for one ID are two
// renames, and the last one wins, which is the only order a lock could have given
// them.
//
// # Why the record is separate from the instance registry
//
// The instance registry (pkg/vminstance) records the *host identity of a running
// VM*: which process, which socket, which jail uid. A sandbox record is the
// *control-plane state of a sandbox*: which template it was made from, when it was
// last used, how long it may idle, and why it failed. The launcher publishes the
// first, and this package writes the second. Keeping them apart is what lets the
// broker resolve a sandbox's VM exactly as it resolves a task's — through one
// registry, by ID — while the sandbox's life is still described in the nouns of the
// sandbox.
//
// # The state machine
//
// Four states, and every one of them is reachable:
//
//		created ──▶ ready ──▶ failed ──▶ stopped
//		   │          │          ▲
//		   │          └──────────┘
//		   └──────────────────────▶ stopped / failed
//
//	  - created: the record exists and the node is building the sandbox — resolving
//	    the template, copying the disk, attaching the network, booting the VM. A
//	    reader that finds a sandbox in this state knows a create is in flight, or that
//	    one died before its guest answered.
//	  - ready: the guest agent answered its hello. This is the only state in which a
//	    sandbox is usable, and it is reached only through snapshot.Launcher, whose
//	    contract is that a launch returns only once the guest answered (readiness.go).
//	    "The load returned" is not ready; "the agent answered" is.
//	  - failed: the node could not make the sandbox usable, or made it usable and then
//	    lost it. Reason says which: a create step that failed, or a reconciliation pass
//	    that found the VMM gone while the record still said ready. The resources a
//	    failed sandbox still holds are rm's to free.
//	  - stopped: the node ended it. It is written before anything is freed and before
//	    the record is deleted, so a teardown that crashes half-way leaves the honest
//	    answer — the node ended this sandbox — and the next rm or reap finishes the
//	    job instead of guessing.
//
// A state a sandbox cannot leave is a mistake, not a milestone: there is no
// "paused", no "starting", no "terminating". A sandbox a caller stopped is a sandbox
// whose record is about to be gone, and a sandbox whose VM is still being built is
// created.
package sandbox

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// State is where a sandbox is in its life.
type State string

// The four states, and the only ones. See the package comment for what each one
// promises: created is a sandbox being built, ready is one whose guest answered,
// failed is one the node could not make usable (or lost), and stopped is one the
// node ended.
const (
	StateCreated State = "created"
	StateReady   State = "ready"
	StateFailed  State = "failed"
	StateStopped State = "stopped"
)

// transitions is the state machine, as data rather than as scattered checks: a
// caller that wants to know whether a move is one the node can make asks
// CanTransition, and a new state cannot be added without deciding what it may
// follow.
var transitions = map[State][]State{
	StateCreated: {StateReady, StateFailed, StateStopped},
	StateReady:   {StateFailed, StateStopped},
	StateFailed:  {StateStopped},
	// stopped is the end of the road: the record that carries it is deleted once
	// the teardown is done, so nothing transitions out of it.
	StateStopped: nil,
}

// Valid reports whether s is one of the four states.
func (s State) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// Terminal reports whether s is the end of a sandbox's life.
func (s State) Terminal() bool { return s == StateStopped }

// CanTransition reports whether a sandbox may move from one state to another. A
// move to the same state is refused: a write that changes nothing is a bug at the
// caller, not a transition.
func CanTransition(from, to State) bool {
	for _, next := range transitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// ErrNotFound means the node has no record for the sandbox.
var ErrNotFound = errors.New("sandbox: no such sandbox")

// ErrExists means a record for that ID already exists.
var ErrExists = errors.New("sandbox: that ID is already a sandbox")

// ErrNotReady means the sandbox exists but is not in the state the operation needs:
// a sandbox can only be exec'd while it is ready.
var ErrNotReady = errors.New("sandbox: the sandbox is not ready")

// ErrInvalid means the request could not describe a sandbox this node can build.
var ErrInvalid = errors.New("sandbox: invalid request")

// Record is the node's control-plane record of one sandbox.
//
// It is a node-local document, not a public one: it holds host paths and the
// location of the sandbox's agent token, so it never leaves the node and has no v1
// envelope. What a caller sees over the API is pkg/apihttp's rendering of it.
type Record struct {
	// ID is the sandbox's identity and names its record file, its network
	// namespace, its jail and its disk. It is one safe path element on purpose:
	// the same string has to be a file name, a namespace name and an instance ID,
	// and runtime.CheckTaskID is the node's rule for that.
	ID string `json:"id"`
	// State is where the sandbox is in its life.
	State State `json:"state"`
	// Template is the resolved template reference ("name@version") the sandbox
	// was created from. It is what the record can be traced back to after the
	// template store has moved on.
	Template string `json:"template,omitempty"`
	// Owner is who asked for the sandbox, when the caller named one. It is kept
	// so a record can be traced back without a second lookup.
	Owner string `json:"owner,omitempty"`

	// Created is when the record was first written, and Updated is when it was
	// last written. Both are the node's clock, not a guest's.
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// LastActive is when the node last saw the sandbox used: its creation, and
	// every exec since. It is what the reaper's idle question is measured from —
	// see IdleFor for why nothing else can be.
	LastActive time.Time `json:"lastActive"`
	// IdleTTLSeconds is how long the sandbox may sit unused before the reaper takes
	// it back. Zero means the sandbox is never reaped, which is a deliberate choice
	// a caller can make.
	IdleTTLSeconds int64 `json:"idleTtlSeconds"`
	// MaxTTLSeconds is how long the sandbox may exist at all, measured from
	// Created, whatever it does in between. It is the second deadline a sandbox may
	// carry, and the one no exec can push back: the idle TTL above is reset by use,
	// this one is not. Zero means the sandbox has no such limit, which is what a
	// node that configured none gives. It is on the record rather than in the
	// node's configuration alone because it is the sandbox's own deadline: a node
	// that restarts, or is reconfigured, still holds every sandbox to the life it
	// was admitted with.
	MaxTTLSeconds int64 `json:"maxTtlSeconds,omitempty"`
	// ReadyAt is when the guest answered. It is zero until it does, and it is what
	// makes "how long did create take" a measurement rather than a target.
	ReadyAt time.Time `json:"readyAt,omitempty"`

	// Reason says why a sandbox is failed. It is empty in every other state, and a
	// reader treats it as the one-line answer to "what went wrong".
	Reason string `json:"reason,omitempty"`

	// NetNS is the sandbox's network namespace, by path (what the jailer is
	// pointed at). TAP is the device inside it, GuestIP and MAC are the address and
	// link address the guest carries.
	NetNS   string `json:"netns,omitempty"`
	TAP     string `json:"tap,omitempty"`
	GuestIP string `json:"guestIp,omitempty"`
	MAC     string `json:"mac,omitempty"`
	// Egress is the allowlist the sandbox was created with, as requested. The
	// resolved addresses live in the namespace, not here.
	Egress []string `json:"egress,omitempty"`

	// VCPUs and MemoryMB are the size the sandbox was started with. A checkpoint
	// records them beside its images, and a fork resumes with the same memory: the
	// image is exactly that much memory, so it is not a value a clone chooses.
	VCPUs    int `json:"vcpus,omitempty"`
	MemoryMB int `json:"memoryMB,omitempty"`

	// ForkedFrom names the checkpoint this sandbox was resumed from, and is empty for
	// a sandbox created from a template. It is what makes a clone's lineage readable
	// without keeping a second index, and what a caller shows to explain where a
	// sandbox came from.
	ForkedFrom string `json:"forkedFrom,omitempty"`

	// Disk is the per-instance disk this node materialized for the sandbox: the
	// copy of the template's artifact that the guest's writes land in (through the
	// jail's staged copy). It is the sandbox's own, so rm frees it.
	Disk string `json:"disk,omitempty"`
	// DiskCopy is how that disk was made: "reflink" shares the template's blocks,
	// "sparse" copies only the data extents, "full" copies every byte.
	DiskCopy string `json:"diskCopy,omitempty"`
	// JailRoot is the jail directory the instance runs in. It is recorded because
	// the jailer's layout, not this package's, and rm must not re-derive it.
	JailRoot string `json:"jailRoot,omitempty"`
	// VsockPath is the host path of the vsock muxer the guest agent answers on: the
	// one path that reaches a sandbox's guest from the node.
	VsockPath string `json:"vsockPath,omitempty"`
	// AgentTokenFile is the host copy of the sandbox's per-boot agent token. It is
	// the credential the node presents to the guest, it is root-only, and rm
	// deletes it: a sandbox that is gone must not leave a live credential behind.
	AgentTokenFile string `json:"agentTokenFile,omitempty"`
	// Kernel is the kernel image the sandbox was booted with.
	Kernel string `json:"kernel,omitempty"`

	// AgentVersion and BootID are what the guest answered when it proved itself
	// ready: the agent build it runs and this boot's identity. They are empty until
	// ready, and they are the record's own evidence that ready means "the agent
	// answered" rather than "the VMM started".
	AgentVersion string `json:"agentVersion,omitempty"`
	BootID       string `json:"bootId,omitempty"`

	// Ports are the guest ports this node serves to its own host through a preview
	// proxy, one entry per exposed port (preview.go). They are the sandbox's because
	// they die with it: rm closes their listeners, and the namespace they relay into is
	// the one rm frees.
	Ports []ExposedPort `json:"ports,omitempty"`
}

// ExposedPort is one guest port reached from the host through this node's preview
// proxy (preview.go).
//
// It is the exposure as the record keeps it: which guest port, the host address the
// proxy listens on, when the exposure dies, and the *digest* of the token a caller must
// present. The token itself is never here — it is printed once, when the port is
// exposed, and a node keeps only what it needs to check it — so the record can be read
// by anything that can read the node's state without handing out a credential.
type ExposedPort struct {
	// GuestPort is the port inside the guest, and it is also the port the host address
	// serves: a service that prints its own URL keeps printing something true.
	GuestPort uint16 `json:"guestPort"`
	// Address is the loopback address on the host the proxy listens on, derived from the
	// sandbox's ID (previewAddress) so that it is stable for one sandbox and different
	// for two — two sandboxes may expose the same guest port without meeting on the
	// host.
	Address string `json:"address"`
	// TokenHash is the SHA-256 of the token that authorizes a request through the proxy,
	// hex encoded. A connection that presents anything else is answered 401; the proxy
	// compares digests, so the token itself is not on disk.
	TokenHash string `json:"tokenHash,omitempty"`
	// Created is when the port was exposed, and ExpiresAt is when the exposure stops
	// serving: from then on the listener answers 410 Gone until the exposure is removed
	// or the sandbox is. A zero ExpiresAt never expires.
	Created   time.Time `json:"created"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

// Host is the host-side address an exposed port answers on, "address:port".
func (e ExposedPort) Host() string {
	return net.JoinHostPort(e.Address, strconv.Itoa(int(e.GuestPort)))
}

// URL is the base URL of an exposed port, without a token: what a caller adds
// "?token=..." to, or presents in an Authorization header.
func (e ExposedPort) URL() string {
	if e.Address == "" {
		return ""
	}
	return "http://" + e.Host() + "/"
}

// Expired reports whether the exposure has stopped serving at now.
func (e ExposedPort) Expired(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt)
}

// exposedPort looks up one exposure in a record.
func (r Record) exposedPort(guestPort uint16) (ExposedPort, bool) {
	for _, p := range r.Ports {
		if p.GuestPort == guestPort {
			return p, true
		}
	}
	return ExposedPort{}, false
}

// exposedPorts is a record's exposed ports, in the order they are recorded.
func (r Record) exposedPorts() []uint16 {
	ports := make([]uint16, 0, len(r.Ports))
	for _, p := range r.Ports {
		ports = append(ports, p.GuestPort)
	}
	return ports
}

// TTL is how long this sandbox may idle before the reaper takes it back. Zero means
// never.
func (r Record) TTL() time.Duration {
	if r.IdleTTLSeconds <= 0 {
		return 0
	}
	return time.Duration(r.IdleTTLSeconds) * time.Second
}

// IdleFor is how long the node has seen the sandbox unused at now.
//
// Idle is measured from LastActive, and LastActive moves only when the node does
// something to the sandbox: creates it, or runs a command in it. There is no
// session to observe and no guest-side activity the node can see, so "the guest is
// doing something" is not a question this node can answer; "nobody has asked this
// sandbox to do anything in TTL" is, and that is the question the reaper asks. A
// caller that wants to keep a sandbox alive without using it can exec in it — which
// is the same thing as using it.
func (r Record) IdleFor(now time.Time) time.Duration {
	if r.LastActive.IsZero() {
		return 0
	}
	if now.Before(r.LastActive) {
		return 0
	}
	return now.Sub(r.LastActive)
}

// Idle reports whether the sandbox has been unused for its own TTL at now. A
// sandbox with no TTL is never idle, which is what "never reaped" means.
func (r Record) Idle(now time.Time) bool {
	ttl := r.TTL()
	return ttl > 0 && r.IdleFor(now) >= ttl
}

// IdleDeadline is when the idle TTL runs out, or the zero time when the sandbox has
// no idle TTL to run out.
func (r Record) IdleDeadline() time.Time {
	ttl := r.TTL()
	if ttl == 0 || r.LastActive.IsZero() {
		return time.Time{}
	}
	return r.LastActive.Add(ttl)
}

// MaxTTL is how long this sandbox may exist at all, from its creation. Zero means no
// limit.
func (r Record) MaxTTL() time.Duration {
	if r.MaxTTLSeconds <= 0 {
		return 0
	}
	return time.Duration(r.MaxTTLSeconds) * time.Second
}

// MaxDeadline is when the sandbox's maximum lifetime runs out, or the zero time when
// it has none. It is an instant rather than a duration so a reader can put it beside
// the idle deadline, and so a node that restarts keeps the deadline the sandbox was
// admitted with rather than starting the clock again.
func (r Record) MaxDeadline() time.Time {
	ttl := r.MaxTTL()
	if ttl == 0 || r.Created.IsZero() {
		return time.Time{}
	}
	return r.Created.Add(ttl)
}

// MaxExpired reports whether the sandbox has outlived its maximum lifetime at now.
// Nothing the caller does moves this deadline: it is measured from Created.
func (r Record) MaxExpired(now time.Time) bool {
	deadline := r.MaxDeadline()
	return !deadline.IsZero() && !now.Before(deadline)
}

// Deadline is when this sandbox is taken back if nothing else changes: its idle deadline,
// its maximum lifetime, or whichever comes first, because that is the limit that decides.
// The zero time means it has neither, and then only removing it ends it.
//
// It is the sandbox's own deadline and not a duration, which is what a reader that has to
// bound something else by the sandbox's life needs — a token bound to a sandbox, above all.
func (r Record) Deadline() time.Time {
	idle, max := r.IdleDeadline(), r.MaxDeadline()
	switch {
	case idle.IsZero():
		return max
	case max.IsZero():
		return idle
	case max.Before(idle):
		return max
	}
	return idle
}

// The two limits a sandbox can reach, as ReapDue reports them.
const (
	// ReapLimitIdle is the limit a sandbox reached by sitting unused past its idle
	// TTL — the limit an exec pushes back.
	ReapLimitIdle = "idle"
	// ReapLimitMax is the limit a sandbox reached by outliving its maximum lifetime,
	// which an exec cannot push back.
	ReapLimitMax = "max_ttl"
)

// ReapDue reports which limit takes this sandbox back at now, and whether one does.
// A sandbox past both is reported against the limit whose deadline came first, because
// that is the limit that took it: the idle TTL and the maximum lifetime run side by
// side, and whichever is reached first is the one that decides.
func (r Record) ReapDue(now time.Time) (string, bool) {
	maxDue, idleDue := r.MaxExpired(now), r.Idle(now)
	switch {
	case maxDue && idleDue:
		if r.MaxDeadline().Before(r.IdleDeadline()) {
			return ReapLimitMax, true
		}
		return ReapLimitIdle, true
	case maxDue:
		return ReapLimitMax, true
	case idleDue:
		return ReapLimitIdle, true
	}
	return "", false
}

// ReadyAtDuration is how long the sandbox took to become ready, or zero if it never
// did.
func (r Record) ReadyAtDuration() time.Duration {
	if r.ReadyAt.IsZero() || r.Created.IsZero() {
		return 0
	}
	return r.ReadyAt.Sub(r.Created)
}

// validate refuses a record the store could not serve, in the registry's terms: an
// ID that names a file inside the store, a state a reader can act on, a clock that
// does not run backwards, and absolute host paths — a relative path would resolve
// against whatever working directory the reader happens to have.
func (r Record) validate() error {
	if err := runtime.CheckTaskID(r.ID); err != nil {
		return fmt.Errorf("sandbox: record ID %q cannot name a sandbox: %w", r.ID, err)
	}
	if !r.State.Valid() {
		return fmt.Errorf("sandbox: record %q is in state %q, which is not a state: want %s, %s, %s or %s",
			r.ID, r.State, StateCreated, StateReady, StateFailed, StateStopped)
	}
	if r.Created.IsZero() {
		return fmt.Errorf("sandbox: record %q has no creation time", r.ID)
	}
	if r.Updated.IsZero() {
		return fmt.Errorf("sandbox: record %q has no update time", r.ID)
	}
	if r.Updated.Before(r.Created) {
		return fmt.Errorf("sandbox: record %q was updated at %s, before it was created at %s",
			r.ID, r.Updated.Format(time.RFC3339Nano), r.Created.Format(time.RFC3339Nano))
	}
	if !r.ReadyAt.IsZero() && r.ReadyAt.Before(r.Created) {
		return fmt.Errorf("sandbox: record %q became ready at %s, before it was created at %s",
			r.ID, r.ReadyAt.Format(time.RFC3339Nano), r.Created.Format(time.RFC3339Nano))
	}
	if r.IdleTTLSeconds < 0 {
		return fmt.Errorf("sandbox: record %q has a negative idle TTL", r.ID)
	}
	if r.MaxTTLSeconds < 0 {
		return fmt.Errorf("sandbox: record %q has a negative maximum lifetime", r.ID)
	}

	for _, field := range []struct {
		name string
		path string
	}{
		{"netns", r.NetNS},
		{"disk", r.Disk},
		{"jailRoot", r.JailRoot},
		{"vsockPath", r.VsockPath},
		{"agentTokenFile", r.AgentTokenFile},
		{"kernel", r.Kernel},
	} {
		if field.path != "" && !filepath.IsAbs(field.path) {
			return fmt.Errorf("sandbox: record %q has a relative %s path %q: want the host path", r.ID, field.name, field.path)
		}
	}

	if r.State == StateFailed && strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("sandbox: record %q is failed but does not say why", r.ID)
	}

	if err := validateExposedPorts(r.ID, r.Ports); err != nil {
		return err
	}
	return nil
}

// validateExposedPorts refuses a record's exposures that a reader could not act on: an
// exposed port is a listener on the host and an opening in the sandbox's own table, so a
// record that names one must name it exactly. A port the kernel has no meaning for, an
// address that is not this host's loopback (a preview is reachable from the host, not from
// the network), a digest that is not a digest, or two exposures of one port would each be a
// record the node cannot honour.
func validateExposedPorts(id string, ports []ExposedPort) error {
	seen := make(map[uint16]bool, len(ports))
	for _, p := range ports {
		if p.GuestPort == 0 {
			return fmt.Errorf("sandbox: record %q exposes port 0, which is not a port", id)
		}
		if seen[p.GuestPort] {
			return fmt.Errorf("sandbox: record %q exposes port %d twice: an exposure is one listener per port", id, p.GuestPort)
		}
		seen[p.GuestPort] = true

		addr, err := netip.ParseAddr(p.Address)
		if err != nil || !addr.Is4() || !addr.IsLoopback() {
			return fmt.Errorf("sandbox: record %q exposes port %d on %q: want a loopback IPv4 address a caller on this host can reach", id, p.GuestPort, p.Address)
		}
		if p.TokenHash != "" && !isHexDigest(p.TokenHash) {
			return fmt.Errorf("sandbox: record %q exposes port %d with a token hash that is not a SHA-256 digest", id, p.GuestPort)
		}
		if p.Created.IsZero() {
			return fmt.Errorf("sandbox: record %q exposes port %d with no creation time", id, p.GuestPort)
		}
		if !p.ExpiresAt.IsZero() && p.ExpiresAt.Before(p.Created) {
			return fmt.Errorf("sandbox: record %q exposes port %d until %s, before it was exposed at %s",
				id, p.GuestPort, p.ExpiresAt.Format(time.RFC3339Nano), p.Created.Format(time.RFC3339Nano))
		}
	}
	return nil
}

// isHexDigest reports whether s is a SHA-256 digest, hex encoded.
func isHexDigest(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
