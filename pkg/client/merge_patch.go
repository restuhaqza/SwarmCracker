// This file owns the RFC 7386 merge-patch overlay for PATCH /v1/services/{ref}.
//
// The overlay starts from a deep copy of the current SwarmKit spec and writes
// only the fields the patch names. That is the whole point of a curated DTO over
// UpdateService, which replaces the entire ServiceSpec: fields this API does not
// model (mount volume options, privileges, endpoint mode, placement preferences,
// log drivers, …) must survive a patch that does not mention them.
//
// Rules, per the contract:
//   - null resets an optional scalar to its default;
//   - null on a map key deletes that key;
//   - [] clears an array;
//   - a non-null array replaces the array wholesale.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
)

// decodeMergePatch parses one merge patch. The body must be a single JSON
// object; trailing data is rejected so a truncated retry cannot be half-applied.
func decodeMergePatch(body []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, invalidField("body", "must be a JSON object")
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()

	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, invalidField("body", "is not valid JSON: "+err.Error())
	}

	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, invalidField("body", "must contain exactly one JSON object")
	}

	object, ok := raw.(map[string]any)
	if !ok {
		return nil, invalidField("body", "must be a JSON object")
	}
	return object, nil
}

// applyServicePatch overlays the top level of a merge patch.
func (c *Client) applyServicePatch(ctx context.Context, spec *api.ServiceSpec, patch map[string]any, allowBindMounts bool) error {
	for key, value := range patch {
		switch key {
		case "labels":
			if err := applyLabelsPatch(spec, value); err != nil {
				return err
			}
		case "spec":
			object, err := patchObject("spec", value)
			if err != nil {
				return err
			}
			if err := c.applySpecPatch(ctx, spec, object, allowBindMounts); err != nil {
				return err
			}
		case "id", "name", "revision", "createdAt", "updatedAt", "status",
			"schemaVersion", "kind":
			// The read envelope is not writable. Accepting it would make a
			// document round-trip silently change an immutable field.
			return invalidField(key, "is immutable")
		default:
			return unknownPatchField(key)
		}
	}
	return nil
}

// applySpecPatch overlays spec.*.
func (c *Client) applySpecPatch(ctx context.Context, spec *api.ServiceSpec, patch map[string]any, allowBindMounts bool) error {
	for key, value := range patch {
		switch key {
		case "mode":
			return invalidField("spec.mode", "is immutable")
		case "replicas":
			if err := applyReplicasPatch(spec, value); err != nil {
				return err
			}
		case "task":
			object, err := patchObject("spec.task", value)
			if err != nil {
				return err
			}
			if err := c.applyTaskPatch(ctx, spec, object, allowBindMounts); err != nil {
				return err
			}
		case "update":
			if err := applyUpdatePatch(&spec.Update, "spec.update", value); err != nil {
				return err
			}
		case "rollback":
			if err := applyUpdatePatch(&spec.Rollback, "spec.rollback", value); err != nil {
				return err
			}
		case "placement":
			if err := applyPlacementPatch(&spec.Task.Placement, value); err != nil {
				return err
			}
		default:
			return unknownPatchField("spec." + key)
		}
	}
	return nil
}

// applyLabelsPatch merges a service's labels. null clears every label; a null
// value deletes one key.
func applyLabelsPatch(spec *api.ServiceSpec, value any) error {
	return applyAnnotationsLabelsPatch(&spec.Annotations, "labels", value)
}

// applyAnnotationsLabelsPatch merges labels on any annotated object. Configs,
// secrets and nodes use the same RFC 7386 rules as a service, so the merge lives
// in one place instead of four copies that could drift apart.
func applyAnnotationsLabelsPatch(annotations *api.Annotations, path string, value any) error {
	if value == nil {
		annotations.Labels = nil
		return nil
	}

	object, err := patchObject(path, value)
	if err != nil {
		return err
	}
	for key, raw := range object {
		if raw == nil {
			delete(annotations.Labels, key)
			continue
		}
		label, err := patchString(path+"."+key, raw)
		if err != nil {
			return err
		}
		if annotations.Labels == nil {
			annotations.Labels = make(map[string]string, len(object))
		}
		annotations.Labels[key] = label
	}
	return nil
}

// applyReplicasPatch refuses replicas on a global service and resets a null to
// the default of one.
func applyReplicasPatch(spec *api.ServiceSpec, value any) error {
	replicated := spec.GetReplicated()
	if replicated == nil {
		return invalidField("spec.replicas", "is only valid when the service mode is replicated")
	}

	if value == nil {
		replicated.Replicas = 1
		return nil
	}
	replicas, err := patchUint("spec.replicas", value)
	if err != nil {
		return err
	}
	replicated.Replicas = replicas
	return nil
}

// applyTaskPatch overlays spec.task.*.
func (c *Client) applyTaskPatch(ctx context.Context, spec *api.ServiceSpec, patch map[string]any, allowBindMounts bool) error {
	container := spec.Task.GetContainer()
	if container == nil {
		return invalidField("spec.task", "the service has no container spec to patch")
	}

	for key, value := range patch {
		switch key {
		case "image":
			image, err := patchString("spec.task.image", value)
			if err != nil {
				return err
			}
			if strings.TrimSpace(image) == "" {
				return invalidField("spec.task.image", "must not be empty")
			}
			container.Image = strings.TrimSpace(image)
		case "command":
			command, err := patchStringSlice("spec.task.command", value)
			if err != nil {
				return err
			}
			container.Command = command
		case "args":
			args, err := patchStringSlice("spec.task.args", value)
			if err != nil {
				return err
			}
			container.Args = args
		case "env":
			if err := applyEnvPatch(container, value); err != nil {
				return err
			}
		case "user":
			user, err := patchOptionalString("spec.task.user", value)
			if err != nil {
				return err
			}
			container.User = user
		case "workdir":
			dir, err := patchOptionalString("spec.task.workdir", value)
			if err != nil {
				return err
			}
			container.Dir = dir
		case "stopSignal":
			signal, err := patchOptionalString("spec.task.stopSignal", value)
			if err != nil {
				return err
			}
			container.StopSignal = signal
		case "stopGraceMs":
			if value == nil {
				container.StopGracePeriod = nil
				break
			}
			ms, err := patchInt("spec.task.stopGraceMs", value)
			if err != nil {
				return err
			}
			if ms < 0 {
				return invalidField("spec.task.stopGraceMs", "must be >= 0")
			}
			container.StopGracePeriod = durationProto(ms)
		case "readOnlyRootfs":
			readOnly, err := patchOptionalBool("spec.task.readOnlyRootfs", value)
			if err != nil {
				return err
			}
			container.ReadOnly = readOnly
		case "vcpus":
			vcpus, err := patchOptionalInt("spec.task.vcpus", value)
			if err != nil {
				return err
			}
			if vcpus < 0 {
				return invalidField("spec.task.vcpus", "must be >= 0")
			}
			applyCPULimit(&spec.Task, vcpus)
		case "memoryBytes":
			memory, err := patchOptionalInt("spec.task.memoryBytes", value)
			if err != nil {
				return err
			}
			if memory < 0 {
				return invalidField("spec.task.memoryBytes", "must be >= 0")
			}
			applyMemoryLimit(&spec.Task, memory)
		case "ports":
			ports, err := patchPorts(value)
			if err != nil {
				return err
			}
			if spec.Endpoint == nil && len(ports) > 0 {
				spec.Endpoint = &api.EndpointSpec{}
			}
			if spec.Endpoint != nil {
				// Only Ports is modeled; EndpointSpec.Mode (the resolver) and any
				// future field stay as they were.
				spec.Endpoint.Ports = ports
			}
		case "mounts":
			mounts, err := patchMounts(value, allowBindMounts)
			if err != nil {
				return err
			}
			container.Mounts = mounts
		case "networks":
			networks, err := c.patchNetworks(ctx, value)
			if err != nil {
				return err
			}
			spec.Task.Networks = networks
			// The deprecated ServiceSpec.Networks shadow is cleared so a read
			// cannot report two different attachment lists.
			spec.Networks = nil
		case "restartPolicy":
			if err := applyRestartPatch(&spec.Task.Restart, value); err != nil {
				return err
			}
		case "healthcheck":
			if err := applyHealthcheckPatch(container, value); err != nil {
				return err
			}
		case "registryAuth":
			if value == nil {
				container.PullOptions = nil
				break
			}
			auth, err := patchString("spec.task.registryAuth", value)
			if err != nil {
				return err
			}
			// An empty string means "no credential", exactly as on create; it
			// must not leave a non-nil PullOptions that reads as cleared but is
			// a different spec.
			if auth == "" {
				container.PullOptions = nil
				break
			}
			container.PullOptions = &api.ContainerSpec_PullOptions{RegistryAuth: auth}
		default:
			return unknownPatchField("spec.task." + key)
		}
	}
	return nil
}

// applyEnvPatch merges the env map. A null value deletes one key and a null map
// clears them all; the list is then resorted so a patch produces a deterministic
// spec.
func applyEnvPatch(container *api.ContainerSpec, value any) error {
	if value == nil {
		container.Env = nil
		return nil
	}

	object, err := patchObject("spec.task.env", value)
	if err != nil {
		return err
	}

	env := envMap(container.Env)
	if env == nil && len(object) > 0 {
		env = make(map[string]string, len(object))
	}
	for key, raw := range object {
		if err := validEnvKey(key); err != nil {
			return err
		}
		if raw == nil {
			delete(env, key)
			continue
		}
		envValue, err := patchString("spec.task.env."+key, raw)
		if err != nil {
			return err
		}
		env[key] = envValue
	}

	container.Env, err = envList(env)
	return err
}

// applyCPULimit writes NanoCPUs while leaving the rest of Resources alone. A
// zero value against no resources at all is a no-op: creating an empty block
// would rewrite bytes the caller did not change.
func applyCPULimit(task *api.TaskSpec, vcpus int64) {
	if vcpus == 0 && (task.Resources == nil ||
		(task.Resources.Limits == nil && task.Resources.Reservations == nil)) {
		return
	}
	ensureLimits(task).NanoCPUs = vcpus * nanoCPUsPerVCPU
}

// applyMemoryLimit writes MemoryBytes with the same no-op rule as the CPU limit.
func applyMemoryLimit(task *api.TaskSpec, memoryBytes int64) {
	if memoryBytes == 0 && (task.Resources == nil ||
		(task.Resources.Limits == nil && task.Resources.Reservations == nil)) {
		return
	}
	ensureLimits(task).MemoryBytes = memoryBytes
}

// ensureLimits returns the block a cpu/memory patch writes to.
//
// Every consumer reads Limits and falls back to Reservations only when Limits is
// absent, so materialising an empty Limits for a patch that names one half of
// the pair would make the other half's reservation inert: a vcpus-only patch
// would read memoryBytes 0 and drop the memory cap. A new Limits is therefore
// seeded from Reservations, and the reservation itself stays untouched.
func ensureLimits(task *api.TaskSpec) *api.Resources {
	if task.Resources == nil {
		task.Resources = &api.ResourceRequirements{}
	}
	if task.Resources.Limits == nil {
		limits := &api.Resources{}
		if reservations := task.Resources.Reservations; reservations != nil {
			limits.NanoCPUs = reservations.NanoCPUs
			limits.MemoryBytes = reservations.MemoryBytes
		}
		task.Resources.Limits = limits
	}
	return task.Resources.Limits
}

// applyRestartPatch merges only the named restart fields, so an unmodeled field
// (or a field the caller did not mention) survives.
func applyRestartPatch(target **api.RestartPolicy, value any) error {
	if value == nil {
		*target = nil
		return nil
	}
	object, err := patchObject("spec.task.restartPolicy", value)
	if err != nil {
		return err
	}
	if *target == nil {
		*target = &api.RestartPolicy{}
	}
	policy := *target

	for key, raw := range object {
		switch key {
		case "condition":
			condition, err := patchOptionalString("spec.task.restartPolicy.condition", raw)
			if err != nil {
				return err
			}
			normalized, err := restartCondition(condition, "spec.task.restartPolicy.condition")
			if err != nil {
				return err
			}
			policy.Condition = normalized
		case "delayMs":
			ms, err := patchOptionalInt("spec.task.restartPolicy.delayMs", raw)
			if err != nil {
				return err
			}
			if ms < 0 {
				return invalidField("spec.task.restartPolicy.delayMs", "must be >= 0")
			}
			if ms == 0 {
				policy.Delay = nil
			} else {
				policy.Delay = durationProto(ms)
			}
		case "maxAttempts":
			attempts, err := patchOptionalUint("spec.task.restartPolicy.maxAttempts", raw)
			if err != nil {
				return err
			}
			policy.MaxAttempts = attempts
		case "windowMs":
			ms, err := patchOptionalInt("spec.task.restartPolicy.windowMs", raw)
			if err != nil {
				return err
			}
			if ms < 0 {
				return invalidField("spec.task.restartPolicy.windowMs", "must be >= 0")
			}
			if ms == 0 {
				policy.Window = nil
			} else {
				policy.Window = durationProto(ms)
			}
		default:
			return unknownPatchField("spec.task.restartPolicy." + key)
		}
	}
	return nil
}

// applyHealthcheckPatch merges the healthcheck. null removes it entirely; an
// explicitly emptied `test` array clears it too, because SwarmKit would read an
// empty test as "inherit", which is not what the merge-patch `[]` rule means.
func applyHealthcheckPatch(container *api.ContainerSpec, value any) error {
	if value == nil {
		container.Healthcheck = nil
		return nil
	}
	object, err := patchObject("spec.task.healthcheck", value)
	if err != nil {
		return err
	}

	var test []string
	if raw, ok := object["test"]; ok {
		test, err = patchStringSlice("spec.task.healthcheck.test", raw)
		if err != nil {
			return err
		}
		if len(test) == 0 {
			container.Healthcheck = nil
			return nil
		}
	}

	if container.Healthcheck == nil {
		container.Healthcheck = &api.HealthConfig{}
	}
	health := container.Healthcheck

	for key, raw := range object {
		switch key {
		case "test":
			health.Test = test
		case "intervalMs":
			ms, err := patchOptionalInt("spec.task.healthcheck.intervalMs", raw)
			if err != nil {
				return err
			}
			duration, err := durationOrNil("spec.task.healthcheck.intervalMs", ms)
			if err != nil {
				return err
			}
			health.Interval = duration
		case "timeoutMs":
			ms, err := patchOptionalInt("spec.task.healthcheck.timeoutMs", raw)
			if err != nil {
				return err
			}
			duration, err := durationOrNil("spec.task.healthcheck.timeoutMs", ms)
			if err != nil {
				return err
			}
			health.Timeout = duration
		case "startPeriodMs":
			ms, err := patchOptionalInt("spec.task.healthcheck.startPeriodMs", raw)
			if err != nil {
				return err
			}
			duration, err := durationOrNil("spec.task.healthcheck.startPeriodMs", ms)
			if err != nil {
				return err
			}
			health.StartPeriod = duration
		case "retries":
			retries, err := patchOptionalInt("spec.task.healthcheck.retries", raw)
			if err != nil {
				return err
			}
			if retries < 0 || retries > math.MaxInt32 {
				return invalidField("spec.task.healthcheck.retries", "must be between 0 and 2147483647")
			}
			//nolint:gosec // bounded above
			health.Retries = int32(retries)
		default:
			return unknownPatchField("spec.task.healthcheck." + key)
		}
	}
	return nil
}

// applyUpdatePatch merges an update or rollback policy. null removes the block.
func applyUpdatePatch(target **api.UpdateConfig, path string, value any) error {
	if value == nil {
		*target = nil
		return nil
	}
	object, err := patchObject(path, value)
	if err != nil {
		return err
	}
	if *target == nil {
		*target = &api.UpdateConfig{}
	}
	policy := *target

	for key, raw := range object {
		switch key {
		case "parallelism":
			parallelism, err := patchOptionalUint(path+".parallelism", raw)
			if err != nil {
				return err
			}
			policy.Parallelism = parallelism
		case "delayMs":
			ms, err := patchOptionalInt(path+".delayMs", raw)
			if err != nil {
				return err
			}
			if ms < 0 {
				return invalidField(path+".delayMs", "must be >= 0")
			}
			policy.Delay = time.Duration(ms) * time.Millisecond
		case "failureAction":
			action, err := patchOptionalString(path+".failureAction", raw)
			if err != nil {
				return err
			}
			policy.FailureAction, err = updateFailureAction(action, path+".failureAction")
			if err != nil {
				return err
			}
		case "order":
			order, err := patchOptionalString(path+".order", raw)
			if err != nil {
				return err
			}
			policy.Order, err = updateOrder(order, path+".order")
			if err != nil {
				return err
			}
		case "monitorMs":
			ms, err := patchOptionalInt(path+".monitorMs", raw)
			if err != nil {
				return err
			}
			policy.Monitor, err = durationOrNil(path+".monitorMs", ms)
			if err != nil {
				return err
			}
		case "maxFailureRatio":
			if raw == nil {
				policy.MaxFailureRatio = 0
				break
			}
			ratio, err := patchFloat(path+".maxFailureRatio", raw)
			if err != nil {
				return err
			}
			if ratio < 0 || ratio > 1 {
				return invalidField(path+".maxFailureRatio", "must be between 0 and 1")
			}
			policy.MaxFailureRatio = float32(ratio)
		default:
			return unknownPatchField(path + "." + key)
		}
	}
	return nil
}

// applyPlacementPatch merges constraints. Preferences, platforms and
// maxReplicas are not modeled and therefore survive untouched.
func applyPlacementPatch(target **api.Placement, value any) error {
	if value == nil {
		*target = nil
		return nil
	}
	object, err := patchObject("spec.placement", value)
	if err != nil {
		return err
	}
	if *target == nil {
		*target = &api.Placement{}
	}

	for key, raw := range object {
		switch key {
		case "constraints":
			constraints, err := patchStringSlice("spec.placement.constraints", raw)
			if err != nil {
				return err
			}
			(*target).Constraints = constraints
		default:
			return unknownPatchField("spec.placement." + key)
		}
	}
	return nil
}

// patchPorts validates an array replacement. null or [] clears the list.
func patchPorts(value any) ([]*api.PortConfig, error) {
	if value == nil {
		return nil, nil
	}
	array, ok := value.([]any)
	if !ok {
		return nil, invalidField("spec.task.ports", "must be an array or null")
	}
	if len(array) == 0 {
		return nil, nil
	}

	out := make([]v1.Port, 0, len(array))
	for i, item := range array {
		path := "spec.task.ports[" + strconv.Itoa(i) + "]"
		object, err := patchObject(path, item)
		if err != nil {
			return nil, err
		}

		port := v1.Port{}
		target := false
		for key, raw := range object {
			switch key {
			case "target":
				n, err := patchUint32(path+".target", raw)
				if err != nil {
					return nil, err
				}
				port.Target = n
				target = true
			case "published":
				n, err := patchUint32(path+".published", raw)
				if err != nil {
					return nil, err
				}
				port.Published = n
			case "protocol":
				s, err := patchString(path+".protocol", raw)
				if err != nil {
					return nil, err
				}
				port.Protocol = s
			case "mode":
				s, err := patchString(path+".mode", raw)
				if err != nil {
					return nil, err
				}
				port.Mode = s
			default:
				return nil, unknownPatchField(path + "." + key)
			}
		}
		if !target {
			return nil, invalidField(path+".target", "is required")
		}
		out = append(out, port)
	}

	return portSpecs(out)
}

// patchMounts validates an array replacement. null or [] clears the list.
func patchMounts(value any, allowBindMounts bool) ([]api.Mount, error) {
	if value == nil {
		return nil, nil
	}
	array, ok := value.([]any)
	if !ok {
		return nil, invalidField("spec.task.mounts", "must be an array or null")
	}
	if len(array) == 0 {
		return nil, nil
	}

	out := make([]v1.Mount, 0, len(array))
	for i, item := range array {
		path := "spec.task.mounts[" + strconv.Itoa(i) + "]"
		object, err := patchObject(path, item)
		if err != nil {
			return nil, err
		}

		mount := v1.Mount{}
		for key, raw := range object {
			switch key {
			case "type":
				s, err := patchString(path+".type", raw)
				if err != nil {
					return nil, err
				}
				mount.Type = s
			case "source":
				s, err := patchOptionalString(path+".source", raw)
				if err != nil {
					return nil, err
				}
				mount.Source = s
			case "target":
				s, err := patchString(path+".target", raw)
				if err != nil {
					return nil, err
				}
				mount.Target = s
			case "readOnly":
				b, err := patchOptionalBool(path+".readOnly", raw)
				if err != nil {
					return nil, err
				}
				mount.ReadOnly = b
			default:
				return nil, unknownPatchField(path + "." + key)
			}
		}
		out = append(out, mount)
	}

	return mountSpecs(out, allowBindMounts)
}

// patchNetworks resolves an array replacement of network attachments.
func (c *Client) patchNetworks(ctx context.Context, value any) ([]*api.NetworkAttachmentConfig, error) {
	if value == nil {
		return nil, nil
	}
	array, ok := value.([]any)
	if !ok {
		return nil, invalidField("spec.task.networks", "must be an array or null")
	}
	if len(array) == 0 {
		return nil, nil
	}

	attachments := make([]v1.NetworkAttachment, 0, len(array))
	for i, item := range array {
		path := "spec.task.networks[" + strconv.Itoa(i) + "]"
		object, err := patchObject(path, item)
		if err != nil {
			return nil, err
		}

		attachment := v1.NetworkAttachment{}
		for key, raw := range object {
			switch key {
			case "id":
				s, err := patchOptionalString(path+".id", raw)
				if err != nil {
					return nil, err
				}
				attachment.ID = s
			case "name":
				s, err := patchOptionalString(path+".name", raw)
				if err != nil {
					return nil, err
				}
				attachment.Name = s
			case "aliases":
				aliases, err := patchStringSlice(path+".aliases", raw)
				if err != nil {
					return nil, err
				}
				attachment.Aliases = aliases
			default:
				return nil, unknownPatchField(path + "." + key)
			}
		}
		attachments = append(attachments, attachment)
	}

	return c.networkAttachments(ctx, attachments)
}

// patchObject decodes one nested object. null is not an object: each caller has
// already decided what null means for its own field.
func patchObject(path string, value any) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invalidField(path, "must be an object")
	}
	return object, nil
}

// patchString requires a string.
func patchString(path string, value any) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", invalidField(path, "must be a string")
	}
	return s, nil
}

// patchOptionalString treats null as the empty string, its default.
func patchOptionalString(path string, value any) (string, error) {
	if value == nil {
		return "", nil
	}
	return patchString(path, value)
}

// patchOptionalBool treats null as false, its default.
func patchOptionalBool(path string, value any) (bool, error) {
	if value == nil {
		return false, nil
	}
	b, ok := value.(bool)
	if !ok {
		return false, invalidField(path, "must be a boolean")
	}
	return b, nil
}

// patchOptionalInt treats null as zero, the "no limit" default, and refuses a
// fractional or non-numeric value.
func patchOptionalInt(path string, value any) (int64, error) {
	if value == nil {
		return 0, nil
	}
	return patchInt(path, value)
}

// patchInt parses an integer. The decoder uses json.Number, so a float such as
// 1.5 never silently truncates.
func patchInt(path string, value any) (int64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, invalidField(path, "must be an integer")
	}
	parsed, err := number.Int64()
	if err != nil {
		return 0, invalidField(path, "must be an integer")
	}
	return parsed, nil
}

// patchOptionalUint treats null as zero.
func patchOptionalUint(path string, value any) (uint64, error) {
	if value == nil {
		return 0, nil
	}
	return patchUint(path, value)
}

// patchUint parses a non-negative integer.
func patchUint(path string, value any) (uint64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, invalidField(path, "must be a non-negative integer")
	}
	parsed, err := strconv.ParseUint(number.String(), 10, 64)
	if err != nil {
		return 0, invalidField(path, "must be a non-negative integer")
	}
	return parsed, nil
}

// patchUint32 parses a port number.
func patchUint32(path string, value any) (uint32, error) {
	parsed, err := patchUint(path, value)
	if err != nil {
		return 0, err
	}
	if parsed > 65535 {
		return 0, invalidField(path, "must be <= 65535")
	}
	return uint32(parsed), nil
}

// patchFloat parses a JSON number.
func patchFloat(path string, value any) (float64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, invalidField(path, "must be a number")
	}
	parsed, err := number.Float64()
	if err != nil {
		return 0, invalidField(path, "must be a number")
	}
	return parsed, nil
}

// patchStringSlice parses an array of strings. null and [] both clear.
func patchStringSlice(path string, value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	array, ok := value.([]any)
	if !ok {
		return nil, invalidField(path, "must be an array of strings or null")
	}

	out := make([]string, 0, len(array))
	for i, item := range array {
		s, ok := item.(string)
		if !ok {
			return nil, invalidField(fmt.Sprintf("%s[%d]", path, i), "must be a string")
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// durationOrNil maps a millisecond count to a proto duration, with zero meaning
// absent.
func durationOrNil(path string, ms int64) (*gogotypes.Duration, error) {
	if ms < 0 {
		return nil, invalidField(path, "must be >= 0")
	}
	if ms == 0 {
		return nil, nil
	}
	return durationProto(ms), nil
}

// unknownPatchField rejects a field the DTO does not model. Silently dropping it
// would make the caller believe the write happened.
func unknownPatchField(path string) error {
	return invalidField(path, "is not a writable field")
}
