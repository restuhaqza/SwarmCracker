package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/golden"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox/netpolicy"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// The pieces a sandbox is built from, as the manager sees them. Each is an
// interface for the same reason the launcher's own seams are: a unit test cannot
// create a network namespace, copy gigabytes or boot a VMM, so it hands back the
// value the real one would and holds the order and the decisions to account.
// Production wires the real ones.

// NetworkPolicy is the node's sandbox networking (pkg/sandbox/netpolicy): the
// namespace, the fixed tap0 in it, the /30, the default-deny policy and the egress
// allowlist. Attach is called before a VM is launched and Detach after it is gone.
type NetworkPolicy interface {
	Attach(ctx context.Context, id string, allow []string) (netpolicy.Network, error)
	Detach(ctx context.Context, id string) error
}

// Launcher starts one sandbox's microVM. It is snapshot.Launcher, which is the one
// way an instance enters the world in this build, and the contract that matters
// here is its readiness rule: a launch returns only once the guest agent answered
// its hello, so "ready" in a sandbox record means the agent answered, not that the
// VMM started.
type Launcher interface {
	Start(ctx context.Context, req snapshot.LaunchRequest) (snapshot.Launched, error)
}

// jailRooter is the part of the jailed launcher this package needs beyond Start:
// the host directory an instance's jail lives in. It is a separate interface rather
// than a field because the layout belongs to the jailer, and a launcher that does
// not build jails has no jail root to report — in which case a sandbox records
// none and no teardown tries to remove one.
type jailRooter interface {
	JailRoot(instanceID string) string
}

// Config configures a Manager. Everything a sandbox needs on the node is named
// here, because a manager that guessed at one of these would boot a VM whose disk,
// network or jail nobody could find again.
type Config struct {
	// Store is the sandbox record store. Required.
	Store *Store

	// GoldenDir holds the sealed templates a sandbox is created from. Required.
	GoldenDir string
	// KernelPath is the kernel a template with no mapped profile boots, and the
	// fallback the pin is checked against (golden.NodeHostPin). Required.
	KernelPath string
	// KernelProfiles maps a template's kernel profile to a kernel image on this
	// host, exactly as the node's own boot path does. Nil means the built-in
	// registry, which is what a node that configures nothing gets.
	KernelProfiles map[string]string
	// FirecrackerBinary is the VMM whose version the template pin is checked
	// against. Empty means the golden package's default (firecracker on PATH).
	FirecrackerBinary string

	// RootfsDir is the node's root filesystem directory. Each sandbox's own disk is
	// materialized at <RootfsDir>/sandboxes/<id>.ext4. Required.
	RootfsDir string
	// LogDir is where a sandbox's serial console is written. Empty leaves the
	// console unrecorded.
	LogDir string

	// Launcher starts the microVM. Required: a sandbox that is not launched is not
	// a sandbox.
	Launcher Launcher
	// Network gives the sandbox its namespace and policy. Required.
	Network NetworkPolicy
	// Instances is the node's registry of running VMs, which is where a sandbox's
	// VMM, sockets and jail uid are recorded and where its teardown frees the netns
	// and the disk. Nil means this node keeps no registry, and then a sandbox has
	// no liveness and no teardown through it: only a test runs that way.
	Instances vminstance.Registry

	// ParentCgroup is the parent cgroup the jailer creates an instance's cgroup
	// under: the instance's cgroup is <cgroupBase>/<ParentCgroup>/<id>. It must be
	// the value the launcher was configured with, because rm removes exactly the
	// directory the jailer created. Empty means the jailer's own default, named here
	// so a teardown can find it.
	ParentCgroup string

	// VCPUs and MemoryMB are the default size of a sandbox. A caller may ask for
	// another size per sandbox; these are what it gets when it does not. Zero means
	// the node's own default (one vCPU, 512 MiB).
	VCPUs    int
	MemoryMB int

	// DefaultTTL is how long a sandbox may idle before the reaper takes it back,
	// for a sandbox created without a TTL of its own. Zero means the reaper has
	// nothing to do, which is the safe default: a node that never configured a TTL
	// must not start deleting sandboxes someone is using.
	DefaultTTL time.Duration
	// Allow is the egress allowlist a sandbox is created with when the caller names
	// none: "host:port" destinations, or empty for egress none, which is the
	// default a sandbox should have.
	Allow []string

	// RevokeTokens deletes the API tokens scoped to one sandbox, and reports how
	// many it deleted. Nil means this node issues no sandbox-scoped tokens, and then
	// rm leaves them alone. It is a seam because the token store lives with the API
	// (pkg/apihttp) and this package must not depend on it.
	RevokeTokens func(ctx context.Context, sandboxID string) (int, error)

	// Now is the clock the records are stamped with. Nil is time.Now. It is a seam
	// because the reaper's whole decision is a comparison against the node's clock.
	Now func() time.Time
}

// Manager creates, runs and removes sandboxes on one node. It is safe for
// concurrent use: two creates that ran at once would otherwise be handed the same
// ID, and one sandbox's teardown must not race another's.
type Manager struct {
	cfg   Config
	now   func() time.Time
	slots chan struct{}
}

// New builds a manager, or says which piece of the node is missing. Every check
// here is a check that would otherwise be made half-way through building a
// sandbox, after a template had been copied and a namespace created.
func New(cfg Config) (*Manager, error) {
	if cfg.Store == nil {
		return nil, errors.New("sandbox: a record store is required")
	}
	if strings.TrimSpace(cfg.GoldenDir) == "" {
		return nil, errors.New("sandbox: a directory of sealed templates is required")
	}
	if strings.TrimSpace(cfg.KernelPath) == "" {
		return nil, errors.New("sandbox: a kernel path is required: a template's pin is checked against the kernel this node would boot it with")
	}
	if strings.TrimSpace(cfg.RootfsDir) == "" {
		return nil, errors.New("sandbox: a rootfs directory is required: every sandbox gets its own disk under it")
	}
	if cfg.Launcher == nil {
		return nil, errors.New("sandbox: a launcher is required")
	}
	if cfg.Network == nil {
		return nil, errors.New("sandbox: a network policy is required: a sandbox with no namespace is open to the node")
	}
	if cfg.KernelProfiles == nil {
		cfg.KernelProfiles = config.DefaultKernelProfiles()
	}
	if cfg.ParentCgroup == "" {
		cfg.ParentCgroup = defaultParentCgroup
	}
	if cfg.VCPUs <= 0 {
		cfg.VCPUs = 1
	}
	if cfg.MemoryMB <= 0 {
		cfg.MemoryMB = 512
	}
	if cfg.DefaultTTL < 0 {
		return nil, fmt.Errorf("sandbox: idle TTL %s is negative", cfg.DefaultTTL)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	return &Manager{
		cfg: cfg,
		now: now,
		// One create at a time. Building a sandbox copies a disk and boots a VM,
		// which is the resource a node has least of, and serializing it is what
		// keeps two creates from racing each other's ID allocation and disk.
		slots: make(chan struct{}, 1),
	}, nil
}

// Store is the manager's record store.
func (m *Manager) Store() *Store { return m.cfg.Store }

// CreateRequest is what a caller asks for. Only Template is required; everything
// else has a default the node's configuration decides.
type CreateRequest struct {
	// Template is the sealed template to boot, by "name" (newest version) or
	// "name@version". It is resolved through golden.Resolve, which is the only path
	// to a template, so the template's pin and its agent are checked here.
	Template string
	// ID is the sandbox's identity. Empty mints one. A caller that names one gets
	// it, and an ID that is already a sandbox is refused rather than replaced: two
	// sandboxes cannot share a disk, a namespace or a jail.
	ID string
	// Owner is who asked for it, kept in the record for tracing. Empty is allowed.
	Owner string
	// Allow is the egress allowlist: "host:port" destinations the sandbox may
	// reach, or empty for egress none. It is the network policy's vocabulary, and
	// it is resolved and installed at attach.
	Allow []string
	// TTL is how long this sandbox may idle before the reaper takes it back. Zero
	// means the node's default; a negative value means never.
	TTL time.Duration
	// VCPUs and MemoryMB are the sandbox's size. Zero means the node's default.
	VCPUs    int
	MemoryMB int
}

// Create builds one sandbox from a sealed template and returns its record once the
// guest agent has answered.
//
// The order is the point, and it is the order the pieces already promise:
//
//  1. The template is resolved and its pin checked (golden.Resolve) — a template
//     built by other host software is refused here, before a disk is copied and
//     before a VMM exists.
//  2. The record is written in state created, so the sandbox is visible while it is
//     being built. A node that dies mid-create leaves a created record rather than
//     nothing, which is what the reaper and a retried rm work from.
//  3. The network is attached: the namespace, tap0, the /30 and the default-deny
//     policy, with the allowlist as its only openings (netpolicy.Attach).
//  4. The per-instance disk is materialized from the template's artifact, and the
//     sandbox's own per-boot agent token is written into it. The token is in the
//     disk before the guest boots because that is the only moment it can be: the
//     guest's agent requires the token from its first hello, and Firecracker
//     refuses to rewrite a disk the guest is already running on.
//  5. The VM is launched inside the jailer, in the sandbox's namespace, with an
//     identity of its own (host name, machine id, entropy, clock, address, MAC)
//     that the guest applies through the metadata service.
//  6. The record becomes ready with what the guest answered. Ready is the agent's
//     hello, not the load: an instance whose agent never answers is a failed create.
//
// A create that fails at any step after the record exists leaves that record in
// state failed with the reason, and frees what it allocated: a failed sandbox holds
// nothing, and says why it is not usable. The record is kept rather than deleted
// because "this sandbox was asked for and this is what happened" is what an operator
// needs, and because the reaper — not the create path — is what forgets a sandbox.
func (m *Manager) Create(ctx context.Context, req CreateRequest) (Record, error) {
	m.slots <- struct{}{}
	defer func() { <-m.slots }()

	ref := strings.TrimSpace(req.Template)
	if ref == "" {
		return Record{}, fmt.Errorf("%w: a template is required (try `swarmcracker template ls`)", ErrInvalid)
	}
	if req.TTL < 0 {
		// A negative TTL asks for a sandbox that is never reaped, and a record
		// carries that as zero. It is one of the two ways to say it, and the one a
		// caller with a shell finds natural.
		req.TTL = 0
	}

	artifact, err := golden.Resolve(ctx, m.cfg.GoldenDir, ref, golden.NodeHostPin(m.cfg.FirecrackerBinary, m.cfg.KernelPath, m.cfg.KernelProfiles))
	if err != nil {
		return Record{}, err
	}
	kernel, err := m.kernelFor(artifact)
	if err != nil {
		return Record{}, err
	}

	id := strings.TrimSpace(req.ID)
	if id == "" {
		id, err = m.mintID()
		if err != nil {
			return Record{}, err
		}
	}
	if err := checkID(id); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, taken, err := m.cfg.Store.Get(id); err != nil {
		return Record{}, err
	} else if taken {
		return Record{}, fmt.Errorf("%w: %s", ErrExists, id)
	}

	now := m.now().UTC()
	ttl := req.TTL
	if ttl == 0 {
		ttl = m.cfg.DefaultTTL
	}
	allow := req.Allow
	if allow == nil {
		allow = m.cfg.Allow
	}

	rec := Record{
		ID:             id,
		State:          StateCreated,
		Template:       artifact.Ref,
		Owner:          strings.TrimSpace(req.Owner),
		Created:        now,
		Updated:        now,
		LastActive:     now,
		IdleTTLSeconds: int64(ttl / time.Second),
		Egress:         append([]string(nil), allow...),
		Kernel:         kernel,
	}
	if err := m.cfg.Store.Put(rec); err != nil {
		return Record{}, err
	}

	// From here the record exists, so every failure has to leave it saying what
	// happened rather than vanishing. fail() frees what this call allocated and
	// records the reason.
	fail := func(cause error) (Record, error) {
		failed, failErr := m.fail(ctx, &rec, cause)
		if failErr != nil {
			// Both failures matter: the cause is what the caller asked about, and
			// the residual resources are what an operator has to clean up.
			log.Warn().Err(failErr).Str("sandbox_id", rec.ID).
				Msg("Could not free everything a failed sandbox create allocated")
			return failed, errors.Join(cause, failErr)
		}
		return failed, cause
	}

	network, err := m.cfg.Network.Attach(ctx, id, allow)
	if err != nil {
		return fail(err)
	}
	rec.NetNS, rec.TAP = network.NetNS, network.Tap
	rec.GuestIP, rec.MAC = network.GuestIP.String(), network.MAC
	// Record what the network allocated as soon as it exists. A create that dies
	// from here on — this process killed, the node losing power — leaves a record
	// whose teardown knows which namespace to free; a namespace that only a live
	// caller's memory knew would outlive the caller with nothing naming it.
	if err := m.save(&rec); err != nil {
		return fail(err)
	}

	disk := m.diskPath(id)
	copyMode, err := m.materializeDisk(ctx, artifact, disk)
	if err != nil {
		return fail(err)
	}
	rec.Disk, rec.DiskCopy = disk, string(copyMode)
	if err := m.save(&rec); err != nil {
		return fail(err)
	}

	token, err := m.provisionToken(ctx, disk)
	if err != nil {
		return fail(err)
	}

	size := m.size(req.VCPUs, req.MemoryMB)
	launched, err := m.cfg.Launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: id,
		Mode:       snapshot.ModeCold,
		Kernel:     kernel,
		Rootfs:     disk,
		VCPUs:      size.vcpus,
		MemoryMB:   size.memoryMB,
		NetNS:      network.NetNS,
		VsockPath:  m.chrootVsockPath(id),
		AgentToken: token,
		Net: []snapshot.NetworkInterface{{
			IfaceID:  ifaceID,
			HostTap:  network.Tap,
			GuestMAC: network.MAC,
		}},
		Identity: m.identity(id, network, token),
		Record: vminstance.Instance{
			ID:      id,
			Kind:    vminstance.KindSandbox,
			OwnerID: strings.TrimSpace(req.Owner),
			Disk:    disk,
			TAP:     network.Tap,
			GuestIP: network.GuestIP.String(),
			MAC:     network.MAC,
		},
	})
	if err != nil {
		return fail(err)
	}

	if rooter, ok := m.cfg.Launcher.(jailRooter); ok {
		rec.JailRoot = rooter.JailRoot(id)
	}
	rec.VsockPath = launched.VsockPath
	// The host's copy of the token sits beside the muxer the guest answers on. It
	// is what the node presents on every later connection, and it is the only
	// credential a sandbox has: rm deletes it.
	tokenFile := agent.TokenFilePath(launched.VsockPath)
	if err := agent.WriteTokenFile(tokenFile, token); err != nil {
		return fail(fmt.Errorf("publish the sandbox's agent token: %w", err))
	}
	rec.AgentTokenFile = tokenFile
	rec.AgentVersion, rec.BootID = launched.Hello.AgentVersion, launched.Hello.BootID

	ready := m.now().UTC()
	rec.State, rec.ReadyAt, rec.Updated, rec.LastActive = StateReady, ready, ready, ready
	if err := m.cfg.Store.Put(rec); err != nil {
		// The guest is up and the record cannot say so, which no reader can act
		// on; fail() stops the sandbox rather than leaving a VM nothing resolves.
		return fail(fmt.Errorf("record the sandbox as ready: %w", err))
	}

	log.Info().
		Str("sandbox_id", rec.ID).
		Str("template", rec.Template).
		Str("state", string(rec.State)).
		Str("guest_ip", rec.GuestIP).
		Str("netns", rec.NetNS).
		Str("disk_copy", rec.DiskCopy).
		Str("agent_version", rec.AgentVersion).
		Dur("ready_in", rec.ReadyAtDuration()).
		Msg("Sandbox is ready")

	return rec, nil
}

// save writes the record as it stands, and is what every step of a create calls
// once it has allocated something. The record is the node's only list of what a
// sandbox holds, so a resource that exists is recorded before the next one is
// allocated: a create that dies half-way then leaves a record whose teardown frees
// exactly what was really there.
func (m *Manager) save(rec *Record) error {
	rec.Updated = m.now().UTC()
	return m.cfg.Store.Put(*rec)
}

// Get returns one sandbox's record.
func (m *Manager) Get(id string) (Record, error) {
	rec, found, err := m.cfg.Store.Get(strings.TrimSpace(id))
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return rec, nil
}

// List returns every sandbox on this node, sorted by ID, after reconciling the
// records against the node's running VMs. skipped names the record files that could
// not be read at all, so a listing can report a bad file without hiding the rest.
//
// Reconciliation is a write, and it is why List is a manager operation rather than
// a store read: a record that says ready while its VMM is gone is a lie a reader
// cannot be expected to detect, and the node's writer is the one that can correct
// it. A sandbox in state created is left alone — it may be mid-create in another
// call — and the reaper's TTL is what takes a create that died.
func (m *Manager) List() ([]Record, []string, error) {
	records, skipped, err := m.cfg.Store.List()
	if err != nil {
		return nil, nil, err
	}

	out := make([]Record, 0, len(records))
	for _, rec := range records {
		if rec.State == StateReady && !m.running(rec.ID) {
			if failed, err := m.markFailed(&rec, errVMGone); err == nil {
				rec = failed
			}
		}
		out = append(out, rec)
	}
	return out, skipped, nil
}

// errVMGone is the reason a sandbox gets when its VMM is no longer there.
var errVMGone = errors.New("the sandbox's VMM is gone: the instance is no longer in this node's registry")

// running reports whether the node still has a live VM recorded for id.
func (m *Manager) running(id string) bool {
	if m.cfg.Instances == nil {
		return true
	}
	_, live, err := m.cfg.Instances.Get(id)
	if err != nil {
		// A registry that cannot be read is not evidence that a sandbox died.
		return true
	}
	return live
}

// fail records that a sandbox could not be made usable and frees what the failed
// attempt allocated, so a failed sandbox holds nothing and says why. The record is
// kept: it is the answer to "what happened to the sandbox I asked for", and the
// reaper is what forgets it.
//
// A free that did not finish is why the record keeps its paths. The fields name the
// resources a teardown frees, so a record whose teardown failed is the handle the
// next attempt uses; clearing them would leave resources nothing names. The address
// and MAC stay either way: they are what this sandbox had, and a record that is
// failed is not claiming to be usable.
func (m *Manager) fail(ctx context.Context, rec *Record, cause error) (Record, error) {
	_, err := m.free(ctx, rec)

	rec.State = StateFailed
	rec.Reason = cause.Error()
	rec.Updated = m.now().UTC()
	if err == nil {
		rec.Disk, rec.DiskCopy, rec.JailRoot, rec.VsockPath, rec.AgentTokenFile = "", "", "", "", ""
	}
	if putErr := m.cfg.Store.Put(*rec); putErr != nil {
		return *rec, errors.Join(putErr, err)
	}
	return *rec, err
}

// markFailed flips a record to failed and writes it, for a sandbox the node found
// already gone rather than one this call was building.
func (m *Manager) markFailed(rec *Record, cause error) (Record, error) {
	next := *rec
	if !CanTransition(next.State, StateFailed) {
		return *rec, fmt.Errorf("sandbox %s: %s cannot become %s", next.ID, next.State, StateFailed)
	}
	next.State = StateFailed
	next.Reason = cause.Error()
	next.Updated = m.now().UTC()
	if err := m.cfg.Store.Put(next); err != nil {
		return *rec, err
	}
	log.Warn().Str("sandbox_id", next.ID).Str("reason", next.Reason).
		Msg("Sandbox is marked failed: its VMM is no longer running")
	return next, nil
}

// size resolves the size a sandbox is created with.
func (m *Manager) size(vcpus, memoryMB int) struct{ vcpus, memoryMB int } {
	size := struct{ vcpus, memoryMB int }{vcpus: m.cfg.VCPUs, memoryMB: m.cfg.MemoryMB}
	if vcpus > 0 {
		size.vcpus = vcpus
	}
	if memoryMB > 0 {
		size.memoryMB = memoryMB
	}
	return size
}

// diskPath is where one sandbox's own disk lives. It is a function of the sandbox
// ID and one directory, so the disk a teardown must free is never guessed.
func (m *Manager) diskPath(id string) string {
	return filepath.Join(m.cfg.RootfsDir, DirName, id+".ext4")
}

// chrootVsockPath is where a sandbox's guest agent answers, as the *instance* sees
// it: every jailed instance has its own /run/firecracker, and the launcher resolves
// this to the host path that reaches the same file (snapshot.Launched.VsockPath).
// The guest agent's device is configured with this path, so it is also the value a
// checkpoint of the sandbox would record.
func (m *Manager) chrootVsockPath(id string) string {
	return fmt.Sprintf("/run/firecracker/%s.vsock", id)
}

// identity is the identity a sandbox's guest adopts: everything that makes it a
// machine of its own rather than a copy of the template it was built from. The
// network half comes from the network policy, which is what keeps the address the
// node allocated and the address the guest wears from drifting; the rest is minted
// here, because the node is the only one that can.
func (m *Manager) identity(id string, network netpolicy.Network, token string) *agent.Identity {
	identity := network.Identity()
	identity.Hostname = "sandbox-" + id
	identity.MachineID = randomHex(16)
	identity.Entropy = randomHex(32)
	identity.ClockUnixNano = m.now().UTC().UnixNano()
	// The token the launch hands the guest is the one the readiness dial presents
	// and the one the guest writes into its own token file, so the sandbox's
	// credential is this boot's and no other sandbox's.
	identity.Token = token
	return &identity
}

// kernelFor resolves the kernel image a template boots, by the same rule the pin
// check used (golden.KernelFor), so the kernel a sandbox is booted with is the
// kernel it was held to.
func (m *Manager) kernelFor(artifact *golden.Artifact) (string, error) {
	if artifact == nil || artifact.Metadata == nil {
		return "", errors.New("sandbox: the resolved template carries no manifest")
	}
	kernel := golden.KernelFor(artifact.Metadata.KernelProfile, m.cfg.KernelPath, m.cfg.KernelProfiles)
	if kernel == "" {
		return "", fmt.Errorf("sandbox: template %s names kernel profile %q and this node maps no kernel to it",
			artifact.Ref, artifact.Metadata.KernelProfile)
	}
	return kernel, nil
}

// materializeDisk gives the sandbox its own disk: a copy of the template's artifact
// at the sandbox's own path. It is a copy and not a share on purpose — the guest
// writes to its disk, and two sandboxes booting one artifact would be one sandbox
// with two kernels.
func (m *Manager) materializeDisk(ctx context.Context, artifact *golden.Artifact, dest string) (snapshot.CopyMode, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return snapshot.CopyNone, fmt.Errorf("sandbox: create the disk directory for %s: %w", dest, err)
	}
	mode, err := snapshot.CopyDisk(ctx, artifact.Path, dest)
	if err != nil {
		return snapshot.CopyNone, fmt.Errorf("sandbox: materialize the disk for template %s: %w", artifact.Ref, err)
	}
	return mode, nil
}

// provisionToken mints this boot's agent token and writes it into the sandbox's
// disk, so the guest agent requires it from its first hello.
//
// The token is minted here rather than left to the node's task path
// (agent.ProvisionForBoot) because this call needs the value as well as the file:
// the launch presents it in the readiness handshake, and a token minted inside
// another call would have to be read back off the disk to be used. The host's copy
// is written later, next to the muxer the guest ends up answering on.
func (m *Manager) provisionToken(ctx context.Context, disk string) (string, error) {
	token, err := agent.NewToken()
	if err != nil {
		return "", err
	}
	if err := agent.InjectToken(ctx, disk, token); err != nil {
		return "", fmt.Errorf("sandbox: write this boot's agent token into the sandbox disk: %w", err)
	}
	return token, nil
}

// mintID mints a sandbox ID: 6 random bytes, hex, which is one safe path element and
// one safe file name (runtime.CheckTaskID's rule).
//
// Twelve characters rather than the 24 a task ID carries, because a sandbox's ID
// appears twice in the host path of its agent's muxer —
// <chroot base>/firecracker/<id>/root/run/firecracker/<id>.vsock — and a unix socket
// address holds at most 107 bytes. With the node's default chroot base that leaves
// room for 17 characters, so a longer ID would make every sandbox fail to launch on
// a node nobody shortened a path on. 48 bits is ample for one node's sandboxes, and a
// collision is caught and re-minted rather than reported.
func (m *Manager) mintID() (string, error) {
	for attempt := 0; attempt < 4; attempt++ {
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("sandbox: mint an ID: %w", err)
		}
		id := hex.EncodeToString(b[:])
		if _, taken, err := m.cfg.Store.Get(id); err != nil {
			return "", err
		} else if !taken {
			return id, nil
		}
	}
	return "", errors.New("sandbox: could not mint an unused ID after four attempts")
}

// randomHex returns n random bytes, hex encoded, or "" if the kernel has no entropy
// to give. An empty value leaves the guest to mint its own, which is what the
// identity's fields mean when they are empty.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// ifaceID is the guest-side interface id a sandbox's NIC is configured with. It is
// the value the checkpoint contract fixes (netpolicy.GuestInterface), so a sandbox's
// checkpoint names the same device every other sandbox's does.
const ifaceID = netpolicy.GuestInterface

// defaultParentCgroup is the cgroup the jailer puts an instance in when nothing
// else is configured, named here so a teardown removes the directory the jailer
// created rather than one this package invented.
const defaultParentCgroup = "firecracker"

// checkID refuses an ID that cannot name a sandbox: one safe path element, because
// the same string is a record file, a network namespace, a jail directory and an
// instance ID. It is the node's own rule (runtime.CheckTaskID), with the noun this
// package's caller asked about.
func checkID(id string) error {
	if strings.TrimSpace(id) != id {
		return fmt.Errorf("sandbox ID %q must not be padded with spaces", id)
	}
	return recordIDProblem(id)
}
