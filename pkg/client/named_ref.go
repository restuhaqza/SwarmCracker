// This file owns reference resolution for configs, secrets, networks and nodes.
//
// The rule is the contract's (contract §5.8): a {ref} is an exact id, an exact
// name (a hostname for a node), or a unique id prefix of at least four
// characters. Exact id wins over exact name, and the name wins over a prefix, so
// an object literally named "web" resolves even though "web" is also four
// characters of some other id.
//
// Every getter and every write goes through these helpers, so a read by name and
// a write by name cannot disagree about which object they mean.
package client

import (
	"context"
	"strconv"
	"strings"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// namedObject is the minimum a resolver needs from a SwarmKit object: its id and
// the name a caller may use instead (a node's hostname is its name).
type namedObject struct {
	id   string
	name string
}

// matchNamedRef is the pure half of resolution, so the ordering and ambiguity
// rules are testable without a control fake.
func matchNamedRef(kind, ref string, objects []namedObject) (string, error) {
	for _, object := range objects {
		if object.id == ref {
			return object.id, nil
		}
	}
	for _, object := range objects {
		if object.name != "" && object.name == ref {
			return object.id, nil
		}
	}

	if len(ref) < minRefPrefix {
		return "", output.NewErrorf(output.CodeInvalid,
			"%s reference %q is too short: use an exact id or name, or at least %d characters of an id",
			kind, ref, minRefPrefix).
			WithDetails(map[string]any{"fields": map[string]any{"id": ref}})
	}

	matches := make([]namedObject, 0, 2)
	for _, object := range objects {
		if strings.HasPrefix(object.id, ref) {
			matches = append(matches, object)
		}
	}

	switch len(matches) {
	case 0:
		return "", notFoundError(kind, ref)
	case 1:
		return matches[0].id, nil
	default:
		candidates := make([]map[string]string, 0, len(matches))
		for _, match := range matches {
			candidates = append(candidates, map[string]string{
				"id":   match.id,
				"name": match.name,
			})
		}
		return "", output.NewErrorf(output.CodeAmbiguous,
			"%s reference %q matches %d %ss", kind, ref, len(matches), kind).
			WithDetails(map[string]any{"candidates": candidates})
	}
}

// requireRef rejects an empty reference before it reaches SwarmKit, which would
// answer InvalidArgument for every empty id.
func requireRef(kind, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", output.NewErrorf(output.CodeInvalid, "%s reference is required", kind).
			WithDetails(map[string]any{"fields": map[string]any{"id": "must not be empty"}})
	}
	return ref, nil
}

// resolveConfigRef resolves a config reference to its id.
func (c *Client) resolveConfigRef(ctx context.Context, ref string) (string, error) {
	ref, err := requireRef("config", ref)
	if err != nil {
		return "", err
	}

	resp, err := c.control.ListConfigs(ctx, &api.ListConfigsRequest{})
	if err != nil {
		return "", err
	}
	objects := make([]namedObject, 0)
	if resp != nil {
		for _, config := range resp.Configs {
			if config != nil {
				objects = append(objects, namedObject{
					id:   config.ID,
					name: config.Spec.Annotations.Name,
				})
			}
		}
	}
	return matchNamedRef("config", ref, objects)
}

// resolveSecretRef resolves a secret reference to its id.
func (c *Client) resolveSecretRef(ctx context.Context, ref string) (string, error) {
	ref, err := requireRef("secret", ref)
	if err != nil {
		return "", err
	}

	resp, err := c.control.ListSecrets(ctx, &api.ListSecretsRequest{})
	if err != nil {
		return "", err
	}
	objects := make([]namedObject, 0)
	if resp != nil {
		for _, secret := range resp.Secrets {
			if secret != nil {
				objects = append(objects, namedObject{
					id:   secret.ID,
					name: secret.Spec.Annotations.Name,
				})
			}
		}
	}
	return matchNamedRef("secret", ref, objects)
}

// resolveNetworkRef resolves a network reference to its id.
func (c *Client) resolveNetworkRef(ctx context.Context, ref string) (string, error) {
	ref, err := requireRef("network", ref)
	if err != nil {
		return "", err
	}

	resp, err := c.control.ListNetworks(ctx, &api.ListNetworksRequest{})
	if err != nil {
		return "", err
	}
	objects := make([]namedObject, 0)
	if resp != nil {
		for _, network := range resp.Networks {
			if network != nil {
				objects = append(objects, namedObject{
					id:   network.ID,
					name: network.Spec.Annotations.Name,
				})
			}
		}
	}
	return matchNamedRef("network", ref, objects)
}

// resolveNodeRef resolves a node reference to its id. A node has no user-set
// name, so its hostname is the exact-name form.
func (c *Client) resolveNodeRef(ctx context.Context, ref string) (string, error) {
	ref, err := requireRef("node", ref)
	if err != nil {
		return "", err
	}

	resp, err := c.control.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		return "", err
	}
	objects := make([]namedObject, 0)
	if resp != nil {
		for _, node := range resp.Nodes {
			if node != nil {
				objects = append(objects, namedObject{
					id:   node.ID,
					name: nodeHostname(node.Description),
				})
			}
		}
	}
	return matchNamedRef("node", ref, objects)
}

// getConfigObject resolves ref and returns the raw SwarmKit object. An exact id
// costs one GetConfig; a name or prefix falls through to the list-based
// resolver, exactly like getServiceObject.
func (c *Client) getConfigObject(ctx context.Context, ref string) (string, *api.Config, error) {
	ref, err := requireRef("config", ref)
	if err != nil {
		return "", nil, err
	}

	resp, err := c.control.GetConfig(ctx, &api.GetConfigRequest{ConfigID: ref})
	if err == nil && resp != nil && resp.Config != nil {
		return resp.Config.ID, resp.Config, nil
	}
	if err != nil && !isLookupMiss(err) {
		return "", nil, err
	}

	id, err := c.resolveConfigRef(ctx, ref)
	if err != nil {
		return "", nil, err
	}
	return c.fetchConfig(ctx, id, ref)
}

// getSecretObject is getConfigObject's secret twin.
func (c *Client) getSecretObject(ctx context.Context, ref string) (string, *api.Secret, error) {
	ref, err := requireRef("secret", ref)
	if err != nil {
		return "", nil, err
	}

	resp, err := c.control.GetSecret(ctx, &api.GetSecretRequest{SecretID: ref})
	if err == nil && resp != nil && resp.Secret != nil {
		return resp.Secret.ID, resp.Secret, nil
	}
	if err != nil && !isLookupMiss(err) {
		return "", nil, err
	}

	id, err := c.resolveSecretRef(ctx, ref)
	if err != nil {
		return "", nil, err
	}
	return c.fetchSecret(ctx, id, ref)
}

// getNetworkObject is getConfigObject's network twin. Unlike ListNetworks, the
// resolver does not hide built-in networks: a direct read of ingress is allowed.
func (c *Client) getNetworkObject(ctx context.Context, ref string) (string, *api.Network, error) {
	ref, err := requireRef("network", ref)
	if err != nil {
		return "", nil, err
	}

	resp, err := c.control.GetNetwork(ctx, &api.GetNetworkRequest{NetworkID: ref})
	if err == nil && resp != nil && resp.Network != nil {
		return resp.Network.ID, resp.Network, nil
	}
	if err != nil && !isLookupMiss(err) {
		return "", nil, err
	}

	id, err := c.resolveNetworkRef(ctx, ref)
	if err != nil {
		return "", nil, err
	}
	return c.fetchNetwork(ctx, id, ref)
}

// getNodeObject is getConfigObject's node twin.
func (c *Client) getNodeObject(ctx context.Context, ref string) (string, *api.Node, error) {
	ref, err := requireRef("node", ref)
	if err != nil {
		return "", nil, err
	}

	resp, err := c.control.GetNode(ctx, &api.GetNodeRequest{NodeID: ref})
	if err == nil && resp != nil && resp.Node != nil {
		return resp.Node.ID, resp.Node, nil
	}
	if err != nil && !isLookupMiss(err) {
		return "", nil, err
	}

	id, err := c.resolveNodeRef(ctx, ref)
	if err != nil {
		return "", nil, err
	}
	return c.fetchNode(ctx, id, ref)
}

// isLookupMiss reports whether a direct Get failed because the reference was not
// an id. SwarmKit answers NotFound for an unknown id and InvalidArgument for an
// empty one; both mean "try the resolver".
func isLookupMiss(err error) bool {
	code := status.Code(err)
	return code == codes.NotFound || code == codes.InvalidArgument
}

func (c *Client) fetchConfig(ctx context.Context, id, ref string) (string, *api.Config, error) {
	resp, err := c.control.GetConfig(ctx, &api.GetConfigRequest{ConfigID: id})
	if err != nil {
		return "", nil, err
	}
	if resp == nil || resp.Config == nil {
		return "", nil, notFoundError("config", ref)
	}
	return id, resp.Config, nil
}

func (c *Client) fetchSecret(ctx context.Context, id, ref string) (string, *api.Secret, error) {
	resp, err := c.control.GetSecret(ctx, &api.GetSecretRequest{SecretID: id})
	if err != nil {
		return "", nil, err
	}
	if resp == nil || resp.Secret == nil {
		return "", nil, notFoundError("secret", ref)
	}
	return id, resp.Secret, nil
}

func (c *Client) fetchNetwork(ctx context.Context, id, ref string) (string, *api.Network, error) {
	resp, err := c.control.GetNetwork(ctx, &api.GetNetworkRequest{NetworkID: id})
	if err != nil {
		return "", nil, err
	}
	if resp == nil || resp.Network == nil {
		return "", nil, notFoundError("network", ref)
	}
	return id, resp.Network, nil
}

func (c *Client) fetchNode(ctx context.Context, id, ref string) (string, *api.Node, error) {
	resp, err := c.control.GetNode(ctx, &api.GetNodeRequest{NodeID: id})
	if err != nil {
		return "", nil, err
	}
	if resp == nil || resp.Node == nil {
		return "", nil, notFoundError("node", ref)
	}
	return id, resp.Node, nil
}

// configForWrite is getConfigObject with the precondition rule applied: a
// missing resource with If-Match is 412, not 404.
func (c *Client) configForWrite(ctx context.Context, ref, ifMatch string) (string, *api.Config, error) {
	id, config, err := c.getConfigObject(ctx, ref)
	if err != nil {
		if ifMatch != "" && isNotFound(err) {
			return "", nil, output.PreconditionFailed(tagValue(ifMatch), "")
		}
		return "", nil, err
	}
	return id, config, nil
}

// secretForWrite is configForWrite's secret twin.
func (c *Client) secretForWrite(ctx context.Context, ref, ifMatch string) (string, *api.Secret, error) {
	id, secret, err := c.getSecretObject(ctx, ref)
	if err != nil {
		if ifMatch != "" && isNotFound(err) {
			return "", nil, output.PreconditionFailed(tagValue(ifMatch), "")
		}
		return "", nil, err
	}
	return id, secret, nil
}

// networkForWrite is configForWrite's network twin.
func (c *Client) networkForWrite(ctx context.Context, ref, ifMatch string) (string, *api.Network, error) {
	id, network, err := c.getNetworkObject(ctx, ref)
	if err != nil {
		if ifMatch != "" && isNotFound(err) {
			return "", nil, output.PreconditionFailed(tagValue(ifMatch), "")
		}
		return "", nil, err
	}
	return id, network, nil
}

// nodeForWrite is configForWrite's node twin.
func (c *Client) nodeForWrite(ctx context.Context, ref, ifMatch string) (string, *api.Node, error) {
	id, node, err := c.getNodeObject(ctx, ref)
	if err != nil {
		if ifMatch != "" && isNotFound(err) {
			return "", nil, output.PreconditionFailed(tagValue(ifMatch), "")
		}
		return "", nil, err
	}
	return id, node, nil
}

// revisionOfConfig renders Meta.Version.Index as the opaque revision.
func revisionOfConfig(config *api.Config) string {
	if config == nil {
		return ""
	}
	return strconv.FormatUint(config.Meta.Version.Index, 10)
}

func revisionOfSecret(secret *api.Secret) string {
	if secret == nil {
		return ""
	}
	return strconv.FormatUint(secret.Meta.Version.Index, 10)
}

func revisionOfNetwork(network *api.Network) string {
	if network == nil {
		return ""
	}
	return strconv.FormatUint(network.Meta.Version.Index, 10)
}

func revisionOfNode(node *api.Node) string {
	if node == nil {
		return ""
	}
	return strconv.FormatUint(node.Meta.Version.Index, 10)
}
