package sandbox

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/broker"
)

// Running one command in a sandbox.
//
// There is one way into a sandbox's guest and it is the agent path this build
// already has: dial the vsock muxer the instance answers on and speak the guest
// agent's protocol (pkg/agent). The node broker serves exec for a task that way
// (pkg/broker's exec handler), and a sandbox is not a task — so this file gives the
// control plane the same two shapes the broker gives a task: a call with writers,
// for a caller in the same process, and the broker's own streaming handler, for a
// caller over the API.
//
// A sandbox's token is presented without the caller having to know it: the host's
// copy sits beside the muxer (agent.TokenFilePath), and agent.Dial reads it. That is
// the same rule the service path uses, and it is why exec needs no argument beyond
// the sandbox ID.

// ExecOptions is how one command runs.
type ExecOptions struct {
	// Stdout and Stderr receive the command's output as it arrives. Nil discards
	// it, which is what a caller that only wants the exit status passes.
	Stdout, Stderr io.Writer
	// Env is extra environment for the command, as K=V pairs. Nil inherits the
	// guest's own environment.
	Env []string
	// WorkDir is the directory the command runs in. Empty uses the guest's default.
	WorkDir string
}

// Exec runs one command in a ready sandbox and returns its exit status. A non-zero
// exit is a successful call: the command ran and its status is the answer. A sandbox
// that is not ready, and a guest that cannot be reached, are errors.
//
// Exit codes and signals are the guest's own (agent.ExecResult): a signal-terminated
// command reports 128+signal with the signal set, which is what a shell reports and
// what a caller comparing against a shell's status expects.
func (m *Manager) Exec(ctx context.Context, id string, command []string, opts ExecOptions) (agent.ExecResult, error) {
	rec, err := m.ready(id)
	if err != nil {
		return agent.ExecResult{}, err
	}
	if len(command) == 0 {
		return agent.ExecResult{}, fmt.Errorf("%w: no command to run", ErrInvalid)
	}

	client, err := agent.Dial(ctx, rec.VsockPath, agent.DefaultPort)
	if err != nil {
		return agent.ExecResult{}, fmt.Errorf("sandbox %s: reach its guest agent: %w", rec.ID, err)
	}
	defer func() { _ = client.Close() }()

	result, err := client.ExecStatus(ctx, command, agent.ExecOptions{
		Stdout:  opts.Stdout,
		Stderr:  opts.Stderr,
		Env:     opts.Env,
		WorkDir: opts.WorkDir,
	})
	if err != nil {
		return agent.ExecResult{}, fmt.Errorf("sandbox %s: run %v: %w", rec.ID, command, err)
	}

	// The command ran, so the sandbox is in use: that is what the reaper's idle
	// question is measured from. A touch that fails does not fail the exec — the
	// command's outcome is what the caller asked for — but it is worth a warning,
	// because a sandbox the node cannot record as used is one the reaper may take
	// from under its caller.
	m.touch(rec.ID)
	return result, nil
}

// ServeExec runs one public exec request (the v1 streaming exec contract) against a
// sandbox, by handing it to the node broker's own exec handler with a task that
// names the sandbox's muxer.
//
// It is the API's path into a sandbox's guest, and it is deliberately the same
// handler, the same frames and the same status codes the task route uses: a caller
// that can follow an NDJSON exec stream for a task can follow one for a sandbox, and
// the sandbox route cannot drift from the task route because it does not have its
// own implementation of exec.
//
// The error it returns is the pre-flight: a sandbox this node does not have, or one
// that is not ready. Everything after the dial is the handler's to report in-stream,
// exactly as it is for a task.
func (m *Manager) ServeExec(ctx context.Context, id string, w http.ResponseWriter, r *http.Request) error {
	rec, err := m.ready(id)
	if err != nil {
		return err
	}

	handler := broker.NewExecHandler(broker.DialAgent)
	handler(ctx, broker.SandboxTask(rec.ID, rec.VsockPath), w, r)

	// The request reached the guest, so the sandbox is in use (see Exec).
	m.touch(rec.ID)
	return nil
}

// ready resolves a sandbox that can run a command, or says why it cannot. It is the
// one place the three answers — no such sandbox, a sandbox that is not ready, and a
// sandbox whose guest has no muxer to dial — are told apart, so the CLI and the API
// cannot disagree about them.
func (m *Manager) ready(id string) (Record, error) {
	rec, err := m.Get(id)
	if err != nil {
		return Record{}, err
	}
	if rec.State != StateReady {
		return Record{}, fmt.Errorf("%w: sandbox %s is %s: %s", ErrNotReady, rec.ID, rec.State, reasonOf(rec))
	}
	if rec.VsockPath == "" {
		return Record{}, fmt.Errorf("%w: sandbox %s has no guest agent to reach", ErrNotReady, rec.ID)
	}
	return rec, nil
}

// reasonOf renders a record's reason for a caller, or a phrase for a sandbox that
// has none.
func reasonOf(rec Record) string {
	if rec.Reason != "" {
		return rec.Reason
	}
	return "it is not usable"
}

// touch records that a sandbox was used, which is what the reaper's idle question is
// measured from. It is best effort: an exec that ran must not be reported as failed
// because the node could not write a timestamp, and a missed touch costs a sandbox a
// longer life at worst.
func (m *Manager) touch(id string) {
	rec, found, err := m.cfg.Store.Get(id)
	if err != nil || !found {
		return
	}
	now := m.now().UTC()
	rec.LastActive, rec.Updated = now, now
	if err := m.cfg.Store.Put(rec); err != nil {
		log.Warn().Err(err).Str("sandbox_id", id).
			Msg("Could not record that the sandbox was used; the reaper may see it as idle")
	}
}
