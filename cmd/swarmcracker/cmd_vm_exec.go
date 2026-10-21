package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/spf13/cobra"
)

// exitCodeError carries a guest process's exit code so the CLI can exit with
// the same status. `vm exec` returns it when the guest command exits non-zero.
type exitCodeError struct {
	code int
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

// vmExecOptions are the inputs to execInVM. They are kept separate from the
// cobra command so the behaviour can be exercised directly in tests.
type vmExecOptions struct {
	socketDir string
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
}

// newVMExecCommand creates the VM exec command.
func newVMExecCommand() *cobra.Command {
	var (
		socketDir   string
		interactive bool
		tty         bool
		env         []string
		workdir     string
	)

	cmd := &cobra.Command{
		Use:   "exec [flags] <vm> -- <command> [args...]",
		Short: "Run a command inside a running microVM",
		Long: `Run a command inside a running microVM over its guest agent.

<vm> is a task ID, or any unique prefix of one (for example the 12-character ID
shown by "swarmcracker task ls"). Everything after "--" is the command and its
arguments, passed to the guest unchanged.

Stdin is forwarded to the guest only with -t: the guest agent allocates a
pseudo-terminal for the command and the local terminal is put into raw mode for
the duration. Without -t the command runs with its stdin closed, because the
protocol carries no stdin channel for non-TTY commands.

The guest command's exit code becomes the exit code of "swarmcracker vm exec".`,
		Example: `  swarmcracker vm exec 5f3a1b2c9d -- uname -a
  swarmcracker vm exec -e APP=prod -w /srv myvm -- env
  swarmcracker vm exec -it myvm -- /bin/sh`,
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

			in := cmd.InOrStdin()
			rawFD := -1
			if f, ok := in.(*os.File); ok {
				rawFD = int(f.Fd())
			}

			return execInVM(cmd.Context(), args[0], vmExecOptions{
				socketDir: socketDir,
				command:   args[1:],
				env:       env,
				workdir:   workdir,
				tty:       tty,
				stdin:     in,
				stdout:    cmd.OutOrStdout(),
				stderr:    cmd.ErrOrStderr(),
				rawFD:     rawFD,
			})
		},
	}

	cmd.Flags().StringVar(&socketDir, "socket-dir", defaultConsoleSocketDir, "Directory containing microVM vsock sockets")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Keep stdin open (requires -t)")
	cmd.Flags().BoolVarP(&tty, "tty", "t", false, "Allocate a pseudo-terminal in the guest")
	cmd.Flags().StringArrayVarP(&env, "env", "e", nil, "Set an environment variable in the guest (repeatable, K=V)")
	cmd.Flags().StringVarP(&workdir, "workdir", "w", "", "Working directory in the guest")

	return cmd
}

// execInVM runs a one-shot command in the guest agent of a running microVM and
// returns an exitCodeError when the command exits non-zero.
func execInVM(ctx context.Context, ref string, opts vmExecOptions) error {
	taskID, err := resolveLiveVMTaskID(opts.socketDir, ref)
	if err != nil {
		return err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client, err := agent.Dial(dialCtx, filepath.Join(opts.socketDir, taskID+".vsock"), agent.DefaultPort)
	if err != nil {
		return err
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

	code, err := client.Exec(ctx, opts.command, execOpts)
	if err != nil {
		return err
	}
	if code != 0 {
		return &exitCodeError{code: code}
	}
	return nil
}
