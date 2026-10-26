package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/console"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/spf13/cobra"
)

// defaultConsoleSocketDir mirrors the daemon's default --socket-dir.
const defaultConsoleSocketDir = "/var/run/firecracker"

// errDetach signals that the user asked to detach from the console. It is the
// shared filter's sentinel, so the local socket path and the remote API path
// agree on the byte sequence.
var errDetach = console.ErrDetached

// newVMAttachCommand creates the VM attach command.
func newVMAttachCommand() *cobra.Command {
	var socketDir string

	cmd := &cobra.Command{
		Use:   "attach <vm>",
		Short: "Attach to a running microVM's serial console",
		Long: `Attach to the serial console of a running microVM.

<vm> is a task ID, or any unique prefix of one (for example the 12-character ID
shown by "swarmcracker task ls"). The console is the same channel Firecracker
uses for guest kernel and ttyS0 output, so for an image whose command is an
interactive shell this gives you a shell inside the microVM.

Without --host the console is reached over the node's Unix socket, which
requires running on the node that hosts the task. With --host the session goes
through the manager's API (POST /v1/tasks/{id}/attach) as one full-duplex HTTP
request carrying raw bytes both ways, so a machine with an API token and the
cluster CA needs no node access. That path requires the tasks:exec scope and the
node broker; a manager without a wired broker answers 501 not_implemented, and a
task that is not running (or has no console) answers 404 not_found with
details.reason "console_unavailable".

Press Ctrl-P Ctrl-Q to detach; the VM keeps running.`,
		Example: `  swarmcracker task ls
  swarmcracker vm attach 5f3a1b2c9d
  swarmcracker vm attach --socket-dir /var/run/firecracker ubuntu
  swarmcracker -H https://manager:4244 --ca ca.crt --token-file token vm attach web-1`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// --host selects the public API. Without it the command stays on the
			// local console socket, byte for byte as before.
			if apiHost != "" {
				c, err := newContractClient()
				if err != nil {
					return err
				}
				defer func() { _ = c.Close() }()
				return attachToRemoteVM(cmd.Context(), c, args[0],
					cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			return attachToVM(cmd.Context(), socketDir, args[0], os.Stdin, os.Stdout)
		},
	}

	cmd.Flags().StringVar(&socketDir, "socket-dir", defaultConsoleSocketDir, "Directory containing microVM console sockets")

	return cmd
}

// remoteAttacher is the remote API path `vm attach` uses with --host.
// *client.Client implements it; tests substitute a fake.
type remoteAttacher interface {
	AttachTask(ctx context.Context, ref string, stdin io.Reader, stdout io.Writer) error
}

// attachToRemoteVM streams the task console through the public API. The
// terminal handling matches the local path — raw mode when stdin is a terminal,
// Ctrl-P Ctrl-Q to leave, nothing else interpreted locally — only the channel
// differs.
func attachToRemoteVM(ctx context.Context, remote remoteAttacher, ref string, in io.Reader, out, errOut io.Writer) error {
	restore, raw := rawModeFor(in)
	defer restore()

	if raw {
		fmt.Fprintf(errOut, "Attached to %s (Ctrl-P Ctrl-Q to detach)\r\n", ref)
	}

	return remote.AttachTask(ctx, ref, in, out)
}

// rawModeFor puts a terminal stdin into raw mode for the session. A pipe or a
// redirected file is left untouched, and the caller must not assume raw mode
// was applied.
func rawModeFor(in io.Reader) (restore func(), ok bool) {
	f, isFile := in.(*os.File)
	if !isFile {
		return func() {}, false
	}
	return makeRaw(int(f.Fd()))
}

// attachToVM connects to a VM console and proxies bytes between the local
// terminal and the guest until the console closes or the user detaches.
func attachToVM(ctx context.Context, socketDir, ref string, in, out *os.File) error {
	taskID, err := resolveLiveVMTaskID(socketDir, ref)
	if err != nil {
		return err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, _, err := console.Dial(dialCtx, socketDir, taskID)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Raw mode forwards keystrokes byte-for-byte (including Ctrl-C and arrow
	// keys) to the guest instead of letting the local terminal interpret them.
	restore, raw := makeRaw(int(in.Fd()))
	defer restore()

	if raw {
		fmt.Fprintf(os.Stderr, "Attached to %s (Ctrl-P Ctrl-Q to detach)\r\n", taskID)
	}

	errCh := make(chan error, 2)
	go func() { errCh <- pumpStdin(in, conn) }()
	go func() {
		_, copyErr := io.Copy(out, conn)
		errCh <- copyErr
	}()

	err = <-errCh
	if errors.Is(err, errDetach) {
		if raw {
			fmt.Fprint(os.Stderr, "\r\nDetached\r\n")
		}
		return nil
	}
	// A nil result means stdin reached EOF (for example piped input). Keep the
	// guest's output flowing until the console itself closes.
	if err == nil {
		err = <-errCh
	}

	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

// resolveLiveVMTaskID resolves a VM reference — an exact task ID or a unique
// prefix — to the task ID of a VM whose Firecracker API socket is currently
// alive. Stale sockets from stopped VMs are ignored.
func resolveLiveVMTaskID(socketDir, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("empty VM reference")
	}

	running, err := runtime.DiscoverRunningVMs(socketDir)
	if err != nil {
		return "", err
	}
	if len(running) == 0 {
		return "", fmt.Errorf("no running VMs found under %s", socketDir)
	}

	ids := make([]string, 0, len(running))
	for _, vm := range running {
		if vm.ID == ref {
			return vm.ID, nil
		}
		ids = append(ids, vm.ID)
	}

	var matches []string
	for _, id := range ids {
		if strings.HasPrefix(id, ref) {
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%q does not match any running VM (available: %s)", ref, strings.Join(ids, ", "))
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%q matches multiple running VMs: %s", ref, strings.Join(matches, ", "))
	}
}

// pumpStdin forwards local input to the guest, intercepting the Ctrl-P Ctrl-Q
// detach sequence through the filter shared with the remote API path.
func pumpStdin(r io.Reader, w io.Writer) error {
	_, err := io.Copy(w, console.NewDetachReader(r))
	return err
}
