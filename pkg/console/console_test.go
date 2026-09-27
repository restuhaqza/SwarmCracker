package console

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// safeBuffer is a goroutine-safe io.Writer used to observe console output.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// shortTempDir creates a temporary directory with a short path. Unix socket
// paths are limited to ~104 bytes on macOS, which t.TempDir() can exceed.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "scc-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func recv(t *testing.T, ch <-chan []byte) ([]byte, bool) {
	t.Helper()
	select {
	case chunk, ok := <-ch:
		return chunk, ok
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for console chunk")
		return nil, false
	}
}

func TestHubBroadcastAndSubscribe(t *testing.T) {
	h := newHub(0)
	defer h.Close()

	ch, cancel := h.Subscribe()
	defer cancel()

	h.Broadcast([]byte("hello "))
	h.Broadcast([]byte("world"))

	got, ok := recv(t, ch)
	require.True(t, ok)
	assert.Equal(t, "hello ", string(got))

	got, ok = recv(t, ch)
	require.True(t, ok)
	assert.Equal(t, "world", string(got))
}

func TestHubScrollbackCapped(t *testing.T) {
	h := newHub(4)
	defer h.Close()

	h.Broadcast([]byte("abcdef"))

	ch, cancel := h.Subscribe()
	defer cancel()

	got, ok := recv(t, ch)
	require.True(t, ok)
	assert.Equal(t, "cdef", string(got), "scrollback should keep only the most recent bytes")
}

func TestHubSlowSubscriberDoesNotBlock(t *testing.T) {
	h := newHub(0)
	defer h.Close()

	_, cancel := h.Subscribe() // never read from it
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < subBuffer*2; i++ {
			h.Broadcast([]byte("x"))
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast blocked on a slow subscriber")
	}
}

func TestHubCloseClosesSubscribers(t *testing.T) {
	h := newHub(0)
	ch, cancel := h.Subscribe()

	h.Close()
	_, ok := <-ch
	assert.False(t, ok, "subscriber channel should be closed")

	// cancel after close must not panic.
	assert.NotPanics(t, cancel)
	h.Close() // idempotent
}

func TestHubSubscribeAfterClose(t *testing.T) {
	h := newHub(0)
	h.Close()

	ch, cancel := h.Subscribe()
	defer cancel()
	_, ok := <-ch
	assert.False(t, ok)
}

func TestSocketPath(t *testing.T) {
	assert.Equal(t, "/var/run/firecracker/abc.console.sock", SocketPath("/var/run/firecracker", "abc"))
}

func TestListAndResolve(t *testing.T) {
	dir := t.TempDir()

	// Missing directory is not an error.
	ids, err := List(filepath.Join(dir, "nope"))
	require.NoError(t, err)
	assert.Empty(t, ids)

	// Create three fake consoles plus decoys.
	for _, id := range []string{"vm-bbb", "vm-aaa", "zzz-ccc"} {
		require.NoError(t, os.WriteFile(SocketPath(dir, id), nil, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "not-a-console.sockx"), nil, 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "subdir.console.sock"), 0o755))

	ids, err = List(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"vm-aaa", "vm-bbb", "zzz-ccc"}, ids, "list must be sorted and skip non-consoles")

	// Exact match.
	id, path, err := Resolve(dir, "vm-bbb")
	require.NoError(t, err)
	assert.Equal(t, "vm-bbb", id)
	assert.Equal(t, SocketPath(dir, "vm-bbb"), path)

	// Unique prefix.
	id, _, err = Resolve(dir, "zzz")
	require.NoError(t, err)
	assert.Equal(t, "zzz-ccc", id)

	// Ambiguous prefix (both vm-aaa and vm-bbb).
	_, _, err = Resolve(dir, "vm-")
	assert.ErrorIs(t, err, ErrAmbiguous)

	// Not found.
	_, _, err = Resolve(dir, "qqq")
	assert.ErrorIs(t, err, ErrNoConsole)

	// Empty reference.
	_, _, err = Resolve(dir, "")
	assert.ErrorIs(t, err, ErrNoConsole)
}

func TestResolveNoConsoles(t *testing.T) {
	_, _, err := Resolve(t.TempDir(), "anything")
	assert.ErrorIs(t, err, ErrNoConsole)
}

func TestNewValidation(t *testing.T) {
	_, err := New(Config{TaskID: "x"})
	assert.Error(t, err)

	_, err = New(Config{SocketDir: t.TempDir()})
	assert.Error(t, err)
}

// TestServerBridge runs a real child process (echoing shell) wired to the
// server's pipes and exercises the full path: child output -> scrollback ->
// attached client, and client input -> child.
func TestServerBridge(t *testing.T) {
	dir := shortTempDir(t)
	mirror := &safeBuffer{}

	srv, err := New(Config{SocketDir: dir, TaskID: "vm-1", Mirror: mirror})
	require.NoError(t, err)

	// The child prints "boot" then echoes its stdin back, standing in for
	// Firecracker's serial loopback.
	cmd := exec.Command("/bin/sh", "-c", "printf boot; cat")
	cmd.Stdin = srv.Stdin()
	cmd.Stdout = srv.Stdout()

	require.NoError(t, cmd.Start())
	srv.Start()

	t.Cleanup(func() {
		srv.Close()
		srv.Wait()
		_ = cmd.Wait()
	})

	// While running, the console is discoverable.
	ids, err := List(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"vm-1"}, ids)

	// Wait until the child's output reached the server (mirror + scrollback).
	require.Eventually(t, func() bool {
		return strings.Contains(mirror.String(), "boot")
	}, 3*time.Second, 10*time.Millisecond, "child output was not mirrored")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, taskID, err := Dial(ctx, dir, "vm-")
	require.NoError(t, err)
	require.Equal(t, "vm-1", taskID)
	defer conn.Close()

	// The buffered "boot" is replayed on attach.
	assert.Equal(t, "boot", readN(t, conn, len("boot")))

	// Input written by the client is echoed back by the child.
	_, err = conn.Write([]byte("ping\n"))
	require.NoError(t, err)
	assert.Equal(t, "ping\n", readN(t, conn, len("ping\n")))
}

func TestServerCloseRemovesSocket(t *testing.T) {
	dir := shortTempDir(t)
	srv, err := New(Config{SocketDir: dir, TaskID: "vm-close"})
	require.NoError(t, err)

	assert.Equal(t, "vm-close", srv.TaskID())
	assert.Equal(t, SocketPath(dir, "vm-close"), srv.SocketPath())

	_, err = os.Stat(srv.SocketPath())
	require.NoError(t, err, "socket should exist after New")

	srv.Close()
	srv.Close() // idempotent
	srv.Wait()  // wait for bridge goroutines to exit

	_, err = os.Stat(srv.SocketPath())
	assert.True(t, os.IsNotExist(err), "socket should be removed on close")
}

func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	if conn, ok := r.(net.Conn); ok {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	require.NoError(t, err, fmt.Sprintf("failed reading %d bytes", n))
	return string(buf)
}
