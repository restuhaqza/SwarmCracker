package main

import (
	"os"
	"strings"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dur(t *testing.T, d *gogotypes.Duration) time.Duration {
	t.Helper()
	got, err := gogotypes.DurationFromProto(d)
	require.NoError(t, err)
	return got
}

func TestBuildServiceSpec_MapsSchedulingAndLifecycle(t *testing.T) {
	spec, err := buildServiceSpec(serviceCreateOptions{
		name:     "web",
		image:    "nginx",
		replicas: 3,
		mode:     modeReplicated,

		constraints:    []string{"node.hostname==n1", "node.role==worker"},
		placementPrefs: []string{"spread=node.labels.zone"},

		restartSet:      true,
		restartCond:     "on-failure",
		restartDelay:    "5s",
		restartAttempts: 3,
		restartWindow:   "1h",

		updateSet:        true,
		updateOrder:      "start-first",
		updateParallel:   2,
		updateDelay:      "10s",
		updateFailAction: "rollback",
		updateMonitor:    "30s",
		updateMaxRatio:   0.2,

		rollbackSet:        true,
		rollbackOrder:      "stop-first",
		rollbackFailAction: "pause",
	})
	require.NoError(t, err)

	require.NotNil(t, spec.GetReplicated())
	assert.Equal(t, uint64(3), spec.GetReplicated().Replicas)

	require.NotNil(t, spec.Task.Placement)
	assert.Equal(t, []string{"node.hostname==n1", "node.role==worker"}, spec.Task.Placement.Constraints)
	require.Len(t, spec.Task.Placement.Preferences, 1)
	assert.Equal(t, "node.labels.zone", spec.Task.Placement.Preferences[0].GetSpread().SpreadDescriptor)

	require.NotNil(t, spec.Task.Restart)
	assert.Equal(t, api.RestartOnFailure, spec.Task.Restart.Condition)
	assert.Equal(t, uint64(3), spec.Task.Restart.MaxAttempts)
	assert.Equal(t, 5*time.Second, dur(t, spec.Task.Restart.Delay))
	assert.Equal(t, time.Hour, dur(t, spec.Task.Restart.Window))

	require.NotNil(t, spec.Update)
	assert.Equal(t, api.UpdateConfig_START_FIRST, spec.Update.Order)
	assert.Equal(t, uint64(2), spec.Update.Parallelism)
	assert.Equal(t, api.UpdateConfig_ROLLBACK, spec.Update.FailureAction)
	assert.Equal(t, float32(0.2), spec.Update.MaxFailureRatio)
	assert.Equal(t, 10*time.Second, spec.Update.Delay)
	assert.Equal(t, 30*time.Second, dur(t, spec.Update.Monitor))

	require.NotNil(t, spec.Rollback)
	assert.Equal(t, api.UpdateConfig_STOP_FIRST, spec.Rollback.Order)
	assert.Equal(t, api.UpdateConfig_PAUSE, spec.Rollback.FailureAction)
}

func TestBuildServiceSpec_GlobalMode(t *testing.T) {
	spec, err := buildServiceSpec(serviceCreateOptions{name: "g", image: "nginx", mode: modeGlobal})
	require.NoError(t, err)
	assert.NotNil(t, spec.GetGlobal())
	assert.Nil(t, spec.GetReplicated())
}

func TestBuildServiceSpec_DefaultsLeaveSectionsUnset(t *testing.T) {
	spec, err := buildServiceSpec(serviceCreateOptions{name: "d", image: "nginx", replicas: 1})
	require.NoError(t, err)
	assert.Nil(t, spec.Task.Placement, "no --constraint/--placement-pref must not set placement")
	assert.Nil(t, spec.Task.Restart, "no --restart-* must not set a restart policy")
	assert.Nil(t, spec.Update, "no --update-* must not set an update config")
	assert.Nil(t, spec.Rollback, "no --rollback-* must not set a rollback config")
}

// TestBuildServiceSpec_RegistryAuth verifies --with-registry-auth puts the
// already-resolved X-Registry-Auth payload on the container's PullOptions, and
// that an unset payload leaves PullOptions nil (workers then use their own
// configured credentials).
func TestBuildServiceSpec_RegistryAuth(t *testing.T) {
	t.Run("payload set", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{
			name:             "web",
			image:            "private.example.com/app:1",
			replicas:         1,
			withRegistryAuth: true,
			registryAuth:     "eyJ1c2VybmFtZSI6ImJvYiJ9",
		})
		require.NoError(t, err)
		require.NotNil(t, spec.Task.GetContainer().PullOptions)
		assert.Equal(t, "eyJ1c2VybmFtZSI6ImJvYiJ9", spec.Task.GetContainer().PullOptions.RegistryAuth)
	})

	t.Run("payload unset", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{name: "web", image: "nginx", replicas: 1})
		require.NoError(t, err)
		assert.Nil(t, spec.Task.GetContainer().PullOptions, "no --with-registry-auth must leave PullOptions nil")
	})

	t.Run("flag set but payload empty", func(t *testing.T) {
		// --with-registry-auth turns the keychain I/O on in createService; if that
		// resolution yields nothing (an empty payload), buildServiceSpec must not
		// invent an empty PullOptions, which would mark the spec as carrying auth
		// that the workers would then misuse.
		spec, err := buildServiceSpec(serviceCreateOptions{
			name:             "web",
			image:            "private.example.com/app:1",
			replicas:         1,
			withRegistryAuth: true,
		})
		require.NoError(t, err)
		assert.Nil(t, spec.Task.GetContainer().PullOptions, "an empty payload must leave PullOptions nil")
	})
}

func TestBuildServiceSpec_RejectsInvalidValues(t *testing.T) {
	cases := map[string]serviceCreateOptions{
		"mode":            {mode: "bogus"},
		"restart-cond":    {restartSet: true, restartCond: "bogus"},
		"restart-delay":   {restartSet: true, restartDelay: "nope"},
		"update-order":    {updateSet: true, updateOrder: "bogus"},
		"update-action":   {updateSet: true, updateFailAction: "bogus"},
		"update-delay":    {updateSet: true, updateDelay: "xyz"},
		"rollback-action": {rollbackSet: true, rollbackFailAction: "bogus"},
		"constraint":      {constraints: []string{"no-operator"}},
		"placement-pref":  {placementPrefs: []string{"bogus"}},
		"spread-empty":    {placementPrefs: []string{"spread="}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			o.name = "x"
			o.image = "nginx"
			_, err := buildServiceSpec(o)
			assert.Error(t, err)
		})
	}
}

func TestBuildMounts(t *testing.T) {
	got, err := buildMounts(
		[]string{"type=volume,source=myvol,target=/data", "type=bind,source=/host/dir,target=/mnt,readonly"},
		[]string{"shortvol:/srv", "shortvol2:/srv2:ro", "/host/short:/mnt2"},
	)
	require.NoError(t, err)
	require.Len(t, got, 5)

	assert.Equal(t, api.MountTypeVolume, got[0].Type)
	assert.Equal(t, "volume://myvol", got[0].Source)
	assert.Equal(t, "/data", got[0].Target)
	assert.False(t, got[0].ReadOnly)

	assert.Equal(t, api.MountTypeBind, got[1].Type)
	assert.Equal(t, "/host/dir", got[1].Source)
	assert.True(t, got[1].ReadOnly)

	assert.Equal(t, api.MountTypeVolume, got[2].Type)
	assert.Equal(t, "volume://shortvol", got[2].Source)

	assert.Equal(t, api.MountTypeVolume, got[3].Type)
	assert.True(t, got[3].ReadOnly)

	assert.Equal(t, api.MountTypeBind, got[4].Type)
	assert.Equal(t, "/host/short", got[4].Source)
}

func TestBuildMounts_RejectsInvalid(t *testing.T) {
	cases := map[string]struct{ mounts, volumes []string }{
		"mount-no-target":  {mounts: []string{"type=volume,source=v"}},
		"mount-no-source":  {mounts: []string{"type=volume,target=/data"}},
		"mount-tmpfs":      {mounts: []string{"type=tmpfs,target=/data"}},
		"mount-unknown":    {mounts: []string{"type=weird,source=v,target=/data"}},
		"mount-rel-target": {mounts: []string{"type=volume,source=v,target=data"}},
		"mount-bad-vol":    {mounts: []string{"type=volume,source=/abs,target=/data"}},
		"volume-too-few":   {volumes: []string{"onlyone"}},
		"volume-bad-opt":   {volumes: []string{"v:/data:xyz"}},
		"volume-empty":     {volumes: []string{":/data"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := buildMounts(tc.mounts, tc.volumes)
			assert.Error(t, err)
		})
	}
}

func TestBuildServiceSpec_Networks(t *testing.T) {
	spec, err := buildServiceSpec(serviceCreateOptions{name: "x", image: "nginx", replicas: 1, networkTargets: []string{"net-1"}})
	require.NoError(t, err)
	require.Len(t, spec.Task.Networks, 1)
	assert.Equal(t, "net-1", spec.Task.Networks[0].Target)
}

func TestBuildNetworkAttachments(t *testing.T) {
	got := buildNetworkAttachments([]string{"a", " b ", ""})
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Target)
	assert.Equal(t, "b", got[1].Target)
}

func TestParseSecretSpec(t *testing.T) {
	t.Run("bare name", func(t *testing.T) {
		spec, err := parseSecretSpec("db-pass")
		require.NoError(t, err)
		assert.Equal(t, "db-pass", spec.Source)
		assert.Empty(t, spec.Target)
		assert.Zero(t, spec.Mode)
	})

	t.Run("full spec", func(t *testing.T) {
		spec, err := parseSecretSpec("src=db-pass,target=/run/secrets/db,mode=0400,uid=1000,gid=2000")
		require.NoError(t, err)
		assert.Equal(t, "db-pass", spec.Source)
		assert.Equal(t, "/run/secrets/db", spec.Target)
		assert.Equal(t, os.FileMode(0o400), spec.Mode)
		assert.Equal(t, "1000", spec.UID)
		assert.Equal(t, "2000", spec.GID)
	})

	t.Run("errors", func(t *testing.T) {
		for _, bad := range []string{"", "src=db-pass,mode=9z", "src=db-pass,bogus=1", "target=/x"} {
			if _, err := parseSecretSpec(bad); err == nil {
				t.Errorf("expected error for %q", bad)
			}
		}
	})
}

func TestBuildServiceSpec_HostnameAndDNS(t *testing.T) {
	spec, err := buildServiceSpec(serviceCreateOptions{
		name:     "web",
		image:    "nginx",
		replicas: 1,
		hostname: "api-1.internal",
		dns:      []string{"1.1.1.1", "9.9.9.9"},
	})
	require.NoError(t, err)

	container := spec.Task.GetContainer()
	require.NotNil(t, container)
	assert.Equal(t, "api-1.internal", container.Hostname)
	require.NotNil(t, container.DNSConfig)
	assert.Equal(t, []string{"1.1.1.1", "9.9.9.9"}, container.DNSConfig.Nameservers)
}

func TestBuildServiceSpec_DefaultsLeaveHostnameDNSUnset(t *testing.T) {
	spec, err := buildServiceSpec(serviceCreateOptions{name: "d", image: "nginx", replicas: 1})
	require.NoError(t, err)
	container := spec.Task.GetContainer()
	require.NotNil(t, container)
	assert.Empty(t, container.Hostname)
	assert.Nil(t, container.DNSConfig)
}

// TestBuildServiceSpec_RejectsUnsupportedExecFlags enforces the #37 contract:
// a flag the executor cannot honor must be rejected with a clear error, never
// silently accepted.
func TestBuildServiceSpec_RejectsUnsupportedExecFlags(t *testing.T) {
	cases := map[string]serviceCreateOptions{
		"user":      {user: "1000:1000"},
		"cap-add":   {capAdd: []string{"NET_ADMIN"}},
		"cap-drop":  {capDrop: []string{"ALL"}},
		"read-only": {readOnly: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			o.name = "x"
			o.image = "nginx"
			_, err := buildServiceSpec(o)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "not supported")
		})
	}
}

func TestBuildServiceSpec_RejectsInvalidHostnameDNS(t *testing.T) {
	cases := map[string]serviceCreateOptions{
		"hostname-space":    {hostname: "bad host"},
		"hostname-slash":    {hostname: "bad/host"},
		"hostname-long":     {hostname: strings.Repeat("a", 254)},
		"hostname-empty-lb": {hostname: "a..b"},
		"dns-not-ip":        {dns: []string{"not-an-ip"}},
		"dns-empty":         {dns: []string{""}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			o.name = "x"
			o.image = "nginx"
			_, err := buildServiceSpec(o)
			assert.Error(t, err)
		})
	}
}

func TestBuildServiceSpec_GoldenRejectsHostnameDNS(t *testing.T) {
	for name, o := range map[string]serviceCreateOptions{
		"hostname": {labels: []string{types.GoldenLabel + "=ubuntu@1.0"}, hostname: "g"},
		"dns":      {labels: []string{types.GoldenLabel + "=ubuntu@1.0"}, dns: []string{"1.1.1.1"}},
	} {
		t.Run(name, func(t *testing.T) {
			o.name = "x"
			o.image = "swarmcracker/golden:ubuntu-1.0"
			_, err := buildServiceSpec(o)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "golden")
		})
	}
}

// TestBuildServiceSpec_ExecFlagsAreHonoredOrRejected asserts that each of the
// container-execution flags either lands in the spec or is rejected -- i.e. no
// flag is accepted and then ignored.
func TestBuildServiceSpec_ExecFlagsAreHonoredOrRejected(t *testing.T) {
	base := func() serviceCreateOptions {
		return serviceCreateOptions{name: "x", image: "nginx", replicas: 1}
	}

	t.Run("hostname honored", func(t *testing.T) {
		o := base()
		o.hostname = "node-a"
		spec, err := buildServiceSpec(o)
		require.NoError(t, err)
		assert.Equal(t, "node-a", spec.Task.GetContainer().Hostname)
	})

	t.Run("dns honored", func(t *testing.T) {
		o := base()
		o.dns = []string{"8.8.8.8"}
		spec, err := buildServiceSpec(o)
		require.NoError(t, err)
		require.NotNil(t, spec.Task.GetContainer().DNSConfig)
		assert.Equal(t, []string{"8.8.8.8"}, spec.Task.GetContainer().DNSConfig.Nameservers)
	})

	for _, tc := range []struct {
		name string
		o    serviceCreateOptions
	}{
		{"user", serviceCreateOptions{user: "root"}},
		{"cap-add", serviceCreateOptions{capAdd: []string{"SYS_ADMIN"}}},
		{"cap-drop", serviceCreateOptions{capDrop: []string{"ALL"}}},
		{"read-only", serviceCreateOptions{readOnly: true}},
	} {
		t.Run(tc.name+" rejected", func(t *testing.T) {
			o := base()
			o.user, o.capAdd, o.capDrop, o.readOnly = tc.o.user, tc.o.capAdd, tc.o.capDrop, tc.o.readOnly
			_, err := buildServiceSpec(o)
			assert.Error(t, err)
		})
	}
}

// TestBuildServiceSpec_HealthcheckDefaults asserts that a bare --health-cmd
// maps to Test ["CMD-SHELL", cmd] and that the Docker-compatible defaults are
// applied to every unset value.
func TestBuildServiceSpec_HealthcheckDefaults(t *testing.T) {
	spec, err := buildServiceSpec(serviceCreateOptions{
		name:     "web",
		image:    "nginx",
		replicas: 1,
		health:   healthOptions{cmd: "curl -fsS localhost:8080/healthz", cmdSet: true},
	})
	require.NoError(t, err)

	hc := spec.Task.GetContainer().Healthcheck
	require.NotNil(t, hc)
	assert.Equal(t, []string{"CMD-SHELL", "curl -fsS localhost:8080/healthz"}, hc.Test)
	assert.Equal(t, 30*time.Second, dur(t, hc.Interval), "unset --health-interval defaults to 30s")
	assert.Equal(t, 30*time.Second, dur(t, hc.Timeout), "unset --health-timeout defaults to 30s")
	assert.Equal(t, int32(3), hc.Retries, "unset --health-retries defaults to 3")
	assert.Equal(t, time.Duration(0), dur(t, hc.StartPeriod), "unset --health-start-period defaults to 0")
}

// TestBuildServiceSpec_HealthcheckExplicit asserts that explicit interval,
// timeout, retries and start-period values are carried through verbatim,
// including a retries value of 5 (not silently coerced to the default 3).
func TestBuildServiceSpec_HealthcheckExplicit(t *testing.T) {
	spec, err := buildServiceSpec(serviceCreateOptions{
		name:     "web",
		image:    "nginx",
		replicas: 1,
		health: healthOptions{
			cmd:            "true",
			cmdSet:         true,
			interval:       "7s",
			intervalSet:    true,
			timeout:        "5s",
			timeoutSet:     true,
			retries:        5,
			retriesSet:     true,
			startPeriod:    "1m",
			startPeriodSet: true,
		},
	})
	require.NoError(t, err)

	hc := spec.Task.GetContainer().Healthcheck
	require.NotNil(t, hc)
	assert.Equal(t, []string{"CMD-SHELL", "true"}, hc.Test)
	assert.Equal(t, 7*time.Second, dur(t, hc.Interval))
	assert.Equal(t, 5*time.Second, dur(t, hc.Timeout))
	assert.Equal(t, int32(5), hc.Retries)
	assert.Equal(t, time.Minute, dur(t, hc.StartPeriod))
}

// TestBuildServiceSpec_NoHealthCmdLeavesHealthcheckNil asserts that omitting
// --health-cmd leaves the field nil so downstream behaviour is unchanged.
func TestBuildServiceSpec_NoHealthCmdLeavesHealthcheckNil(t *testing.T) {
	for name, o := range map[string]serviceCreateOptions{
		"all-unset": {},
		"empty-cmd": {health: healthOptions{cmd: ""}},
	} {
		t.Run(name, func(t *testing.T) {
			o.name = "x"
			o.image = "nginx"
			o.replicas = 1
			spec, err := buildServiceSpec(o)
			require.NoError(t, err)
			assert.Nil(t, spec.Task.GetContainer().Healthcheck)
		})
	}
}

// TestBuildServiceSpec_RejectsInvalidHealthcheck asserts each validation
// failure names the offending flag, so nothing is silently ignored. The
// explicit-zero cases are the contract this task fixes: --health-retries 0 and
// --health-interval/--health-timeout 0s must error, not fall back to a default.
func TestBuildServiceSpec_RejectsInvalidHealthcheck(t *testing.T) {
	cases := map[string]struct {
		o       serviceCreateOptions
		wantSub string
	}{
		"other-flags-without-cmd":   {serviceCreateOptions{health: healthOptions{interval: "10s", intervalSet: true}}, "--health-cmd"},
		"retries-without-cmd":       {serviceCreateOptions{health: healthOptions{retries: 3, retriesSet: true}}, "--health-cmd"},
		"value-without-cmd":         {serviceCreateOptions{health: healthOptions{interval: "10s"}}, "--health-cmd"},
		"explicit-retries-zero":     {serviceCreateOptions{health: healthOptions{cmd: "true", cmdSet: true, retries: 0, retriesSet: true}}, "--health-retries"},
		"explicit-retries-negative": {serviceCreateOptions{health: healthOptions{cmd: "true", cmdSet: true, retries: -1, retriesSet: true}}, "--health-retries"},
		"explicit-interval-zero":    {serviceCreateOptions{health: healthOptions{cmd: "true", cmdSet: true, interval: "0s", intervalSet: true}}, "--health-interval"},
		"explicit-timeout-zero":     {serviceCreateOptions{health: healthOptions{cmd: "true", cmdSet: true, timeout: "0s", timeoutSet: true}}, "--health-timeout"},
		"explicit-start-neg":        {serviceCreateOptions{health: healthOptions{cmd: "true", cmdSet: true, startPeriod: "-1s", startPeriodSet: true}}, "--health-start-period"},
		"retries-overflow":          {serviceCreateOptions{health: healthOptions{cmd: "true", cmdSet: true, retries: 1 << 31, retriesSet: true}}, "--health-retries"},
		"interval-unparseable":      {serviceCreateOptions{health: healthOptions{cmd: "true", cmdSet: true, interval: "nope", intervalSet: true}}, "--health-interval"},
		"interval-explicit-empty":   {serviceCreateOptions{health: healthOptions{cmd: "true", cmdSet: true, interval: "", intervalSet: true}}, "--health-interval"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			o := tc.o
			o.name = "x"
			o.image = "nginx"
			_, err := buildServiceSpec(o)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSub)
		})
	}
}

// TestBuildHealthcheck_DistinguishesExplicitFromUnset exercises the shared,
// cobra-free helper directly: the per-flag set bits are what let an explicit
// zero be rejected while an unset flag keeps its Docker default.
func TestBuildHealthcheck_DistinguishesExplicitFromUnset(t *testing.T) {
	t.Run("unset values use defaults", func(t *testing.T) {
		hc, err := buildHealthcheck(healthOptions{cmd: "true", cmdSet: true})
		require.NoError(t, err)
		require.NotNil(t, hc)
		assert.Equal(t, 30*time.Second, dur(t, hc.Interval))
		assert.Equal(t, 30*time.Second, dur(t, hc.Timeout))
		assert.Equal(t, int32(3), hc.Retries)
		assert.Equal(t, time.Duration(0), dur(t, hc.StartPeriod))
	})

	t.Run("explicit values honored", func(t *testing.T) {
		hc, err := buildHealthcheck(healthOptions{
			cmd:            "true",
			cmdSet:         true,
			interval:       "7s",
			intervalSet:    true,
			retries:        5,
			retriesSet:     true,
			startPeriod:    "2s",
			startPeriodSet: true,
		})
		require.NoError(t, err)
		require.NotNil(t, hc)
		assert.Equal(t, 7*time.Second, dur(t, hc.Interval))
		assert.Equal(t, int32(5), hc.Retries)
		assert.Equal(t, 2*time.Second, dur(t, hc.StartPeriod))
	})

	for name, tc := range map[string]struct {
		h       healthOptions
		wantSub string
	}{
		"explicit-retries-zero": {healthOptions{cmd: "true", cmdSet: true, retries: 0, retriesSet: true}, "--health-retries"},
		"explicit-interval-zero": {healthOptions{cmd: "true", cmdSet: true, interval: "0s", intervalSet: true},
			"--health-interval"},
		"explicit-timeout-zero": {healthOptions{cmd: "true", cmdSet: true, timeout: "0s", timeoutSet: true},
			"--health-timeout"},
		"explicit-start-negative": {healthOptions{cmd: "true", cmdSet: true, startPeriod: "-1s", startPeriodSet: true},
			"--health-start-period"},
		"retries-without-cmd": {healthOptions{retries: 3, retriesSet: true}, "--health-cmd"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildHealthcheck(tc.h)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSub)
		})
	}
}

// TestRegistryAuthForUpdate covers the three --with-registry-auth cases for
// `service update` directly: flag absent preserves, true sets the resolved
// payload, false clears (an empty applied payload).
func TestRegistryAuthForUpdate(t *testing.T) {
	const payload = "eyJ1c2VybmFtZSI6ImJvYiJ9"

	tests := []struct {
		name     string
		set      bool
		value    bool
		resolved string
		want     string
		apply    bool
	}{
		{name: "absent preserves", set: false, value: false, resolved: "", want: "", apply: false},
		{name: "true sets the resolved payload", set: true, value: true, resolved: payload, want: payload, apply: true},
		{name: "false clears", set: true, value: false, resolved: payload, want: "", apply: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, apply := registryAuthForUpdate(tc.set, tc.value, tc.resolved)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.apply, apply)
		})
	}
}

// TestBuildServiceSpec_Balloon verifies --balloon-mib travels as the
// types.BalloonLabel service label (the carrier the executor copies onto the
// task) and that an unset flag adds no label at all.
func TestBuildServiceSpec_Balloon(t *testing.T) {
	t.Run("unset adds no label", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{name: "web", image: "nginx", replicas: 1})
		require.NoError(t, err)
		_, ok := spec.Annotations.Labels[types.BalloonLabel]
		assert.False(t, ok, "an unset --balloon-mib must not add a label")
	})

	t.Run("set carries the target", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			memory: "1G", balloonMib: 256, balloonSet: true,
		})
		require.NoError(t, err)
		assert.Equal(t, "256", spec.Annotations.Labels[types.BalloonLabel])
	})

	t.Run("set without a memory limit still carries the target", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			balloonMib: 128, balloonSet: true,
		})
		require.NoError(t, err)
		assert.Equal(t, "128", spec.Annotations.Labels[types.BalloonLabel])
	})

	t.Run("agrees with an explicit label", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			labels:     []string{types.BalloonLabel + "=256"},
			balloonMib: 256, balloonSet: true,
		})
		require.NoError(t, err)
		assert.Equal(t, "256", spec.Annotations.Labels[types.BalloonLabel])
	})

	t.Run("a conflicting explicit label is rejected", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			labels:     []string{types.BalloonLabel + "=128"},
			balloonMib: 256, balloonSet: true,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--balloon-mib 256")
		assert.Contains(t, err.Error(), `"128"`)
	})

	// The conflict check is presence-based, not value-based: `--label
	// swarmcracker.balloon=` sets the key to the empty value, which is still an
	// explicit request and must not be silently overwritten by the flag.
	t.Run("an explicit empty label is a conflict, not an override", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			labels:     []string{types.BalloonLabel + "="},
			balloonMib: 256, balloonSet: true,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--balloon-mib 256")
		assert.Contains(t, err.Error(), types.BalloonLabel)
		assert.Contains(t, err.Error(), "--label")
	})

	// ...while the same empty label without the flag is left exactly as the user
	// wrote it (there is nothing to drop).
	t.Run("an explicit empty label without the flag is left alone", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			labels: []string{types.BalloonLabel + "="},
		})
		require.NoError(t, err)
		existing, ok := spec.Annotations.Labels[types.BalloonLabel]
		assert.True(t, ok, "the user's label must survive")
		assert.Equal(t, "", existing)
	})

	t.Run("zero target is rejected", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			balloonMib: 0, balloonSet: true,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--balloon-mib 0")
		assert.Contains(t, err.Error(), "positive")
	})

	t.Run("target not smaller than the memory is rejected", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			memory: "512M", balloonMib: 512, balloonSet: true,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--balloon-mib 512")
		assert.Contains(t, err.Error(), "memory size 512")
	})

	// A target that fits the old 512 MiB floor but not the declared 256 MiB VM
	// must be rejected before any RPC: buildServiceSpec is the pre-RPC validator
	// and it now validates against the size the node will actually boot (#95).
	t.Run("a target above a small declared VM is rejected naming both numbers", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			memory: "256m", balloonMib: 300, balloonSet: true,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--balloon-mib 300")
		assert.Contains(t, err.Error(), "memory size 256")
	})

	// ...while a target that is smaller than the small declared VM is accepted.
	t.Run("a target below a small declared VM is accepted", func(t *testing.T) {
		spec, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			memory: "256m", balloonMib: 128, balloonSet: true,
		})
		require.NoError(t, err)
		assert.Equal(t, "128", spec.Annotations.Labels[types.BalloonLabel])
	})

	// With no --memory the VM keeps the 512 MiB default, so that is still what
	// the balloon is validated against.
	t.Run("without --memory the 512 MiB default is the baseline", func(t *testing.T) {
		_, err := buildServiceSpec(serviceCreateOptions{
			name: "web", image: "nginx", replicas: 1,
			balloonMib: 512, balloonSet: true,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--balloon-mib 512")
		assert.Contains(t, err.Error(), "memory size 512")
	})
}

// TestBalloonLabelValue_Validation pins the two validation errors directly: a
// non-positive target and a target that is not smaller than the VM's memory.
// Both name the offending value, and neither ever produces a label value.
func TestBalloonLabelValue_Validation(t *testing.T) {
	tests := []struct {
		name    string
		mib     int64
		set     bool
		memMib  int64
		want    string
		wantErr []string
	}{
		{name: "unset adds nothing", set: false, memMib: 512, want: ""},
		{name: "valid target", mib: 256, set: true, memMib: 512, want: "256"},
		{name: "zero rejected", mib: 0, set: true, memMib: 512, wantErr: []string{"--balloon-mib 0", "positive"}},
		{name: "negative rejected", mib: -1, set: true, memMib: 512, wantErr: []string{"--balloon-mib -1", "positive"}},
		{name: "equal to memory rejected", mib: 512, set: true, memMib: 512, wantErr: []string{"--balloon-mib 512", "memory size 512"}},
		{name: "larger than memory rejected", mib: 2048, set: true, memMib: 1024, wantErr: []string{"--balloon-mib 2048", "memory size 1024"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := balloonLabelValue(tc.mib, tc.set, tc.memMib)
			if len(tc.wantErr) > 0 {
				require.Error(t, err)
				for _, want := range tc.wantErr {
					assert.Contains(t, err.Error(), want)
				}
				assert.Empty(t, got, "a rejected target must not produce a label value")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestServiceMemoryMib pins the size a service's --balloon-mib is validated
// against: the declared --memory value when whole-MiB, else the size resolved
// from the current task resources the same way the daemon does
// (types.ResolveVMSize: Limits then Reservations then the 512 MiB default). A
// declared 256 MiB stays 256 MiB — there is no floor.
func TestServiceMemoryMib(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		res   *api.ResourceRequirements
		want  int64
	}{
		{name: "unset with no resources uses the default", bytes: 0, want: 512},
		{name: "a declared 256 MiB is honoured, not floored", bytes: 256 * 1024 * 1024, want: 256},
		{name: "a declared 512 MiB stays 512", bytes: 512 * 1024 * 1024, want: 512},
		{name: "a declared 2 GiB is honoured", bytes: 2 * 1024 * 1024 * 1024, want: 2048},
		{name: "a sub-MiB declared value keeps the default", bytes: 1024, want: 512},
		{
			name: "an existing Limits size is resolved when no --memory is given",
			res:  &api.ResourceRequirements{Limits: &api.Resources{MemoryBytes: 256 * 1024 * 1024}},
			want: 256,
		},
		{
			name: "a size living only in Reservations is resolved",
			res:  &api.ResourceRequirements{Reservations: &api.Resources{MemoryBytes: 1024 * 1024 * 1024}},
			want: 1024,
		},
		{
			name: "Limits wins over Reservations",
			res: &api.ResourceRequirements{
				Limits:       &api.Resources{MemoryBytes: 256 * 1024 * 1024},
				Reservations: &api.Resources{MemoryBytes: 1024 * 1024 * 1024},
			},
			want: 256,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, serviceMemoryMib(tc.bytes, tc.res))
		})
	}
}
