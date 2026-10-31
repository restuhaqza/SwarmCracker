package main

import (
	"github.com/restuhaqza/swarmcracker/pkg/client"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// defaultLocalStateDir is the node state directory the local commands read when
// --state-dir is not given: $SWARM_STATE_DIR when set, otherwise the daemon's
// default (/var/lib/swarmkit, the same value `cluster init`/`join` write into the
// unit). It is client.DefaultConfig().StateDir — the resolution the API token
// store already uses to find daemon state — so one environment variable moves
// every command that reads what the daemon wrote.
func defaultLocalStateDir() string {
	return client.DefaultConfig().StateDir
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
