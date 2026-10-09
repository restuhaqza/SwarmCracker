package main

import (
	"fmt"
	"strings"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// serviceCreateOptions carries every input needed to build a service spec. It
// is kept separate from the cobra command so the spec mapping is a pure,
// unit-testable function.
type serviceCreateOptions struct {
	name        string
	image       string
	replicas    uint64
	cpu         float64
	memory      string
	disk        string
	env         []string
	command     []string
	args        []string
	labels      []string
	publish     []string
	publishMode string

	// Scheduling / lifecycle (all honored natively by SwarmKit).
	mode            string
	constraints     []string
	placementPrefs  []string
	restartSet      bool
	restartCond     string
	restartDelay    string
	restartAttempts uint64
	restartWindow   string

	updateSet        bool
	updateOrder      string
	updateParallel   uint64
	updateDelay      string
	updateFailAction string
	updateMonitor    string
	updateMaxRatio   float64

	rollbackSet        bool
	rollbackOrder      string
	rollbackParallel   uint64
	rollbackDelay      string
	rollbackFailAction string
	rollbackMonitor    string
	rollbackMaxRatio   float64
}

// buildServiceSpec builds a SwarmKit ServiceSpec from the CLI options. It
// validates every value it maps, so an invalid flag fails at create time rather
// than being silently ignored.
func buildServiceSpec(o serviceCreateOptions) (*api.ServiceSpec, error) {
	memoryBytes, err := parseMemory(o.memory)
	if err != nil {
		return nil, fmt.Errorf("invalid memory value: %w", err)
	}

	publishMode, err := types.NormalizePublishMode(o.publishMode)
	if err != nil {
		return nil, err
	}

	mode, err := normalizeServiceMode(o.mode)
	if err != nil {
		return nil, err
	}

	// The requested VM disk size is carried as a service label so the executor
	// can size the rootfs. A user-supplied label of the same name wins.
	svcLabels := parseLabels(o.labels)
	if o.disk != "" {
		if svcLabels == nil {
			svcLabels = make(map[string]string)
		}
		if _, ok := svcLabels[types.DiskSizeLabel]; !ok {
			svcLabels[types.DiskSizeLabel] = o.disk
		}
	}

	// Parse published ports. The mapping travels to the executor as a service
	// label (SwarmKit copies service labels onto each task) and is also
	// recorded on the spec's Endpoint so it surfaces in service ls/inspect.
	var endpointPorts []*api.PortConfig
	if len(o.publish) > 0 {
		published, err := types.ParsePublishSpec(o.publish)
		if err != nil {
			return nil, fmt.Errorf("invalid --publish: %w", err)
		}
		if svcLabels == nil {
			svcLabels = make(map[string]string)
		}
		svcLabels[types.PublishLabel] = types.FormatPublishLabel(published)
		svcLabels[types.PublishModeLabel] = publishMode
		endpointPorts = buildEndpointPorts(published, publishMode)
	}

	spec := &api.ServiceSpec{
		Annotations: api.Annotations{
			Name:   o.name,
			Labels: svcLabels,
		},
		Task: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image:   o.image,
					Env:     o.env,
					Command: o.command,
					Args:    o.args,
				},
			},
		},
	}

	if mode == modeGlobal {
		spec.Mode = &api.ServiceSpec_Global{Global: &api.GlobalService{}}
	} else {
		spec.Mode = &api.ServiceSpec_Replicated{Replicated: &api.ReplicatedService{Replicas: o.replicas}}
	}

	if len(endpointPorts) > 0 {
		spec.Endpoint = &api.EndpointSpec{Ports: endpointPorts}
	}

	if cpu := o.cpu; cpu > 0 || memoryBytes > 0 {
		spec.Task.Resources = &api.ResourceRequirements{Limits: &api.Resources{}}
		if cpu > 0 {
			spec.Task.Resources.Limits.NanoCPUs = int64(cpu * 1e9)
		}
		if memoryBytes > 0 {
			spec.Task.Resources.Limits.MemoryBytes = memoryBytes
		}
	}

	if placement, err := buildPlacement(o.constraints, o.placementPrefs); err != nil {
		return nil, err
	} else if placement != nil {
		spec.Task.Placement = placement
	}

	if restart, err := buildRestartPolicy(o); err != nil {
		return nil, err
	} else if restart != nil {
		spec.Task.Restart = restart
	}

	if update, err := buildUpdateConfig("update", o.updateSet, o.updateOrder, o.updateParallel, o.updateDelay, o.updateFailAction, o.updateMonitor, o.updateMaxRatio); err != nil {
		return nil, err
	} else if update != nil {
		spec.Update = update
	}

	if rollback, err := buildUpdateConfig("rollback", o.rollbackSet, o.rollbackOrder, o.rollbackParallel, o.rollbackDelay, o.rollbackFailAction, o.rollbackMonitor, o.rollbackMaxRatio); err != nil {
		return nil, err
	} else if rollback != nil {
		spec.Rollback = rollback
	}

	return spec, nil
}

const (
	modeReplicated = "replicated"
	modeGlobal     = "global"
)

// serviceUpdateOptions carries the inputs for `service update`. Each section has
// a "set" flag so an omitted flag leaves that part of the spec untouched.
type serviceUpdateOptions struct {
	replicas       uint64
	replicasSet    bool
	cpuLimit       float64
	memoryLimit    string
	image          string
	env            []string
	envRemove      []string
	publishMode    string
	publishModeSet bool
	force          bool

	rollbackAction bool

	modeSet        bool
	mode           string
	constraints    []string
	placementPrefs []string
	placementSet   bool

	restartSet      bool
	restartCond     string
	restartDelay    string
	restartAttempts uint64
	restartWindow   string

	updateSet        bool
	updateOrder      string
	updateParallel   uint64
	updateDelay      string
	updateFailAction string
	updateMonitor    string
	updateMaxRatio   float64

	rollbackSet        bool
	rollbackOrder      string
	rollbackParallel   uint64
	rollbackDelay      string
	rollbackFailAction string
	rollbackMonitor    string
	rollbackMaxRatio   float64
}

func normalizeServiceMode(m string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "", modeReplicated:
		return modeReplicated, nil
	case modeGlobal:
		return modeGlobal, nil
	default:
		return "", fmt.Errorf("invalid --mode %q (want replicated or global)", m)
	}
}

func buildPlacement(constraints, prefs []string) (*api.Placement, error) {
	if len(constraints) == 0 && len(prefs) == 0 {
		return nil, nil
	}
	placement := &api.Placement{}
	for _, c := range constraints {
		c = strings.TrimSpace(c)
		if c == "" {
			return nil, fmt.Errorf("empty --constraint")
		}
		if !strings.Contains(c, "==") && !strings.Contains(c, "!=") {
			return nil, fmt.Errorf("invalid --constraint %q (want key==value or key!=value)", c)
		}
		placement.Constraints = append(placement.Constraints, c)
	}
	for _, p := range prefs {
		p = strings.TrimSpace(p)
		if !strings.HasPrefix(p, "spread=") || strings.TrimPrefix(p, "spread=") == "" {
			return nil, fmt.Errorf("invalid --placement-pref %q (want spread=<key>)", p)
		}
		placement.Preferences = append(placement.Preferences, &api.PlacementPreference{
			Preference: &api.PlacementPreference_Spread{
				Spread: &api.SpreadOver{SpreadDescriptor: strings.TrimPrefix(p, "spread=")},
			},
		})
	}
	return placement, nil
}

func buildRestartPolicy(o serviceCreateOptions) (*api.RestartPolicy, error) {
	if !o.restartSet {
		return nil, nil
	}
	cond, err := normalizeRestartCondition(o.restartCond)
	if err != nil {
		return nil, err
	}
	rp := &api.RestartPolicy{Condition: cond, MaxAttempts: o.restartAttempts}
	if o.restartDelay != "" {
		d, err := parseOptDuration(o.restartDelay)
		if err != nil {
			return nil, fmt.Errorf("invalid --restart-delay: %w", err)
		}
		rp.Delay = gogotypes.DurationProto(d)
	}
	if o.restartWindow != "" {
		w, err := parseOptDuration(o.restartWindow)
		if err != nil {
			return nil, fmt.Errorf("invalid --restart-window: %w", err)
		}
		rp.Window = gogotypes.DurationProto(w)
	}
	return rp, nil
}

func buildUpdateConfig(prefix string, set bool, order string, parallelism uint64, delay, failAction, monitor string, maxRatio float64) (*api.UpdateConfig, error) {
	if !set {
		return nil, nil
	}
	o, err := normalizeUpdateOrder(order)
	if err != nil {
		return nil, fmt.Errorf("invalid --%s-order: %w", prefix, err)
	}
	fa, err := normalizeFailureAction(failAction)
	if err != nil {
		return nil, fmt.Errorf("invalid --%s-failure-action: %w", prefix, err)
	}
	d, err := parseOptDuration(delay)
	if err != nil {
		return nil, fmt.Errorf("invalid --%s-delay: %w", prefix, err)
	}
	uc := &api.UpdateConfig{
		Parallelism:     parallelism,
		Delay:           d,
		FailureAction:   fa,
		Order:           o,
		MaxFailureRatio: float32(maxRatio),
	}
	if monitor != "" {
		m, err := parseOptDuration(monitor)
		if err != nil {
			return nil, fmt.Errorf("invalid --%s-monitor: %w", prefix, err)
		}
		uc.Monitor = gogotypes.DurationProto(m)
	}
	return uc, nil
}

func normalizeRestartCondition(c string) (api.RestartPolicy_RestartCondition, error) {
	switch strings.ToLower(strings.TrimSpace(c)) {
	case "", "any":
		return api.RestartOnAny, nil
	case "none":
		return api.RestartOnNone, nil
	case "on-failure":
		return api.RestartOnFailure, nil
	default:
		return 0, fmt.Errorf("invalid --restart-condition %q (want none, on-failure or any)", c)
	}
}

func normalizeUpdateOrder(o string) (api.UpdateConfig_UpdateOrder, error) {
	switch strings.ToLower(strings.TrimSpace(o)) {
	case "", "stop-first":
		return api.UpdateConfig_STOP_FIRST, nil
	case "start-first":
		return api.UpdateConfig_START_FIRST, nil
	default:
		return 0, fmt.Errorf("order %q (want stop-first or start-first)", o)
	}
}

func normalizeFailureAction(a string) (api.UpdateConfig_FailureAction, error) {
	switch strings.ToLower(strings.TrimSpace(a)) {
	case "", "pause":
		return api.UpdateConfig_PAUSE, nil
	case "continue":
		return api.UpdateConfig_CONTINUE, nil
	case "rollback":
		return api.UpdateConfig_ROLLBACK, nil
	default:
		return 0, fmt.Errorf("action %q (want pause, continue or rollback)", a)
	}
}

func parseOptDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	if d < 0 {
		return 0, fmt.Errorf("duration must not be negative: %q", s)
	}
	return d, nil
}

// Human-readable formatters for the enums, used by `service inspect`.

func restartConditionString(c api.RestartPolicy_RestartCondition) string {
	switch c {
	case api.RestartOnNone:
		return "none"
	case api.RestartOnFailure:
		return "on-failure"
	case api.RestartOnAny:
		return "any"
	default:
		return "unknown"
	}
}

func updateOrderString(o api.UpdateConfig_UpdateOrder) string {
	if o == api.UpdateConfig_START_FIRST {
		return "start-first"
	}
	return "stop-first"
}

func failureActionString(a api.UpdateConfig_FailureAction) string {
	switch a {
	case api.UpdateConfig_CONTINUE:
		return "continue"
	case api.UpdateConfig_ROLLBACK:
		return "rollback"
	default:
		return "pause"
	}
}
