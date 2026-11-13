// Package sandboxd assembles the node's sandbox control plane: the record store,
// the instance registry, the network policy, the jailed launcher, the API token
// store and the manager the control plane serves.
//
// It is a package rather than a function inside the daemon so the same assembly
// can be driven from two places: cmd/sandboxd, which maps its flags onto Config,
// and a host test that needs a manager wired the way the daemon wires it, from
// values it names itself. A test that assembled its own manager would be
// evidence about the test, not about the daemon's path.
package sandboxd

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/apihttp"
	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox/netpolicy"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// Config is everything a sandbox needs on this node, as values.
//
// The defaults a caller leaves empty are the node's own paths, the ones
// swarmd-firecracker and the CLI use, so a sandbox created here is found by
// everything else on the node.
type Config struct {
	// StateDir is the node's state directory: the sandbox records, the instance
	// registry and, unless TokenFile names another, the API token store.
	StateDir string

	// GoldenDir holds the sealed templates a sandbox is created from.
	GoldenDir string
	// KernelPath is the kernel a template with no mapped profile boots.
	KernelPath string
	// KernelProfiles maps a template's kernel profile to a kernel image on this
	// host. Nil means the built-in registry.
	KernelProfiles map[string]string

	// RootfsDir is the node's root filesystem directory: each sandbox's own disk
	// is materialized under it.
	RootfsDir string
	// LogDir is where a sandbox's serial console is written.
	LogDir string

	// JailerPath, JailerChrootDir, ParentCgroup and the two uid bounds are the
	// jailer's configuration: the binary, the base directory its chroots live
	// under, the cgroup it creates an instance's cgroup in, and the range of uids
	// an instance may run as.
	JailerPath      string
	JailerChrootDir string
	JailerUIDFirst  int
	JailerUIDSize   int
	ParentCgroup    string

	// VMMPidsMax is the pids.max a sandbox's VMM cgroup gets; 0 means no cgroup.
	// VMMMemoryMaxMB is its memory.max in MiB; 0 leaves it unset.
	VMMPidsMax     int
	VMMMemoryMaxMB int

	// VCPUs and MemoryMB are the default size of a sandbox.
	VCPUs    int
	MemoryMB int

	// DefaultTTL is how long a sandbox may idle before the reaper takes it back,
	// for a sandbox created without a TTL of its own.
	DefaultTTL time.Duration
	// ReadyTimeout is how long a sandbox's boot may take to reach the guest
	// agent's hello.
	ReadyTimeout time.Duration

	// TokenFile is the token store the API authenticates against. Empty means
	// <StateDir>/api/tokens.json.
	TokenFile string

	// Checkpoints is the node's sandbox checkpoints, and CheckpointBlobs the D11
	// chunk store their memory images live in. Snapshots creates the Firecracker
	// images a checkpoint is made of.
	//
	// All three nil means this node takes and forks no checkpoints, which is what
	// this build's daemon configures: it serves no route that asks for one. A
	// caller that supplies them gets a manager that takes them; a caller that
	// supplies one or two gets a manager that refuses both rather than half-doing
	// it (sandbox.Manager.checkpointsAvailable).
	Checkpoints     *sandbox.CheckpointStore
	CheckpointBlobs checkpoint.Store
	Snapshots       sandbox.Snapshotter
}

// New opens everything a sandbox needs on this node and returns the manager and
// the token store the API authenticates against.
func New(cfg Config) (*sandbox.Manager, *apihttp.TokenStore, error) {
	store, err := sandbox.NewStore(sandbox.StoreConfig{Dir: sandbox.Dir(cfg.StateDir)})
	if err != nil {
		return nil, nil, err
	}
	instances, err := vminstance.NewStore(vminstance.Config{Dir: vminstance.Dir(cfg.StateDir)})
	if err != nil {
		return nil, nil, err
	}

	// The network policy is what makes a sandbox isolated rather than merely a VM: its
	// own namespace, the fixed tap0 in it, its own /30 and a default-deny table. It
	// refuses to build without iproute2 and nftables, which is the point.
	network, err := netpolicy.New(netpolicy.Config{})
	if err != nil {
		return nil, nil, err
	}

	uids, err := jailer.NewUIDAllocator(cfg.JailerUIDFirst, cfg.JailerUIDSize)
	if err != nil {
		return nil, nil, err
	}

	// The cgroup the jailer builds for an instance is what makes the VMM's own
	// resource use visible and bounded, and it is the fifth thing a teardown frees.
	// The jailer creates it only when it is given a limit to apply, so the pids limit
	// is what makes a sandbox have a cgroup at all; memory.max is left unset by
	// default because one value would have to cover every sandbox size, and a limit
	// below a guest's own memory is an OOM kill rather than a guard.
	launcher := snapshot.JailedLauncher{
		ChrootBaseDir:  cfg.JailerChrootDir,
		JailerPath:     cfg.JailerPath,
		ParentCgroup:   cfg.ParentCgroup,
		LogDir:         cfg.LogDir,
		UIDs:           uids,
		Registry:       instances,
		ReadyTimeout:   cfg.ReadyTimeout,
		PidsMax:        cfg.VMMPidsMax,
		MemoryMaxBytes: int64(cfg.VMMMemoryMaxMB) << 20,
	}

	tokens, err := apihttp.OpenTokenStore(tokenFile(cfg))
	if err != nil {
		return nil, nil, err
	}

	manager, err := sandbox.New(sandbox.Config{
		Store:           store,
		GoldenDir:       cfg.GoldenDir,
		KernelPath:      cfg.KernelPath,
		KernelProfiles:  cfg.KernelProfiles,
		RootfsDir:       cfg.RootfsDir,
		LogDir:          cfg.LogDir,
		Launcher:        launcher,
		Network:         network,
		Instances:       instances,
		ParentCgroup:    cfg.ParentCgroup,
		VCPUs:           cfg.VCPUs,
		MemoryMB:        cfg.MemoryMB,
		DefaultTTL:      cfg.DefaultTTL,
		Checkpoints:     cfg.Checkpoints,
		CheckpointBlobs: cfg.CheckpointBlobs,
		Snapshots:       cfg.Snapshots,
		// Removing a sandbox removes the credentials scoped to it: a token that
		// outlived its sandbox would be a key to a guest that no longer exists.
		RevokeTokens: func(_ context.Context, id string) (int, error) {
			return tokens.RevokeSandbox(id)
		},
	})
	if err != nil {
		return nil, nil, err
	}
	// Reclaim what a previous life of this node left behind before it serves anything: a
	// checkpoint writes its chunks and then the manifest that names them, so a process
	// killed in between leaves blocks nothing references and nothing would ever trigger
	// the reclamation of. A failure here does not stop the daemon — the chunks are wasted
	// space, not incorrect state — but it is reported, because a store that cannot be
	// swept is one to look at before it grows.
	if _, err := manager.ReconcileCheckpoints(context.Background()); err != nil {
		log.Warn().Err(err).Msg("Could not reclaim orphaned checkpoint chunks at start")
	}
	return manager, tokens, nil
}

// tokenFile is where the API's bearer tokens live: the configured store, then the
// node's own API directory, so a token the operator minted with
// `swarmcracker api token create` is the token this daemon accepts.
func tokenFile(cfg Config) string {
	if path := strings.TrimSpace(cfg.TokenFile); path != "" {
		return path
	}
	return filepath.Join(cfg.StateDir, "api", "tokens.json")
}
