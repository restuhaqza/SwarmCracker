package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Backward compatibility wrappers for legacy commands
// These provide deprecation warnings and redirect to new commands

const deprecationWarning = `WARNING: This command is DEPRECATED and will be removed in a future release.

Please use the new command structure instead:

  swarmcracker cluster <command>    - Cluster lifecycle (init, join, leave, token, status, health, reset, deinit)
  swarmcracker node <command>       - Node management (ls, inspect, drain, activate, promote, rm)
  swarmcracker service <command>    - Service management (ls, inspect, ps, create, update, scale, rm)
  swarmcracker task <command>       - Task management (ls, inspect)
  swarmcracker vm <command>         - VM operations (list, status, create, stop, logs, attach, snapshot)
  swarmcracker network <command>    - Network introspection (vxlan, bridge)
  swarmcracker asset <command>      - Asset management (kernel, rootfs)
  swarmcracker config <command>     - Configuration (ls, validate, migrate)
  swarmcracker metrics              - Display VM resource usage

For more information, run: swarmcracker --help
`

// showDeprecationWarning displays a deprecation warning
func showDeprecationWarning(newCommand string) {
	fmt.Fprint(os.Stderr, deprecationWarning)
	if newCommand != "" {
		fmt.Fprintf(os.Stderr, "\nNew equivalent command:\n  swarmcracker %s\n", newCommand)
	}
	fmt.Fprintln(os.Stderr)
}

// wrapDeprecated turns an existing command into a deprecated alias for
// newCommand (reported as "swarmcracker <newCommand>").
//
// It preserves the wrapped command's own PreRun hook by chaining it after the
// deprecation notice. Dropping that hook silently broke commands whose PreRun
// captures positional state — most notably `join <manager-addr>`, which stores
// the manager address in cfg.ManagerAddr. Without it, `swarmcracker join` always
// probed an empty address (":4242") and failed with "cannot reach manager".
func wrapDeprecated(cmd *cobra.Command, newCommand string) *cobra.Command {
	original := cmd.PreRun
	cmd.Deprecated = fmt.Sprintf("Use 'swarmcracker %s' instead", newCommand)
	cmd.PreRun = func(c *cobra.Command, args []string) {
		showDeprecationWarning(newCommand)
		if original != nil {
			original(c, args)
		}
	}
	return cmd
}

// newDeprecatedInitCommand creates a deprecated init command wrapper
func newDeprecatedInitCommand() *cobra.Command {
	return wrapDeprecated(newInitCommand(), "cluster init")
}

// newDeprecatedJoinCommand creates a deprecated join command wrapper
func newDeprecatedJoinCommand() *cobra.Command {
	return wrapDeprecated(newJoinCommand(), "cluster join")
}

// newDeprecatedLeaveCommand creates a deprecated leave command wrapper
func newDeprecatedLeaveCommand() *cobra.Command {
	return wrapDeprecated(newLeaveCommand(), "cluster leave")
}

// newDeprecatedDeinitCommand creates a deprecated deinit command wrapper
func newDeprecatedDeinitCommand() *cobra.Command {
	return wrapDeprecated(newDeinitCommand(), "cluster deinit")
}

// newDeprecatedResetCommand creates a deprecated reset command wrapper
func newDeprecatedResetCommand() *cobra.Command {
	return wrapDeprecated(newResetCommand(), "cluster reset")
}

// newDeprecatedRunCommand creates a deprecated run command that redirects to vm create
func newDeprecatedRunCommand() *cobra.Command {
	// Reuse vm create command but with deprecation warning
	cmd := newVMCreateCommand()
	cmd.Use = "run <image>"
	cmd.Short = "Run a container as a microVM (DEPRECATED)"
	cmd.Long = "Run a container as a microVM.\n\n" + deprecationWarning + "Use: swarmcracker vm create"
	return wrapDeprecated(cmd, "vm create")
}

// newDeprecatedDeployCommand creates a deprecated deploy command wrapper
func newDeprecatedDeployCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:        "deploy <image>",
		Short:      "Deploy to remote hosts (DEPRECATED)",
		Long:       "Deploy to remote hosts.\n\n" + deprecationWarning + "Use: swarmcracker service create",
		Args:       cobra.ExactArgs(1),
		Deprecated: "Use 'swarmcracker service create' instead",
		PreRun: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(os.Stderr, "WARNING: The 'deploy' command is deprecated")
			fmt.Fprintln(os.Stderr, "Use 'swarmcracker service create' for cluster deployment")
			fmt.Fprintln(os.Stderr)
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("deploy command is deprecated - use 'swarmcracker service create'")
		},
	}
	return cmd
}

// newDeprecatedValidateCommand creates a deprecated validate command wrapper
func newDeprecatedValidateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:        "validate <config-file>",
		Short:      "Validate configuration file (DEPRECATED)",
		Long:       "Validate configuration file.\n\n" + deprecationWarning + "Use: swarmcracker config validate",
		Args:       cobra.ExactArgs(1),
		Deprecated: "Use 'swarmcracker config validate' instead",
		PreRun: func(cmd *cobra.Command, args []string) {
			showDeprecationWarning("config validate " + args[0])
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("validate command is deprecated - use 'swarmcracker config validate'")
		},
	}
	return cmd
}

// newDeprecatedListCommand creates a deprecated list command wrapper
func newDeprecatedListCommand() *cobra.Command {
	// Reuse existing list command
	return wrapDeprecated(newListCommand(), "vm list")
}

// newDeprecatedStatusCommand creates a deprecated status command wrapper
func newDeprecatedStatusCommand() *cobra.Command {
	// Reuse existing status command
	return wrapDeprecated(newStatusCommand(), "vm status")
}

// newDeprecatedLogsCommand creates a deprecated logs command wrapper
func newDeprecatedLogsCommand() *cobra.Command {
	// Reuse existing logs command
	return wrapDeprecated(newLogsCommand(), "vm logs")
}

// newDeprecatedStopCommand creates a deprecated stop command wrapper
func newDeprecatedStopCommand() *cobra.Command {
	// Reuse existing stop command
	return wrapDeprecated(newStopCommand(), "vm stop")
}

// newDeprecatedSnapshotCommand creates a deprecated snapshot command wrapper
func newDeprecatedSnapshotCommand() *cobra.Command {
	// Reuse existing snapshot command
	return wrapDeprecated(newSnapshotCommand(), "vm snapshot")
}
