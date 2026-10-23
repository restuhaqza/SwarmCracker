// This file owns the write side of pkg/client: reference resolution, the DTO →
// SwarmKit mapping and the revision preconditions. The merge-patch overlay lives
// in merge_patch.go so the mapping and the patch rules stay readable apart.
//
// Every write is resolved and preconditioned here, not in pkg/apihttp: only this
// package can see Meta.Version.Index, and only this package may import the
// SwarmKit control surface.
package client

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// minRefPrefix is the shortest ID prefix a caller may use in a {ref}. A
	// shorter non-exact value is invalid rather than a wildcard search: two hex
	// characters match too many objects to be a useful identifier.
	minRefPrefix = 4

	// nanoCPUsPerVCPU converts the contract's vcpus to SwarmKit's NanoCPUs.
	nanoCPUsPerVCPU = 1_000_000_000
)

// CreateService creates a service from the wire DTO. It is the local SwarmKit
// path; a remote client sends the equivalent HTTP request.
func (c *Client) CreateService(ctx context.Context, write v1.ServiceWrite, opts v1.WriteOptions) (v1.Service, error) {
	if c.remote != nil {
		return c.remote.createService(ctx, write, opts)
	}
	if err := checkCreateOnly(opts); err != nil {
		return v1.Service{}, err
	}

	spec, err := c.buildServiceSpec(ctx, write, opts.AllowBindMounts)
	if err != nil {
		return v1.Service{}, err
	}

	resp, err := c.control.CreateService(ctx, &api.CreateServiceRequest{Spec: spec})
	if err != nil {
		return v1.Service{}, err
	}
	if resp == nil || resp.Service == nil {
		return v1.Service{}, output.NewError(output.CodeInternal,
			"create service: the manager returned no service")
	}

	// The response is the just-created object and no task exists yet, so zero
	// counts are the honest status. Callers that need convergence use a read or
	// the phase-2 wait option.
	return serviceFromSwarmkit(resp.Service, taskCounts{}), nil
}

// UpdateService applies an RFC 7386 merge patch over the current service spec.
//
// The patch is applied to a deep copy of the current SwarmKit spec and only the
// fields the patch names are touched, so every field the DTO does not model
// survives the update byte for byte.
func (c *Client) UpdateService(ctx context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Service, error) {
	if c.remote != nil {
		return c.remote.updateService(ctx, ref, patch, opts)
	}
	if err := requireIfMatch(opts.IfMatch); err != nil {
		return v1.Service{}, err
	}

	id, current, err := c.serviceForWrite(ctx, ref, opts.IfMatch)
	if err != nil {
		return v1.Service{}, err
	}
	if err := checkIfMatch(opts.IfMatch, revisionOf(current)); err != nil {
		return v1.Service{}, err
	}

	decoded, err := decodeMergePatch(patch)
	if err != nil {
		return v1.Service{}, err
	}
	if len(decoded) == 0 {
		// An empty merge patch changes nothing. Answering the current document
		// without an UpdateService call keeps a reconciler that sends {} every
		// loop from churning revisions and PreviousSpec.
		return serviceFromSwarmkit(current, taskCounts{}), nil
	}
	spec := current.Spec.Copy()
	if err := c.applyServicePatch(ctx, spec, decoded, opts.AllowBindMounts); err != nil {
		return v1.Service{}, err
	}

	return c.commitServiceUpdate(ctx, id, spec, current)
}

// DeleteService removes a service and returns the document as it was before the
// removal, so the HTTP layer can audit what was deleted.
//
// A miss with IgnoreMissing is (zero, nil): the caller asked for the end state,
// not for the object. Any other miss keeps its error.
func (c *Client) DeleteService(ctx context.Context, ref string, opts v1.WriteOptions) (v1.Service, error) {
	if c.remote != nil {
		return c.remote.deleteService(ctx, ref, opts)
	}

	id, current, err := c.getServiceObject(ctx, ref)
	if err != nil {
		if isNotFound(err) {
			if opts.IfMatch != "" {
				return v1.Service{}, output.PreconditionFailed(tagValue(opts.IfMatch), "")
			}
			if opts.IgnoreMissing {
				return v1.Service{}, nil
			}
		}
		return v1.Service{}, err
	}

	if opts.IfMatch != "" {
		if err := checkIfMatch(opts.IfMatch, revisionOf(current)); err != nil {
			return v1.Service{}, err
		}
	}

	// The delete is deliberately not atomic with the check above: SwarmKit's
	// RemoveServiceRequest carries no version, so an update can land between the
	// If-Match check and the removal. The contract documents that the delete
	// wins; a caller that needs strict ordering patches first.
	if _, err := c.control.RemoveService(ctx, &api.RemoveServiceRequest{ServiceID: id}); err != nil {
		return v1.Service{}, err
	}

	return serviceFromSwarmkit(current, taskCounts{}), nil
}

// RollbackService asks SwarmKit to restore the service's previous spec. It is an
// update, so it requires If-Match like any other PATCH.
func (c *Client) RollbackService(ctx context.Context, ref string, opts v1.WriteOptions) (v1.Service, error) {
	if c.remote != nil {
		return c.remote.rollbackService(ctx, ref, opts)
	}
	if err := requireIfMatch(opts.IfMatch); err != nil {
		return v1.Service{}, err
	}

	id, current, err := c.serviceForWrite(ctx, ref, opts.IfMatch)
	if err != nil {
		return v1.Service{}, err
	}
	if err := checkIfMatch(opts.IfMatch, revisionOf(current)); err != nil {
		return v1.Service{}, err
	}

	resp, err := c.control.UpdateService(ctx, &api.UpdateServiceRequest{
		ServiceID:      id,
		ServiceVersion: &api.Version{Index: current.Meta.Version.Index},
		// A rollback ignores Spec: the manager restores PreviousSpec.
		Rollback: api.UpdateServiceRequest_PREVIOUS,
	})
	if err != nil {
		return v1.Service{}, err
	}
	if resp == nil || resp.Service == nil {
		return v1.Service{}, output.NewError(output.CodeInternal,
			"rollback service: the manager returned no service")
	}
	return serviceFromSwarmkit(resp.Service, taskCounts{}), nil
}

// resolveServiceRef resolves a {ref} to a service ID: exact ID, exact name, then
// a unique ID prefix of at least minRefPrefix characters.
func (c *Client) resolveServiceRef(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", output.NewError(output.CodeInvalid, "service reference is required").
			WithDetails(map[string]any{"fields": map[string]any{"id": "must not be empty"}})
	}

	resp, err := c.control.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		return "", err
	}
	if resp == nil {
		resp = &api.ListServicesResponse{}
	}
	return matchServiceRef(ref, resp.Services)
}

// matchServiceRef is the pure half of resolution, so the ordering rule is
// testable without a control fake.
func matchServiceRef(ref string, services []*api.Service) (string, error) {
	for _, svc := range services {
		if svc != nil && svc.ID == ref {
			return svc.ID, nil
		}
	}
	// The name beats a prefix: a service literally named "web" must resolve even
	// though "web" is also four characters of some other ID.
	for _, svc := range services {
		if svc != nil && svc.Spec.Annotations.Name == ref {
			return svc.ID, nil
		}
	}

	if len(ref) < minRefPrefix {
		return "", output.NewErrorf(output.CodeInvalid,
			"service reference %q is too short: use an exact id or name, or at least %d characters of an id",
			ref, minRefPrefix).
			WithDetails(map[string]any{"fields": map[string]any{"id": ref}})
	}

	var matches []*api.Service
	for _, svc := range services {
		if svc != nil && strings.HasPrefix(svc.ID, ref) {
			matches = append(matches, svc)
		}
	}

	switch len(matches) {
	case 0:
		return "", notFoundError("service", ref)
	case 1:
		return matches[0].ID, nil
	default:
		candidates := make([]map[string]string, 0, len(matches))
		for _, svc := range matches {
			candidates = append(candidates, map[string]string{
				"id":   svc.ID,
				"name": svc.Spec.Annotations.Name,
			})
		}
		return "", output.NewErrorf(output.CodeAmbiguous,
			"service reference %q matches %d services", ref, len(matches)).
			WithDetails(map[string]any{"candidates": candidates})
	}
}

// getServiceObject resolves ref and returns the raw SwarmKit object, which the
// write paths need for its spec and version.
//
// An exact ID resolves with one GetService call: SwarmKit answers NotFound for a
// name or a prefix, which then falls through to the list-based resolver. That
// keeps the common read (and every write by ID) at one RPC.
func (c *Client) getServiceObject(ctx context.Context, ref string) (string, *api.Service, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil, output.NewError(output.CodeInvalid, "service reference is required").
			WithDetails(map[string]any{"fields": map[string]any{"id": "must not be empty"}})
	}

	resp, err := c.control.GetService(ctx, &api.GetServiceRequest{ServiceID: ref})
	if err == nil && resp != nil && resp.Service != nil {
		return resp.Service.ID, resp.Service, nil
	}
	if err != nil && status.Code(err) != codes.NotFound && status.Code(err) != codes.InvalidArgument {
		return "", nil, err
	}

	id, err := c.resolveServiceRef(ctx, ref)
	if err != nil {
		return "", nil, err
	}

	resp, err = c.control.GetService(ctx, &api.GetServiceRequest{ServiceID: id})
	if err != nil {
		return "", nil, err
	}
	if resp == nil || resp.Service == nil {
		return "", nil, notFoundError("service", ref)
	}
	return id, resp.Service, nil
}

// serviceForWrite is getServiceObject with the precondition rule applied: a
// missing resource with If-Match is 412, not 404, because the caller asserted a
// revision of something that is gone.
func (c *Client) serviceForWrite(ctx context.Context, ref, ifMatch string) (string, *api.Service, error) {
	id, svc, err := c.getServiceObject(ctx, ref)
	if err != nil {
		if ifMatch != "" && isNotFound(err) {
			return "", nil, output.PreconditionFailed(tagValue(ifMatch), "")
		}
		return "", nil, err
	}
	return id, svc, nil
}

// commitServiceUpdate sends the merged spec with the expected version, so the
// manager refuses the write if another update landed after our read.
func (c *Client) commitServiceUpdate(ctx context.Context, id string, spec *api.ServiceSpec, current *api.Service) (v1.Service, error) {
	resp, err := c.control.UpdateService(ctx, &api.UpdateServiceRequest{
		ServiceID:      id,
		ServiceVersion: &api.Version{Index: current.Meta.Version.Index},
		Spec:           spec,
	})
	if err != nil {
		return v1.Service{}, err
	}
	if resp == nil || resp.Service == nil {
		return v1.Service{}, output.NewError(output.CodeInternal,
			"update service: the manager returned no service")
	}
	return serviceFromSwarmkit(resp.Service, taskCounts{}), nil
}

// checkCreateOnly validates If-None-Match on POST. Only "*" is accepted; the
// header marks the request create-only and never turns POST into an upsert.
func checkCreateOnly(opts v1.WriteOptions) error {
	if opts.IfNoneMatch == "" || opts.IfNoneMatch == "*" {
		return nil
	}
	return output.NewError(output.CodeInvalid,
		`If-None-Match on create must be exactly "*"`).
		WithDetails(map[string]any{"fields": map[string]any{"If-None-Match": opts.IfNoneMatch}})
}

// requireIfMatch rejects a write that carries no precondition. The HTTP layer
// answers 428 from the same details.
func requireIfMatch(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return output.PreconditionRequired()
	}
	return nil
}

// checkIfMatch evaluates an If-Match header against the current revision.
//
// Accepted forms are "*", a quoted strong validator ("17") and a bare revision
// (17). A weak validator (W/"17") never matches: it promises semantic
// equivalence, which is not what an opaque SwarmKit revision means. A list is
// accepted; any strong member that equals the revision satisfies it, and a weak
// member alone does not.
func checkIfMatch(raw, current string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return output.PreconditionRequired()
	}

	weak := false
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return nil
		}
		if strings.HasPrefix(candidate, "W/") || strings.HasPrefix(candidate, "w/") {
			weak = true
			continue
		}
		if tagValue(candidate) == current {
			return nil
		}
	}

	e := output.PreconditionFailed(tagValue(raw), current)
	if weak {
		e.Details["weak"] = true
	}
	return e
}

// tagValue removes an optional weak marker and the surrounding quotes of one
// If-Match element.
func tagValue(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.TrimPrefix(raw, "w/")
	return strings.Trim(raw, `"`)
}

// revisionOf returns Meta.Version.Index as the contract's opaque revision.
func revisionOf(svc *api.Service) string {
	if svc == nil {
		return ""
	}
	return strconv.FormatUint(svc.Meta.Version.Index, 10)
}

// notFoundError is the typed miss every resolver reports. It is a gRPC NotFound
// so the read path keeps the same error a direct GetService produced before
// references were resolvable, and pkg/output maps it to not_found at the edge.
func notFoundError(kind, ref string) error {
	return status.Errorf(codes.NotFound, "%s %q not found", kind, ref)
}

// isNotFound reports whether err is the typed miss, whichever layer produced it.
func isNotFound(err error) bool {
	var typed *output.Error
	if errors.As(err, &typed) {
		return typed.Code == output.CodeNotFound
	}
	return status.Code(err) == codes.NotFound
}

// buildServiceSpec maps the wire DTO to a new SwarmKit spec.
func (c *Client) buildServiceSpec(ctx context.Context, write v1.ServiceWrite, allowBindMounts bool) (*api.ServiceSpec, error) {
	name := strings.TrimSpace(write.Name)
	if name == "" {
		return nil, invalidField("name", "must not be empty")
	}
	if strings.TrimSpace(write.Spec.Task.Image) == "" {
		return nil, invalidField("spec.task.image", "must not be empty")
	}

	mode, err := serviceMode(write.Spec.Mode)
	if err != nil {
		return nil, err
	}
	if err := validateReplicas(mode, write.Spec.Replicas); err != nil {
		return nil, err
	}

	container, err := containerSpec(write.Spec.Task, allowBindMounts)
	if err != nil {
		return nil, err
	}
	resources, err := resourcesSpec(write.Spec.Task.TaskSpec)
	if err != nil {
		return nil, err
	}
	restart, err := restartSpec(write.Spec.Task.RestartPolicy)
	if err != nil {
		return nil, err
	}
	placement, err := placementSpec(write.Spec.Placement)
	if err != nil {
		return nil, err
	}
	ports, err := portSpecs(write.Spec.Task.Ports)
	if err != nil {
		return nil, err
	}
	networks, err := c.networkAttachments(ctx, write.Spec.Task.Networks)
	if err != nil {
		return nil, err
	}
	update, err := updateSpec(write.Spec.Update, "spec.update")
	if err != nil {
		return nil, err
	}
	rollback, err := updateSpec(write.Spec.Rollback, "spec.rollback")
	if err != nil {
		return nil, err
	}

	spec := &api.ServiceSpec{
		Annotations: api.Annotations{Name: name, Labels: copyMap(write.Labels)},
		Task: api.TaskSpec{
			Runtime:   &api.TaskSpec_Container{Container: container},
			Resources: resources,
			Restart:   restart,
			Placement: placement,
			Networks:  networks,
		},
		Update:   update,
		Rollback: rollback,
	}

	if len(ports) > 0 {
		spec.Endpoint = &api.EndpointSpec{Ports: ports}
	}

	switch mode {
	case "replicated":
		replicas := int64(1)
		if write.Spec.Replicas != nil {
			replicas = *write.Spec.Replicas
		}
		spec.Mode = &api.ServiceSpec_Replicated{
			//nolint:gosec // validated non-negative above
			Replicated: &api.ReplicatedService{Replicas: uint64(replicas)},
		}
	case "global":
		spec.Mode = &api.ServiceSpec_Global{Global: &api.GlobalService{}}
	}

	return spec, nil
}

// serviceMode normalizes the mode: absent means replicated, and any other value
// is invalid. Jobs are deliberately not writable in v1.
func serviceMode(raw string) (string, error) {
	switch raw {
	case "":
		return "replicated", nil
	case "replicated", "global":
		return raw, nil
	default:
		return "", invalidField("spec.mode", `must be "replicated" or "global"`)
	}
}

// validateReplicas rejects replicas on a global service and a negative count on
// a replicated one.
func validateReplicas(mode string, replicas *int64) error {
	if mode == "global" {
		if replicas != nil {
			return invalidField("spec.replicas", "must be omitted when mode is global")
		}
		return nil
	}
	if replicas != nil && *replicas < 0 {
		return invalidField("spec.replicas", "must be >= 0")
	}
	return nil
}

// containerSpec maps the writable task fields onto a ContainerSpec.
func containerSpec(task v1.TaskSpecWrite, allowBindMounts bool) (*api.ContainerSpec, error) {
	container := &api.ContainerSpec{
		Image:      strings.TrimSpace(task.Image),
		Command:    append([]string(nil), task.Command...),
		Args:       append([]string(nil), task.Args...),
		User:       task.User,
		Dir:        task.Workdir,
		StopSignal: task.StopSignal,
		ReadOnly:   task.ReadOnlyRootfs,
	}

	env, err := envList(task.Env)
	if err != nil {
		return nil, err
	}
	container.Env = env

	if task.StopGraceMs < 0 {
		return nil, invalidField("spec.task.stopGraceMs", "must be >= 0")
	}
	if task.StopGraceMs > 0 {
		container.StopGracePeriod = durationProto(task.StopGraceMs)
	}

	mounts, err := mountSpecs(task.Mounts, allowBindMounts)
	if err != nil {
		return nil, err
	}
	container.Mounts = mounts

	health, err := healthcheckSpec(task.Healthcheck)
	if err != nil {
		return nil, err
	}
	container.Healthcheck = health

	if task.RegistryAuth != "" {
		container.PullOptions = &api.ContainerSpec_PullOptions{
			RegistryAuth: task.RegistryAuth,
		}
	}

	return container, nil
}

// envList renders the map as sorted K=V entries. SwarmKit stores env as a list;
// sorting keeps the spec stable across Go map iteration and across updates.
func envList(env map[string]string) ([]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		if err := validEnvKey(key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out, nil
}

// validEnvKey rejects a key that cannot round-trip through SwarmKit's K=V list.
// A key containing '=' would read back as a different key, and surrounding
// whitespace is never part of an environment variable name.
func validEnvKey(key string) error {
	switch {
	case strings.TrimSpace(key) == "":
		return invalidField("spec.task.env", "keys must not be empty")
	case key != strings.TrimSpace(key):
		return invalidField("spec.task.env", "keys must not have surrounding whitespace")
	case strings.ContainsRune(key, '='):
		return invalidField("spec.task.env", `keys must not contain "="`)
	default:
		return nil
	}
}

// resourcesSpec maps vcpus and memoryBytes onto Limits. A zero value means "no
// limit" and produces no Resources block at all.
func resourcesSpec(task v1.TaskSpec) (*api.ResourceRequirements, error) {
	if task.VCPUs < 0 {
		return nil, invalidField("spec.task.vcpus", "must be >= 0")
	}
	if task.MemoryBytes < 0 {
		return nil, invalidField("spec.task.memoryBytes", "must be >= 0")
	}
	if task.VCPUs == 0 && task.MemoryBytes == 0 {
		return nil, nil
	}
	return &api.ResourceRequirements{
		Limits: &api.Resources{
			NanoCPUs:    task.VCPUs * nanoCPUsPerVCPU,
			MemoryBytes: task.MemoryBytes,
		},
	}, nil
}

// restartSpec maps the restart policy. An absent policy stays nil, which means
// SwarmKit's default rather than an explicit "none".
func restartSpec(in *v1.RestartPolicy) (*api.RestartPolicy, error) {
	if in == nil {
		return nil, nil
	}

	condition, err := restartCondition(in.Condition, "spec.task.restartPolicy.condition")
	if err != nil {
		return nil, err
	}
	if in.DelayMs < 0 {
		return nil, invalidField("spec.task.restartPolicy.delayMs", "must be >= 0")
	}
	if in.WindowMs < 0 {
		return nil, invalidField("spec.task.restartPolicy.windowMs", "must be >= 0")
	}

	out := &api.RestartPolicy{
		Condition:   condition,
		MaxAttempts: in.MaxAttempts,
	}
	if in.DelayMs > 0 {
		out.Delay = durationProto(in.DelayMs)
	}
	if in.WindowMs > 0 {
		out.Window = durationProto(in.WindowMs)
	}
	return out, nil
}

// placementSpec maps the constraints. An empty placement is nil so the spec says
// nothing rather than carrying an empty object.
func placementSpec(in *v1.Placement) (*api.Placement, error) {
	if in == nil || len(in.Constraints) == 0 {
		return nil, nil
	}
	return &api.Placement{Constraints: append([]string(nil), in.Constraints...)}, nil
}

// portSpecs maps the published ports onto an EndpointSpec's port list.
func portSpecs(in []v1.Port) ([]*api.PortConfig, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]*api.PortConfig, 0, len(in))
	for i, p := range in {
		path := "spec.task.ports[" + strconv.Itoa(i) + "]"
		if p.Target > 65535 {
			return nil, invalidField(path+".target", "must be <= 65535")
		}
		if p.Published > 65535 {
			return nil, invalidField(path+".published", "must be <= 65535")
		}
		protocol, err := portProtocol(p.Protocol, path+".protocol")
		if err != nil {
			return nil, err
		}
		mode, err := publishMode(p.Mode, path+".mode")
		if err != nil {
			return nil, err
		}
		out = append(out, &api.PortConfig{
			TargetPort:    p.Target,
			PublishedPort: p.Published,
			Protocol:      protocol,
			PublishMode:   mode,
		})
	}
	return out, nil
}

// mountSpecs maps the container mounts.
func mountSpecs(in []v1.Mount, allowBindMounts bool) ([]api.Mount, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]api.Mount, 0, len(in))
	for i, m := range in {
		path := "spec.task.mounts[" + strconv.Itoa(i) + "]"
		kind, err := mountType(m.Type, path+".type", allowBindMounts)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(m.Target) == "" {
			return nil, invalidField(path+".target", "must not be empty")
		}
		out = append(out, api.Mount{
			Type:     kind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	return out, nil
}

// healthcheckSpec maps the healthcheck. An empty test list is invalid: SwarmKit
// would read it as "inherit", which is not what the caller wrote.
func healthcheckSpec(in *v1.Healthcheck) (*api.HealthConfig, error) {
	if in == nil {
		return nil, nil
	}
	if len(in.Test) == 0 {
		return nil, invalidField("spec.task.healthcheck.test", "must not be empty")
	}
	if in.IntervalMs < 0 || in.TimeoutMs < 0 || in.StartPeriodMs < 0 {
		return nil, invalidField("spec.task.healthcheck", "durations must be >= 0")
	}
	if in.Retries < 0 || in.Retries > math.MaxInt32 {
		return nil, invalidField("spec.task.healthcheck.retries", "must be between 0 and 2147483647")
	}

	out := &api.HealthConfig{
		Test: append([]string(nil), in.Test...),
		//nolint:gosec // retries is bounded above
		Retries: int32(in.Retries),
	}
	if in.IntervalMs > 0 {
		out.Interval = durationProto(in.IntervalMs)
	}
	if in.TimeoutMs > 0 {
		out.Timeout = durationProto(in.TimeoutMs)
	}
	if in.StartPeriodMs > 0 {
		out.StartPeriod = durationProto(in.StartPeriodMs)
	}
	return out, nil
}

// updateSpec maps an update or rollback policy. An absent block stays nil.
func updateSpec(in *v1.UpdateConfig, path string) (*api.UpdateConfig, error) {
	if in == nil {
		return nil, nil
	}

	failure, err := updateFailureAction(in.FailureAction, path+".failureAction")
	if err != nil {
		return nil, err
	}
	order, err := updateOrder(in.Order, path+".order")
	if err != nil {
		return nil, err
	}
	if in.DelayMs < 0 || in.MonitorMs < 0 {
		return nil, invalidField(path, "durations must be >= 0")
	}
	if in.MaxFailureRatio < 0 || in.MaxFailureRatio > 1 {
		return nil, invalidField(path+".maxFailureRatio", "must be between 0 and 1")
	}

	out := &api.UpdateConfig{
		Parallelism:     in.Parallelism,
		Delay:           time.Duration(in.DelayMs) * time.Millisecond,
		FailureAction:   failure,
		Order:           order,
		MaxFailureRatio: in.MaxFailureRatio,
	}
	if in.MonitorMs > 0 {
		out.Monitor = durationProto(in.MonitorMs)
	}
	return out, nil
}

// networkAttachments resolves each attachment's id-or-name to a network ID and
// builds the SwarmKit attachment list. One ListNetworks call serves them all.
func (c *Client) networkAttachments(ctx context.Context, in []v1.NetworkAttachment) ([]*api.NetworkAttachmentConfig, error) {
	if len(in) == 0 {
		return nil, nil
	}

	resp, err := c.control.ListNetworks(ctx, &api.ListNetworksRequest{})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		resp = &api.ListNetworksResponse{}
	}

	byID := make(map[string]*api.Network, len(resp.Networks))
	byName := make(map[string]*api.Network, len(resp.Networks))
	for _, network := range resp.Networks {
		if network == nil {
			continue
		}
		byID[network.ID] = network
		if network.Spec.Annotations.Name != "" {
			byName[network.Spec.Annotations.Name] = network
		}
	}

	out := make([]*api.NetworkAttachmentConfig, 0, len(in))
	for i, attachment := range in {
		path := "spec.task.networks[" + strconv.Itoa(i) + "]"
		ref := strings.TrimSpace(attachment.ID)
		if ref == "" {
			ref = strings.TrimSpace(attachment.Name)
		}
		if ref == "" {
			return nil, invalidField(path+".id", "must name a network id or name")
		}

		network := byID[ref]
		if network == nil {
			network = byName[ref]
		}
		if network == nil {
			return nil, invalidField(path+".id",
				"no network matches "+strconv.Quote(ref))
		}

		out = append(out, &api.NetworkAttachmentConfig{
			Target:  network.ID,
			Aliases: append([]string(nil), attachment.Aliases...),
		})
	}
	return out, nil
}

// invalidField is the one invalid-input constructor: the contract names the
// offending field in details.fields.
func invalidField(path, reason string) error {
	return output.NewErrorf(output.CodeInvalid, "%s %s", path, reason).
		WithDetails(map[string]any{"fields": map[string]any{path: reason}})
}

// durationProto converts integer milliseconds to a proto duration.
func durationProto(ms int64) *gogotypes.Duration {
	return gogotypes.DurationProto(time.Duration(ms) * time.Millisecond)
}

// portProtocol maps the lowercase wire enum. An absent value is TCP, SwarmKit's
// zero value.
func portProtocol(raw, path string) (api.PortConfig_Protocol, error) {
	switch raw {
	case "", "tcp":
		return api.ProtocolTCP, nil
	case "udp":
		return api.ProtocolUDP, nil
	case "sctp":
		return api.ProtocolSCTP, nil
	default:
		return api.ProtocolTCP, invalidField(path, `must be "tcp", "udp" or "sctp"`)
	}
}

// publishMode maps the lowercase wire enum. An absent value is ingress.
func publishMode(raw, path string) (api.PortConfig_PublishMode, error) {
	switch raw {
	case "", "ingress":
		return api.PublishModeIngress, nil
	case "host":
		return api.PublishModeHost, nil
	default:
		return api.PublishModeIngress, invalidField(path, `must be "ingress" or "host"`)
	}
}

// mountType maps the lowercase wire enum. A bind mount references a host path,
// which the contract reserves for admin (contract §5.3).
func mountType(raw, path string, allowBindMounts bool) (api.Mount_MountType, error) {
	switch raw {
	case "bind":
		if !allowBindMounts {
			return api.MountTypeBind, bindMountForbidden(path)
		}
		return api.MountTypeBind, nil
	case "volume":
		return api.MountTypeVolume, nil
	case "tmpfs":
		return api.MountTypeTmpfs, nil
	case "npipe":
		return api.MountTypeNamedPipe, nil
	case "cluster":
		return api.MountTypeCluster, nil
	default:
		return api.MountTypeBind, invalidField(path,
			`must be "bind", "volume", "tmpfs", "npipe" or "cluster"`)
	}
}

// bindMountForbidden is the 403 for a bind mount sent without the admin role.
//
// The role is named in details.requiredRole rather than details.requiredScope:
// the caller already holds services:write, and adding an admin-only scope would
// invent a scope the token model does not have.
func bindMountForbidden(path string) error {
	return output.NewErrorf(output.CodeForbidden,
		"%s: bind mounts require the admin role", path).
		WithDetails(map[string]any{
			"reason":       "bind_mount_requires_admin",
			"requiredRole": "admin",
			"fields":       map[string]any{path: "requires the admin role"},
		})
}

// restartCondition maps the lowercase wire enum.
func restartCondition(raw, path string) (api.RestartPolicy_RestartCondition, error) {
	switch raw {
	case "", "any":
		return api.RestartOnAny, nil
	case "none":
		return api.RestartOnNone, nil
	case "on-failure":
		return api.RestartOnFailure, nil
	default:
		return api.RestartOnAny, invalidField(path, `must be "none", "on-failure" or "any"`)
	}
}

// updateFailureAction maps the lowercase wire enum.
func updateFailureAction(raw, path string) (api.UpdateConfig_FailureAction, error) {
	switch raw {
	case "", "pause":
		return api.UpdateConfig_PAUSE, nil
	case "continue":
		return api.UpdateConfig_CONTINUE, nil
	case "rollback":
		return api.UpdateConfig_ROLLBACK, nil
	default:
		return api.UpdateConfig_PAUSE, invalidField(path,
			`must be "pause", "continue" or "rollback"`)
	}
}

// updateOrder maps the lowercase wire enum.
func updateOrder(raw, path string) (api.UpdateConfig_UpdateOrder, error) {
	switch raw {
	case "", "stop-first":
		return api.UpdateConfig_STOP_FIRST, nil
	case "start-first":
		return api.UpdateConfig_START_FIRST, nil
	default:
		return api.UpdateConfig_STOP_FIRST, invalidField(path,
			`must be "stop-first" or "start-first"`)
	}
}
