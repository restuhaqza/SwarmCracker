package agent

// The client side of the hook, over a real connection.
//
// pkg/snapshot drives a launch with `Client.Prepare`; the agent's own test of the
// operation covers the server. What is left is the wire between them, which is where
// SBX-042's guarantee lives: an agent that does not advertise the operation is refused
// *before a frame is written*, so a caller can never mistake a hook that was skipped for
// one that ran.

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientPrepareRunsTheHookOverTheWire drives the operation the way a launch does: the
// host asks, the guest reads the identity its node published and applies it, and the
// reply carries what the guest holds afterwards.
func TestClientPrepareRunsTheHookOverTheWire(t *testing.T) {
	applied := installPrepareSeams(t, helloIdentity(), nil, nil)
	_, uds := startProxy(t, DefaultPort)

	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	report, err := c.Prepare(context.Background())

	require.NoError(t, err)
	assert.Equal(t, helloIdentity().MAC, report.MAC)
	assert.ElementsMatch(t, []string{PartMAC, PartBootID}, report.Changed,
		"the guest reports the parts it changed, and the boot identity it now has")
	require.Len(t, *applied, 1, "the guest applied the identity it was asked for")
	assert.Equal(t, helloIdentity(), (*applied)[0])
}

// TestClientPrepareIsRefusedBeforeAFrameIsWritten is the SBX-042 case from the host side:
// a guest that speaks protocol 2 without advertising the hook. The client refuses on the
// handshake it already has, with CodeUnsupportedOp, and writes nothing — so there is no
// window in which the guest could answer "ok" to an operation it cannot serve and the
// launch could go on to hand over an instance wearing its source's identity.
func TestClientPrepareIsRefusedBeforeAFrameIsWritten(t *testing.T) {
	frames := make(chan struct{}, 1)
	_, uds := startProxy(t, DefaultPort, withServe(func(conn net.Conn) {
		var req Request
		if _, err := ReadFrame(conn, &req); err != nil {
			return
		}
		// A protocol-2 agent with no hook: exactly what a template built before this
		// operation advertises.
		_ = WriteFrame(conn, Message{
			Type: "hello", ID: req.ID, Version: Version, Versions: []string{Version},
			BootID: "boot-b", Caps: []string{CapHello, CapExec},
		})

		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		if _, err := ReadFrame(conn, &Request{}); err == nil {
			frames <- struct{}{}
		}
	}))

	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	_, err = c.Prepare(context.Background())

	require.Error(t, err)
	assert.Equal(t, CodeUnsupportedOp, CodeOf(err))
	assert.Contains(t, err.Error(), CapPrepare, "the refusal names the operation the guest does not advertise")
	select {
	case <-frames:
		t.Fatal("the client wrote a frame for an operation the guest does not advertise")
	case <-time.After(400 * time.Millisecond):
	}
}

// TestClientPrepareReportsWhatTheGuestRefused covers the guest's own failure: a hook that
// ran and failed comes back as an error frame, and the code the guest chose survives.
func TestClientPrepareReportsWhatTheGuestRefused(t *testing.T) {
	_, uds := startRawProxy(t, DefaultPort, func(conn net.Conn) {
		var req Request
		_, _ = ReadFrame(conn, &req)
		_ = WriteFrame(conn, Message{
			Type: "error", ID: req.ID, ErrorCode: CodePrepareFailed,
			Error: "prepare: set the default route via 10.97.0.1: network is unreachable",
		})
	})

	c, err := Dial(context.Background(), uds, DefaultPort)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	_, err = c.Prepare(context.Background())

	require.Error(t, err)
	assert.Equal(t, CodePrepareFailed, CodeOf(err))
	assert.Contains(t, err.Error(), "network is unreachable")
}

// TestClientPrepareRefusesAReplyItCannotUse covers the two replies that are not a report:
// one that says it prepared and carries nothing, and one that answers a different
// operation. Both are errors rather than an empty report, because an empty report reads as
// a guest that carries nothing rather than as a broken answer.
func TestClientPrepareRefusesAReplyItCannotUse(t *testing.T) {
	tests := []struct {
		name    string
		reply   func(id string) Message
		wantErr string
	}{
		{
			name:    "a prepared frame with no report",
			reply:   func(id string) Message { return Message{Type: "prepared", ID: id} },
			wantErr: "reply carried no report",
		},
		{
			name:    "a frame for another operation",
			reply:   func(id string) Message { return Message{Type: "metrics", ID: id} },
			wantErr: `unexpected message type "metrics"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, uds := startRawProxy(t, DefaultPort, func(conn net.Conn) {
				var req Request
				_, _ = ReadFrame(conn, &req)
				_ = WriteFrame(conn, tc.reply(req.ID))
			})

			c, err := Dial(context.Background(), uds, DefaultPort)
			require.NoError(t, err)
			defer func() { _ = c.Close() }()

			_, err = c.Prepare(context.Background())

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
