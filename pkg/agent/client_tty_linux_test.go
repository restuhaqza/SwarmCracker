//go:build linux

package agent

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientExecTTYStdinForwarded proves that a TTY exec actually delivers the
// bytes of opts.Stdin to the guest: the guest runs on a PTY, so `read` consumes
// the forwarded line and the command echoes it back with a marker.
func TestClientExecTTYStdinForwarded(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	var stdout bytes.Buffer
	code, err := c.Exec(context.Background(),
		[]string{"sh", "-c", "read line; echo MARKER:$line"},
		ExecOptions{TTY: true, Stdin: strings.NewReader("hello\n"), Stdout: &stdout})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout.String(), "MARKER:hello")
}

// TestClientExecTTYConsumesClientWithoutStdin asserts that a TTY exec consumes
// the client even when no stdin is forwarded: the guest ends the connection
// after a TTY session, so a later request must fail fast.
func TestClientExecTTYConsumesClientWithoutStdin(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	code, err := c.Exec(context.Background(), []string{"echo", "hi"}, ExecOptions{TTY: true})
	require.NoError(t, err)
	assert.Equal(t, 0, code)

	_, err = c.Exec(context.Background(), []string{"echo", "again"}, ExecOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "consumed")

	err = c.CopyTo(context.Background(), "/guest/f", strings.NewReader("x"), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "consumed")
}

// TestClientExecTTYStdinConsumesClient asserts the documented contract that a
// TTY exec which forwarded raw stdin leaves the connection unsafe to reuse: a
// later request fails fast instead of desyncing the protocol.
func TestClientExecTTYStdinConsumesClient(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	code, err := c.Exec(context.Background(),
		[]string{"sh", "-c", "read line; echo done"},
		ExecOptions{TTY: true, Stdin: strings.NewReader("x\n")})
	require.NoError(t, err)
	assert.Equal(t, 0, code)

	_, err = c.Exec(context.Background(), []string{"echo", "again"}, ExecOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "consumed")

	err = c.CopyTo(context.Background(), "/guest/f", strings.NewReader("x"), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "consumed")
}

// blockingReader models os.Stdin: a reader that never reaches EOF until it is
// explicitly released.
type blockingReader struct{ release <-chan struct{} }

func (r blockingReader) Read([]byte) (int, error) {
	<-r.release
	return 0, io.EOF
}

// TestClientExecTTYStdinPumpStopsOnReturn checks that Exec returns promptly when
// the command exits even though the stdin reader never reaches EOF, i.e. the
// stdin pump cannot keep Exec alive.
func TestClientExecTTYStdinPumpStopsOnReturn(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)
	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	release := make(chan struct{})
	defer close(release)

	done := make(chan error, 1)
	var stdout bytes.Buffer
	go func() {
		_, err := c.Exec(context.Background(),
			[]string{"sh", "-c", "echo done"},
			ExecOptions{TTY: true, Stdin: blockingReader{release: release}, Stdout: &stdout})
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Exec did not return while stdin was still blocked")
	}
	assert.Contains(t, stdout.String(), "done")
}
