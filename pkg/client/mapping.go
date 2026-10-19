package client

import (
	"strconv"
	"strings"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
)

// taskCounts is the small aggregate the service status needs.
type taskCounts struct {
	total      int
	desired    int
	running    int
	transition int
}

func countTasks(tasks []*api.Task) map[string]taskCounts {
	out := make(map[string]taskCounts)
	for _, task := range tasks {
		if task == nil {
			continue
		}
		c := out[task.ServiceID]
		c.total++
		if task.DesiredState == api.TaskStateRunning {
			c.desired++
		}
		switch task.Status.State {
		case api.TaskStateRunning:
			c.running++
		case api.TaskStateNew,
			api.TaskStatePending,
			api.TaskStateAssigned,
			api.TaskStateAccepted,
			api.TaskStatePreparing,
			api.TaskStateReady,
			api.TaskStateStarting:
			c.transition++
		}
		out[task.ServiceID] = c
	}
	return out
}

func serviceFromSwarmkit(svc *api.Service, counts taskCounts) v1.Service {
	container := svc.Spec.Task.GetContainer()
	spec := v1.ServiceSpec{
		Mode: modeString(svc.Spec),
		Task: taskSpec(container, &svc.Spec.Task),
	}
	if replicated := svc.Spec.GetReplicated(); replicated != nil {
		replicas := int64(replicated.Replicas)
		spec.Replicas = &replicas
	}
	spec.Task.Ports = ports(svc.Spec.Endpoint)
	if len(spec.Task.Networks) == 0 && len(svc.Spec.Networks) > 0 {
		// Older services keep network attachments on ServiceSpec.
		spec.Task.Networks = networks(svc.Spec.Networks)
	}
	spec.Update = updateConfig(svc.Spec.Update)
	spec.Rollback = updateConfig(svc.Spec.Rollback)
	spec.Placement = placement(svc.Spec.Task.Placement)

	desired := counts.desired
	if spec.Replicas != nil {
		desired = int(*spec.Replicas)
	}
	status := v1.ServiceStatus{
		DesiredTasks:      desired,
		RunningTasks:      counts.running,
		TasksInTransition: counts.transition,
		Converged:         desired == counts.running && counts.transition == 0,
		Endpoint:          endpoint(svc.Endpoint),
	}

	return v1.Service{
		ID:        svc.ID,
		Name:      svc.Spec.Annotations.Name,
		Labels:    copyMap(svc.Spec.Annotations.Labels),
		Revision:  strconv.FormatUint(svc.Meta.Version.Index, 10),
		CreatedAt: timestamp(svc.Meta.CreatedAt),
		UpdatedAt: timestamp(svc.Meta.UpdatedAt),
		Spec:      spec,
		Status:    status,
	}
}

func taskSpec(container *api.ContainerSpec, task *api.TaskSpec) v1.TaskSpec {
	out := v1.TaskSpec{}
	if container != nil {
		out.Image = container.Image
		out.Command = append([]string(nil), container.Command...)
		out.Args = append([]string(nil), container.Args...)
		out.Env = envMap(container.Env)
		out.User = container.User
		out.Workdir = container.Dir
		out.StopSignal = container.StopSignal
		out.StopGraceMs = durationMs(container.StopGracePeriod)
		out.ReadOnlyRootfs = container.ReadOnly
		out.Mounts = mounts(container.Mounts)
		out.Healthcheck = healthcheck(container.Healthcheck)
	}
	if task != nil {
		if task.Resources != nil {
			res := task.Resources.Limits
			if res == nil {
				res = task.Resources.Reservations
			}
			if res != nil {
				out.VCPUs = res.NanoCPUs / 1_000_000_000
				out.MemoryBytes = res.MemoryBytes
			}
		}
		out.Networks = networks(task.Networks)
		out.RestartPolicy = restartPolicy(task.Restart)
	}
	return out
}

func modeString(spec api.ServiceSpec) string {
	switch {
	case spec.GetReplicated() != nil:
		return "replicated"
	case spec.GetGlobal() != nil:
		return "global"
	case spec.GetReplicatedJob() != nil:
		return "replicated-job"
	case spec.GetGlobalJob() != nil:
		return "global-job"
	default:
		return "unknown"
	}
}

func envMap(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func ports(endpoint *api.EndpointSpec) []v1.Port {
	if endpoint == nil || len(endpoint.Ports) == 0 {
		return nil
	}
	out := make([]v1.Port, 0, len(endpoint.Ports))
	for _, p := range endpoint.Ports {
		if p == nil {
			continue
		}
		out = append(out, v1.Port{
			Target:    p.TargetPort,
			Published: p.PublishedPort,
			Protocol:  protocolString(p.Protocol),
			Mode:      publishModeString(p.PublishMode),
		})
	}
	return out
}

func endpoint(endpoint *api.Endpoint) *v1.Endpoint {
	if endpoint == nil || len(endpoint.Ports) == 0 {
		return nil
	}
	out := &v1.Endpoint{Ports: make([]v1.Port, 0, len(endpoint.Ports))}
	for _, p := range endpoint.Ports {
		if p == nil {
			continue
		}
		out.Ports = append(out.Ports, v1.Port{
			Target:    p.TargetPort,
			Published: p.PublishedPort,
			Protocol:  protocolString(p.Protocol),
			Mode:      publishModeString(p.PublishMode),
		})
	}
	if len(out.Ports) == 0 {
		return nil
	}
	return out
}

func mounts(in []api.Mount) []v1.Mount {
	if len(in) == 0 {
		return nil
	}
	out := make([]v1.Mount, 0, len(in))
	for _, m := range in {
		out = append(out, v1.Mount{
			Type:     mountTypeString(m.Type),
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	return out
}

func networks(in []*api.NetworkAttachmentConfig) []v1.NetworkAttachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]v1.NetworkAttachment, 0, len(in))
	for _, n := range in {
		if n == nil {
			continue
		}
		// The network name is resolved by the gateway when it has the network
		// list; echoing the ID as a name would be misleading.
		out = append(out, v1.NetworkAttachment{
			ID:      n.Target,
			Aliases: append([]string(nil), n.Aliases...),
		})
	}
	return out
}

func healthcheck(in *api.HealthConfig) *v1.Healthcheck {
	if in == nil || len(in.Test) == 0 {
		return nil
	}
	return &v1.Healthcheck{
		Test:          append([]string(nil), in.Test...),
		IntervalMs:    durationMs(in.Interval),
		TimeoutMs:     durationMs(in.Timeout),
		Retries:       int64(in.Retries),
		StartPeriodMs: durationMs(in.StartPeriod),
	}
}

func restartPolicy(in *api.RestartPolicy) *v1.RestartPolicy {
	if in == nil {
		return nil
	}
	return &v1.RestartPolicy{
		Condition:   restartConditionString(in.Condition),
		DelayMs:     durationMs(in.Delay),
		MaxAttempts: in.MaxAttempts,
		WindowMs:    durationMs(in.Window),
	}
}

func updateConfig(in *api.UpdateConfig) *v1.UpdateConfig {
	if in == nil {
		return nil
	}
	return &v1.UpdateConfig{
		Parallelism:     in.Parallelism,
		DelayMs:         in.Delay.Milliseconds(),
		FailureAction:   updateFailureActionString(in.FailureAction),
		Order:           updateOrderString(in.Order),
		MonitorMs:       durationMs(in.Monitor),
		MaxFailureRatio: in.MaxFailureRatio,
	}
}

func placement(in *api.Placement) *v1.Placement {
	if in == nil || len(in.Constraints) == 0 {
		return nil
	}
	return &v1.Placement{Constraints: append([]string(nil), in.Constraints...)}
}

func timestamp(ts *gogotypes.Timestamp) string {
	if ts == nil {
		return ""
	}
	return time.Unix(ts.Seconds, int64(ts.Nanos)).UTC().Format(time.RFC3339Nano)
}

func durationMs(d *gogotypes.Duration) int64 {
	if d == nil {
		return 0
	}
	dur, err := gogotypes.DurationFromProto(d)
	if err != nil {
		return 0
	}
	return dur.Milliseconds()
}

func protocolString(p api.PortConfig_Protocol) string {
	switch p {
	case api.ProtocolTCP:
		return "tcp"
	case api.ProtocolUDP:
		return "udp"
	case api.ProtocolSCTP:
		return "sctp"
	default:
		return ""
	}
}

func publishModeString(m api.PortConfig_PublishMode) string {
	switch m {
	case api.PublishModeIngress:
		return "ingress"
	case api.PublishModeHost:
		return "host"
	default:
		return ""
	}
}

func mountTypeString(t api.Mount_MountType) string {
	switch t {
	case api.MountTypeBind:
		return "bind"
	case api.MountTypeVolume:
		return "volume"
	case api.MountTypeTmpfs:
		return "tmpfs"
	case api.MountTypeNamedPipe:
		return "npipe"
	case api.MountTypeCluster:
		return "cluster"
	default:
		return ""
	}
}

func restartConditionString(c api.RestartPolicy_RestartCondition) string {
	switch c {
	case api.RestartOnAny:
		return "any"
	case api.RestartOnFailure:
		return "on-failure"
	case api.RestartOnNone:
		return "none"
	default:
		return ""
	}
}

func updateFailureActionString(a api.UpdateConfig_FailureAction) string {
	switch a {
	case api.UpdateConfig_PAUSE:
		return "pause"
	case api.UpdateConfig_CONTINUE:
		return "continue"
	case api.UpdateConfig_ROLLBACK:
		return "rollback"
	default:
		return ""
	}
}

func updateOrderString(o api.UpdateConfig_UpdateOrder) string {
	switch o {
	case api.UpdateConfig_STOP_FIRST:
		return "stop-first"
	case api.UpdateConfig_START_FIRST:
		return "start-first"
	default:
		return ""
	}
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
