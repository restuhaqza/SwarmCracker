// This file owns the phase-2 writes for configs, secrets, networks and nodes.
//
// It is the same pattern as write.go: resolve the reference, evaluate the
// precondition against Meta.Version.Index, deep-copy the current spec, write only
// the fields the request names, and commit with the expected version. The HTTP
// layer owns transport concerns only (media type, body cap, header presence), so
// there is no second SwarmKit mapping anywhere.
package client

import (
	"context"
	"encoding/base64"
	"net"
	"strings"
	"unicode/utf8"

	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// defaultNetworkDriver is the driver a network gets when the request omits one.
const defaultNetworkDriver = "overlay"

// CreateConfig creates a config from the wire DTO and returns the created
// document (metadata plus, for a privileged reader, the payload).
func (c *Client) CreateConfig(ctx context.Context, write v1.ConfigWrite, opts v1.WriteOptions) (v1.Config, error) {
	if c.remote != nil {
		return c.remote.createConfig(ctx, write, opts)
	}
	if err := checkCreateOnly(opts); err != nil {
		return v1.Config{}, err
	}

	spec, err := configSpec(write)
	if err != nil {
		return v1.Config{}, err
	}

	if opts.DryRun {
		if err := c.ensureConfigNameFree(ctx, spec.Annotations.Name); err != nil {
			return v1.Config{}, err
		}
		return synthesizedConfig(configFromSwarmkit(&api.Config{Spec: *spec})), nil
	}

	resp, err := c.control.CreateConfig(ctx, &api.CreateConfigRequest{Spec: spec})
	if err != nil {
		return v1.Config{}, err
	}
	if resp == nil || resp.Config == nil {
		return v1.Config{}, output.NewError(output.CodeInternal,
			"create config: the manager returned no config")
	}
	return configFromSwarmkit(resp.Config), nil
}

// UpdateConfig applies a labels-only merge patch. The payload, the name and
// every read-envelope field are immutable: SwarmKit's UpdateConfig refuses them,
// and accepting them here would make a document round-trip silently change one.
func (c *Client) UpdateConfig(ctx context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Config, error) {
	if c.remote != nil {
		return c.remote.updateConfig(ctx, ref, patch, opts)
	}
	if err := requireIfMatch(opts.IfMatch); err != nil {
		return v1.Config{}, err
	}

	id, current, err := c.configForWrite(ctx, ref, opts.IfMatch)
	if err != nil {
		return v1.Config{}, err
	}
	if err := checkIfMatch(opts.IfMatch, revisionOfConfig(current)); err != nil {
		return v1.Config{}, err
	}

	decoded, err := decodeMergePatch(patch)
	if err != nil {
		return v1.Config{}, err
	}
	if len(decoded) == 0 {
		return configFromSwarmkit(current), nil
	}

	spec := current.Spec.Copy()
	for key, value := range decoded {
		switch key {
		case "labels":
			if err := applyAnnotationsLabelsPatch(&spec.Annotations, "labels", value); err != nil {
				return v1.Config{}, err
			}
		case "data", "name", "id", "revision", "createdAt", "updatedAt", "schemaVersion", "kind":
			return v1.Config{}, invalidField(key, "is immutable")
		default:
			return v1.Config{}, unknownPatchField(key)
		}
	}

	if opts.DryRun {
		preview := current.Copy()
		preview.Spec = *spec
		return configFromSwarmkit(preview), nil
	}

	resp, err := c.control.UpdateConfig(ctx, &api.UpdateConfigRequest{
		ConfigID:      id,
		ConfigVersion: &api.Version{Index: current.Meta.Version.Index},
		Spec:          spec,
	})
	if err != nil {
		return v1.Config{}, err
	}
	if resp == nil || resp.Config == nil {
		return v1.Config{}, output.NewError(output.CodeInternal,
			"update config: the manager returned no config")
	}
	return configFromSwarmkit(resp.Config), nil
}

// DeleteConfig removes a config and returns the document as it was before the
// removal. A miss with IgnoreMissing is (zero, nil); preconditions are evaluated
// before IgnoreMissing.
//
// The previous document is a local-client convenience: a remote client returns
// the zero document, because the wire DELETE carries no body and must work for a
// token that holds configs:write without configs:read.
func (c *Client) DeleteConfig(ctx context.Context, ref string, opts v1.WriteOptions) (v1.Config, error) {
	if c.remote != nil {
		return c.remote.deleteConfig(ctx, ref, opts)
	}

	id, current, err := c.getConfigObject(ctx, ref)
	if err != nil {
		if isNotFound(err) {
			if opts.IfMatch != "" {
				return v1.Config{}, output.PreconditionFailed(tagValue(opts.IfMatch), "")
			}
			if opts.IgnoreMissing {
				return v1.Config{}, nil
			}
		}
		return v1.Config{}, err
	}

	if opts.IfMatch != "" {
		if err := checkIfMatch(opts.IfMatch, revisionOfConfig(current)); err != nil {
			return v1.Config{}, err
		}
	}

	if _, err := c.control.RemoveConfig(ctx, &api.RemoveConfigRequest{ConfigID: id}); err != nil {
		return v1.Config{}, err
	}
	return configFromSwarmkit(current), nil
}

// CreateSecret creates a secret. Exactly one of Data (base64) or Driver must be
// present, because a secret with both would have two sources of truth.
func (c *Client) CreateSecret(ctx context.Context, write v1.SecretWrite, opts v1.WriteOptions) (v1.Secret, error) {
	if c.remote != nil {
		return c.remote.createSecret(ctx, write, opts)
	}
	if err := checkCreateOnly(opts); err != nil {
		return v1.Secret{}, err
	}

	spec, err := secretSpec(write)
	if err != nil {
		return v1.Secret{}, err
	}

	if opts.DryRun {
		if err := c.ensureSecretNameFree(ctx, spec.Annotations.Name); err != nil {
			return v1.Secret{}, err
		}
		return synthesizedSecret(secretFromSwarmkit(&api.Secret{Spec: *spec})), nil
	}

	resp, err := c.control.CreateSecret(ctx, &api.CreateSecretRequest{Spec: spec})
	if err != nil {
		return v1.Secret{}, err
	}
	if resp == nil || resp.Secret == nil {
		return v1.Secret{}, output.NewError(output.CodeInternal,
			"create secret: the manager returned no secret")
	}
	return secretFromSwarmkit(resp.Secret), nil
}

// UpdateSecret applies a labels-only merge patch. The payload and the driver are
// immutable; rotation is create-new and repoint.
func (c *Client) UpdateSecret(ctx context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Secret, error) {
	if c.remote != nil {
		return c.remote.updateSecret(ctx, ref, patch, opts)
	}
	if err := requireIfMatch(opts.IfMatch); err != nil {
		return v1.Secret{}, err
	}

	id, current, err := c.secretForWrite(ctx, ref, opts.IfMatch)
	if err != nil {
		return v1.Secret{}, err
	}
	if err := checkIfMatch(opts.IfMatch, revisionOfSecret(current)); err != nil {
		return v1.Secret{}, err
	}

	decoded, err := decodeMergePatch(patch)
	if err != nil {
		return v1.Secret{}, err
	}
	if len(decoded) == 0 {
		return secretFromSwarmkit(current), nil
	}

	spec := current.Spec.Copy()
	for key, value := range decoded {
		switch key {
		case "labels":
			if err := applyAnnotationsLabelsPatch(&spec.Annotations, "labels", value); err != nil {
				return v1.Secret{}, err
			}
		case "data", "driver", "name", "id", "revision", "createdAt", "updatedAt",
			"schemaVersion", "kind":
			return v1.Secret{}, invalidField(key, "is immutable")
		default:
			return v1.Secret{}, unknownPatchField(key)
		}
	}

	if opts.DryRun {
		preview := current.Copy()
		preview.Spec = *spec
		return secretFromSwarmkit(preview), nil
	}

	resp, err := c.control.UpdateSecret(ctx, &api.UpdateSecretRequest{
		SecretID:      id,
		SecretVersion: &api.Version{Index: current.Meta.Version.Index},
		Spec:          spec,
	})
	if err != nil {
		return v1.Secret{}, err
	}
	if resp == nil || resp.Secret == nil {
		return v1.Secret{}, output.NewError(output.CodeInternal,
			"update secret: the manager returned no secret")
	}
	return secretFromSwarmkit(resp.Secret), nil
}

// DeleteSecret removes a secret and returns the document as it was before the
// removal. Like DeleteConfig, a remote client returns the zero document so a
// write-only token never needs read scope.
func (c *Client) DeleteSecret(ctx context.Context, ref string, opts v1.WriteOptions) (v1.Secret, error) {
	if c.remote != nil {
		return c.remote.deleteSecret(ctx, ref, opts)
	}

	id, current, err := c.getSecretObject(ctx, ref)
	if err != nil {
		if isNotFound(err) {
			if opts.IfMatch != "" {
				return v1.Secret{}, output.PreconditionFailed(tagValue(opts.IfMatch), "")
			}
			if opts.IgnoreMissing {
				return v1.Secret{}, nil
			}
		}
		return v1.Secret{}, err
	}

	if opts.IfMatch != "" {
		if err := checkIfMatch(opts.IfMatch, revisionOfSecret(current)); err != nil {
			return v1.Secret{}, err
		}
	}

	if _, err := c.control.RemoveSecret(ctx, &api.RemoveSecretRequest{SecretID: id}); err != nil {
		return v1.Secret{}, err
	}
	return secretFromSwarmkit(current), nil
}

// CreateNetwork creates a network. Driver defaults to overlay; there is no
// update, so driver, IPAM and labels are fixed at create.
func (c *Client) CreateNetwork(ctx context.Context, write v1.NetworkWrite, opts v1.WriteOptions) (v1.Network, error) {
	if c.remote != nil {
		return c.remote.createNetwork(ctx, write, opts)
	}
	if err := checkCreateOnly(opts); err != nil {
		return v1.Network{}, err
	}

	spec, err := networkSpec(write)
	if err != nil {
		return v1.Network{}, err
	}

	if opts.DryRun {
		if err := c.ensureNetworkNameFree(ctx, spec.Annotations.Name); err != nil {
			return v1.Network{}, err
		}
		return synthesizedNetwork(networkFromSwarmkit(&api.Network{Spec: *spec})), nil
	}

	resp, err := c.control.CreateNetwork(ctx, &api.CreateNetworkRequest{Spec: spec})
	if err != nil {
		return v1.Network{}, err
	}
	if resp == nil || resp.Network == nil {
		return v1.Network{}, output.NewError(output.CodeInternal,
			"create network: the manager returned no network")
	}
	return networkFromSwarmkit(resp.Network), nil
}

// DeleteNetwork removes a network and returns the document as it was before the
// removal. Like DeleteConfig, a remote client returns the zero document so a
// write-only token never needs read scope.
func (c *Client) DeleteNetwork(ctx context.Context, ref string, opts v1.WriteOptions) (v1.Network, error) {
	if c.remote != nil {
		return c.remote.deleteNetwork(ctx, ref, opts)
	}

	id, current, err := c.getNetworkObject(ctx, ref)
	if err != nil {
		if isNotFound(err) {
			if opts.IfMatch != "" {
				return v1.Network{}, output.PreconditionFailed(tagValue(opts.IfMatch), "")
			}
			if opts.IgnoreMissing {
				return v1.Network{}, nil
			}
		}
		return v1.Network{}, err
	}

	if opts.IfMatch != "" {
		if err := checkIfMatch(opts.IfMatch, revisionOfNetwork(current)); err != nil {
			return v1.Network{}, err
		}
	}

	if _, err := c.control.RemoveNetwork(ctx, &api.RemoveNetworkRequest{NetworkID: id}); err != nil {
		return v1.Network{}, err
	}
	return networkFromSwarmkit(current), nil
}

// UpdateNode patches a node's availability and labels. Role and membership are
// immutable here: changing them is a cluster-reshaping operation, not a deploy.
//
// SwarmKit's UpdateNode replaces the whole NodeSpec, so the request carries a
// deep copy of the current spec with only the named fields written.
func (c *Client) UpdateNode(ctx context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Node, error) {
	if c.remote != nil {
		return c.remote.updateNode(ctx, ref, patch, opts)
	}
	if err := requireIfMatch(opts.IfMatch); err != nil {
		return v1.Node{}, err
	}

	id, current, err := c.nodeForWrite(ctx, ref, opts.IfMatch)
	if err != nil {
		return v1.Node{}, err
	}
	if err := checkIfMatch(opts.IfMatch, revisionOfNode(current)); err != nil {
		return v1.Node{}, err
	}

	decoded, err := decodeMergePatch(patch)
	if err != nil {
		return v1.Node{}, err
	}
	if len(decoded) == 0 {
		return nodeFromSwarmkit(current), nil
	}

	spec := current.Spec.Copy()
	for key, value := range decoded {
		switch key {
		case "availability":
			availability, err := nodeAvailability(value)
			if err != nil {
				return v1.Node{}, err
			}
			spec.Availability = availability
		case "labels":
			if err := applyAnnotationsLabelsPatch(&spec.Annotations, "labels", value); err != nil {
				return v1.Node{}, err
			}
		case "role", "membership", "id", "hostname", "revision", "createdAt", "updatedAt",
			"state", "address", "resources", "manager", "agentVersion", "schemaVersion", "kind":
			return v1.Node{}, invalidField(key, "is immutable")
		default:
			return v1.Node{}, unknownPatchField(key)
		}
	}

	if opts.DryRun {
		preview := current.Copy()
		preview.Spec = *spec
		return nodeFromSwarmkit(preview), nil
	}

	resp, err := c.control.UpdateNode(ctx, &api.UpdateNodeRequest{
		NodeID:      id,
		NodeVersion: &api.Version{Index: current.Meta.Version.Index},
		Spec:        spec,
	})
	if err != nil {
		return v1.Node{}, err
	}
	if resp == nil || resp.Node == nil {
		return v1.Node{}, output.NewError(output.CodeInternal,
			"update node: the manager returned no node")
	}
	return nodeFromSwarmkit(resp.Node), nil
}

// configSpec maps a ConfigWrite to a SwarmKit spec. The payload is UTF-8 text;
// a binary payload is refused with the contract's binary_not_supported reason.
func configSpec(write v1.ConfigWrite) (*api.ConfigSpec, error) {
	name := strings.TrimSpace(write.Name)
	if name == "" {
		return nil, invalidField("name", "must not be empty")
	}
	if !utf8.ValidString(write.Data) {
		return nil, binaryNotSupported("data")
	}

	spec := &api.ConfigSpec{
		Annotations: api.Annotations{Name: name, Labels: copyMap(write.Labels)},
	}
	if write.Data != "" {
		spec.Data = []byte(write.Data)
	}
	return spec, nil
}

// secretSpec maps a SecretWrite to a SwarmKit spec. Exactly one of data or
// driver must be present and the data must be base64.
func secretSpec(write v1.SecretWrite) (*api.SecretSpec, error) {
	name := strings.TrimSpace(write.Name)
	if name == "" {
		return nil, invalidField("name", "must not be empty")
	}

	hasData := write.Data != ""
	hasDriver := write.Driver != nil
	switch {
	case hasData && hasDriver:
		return nil, invalidField("driver", "must be omitted when data is set")
	case !hasData && !hasDriver:
		return nil, invalidField("data", "or driver is required")
	}

	spec := &api.SecretSpec{
		Annotations: api.Annotations{Name: name, Labels: copyMap(write.Labels)},
	}

	if hasDriver {
		driverName := strings.TrimSpace(write.Driver.Name)
		if driverName == "" {
			return nil, invalidField("driver.name", "must not be empty")
		}
		spec.Driver = &api.Driver{Name: driverName, Options: copyMap(write.Driver.Options)}
		return spec, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(write.Data)
	if err != nil {
		// A caller that omitted the RFC 4648 padding is still unambiguous: the
		// unpadded alphabet decodes the same bytes, so accept it rather than
		// making base64 trivia a 400.
		decoded, err = base64.RawStdEncoding.DecodeString(write.Data)
	}
	if err != nil {
		return nil, invalidField("data", "must be base64-encoded")
	}
	spec.Data = decoded
	return spec, nil
}

// networkSpec maps a NetworkWrite to a SwarmKit spec and validates the IPAM
// pool: a subnet must parse as CIDR and a gateway must sit inside it.
func networkSpec(write v1.NetworkWrite) (*api.NetworkSpec, error) {
	name := strings.TrimSpace(write.Name)
	if name == "" {
		return nil, invalidField("name", "must not be empty")
	}

	driver := strings.TrimSpace(write.Driver)
	if driver == "" {
		driver = defaultNetworkDriver
	}

	spec := &api.NetworkSpec{
		Annotations:  api.Annotations{Name: name, Labels: copyMap(write.Labels)},
		DriverConfig: &api.Driver{Name: driver},
		Attachable:   write.Attachable,
	}

	if write.IPAM == nil {
		return spec, nil
	}

	subnet := strings.TrimSpace(write.IPAM.Subnet)
	gateway := strings.TrimSpace(write.IPAM.Gateway)
	if subnet == "" && gateway == "" {
		return spec, nil
	}
	if subnet == "" {
		return nil, invalidField("ipam.gateway", "requires ipam.subnet")
	}

	address, pool, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, invalidField("ipam.subnet", "must be a CIDR address")
	}
	if !address.Equal(pool.IP) {
		// 10.0.1.7/24 describes the same pool as 10.0.1.0/24, but storing it
		// verbatim would make the document claim a host address where the wire
		// contract shows a pool.
		return nil, invalidField("ipam.subnet", "must be a network address without host bits")
	}
	if gateway != "" {
		gatewayIP := net.ParseIP(gateway)
		if gatewayIP == nil {
			return nil, invalidField("ipam.gateway", "must be an IP address")
		}
		if !pool.Contains(gatewayIP) {
			return nil, invalidField("ipam.gateway", "must be inside ipam.subnet")
		}
		if gatewayIP.Equal(pool.IP) {
			return nil, invalidField("ipam.gateway", "must not be the network address")
		}
		if broadcast := ipv4Broadcast(pool); broadcast != nil && gatewayIP.To4().Equal(broadcast) {
			return nil, invalidField("ipam.gateway", "must not be the broadcast address")
		}
	}

	spec.IPAM = &api.IPAMOptions{
		Configs: []*api.IPAMConfig{{Subnet: subnet, Gateway: gateway}},
	}
	return spec, nil
}

// ipv4Broadcast returns the last address of an IPv4 pool, or nil when the pool
// is not IPv4. IPv6 has no broadcast address, so no such check applies there.
func ipv4Broadcast(pool *net.IPNet) net.IP {
	network := pool.IP.To4()
	if network == nil {
		return nil
	}
	mask := pool.Mask
	if len(mask) == net.IPv6len {
		mask = mask[12:]
	}
	if len(mask) != net.IPv4len {
		return nil
	}

	broadcast := make(net.IP, net.IPv4len)
	for i := range broadcast {
		broadcast[i] = network[i] | ^mask[i]
	}
	return broadcast
}

// nodeAvailability maps the lowercase wire enum onto SwarmKit's. The read
// document emits lowercase, so the write side accepts nothing else.
func nodeAvailability(value any) (api.NodeSpec_Availability, error) {
	raw, ok := value.(string)
	if !ok {
		return api.NodeAvailabilityActive, invalidField("availability",
			`must be "active", "pause" or "drain"`)
	}
	switch raw {
	case "active":
		return api.NodeAvailabilityActive, nil
	case "pause":
		return api.NodeAvailabilityPause, nil
	case "drain":
		return api.NodeAvailabilityDrain, nil
	default:
		return api.NodeAvailabilityActive, invalidField("availability",
			`must be "active", "pause" or "drain"`)
	}
}

// binaryNotSupported is the contract's refusal of a non-UTF-8 config payload.
func binaryNotSupported(path string) error {
	return output.NewErrorf(output.CodeInvalid,
		"%s must be UTF-8 text; binary payloads are not supported in v1", path).
		WithDetails(map[string]any{
			"reason": "binary_not_supported",
			"fields": map[string]any{path: "must be UTF-8 text"},
		})
}
