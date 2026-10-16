// Package redact masks registry credentials before service or task objects are
// serialized into CLI output.
//
// A registry credential lives on api.ContainerSpec.PullOptions.RegistryAuth as
// an opaque base64 X-Registry-Auth payload. SwarmKit ships it back on every
// service and task read, so any command that prints a service or task — inspect,
// ls, ps, and their JSON variants — would echo it verbatim. Issue #44 requires
// that credentials are never written to logs, service inspect or metrics, so
// every serialization path goes through this package first.
//
// Both cmd/swarmcracker and cmd/swarmctl are separate main packages; this one
// package is the single implementation they share.
package redact

import "github.com/moby/swarmkit/v2/api"

// Placeholder is the value that replaces an X-Registry-Auth payload in
// serialized output. It is deliberately not the empty string: an operator must
// still be able to see that a service carries credentials, just not what they
// are.
const Placeholder = "***"

// Service returns a deep copy of svc whose registry credentials are replaced by
// Placeholder. The copy is what gets serialized, so the response object the
// caller keeps using is never mutated and every other field round-trips
// unchanged. A nil service is returned as nil.
func Service(svc *api.Service) *api.Service {
	if svc == nil {
		return nil
	}
	redacted := svc.Copy()
	maskPullOptions(redacted.Spec.Task.GetContainer())
	// PreviousSpec is a full ServiceSpec as well, so a credential the service
	// carried before its last update would leak from there just the same.
	if redacted.PreviousSpec != nil {
		maskPullOptions(redacted.PreviousSpec.Task.GetContainer())
	}
	return redacted
}

// Task returns a deep copy of task whose registry credentials are replaced by
// Placeholder, with the same no-mutation guarantee as Service. A nil task is
// returned as nil.
func Task(task *api.Task) *api.Task {
	if task == nil {
		return nil
	}
	redacted := task.Copy()
	maskPullOptions(redacted.Spec.GetContainer())
	return redacted
}

// maskPullOptions overwrites the credential carried by a container spec's
// PullOptions. A container without credentials keeps a nil PullOptions, so such
// a spec still serializes exactly as it did before redaction existed.
func maskPullOptions(container *api.ContainerSpec) {
	if container == nil || container.PullOptions == nil || container.PullOptions.RegistryAuth == "" {
		return
	}
	container.PullOptions.RegistryAuth = Placeholder
}
