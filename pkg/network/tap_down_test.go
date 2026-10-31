//go:build !integration

package network

// The TAP lifecycle a migration restore drives, asserted without root: the device is
// created attached to its bridge and left DOWN, it is brought up only when the caller
// asks, and removing a device that is already gone is success. These are the three
// contracts pkg/migration's undo and re-address steps rely on, and they are the reason
// this package has one `ip tuntap add` rather than one per caller.

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedTAPExecutor answers ip(8) commands from a function, so a test can decide the
// answer per command line rather than per binary. The shared mock records commands but
// answers by binary name, which cannot tell `ip link delete` from `ip link show` — the
// two commands whose answers a "the device is already gone" rule is made of.
type scriptedTAPExecutor struct {
	mu     sync.Mutex
	calls  [][]string
	run    func(argv []string) error
	output func(argv []string) ([]byte, error)
}

func (s *scriptedTAPExecutor) Command(name string, arg ...string) *exec.Cmd {
	s.record(append([]string{name}, arg...))
	return exec.Command(name, arg...)
}

func (s *scriptedTAPExecutor) CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	s.record(append([]string{name}, arg...))
	return exec.CommandContext(ctx, name, arg...)
}

func (s *scriptedTAPExecutor) Run(cmd *exec.Cmd) error {
	if s.run == nil {
		return nil
	}
	return s.run(argvOf(cmd))
}

func (s *scriptedTAPExecutor) Output(cmd *exec.Cmd) ([]byte, error) {
	if s.output == nil {
		return nil, nil
	}
	return s.output(argvOf(cmd))
}

func (s *scriptedTAPExecutor) CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	return s.Output(cmd)
}

func (s *scriptedTAPExecutor) record(argv []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, argv)
}

// commandLines renders what was asked of ip(8), one line per call, for assertions that
// read like the sequence they describe.
func (s *scriptedTAPExecutor) commandLines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.calls))
	for _, call := range s.calls {
		out = append(out, strings.Join(call, " "))
	}
	return out
}

// argvOf is the command line a *exec.Cmd describes: the name it was created with
// followed by its arguments, without the resolved path.
func argvOf(cmd *exec.Cmd) []string {
	return cmd.Args
}

// TestCreateTAPDeviceDownLeavesTheDeviceDown pins the one contract a restore depends on:
// the device exists, is attached to its bridge, and is NOT up. A device that came up
// before the guest was re-addressed would put the source node's address on this node's
// bridge.
func TestCreateTAPDeviceDownLeavesTheDeviceDown(t *testing.T) {
	exec := &scriptedTAPExecutor{
		output: func([]string) ([]byte, error) {
			return []byte("tap-aabbccdd-0: <BROADCAST,MULTICAST> aa:fc:00:00:00:01\n"), nil
		},
	}

	require.NoError(t, CreateTAPDeviceDownWithExecutor("tap-aabbccdd-0", "swarm-br0", exec))

	lines := exec.commandLines()
	require.Equal(t, []string{
		"ip link delete tap-aabbccdd-0",
		"ip tuntap add tap-aabbccdd-0 mode tap",
		"ip link set tap-aabbccdd-0 master swarm-br0",
		"ip -br link show tap-aabbccdd-0",
	}, lines, "the device is created, attached and left down")

	for _, line := range lines {
		assert.NotEqual(t, "ip link set tap-aabbccdd-0 up", line,
			"a restore's TAP must stay down until the guest has its new address")
	}
}

// TestCreateTAPDeviceBringsTheDeviceUp pins the boot path's half of the shared code: the
// creator that a boot uses still brings the interface up before the VM starts, so the
// refactor that added the down variant did not change what a boot does.
func TestCreateTAPDeviceBringsTheDeviceUp(t *testing.T) {
	exec := &scriptedTAPExecutor{
		output: func([]string) ([]byte, error) {
			return []byte("tap-aabbccdd-0: <BROADCAST,MULTICAST,UP> aa:fc:00:00:00:01\n"), nil
		},
	}

	tap, err := CreateTAPDeviceWithExecutor("tap-aabbccdd-0", "swarm-br0", exec)
	require.NoError(t, err)
	require.NotNil(t, tap)
	assert.Equal(t, "aa:fc:00:00:00:01", tap.MAC)

	assert.Equal(t, []string{
		"ip link delete tap-aabbccdd-0",
		"ip tuntap add tap-aabbccdd-0 mode tap",
		"ip link set tap-aabbccdd-0 up",
		"ip link set tap-aabbccdd-0 master swarm-br0",
		"ip -br link show tap-aabbccdd-0",
	}, exec.commandLines())
}

// TestBringTAPUp pins the second half of the restore's lifecycle: one command, and a
// failure is reported rather than swallowed (an interface that never came up is a guest
// nobody can reach).
func TestBringTAPUp(t *testing.T) {
	t.Run("up", func(t *testing.T) {
		exec := &scriptedTAPExecutor{}
		require.NoError(t, BringTAPUpWithExecutor("tap-aabbccdd-0", exec))
		assert.Equal(t, []string{"ip link set tap-aabbccdd-0 up"}, exec.commandLines())
	})

	t.Run("failure", func(t *testing.T) {
		exec := &scriptedTAPExecutor{run: func([]string) error {
			return errors.New("operation not permitted")
		}}

		err := BringTAPUpWithExecutor("tap-aabbccdd-0", exec)
		require.Error(t, err)
		assert.ErrorContains(t, err, "tap-aabbccdd-0")
		assert.ErrorContains(t, err, "operation not permitted")
	})
}

// TestRemoveTAPDeviceIsIdempotent pins the undo's contract: a device that is gone is the
// state the removal wanted. `ip link delete` fails the same way for a missing device and
// for a permission problem, so the existence probe is what tells them apart.
func TestRemoveTAPDeviceIsIdempotent(t *testing.T) {
	t.Run("a device that is already gone is success", func(t *testing.T) {
		exec := &scriptedTAPExecutor{
			output: func(argv []string) ([]byte, error) {
				if hasPrefix(argv, "ip", "link", "delete") {
					return []byte("Cannot find device \"tap-aabbccdd-0\""), errors.New("exit status 1")
				}
				return nil, nil
			},
			run: func(argv []string) error {
				if hasPrefix(argv, "ip", "link", "show") {
					return errors.New("Device \"tap-aabbccdd-0\" does not exist")
				}
				return nil
			},
		}

		require.NoError(t, RemoveTAPDeviceWithExecutor("tap-aabbccdd-0", exec),
			"a restore that failed before creating the device has nothing to remove")
	})

	t.Run("a device that survives the delete is reported", func(t *testing.T) {
		exec := &scriptedTAPExecutor{
			output: func(argv []string) ([]byte, error) {
				if hasPrefix(argv, "ip", "link", "delete") {
					return []byte("operation not permitted"), errors.New("exit status 1")
				}
				return nil, nil
			},
		}

		err := RemoveTAPDeviceWithExecutor("tap-aabbccdd-0", exec)
		require.Error(t, err)
		assert.ErrorContains(t, err, "tap-aabbccdd-0")
	})

	t.Run("a device that is removed is success", func(t *testing.T) {
		exec := &scriptedTAPExecutor{}
		require.NoError(t, RemoveTAPDeviceWithExecutor("tap-aabbccdd-0", exec))
	})
}

// hasPrefix reports whether argv starts with prefix.
func hasPrefix(argv []string, prefix ...string) bool {
	if len(argv) < len(prefix) {
		return false
	}
	for i := range prefix {
		if argv[i] != prefix[i] {
			return false
		}
	}
	return true
}
