package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tapNameReference is the documented rule written out independently of TapName,
// so a test that compares against it fails if the implementation drifts:
// tap-<sha256(taskID)[:8]>-<index>, hex encoded.
func tapNameReference(taskID string, index int) string {
	sum := sha256.Sum256([]byte(taskID))
	return fmt.Sprintf("tap-%s-%d", hex.EncodeToString(sum[:])[:8], index)
}

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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TapName(tc.taskID, tc.index)

			require.Equal(t, tc.want, got)
			assert.Equal(t, tapNameReference(tc.taskID, tc.index), got,
				"TapName must implement the documented sha256 rule")
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

// TestVsockPathAgreesWithBrokerRule pins the rule the broker used before it
// delegated here: <dir of the API socket>/<task ID>.vsock.
func TestVsockPathAgreesWithBrokerRule(t *testing.T) {
	const socketPath = "/var/run/firecracker/task-1.sock"

	assert.Equal(t, socketPath[:len(socketPath)-len(".sock")]+".vsock",
		VsockPath("/var/run/firecracker", "task-1"))
}
