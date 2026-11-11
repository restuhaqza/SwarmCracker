// Package netpolicy is the node's network policy for the sandboxes it runs: one
// network namespace per sandbox with its own TAP device and its own /30, and a
// default-deny ruleset inside it (RFC D5, B5).
//
// # Why a namespace per sandbox
//
// The service path shares one L2 bridge (swarm-br0, pkg/network): every VM on the
// node can address every other one, the node forwards between them, and a global
// masquerade rule gives them all the node's egress. That is the right shape for
// long-lived replicas of one workload and the wrong shape for sandboxes that are
// not meant to talk to each other at all. Here each sandbox gets its own network
// namespace, so isolation is a property of the namespace and not of a device name:
// two sandboxes can both own a device called tap0, and neither can be reached by
// the other, because neither has a path to the other's namespace. It is also what
// makes a frozen MAC harmless (B2): two clones may wear the same link address, and
// on two namespaces that is still two machines.
//
// # The shape of one sandbox's network
//
//	namespace sbx-<id>
//	├── lo                        (up; the sandbox's own loopback)
//	└── tap0                      (the VMM's NIC, the name the checkpoint freezes)
//	    <gateway>/30              the sandbox's own gateway
//
// The guest carries the other half of the /30 and its default route points at this
// gateway — the addresse that a later slice will host the sandbox's DNS view and
// egress proxy on. The gateway is the only address the guest has a path to, and in
// this slice the ruleset below drops even that: a sandbox with no egress has no
// egress to its own gateway either.
//
// # What the policy is
//
// Every sandbox gets a table in its own namespace with default-drop input, forward
// and output chains. Nothing is allowed through it, which is the `egress: none`
// mode of RFC D5; the allowlist mode adds explicit accepts for the node's DNS view
// and egress proxy on top of this same table. Because the table lives inside the
// namespace, it cannot outlive it, cannot leak into another sandbox and cannot be
// reached from the guest, which has a kernel and a namespace of its own.
//
// # Scope
//
// This is the node-side half of M3's first slice: the namespace, the fixed tap0,
// the per-sandbox /30 and the default-deny ruleset. The egress allowlist and proxy,
// the DNS view and the preview proxy are not here. Neither is the sandbox control
// plane: Attach is what a caller (sandboxd, M5) calls before it launches a VM, and
// Detach is what it calls after it has stopped one.
package netpolicy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"sync"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

const (
	// NetNSPrefix is what every namespace this package creates is named: sbx-<id>.
	// The prefix is what lets a reader tell the node's sandbox namespaces from the
	// namespaces another component (or an operator) created.
	NetNSPrefix = "sbx-"

	// NetNSDir is the directory a namespace is named under. It is not
	// configuration: `ip netns add` and `ip netns exec` resolve a name under this
	// one directory, so a namespace created anywhere else could not be entered by
	// the commands this package runs, and the launcher (which hands the jailer
	// `--netns`) could not name it either.
	NetNSDir = "/var/run/netns"

	// TapName is the TAP device inside the namespace, and it is fixed on purpose:
	// the device name is part of the state a checkpoint freezes and of the copy of
	// that state a restore loads, so it cannot be a function of the sandbox ID
	// without making every checkpoint of every sandbox a different device. Two
	// sandboxes both own a tap0, in their own namespaces.
	TapName = "tap0"

	// GuestInterface is the guest-side interface id the VMM configures and the
	// interface the guest's identity is applied to. The identity channel
	// (pkg/snapshot's identity.go) matches the two by this name.
	GuestInterface = "eth0"

	// PrefixLength is the per-sandbox prefix: a /30, which is two usable addresses
	// — the namespace's gateway and the guest. It replaces the shared /24 for
	// sandboxes, whose 254 addresses capped a node at ~250 of them; a /30 pool does
	// not have to be shared between sandboxes at all.
	PrefixLength = 30

	// DefaultPool is the supernet sandbox /30s are carved from when a manager names
	// no other. It is inside RFC1918, so a sandbox address can never be a public
	// one, and it is deliberately not the service pool (the bridge /24 in
	// pkg/network): the two allocators must not be able to hand out the same
	// address, and a sandbox /30 is never on the bridge.
	DefaultPool = "10.63.0.0/16"

	// policyTable is the name of the nftables table inside every sandbox namespace.
	policyTable = "sandbox"
)

// Exec runs one host command and returns its combined output. It is a function
// type because that is all a seam needs: a unit test records the argv a manager
// would have run against a host it cannot touch, and production leaves it nil.
type Exec func(ctx context.Context, name string, args ...string) ([]byte, error)

// Occupancy reports the prefixes this node already has an address or a route on.
// It is what keeps a sandbox pool from being carved out of a range some other
// component is already using, including one another node-local tool chose.
type Occupancy func(ctx context.Context) ([]netip.Prefix, error)

// Network is one sandbox's network, as allocated and as created on the node: the
// identity a caller hands to a launch, and the answer to "where is it".
//
// It is a value, not a handle: nothing here keeps the namespace alive, and the
// caller that stops the sandbox gives the record's namespace and TAP to the
// teardown (pkg/vminstance), which frees them the way it frees the disk.
type Network struct {
	// ID is the sandbox this network belongs to.
	ID string
	// Name is the namespace name, sbx-<ID>.
	Name string
	// NetNS is the namespace path the jailer is pointed at (its --netns argument)
	// and the value a record keeps.
	NetNS string
	// Tap is the host TAP device the VMM's interface is attached to. It is inside
	// the namespace, so the name alone does not identify a device on the node.
	Tap string
	// CIDR is the sandbox's own /30.
	CIDR netip.Prefix
	// Gateway is the namespace's address on the TAP device: the guest's only next
	// hop.
	Gateway netip.Addr
	// GuestIP is the guest's address, the other usable address of the /30.
	GuestIP netip.Addr
	// MAC is the link address the guest wears. It is derived from the sandbox ID,
	// so it is stable for one sandbox and different for two, which is what keeps
	// clones of one checkpoint off each other's traffic (B2). The namespace, not
	// the address, is what makes a frozen MAC harmless; this is what keeps it from
	// colliding in the first place.
	MAC string
}

// Identity is the network half of the identity this sandbox's guest adopts: its
// interface, link address, address, next hop and resolver. The caller adds the
// parts only it knows — the host name, the machine id, the agent token, the
// entropy and the clock — and hands the whole thing to the launch, which publishes
// it to the guest's metadata service and has the guest apply it (RFC D10).
//
// It is built here rather than by each caller so that the address the node
// allocates and the address the guest wears cannot drift: there is one place that
// decides both.
func (n Network) Identity() agent.Identity {
	return agent.Identity{
		Interface: GuestInterface,
		MAC:       n.MAC,
		Address:   n.GuestIP.String() + fmt.Sprintf("/%d", n.CIDR.Bits()),
		Gateway:   n.Gateway.String(),
		DNS:       n.DNS(),
	}
}

// DNS is the resolver a guest of this network is configured with: its own
// gateway.
//
// The gateway is the address the node will answer its DNS view on for this
// sandbox (RFC D5's DNS role), and in this slice it answers nothing: the ruleset
// drops every packet including those to the gateway itself, so a guest with this
// resolver resolves no name at all. Naming the sandbox's own gateway as its
// resolver — rather than a node-wide address shared by every sandbox — is what
// keeps the resolver inside the /30 the sandbox was allocated and out of every
// other sandbox's reach.
func (n Network) DNS() []string {
	if !n.Gateway.IsValid() {
		return nil
	}
	return []string{n.Gateway.String()}
}

// Interface is the launch's side of this network: the guest-side interface id the
// VMM configures, and the host TAP device it opens. A caller turns it into the
// request its launcher takes; the three fields are the whole contract, so they are
// returned as they are rather than hidden behind a type.
func (n Network) Interface() (ifaceID, hostTap, guestMAC string) {
	return GuestInterface, n.Tap, n.MAC
}

// Config configures a Manager. The zero value is a working node: the default
// pool, the real command runner, the node's own answer for what is in use.
type Config struct {
	// Pool is the supernet sandbox /30s are carved from. Zero selects DefaultPool.
	Pool netip.Prefix
	// Exec runs one host command. Nil runs it for real.
	Exec Exec
	// Occupancy reports the prefixes already in use on this node. Nil reads the
	// node's own addresses and routes, in its own namespace and in every namespace
	// that already exists.
	Occupancy Occupancy
	// LookPath resolves the ip and nft binaries. Nil uses exec.LookPath. It is a
	// seam for the same reason Exec is: a unit test has no privileged binaries to
	// find, and New must be provable without one.
	LookPath func(string) (string, error)
}

// Manager creates and removes sandbox networks. It is safe for concurrent use:
// allocations are serialized, because two attaches that ran at once could
// otherwise be handed the same /30.
type Manager struct {
	pool      netip.Prefix
	exec      Exec
	occupancy Occupancy
	ip        string
	nft       string

	mu     sync.Mutex
	leases map[string]netip.Prefix
}

// New returns a manager for this node, or the reason it could not be built.
//
// The two binaries are resolved here rather than at first use because both are
// load-bearing: without `ip` no sandbox has a namespace, and without `nft` none
// has a policy, and a sandbox without a policy is exactly what this package
// exists to prevent. A node that cannot enforce the policy must refuse to create
// the sandbox, not create one that is open.
func New(cfg Config) (*Manager, error) {
	lookPath := cfg.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	pool := cfg.Pool
	if !pool.IsValid() {
		parsed, err := netip.ParsePrefix(DefaultPool)
		if err != nil {
			return nil, fmt.Errorf("netpolicy: the built-in pool %s is not a prefix: %w", DefaultPool, err)
		}
		pool = parsed
	}
	pool = pool.Masked()
	if !pool.Addr().Is4() {
		return nil, fmt.Errorf("netpolicy: pool %s is not IPv4: a sandbox's /30 is an IPv4 link", pool)
	}
	if pool.Bits() > PrefixLength {
		return nil, fmt.Errorf("netpolicy: pool %s is smaller than the /%d one sandbox needs", pool, PrefixLength)
	}

	ip, err := lookPath("ip")
	if err != nil {
		return nil, fmt.Errorf("netpolicy: iproute2 is required to create a sandbox namespace: %w", err)
	}
	// nftables ships in /usr/sbin on every distribution this project targets, which
	// is not always on a daemon's PATH, so a miss on PATH is looked for in the
	// standard location before it is called missing.
	nft, err := lookPath("nft")
	if err != nil {
		nft, err = lookPath("/usr/sbin/nft")
		if err != nil {
			return nil, fmt.Errorf("netpolicy: nftables is required to enforce a sandbox's default-deny policy: %w", err)
		}
	}

	execFn := cfg.Exec
	if execFn == nil {
		execFn = runCommand
	}
	occupancy := cfg.Occupancy
	if occupancy == nil {
		occupancy = hostOccupancy(execFn, ip)
	}

	return &Manager{
		pool:      pool,
		exec:      execFn,
		occupancy: occupancy,
		ip:        ip,
		nft:       nft,
		leases:    make(map[string]netip.Prefix),
	}, nil
}

// Pool is the supernet this manager carves sandbox /30s from.
func (m *Manager) Pool() netip.Prefix { return m.pool }

// Name is the namespace name one sandbox's network is created under.
func Name(id string) string { return NetNSPrefix + id }

// Path is the namespace path one sandbox's network is created at. It is what a
// record keeps and what the jailer is pointed at.
func Path(id string) string { return NetNSDir + "/" + Name(id) }

// Attach gives the sandbox id its network: a namespace, the fixed tap0 in it, the
// sandbox's own /30, and a default-deny policy. It is idempotent — attaching an
// already attached sandbox re-applies the same configuration, which is what makes
// a retried attach safe — and a failure leaves nothing behind: a namespace this
// call created is removed again, and an address this call took is given back, so
// the next attach under the same ID starts from the same state this one did.
//
// What a failure does *not* do is take away an address the sandbox already had. A
// retry of an attached sandbox that fails keeps its /30, address and MAC: the guest
// is wearing them, and an attach that moved them would leave it answering on an
// address its own network no longer has.
//
// The order is the point. The addresses are configured before the policy, so the
// policy's own counters are the only thing that ever sees a packet, and the policy
// is applied last, so a sandbox is never described as allocated while it is still
// open.
func (m *Manager) Attach(ctx context.Context, id string) (Network, error) {
	if err := sandboxIDProblem(id); err != nil {
		return Network{}, err
	}
	if err := ctx.Err(); err != nil {
		return Network{}, err
	}

	name := Name(id)
	prefix, took, err := m.reserve(ctx, id)
	if err != nil {
		return Network{}, err
	}
	n, err := newNetwork(id, name, prefix)
	if err != nil {
		m.giveBack(id, took)
		return Network{}, err
	}

	created, err := m.ensureNamespace(ctx, name)
	if err != nil {
		m.giveBack(id, took)
		return Network{}, err
	}
	if err := m.configureLink(ctx, n); err != nil {
		m.undo(ctx, id, name, created, took)
		return Network{}, err
	}
	if err := m.applyPolicy(ctx, name); err != nil {
		m.undo(ctx, id, name, created, took)
		return Network{}, err
	}
	return n, nil
}

// Detach removes the sandbox's network. It is idempotent: a sandbox whose
// namespace is already gone, and one that was never attached, are both success.
//
// Removing the namespace is removing the whole network — the TAP device, its
// addresses, its routes and the ruleset that lives in it — because all of them are
// state of the namespace. That is why the ruleset needs no separate removal step
// here, and it is also why the teardown (pkg/vminstance) can free a sandbox's TAP
// by freeing its namespace: the two are one resource.
func (m *Manager) Detach(ctx context.Context, id string) error {
	if err := sandboxIDProblem(id); err != nil {
		return err
	}

	err := network.RemoveNetNS(Path(id))
	m.forget(id)
	return err
}

// newNetwork is the network a /30 describes: the gateway the guest's default route
// points at, the guest's own address, and the link address this sandbox wears.
func newNetwork(id, name string, prefix netip.Prefix) (Network, error) {
	gateway := prefix.Addr().Next()
	guest := gateway.Next()
	if !prefix.Contains(guest) {
		return Network{}, fmt.Errorf("netpolicy: the /%d %s of sandbox %q has no room for a guest address", PrefixLength, prefix, id)
	}

	return Network{
		ID:      id,
		Name:    name,
		NetNS:   Path(id),
		Tap:     TapName,
		CIDR:    prefix,
		Gateway: gateway,
		GuestIP: guest,
		MAC:     macFor(id),
	}, nil
}

// giveBack releases the sandbox's lease when this call is what took it. An address
// that was already the sandbox's stays where it is (see Attach).
func (m *Manager) giveBack(id string, took bool) {
	if took {
		m.forget(id)
	}
}

// undo ends an attach that failed after it changed something. created is whether
// this call made the namespace: a namespace that was already there belongs to a
// sandbox that still exists, so it is left as it was found rather than removed
// around a caller's back. took is whether this call took the address, which is what
// decides whether the address goes back.
func (m *Manager) undo(ctx context.Context, id, name string, created, took bool) {
	if created {
		if _, err := m.exec(ctx, m.ip, "netns", "delete", name); err != nil {
			// The failure that ended the attach is what the caller needs; this one
			// only says the undo did not finish. It is left to the teardown and to
			// the next attach, which re-applies the configuration.
			_ = err
		}
	}
	m.giveBack(id, took)
}

// ensureNamespace creates the sandbox's namespace, and reports whether this call
// is what created it. An existing namespace with this name is reused: the name is
// a function of the sandbox ID, so a namespace under it is this sandbox's, left by
// a manager that died between creating it and recording it.
func (m *Manager) ensureNamespace(ctx context.Context, name string) (bool, error) {
	existing, err := m.namespaces(ctx)
	if err != nil {
		return false, err
	}
	if existing[name] {
		return false, nil
	}
	if _, err := m.exec(ctx, m.ip, "netns", "add", name); err != nil {
		return false, fmt.Errorf("netpolicy: create the namespace %s: %w", name, err)
	}
	return true, nil
}

// namespaces lists the namespace names the node holds. A name has to be in one
// directory for `ip netns exec` and the jailer to resolve it, which is the same
// directory this package creates namespaces in — so a namespace under a sandbox's
// name is a namespace the node can enter, and one it cannot enter is not one.
func (m *Manager) namespaces(ctx context.Context) (map[string]bool, error) {
	names, err := namespaceNames(ctx, m.exec, m.ip)
	if err != nil {
		return nil, fmt.Errorf("netpolicy: %w", err)
	}
	held := make(map[string]bool, len(names))
	for _, name := range names {
		held[name] = true
	}
	return held, nil
}

// sandboxIDProblem refuses an ID this package cannot name a namespace after. The
// rule is the node's own rule for "this string can name a file"
// (runtime.CheckTaskID, which the launcher applies to the same ID), so the
// namespace and the instance cannot disagree about what the sandbox is called.
func sandboxIDProblem(id string) error {
	if err := runtime.CheckTaskID(id); err != nil {
		return fmt.Errorf("netpolicy: sandbox %q cannot name a network namespace: %w", id, err)
	}
	return nil
}

// macFor derives a sandbox's link address from its ID: the first five bytes of a
// digest, under a locally administered unicast prefix.
//
// Deriving it, rather than allocating it, is what makes it stable across a
// restart with no state to keep: the same sandbox comes back with the same link
// address, and two sandboxes differ. The node has no other allocator to consult
// (pkg/network's IPAllocator allocates addresses, not link addresses), and a
// derived address cannot be leaked.
func macFor(id string) string {
	sum := sha256.Sum256([]byte("swarmcracker/sandbox/mac/" + id))
	return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", sum[0], sum[1], sum[2], sum[3], sum[4])
}

// runCommand is the production Exec: the node's own shell.
func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// errNoPoolSpace is returned when every /30 in the pool is taken.
var errNoPoolSpace = errors.New("netpolicy: every /30 of the sandbox pool is in use")
