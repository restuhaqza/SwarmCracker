package vminstance

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProcessAlive pins the default liveness answer the store uses when Config
// does not inject one: a record's PID only resolves while that process is
// actually running.
func TestProcessAlive(t *testing.T) {
	assert.False(t, processAlive(0), "there is no process 0 to resolve")
	assert.False(t, processAlive(-1))
	assert.True(t, processAlive(os.Getpid()), "the test's own process is running")

	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	assert.False(t, processAlive(cmd.Process.Pid), "a process that exited is not a running VM")
}
