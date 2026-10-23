// This file owns the dry-run half of the write paths: the name-conflict checks a
// create performs without persisting and the synthetic documents a dry run
// returns.
//
// A dry run is the real validation with the commit removed. Everything a caller
// could get wrong — an unresolvable reference, an immutable field, a bad enum, a
// stale If-Match, a duplicate name — fails exactly as it would on the real path;
// only the mutating RPC is skipped. The HTTP layer decides whether a route
// supports dry run at all (select/rollback rules live there, not here).
package client

import (
	"context"

	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// nameConflict is the create-only failure a dry run reports when the name is
// already taken. The real path gets the same answer from SwarmKit's
// AlreadyExists, which pkg/output classifies as conflict.
func nameConflict(kind, name string) error {
	return output.NewErrorf(output.CodeConflict, "a %s named %q already exists", kind, name)
}

// ensureServiceNameFree lists the services and compares the name, because a dry
// run never reaches the CreateService call that would have enforced uniqueness.
func (c *Client) ensureServiceNameFree(ctx context.Context, name string) error {
	resp, err := c.control.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		return err
	}
	if resp == nil {
		return nil
	}
	for _, service := range resp.Services {
		if service != nil && service.Spec.Annotations.Name == name {
			return nameConflict("service", name)
		}
	}
	return nil
}

// ensureConfigNameFree is ensureServiceNameFree for configs.
func (c *Client) ensureConfigNameFree(ctx context.Context, name string) error {
	resp, err := c.control.ListConfigs(ctx, &api.ListConfigsRequest{})
	if err != nil {
		return err
	}
	if resp == nil {
		return nil
	}
	for _, config := range resp.Configs {
		if config != nil && config.Spec.Annotations.Name == name {
			return nameConflict("config", name)
		}
	}
	return nil
}

// ensureSecretNameFree is ensureServiceNameFree for secrets.
func (c *Client) ensureSecretNameFree(ctx context.Context, name string) error {
	resp, err := c.control.ListSecrets(ctx, &api.ListSecretsRequest{})
	if err != nil {
		return err
	}
	if resp == nil {
		return nil
	}
	for _, secret := range resp.Secrets {
		if secret != nil && secret.Spec.Annotations.Name == name {
			return nameConflict("secret", name)
		}
	}
	return nil
}

// ensureNetworkNameFree is ensureServiceNameFree for networks.
func (c *Client) ensureNetworkNameFree(ctx context.Context, name string) error {
	resp, err := c.control.ListNetworks(ctx, &api.ListNetworksRequest{})
	if err != nil {
		return err
	}
	if resp == nil {
		return nil
	}
	for _, network := range resp.Networks {
		if network != nil && network.Spec.Annotations.Name == name {
			return nameConflict("network", name)
		}
	}
	return nil
}

// dryRunUnsupportedOnDelete is the client-side mirror of the HTTP layer's
// refusal: a delete has no document to preview, so a dry run would have to do
// the deletion to answer.
func dryRunUnsupportedOnDelete() error {
	return invalidField("dryRun", "is not supported on delete")
}

// dryRunUnsupportedOnRollback is the client-side mirror of the HTTP layer's 501:
// the previous spec is not the document a rollback would write.
func dryRunUnsupportedOnRollback() error {
	return output.NewError(output.CodeNotImplemented,
		"dryRun is not supported on rollback").
		WithDetails(map[string]any{"capability": "dryRun"})
}

// synthesizedService strips the identity of a create preview: the object was
// never persisted, so it has no id, revision, or timestamps to report.
func synthesizedService(service v1.Service) v1.Service {
	service.ID = ""
	service.Revision = ""
	service.CreatedAt = ""
	service.UpdatedAt = ""
	return service
}

// synthesizedConfig is synthesizedService for configs.
func synthesizedConfig(config v1.Config) v1.Config {
	config.ID = ""
	config.Revision = ""
	config.CreatedAt = ""
	config.UpdatedAt = ""
	return config
}

// synthesizedSecret is synthesizedService for secrets.
func synthesizedSecret(secret v1.Secret) v1.Secret {
	secret.ID = ""
	secret.Revision = ""
	secret.CreatedAt = ""
	secret.UpdatedAt = ""
	return secret
}

// synthesizedNetwork is synthesizedService for networks. A network has no
// updatedAt.
func synthesizedNetwork(network v1.Network) v1.Network {
	network.ID = ""
	network.Revision = ""
	network.CreatedAt = ""
	return network
}
