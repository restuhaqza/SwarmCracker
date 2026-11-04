package console

import (
	"bytes"
	"context"
	"errors"
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

	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
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
	resolver := Resolver{SocketDir: dir}

	// Missing directory is not an error.
	consoles, err := Resolver{SocketDir: filepath.Join(dir, "nope")}.List()
	require.NoError(t, err)
	assert.Empty(t, consoles)

	// Create three fake consoles plus decoys.
	for _, id := range []string{"vm-bbb", "vm-aaa", "zzz-ccc"} {
		require.NoError(t, os.WriteFile(SocketPath(dir, id), nil, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "not-a-console.sockx"), nil, 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "subdir.console.sock"), 0o755))

	consoles, err = resolver.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"vm-aaa", "vm-bbb", "zzz-ccc"}, consoleIDs(consoles),
		"list must be sorted and skip non-consoles")
	for _, c := range consoles {
		assert.False(t, c.Recorded, "a VM found by the derivation has no record")
		assert.Equal(t, SocketPath(dir, c.ID), c.Socket)
	}

	// Exact match.
	resolved, err := resolver.Resolve("vm-bbb")
	require.NoError(t, err)
	assert.Equal(t, "vm-bbb", resolved.ID)
	assert.Equal(t, SocketPath(dir, "vm-bbb"), resolved.Socket)

	// Unique prefix.
	resolved, err = resolver.Resolve("zzz")
	require.NoError(t, err)
	assert.Equal(t, "zzz-ccc", resolved.ID)

	// Ambiguous prefix (both vm-aaa and vm-bbb).
	_, err = resolver.Resolve("vm-")
	assert.ErrorIs(t, err, ErrAmbiguous)

	// Not found.
	_, err = resolver.Resolve("qqq")
	assert.ErrorIs(t, err, ErrNoConsole)

	// Empty reference.
	_, err = resolver.Resolve("")
	assert.ErrorIs(t, err, ErrNoConsole)
}

func TestResolveNoConsoles(t *testing.T) {
	_, err := Resolver{SocketDir: t.TempDir()}.Resolve("anything")
	assert.ErrorIs(t, err, ErrNoConsole)
}

// fakeInstances is the node's registry as console resolution reads it: the
// instances a test hands it, plus the one failure a listing can answer with.
type fakeInstances struct {
	instances []vminstance.Instance
	skipped   []string
	err       error
}

func (f fakeInstances) List() ([]vminstance.Instance, []string, error) {
	return f.instances, f.skipped, f.err
}

// TestResolverAnswersFromTheRecord pins the rule this change is for: the record
// names the console socket, so a VM whose console is not
// <socketDir>/<id>.console.sock resolves — and a record whose VM has no console
// is not offered the derived path a legacy VM would get.
func TestResolverAnswersFromTheRecord(t *testing.T) {
	dir := t.TempDir()

	// A sandbox VM whose console lives outside the socket directory, and a
	// pooled VM the record says has no console at all, for which a stale derived
	// socket file exists on disk.
	sandboxConsole := filepath.Join(t.TempDir(), "sandbox-1.console.sock")
	require.NoError(t, os.WriteFile(sandboxConsole, nil, 0o600))
	require.NoError(t, os.WriteFile(SocketPath(dir, "pool-1"), nil, 0o600))
	// A legacy VM: a console socket with no record behind it.
	require.NoError(t, os.WriteFile(SocketPath(dir, "legacy-1"), nil, 0o600))

	resolver := Resolver{
		SocketDir: dir,
		Instances: fakeInstances{
			instances: []vminstance.Instance{
				{ID: "sandbox-1", Console: sandboxConsole},
				{ID: "pool-1"}, // no console: the record says so
			},
			skipped: []string{filepath.Join(dir, "unreadable.json")},
		},
	}

	consoles, err := resolver.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"legacy-1", "sandbox-1"}, consoleIDs(consoles),
		"a record with no console must not be handed the derived path")

	resolved, err := resolver.Resolve("sandbox")
	require.NoError(t, err)
	assert.Equal(t, sandboxConsole, resolved.Socket, "the record's path is authoritative")
	assert.True(t, resolved.Recorded)

	resolved, err = resolver.Resolve("legacy-1")
	require.NoError(t, err)
	assert.False(t, resolved.Recorded, "a VM with no record still resolves by derivation")
	assert.Equal(t, SocketPath(dir, "legacy-1"), resolved.Socket)

	// A record answers for its own ID even when the derivation would have found a
	// file at the derived path: the record wins, and the two are the same file
	// here only because the record says so.
	resolver.Instances = fakeInstances{instances: []vminstance.Instance{
		{ID: "legacy-1", Console: sandboxConsole},
	}}
	resolved, err = resolver.Resolve("legacy-1")
	require.NoError(t, err)
	assert.Equal(t, sandboxConsole, resolved.Socket)
	assert.True(t, resolved.Recorded)
}

// TestResolverReportsARegistryFailure pins that an unreadable registry is an
// error, not an empty list: answering "no consoles" would turn a broken node into
// a node with no VMs.
func TestResolverReportsARegistryFailure(t *testing.T) {
	_, err := Resolver{Instances: fakeInstances{err: errors.New("registry is unreadable")}}.List()
	require.Error(t, err)
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
	consoles, err := Resolver{SocketDir: dir}.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"vm-1"}, consoleIDs(consoles))

	// Wait until the child's output reached the server (mirror + scrollback).
	require.Eventually(t, func() bool {
		return strings.Contains(mirror.String(), "boot")
	}, 3*time.Second, 10*time.Millisecond, "child output was not mirrored")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resolved, err := Resolver{SocketDir: dir}.Dial(ctx, "vm-")
	require.NoError(t, err)
	require.Equal(t, "vm-1", resolved.ID)
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

// consoleIDs is the sorted identity list of a listing, for assertions that are
// about which VMs were offered rather than where their sockets are.
func consoleIDs(consoles []Console) []string {
	ids := make([]string, 0, len(consoles))
	for _, c := range consoles {
		ids = append(ids, c.ID)
	}
	return ids
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
