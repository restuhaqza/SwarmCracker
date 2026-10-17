package metrics

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// This file is the guard that ties infrastructure/observability/prometheus/
// alerts.yml to the metrics this package actually registers. A rule pointed at
// a metric that does not exist can never fire — it is false confidence — so the
// test fails on any expr that names a series the default Prometheus registry
// does not carry.

// repoPath resolves a path relative to the repository root. Go runs a package's
// tests with the package directory as the working directory, so pkg/metrics is
// two levels below the root. A missing file fails loudly: a silently skipped
// guard is worse than no guard.
func repoPath(t *testing.T, rel string) string {
	t.Helper()

	p := filepath.Join("..", "..", rel)
	if _, err := os.Stat(p); err != nil {
		wd, _ := os.Getwd()
		t.Fatalf("cannot find %s (looked for %s relative to %s): %v", rel, p, wd, err)
	}
	return p
}

// ── PromQL metric-name extraction ─────────────────────────────────

var (
	reStringLiteral   = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	reModifierParens  = regexp.MustCompile(`\b(?:by|without|on|ignoring|group_left|group_right)\s*\([^()]*\)`)
	reLabelMatchers   = regexp.MustCompile(`\{[^{}]*\}`)
	reRangeSelector   = regexp.MustCompile(`\[[^\[\]]*\]`)
	reDurationLiteral = regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ms|s|m|h|d|w|y)\b`)
	reIdentifier      = regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*`)
	reFollowedByParen = regexp.MustCompile(`^\s*\(`)
)

// promQLKeywords are bare words that can appear in an expression but are not
// metric names.
var promQLKeywords = map[string]bool{
	"and": true, "or": true, "unless": true, "offset": true,
	"bool": true, "inf": true, "nan": true,
}

// metricNames returns the metric names an expression reads, sorted and without
// duplicates. Label matchers and their string values, range selectors and
// aggregation modifiers are stripped first; identifiers that are function calls
// (followed by "(") or PromQL keywords are not metric names.
func metricNames(expr string) []string {
	s := reStringLiteral.ReplaceAllString(expr, `""`)
	s = reModifierParens.ReplaceAllString(s, " ")
	s = reLabelMatchers.ReplaceAllString(s, " ")
	s = reRangeSelector.ReplaceAllString(s, " ")
	s = reDurationLiteral.ReplaceAllString(s, "0")

	var names []string
	seen := map[string]bool{}
	rest := s
	for {
		loc := reIdentifier.FindStringIndex(rest)
		if loc == nil {
			break
		}
		ident := rest[loc[0]:loc[1]]
		rest = rest[loc[1]:]
		if reFollowedByParen.MatchString(rest) {
			continue // function call: rate(...), increase(...), sum(...)
		}
		if promQLKeywords[ident] {
			continue
		}
		if !seen[ident] {
			seen[ident] = true
			names = append(names, ident)
		}
	}
	sort.Strings(names)
	return names
}

// prometheusGeneratedMetrics are the meta-series Prometheus' own scrape loop
// synthesises. No client library can register them, so they are the one thing a
// rule may name that the registry does not carry.
var prometheusGeneratedMetrics = map[string]bool{
	"up": true,
}

// reLabelEquality matches a plain `name="value"` equality matcher. Regex
// matchers (=~, !~) and negations (!=) deliberately do not match: only a plain
// equality pins a literal label value that must exist in the code or the config.
var reLabelEquality = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*"((?:[^"\\]|\\.)*)"`)

// labelEqualities returns, per label name, the literal values an expression
// requires with a plain `=` matcher.
func labelEqualities(expr string) map[string][]string {
	out := map[string][]string{}
	for _, m := range reLabelEquality.FindAllStringSubmatch(expr, -1) {
		out[m[1]] = append(out[m[1]], m[2])
	}
	return out
}

// exportedMetricNames returns the metric family names registered with the
// default Prometheus registry — the registry pkg/metrics declares its series on
// — so the rule file is checked against the code, not a hand-written list.
//
// A vector that has never been touched emits no family, so one child is created
// per declared vector and removed again afterwards. This test runs first in the
// package and the cleanup restores the empty state it started from.
func exportedMetricNames(t *testing.T) map[string]bool {
	t.Helper()

	VMsTotal.WithLabelValues("alertrules-guard", "started")
	VMBootErrors.WithLabelValues("alertrules-guard", "boot_failure")
	VMCrashes.WithLabelValues("alertrules-guard", "alertrules-guard", "unexpected_exit")
	VMHealth.WithLabelValues("alertrules-guard", "alertrules-guard", "healthy")
	VMBootDuration.WithLabelValues("alertrules-guard", "alertrules-guard")
	VMBalloonMB.WithLabelValues("alertrules-guard", "alertrules-guard", "alertrules-guard")
	DiskUsageBytes.WithLabelValues("alertrules-guard", "rootfs")
	DiskTotalBytes.WithLabelValues("alertrules-guard", "rootfs")
	t.Cleanup(func() {
		VMsTotal.Reset()
		VMBootErrors.Reset()
		VMCrashes.Reset()
		VMHealth.Reset()
		VMBootDuration.Reset()
		VMBalloonMB.Reset()
		DiskUsageBytes.Reset()
		DiskTotalBytes.Reset()
	})

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	names := make(map[string]bool, len(families))
	for _, mf := range families {
		names[mf.GetName()] = true
	}
	return names
}

// ── The rule files ────────────────────────────────────────────────

type alertRule struct {
	Alert       string            `yaml:"alert"`
	Expr        string            `yaml:"expr"`
	For         string            `yaml:"for"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`

	// group is the owning group's name, filled in by allRules.
	group string
}

type alertRuleFile struct {
	Groups []struct {
		Name  string      `yaml:"name"`
		Rules []alertRule `yaml:"rules"`
	} `yaml:"groups"`
}

func loadAlertRules(t *testing.T) alertRuleFile {
	t.Helper()

	raw, err := os.ReadFile(repoPath(t, "infrastructure/observability/prometheus/alerts.yml"))
	require.NoError(t, err)

	var f alertRuleFile
	require.NoError(t, yaml.Unmarshal(raw, &f))
	require.NotEmpty(t, f.Groups, "alerts.yml must define at least one group")
	return f
}

// prometheusConfig is the subset of prometheus.yml the guard needs: the scrape
// jobs a rule is allowed to name, and the global evaluation interval that every
// `for:` is ultimately rounded to.
type prometheusConfig struct {
	Global struct {
		EvaluationInterval string `yaml:"evaluation_interval"`
	} `yaml:"global"`
	ScrapeConfigs []struct {
		JobName string `yaml:"job_name"`
	} `yaml:"scrape_configs"`
}

func loadPrometheusConfig(t *testing.T) prometheusConfig {
	t.Helper()

	raw, err := os.ReadFile(repoPath(t, "infrastructure/observability/prometheus/prometheus.yml"))
	require.NoError(t, err)

	var cfg prometheusConfig
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	require.NotEmpty(t, cfg.ScrapeConfigs, "prometheus.yml must define at least one scrape job")
	return cfg
}

// allRules flattens every rule in the file, keeping the group name for messages.
func allRules(t *testing.T, f alertRuleFile) []alertRule {
	t.Helper()

	var out []alertRule
	for _, g := range f.Groups {
		for _, r := range g.Rules {
			r.group = g.Name
			out = append(out, r)
		}
	}
	return out
}

func TestMetricNamesExtraction(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want []string
	}{
		{
			name: "range function",
			expr: `increase(swarmcracker_vm_crashes_total[15m]) >= 3`,
			want: []string{"swarmcracker_vm_crashes_total"},
		},
		{
			name: "label matcher value is not a metric",
			expr: `swarmcracker_vm_health{state="unhealthy"} > 0`,
			want: []string{"swarmcracker_vm_health"},
		},
		{
			name: "scrape meta series",
			expr: `up{job="swarmcracker"} == 0`,
			want: []string{"up"},
		},
		{
			name: "ratio of two series",
			expr: `swarmcracker_disk_usage_bytes / swarmcracker_disk_total_bytes > 0.8`,
			want: []string{"swarmcracker_disk_total_bytes", "swarmcracker_disk_usage_bytes"},
		},
		{
			name: "aggregation with by",
			expr: `sum by (instance) (increase(swarmcracker_vm_boot_errors_total[15m])) > 3`,
			want: []string{"swarmcracker_vm_boot_errors_total"},
		},
		{
			name: "offset is not a metric",
			expr: `swarmcracker_vms_running - swarmcracker_vms_running offset 5m < 0`,
			want: []string{"swarmcracker_vms_running"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, metricNames(tt.expr))
		})
	}
}

func TestAlertRulesReferenceOnlyExportedSeries(t *testing.T) {
	registered := exportedMetricNames(t)
	rules := loadAlertRules(t)

	ruleCount := 0
	referenced := map[string]bool{}
	for _, r := range allRules(t, rules) {
		ruleCount++
		require.NotEmptyf(t, r.Expr, "rule %q has no expr", r.Alert)

		names := metricNames(r.Expr)
		require.NotEmptyf(t, names, "rule %q: no metric name extracted from %q", r.Alert, r.Expr)

		for _, n := range names {
			referenced[n] = true
			assert.Truef(t, registered[n] || prometheusGeneratedMetrics[n],
				"rule %q references %q, which this repo does not register as a metric", r.Alert, n)
		}
	}

	require.NotZero(t, ruleCount, "alerts.yml must define at least one rule")
	// Keep the guard itself honest: these must have been seen, or the
	// extraction above silently checked nothing.
	for _, want := range []string{
		"swarmcracker_vm_boot_errors_total",
		"swarmcracker_vm_crashes_total",
		"swarmcracker_vm_health",
		"swarmcracker_disk_usage_bytes",
		"swarmcracker_disk_total_bytes",
	} {
		assert.Containsf(t, referenced, want, "expected a rule to read %s", want)
	}
}

// healthStates are the values RecordVMHealth can publish, read straight from the
// constants the daemon writes into VMHealth.State. A rule matching anything else
// (a typo like `state="unhealty"`) matches no series the daemon ever produces, so
// it can never fire even though its metric name is registered.
var healthStates = map[string]bool{
	scruntime.HealthStarting:  true,
	scruntime.HealthHealthy:   true,
	scruntime.HealthFailing:   true,
	scruntime.HealthUnhealthy: true,
}

// TestAlertRulesMatchRealHealthStates closes the hole the registered-name guard
// leaves: the metric may exist while the label value a rule filters on does not.
func TestAlertRulesMatchRealHealthStates(t *testing.T) {
	rules := loadAlertRules(t)

	checked := 0
	for _, r := range allRules(t, rules) {
		if !strings.Contains(r.Expr, "swarmcracker_vm_health") {
			continue
		}
		states := labelEqualities(r.Expr)["state"]
		require.NotEmptyf(t, states,
			"rule %q (group %s) reads swarmcracker_vm_health with no state matcher, so it matches every health series including healthy ones",
			r.Alert, r.group)
		for _, s := range states {
			checked++
			assert.Truef(t, healthStates[s],
				"rule %q (group %s) matches state=%q, which is not one of the states the daemon publishes (%s)",
				r.Alert, r.group, s, strings.Join([]string{
					scruntime.HealthStarting, scruntime.HealthHealthy,
					scruntime.HealthFailing, scruntime.HealthUnhealthy,
				}, ", "))
		}
	}
	assert.NotZero(t, checked, "no rule matched a swarmcracker_vm_health state; the guard checked nothing")
}

// TestAlertRulesReferenceRealScrapeJobs pins every job="..." matcher in the rules
// to a job prometheus.yml actually scrapes: `up{job="typo"} == 0` is an empty
// vector forever, so it passes a metric-name guard while never firing.
func TestAlertRulesReferenceRealScrapeJobs(t *testing.T) {
	cfg := loadPrometheusConfig(t)
	jobs := map[string]bool{}
	for _, sc := range cfg.ScrapeConfigs {
		jobs[sc.JobName] = true
	}

	rules := loadAlertRules(t)
	checked := 0
	for _, r := range allRules(t, rules) {
		for _, job := range labelEqualities(r.Expr)["job"] {
			checked++
			assert.Truef(t, jobs[job],
				"rule %q (group %s) matches job=%q, which no scrape_config in prometheus.yml defines",
				r.Alert, r.group, job)
		}
	}
	assert.NotZero(t, checked, "no rule matched a job label; the guard checked nothing")
}

// TestRuleEvaluationIntervalCanObserveFors fails when rules are evaluated less
// often than the shortest `for:` in the file. global.evaluation_interval is the
// rule manager's evaluation interval and its built-in default is 1m — coarser
// than the 30s health rule, which would leave the condition free to rise and
// clear between two evaluations.
func TestRuleEvaluationIntervalCanObserveFors(t *testing.T) {
	cfg := loadPrometheusConfig(t)
	require.NotEmptyf(t, cfg.Global.EvaluationInterval,
		"prometheus.yml must set global.evaluation_interval; the 1m default is coarser than the shortest rule `for:`")

	eval, err := time.ParseDuration(cfg.Global.EvaluationInterval)
	require.NoErrorf(t, err, "global.evaluation_interval %q is not a duration", cfg.Global.EvaluationInterval)
	require.Positive(t, eval, "global.evaluation_interval must be positive")

	shortest := time.Duration(0)
	for _, r := range allRules(t, loadAlertRules(t)) {
		if r.For == "" || r.For == "0s" {
			continue
		}
		d, err := time.ParseDuration(r.For)
		if err != nil {
			// Prometheus-only units (d/w/y) cannot be shorter than the
			// second/millisecond range this invariant cares about.
			continue
		}
		if shortest == 0 || d < shortest {
			shortest = d
		}
	}
	require.NotZero(t, shortest, "no rule declares a `for:` to check the evaluation interval against")

	assert.LessOrEqualf(t, eval, shortest,
		"global.evaluation_interval (%s) is coarser than the shortest rule `for:` (%s): a rule can be pending for a window shorter than its own evaluation interval",
		eval, shortest)
}

func TestAlertRulesCoverEveryFailureMode(t *testing.T) {
	rules := loadAlertRules(t)

	modes := []struct {
		mode     string
		requires []string
	}{
		{"VMs that never reach running", []string{"swarmcracker_vm_boot_errors_total"}},
		{"crash loop after running", []string{"swarmcracker_vm_crashes_total"}},
		{"healthcheck failing or unhealthy", []string{"swarmcracker_vm_health"}},
		{"disk pressure", []string{"swarmcracker_disk_usage_bytes", "swarmcracker_disk_total_bytes"}},
	}

	for _, m := range modes {
		found := false
		for _, r := range allRules(t, rules) {
			if containsAll(r.Expr, m.requires) {
				found = true
				break
			}
		}
		assert.Truef(t, found, "no rule reads the %s signal (%v)", m.mode, m.requires)
	}
}

func containsAll(haystack string, needles []string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}

func TestPrometheusConfigLoadsAlertRules(t *testing.T) {
	raw, err := os.ReadFile(repoPath(t, "infrastructure/observability/prometheus/prometheus.yml"))
	require.NoError(t, err)

	var cfg struct {
		RuleFiles []string `yaml:"rule_files"`
		Alerting  struct {
			Alertmanagers []struct {
				StaticConfigs []struct {
					Targets []string `yaml:"targets"`
				} `yaml:"static_configs"`
			} `yaml:"alertmanagers"`
		} `yaml:"alerting"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))

	assert.Contains(t, cfg.RuleFiles, "alerts.yml", "prometheus.yml must load alerts.yml")

	// Rules that fire into nowhere are inert, so the wiring must also name the
	// Alertmanager service from the same compose file.
	var targets []string
	for _, am := range cfg.Alerting.Alertmanagers {
		for _, sc := range am.StaticConfigs {
			targets = append(targets, sc.Targets...)
		}
	}
	assert.Contains(t, targets, "alertmanager:9093",
		"prometheus.yml must send alerts to the alertmanager service")
}

func TestAlertmanagerConfigHasReachableReceiver(t *testing.T) {
	raw, err := os.ReadFile(repoPath(t, "infrastructure/observability/alertmanager/alertmanager.yml"))
	require.NoError(t, err)

	var cfg struct {
		Route struct {
			Receiver string   `yaml:"receiver"`
			GroupBy  []string `yaml:"group_by"`
		} `yaml:"route"`
		Receivers []struct {
			Name           string `yaml:"name"`
			WebhookConfigs []struct {
				URL string `yaml:"url"`
			} `yaml:"webhook_configs"`
		} `yaml:"receivers"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))

	require.NotEmpty(t, cfg.Route.Receiver, "alertmanager route must name a receiver")
	assert.NotEmpty(t, cfg.Route.GroupBy, "alertmanager route must group alerts")

	defined := map[string]bool{}
	var defaultWebhookURLs []string
	for _, r := range cfg.Receivers {
		defined[r.Name] = true
		if r.Name == cfg.Route.Receiver {
			for _, w := range r.WebhookConfigs {
				defaultWebhookURLs = append(defaultWebhookURLs, w.URL)
			}
		}
	}
	assert.Truef(t, defined[cfg.Route.Receiver],
		"route receiver %q is not defined in receivers", cfg.Route.Receiver)

	// The default receiver must actually deliver somewhere, or alerts are
	// dropped on the floor.
	delivers := false
	for _, u := range defaultWebhookURLs {
		if u != "" {
			delivers = true
		}
	}
	assert.Truef(t, delivers,
		"default receiver %q must configure a webhook endpoint", cfg.Route.Receiver)
}
