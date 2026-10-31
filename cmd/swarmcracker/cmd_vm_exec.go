package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/client"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/spf13/cobra"
)

// exitCodeError carries a guest process's exit code so the CLI can exit with
// the same status. `vm exec` returns it when the guest command exits non-zero.
type exitCodeError struct {
	code int
	// signal is the terminating signal when the guest process was killed by
	// one. The code is then 128+signal.
	signal int
}

// Error implements error. The wording mirrors a shell's report for a non-zero
// exit, so a caller that does print it sees something familiar.
func (e *exitCodeError) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

// cliExitCode maps a root-command error to the process exit status: a guest
// exit code is propagated verbatim, any other error is a generic failure (1).
func cliExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec *exitCodeError
	if errors.As(err, &ec) && ec.code > 0 {
		return ec.code
	}
	return output.ExitCode(err)
}

// exitCodeMode selects the exit-status convention of `vm exec`.
type exitCodeMode string

const (
	// exitCodeGuest passes the guest process status through (the default).
	exitCodeGuest exitCodeMode = "guest"
	// exitCodeAPI follows the API convention: any non-zero normal exit is 8, so
	// a caller can distinguish "the command failed" from "the API call failed"
	// (which uses the contract's 1-7 range). A signal keeps 128+signal.
	exitCodeAPI exitCodeMode = "api"

	// apiFailureExitCode is the API convention's status for a non-zero guest
	// exit.
	apiFailureExitCode = 8
)

// parseExitCodeMode validates the --exit-code flag.
func parseExitCodeMode(raw string) (exitCodeMode, error) {
	switch mode := exitCodeMode(raw); mode {
	case "", exitCodeGuest:
		return exitCodeGuest, nil
	case exitCodeAPI:
		return exitCodeAPI, nil
	default:
		return "", output.NewErrorf(output.CodeUsage,
			"--exit-code must be %q or %q, got %q", exitCodeGuest, exitCodeAPI, raw)
	}
}

// execOutcome is the guest process result both the local and remote paths
// produce, before the mode decides the CLI status.
type execOutcome struct {
	code   int
	signal int
}

// execExitError maps an outcome to the CLI error for the selected mode. A nil
// return means success.
func execExitError(outcome execOutcome, mode exitCodeMode) error {
	if outcome.signal > 0 {
		return &exitCodeError{code: v1.SignalExitCode(outcome.signal), signal: outcome.signal}
	}
	if outcome.code == 0 {
		return nil
	}
	if mode == exitCodeAPI {
		return &exitCodeError{code: apiFailureExitCode}
	}
	return &exitCodeError{code: outcome.code}
}

// remoteExecer is the remote API path `vm exec` uses with --host. *client.Client
// implements it; tests substitute a fake.
type remoteExecer interface {
	ExecTask(ctx context.Context, ref string, req v1.ExecRequest, stdout, stderr io.Writer) (client.ExecResult, error)
}

// vmExecOptions are the inputs to execInVM. They are kept separate from the
// cobra command so the behaviour can be exercised directly in tests.
type vmExecOptions struct {
	// socketDir holds the task's vsock muxers; stateDir is the node state
	// directory the restored-instance records live in and decides which muxer a
	// task started from a migration artifact is dialed on. An empty stateDir
	// derives the path from the task ID, i.e. the behaviour for every task this
	// node booted itself.
	socketDir string
	stateDir  string
	command   []string
	env       []string
	workdir   string
	tty       bool
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
	// rawFD is the local terminal file descriptor to put into raw mode for a
	// TTY session; a negative value disables raw mode (for example a pipe).
	rawFD int
	// exitMode selects the exit-status convention. The zero value is "guest".
	exitMode exitCodeMode
	// remote, when non-nil, runs the command through the public API instead of
	// the local vsock socket. It is set when --host is present.
	remote remoteExecer
}

// newVMExecCommand creates the VM exec command.
func newVMExecCommand() *cobra.Command {
	var (
		socketDir   string
		stateDir    string
		interactive bool
		tty         bool
		env         []string
		workdir     string
		exitCode    string
	)

	cmd := &cobra.Command{
		Use:   "exec [flags] <vm> -- <command> [args...]",
		Short: "Run a command inside a running microVM",
		Long: `Run a command inside a running microVM over its guest agent.

<vm> is a task ID, or any unique prefix of one (for example the 12-character ID
shown by "swarmcracker task ls"). Everything after "--" is the command and its
arguments, passed to the guest unchanged.

Without --host the command runs over the local guest-agent vsock socket, which
requires running on the node that hosts the task. A task that was moved to this
node by "vm migrate" runs a VM that still answers on the source task's muxer, so
the local path reads the node's restored-instance record (--state-dir, default
$SWARM_STATE_DIR or /var/lib/swarmkit) instead of deriving a path from the task
ID. With --host it goes through the manager's API (POST /v1/tasks/{id}/exec), so
a CI runner needs only an API token and the cluster CA.

Stdin is forwarded to the guest only with -t: the guest agent allocates a
pseudo-terminal for the command and the local terminal is put into raw mode for
the duration. Without -t the command runs with its stdin closed, because the
protocol carries no stdin channel for non-TTY commands. Interactive TTY over the
API is not implemented yet: a remote -t returns the API's typed 501.

The guest command's exit code becomes the exit code of "swarmcracker vm exec" by
default. --exit-code=api instead maps any non-zero guest exit to 8; a signal
stays 128+signal in both modes.`,
		Example: `  swarmcracker vm exec 5f3a1b2c9d -- uname -a
  swarmcracker vm exec -e APP=prod -w /srv myvm -- env
  swarmcracker vm exec -it myvm -- /bin/sh
  swarmcracker -H https://manager:4244 --ca ca.crt --token-file token vm exec myvm -- go test ./...
  swarmcracker -H https://manager:4244 --ca ca.crt --token-file token vm exec --exit-code=api myvm -- make ci`,
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return fmt.Errorf("no command specified; use `swarmcracker vm exec <vm> -- <command>`")
			}
			if interactive && !tty {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: -i has no effect without -t (stdin forwarding requires a guest TTY)")
			}

			mode, err := parseExitCodeMode(exitCode)
			if err != nil {
				return err
			}

			// --host selects the public API. Without it the command stays on the
			// local vsock path, exactly as before.
			var remote remoteExecer
			if apiHost != "" {
				c, err := newContractClient()
				if err != nil {
					return err
				}
				defer func() { _ = c.Close() }()
				remote = c
			}

			in := cmd.InOrStdin()
			rawFD := -1
			if f, ok := in.(*os.File); ok {
				rawFD = int(f.Fd())
			}

			return execInVM(cmd.Context(), args[0], vmExecOptions{
				socketDir: socketDir,
				stateDir:  stateDir,
				command:   args[1:],
				env:       env,
				workdir:   workdir,
				tty:       tty,
				stdin:     in,
				stdout:    cmd.OutOrStdout(),
				stderr:    cmd.ErrOrStderr(),
				rawFD:     rawFD,
				exitMode:  mode,
				remote:    remote,
			})
		},
	}

	cmd.Flags().StringVar(&socketDir, "socket-dir", defaultConsoleSocketDir, "Directory containing microVM vsock sockets")
	cmd.Flags().StringVar(&stateDir, "state-dir", defaultLocalStateDir(),
		"Node state directory holding the restored-instance records of migrated tasks")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Keep stdin open (requires -t)")
	cmd.Flags().BoolVarP(&tty, "tty", "t", false, "Allocate a pseudo-terminal in the guest")
	cmd.Flags().StringArrayVarP(&env, "env", "e", nil, "Set an environment variable in the guest (repeatable, K=V)")
	cmd.Flags().StringVarP(&workdir, "workdir", "w", "", "Working directory in the guest")
	cmd.Flags().StringVar(&exitCode, "exit-code", string(exitCodeGuest),
		"Exit status convention: guest (pass the guest code through) or api (non-zero exits as 8)")

	return cmd
}

// execInVM runs a one-shot command in the guest agent of a running microVM and
// returns an exitCodeError when the command exits non-zero.
func execInVM(ctx context.Context, ref string, opts vmExecOptions) error {
	outcome, err := execInTarget(ctx, ref, opts)
	if err != nil {
		return err
	}
	return execExitError(outcome, opts.exitMode)
}

// execInTarget dispatches to the remote API or the local vsock path.
func execInTarget(ctx context.Context, ref string, opts vmExecOptions) (execOutcome, error) {
	if opts.remote != nil {
		return execRemote(ctx, ref, opts)
	}
	return execLocal(ctx, ref, opts)
}

// execRemote runs the command through the public API. TTY is forwarded as-is:
// the API owns the 501 for it, so the CLI does not duplicate the contract.
func execRemote(ctx context.Context, ref string, opts vmExecOptions) (execOutcome, error) {
	env, err := envMap(opts.env)
	if err != nil {
		return execOutcome{}, err
	}

	result, err := opts.remote.ExecTask(ctx, ref, v1.ExecRequest{
		Command: opts.command,
		Env:     env,
		WorkDir: opts.workdir,
		TTY:     opts.tty,
		Stdin:   opts.tty && opts.stdin != nil,
	}, opts.stdout, opts.stderr)
	if err != nil {
		return execOutcome{}, err
	}
	return execOutcome{code: result.ExitCode, signal: result.Signal}, nil
}

// envMap converts repeatable K=V flags into the API's environment map. A
// malformed pair is a usage error rather than something the guest discovers.
func envMap(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}

	env := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if !found || strings.TrimSpace(key) == "" {
			return nil, output.NewErrorf(output.CodeUsage,
				"environment variable %q must be K=V", pair)
		}
		env[key] = value
	}
	return env, nil
}

// execLocal runs the command over the guest-agent vsock socket.
func execLocal(ctx context.Context, ref string, opts vmExecOptions) (execOutcome, error) {
	taskID, err := resolveLiveVMTaskID(opts.socketDir, ref)
	if err != nil {
		return execOutcome{}, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// A task started from a migration artifact answers on the muxer its
	// restored-instance record names, which belongs to the source task's VM;
	// every other task derives it from its own ID.
	client, err := agent.Dial(dialCtx, localVsockPath(opts.socketDir, opts.stateDir, taskID), agent.DefaultPort)
	if err != nil {
		return execOutcome{}, err
	}
	defer client.Close()

	execOpts := agent.ExecOptions{
		TTY:     opts.tty,
		Stdout:  opts.stdout,
		Stderr:  opts.stderr,
		Env:     opts.env,
		WorkDir: opts.workdir,
	}
	if opts.tty {
		// The protocol has no stdin frame for non-TTY commands, so stdin is only
		// wired up when the guest allocates a TTY (which is when -i matters too).
		execOpts.Stdin = opts.stdin
		restore, _ := makeRaw(opts.rawFD)
		defer restore()
	}

	result, err := client.ExecStatus(ctx, opts.command, execOpts)
	if err != nil {
		return execOutcome{}, err
	}
	return execOutcome{code: result.ExitCode, signal: result.Signal}, nil
}
