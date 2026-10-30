package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/jailer"
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
	mounts      []string
	volumes     []string

	// balloonMib is --balloon-mib: the memory-balloon target in MiB. balloonSet
	// records whether the flag was given at all, so an explicit 0 is rejected
	// rather than mistaken for "unset" and silently ignored.
	balloonMib int
	balloonSet bool

	// cpuset is --cpuset: the host CPUs the VM is pinned to (a Linux cpulist).
	// cpusetSet records whether the flag was given at all, so an explicit
	// value is never mistaken for "unset" and silently dropped.
	cpuset    string
	cpusetSet bool

	// hugepages/hugepagesSet are --hugepages: back guest memory with 2M huge
	// pages. hugepagesSet records whether the flag was given at all, so
	// --hugepages=false on create is still a no-label request rather than an
	// instruction to store something.
	hugepages    bool
	hugepagesSet bool

	// numaNode/numaNodeSet are --numa-node: the host NUMA nodes to bind the
	// VM's memory to (a Linux cpulist). numaNodeSet records whether the flag
	// was given at all, so an explicit value is never mistaken for "unset".
	numaNode    string
	numaNodeSet bool

	// autoscale is --autoscale: the replica bounds and target the manager loop
	// keeps the service between, stored as the swarmcracker.autoscale.* service
	// labels. An empty value is no policy at all; clearing one is an update.
	autoscale string

	// withRegistryAuth is --with-registry-auth (#44). registryAuth holds the
	// resolved base64 X-Registry-Auth payload for the image; createService fills
	// it in (resolving it is I/O), so this mapping function stays pure.
	withRegistryAuth bool
	registryAuth     string

	// networks are the user-defined network names/IDs to attach the service to;
	// networkTargets holds the resolved IDs.
	networks       []string
	networkTargets []string

	// secrets/configs are --secret/--config values, resolved in createService.
	secrets []string
	configs []string

	// Guest overrides honored by the executor (applied by the guest init).
	hostname string
	dns      []string

	// Container-execution flags. user/capAdd/capDrop/readOnly are rejected with
	// a clear error (the microVM executor cannot enforce them); hostname/dns
	// are honored.
	user     string
	capAdd   []string
	capDrop  []string
	readOnly bool

	// Container healthcheck flags (#39). health carries the flag values plus a
	// per-flag "explicitly set" bit (from cobra's Flags().Changed), so an
	// explicit zero is rejected rather than silently replaced by a default.
	health healthOptions

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

	// Container-execution flags: reject the ones the microVM executor cannot
	// enforce, so no flag is ever accepted-but-ignored.
	if err := validateUnsupportedExecFlags(o.user, o.capAdd, o.capDrop, o.readOnly); err != nil {
		return nil, err
	}
	hostname, err := buildHostname(o.hostname)
	if err != nil {
		return nil, err
	}
	dns, err := buildDNS(o.dns)
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

	// A memory-balloon target travels as the swarmcracker.balloon service label,
	// which the executor copies onto each task. Validate it here — against the
	// memory the VM will get — so an invalid request fails before any RPC, and
	// an unset flag adds nothing at all.
	if value, err := balloonLabelValue(int64(o.balloonMib), o.balloonSet, serviceMemoryMib(memoryBytes, nil)); err != nil {
		return nil, err
	} else if value != "" {
		if svcLabels == nil {
			svcLabels = make(map[string]string)
		}
		// A user-supplied label for the same key is the lower-level way to
		// request a balloon; honouring it silently would drop the flag. Accept
		// it only when it agrees. The check is presence-based, not value-based:
		// a label explicitly set to the empty value is still an explicit
		// request, and the flag must not silently overwrite it.
		if existing, ok := svcLabels[types.BalloonLabel]; ok && existing != value {
			return nil, fmt.Errorf("--balloon-mib %d conflicts with the %s label %q already set via --label", o.balloonMib, types.BalloonLabel, existing)
		}
		svcLabels[types.BalloonLabel] = value
	}

	// A cpuset pin travels as the swarmcracker.cpuset service label, which the
	// executor copies onto each task and the cgroup applies. Validate the list
	// here so a malformed request fails before any RPC, and store the canonical
	// form so what the CLI reports and what the daemon parses never differ. An
	// unset flag adds nothing at all.
	if o.cpusetSet {
		value, err := canonicalCpuset(o.cpuset)
		if err != nil {
			return nil, err
		}
		if svcLabels == nil {
			svcLabels = make(map[string]string)
		}
		// Mirror --balloon-mib: --label is the lower-level way to request the
		// same pin, so accept a user-supplied label only when it agrees —
		// honouring one silently would drop the flag. Presence-based, so a
		// label explicitly set to the empty value is still a request.
		if existing, ok := svcLabels[types.CpusetLabel]; ok && existing != value {
			return nil, fmt.Errorf("--cpuset %q conflicts with the %s label %q already set via --label", o.cpuset, types.CpusetLabel, existing)
		}
		svcLabels[types.CpusetLabel] = value
	}

	// Hugepages and NUMA placement travel as service labels too: the executor
	// copies them onto each task, where the translator turns hugepages into the
	// Firecracker machine-config and the cgroup turns the nodelist into
	// cpuset.mems. Hugepages is a boolean request — the only stored form is
	// "true" — so only an opt-in adds a label. A false flag (the default, or an
	// explicit --hugepages=false) is not a request at all.
	if o.hugepagesSet {
		// Mirror --balloon-mib/--cpuset/--numa-node: --label is the lower-level
		// way to request the same backing, so accept a user-supplied label only
		// when it agrees — honouring one silently would drop the flag, and an
		// explicit --hugepages=false must not leave an enabled label behind.
		// The comparison is semantic (types.ParseHugepages), because "2M" is a
		// synonym for "true" and "false"/"0"/"none" all mean 4K memory; a
		// label that cannot be read is a conflict too, never an override.
		if existing, ok := svcLabels[types.HugepagesLabel]; ok {
			requested, err := types.ParseHugepages(existing)
			if err != nil || requested != o.hugepages {
				return nil, fmt.Errorf("--hugepages=%t conflicts with the %s label %q already set via --label", o.hugepages, types.HugepagesLabel, existing)
			}
		}
		if o.hugepages {
			if svcLabels == nil {
				svcLabels = make(map[string]string)
			}
			svcLabels[types.HugepagesLabel] = "true"
		}
	}

	// --numa-node is validated here, like --cpuset, so a malformed list fails
	// before any RPC, and stored canonically so what the CLI reports and what
	// the daemon parses never differ.
	if o.numaNodeSet {
		value, err := canonicalCPUSet("--numa-node", o.numaNode)
		if err != nil {
			return nil, err
		}
		if svcLabels == nil {
			svcLabels = make(map[string]string)
		}
		// Mirror --cpuset: --label is the lower-level way to request the same
		// binding, so accept a user-supplied label only when it agrees.
		if existing, ok := svcLabels[types.NumaLabel]; ok && existing != value {
			return nil, fmt.Errorf("--numa-node %q conflicts with the %s label %q already set via --label", o.numaNode, types.NumaLabel, existing)
		}
		svcLabels[types.NumaLabel] = value
	}

	// An autoscale policy travels as the six swarmcracker.autoscale.* service
	// labels, which the manager loop reads. Validate it here so a malformed
	// policy fails before any RPC, and store the canonical labels so what the
	// CLI reports and what the loop reads never differ. An empty value is no
	// request at all on create (clearing belongs to `service update`).
	if strings.TrimSpace(o.autoscale) != "" {
		policy, err := types.ParseAutoscaleFlag(o.autoscale)
		if err != nil {
			return nil, fmt.Errorf("invalid --autoscale: %w", err)
		}
		// --label is the lower-level way to declare the same policy, so it is
		// accepted only when it agrees. The check is semantic: the labels the
		// user supplied replace the flag's on top of an otherwise complete set,
		// then the whole set is parsed, so "70" and "70.0" name the same target
		// while a value no policy could hold is a conflict rather than an
		// override. A partial label set is fine here — the flag fills it in,
		// and the merged six labels are what gets stored.
		if hasAutoscaleLabels(svcLabels) {
			merged := policy.Labels()
			for _, key := range types.AutoscaleLabelKeys() {
				if value, ok := svcLabels[key]; ok {
					merged[key] = value
				}
			}
			declared, err := types.AutoscaleFromLabels(merged)
			if err != nil {
				return nil, fmt.Errorf("--autoscale %q conflicts with the autoscale labels already set via --label (%s): %w",
					o.autoscale, describeAutoscaleLabels(svcLabels), err)
			}
			if declared == nil || *declared != policy {
				return nil, fmt.Errorf("--autoscale %q conflicts with the autoscale labels already set via --label (%s)",
					o.autoscale, describeAutoscaleLabels(svcLabels))
			}
		}
		if svcLabels == nil {
			svcLabels = make(map[string]string)
		}
		for key, value := range policy.Labels() {
			svcLabels[key] = value
		}
	}

	if svcLabels[types.GoldenLabel] != "" && (hostname != "" || len(dns) > 0) {
		return nil, fmt.Errorf("--hostname/--dns are not supported with --golden: golden images boot their own init and do not use the OCI init wrapper")
	}
	if svcLabels[types.GoldenLabel] != "" && (len(o.command) > 0 || len(o.args) > 0) {
		return nil, fmt.Errorf("--command/--args are not supported with --golden: golden images boot their own init and do not use the OCI init wrapper")
	}

	// The command and args travel to the guest on the kernel command line, so
	// values it cannot carry are rejected here instead of being altered at boot.
	if err := types.ValidateArgv("--command", o.command); err != nil {
		return nil, err
	}
	if err := types.ValidateArgv("--args", o.args); err != nil {
		return nil, err
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
					Image:    o.image,
					Env:      o.env,
					Command:  o.command,
					Args:     o.args,
					Hostname: hostname,
				},
			},
		},
	}

	if len(dns) > 0 {
		spec.Task.GetContainer().DNSConfig = &api.ContainerSpec_DNSConfig{Nameservers: dns}
	}

	// --with-registry-auth: the payload is already resolved (createService does
	// the keychain I/O), so it is copied onto the container verbatim. Empty means
	// the workers fall back to their own configured credentials.
	if o.registryAuth != "" {
		spec.Task.GetContainer().PullOptions = &api.ContainerSpec_PullOptions{RegistryAuth: o.registryAuth}
	}

	if hc, err := buildHealthcheck(o.health); err != nil {
		return nil, err
	} else if hc != nil {
		spec.Task.GetContainer().Healthcheck = hc
	}

	if mounts, err := buildMounts(o.mounts, o.volumes); err != nil {
		return nil, err
	} else if len(mounts) > 0 {
		spec.Task.GetContainer().Mounts = mounts
	}

	if len(o.networkTargets) > 0 {
		spec.Task.Networks = buildNetworkAttachments(o.networkTargets)
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

	// Registry credentials (#44). withRegistryAuth is the --with-registry-auth
	// value; withRegistryAuthSet records whether the flag was given at all, so
	// an absent flag preserves the credential already on the service while an
	// explicit --with-registry-auth=false clears it. registryAuth holds the
	// already-resolved base64 X-Registry-Auth payload (updateService does the
	// keychain I/O), keeping this mapping pure.
	withRegistryAuth    bool
	withRegistryAuthSet bool
	registryAuth        string

	mounts    []string
	volumes   []string
	mountsSet bool

	// balloonMib/balloonSet are --balloon-mib on `service update`: tri-state, so
	// absent preserves the service's current label, a value sets it, and an
	// explicit 0 removes it (see updateServiceWithClient).
	balloonMib int
	balloonSet bool

	// cpuset/cpusetSet are --cpuset on `service update`: tri-state, so absent
	// preserves the service's current label, a value pins the VM to those host
	// CPUs (stored canonically), and an explicit empty value removes the pin
	// (see updateServiceWithClient).
	cpuset    string
	cpusetSet bool

	// hugepages/hugepagesSet and numaNode/numaNodeSet are --hugepages and
	// --numa-node on `service update`, both tri-state like --cpuset: absent
	// preserves the service's current label, a value replaces it, and an
	// explicit --hugepages=false / --numa-node "" removes it (see
	// updateServiceWithClient).
	hugepages    bool
	hugepagesSet bool
	numaNode     string
	numaNodeSet  bool

	// autoscale/autoscaleSet are --autoscale on `service update`, tri-state
	// like --cpuset: absent preserves the declared policy, a value replaces it,
	// and an explicit empty value clears every autoscale label.
	autoscale    string
	autoscaleSet bool

	networks    []string
	networksSet bool

	secrets    []string
	secretsSet bool
	configs    []string
	configsSet bool

	hostname    string
	dns         []string
	hostnameSet bool
	dnsSet      bool
	user        string
	capAdd      []string
	capDrop     []string
	readOnly    bool

	// Container healthcheck (#39). health carries the flag values plus a
	// per-flag "explicitly set" bit; a health flag without --health-cmd is
	// rejected.
	health healthOptions

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

// registryAuthForUpdate is the pure decision behind `service update
// --with-registry-auth` (#44). It maps the flag's set bit and value to what the
// updated spec should carry:
//
//   - the flag was absent (set=false): return apply=false so the credential
//     copied onto the spec from the current service is preserved untouched;
//   - the flag was given true (set=true, value=true): return the
//     already-resolved payload with apply=true so it replaces the current
//     credential;
//   - the flag was given false (set=true, value=false): return an empty payload
//     with apply=true so the credential is cleared.
//
// resolved is ignored unless the flag was given true; callers resolve it from
// the keychain before any RPC so a missing credential fails fast.
func registryAuthForUpdate(set, value bool, resolved string) (payload string, apply bool) {
	if !set {
		return "", false
	}
	if !value {
		return "", true
	}
	return resolved, true
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

// defaultServiceMemoryMib is the memory size (MiB) a service's VM gets when no
// --memory is declared — the same default the daemon's translator applies
// (pkg/swarmkit/translator.go via pkg/types.ResolveVMSize). It is a default,
// not a floor: an explicit --memory below it is honoured as declared, because
// the daemon no longer applies a floor either.
const defaultServiceMemoryMib = 512

// serviceMemoryMib resolves the effective VM memory size in MiB that
// --balloon-mib is validated against. A --memory declared by this invocation
// (already parsed to bytes) replaces Limits, so it wins when whole-MiB; when
// none was declared the size is resolved from the supplied task resources
// (res) exactly as the daemon does, via pkg/types.ResolveVMSize
// (Limits -> Reservations -> defaultServiceMemoryMib). Because both the command
// and the node resolve through the same helper, this path cannot accept a
// balloon target the node then refuses. A sub-MiB declared value keeps the
// default, matching ResolveVMSize. Callers with no existing spec (create) pass
// res == nil.
func serviceMemoryMib(memoryBytes int64, res *api.ResourceRequirements) int64 {
	if mib := memoryBytes / (1024 * 1024); mib > 0 {
		return mib
	}
	_, memMib := types.ResolveVMSize(vmResourceRequirements(res), 1, defaultServiceMemoryMib)
	return int64(memMib)
}

// vmResourceRequirements adapts a SwarmKit api.ResourceRequirements to the
// SwarmCracker types.ResourceRequirements that types.ResolveVMSize consumes, so
// the CLI and the daemon resolve a VM's size from the same two arms (Limits
// then Reservations) with no re-implementation to drift.
func vmResourceRequirements(res *api.ResourceRequirements) *types.ResourceRequirements {
	if res == nil {
		return nil
	}
	out := &types.ResourceRequirements{}
	if res.Limits != nil {
		out.Limits = &types.Resources{NanoCPUs: res.Limits.NanoCPUs, MemoryBytes: res.Limits.MemoryBytes}
	}
	if res.Reservations != nil {
		out.Reservations = &types.Resources{NanoCPUs: res.Reservations.NanoCPUs, MemoryBytes: res.Reservations.MemoryBytes}
	}
	return out
}

// balloonLabelValue validates --balloon-mib against the VM's memory size (MiB)
// and returns the value to carry as types.BalloonLabel. An unset flag returns ""
// so nothing is added and an unconfigured service stays byte-identical to
// before. A non-positive target, or one that is not smaller than the VM's
// memory, is an error naming the offending value: the balloon device is created
// before boot (it cannot be added to a running VM), so such a request can never
// be honoured and must not be accepted.
func balloonLabelValue(balloonMib int64, set bool, memMib int64) (string, error) {
	if !set {
		return "", nil
	}
	if balloonMib <= 0 {
		return "", fmt.Errorf("invalid --balloon-mib %d: must be a positive integer (MiB)", balloonMib)
	}
	if memMib > 0 && balloonMib >= memMib {
		return "", fmt.Errorf("invalid --balloon-mib %d: must be less than the VM memory size %d MiB", balloonMib, memMib)
	}
	return strconv.FormatInt(balloonMib, 10), nil
}

// canonicalCPUSet validates a Linux cpulist for the named flag and returns the
// canonical form to store (ranges ascending, overlaps/adjacencies merged, no
// leading zeros). --cpuset and --numa-node accept the same syntax, so both
// share this one parser and canonicaliser instead of duplicating it: the CLI,
// the daemon and the jailer then all parse the same string. A malformed or
// empty value is an error naming the flag and the offending value: the request
// would otherwise be stored and only fail, or silently mismatch, at VM start,
// so it must be rejected before any RPC.
func canonicalCPUSet(flag, value string) (string, error) {
	canonical, err := jailer.ParseCPUSet(value)
	if err != nil {
		return "", fmt.Errorf("%s %q: %w", flag, value, err)
	}
	return canonical, nil
}

// canonicalCpuset is canonicalCPUSet for --cpuset. It keeps the cpuset call
// sites reading as a cpuset operation while sharing the cpulist parser with
// --numa-node.
func canonicalCpuset(value string) (string, error) {
	return canonicalCPUSet("--cpuset", value)
}

// hasAutoscaleLabels reports whether any autoscale label is present, so a
// conflict check runs for a half-written policy too instead of only for a
// complete one that AutoscaleFromLabels would accept.
func hasAutoscaleLabels(labels map[string]string) bool {
	for _, key := range types.AutoscaleLabelKeys() {
		if _, ok := labels[key]; ok {
			return true
		}
	}
	return false
}

// describeAutoscaleLabels renders the autoscale labels a caller supplied via
// --label, in the label table's order, for a conflict error that names both
// the flag value and what it disagrees with.
func describeAutoscaleLabels(labels map[string]string) string {
	parts := make([]string, 0, len(types.AutoscaleLabelKeys()))
	for _, key := range types.AutoscaleLabelKeys() {
		if value, ok := labels[key]; ok {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, ", ")
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

// Docker-compatible healthcheck defaults, applied only when --health-cmd is
// declared (see buildHealthcheck).
const (
	defaultHealthInterval = 30 * time.Second
	defaultHealthTimeout  = 30 * time.Second
	defaultHealthRetries  = 3
	maxHealthRetries      = 1<<31 - 1
)

// healthOptions carries the container-healthcheck flag values together with a
// per-flag "explicitly set" bit. The bit comes from cobra's Flags().Changed in
// the command layer; keeping it in a plain struct lets buildHealthcheck stay a
// pure, cobra-free function. Without it, an explicit zero (--health-retries 0,
// --health-interval 0s) would be indistinguishable from "unset" and silently
// rewritten to a Docker default.
type healthOptions struct {
	cmd            string
	cmdSet         bool
	interval       string
	intervalSet    bool
	timeout        string
	timeoutSet     bool
	retries        int
	retriesSet     bool
	startPeriod    string
	startPeriodSet bool
}

// set reports whether any --health-* flag was supplied explicitly, including
// --health-cmd itself.
func (h healthOptions) set() bool {
	return h.cmdSet || h.intervalSet || h.timeoutSet || h.retriesSet || h.startPeriodSet
}

// buildHealthcheck maps the --health-* options onto an api.HealthConfig. It
// returns (nil, nil) when no healthcheck is requested, so an absent --health-cmd
// leaves the spec field nil and downstream behaviour is unchanged. Any other
// --health-* flag supplied without --health-cmd is an error, so a flag is never
// accepted and ignored.
func buildHealthcheck(h healthOptions) (*api.HealthConfig, error) {
	// A non-empty value counts as set even if the caller forgot the bit, so a
	// value is never silently ignored. An explicit zero still depends on the
	// bit (Changed-based detection), which is the whole point of the split.
	if !h.intervalSet {
		h.intervalSet = strings.TrimSpace(h.interval) != ""
	}
	if !h.timeoutSet {
		h.timeoutSet = strings.TrimSpace(h.timeout) != ""
	}
	if !h.startPeriodSet {
		h.startPeriodSet = strings.TrimSpace(h.startPeriod) != ""
	}
	if !h.retriesSet {
		h.retriesSet = h.retries != 0
	}

	cmd := strings.TrimSpace(h.cmd)
	if cmd == "" {
		if h.set() {
			return nil, fmt.Errorf("--health-cmd is required when any other --health-* flag is set")
		}
		return nil, nil
	}

	iv, err := healthDuration("--health-interval", h.interval, h.intervalSet, defaultHealthInterval, true)
	if err != nil {
		return nil, err
	}
	to, err := healthDuration("--health-timeout", h.timeout, h.timeoutSet, defaultHealthTimeout, true)
	if err != nil {
		return nil, err
	}
	sp, err := healthDuration("--health-start-period", h.startPeriod, h.startPeriodSet, 0, false)
	if err != nil {
		return nil, err
	}

	retries := defaultHealthRetries
	if h.retriesSet {
		if h.retries < 1 {
			return nil, fmt.Errorf("--health-retries must be at least 1 (got %d)", h.retries)
		}
		if h.retries > maxHealthRetries {
			return nil, fmt.Errorf("--health-retries must be at most %d (got %d)", maxHealthRetries, h.retries)
		}
		retries = h.retries
	}

	return &api.HealthConfig{
		Test:        []string{"CMD-SHELL", cmd},
		Interval:    gogotypes.DurationProto(iv),
		Timeout:     gogotypes.DurationProto(to),
		Retries:     int32(retries),
		StartPeriod: gogotypes.DurationProto(sp),
	}, nil
}

// healthDuration parses a healthcheck duration flag. When the flag was not set
// explicitly it falls back to def; an explicit value must parse and be positive
// (interval/timeout) or non-negative (start-period), naming the flag so the
// error is actionable.
func healthDuration(flag, raw string, set bool, def time.Duration, positive bool) (time.Duration, error) {
	if !set {
		return def, nil
	}
	raw = strings.TrimSpace(raw)
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q", flag, raw)
	}
	if positive && d <= 0 {
		return 0, fmt.Errorf("%s must be greater than 0", flag)
	}
	if !positive && d < 0 {
		return 0, fmt.Errorf("%s must not be negative", flag)
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

// buildNetworkAttachments converts resolved network IDs into SwarmKit task
// network attachments.
func buildNetworkAttachments(targets []string) []*api.NetworkAttachmentConfig {
	out := make([]*api.NetworkAttachmentConfig, 0, len(targets))
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out = append(out, &api.NetworkAttachmentConfig{Target: t})
	}
	return out
}

// secretSpec is a parsed --secret / --config flag value. Both flags share the
// same syntax: [src=]NAME[,target=PATH][,mode=0400][,uid=N][,gid=N].
type secretSpec struct {
	Source string
	Target string
	Mode   os.FileMode
	UID    string
	GID    string
}

// parseSecretSpec parses a --secret / --config value.
func parseSecretSpec(raw string) (secretSpec, error) {
	spec := secretSpec{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return spec, fmt.Errorf("empty secret/config value")
	}

	// A bare value is the source name/ID.
	if !strings.Contains(raw, "=") {
		spec.Source = raw
		return spec, nil
	}

	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return spec, fmt.Errorf("invalid component %q (want key=value)", part)
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		switch key {
		case "src", "source":
			spec.Source = val
		case "target", "dst", "destination":
			spec.Target = val
		case "mode":
			m, err := strconv.ParseUint(val, 8, 32)
			if err != nil {
				return spec, fmt.Errorf("invalid mode %q: %w", val, err)
			}
			spec.Mode = os.FileMode(m)
		case "uid":
			spec.UID = val
		case "gid":
			spec.GID = val
		default:
			return spec, fmt.Errorf("unknown key %q (want src, target, mode, uid, gid)", key)
		}
	}

	if spec.Source == "" {
		return spec, fmt.Errorf("requires src=<name>")
	}
	return spec, nil
}

// buildMounts converts --mount and --volume specs into SwarmKit mounts.
func buildMounts(mounts, volumes []string) ([]api.Mount, error) {
	out := make([]api.Mount, 0, len(mounts)+len(volumes))
	for _, raw := range mounts {
		m, err := parseMountSpec(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	for _, raw := range volumes {
		m, err := parseVolumeSpec(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// parseMountSpec parses Docker's long form:
// type=volume|bind,source=<src>,target=<path>[,readonly[=true|false]].
func parseMountSpec(raw string) (api.Mount, error) {
	opts := map[string]string{}
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		k, v, _ := strings.Cut(f, "=")
		opts[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	typ := strings.ToLower(opts["type"])
	if typ == "" {
		typ = "volume"
	}
	src := opts["source"]
	if src == "" {
		src = opts["src"]
	}
	target := opts["target"]
	if target == "" {
		target = opts["destination"]
	}
	if src == "" {
		return api.Mount{}, fmt.Errorf("invalid --mount %q: source is required", raw)
	}
	ro := false
	if v, ok := opts["readonly"]; ok {
		ro = v == "" || v == "true" || v == "1"
	}
	return buildMount(typ, src, target, ro, raw)
}

// parseVolumeSpec parses Docker's short form: <src>:<dst>[:ro|rw].
func parseVolumeSpec(raw string) (api.Mount, error) {
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return api.Mount{}, fmt.Errorf("invalid --volume %q (want src:dst[:ro|rw])", raw)
	}
	src := strings.TrimSpace(parts[0])
	target := strings.TrimSpace(parts[1])
	ro := false
	if len(parts) == 3 {
		opt := strings.ToLower(strings.TrimSpace(parts[2]))
		if opt != "ro" && opt != "rw" {
			return api.Mount{}, fmt.Errorf("invalid --volume option %q (want ro or rw)", parts[2])
		}
		ro = opt == "ro"
	}
	if src == "" || target == "" {
		return api.Mount{}, fmt.Errorf("invalid --volume %q (want src:dst[:ro|rw])", raw)
	}
	typ := "volume"
	if strings.HasPrefix(src, "/") || strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
		typ = "bind"
	}
	return buildMount(typ, src, target, ro, raw)
}

func buildMount(typ, src, target string, ro bool, raw string) (api.Mount, error) {
	if err := validateMountTarget(target); err != nil {
		return api.Mount{}, fmt.Errorf("invalid mount target %q: %w", target, err)
	}
	switch typ {
	case "volume":
		if !isValidVolumeName(src) {
			return api.Mount{}, fmt.Errorf("invalid --mount %q: invalid volume name %q", raw, src)
		}
		return api.Mount{Type: api.MountTypeVolume, Source: "volume://" + src, Target: target, ReadOnly: ro}, nil
	case "bind":
		if !strings.HasPrefix(src, "/") {
			return api.Mount{}, fmt.Errorf("invalid --mount %q: bind source must be an absolute host path", raw)
		}
		return api.Mount{Type: api.MountTypeBind, Source: src, Target: target, ReadOnly: ro}, nil
	case "tmpfs":
		return api.Mount{}, fmt.Errorf("invalid --mount %q: tmpfs mounts are not supported", raw)
	default:
		return api.Mount{}, fmt.Errorf("invalid --mount %q: unsupported type %q (want volume or bind)", raw, typ)
	}
}

func validateMountTarget(target string) error {
	if target == "" {
		return fmt.Errorf("target cannot be empty")
	}
	if !strings.HasPrefix(target, "/") {
		return fmt.Errorf("target must be an absolute path")
	}
	if strings.Contains(target, "..") {
		return fmt.Errorf("target must not contain '..'")
	}
	return nil
}

func isValidVolumeName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '_' || r == '.' || r == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

// validateUnsupportedExecFlags rejects container-execution flags that the
// microVM executor cannot enforce. Rejecting them here guarantees a flag is
// never accepted and then silently ignored.
func validateUnsupportedExecFlags(user string, capAdd, capDrop []string, readOnly bool) error {
	switch {
	case strings.TrimSpace(user) != "":
		return fmt.Errorf("--user is not supported: the microVM guest runs the workload as root (per-task user switching needs a container runtime)")
	case len(capAdd) > 0:
		return fmt.Errorf("--cap-add is not supported: the microVM guest has no capability bounding set to grant")
	case len(capDrop) > 0:
		return fmt.Errorf("--cap-drop is not supported: the microVM guest has no capability bounding set to drop")
	case readOnly:
		return fmt.Errorf("--read-only is not supported: the microVM root filesystem is writable and has no overlay; use a read-only --mount for individual paths")
	}
	return nil
}

// buildHostname validates and normalizes a --hostname value (RFC 1123 labels).
func buildHostname(h string) (string, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return "", nil
	}
	if len(h) > 253 {
		return "", fmt.Errorf("invalid --hostname %q: longer than 253 characters", h)
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return "", fmt.Errorf("invalid --hostname %q: each dot-separated label must be 1-63 characters", h)
		}
		for i, r := range label {
			isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			isHyphen := r == '-' && i != 0 && i != len(label)-1
			if !isAlnum && !isHyphen {
				return "", fmt.Errorf("invalid --hostname %q: %q is not allowed", h, string(r))
			}
		}
	}
	return h, nil
}

// buildDNS validates a list of --dns nameservers.
func buildDNS(dns []string) ([]string, error) {
	if len(dns) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(dns))
	for _, d := range dns {
		d = strings.TrimSpace(d)
		if net.ParseIP(d) == nil {
			return nil, fmt.Errorf("invalid --dns %q: not an IP address", d)
		}
		out = append(out, d)
	}
	return out, nil
}
