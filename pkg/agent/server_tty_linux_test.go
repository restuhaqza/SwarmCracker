//go:build linux

package agent

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServeConnTTYExecEndsConnection asserts that a TTY exec ends the framed
// session: after sending the exit frame, ServeConn returns so the caller closes
// the connection. The framed protocol cannot continue after a TTY session
// because the guest reads the client's raw stdin off the same stream.
func TestServeConnTTYExecEndsConnection(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeConn(server) }()
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, WriteFrame(client, Request{
		Op:      "exec",
		Command: []string{"echo", "hi"},
		TTY:     true,
	}))

	// Drain frames until the exit frame; it must arrive before ServeConn
	// returns.
	sawExit := false
	for !sawExit {
		var msg Message
		_, err := ReadFrame(client, &msg)
		require.NoError(t, err)
		if msg.Type == "exit" {
			sawExit = true
		}
	}
	require.True(t, sawExit, "a TTY exec must send an exit frame")

	select {
	case err := <-done:
		assert.NoError(t, err, "ServeConn must end a TTY session without error")
	case <-time.After(2 * time.Second):
		t.Fatal("ServeConn did not return after a TTY exec")
	}
}
