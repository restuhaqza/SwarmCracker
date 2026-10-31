package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTapName pins the name against literal strings. The literals are the rule:
// tap-<first 8 hex of sha256(taskID)>-<index>. They are written out rather than
// recomputed here, because a test that re-implements the expression it checks
// proves only that the expression equals itself.
func TestTapName(t *testing.T) {
	cases := []struct {
		name   string
		taskID string
		index  int
		want   string
	}{
		{
			name:   "first interface of a task",
			taskID: "test-task",
			index:  0,
			want:   "tap-0a76cd9b-0",
		},
		{
			name:   "second interface of the same task",
			taskID: "test-task",
			index:  1,
			want:   "tap-0a76cd9b-1",
		},
		{
			name:   "short task ID still hashes to eight hex characters",
			taskID: "abc",
			index:  0,
			want:   "tap-ba7816bf-0",
		},
		{
			name:   "task ID from another generator",
			taskID: "task-1",
			index:  2,
			want:   "tap-7afaa346-2",
		},
		{
			name:   "a real SwarmKit 25-character task ID",
			taskID: "abcdefghijklmnopqrstuvwxy",
			index:  0,
			want:   "tap-69b98054-0",
		},
		{
			name:   "two-digit index stays inside the kernel limit",
			taskID: "abc",
			index:  10,
			want:   "tap-ba7816bf-10",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, TapName(tc.taskID, tc.index))
		})
	}
}

func TestTapNameFitsLinuxInterfaceNameLimit(t *testing.T) {
	// A SwarmKit task ID is 25 characters; IFNAMSIZ is 16 bytes including the
	// terminating NUL, so the name must stay within 15 characters.
	const swarmkitTaskID = "abcdefghijklmnopqrstuvwxy"

	for _, index := range []int{0, 1, 9, 10} {
		name := TapName(swarmkitTaskID, index)
		assert.LessOrEqual(t, len(name), 15, "interface name %q is too long for the kernel", name)
	}
}

// TestVsockPath pins the identity rule against literal strings.
func TestVsockPath(t *testing.T) {
	cases := []struct {
		name      string
		socketDir string
		taskID    string
		want      string
	}{
		{
			name:      "default socket directory",
			socketDir: "/var/run/firecracker",
			taskID:    "task-1",
			want:      "/var/run/firecracker/task-1.vsock",
		},
		{
			name:      "jailer-style nested socket directory",
			socketDir: "/srv/jail/abc/root/run",
			taskID:    "abc",
			want:      "/srv/jail/abc/root/run/abc.vsock",
		},
		{
			name:      "no task ID means no vsock path",
			socketDir: "/var/run/firecracker",
			want:      "",
		},
		{
			name:   "no socket directory means no vsock path",
			taskID: "task-1",
			want:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, VsockPath(tc.socketDir, tc.taskID))
		})
	}
}

// TestVsockPathFromSocket pins the socket-shaped form of the same rule: the
// vsock muxer sits beside the API socket and differs from it only in the
// extension.
func TestVsockPathFromSocket(t *testing.T) {
	cases := []struct {
		name       string
		socketPath string
		want       string
	}{
		{
			name:       "default socket directory",
			socketPath: "/var/run/firecracker/task-1.sock",
			want:       "/var/run/firecracker/task-1.vsock",
		},
		{
			name:       "jailer-style nested socket directory",
			socketPath: "/srv/jail/abc/root/run/abc.sock",
			want:       "/srv/jail/abc/root/run/abc.vsock",
		},
		{
			name:       "restored VM socket keeps its own suffix",
			socketPath: "/var/run/firecracker/task-1-restored.sock",
			want:       "/var/run/firecracker/task-1-restored.vsock",
		},
		{
			name:       "relative socket path stays relative",
			socketPath: "task-1.sock",
			want:       "task-1.vsock",
		},
		{
			name:       "a path without the socket suffix only gains the vsock one",
			socketPath: "/var/run/firecracker/task-1",
			want:       "/var/run/firecracker/task-1.vsock",
		},
		{
			name:       "no socket path means no vsock path",
			socketPath: "",
			want:       "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, VsockPathFromSocket(tc.socketPath))
		})
	}
}

// TestGuestMAC pins the guest's address against literal strings for the same
// reason TestTapName does: the literals are the rule, and a re-anneal on a peer
// has to derive exactly the address the guest was booted with.
func TestGuestMAC(t *testing.T) {
	cases := []struct {
		name   string
		taskID string
		index  int
		want   string
	}{
		{name: "first interface of a task", taskID: "test-task", index: 0, want: "AA:FC:09:B6:4B:7B"},
		{name: "second interface of the same task", taskID: "task-1", index: 1, want: "AA:FC:71:21:5F:10"},
		{name: "another task", taskID: "abc", index: 0, want: "AA:FC:15:D0:AD:2C"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, GuestMAC(tc.taskID, tc.index))
		})
	}

	assert.NotEqual(t, GuestMAC("task-1", 0), GuestMAC("task-2", 0),
		"two tasks never share an address on the one L2 segment they share")
	assert.NotEqual(t, GuestMAC("task-1", 0), GuestMAC("task-1", 1),
		"two interfaces of one task never share an address")
	assert.True(t, strings.HasPrefix(GuestMAC("task-1", 0), "AA:FC:"),
		"the address is locally administered, so it cannot collide with a vendor's")
}

// TestVXLANInterfaceName pins both halves of the overlay name rule: the
// conventional name when the kernel has room for it, and the shortened, digest
// derived one when it does not.
func TestVXLANInterfaceName(t *testing.T) {
	cases := []struct {
		bridge string
		want   string
	}{
		{bridge: "swarm-br0", want: "swarm-br0-vxlan"},
		{bridge: "br-backend", want: "vx-2f97707e6e91"},
	}

	for _, tc := range cases {
		t.Run(tc.bridge, func(t *testing.T) {
			got := VXLANInterfaceName(tc.bridge)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len(got), 15, "an interface name is at most IFNAMSIZ-1 bytes")
		})
	}
}
