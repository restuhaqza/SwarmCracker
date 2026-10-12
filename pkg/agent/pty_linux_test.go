//go:build linux

package agent

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenPTY(t *testing.T) {
	master, slave, err := openPTY()
	require.NoError(t, err)
	defer func() { _ = master.Close() }()

	cmd := exec.Command("echo", "hi")
	cmd.Stdout = slave
	cmd.Stderr = slave
	require.NoError(t, cmd.Start())
	// The child holds its own reference to the slave end.
	require.NoError(t, slave.Close())
	require.NoError(t, cmd.Wait())

	var out strings.Builder
	buf := make([]byte, 64)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "hi") {
		n, err := master.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	assert.Contains(t, out.String(), "hi")
}

func TestServeConnTTYExec(t *testing.T) {
	conn, _ := newPipePair(t)
	stdout, _, code := runExec(t, conn, Request{
		Op:      "exec",
		TTY:     true,
		Command: []string{"sh", "-c", "echo hi"},
	})
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "hi")
}
