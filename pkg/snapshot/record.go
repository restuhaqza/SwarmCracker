// The instance record a launch publishes (SBX-025).
//
// A running VM's host identity — which process is its VMM, where its API socket,
// vsock muxer, console and netns are, which uid it runs as — is otherwise re-derived
// from an ID and a directory glob, which holds only for the VMs the node booted
// itself from a SwarmKit task (see pkg/vminstance). The launcher is the one place
// where all of it exists together: it chose the socket, the console and the jail, it
// holds the VMM's pid and the uid the jailer runs it as, and the caller hands it the
// identity it allocated for the instance — its kind and owner, its network namespace,
// the TAP device and the address and MAC the instance will carry.
//
// So the launch is what publishes the record, and the record is what every later
// reader asks: the broker's resolver, console attach, health and `vm list` read it
// instead of guessing a path from an ID.
//
// Two rules shape the write, and both are the registry's own (pkg/vminstance):
//
//   - The record is published as soon as the process exists and before the guest is
//     reachable. The paths are true the moment the VMM is up, so a caller can resolve
//     an instance that is still booting, and a launch that fails later still leaves
//     the record a teardown works from. Ready is false until the agent answers
//     (readiness.go), and it is that second write — best effort, because a VM killed
//     for a metadata write is worse than a record that says "still booting" — that
//     marks the instance as one a caller can use.
//   - Nothing is guessed. A field the caller did not fill and the launcher cannot
//     know is recorded empty, which a reader is trained to read as "this instance has
//     none": an instance that boots no NIC has no guest address, and a made-up one
//     would send an operator to an address nobody answers on.
//
// A launcher that holds no registry writes nothing and tears nothing down through
// one, which is the behaviour every caller had before the registry existed.
package snapshot

import (
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// launchFacts is what a launcher knows about the instance it just started, in the
// registry's own terms. Everything else in the record comes from the request: what
// the caller knows about the instance it asked for.
//
// netns is the namespace the launcher actually put the VMM in — the path the jailer
// joined, or empty for a launcher that has no jail, and therefore no namespace of its
// own to put a VMM in. jailUID is zero for a launcher that does not jail the VMM, which
// is what the registry's own field means by zero.
type launchFacts struct {
	pid       int
	apiSocket string
	vsockPath string
	console   string
	jailUID   int
	netns     string
}

// launchRecord completes the caller's record with the launcher's facts.
//
// It refuses a record whose name is not the instance being launched: a record is what
// a reader resolves an ID to, so one that answers to a different name is a record
// nobody can use, and writing it would hide the mistake behind a lookup that finds
// nothing.
func launchRecord(req LaunchRequest, facts launchFacts, booted time.Time) (vminstance.Instance, error) {
	record := req.Record
	if record.ID != "" && record.ID != req.InstanceID {
		return vminstance.Instance{}, fmt.Errorf(
			"the record names instance %q but this launch is %q: one instance has one identity", record.ID, req.InstanceID)
	}

	record.ID = req.InstanceID
	record.PID = facts.pid
	record.APISocket = facts.apiSocket
	record.VsockPath = facts.vsockPath
	record.Console = facts.console
	record.JailUID = facts.jailUID
	record.NetNS = facts.netns
	record.Booted = booted
	record.Ready = false
	return record, nil
}

// publishRecord writes the record of an instance that is starting, and reports the
// failure the launcher must fail the launch with: a node that keeps a registry must
// not run a VM it cannot resolve, because nothing else knows the paths it chose.
func publishRecord(registry vminstance.Registry, record vminstance.Instance) error {
	if registry == nil {
		return nil
	}
	if err := registry.Put(record); err != nil {
		return fmt.Errorf("the instance registry could not record it: %w", err)
	}
	return nil
}

// markRecordReady records that the instance's guest answered, which is what makes the
// record one a reader may act on. It cannot fail the launch: the VM is already up and
// answering, and a record left at Ready=false costs a reader a second look while a
// killed VM costs the caller its instance.
func markRecordReady(registry vminstance.Registry, record vminstance.Instance) {
	if registry == nil || record.ID == "" {
		return
	}
	record.Ready = true
	if err := registry.Put(record); err != nil {
		log.Warn().Err(err).Str("instance_id", record.ID).
			Msg("Could not mark the instance record ready; readers keep seeing it as still booting")
	}
}

// releaseRecord frees what the record names — the netns, the TAP device and the disk,
// none of which the launcher created — and then deletes it. It is what a teardown runs
// after the instance's own process, socket, cgroup, jail and uid are gone, and it is
// best effort for the registry's own reason: the record is kept when a release fails,
// so a retry has the same handle.
//
// A record whose VM is still running is refused by the registry (vminstance.Teardown
// asks the liveness question first), which is what keeps a stop that could not end its
// VMM from freeing the devices of a VM that is still there.
func releaseRecord(registry vminstance.Registry, instanceID string) {
	if registry == nil {
		return
	}
	found, err := vminstance.Teardown(registry, instanceID, vminstance.Leftovers{})
	switch {
	case err != nil:
		log.Warn().Err(err).Str("instance_id", instanceID).
			Msg("Could not release everything the instance record names; the record is kept for a retry")
	case found:
		log.Debug().Str("instance_id", instanceID).Msg("Released the instance record")
	}
}
