// The instance identity channel: how a launch gives a guest an identity of its own
// (RFC D10, SBX-042).
//
// A checkpoint freezes the guest's MAC, addresses, routes, resolver, machine-id,
// host keys, clock and entropy pool, and Firecracker refuses to rewrite any of it
// from the host once the image is loaded. The one channel that stays writable is
// MMDS, so a launch publishes the clone's identity there — while a restored guest
// is still paused, or before a cold boot starts — and the guest applies it from
// the inside with the agent's `prepare` operation (pkg/agent's prepare.go).
//
// Nothing here is optional once a request carries an identity:
//
//   - An agent that does not advertise `prepare` fails the launch with
//     ErrPrepareUnsupported and a message naming the protocol and the capabilities
//     it did advertise. It is never skipped: a launch that returned while the guest
//     still wore its source's identity would be the exact failure this contract
//     exists to prevent.
//   - A hook that ran and failed, and a hook whose report does not show the
//     identity that was published, both fail the launch with ErrPrepareFailed. The
//     report is the guest's own answer, and the node holds the guest to what it
//     published instead of trusting that a request was carried out.
//
// A launch that carries no identity publishes nothing and runs no hook: that is
// the pre-sandbox path (a template build boots no NIC at all). A sandbox always
// carries one, so "the hook did not run" and "there was nothing to apply" stay
// distinguishable to a reader of the request.
package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
)

const (
	// mmdsVersion is the metadata service version a launch configures. Version 2
	// requires a session token, which the guest mints itself, and is the version
	// current Firecracker documents; version 1 is deprecated.
	mmdsVersion = "V2"

	// DefaultPrepareTimeout bounds the post-restore hook: the second dial, the
	// guest's read of the metadata service, and its application of the identity.
	// It is a wall-clock budget on the host, so a guest whose own clock is wrong
	// after a restore cannot extend it.
	DefaultPrepareTimeout = 30 * time.Second
)

// ErrPrepareUnsupported means the guest agent cannot run the post-restore hook:
// the agent answered the handshake but does not advertise the operation, so the
// instance cannot be given the identity it needs. It is a failed launch with a
// version error, not a hook that was quietly left out.
var ErrPrepareUnsupported = errors.New("the guest agent cannot run the post-restore hook")

// ErrPrepareFailed means the hook did not produce the identity the launch
// published: the guest could not read the metadata service, could not apply what
// it read, or answered with something else. The instance is torn down rather than
// handed over wearing the identity of the machine its checkpoint came from.
var ErrPrepareFailed = errors.New("the post-restore hook failed")

// mmdsIdentityKey is the metadata key the identity document is published under.
// It is derived from the path the guest reads, so the two sides cannot drift.
var mmdsIdentityKey = strings.TrimPrefix(agent.MMDSIdentityPath, "/")

// The two calls the identity channel makes to the VMM are seams for the same
// reason the other API calls in this package are: a unit test has no Firecracker
// to write to, so it records what a launch would have published. Production never
// replaces them.
var (
	publishInstanceIdentity = publishIdentity
	configureInstanceMMDS   = configureMetadataService
)

// NetworkInterface is one interface an instance is given, and the host device it
// is attached to.
type NetworkInterface struct {
	// IfaceID is the guest-side interface id the VMM configures, as the
	// checkpoint records it ("eth0"). A restore matches the checkpoint's recorded
	// device by this id.
	IfaceID string
	// HostTap is the host tap device the VMM opens for this interface. The caller
	// owns the device: this package only hands the name to the VMM, and on a
	// restore it is what replaces the tap the checkpoint recorded, so a clone
	// needs no device from the template's node to exist.
	HostTap string
	// GuestMAC is the MAC the VMM presents at boot, which a checkpoint then
	// freezes. It is the *pre-hook* identity: the identity's own MAC replaces it
	// inside the guest (pkg/agent's prepare.go), because Firecracker refuses to
	// change it from here once the image is loaded.
	GuestMAC string
}

// networkProblem refuses an interface description this launcher cannot hand to the
// VMM.
func networkProblem(network []NetworkInterface) error {
	seen := make(map[string]bool, len(network))
	for _, iface := range network {
		if strings.TrimSpace(iface.IfaceID) == "" {
			return errors.New("a network interface needs an iface id (\"eth0\")")
		}
		if seen[iface.IfaceID] {
			return fmt.Errorf("interface id %q is named twice: one id addresses one device", iface.IfaceID)
		}
		seen[iface.IfaceID] = true
		if strings.TrimSpace(iface.HostTap) == "" {
			return fmt.Errorf("interface %q needs the host tap device the VMM opens", iface.IfaceID)
		}
	}
	return nil
}

// identityProblem refuses a request whose identity this node could not apply. A
// request with no identity has no rules: it is the pre-sandbox path.
func identityProblem(network []NetworkInterface, id *agent.Identity) error {
	if id == nil {
		return nil
	}
	if len(network) == 0 {
		return errors.New("an identity is read through the instance's own interface, and this launch configures none: add the interface to Network")
	}
	if err := id.Validate(); err != nil {
		return err
	}

	// The two parts the hook has to be able to prove: a clone that kept its
	// source's MAC or address is not a different machine, and the node has no way
	// to tell from the report that anything happened at all.
	if strings.TrimSpace(id.MAC) == "" {
		return errors.New("an identity must name the MAC the clone carries: it is what keeps two clones of one checkpoint off each other's traffic")
	}
	if strings.TrimSpace(id.Address) == "" {
		return errors.New("an identity must name the address the clone carries: it is what keeps two clones of one checkpoint from answering for one address")
	}

	want := id.Interface
	if want == "" {
		want = agent.DefaultIdentityInterface
	}
	for _, iface := range network {
		if iface.IfaceID == want {
			return nil
		}
	}
	return fmt.Errorf("the identity is applied to interface %q, which this launch does not configure", want)
}

// publishIdentity writes id into the instance's metadata service, where the guest
// reads it during the hook.
//
// It happens while the VM is loaded but not yet running (a restore), or before the
// cold boot starts the guest, because a guest that resumed first would read the
// service before the node had written to it. The document is published as a JSON
// *string* under one key: Firecracker answers an object node with a list of its
// keys, so a guest that wants the whole document has to read one leaf.
func publishIdentity(ctx context.Context, socketPath string, id *agent.Identity) error {
	if id == nil {
		return nil
	}
	document, err := json.Marshal(id)
	if err != nil {
		return fmt.Errorf("encode the instance identity for the metadata service: %w", err)
	}
	payload := map[string]string{mmdsIdentityKey: string(document)}
	if err := putFirecrackerAPI(ctx, socketPath, "/mmds", payload); err != nil {
		return fmt.Errorf("publish the instance identity to its metadata service: %w", err)
	}
	return nil
}

// configureMetadataService enables the metadata service on the instance's own
// interfaces, before the guest exists.
//
// Firecracker freezes this configuration when the microVM starts — the call that
// rewrites it afterwards answers `400 not supported after starting the microVM` —
// so a cold boot is the only launch that sets it. A restore inherits it from the
// checkpoint's device state, which is why only the document is rewritten per
// clone.
func configureMetadataService(ctx context.Context, socketPath string, network []NetworkInterface) error {
	if len(network) == 0 {
		return nil
	}
	ids := make([]string, 0, len(network))
	for _, iface := range network {
		ids = append(ids, iface.IfaceID)
	}
	payload := map[string]any{"version": mmdsVersion, "network_interfaces": ids}
	if err := putFirecrackerAPI(ctx, socketPath, "/mmds/config", payload); err != nil {
		return fmt.Errorf("configure the metadata service on %s: %w", strings.Join(ids, ","), err)
	}
	return nil
}

// prepareClient is the part of *agent.Client the hook uses: the handshake it has
// to read the capabilities from, the hook itself, and closing the connection.
type prepareClient interface {
	Hello() agent.Hello
	Prepare(ctx context.Context) (agent.PrepareReport, error)
	Close() error
}

// prepareDial is the one way a launch asks the guest to adopt its identity. It
// presents the launch's token when it has one, exactly like the readiness dial.
var prepareDial = func(ctx context.Context, vsockPath string, port uint32, token string) (prepareClient, error) {
	client, err := agent.DialWithToken(ctx, vsockPath, port, token)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// prepareTimeout is the budget the hook gets: the launcher's own window, or
// DefaultPrepareTimeout when it configures none. A negative window is read as the
// default rather than as "no time at all".
func prepareTimeout(configured time.Duration) time.Duration {
	if configured <= 0 {
		return DefaultPrepareTimeout
	}
	return configured
}

// prepareInstance runs the hook for a launch that carries an identity, and
// returns nothing for one that carries none.
//
// It is one function for both launchers: the post-restore contract is a property
// of the request, not of how the VMM was started, and a launcher that skipped it
// would hand back an instance wearing its source's identity.
func prepareInstance(ctx context.Context, req LaunchRequest, vsockPath string, budget time.Duration) (*agent.PrepareReport, error) {
	if req.Identity == nil {
		return nil, nil
	}
	report, err := runPrepare(ctx, req.InstanceID, vsockPath, req.AgentToken, *req.Identity, budget)
	if err != nil {
		return nil, err
	}
	return &report, nil
}

// runPrepare has the guest apply the identity the launch published, and holds the
// guest to it.
//
// The capability gate is explicit and comes first, so an agent that cannot serve
// the operation is refused with a message that names the protocol and the
// capabilities it did advertise — the case SBX-042 exists for, where the
// alternative is an instance that quietly keeps the identity of the machine its
// checkpoint came from. The client refuses the same operation again if it gets
// past this check, which is the backstop rather than the contract.
func runPrepare(ctx context.Context, instanceID, vsockPath, token string, id agent.Identity, budget time.Duration) (agent.PrepareReport, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	client, err := prepareDial(ctx, vsockPath, agent.DefaultPort, token)
	if err != nil {
		return agent.PrepareReport{}, fmt.Errorf("%w: instance %q: dial the guest to run the hook: %w",
			ErrPrepareFailed, instanceID, err)
	}
	defer func() { _ = client.Close() }()

	hello := client.Hello()
	if !hello.Supports(agent.CapPrepare) {
		return agent.PrepareReport{}, fmt.Errorf(
			"%w: instance %q: the guest agent speaks protocol %s and advertises [%s], which does not include %q: this template's agent predates the post-restore hook, so the clone cannot be given its own identity",
			ErrPrepareUnsupported, instanceID, hello.Version, strings.Join(hello.Caps, " "), agent.CapPrepare)
	}

	report, err := client.Prepare(ctx)
	if err != nil {
		if agent.CodeOf(err) == agent.CodeUnsupportedOp {
			return agent.PrepareReport{}, fmt.Errorf("%w: instance %q: %w", ErrPrepareUnsupported, instanceID, err)
		}
		return agent.PrepareReport{}, fmt.Errorf("%w: instance %q: %w", ErrPrepareFailed, instanceID, err)
	}
	if err := reportSatisfies(id, report); err != nil {
		return report, fmt.Errorf("%w: instance %q: %w", ErrPrepareFailed, instanceID, err)
	}
	return report, nil
}

// reportSatisfies holds the guest's own report to the identity the launch
// published: every part the identity named has to be in the report, or the launch
// failed.
//
// The report is read back inside the guest, so this is what turns "the request was
// accepted" into "the guest is wearing this identity". A part the identity leaves
// empty is not checked, because the guest is allowed to mint it: an identity that
// names no machine id gets a fresh one per clone rather than the checkpoint's.
func reportSatisfies(id agent.Identity, report agent.PrepareReport) error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if id.MAC != "" && !strings.EqualFold(id.MAC, report.MAC) {
		add("the guest carries MAC %q, want %q", report.MAC, id.MAC)
	}
	if id.Address != "" && !addressReported(report.Addresses, id.Address) {
		add("the guest carries addresses %v, want %q among them", report.Addresses, id.Address)
	}
	if id.Hostname != "" && report.Hostname != id.Hostname {
		add("the guest reports host name %q, want %q", report.Hostname, id.Hostname)
	}
	if id.MachineID != "" && report.MachineID != id.MachineID {
		add("the guest carries machine id %q, want %q", report.MachineID, id.MachineID)
	}
	if id.Token != "" && !report.TokenRotated {
		add("the guest did not adopt the token it was given")
	}
	if len(id.DNS) > 0 && !sameStrings(report.DNS, id.DNS) {
		add("the guest resolves through %v, want %v", report.DNS, id.DNS)
	}
	if id.Gateway != "" && !gatewayReported(report.Routes, id.Gateway) {
		add("the guest's routes %v do not use the gateway %q", report.Routes, id.Gateway)
	}
	if len(problems) > 0 {
		return errors.New("the guest did not adopt the identity it was given: " + strings.Join(problems, "; "))
	}
	return nil
}

// addressReported reports whether addresses holds want, compared as the address and
// prefix they mean rather than as the strings they were written as.
//
// net.ParseCIDR returns the address and the network it masks to, so the address has
// to be compared separately from the prefix: "10.99.0.11/24" is not the address
// "10.99.0.0/24", and a guest that came up on the network address instead of the one
// it was given has not adopted the identity.
func addressReported(addresses []string, want string) bool {
	wantIP, wantNet, err := net.ParseCIDR(want)
	if err != nil {
		return false
	}
	for _, address := range addresses {
		haveIP, haveNet, err := net.ParseCIDR(address)
		if err != nil {
			continue
		}
		if haveIP.Equal(wantIP) && net.IPMask(haveNet.Mask).String() == net.IPMask(wantNet.Mask).String() {
			return true
		}
	}
	return false
}

// gatewayReported reports whether some route in routes goes through gateway.
func gatewayReported(routes []string, gateway string) bool {
	want := net.ParseIP(gateway)
	if want == nil {
		return false
	}
	for _, route := range routes {
		_, via, ok := strings.Cut(route, " via ")
		if !ok {
			continue
		}
		if fields := strings.Fields(via); len(fields) > 0 && net.ParseIP(fields[0]).Equal(want) {
			return true
		}
	}
	return false
}

// sameStrings compares two lists in order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// networkOverrides is the list a restore hands /snapshot/load so a clone opens its
// own host devices instead of the ones the checkpoint recorded.
//
// The host device name is the one identity attribute a load may replace; the guest
// MAC is not, which is why the clone's MAC is applied from inside the guest.
func networkOverrides(network []NetworkInterface) []map[string]string {
	overrides := make([]map[string]string, 0, len(network))
	for _, iface := range network {
		overrides = append(overrides, map[string]string{"iface_id": iface.IfaceID, "host_dev_name": iface.HostTap})
	}
	return overrides
}

// resumeLoadedGuest runs a guest that a load left stopped. It is a PATCH of the VM
// state, not InstanceStart: Firecracker refuses InstanceStart once a snapshot is
// loaded.
func resumeLoadedGuest(ctx context.Context, socketPath string) error {
	return patchFirecrackerAPI(ctx, socketPath, "/vm", map[string]string{"state": "Resumed"})
}
