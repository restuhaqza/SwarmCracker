package types

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Autoscale service labels. A policy is declared on a service as a set of
// labels — like every other SwarmCracker service knob — so it survives a
// `service update` that does not touch it and is readable with `service
// inspect`. The manager loop that acts on the policy reads exactly these keys.
const (
	// AutoscaleLabelPrefix is the namespace every autoscale label lives in. It
	// is a constant of its own because it is also the typo boundary: a label
	// under this prefix that is not one of the six below is a policy the loop
	// cannot read, and saying so beats treating a misspelled policy as "no
	// policy" on a service that looks configured.
	AutoscaleLabelPrefix = "swarmcracker.autoscale."
	// AutoscaleMinLabel is the lower replica bound, e.g. "2".
	AutoscaleMinLabel = AutoscaleLabelPrefix + "min"
	// AutoscaleMaxLabel is the upper replica bound, e.g. "10".
	AutoscaleMaxLabel = AutoscaleLabelPrefix + "max"
	// AutoscaleMetricLabel names the metric to scale on; "cpu" is the only
	// metric in this cut.
	AutoscaleMetricLabel = AutoscaleLabelPrefix + "metric"
	// AutoscaleTargetLabel is the target as a percentage of one vCPU, e.g.
	// "70". It is strictly greater than 0 and at most 100.
	AutoscaleTargetLabel = AutoscaleLabelPrefix + "target"
	// AutoscaleCooldownUpLabel is the minimum time between two scale-ups, e.g.
	// "60s". Optional; absent means DefaultAutoscaleCooldownUp.
	AutoscaleCooldownUpLabel = AutoscaleLabelPrefix + "cooldown_up"
	// AutoscaleCooldownDownLabel is the minimum time between two scale-downs,
	// e.g. "5m". Optional; absent means DefaultAutoscaleCooldownDown.
	AutoscaleCooldownDownLabel = AutoscaleLabelPrefix + "cooldown_down"
)

const (
	// AutoscaleMetricCPU scales on the vCPU utilisation reported by the guest.
	// It is the only metric in this cut.
	AutoscaleMetricCPU = "cpu"

	// DefaultAutoscaleCooldownUp is how long a policy waits after a scale-up
	// before another one; it absorbs the guest sampling cadence (15s) and the
	// boot time of the new replica.
	DefaultAutoscaleCooldownUp = 60 * time.Second

	// DefaultAutoscaleCooldownDown is how long a policy waits after a
	// scale-down. It is deliberately longer than the scale-up cooldown: a
	// replica removed too eagerly costs a boot, and load spikes are usually
	// shorter than the resulting drop.
	DefaultAutoscaleCooldownDown = 300 * time.Second
)

// AutoscalePolicy is a parsed autoscale declaration: keep the replica count
// between Min and Max, aiming for Target percent CPU utilisation, and wait at
// least CooldownUp/CooldownDown between two scaling actions in that direction.
type AutoscalePolicy struct {
	Min, Max     int
	Metric       string        // AutoscaleMetricCPU ("cpu")
	Target       float64       // percent of one vCPU, e.g. 70
	CooldownUp   time.Duration // default DefaultAutoscaleCooldownUp
	CooldownDown time.Duration // default DefaultAutoscaleCooldownDown
}

// Short key names of the flag grammar. They are internal: the labels above are
// the stored form, these are what `--autoscale` accepts.
const (
	autoscaleKeyMin          = "min"
	autoscaleKeyMax          = "max"
	autoscaleKeyMetric       = "metric"
	autoscaleKeyTarget       = "target"
	autoscaleKeyCooldownUp   = "cooldown-up"
	autoscaleKeyCooldownDown = "cooldown-down"
)

// autoscaleRequiredKeys are the keys every policy must declare, in the order
// the "missing" error names them. The cooldowns are optional.
var autoscaleRequiredKeys = []string{
	autoscaleKeyMin,
	autoscaleKeyMax,
	autoscaleKeyMetric,
	autoscaleKeyTarget,
}

// autoscaleLabelSpecs maps a stored label to its short key, so reading a
// policy out of labels and clearing every label use the same table.
var autoscaleLabelSpecs = []struct {
	label string
	key   string
}{
	{AutoscaleMinLabel, autoscaleKeyMin},
	{AutoscaleMaxLabel, autoscaleKeyMax},
	{AutoscaleMetricLabel, autoscaleKeyMetric},
	{AutoscaleTargetLabel, autoscaleKeyTarget},
	{AutoscaleCooldownUpLabel, autoscaleKeyCooldownUp},
	{AutoscaleCooldownDownLabel, autoscaleKeyCooldownDown},
}

// AutoscaleLabelKeys returns every label a policy is stored as. It is the list
// a caller removes to clear a policy, and it is returned fresh so a caller
// cannot reorder the package's table.
func AutoscaleLabelKeys() []string {
	keys := make([]string, 0, len(autoscaleLabelSpecs))
	for _, spec := range autoscaleLabelSpecs {
		keys = append(keys, spec.label)
	}
	return keys
}

// unknownAutoscaleLabels returns the labels under AutoscaleLabelPrefix that are
// not part of the policy grammar, sorted so the same typo always produces the
// same error (map order is not stable, and an error that reorders between passes
// reads like a moving target).
func unknownAutoscaleLabels(labels map[string]string) []string {
	var unknown []string
	for key := range labels {
		if !strings.HasPrefix(key, AutoscaleLabelPrefix) || isAutoscaleLabel(key) {
			continue
		}
		unknown = append(unknown, key)
	}
	sort.Strings(unknown)
	return unknown
}

// isAutoscaleLabel reports whether key is one of the six stored policy labels.
func isAutoscaleLabel(key string) bool {
	for _, spec := range autoscaleLabelSpecs {
		if spec.label == key {
			return true
		}
	}
	return false
}

// quoteAutoscaleKeys renders label keys one per quoted item, so an error naming
// two typos cannot read as one long key.
func quoteAutoscaleKeys(keys []string) string {
	quoted := make([]string, 0, len(keys))
	for _, key := range keys {
		quoted = append(quoted, strconv.Quote(key))
	}
	return strings.Join(quoted, ", ")
}

// autoscaleFlagForm is the valid --autoscale form, quoted in errors so a
// mistyped policy reports what it should have looked like.
const autoscaleFlagForm = "min=<n>,max=<n>,metric=cpu,target=<percent>[,cooldown-up=<dur>][,cooldown-down=<dur>]"

// autoscaleNames is how one input form (flag keys or service labels) is named
// in errors. The same validation produces both spellings, so a policy the flag
// accepts is exactly one the labels accept.
type autoscaleNames struct {
	min, max, metric, target, cooldownUp, cooldownDown string
	// form is what the "missing keys" error says a complete policy looks like.
	form string
	// bareMetric keeps the metric error byte-identical to the documented flag
	// grammar message; the label form prefixes the offending label instead.
	bareMetric bool
}

var (
	autoscaleFlagNames = autoscaleNames{
		min:          autoscaleKeyMin,
		max:          autoscaleKeyMax,
		metric:       autoscaleKeyMetric,
		target:       autoscaleKeyTarget,
		cooldownUp:   autoscaleKeyCooldownUp,
		cooldownDown: autoscaleKeyCooldownDown,
		form:         autoscaleFlagForm,
		bareMetric:   true,
	}
	autoscaleLabelNames = autoscaleNames{
		min:          AutoscaleMinLabel,
		max:          AutoscaleMaxLabel,
		metric:       AutoscaleMetricLabel,
		target:       AutoscaleTargetLabel,
		cooldownUp:   AutoscaleCooldownUpLabel,
		cooldownDown: AutoscaleCooldownDownLabel,
		form:         "the min, max, metric and target labels are all required",
	}
)

// ParseAutoscaleFlag parses the --autoscale value:
//
//	min=<n>,max=<n>,metric=cpu,target=<percent>[,cooldown-up=<dur>][,cooldown-down=<dur>]
//
// Keys may appear in any order and whitespace around a component is ignored.
// Every bound is validated (min >= 1, max >= min, 0 < target <= 100) and every
// error names the offending key and the valid form, so a typo fails at the
// command that made it rather than at the manager loop that consumes it.
func ParseAutoscaleFlag(value string) (AutoscalePolicy, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return AutoscalePolicy{}, fmt.Errorf("empty autoscale policy (want %s)", autoscaleFlagForm)
	}

	values := make(map[string]string, len(autoscaleRequiredKeys)+2)
	for _, component := range strings.Split(value, ",") {
		component = strings.TrimSpace(component)
		key, raw, ok := strings.Cut(component, "=")
		if !ok {
			return AutoscalePolicy{}, fmt.Errorf("invalid autoscale component %q (want key=value)", component)
		}
		key = strings.TrimSpace(key)
		raw = strings.TrimSpace(raw)
		// The key must be known before it can be a duplicate: reporting a
		// repeated typo as "duplicate" would send the operator looking for a
		// key that does not exist.
		if !isAutoscaleKey(key) {
			return AutoscalePolicy{}, fmt.Errorf(
				"unknown autoscale key %q (want %s)", key,
				strings.Join([]string{
					autoscaleKeyMin, autoscaleKeyMax, autoscaleKeyMetric,
					autoscaleKeyTarget, autoscaleKeyCooldownUp, autoscaleKeyCooldownDown,
				}, ", "))
		}
		if _, dup := values[key]; dup {
			return AutoscalePolicy{}, fmt.Errorf(
				"duplicate autoscale key %q (want each key at most once, e.g. %s)",
				key, autoscaleFlagForm)
		}
		values[key] = raw
	}

	return autoscalePolicyFromValues(values, autoscaleFlagNames)
}

// AutoscaleFromLabels returns the policy declared on a service, or nil when the
// service declares none. A service carrying only some of the required labels is
// an error naming the missing ones: a half-written policy must not silently
// leave autoscaling off while `service inspect` shows it as configured.
//
// A label under AutoscaleLabelPrefix that is not one of the six known keys is an
// error too, naming every offending key and the valid set. The flag grammar
// rejects an unknown key, so the label form must: otherwise a typo like
// `...autoscale.minn` leaves a service that looks configured and never scales,
// with nothing said about why.
func AutoscaleFromLabels(labels map[string]string) (*AutoscalePolicy, error) {
	if unknown := unknownAutoscaleLabels(labels); len(unknown) > 0 {
		// The check comes first: a typo key is not a value error, and reporting
		// it ahead of "missing min" explains the real problem instead of the
		// symptom (the misspelled key never contributed a value at all).
		noun := "label"
		if len(unknown) > 1 {
			noun = "labels"
		}
		return nil, fmt.Errorf("unknown autoscale %s %s (valid labels: %s)",
			noun, quoteAutoscaleKeys(unknown), strings.Join(AutoscaleLabelKeys(), ", "))
	}

	values := make(map[string]string, len(autoscaleLabelSpecs))
	for _, spec := range autoscaleLabelSpecs {
		if raw, ok := labels[spec.label]; ok {
			values[spec.key] = strings.TrimSpace(raw)
		}
	}
	if len(values) == 0 {
		return nil, nil
	}

	policy, err := autoscalePolicyFromValues(values, autoscaleLabelNames)
	if err != nil {
		return nil, err
	}
	return &policy, nil
}

// Labels returns the label set a policy is stored as. Labels and
// AutoscaleFromLabels round-trip: the stored form is the only form the manager
// loop reads, so a formatting difference here would silently change the policy.
func (p AutoscalePolicy) Labels() map[string]string {
	return map[string]string{
		AutoscaleMinLabel:          strconv.Itoa(p.Min),
		AutoscaleMaxLabel:          strconv.Itoa(p.Max),
		AutoscaleMetricLabel:       p.Metric,
		AutoscaleTargetLabel:       formatAutoscaleTarget(p.Target),
		AutoscaleCooldownUpLabel:   p.CooldownUp.String(),
		AutoscaleCooldownDownLabel: p.CooldownDown.String(),
	}
}

// String renders the policy in the --autoscale grammar, so a diagnostic that
// quotes a policy can be pasted straight back into the CLI.
func (p AutoscalePolicy) String() string {
	return fmt.Sprintf("min=%d,max=%d,metric=%s,target=%s,cooldown-up=%s,cooldown-down=%s",
		p.Min, p.Max, p.Metric, formatAutoscaleTarget(p.Target), p.CooldownUp, p.CooldownDown)
}

// autoscalePolicyFromValues validates one key/value set (flag or label) and
// builds the policy. Missing keys are reported first, so a policy cut short is
// never reported as "min is not a number" when min was not given at all.
func autoscalePolicyFromValues(values map[string]string, names autoscaleNames) (AutoscalePolicy, error) {
	missing := make([]string, 0, len(autoscaleRequiredKeys))
	for _, key := range autoscaleRequiredKeys {
		if values[key] == "" {
			missing = append(missing, nameForAutoscaleKey(names, key))
		}
	}
	if len(missing) > 0 {
		return AutoscalePolicy{}, fmt.Errorf(
			"incomplete autoscale policy: missing %s (want %s)",
			strings.Join(missing, ", "), names.form)
	}

	min, err := parseAutoscaleInt(nameForAutoscaleKey(names, autoscaleKeyMin), values[autoscaleKeyMin])
	if err != nil {
		return AutoscalePolicy{}, err
	}
	if min < 1 {
		return AutoscalePolicy{}, fmt.Errorf(
			"invalid autoscale %s %q: must be an integer >= 1",
			nameForAutoscaleKey(names, autoscaleKeyMin), values[autoscaleKeyMin])
	}

	max, err := parseAutoscaleInt(nameForAutoscaleKey(names, autoscaleKeyMax), values[autoscaleKeyMax])
	if err != nil {
		return AutoscalePolicy{}, err
	}
	if max < min {
		return AutoscalePolicy{}, fmt.Errorf(
			"invalid autoscale %s %q: must be greater than or equal to min (%d)",
			nameForAutoscaleKey(names, autoscaleKeyMax), values[autoscaleKeyMax], min)
	}

	metric := strings.ToLower(values[autoscaleKeyMetric])
	if metric != AutoscaleMetricCPU {
		err := fmt.Errorf("unsupported autoscale metric %q (supported: %s)", values[autoscaleKeyMetric], AutoscaleMetricCPU)
		if names.bareMetric {
			return AutoscalePolicy{}, err
		}
		return AutoscalePolicy{}, fmt.Errorf("%s: %w", nameForAutoscaleKey(names, autoscaleKeyMetric), err)
	}

	target, err := strconv.ParseFloat(values[autoscaleKeyTarget], 64)
	if err != nil || math.IsNaN(target) || target <= 0 || target > 100 {
		return AutoscalePolicy{}, fmt.Errorf(
			"invalid autoscale %s %q: must be a percentage greater than 0 and at most 100 (percent of one vCPU)",
			nameForAutoscaleKey(names, autoscaleKeyTarget), values[autoscaleKeyTarget])
	}

	cooldownUp := DefaultAutoscaleCooldownUp
	if raw := values[autoscaleKeyCooldownUp]; raw != "" {
		d, err := parseAutoscaleCooldown(nameForAutoscaleKey(names, autoscaleKeyCooldownUp), raw)
		if err != nil {
			return AutoscalePolicy{}, err
		}
		cooldownUp = d
	}

	cooldownDown := DefaultAutoscaleCooldownDown
	if raw := values[autoscaleKeyCooldownDown]; raw != "" {
		d, err := parseAutoscaleCooldown(nameForAutoscaleKey(names, autoscaleKeyCooldownDown), raw)
		if err != nil {
			return AutoscalePolicy{}, err
		}
		cooldownDown = d
	}

	return AutoscalePolicy{
		Min:          min,
		Max:          max,
		Metric:       metric,
		Target:       target,
		CooldownUp:   cooldownUp,
		CooldownDown: cooldownDown,
	}, nil
}

// parseAutoscaleInt parses a replica bound, naming the key and the required
// form so a value like "ten" is actionable rather than a bare strconv error.
func parseAutoscaleInt(name, raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid autoscale %s %q: must be an integer", name, raw)
	}
	return n, nil
}

// parseAutoscaleCooldown parses one cooldown. A zero duration is accepted —
// "no cooldown in this direction" is a meaningful request — but a negative one
// is rejected, since it can never mean anything.
func parseAutoscaleCooldown(name, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid autoscale %s %q: must be a duration, e.g. 60s or 5m", name, raw)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid autoscale %s %q: must not be negative", name, raw)
	}
	return d, nil
}

// nameForAutoscaleKey spells an internal key in the given input form, so the
// one validator reports flag keys in a flag error and label keys in a label
// error.
func nameForAutoscaleKey(names autoscaleNames, key string) string {
	switch key {
	case autoscaleKeyMin:
		return names.min
	case autoscaleKeyMax:
		return names.max
	case autoscaleKeyMetric:
		return names.metric
	case autoscaleKeyTarget:
		return names.target
	case autoscaleKeyCooldownUp:
		return names.cooldownUp
	case autoscaleKeyCooldownDown:
		return names.cooldownDown
	default:
		return key
	}
}

// isAutoscaleKey reports whether key is part of the flag grammar. An unknown
// key is rejected rather than ignored, so a misspelled "cdown-down" cannot be
// mistaken for a default.
func isAutoscaleKey(key string) bool {
	switch key {
	case autoscaleKeyMin, autoscaleKeyMax, autoscaleKeyMetric,
		autoscaleKeyTarget, autoscaleKeyCooldownUp, autoscaleKeyCooldownDown:
		return true
	default:
		return false
	}
}

// formatAutoscaleTarget prints a percentage without trailing zeros, so 70 does
// not become "70.000000" (which would still parse, but would make a stored
// policy differ from what the operator typed).
func formatAutoscaleTarget(target float64) string {
	return strconv.FormatFloat(target, 'f', -1, 64)
}
