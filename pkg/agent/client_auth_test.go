package agent

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authenticatedProxy serves the guest side with token authentication enabled.
func authenticatedProxy(t *testing.T, guestToken string) (*fakeProxy, string) {
	t.Helper()
	path := writeTokenFile(t, t.TempDir(), guestToken)
	auth, err := LoadAuthenticator(path)
	require.NoError(t, err)
	require.NotNil(t, auth)

	return startProxy(t, DefaultPort, withServe(func(conn net.Conn) {
		_ = ServeConnAuth(conn, auth)
	}))
}

// publishHostToken writes the node's copy of a VM's token where Dial looks for
// it: next to the vsock muxer.
func publishHostToken(t *testing.T, udsPath, token string) {
	t.Helper()
	require.NoError(t, WriteTokenFile(TokenFilePath(udsPath), token))
}

func TestDialPresentsTheTokenNextToTheMuxer(t *testing.T) {
	_, uds := authenticatedProxy(t, "boot-token")
	publishHostToken(t, uds, "boot-token")

	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	var stdout bytes.Buffer
	code, err := c.Exec(context.Background(), []string{"sh", "-c", "echo ok"}, ExecOptions{Stdout: &stdout})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "ok\n", stdout.String(), "an authenticated session serves ops normally")
}

func TestDialWithTokenWorksWithoutAHostTokenFile(t *testing.T) {
	_, uds := authenticatedProxy(t, "boot-token")

	c, err := DialWithToken(context.Background(), uds, DefaultPort, "boot-token")
	require.NoError(t, err)
	require.NoError(t, c.Close())
}

// TestDialRejectsAWrongToken is the host half of the acceptance test: the guest
// hangs up without a frame, and the host reports it as an authentication
// failure rather than a protocol error.
func TestDialRejectsAWrongToken(t *testing.T) {
	_, uds := authenticatedProxy(t, "boot-token")
	publishHostToken(t, uds, "another-boot")

	_, err := Dial(context.Background(), uds, DefaultPort)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenRejected)
}

// TestDialRejectsALegacyGuestWhenATokenIsProvisioned proves the node never
// silently accepts an agent that cannot answer a v2 hello: a pre-v2 guest is
// refused as an unsupported protocol, which is the same code the doctor check
// reports as an outdated image.
func TestDialRejectsALegacyGuestWhenATokenIsProvisioned(t *testing.T) {
	_, uds := startProxy(t, DefaultPort, func(p *fakeProxy) { p.helloVer = VersionLegacy })
	publishHostToken(t, uds, "boot-token")

	_, err := Dial(context.Background(), uds, DefaultPort)
	require.Error(t, err)
	assert.Equal(t, CodeProtocolUnsupported, CodeOf(err))
}

// TestDialWithTokenRejectsALegacyGuest keeps the typed refusal on the explicit
// token path. A pre-v2 agent answers a v2 hello with a bare error frame, so the
// caller must see agent_protocol_unsupported whether it dialled with a token or
// without one — a raw string here would make the doctor check call an outdated
// guest "silent".
func TestDialWithTokenRejectsALegacyGuest(t *testing.T) {
	_, uds := startProxy(t, DefaultPort, func(p *fakeProxy) {
		p.helloErr = "agent version mismatch: client " + Version + ", guest " + VersionLegacy
	})

	_, err := DialWithToken(context.Background(), uds, DefaultPort, "boot-token")
	require.Error(t, err)
	assert.Equal(t, CodeProtocolUnsupported, CodeOf(err))
}

// TestDialRejectsAV2GuestThatIgnoresTheToken covers the other half: an agent
// that answers a v2 hello without acknowledging the token speaks a protocol
// this host understands, so the failure is named as the token problem it is
// rather than as an outdated image.
func TestDialRejectsAV2GuestThatIgnoresTheToken(t *testing.T) {
	_, uds := startProxy(t, DefaultPort, withServe(func(conn net.Conn) {
		var req Request
		_, _ = ReadFrame(conn, &req)
		reply := HelloReply(Capabilities())
		reply.ID = req.ID
		_ = WriteFrame(conn, reply)
	}))
	publishHostToken(t, uds, "boot-token")

	_, err := Dial(context.Background(), uds, DefaultPort)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenUnsupported)
}

// TestDialWithoutATokenAgainstAnAuthenticatingGuest names the failure an
// operator sees when the host copy of the token is missing.
func TestDialWithoutATokenAgainstAnAuthenticatingGuest(t *testing.T) {
	_, uds := authenticatedProxy(t, "boot-token")

	_, err := Dial(context.Background(), uds, DefaultPort)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token")
}

// TestDialWithoutTokenAuthIsUnchanged pins the additive behaviour: a VM with no
// token file dials and runs exactly as before.
func TestDialWithoutTokenAuthIsUnchanged(t *testing.T) {
	_, uds := startProxy(t, DefaultPort)

	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	var stdout bytes.Buffer
	code, err := c.Exec(context.Background(), []string{"sh", "-c", "echo plain"}, ExecOptions{Stdout: &stdout})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "plain\n", stdout.String())
}

// TestDialFailsWhenTheHostTokenFileIsUnreadable refuses to fall back to an
// unauthenticated dial: a token file the daemon cannot read is an error.
func TestDialFailsWhenTheHostTokenFileIsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-0000 file")
	}
	_, uds := authenticatedProxy(t, "boot-token")
	path := TokenFilePath(uds)
	publishHostToken(t, uds, "boot-token")
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err := Dial(context.Background(), uds, DefaultPort)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read token file")
}

// TestDialTokenFileIsPerMuxer keeps the host lookup tied to the VM's own muxer:
// a token file for another task must not authenticate this one.
func TestDialTokenFileIsPerMuxer(t *testing.T) {
	_, uds := authenticatedProxy(t, "boot-token")
	other := filepath.Join(filepath.Dir(uds), "other-task.agent-token")
	require.NoError(t, WriteTokenFile(other, "boot-token"))

	_, err := Dial(context.Background(), uds, DefaultPort)
	require.Error(t, err, "another VM's token file must not be picked up")
}

// TestDialWithoutATokenIsRefusedByAGuestThatAnnouncesAuth covers the reply shape
// of an agent that authenticates: it answers a full v2 hello but says a token is
// required, so the host refuses instead of continuing unauthenticated.
func TestDialWithoutATokenIsRefusedByAGuestThatAnnouncesAuth(t *testing.T) {
	_, uds := startProxy(t, DefaultPort, withServe(func(conn net.Conn) {
		var req Request
		_, _ = ReadFrame(conn, &req)
		reply := HelloReply(Capabilities())
		reply.ID = req.ID
		reply.AuthRequired = true
		_ = WriteFrame(conn, reply)
	}))

	_, err := Dial(context.Background(), uds, DefaultPort)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenRequired)
}

// TestDialWithTokenHonoursTheContextDeadline covers the deadline branch of the
// handshake: a bounded dial must still authenticate, and must clear the deadline
// afterwards so the session is not cut short.
func TestDialWithTokenHonoursTheContextDeadline(t *testing.T) {
	_, uds := authenticatedProxy(t, "boot-token")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := DialWithToken(ctx, uds, DefaultPort, "boot-token")
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	var stdout bytes.Buffer
	code, err := c.Exec(context.Background(), []string{"sh", "-c", "echo ok"}, ExecOptions{Stdout: &stdout})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "ok\n", stdout.String(), "the deadline must not leak into the session")
}
