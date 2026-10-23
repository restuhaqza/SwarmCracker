//go:build unix

package agent

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServeConnExecKilledBySignalReportsSignal proves the guest agent reports a
// terminating signal on the exit frame, with the 128+N status convention.
func TestServeConnExecKilledBySignalReportsSignal(t *testing.T) {
	conn, _ := newPipePair(t)
	require.NoError(t, WriteFrame(conn, Request{
		Op:      "exec",
		Command: []string{"sh", "-c", "kill -TERM $$"},
	}))

	for {
		msg := readMessage(t, conn)
		switch msg.Type {
		case "stdout", "stderr":
			continue
		case "error":
			t.Fatalf("unexpected error frame: %s", msg.Error)
		case "exit":
			require.NotNil(t, msg.Signal, "a signal-terminated guest must report its signal")
			require.NotNil(t, msg.Code)
			assert.Equal(t, 15, *msg.Signal, "SIGTERM is 15 on Linux and macOS")
			assert.Equal(t, 143, *msg.Code, "exitCode is 128+signal")
			return
		default:
			t.Fatalf("unexpected message type %q", msg.Type)
		}
	}
}

// TestExitStatus pins the (code, signal) mapping for the non-signal paths.
func TestExitStatus(t *testing.T) {
	code, signal := exitStatus(nil)
	assert.Zero(t, code)
	assert.Zero(t, signal)

	code, signal = exitStatus(errors.New("not an exit error"))
	assert.Equal(t, -1, code)
	assert.Zero(t, signal)

	assert.Zero(t, exitSignal(nil))
	assert.Zero(t, exitSignal(errors.New("not an exit error")))
}
