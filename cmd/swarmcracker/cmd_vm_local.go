package main

import (
	"github.com/restuhaqza/swarmcracker/pkg/client"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
	"github.com/rs/zerolog/log"
)

// defaultLocalStateDir is the node state directory the local commands read when
// --state-dir is not given: $SWARM_STATE_DIR when it is set, otherwise
// /var/lib/swarmkit, which is the daemon's own default (cmd/swarmd-firecracker)
// and the value `cluster init`/`join` write into the unit.
//
// The two agree only while neither is overridden: the daemon takes its state
// directory from its own --state-dir and does not read $SWARM_STATE_DIR, so a
// daemon started with a different --state-dir needs the matching --state-dir (or
// the variable) on these commands, or their record lookups read a directory the
// daemon never wrote. It is client.DefaultConfig().StateDir — the resolution the
// API token store already uses to find daemon state — so one environment variable
// moves every command that reads what the daemon wrote.
func defaultLocalStateDir() string {
	return client.DefaultConfig().StateDir
}

// vmInstanceStore opens the node's instance registry (pkg/vminstance) under the CLI's
// state directory: the same store `vm create` writes and the daemon keeps, so a
// local command resolves the VMs the node recorded instead of deriving their
// paths from an ID.
//
// It returns nil when the directory cannot be opened — an unprivileged run on a
// node whose state directory belongs to the daemon — because "no registry" is a
// state every reader handles: resolution falls back to the derivation, which is
// what every node did before records existed. A typed-nil *vminstance.Store would
// not do: it is a non-nil interface whose every call fails, so the failure is
// logged here and nil is returned.
func vmInstanceStore() *vminstance.Store {
	dir := vminstance.Dir(defaultLocalStateDir())
	if dir == "" {
		return nil
	}
	store, err := vminstance.NewStore(vminstance.Config{Dir: dir})
	if err != nil {
		log.Warn().Err(err).
			Msg("Could not open the instance registry; resolving VMs from their socket names")
		return nil
	}
	return store
}

// localVsockPath answers which vsock muxer a task's VM listens on.
//
// A task SwarmKit started from a migration artifact runs a VM whose Firecracker
// state names the *source* task's muxer, so the path cannot be derived from this
// task's ID; the node's restored-instance record is what says which one to dial
// (pkg/runtime.RestoredVsockPath reads it). Every other task has no record, and
// the derivation is exactly what it always was.
//
// A record whose muxer is gone is still returned: the dial then fails with the
// path that VM's state names, which is the true state of that task. Falling back
// to the derived path would dial a socket that belongs to nobody the caller asked
// about.
func localVsockPath(socketDir, stateDir, taskID string) string {
	if recorded, ok := scruntime.RestoredVsockPath(stateDir, taskID); ok {
		return recorded
	}
	return scruntime.VsockPath(socketDir, taskID)
}
