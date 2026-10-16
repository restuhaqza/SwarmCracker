package main

import (
	"encoding/json"

	"github.com/moby/swarmkit/v2/api"

	"github.com/restuhaqza/swarmcracker/pkg/redact"
)

// The CLI prints service and task responses that carry a registry credential on
// their container spec's PullOptions. Every such serialization goes through the
// shared pkg/redact helpers, which mask the credential on a copy; the response
// object other code still uses is never mutated. cmd/swarmctl uses the same
// package.

// marshalServicesJSON renders a service listing for `service ls --format json`,
// masking the registry credentials of every service in it.
func marshalServicesJSON(services []*api.Service) ([]byte, error) {
	redacted := make([]*api.Service, len(services))
	for i, svc := range services {
		redacted[i] = redact.Service(svc)
	}
	return json.MarshalIndent(redacted, "", "  ")
}

// marshalServiceJSON renders one service for `service inspect --format json`,
// masking the registry credential it carries.
func marshalServiceJSON(svc *api.Service, pretty bool) ([]byte, error) {
	if pretty {
		return json.MarshalIndent(redact.Service(svc), "", "  ")
	}
	return json.Marshal(redact.Service(svc))
}
