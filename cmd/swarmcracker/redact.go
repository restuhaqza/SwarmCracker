package main

import (
	"encoding/json"

	"github.com/moby/swarmkit/v2/api"
)

// redactedRegistryAuth is the placeholder that replaces an X-Registry-Auth
// payload in serialized CLI output. It is deliberately not the empty string: an
// operator must still be able to see that a service carries credentials, just
// not what they are.
const redactedRegistryAuth = "***"

// redactedService returns a deep copy of svc whose registry credentials are
// replaced by redactedRegistryAuth. The copy is what gets serialized, so the
// response object the caller keeps using is never mutated and every other field
// round-trips unchanged. A nil service is returned as nil.
func redactedService(svc *api.Service) *api.Service {
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

// redactedTask returns a deep copy of task whose registry credentials are
// replaced by redactedRegistryAuth, with the same no-mutation guarantee as
// redactedService. A nil task is returned as nil.
func redactedTask(task *api.Task) *api.Task {
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
	container.PullOptions.RegistryAuth = redactedRegistryAuth
}

// marshalServicesJSON renders a service listing for `service ls --format json`,
// masking the registry credentials of every service in it.
func marshalServicesJSON(services []*api.Service) ([]byte, error) {
	redacted := make([]*api.Service, len(services))
	for i, svc := range services {
		redacted[i] = redactedService(svc)
	}
	return json.MarshalIndent(redacted, "", "  ")
}

// marshalServiceJSON renders one service for `service inspect --format json`,
// masking the registry credential it carries.
func marshalServiceJSON(svc *api.Service, pretty bool) ([]byte, error) {
	if pretty {
		return json.MarshalIndent(redactedService(svc), "", "  ")
	}
	return json.Marshal(redactedService(svc))
}
