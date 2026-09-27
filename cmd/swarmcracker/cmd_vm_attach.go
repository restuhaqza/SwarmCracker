package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/console"
	"github.com/spf13/cobra"
)

// defaultConsoleSocketDir mirrors the daemon's default --socket-dir.
const defaultConsoleSocketDir = "/var/run/firecracker"

// detachPrefix and detachSuffix form the Ctrl-P Ctrl-Q detach sequence, which
// is intercepted locally instead of being forwarded to the guest.
const (
	detachPrefix = 0x10 // Ctrl-P
	detachSuffix = 0x11 // Ctrl-Q
)

// errDetach signals that the user asked to detach from the console.
var errDetach = errors.New("console detached")

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

Press Ctrl-P Ctrl-Q to detach; the VM keeps running.`,
		Example: `  swarmcracker task ls
  swarmcracker vm attach 5f3a1b2c9d
  swarmcracker vm attach --socket-dir /var/run/firecracker ubuntu`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return attachToVM(cmd.Context(), socketDir, args[0], os.Stdin, os.Stdout)
		},
	}

	cmd.Flags().StringVar(&socketDir, "socket-dir", defaultConsoleSocketDir, "Directory containing microVM console sockets")

	return cmd
}

// attachToVM connects to a VM console and proxies bytes between the local
// terminal and the guest until the console closes or the user detaches.
func attachToVM(ctx context.Context, socketDir, ref string, in, out *os.File) error {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, taskID, err := console.Dial(dialCtx, socketDir, ref)
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

// pumpStdin forwards local input to the guest, intercepting the Ctrl-P Ctrl-Q
// detach sequence. The prefix may straddle two reads, so one pending byte of
// state is carried between reads.
func pumpStdin(r io.Reader, w io.Writer) error {
	buf := make([]byte, 1024)
	pendingCtrlP := false

	for {
		n, err := r.Read(buf)
		if n > 0 {
			out := make([]byte, 0, n+1)
			for i := 0; i < n; i++ {
				b := buf[i]
				if pendingCtrlP {
					pendingCtrlP = false
					if b == detachSuffix {
						return errDetach
					}
					out = append(out, detachPrefix)
				}
				if b == detachPrefix {
					pendingCtrlP = true
					continue
				}
				out = append(out, b)
			}
			if len(out) > 0 {
				if _, werr := w.Write(out); werr != nil {
					return werr
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
