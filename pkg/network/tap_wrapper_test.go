//go:build !integration

package network

// The production half of the restore's TAP lifecycle: the exported wrappers are what the
// executor wires, and the tests in tap_down_test.go only exercise the WithExecutor
// variants. A wrapper that drifted — CreateTAPDeviceDown calling the boot variant, say —
// would pass every one of those tests and would bring a restored guest's interface up
// before the restore re-addressed it.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateTAPDeviceDownIsTheDownVariant pins the production wrapper: it creates the
// device attached to its bridge and leaves it down, with no injectable executor in sight.
func TestCreateTAPDeviceDownIsTheDownVariant(t *testing.T) {
	exec := &scriptedTAPExecutor{
		output: func([]string) ([]byte, error) {
			return []byte("tap-aabbccdd-0: <BROADCAST,MULTICAST> aa:fc:00:00:00:01\n"), nil
		},
	}
	original := newTAPExecutor
	newTAPExecutor = func() TAPExecutor { return exec }
	t.Cleanup(func() { newTAPExecutor = original })

	require.NoError(t, CreateTAPDeviceDown("tap-aabbccdd-0", "swarm-br0"))

	assert.Equal(t, []string{
		"ip link delete tap-aabbccdd-0",
		"ip tuntap add tap-aabbccdd-0 mode tap",
		"ip link set tap-aabbccdd-0 master swarm-br0",
		"ip -br link show tap-aabbccdd-0",
	}, exec.commandLines(), "the production creator is the down variant, not the boot one")
}
