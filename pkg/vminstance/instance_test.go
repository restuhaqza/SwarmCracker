package vminstance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// instanceFixture is a complete sandbox record for id. Every path is distinct so
// that a field written or decoded under its neighbour's name fails the
// round-trip assertion instead of matching by accident.
func instanceFixture(id string, pid int) Instance {
	return Instance{
		ID:        id,
		Kind:      KindSandbox,
		OwnerID:   "tenant-7",
		NodeID:    "node-2",
		PID:       pid,
		APISocket: "/var/run/firecracker/" + id + ".sock",
		VsockPath: "/var/run/firecracker/" + id + ".vsock",
		Console:   "/var/log/firecracker/" + id + ".console.sock",
		NetNS:     "/var/run/netns/sbx_" + id,
		TAP:       "tap-0a1b2c3d-0",
		GuestIP:   "10.62.0.2",
		MAC:       "AA:FC:12:34:56:78",
		JailUID:   1001,
		Booted:    time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC),
		Ready:     true,
	}
}

func TestInstanceValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Instance)
		want   string
	}{
		{
			name:   "a complete sandbox record",
			mutate: func(*Instance) {},
		},
		{
			name: "a pool record owns nothing yet",
			mutate: func(i *Instance) {
				i.Kind = KindPool
				i.OwnerID = ""
				i.Ready = false
			},
		},
		{
			name: "a task record with none of the optional details",
			mutate: func(i *Instance) {
				i.Kind = KindTask
				i.OwnerID = "service-9"
				i.VsockPath = ""
				i.Console = ""
				i.NetNS = ""
				i.TAP = ""
				i.GuestIP = ""
				i.MAC = ""
				i.JailUID = 0
				i.Ready = false
			},
		},
		{name: "an empty id", mutate: func(i *Instance) { i.ID = "" }, want: "id is empty"},
		{name: "an id with a path separator", mutate: func(i *Instance) { i.ID = "sbx/1" }, want: "path separator"},
		{name: "an id with a dot", mutate: func(i *Instance) { i.ID = "sbx.1" }, want: "dot"},
		{name: "an id with whitespace", mutate: func(i *Instance) { i.ID = "sbx 1" }, want: "whitespace"},
		{
			name:   "an id too long to be a file name",
			mutate: func(i *Instance) { i.ID = strings.Repeat("a", runtime.MaxIDLength+1) },
			want:   "at most",
		},
		{name: "an empty kind", mutate: func(i *Instance) { i.Kind = "" }, want: "kind is empty"},
		{name: "an unknown kind", mutate: func(i *Instance) { i.Kind = "vm" }, want: `kind "vm" is not an instance kind`},
		{name: "a zero pid", mutate: func(i *Instance) { i.PID = 0 }, want: "pid is 0"},
		{name: "a negative pid", mutate: func(i *Instance) { i.PID = -3 }, want: "pid is -3"},
		{name: "no api socket", mutate: func(i *Instance) { i.APISocket = "" }, want: "apiSocket is empty"},
		{name: "a relative api socket", mutate: func(i *Instance) { i.APISocket = "run/vm.sock" }, want: "apiSocket"},
		{name: "a relative vsock path", mutate: func(i *Instance) { i.VsockPath = "run/vm.vsock" }, want: "vsockPath"},
		{name: "a relative console", mutate: func(i *Instance) { i.Console = "vm.console.sock" }, want: "console"},
		{name: "a relative netns", mutate: func(i *Instance) { i.NetNS = "sbx_1" }, want: "netns"},
		{name: "a negative jail uid", mutate: func(i *Instance) { i.JailUID = -1 }, want: "jailUid"},
		{name: "no boot time", mutate: func(i *Instance) { i.Booted = time.Time{} }, want: "booted is zero"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := instanceFixture("sbx-1", livePID)
			tc.mutate(&inst)

			err := inst.validate()
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestInstanceRoundTripsThroughJSON pins that the record's own encoding keeps
// every field: the registry's whole purpose is that a later process reads the
// paths the writer meant, not paths derived from the ID.
func TestInstanceRoundTripsThroughJSON(t *testing.T) {
	want := instanceFixture("sbx-1", livePID)

	data, err := json.Marshal(want)
	require.NoError(t, err)

	var got Instance
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, want, got)
}
