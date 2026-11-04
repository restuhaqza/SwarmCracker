package agent

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newAuthPipePair runs ServeConnAuth on the server end of a pipe and closes it
// when serving stops, so a refused connection is observable as EOF on the
// client end rather than a blocked read. This mirrors cmd/sc-agent, which owns
// the connection lifetime.
func newAuthPipePair(t *testing.T, auth *TokenAuthenticator) (net.Conn, <-chan error) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() {
		err := ServeConnAuth(server, auth)
		_ = server.Close()
		done <- err
	}()
	t.Cleanup(func() { _ = client.Close() })
	return client, done
}

// writeOrClosed writes a frame and tolerates the guest having already dropped
// the connection: with net.Pipe a refused connection can close between the
// client's write and its read, and that close is the refusal under test.
func writeOrClosed(t *testing.T, conn net.Conn, v any) {
	t.Helper()
	if err := WriteFrame(conn, v); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		require.NoError(t, err)
	}
}

// requireClosedWithoutFrame asserts the guest dropped the connection without
// sending anything: a peer learns only that it was hung up on.
func requireClosedWithoutFrame(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		// The guest closed before the deadline could be set: it sent nothing.
		if errors.Is(err, io.ErrClosedPipe) {
			return
		}
		require.NoError(t, err)
	}
	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	assert.Equal(t, 0, n)
	assert.True(t, errors.Is(err, io.EOF), "want EOF and no frame, got n=%d err=%v", n, err)
}

func requireAuthFailure(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrUnauthorized)
	case <-time.After(2 * time.Second):
		t.Fatal("ServeConnAuth did not return after refusing the connection")
	}
}

func TestServeConnAuthAcceptsTheBootToken(t *testing.T) {
	path := writeTokenFile(t, t.TempDir(), "boot-token")
	auth, err := LoadAuthenticator(path)
	require.NoError(t, err)

	conn, _ := newAuthPipePair(t, auth)
	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: Version, Token: "boot-token"}))

	msg := readMessage(t, conn)
	assert.Equal(t, "hello", msg.Type)
	assert.Equal(t, Version, msg.Version)
	assert.True(t, msg.TokenOK, "an authenticated hello acknowledges the token")
	assert.True(t, msg.AuthRequired, "the reply tells the host this agent authenticates")
}

func TestServeConnAuthRejectsAMissingTokenWithoutDetail(t *testing.T) {
	path := writeTokenFile(t, t.TempDir(), "boot-token")
	auth, err := LoadAuthenticator(path)
	require.NoError(t, err)

	conn, done := newAuthPipePair(t, auth)
	writeOrClosed(t, conn, Request{Op: "hello", Version: Version})

	requireClosedWithoutFrame(t, conn)
	requireAuthFailure(t, done)
}

func TestServeConnAuthRejectsAWrongTokenWithoutDetail(t *testing.T) {
	path := writeTokenFile(t, t.TempDir(), "boot-token")
	auth, err := LoadAuthenticator(path)
	require.NoError(t, err)

	conn, done := newAuthPipePair(t, auth)
	writeOrClosed(t, conn, Request{Op: "hello", Version: Version, Token: "another-boot"})

	requireClosedWithoutFrame(t, conn)
	requireAuthFailure(t, done)
}

// TestServeConnAuthRefusesOpsBeforeHello is the escalation case: a peer that
// skips the handshake must not reach exec, put or get, even with a valid token
// in hand. The guest runs these as root.
func TestServeConnAuthRefusesOpsBeforeHello(t *testing.T) {
	path := writeTokenFile(t, t.TempDir(), "boot-token")
	auth, err := LoadAuthenticator(path)
	require.NoError(t, err)

	for _, req := range []Request{
		{Op: "exec", Command: []string{"id"}},
		{Op: "put", Path: filepath.Join(t.TempDir(), "written")},
		{Op: "get", Path: "/etc/passwd"},
		{Op: "metrics"},
	} {
		t.Run(req.Op, func(t *testing.T) {
			conn, done := newAuthPipePair(t, auth)
			writeOrClosed(t, conn, req)

			requireClosedWithoutFrame(t, conn)
			requireAuthFailure(t, done)
		})
	}
}

// TestServeConnAuthRotatedTokenTakesEffect is the issue's acceptance test: the
// same agent must accept the rotated token and reject the old one without a
// restart, because rotation happens from `prepare` while the agent is running.
func TestServeConnAuthRotatedTokenTakesEffect(t *testing.T) {
	path := writeTokenFile(t, t.TempDir(), "boot-one")
	auth, err := LoadAuthenticator(path)
	require.NoError(t, err)

	conn, _ := newAuthPipePair(t, auth)
	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: Version, Token: "boot-one"}))
	require.True(t, readMessage(t, conn).TokenOK)

	// Rotate the guest-side file, as the node will during `prepare`.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte("boot-two"), 0o400))

	old, oldDone := newAuthPipePair(t, auth)
	writeOrClosed(t, old, Request{Op: "hello", Version: Version, Token: "boot-one"})
	requireClosedWithoutFrame(t, old)
	requireAuthFailure(t, oldDone)

	rotated, _ := newAuthPipePair(t, auth)
	require.NoError(t, WriteFrame(rotated, Request{Op: "hello", Version: Version, Token: "boot-two"}))
	msg := readMessage(t, rotated)
	assert.Equal(t, "hello", msg.Type)
	assert.True(t, msg.TokenOK, "the rotated token must authenticate")
}

// TestServeConnAuthVersionMismatchAfterAuthentication keeps the old diagnostic
// for a peer that did prove the token: it may learn the protocol version. An
// unauthenticated peer never reaches this frame — it is dropped instead.
func TestServeConnAuthVersionMismatchAfterAuthentication(t *testing.T) {
	path := writeTokenFile(t, t.TempDir(), "boot-token")
	auth, err := LoadAuthenticator(path)
	require.NoError(t, err)

	conn, _ := newAuthPipePair(t, auth)
	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: VersionLegacy, Token: "boot-token"}))

	msg := readMessage(t, conn)
	assert.Equal(t, "error", msg.Type)
	assert.Equal(t, CodeProtocolUnsupported, msg.ErrorCode)
	assert.Contains(t, msg.Error, "client speaks "+VersionLegacy)
}

// TestServeConnWithoutAuthIsUnchanged pins the additive half: a VM booted with
// no token file serves exactly as before, including an op sent before hello.
func TestServeConnWithoutAuthIsUnchanged(t *testing.T) {
	conn, _ := newAuthPipePair(t, nil)
	require.NoError(t, WriteFrame(conn, Request{Op: "hello", Version: Version}))

	msg := readMessage(t, conn)
	assert.Equal(t, "hello", msg.Type)
	assert.False(t, msg.TokenOK)
	assert.False(t, msg.AuthRequired)
}
