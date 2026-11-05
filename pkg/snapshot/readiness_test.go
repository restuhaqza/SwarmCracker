package snapshot

// The readiness criterion on its own: what a launch waits for, how long it waits,
// and what it says when the wait runs out. A unit test cannot run a guest, so it
// replaces the dial with a muxer that refuses, answers late, or answers at once, and
// holds the loop around it to account — the retries inside the budget, the budget
// itself, the typed failure, and the connection the check must not leak.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
)

// fakeReadyDialer is a guest agent behind a muxer. answerOn is the 1-based attempt
// it starts answering on, and zero means it never does, which is a guest whose agent
// is absent, stopped, or refusing what the launch presents.
type fakeReadyDialer struct {
	answerOn int
	hello    agent.Hello
	dialErr  error

	attempts []readyCall
	closed   int
}

// install replaces the dial for one test and restores it afterwards.
func (f *fakeReadyDialer) install(t *testing.T) {
	t.Helper()

	previous := readyDial
	t.Cleanup(func() { readyDial = previous })

	readyDial = func(_ context.Context, vsockPath string, port uint32, token string) (readyClient, error) {
		f.attempts = append(f.attempts, readyCall{vsock: vsockPath, port: port, token: token})
		if f.answerOn == 0 || len(f.attempts) < f.answerOn {
			return nil, f.dialErr
		}
		return fakeReadyClient{hello: f.hello, closed: &f.closed}, nil
	}
}

// TestAwaitAgentHelloKeepsAskingUntilTheGuestAnswers pins the retry a booting guest
// needs: the muxer is there and refuses the first connections (Firecracker drops a
// host connection while nothing listens on the guest port), and the answer that
// arrives later is what the launch returns.
func TestAwaitAgentHelloKeepsAskingUntilTheGuestAnswers(t *testing.T) {
	hello := agent.Hello{Version: agent.Version, AgentVersion: "1.0.0", BootID: "boot-late"}
	dialer := &fakeReadyDialer{
		answerOn: 3,
		hello:    hello,
		dialErr:  errors.New("dial vsock socket: connect: connection refused"),
	}
	dialer.install(t)

	got, err := awaitAgentHello(context.Background(), "/run/firecracker/agent.vsock", "", 2*time.Second)

	require.NoError(t, err)
	assert.Equal(t, hello, got, "the answer comes from the guest, not from the launcher")
	assert.Equal(t, 3, len(dialer.attempts), "the first two refusals are retried, not fatal")
	assert.Equal(t, "/run/firecracker/agent.vsock", dialer.attempts[0].vsock)
	assert.Equal(t, agent.DefaultPort, dialer.attempts[0].port)
	assert.Equal(t, 1, dialer.closed, "the connection the check opened is closed")
}

// TestAwaitAgentHelloIsBoundedByItsBudget pins the other half: a guest that never
// answers ends the wait, keeps retrying inside the budget, and fails with the typed
// error rather than hanging or succeeding late.
func TestAwaitAgentHelloIsBoundedByItsBudget(t *testing.T) {
	const budget = 700 * time.Millisecond
	refused := errors.New("dial vsock socket: connect: no such file or directory")
	dialer := &fakeReadyDialer{dialErr: refused}
	dialer.install(t)

	started := time.Now()
	_, err := awaitAgentHello(context.Background(), "/run/firecracker/agent.vsock", "", budget)
	elapsed := time.Since(started)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAgentNotReady, "the failure is typed: a caller branches on it without reading the text")
	assert.ErrorIs(t, err, refused, "and the refusal that came last is still reachable")
	assert.Contains(t, err.Error(), "/run/firecracker/agent.vsock", "the failure names the muxer it dialled")
	assert.GreaterOrEqual(t, len(dialer.attempts), 3,
		"the budget is spent asking, not waiting for one attempt: a guest that starts its agent after a second still gets in")
	assert.Less(t, elapsed, 5*time.Second, "the wait is bounded by the budget, not by the dial's own retry window")
	assert.Zero(t, dialer.closed, "a connection that was never established is not closed")
}

// TestAwaitAgentHelloStopsWhenTheCallerGivesUp pins that the wait is the caller's to
// end: a launch that is cancelled must not keep dialing, and it still reports the
// typed failure.
func TestAwaitAgentHelloStopsWhenTheCallerGivesUp(t *testing.T) {
	dialer := &fakeReadyDialer{dialErr: errors.New("connection refused")}
	dialer.install(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	_, err := awaitAgentHello(ctx, "/run/firecracker/agent.vsock", "", 30*time.Second)
	elapsed := time.Since(started)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAgentNotReady)
	assert.Less(t, elapsed, 5*time.Second, "a cancelled launch must not spend the rest of its budget")
	assert.NotZero(t, len(dialer.attempts))
}

// TestAwaitAgentHelloReportsTheGuestsRefusal types the failures a caller has to tell
// apart: a guest that requires a token the launch does not carry refuses the
// handshake, and the launch fails as not ready with pkg/agent's own error wrapped, so
// the answer names the token rather than a timeout.
func TestAwaitAgentHelloReportsTheGuestsRefusal(t *testing.T) {
	dialer := &fakeReadyDialer{dialErr: agent.ErrTokenRequired}
	dialer.install(t)

	_, err := awaitAgentHello(context.Background(), "/run/firecracker/agent.vsock", "", 100*time.Millisecond)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAgentNotReady)
	assert.ErrorIs(t, err, agent.ErrTokenRequired)
	assert.Contains(t, err.Error(), "token", "the reason is in the message too")
}

// TestReadyTimeoutDefaults pins the one window a launcher that configures none gets,
// and that a window of zero or less is read as "the default" rather than as a launch
// no guest could pass.
func TestReadyTimeoutDefaults(t *testing.T) {
	assert.Equal(t, DefaultReadyTimeout, readyTimeout(0))
	assert.Equal(t, DefaultReadyTimeout, readyTimeout(-time.Second))
	assert.Equal(t, 5*time.Second, readyTimeout(5*time.Second))
}

// TestVsockProblem pins the one rule both launchers share for the muxer path: it must
// be named, and it must be absolute, because a relative path resolves against
// whatever directory the launcher or the jail happens to be in.
func TestVsockProblem(t *testing.T) {
	assert.NoError(t, vsockProblem("/run/firecracker/agent.vsock"))

	err := vsockProblem("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vsockPath is empty")

	err = vsockProblem("   ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vsockPath is empty")

	err = vsockProblem("run/agent.vsock")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is relative")
}
