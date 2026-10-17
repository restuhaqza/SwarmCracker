package metrics

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
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

type alertRuleFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Alert       string            `yaml:"alert"`
			Expr        string            `yaml:"expr"`
			For         string            `yaml:"for"`
			Labels      map[string]string `yaml:"labels"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"rules"`
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
	for _, g := range rules.Groups {
		for _, r := range g.Rules {
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

func TestAlertRulesCoverEveryFailureMode(t *testing.T) {
	rules := loadAlertRules(t)

	modes := []struct {
		mode     string
		requires []string
	}{
		{"VMs that never reach running", []string{"swarmcracker_vm_boot_errors_total"}},
		{"crash loop after running", []string{"swarmcracker_vm_crashes_total"}},
		{"unhealthy task", []string{"swarmcracker_vm_health"}},
		{"disk pressure", []string{"swarmcracker_disk_usage_bytes", "swarmcracker_disk_total_bytes"}},
	}

	for _, m := range modes {
		found := false
		for _, g := range rules.Groups {
			for _, r := range g.Rules {
				if containsAll(r.Expr, m.requires) {
					found = true
					break
				}
			}
			if found {
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
