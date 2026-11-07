// Readiness: the criterion that makes a launch say an instance is up.
//
// A launcher can hand back an instance as soon as Firecracker has answered: the
// checkpoint loaded, the guest resumed, the boot configured. Every one of those
// answers describes the VMM, and none of them describes the guest behind it — a
// truncated memory image, a kernel that panics on resume and an image whose init
// never starts the agent all produce the same 204.
//
// The evidence that a guest is running is the guest's own: its agent answering
// the v2 handshake (pkg/agent) over the instance's vsock muxer. So a launch is
// conditional on a hello. An instance whose agent never answers is a failed
// launch — ErrAgentNotReady — and the launcher ends it exactly as it ends any
// other failed launch, because a caller handed a Launched it cannot use has no
// handle to clean up with.
//
// This is also where the post-restore hook lands. SBX-083's prepare op — network,
// clock, machine-id, host keys, entropy, applied inside the guest — runs after
// this hello and before the launch returns, so that a caller holding a Launched
// has an instance that is addressed rather than merely alive. The hook itself is
// identity.go: it is armed by LaunchRequest.Identity, it refuses an agent that
// does not advertise the operation, and a hook that fails fails the launch.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
)

const (
	// DefaultReadyTimeout is how long a launch waits for the guest agent's hello
	// before it gives up. It spans both kinds of launch: a resume answers in well
	// under a second, because the agent is already in the memory image that was
	// loaded, while a cold boot has to reach an init system that starts the agent
	// (the golden builder budgets minutes for a first boot of a full OS image,
	// because that boot also writes a machine-id). Thirty seconds is room for the
	// slow half without making a guest that will never answer look like a hang;
	// a launcher that knows better sets ReadyTimeout.
	DefaultReadyTimeout = 30 * time.Second

	// readyPollInterval is the pause between dials while waiting. A muxer that
	// exists but has no guest listening refuses the connection instantly, so the
	// pause is what keeps a booting guest from being dialled in a spin.
	readyPollInterval = 250 * time.Millisecond
)

// ErrAgentNotReady means an instance did not answer its readiness check: the
// guest agent behind its vsock muxer never completed the v2 handshake inside the
// launch's budget. The instance is not running a guest the node can reach, which
// is the one thing a launch promises, so this is a failed launch and not a slow
// one. The failure that came last — a refused dial, a token the guest rejected, a
// guest that closed the connection — is wrapped with it, so a caller can tell
// those apart with errors.Is.
var ErrAgentNotReady = errors.New("the guest agent did not answer hello")

// readyClient is the part of *agent.Client the readiness check uses: what the
// guest answered, and closing the connection. It is an interface for the same
// reason the launchers' own API calls are seams — a unit test has no KVM and no
// guest, so it hands back a hello and holds the loop around it (the budget, the
// retries, the error) to account. Production never replaces it.
type readyClient interface {
	Hello() agent.Hello
	Close() error
}

// readyDial is the one way a launch asks the guest whether it is there. It
// presents the launch's token when it has one; an empty token is an
// unauthenticated dial, which only a guest booted without token authentication
// answers (pkg/agent refuses the other combinations explicitly).
var readyDial = func(ctx context.Context, vsockPath string, port uint32, token string) (readyClient, error) {
	client, err := agent.DialWithToken(ctx, vsockPath, port, token)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// awaitAgentHello dials the instance's guest agent until it answers the v2
// handshake and reports what it answered. It fails with ErrAgentNotReady once
// the budget is spent, wrapping the last refusal so the reason arrives with the
// failure instead of a bare timeout.
//
// The budget is measured on the host's clock and enforced with it, so the wait
// is bounded by the launch and not by the guest: a guest whose own clock is
// wrong after a restore cannot extend it, and a caller that gives up cancels it
// rather than leaving a dial loop behind.
func awaitAgentHello(ctx context.Context, vsockPath, token string, budget time.Duration) (agent.Hello, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	var lastErr error
wait:
	for {
		client, err := readyDial(ctx, vsockPath, agent.DefaultPort, token)
		if err == nil {
			hello := client.Hello()
			// Closing is not a failure: the hello has been read, and the launch
			// holds no long-lived session of its own — the next connection is the
			// caller's to make.
			_ = client.Close()
			return hello, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			break wait
		case <-time.After(readyPollInterval):
		}
	}

	return agent.Hello{}, fmt.Errorf("%w: the guest behind %s never completed the agent handshake (waited %s): %w",
		ErrAgentNotReady, vsockPath, time.Since(started).Round(time.Millisecond), lastErr)
}

// readyTimeout is the budget a launch gives the guest agent's hello: the
// launcher's own window, or DefaultReadyTimeout when it configures none. A
// negative window is read as the default rather than as "no time at all",
// because a zero-length wait is a launch nothing could ever satisfy.
func readyTimeout(configured time.Duration) time.Duration {
	if configured <= 0 {
		return DefaultReadyTimeout
	}
	return configured
}

// vsockProblem refuses a request that cannot prove its readiness. It is one rule
// for both launchers: a launch must name the muxer its guest agent answers on —
// there is nothing else to ask — and the path is absolute, because a relative one
// would resolve against the launcher's own working directory or, in a jail,
// against a directory the caller did not choose.
func vsockProblem(vsockPath string) error {
	if strings.TrimSpace(vsockPath) == "" {
		return errors.New("vsockPath is empty: a launch proves the guest is up by its agent's hello, so it needs the muxer path that agent answers on")
	}
	if !filepath.IsAbs(vsockPath) {
		return fmt.Errorf("vsockPath %q is relative: want an absolute path, since it resolves in the instance's own namespace", vsockPath)
	}
	return nil
}
