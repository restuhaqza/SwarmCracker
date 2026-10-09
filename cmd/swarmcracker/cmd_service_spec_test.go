package main

import (
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
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
