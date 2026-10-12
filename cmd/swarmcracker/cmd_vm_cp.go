package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/spf13/cobra"
)

// errCopyDirectory reports that a directory was given where a regular file is
// required. The guest agent protocol has no directory operations.
var errCopyDirectory = errors.New("copying directories is not supported yet")

// newVMCpCommand creates the VM cp command.
func newVMCpCommand() *cobra.Command {
	var (
		socketDir string
		modeStr   string
	)

	cmd := &cobra.Command{
		Use:   "cp <src> <vm>:<dst> | <vm>:<src> <dst>",
		Short: "Copy a file between the host and a running microVM",
		Long: `Copy a file between the host and a running microVM over its guest agent.

Exactly one of the two arguments carries a "<vm>:" prefix, which selects the
direction:

  swarmcracker vm cp ./local.txt myvm:/tmp/remote.txt   # host -> guest
  swarmcracker vm cp myvm:/tmp/remote.txt ./local.txt   # guest -> host

<vm> is a task ID, or any unique prefix of one (for example the 12-character ID
shown by "swarmcracker task ls"). Only regular files are supported; copying
directories is not supported yet.

The first ":" in an argument selects the direction, so a local path may not
contain a colon.`,
		Example: `  swarmcracker vm cp ./config.yaml 5f3a1b2c9d:/etc/app/config.yaml
  swarmcracker vm cp --mode 0600 ./secret myvm:/root/secret
  swarmcracker vm cp myvm:/var/log/app.log ./app.log`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, err := parseFileMode(modeStr)
			if err != nil {
				return err
			}
			return copyFileToOrFromVM(cmd.Context(), socketDir, args[0], args[1], mode)
		},
	}

	cmd.Flags().StringVar(&socketDir, "socket-dir", defaultConsoleSocketDir, "Directory containing microVM vsock sockets")
	cmd.Flags().StringVar(&modeStr, "mode", "0644", "File mode for host-to-guest copies (octal)")

	return cmd
}

// parseFileMode parses an octal file mode such as "0644" or "644".
func parseFileMode(s string) (uint32, error) {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0o"), 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid file mode %q: %w", s, err)
	}
	return uint32(v), nil
}

// splitCopyArg splits a "vm:path" argument into its VM reference and guest path.
// It reports false when the argument carries no usable prefix, in which case
// the whole argument is treated as a host path.
func splitCopyArg(arg string) (ref, guestPath string, ok bool) {
	ref, guestPath, found := strings.Cut(arg, ":")
	if !found || ref == "" || guestPath == "" {
		return "", arg, false
	}
	return ref, guestPath, true
}

// copyFileToOrFromVM copies one regular file between the host and a guest. The
// direction is chosen by which argument carries the "<vm>:" prefix.
func copyFileToOrFromVM(ctx context.Context, socketDir, arg1, arg2 string, mode uint32) error {
	srcRef, srcGuestPath, srcIsGuest := splitCopyArg(arg1)
	dstRef, dstGuestPath, dstIsGuest := splitCopyArg(arg2)
	if srcIsGuest == dstIsGuest {
		return fmt.Errorf("exactly one of the two arguments must carry a \"<vm>:\" prefix (for example \"myvm:/tmp/file\")")
	}

	var ref, guestPath, localPath string
	if srcIsGuest {
		ref, guestPath, localPath = srcRef, srcGuestPath, arg2
	} else {
		ref, guestPath, localPath = dstRef, dstGuestPath, arg1
	}

	taskID, err := resolveLiveVMTaskID(socketDir, ref)
	if err != nil {
		return err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client, err := agent.Dial(dialCtx, filepath.Join(socketDir, taskID+".vsock"), agent.DefaultPort)
	if err != nil {
		return err
	}
	defer client.Close()

	if srcIsGuest {
		return copyFromVM(ctx, client, guestPath, localPath)
	}
	return copyToVM(ctx, client, localPath, guestPath, mode)
}

// copyToVM uploads a local regular file to the guest.
func copyToVM(ctx context.Context, client *agent.Client, localPath, guestPath string, mode uint32) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", localPath, err)
	}
	if info.IsDir() {
		return errCopyDirectory
	}

	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", localPath, err)
	}
	defer func() { _ = f.Close() }()

	if err := client.CopyTo(ctx, guestPath, f, mode); err != nil {
		return err
	}
	return nil
}

// copyFromVM downloads a guest file to a local path.
//
// The incoming bytes are written to a temporary file in the destination
// directory and renamed onto the destination only after the transfer
// completes, so a failed or partial transfer cannot truncate an existing local
// file. The temporary file is removed on every error path.
func copyFromVM(ctx context.Context, client *agent.Client, guestPath, localPath string) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(localPath); err == nil {
		if info.IsDir() {
			return errCopyDirectory
		}
		// Preserve the mode of an existing destination file.
		mode = info.Mode().Perm()
	}

	dir := filepath.Dir(localPath)
	tmp, err := os.CreateTemp(dir, filepath.Base(localPath)+".sc-cp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	// Best effort: a no-op once the rename below succeeds.
	defer func() { _ = os.Remove(tmpPath) }()

	if err := client.CopyFrom(ctx, guestPath, tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, localPath); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpPath, localPath, err)
	}
	return nil
}
