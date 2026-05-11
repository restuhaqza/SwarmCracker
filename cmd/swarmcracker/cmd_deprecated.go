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

  swarmcracker cluster <command>    - Cluster lifecycle (init, join, leave, status, reset, deinit)
  swarmcracker node <command>       - Node management (ls, inspect, drain, activate, promote, rm)
  swarmcracker service <command>    - Service management (ls, inspect, ps, create, update, scale, rm)
  swarmcracker task <command>       - Task management (ls, inspect)
  swarmcracker vm <command>         - VM operations (ls, inspect, create, stop, rm, logs, snapshot)
  swarmcracker network <command>    - Network configuration (ls, inspect, create, rm)
  swarmcracker asset <command>      - Asset management (ls, pull, rm)
  swarmcracker config <command>     - Configuration management (view, validate, init, set, unset)

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

// newDeprecatedInitCommand creates a deprecated init command wrapper
func newDeprecatedInitCommand() *cobra.Command {
	// Just reuse the actual init command - don't recreate logic
	cmd := newInitCommand()
	cmd.Deprecated = "Use 'swarmcracker cluster init' instead"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("cluster init")
		setupLogging(logLevel)
	}
	return cmd
}

// newDeprecatedJoinCommand creates a deprecated join command wrapper
func newDeprecatedJoinCommand() *cobra.Command {
	cmd := newJoinCommand()
	cmd.Deprecated = "Use 'swarmcracker cluster join' instead"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("cluster join")
		setupLogging(logLevel)
	}
	return cmd
}

// newDeprecatedLeaveCommand creates a deprecated leave command wrapper
func newDeprecatedLeaveCommand() *cobra.Command {
	cmd := newLeaveCommand()
	cmd.Deprecated = "Use 'swarmcracker cluster leave' instead"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("cluster leave")
		setupLogging(logLevel)
	}
	return cmd
}

// newDeprecatedDeinitCommand creates a deprecated deinit command wrapper
func newDeprecatedDeinitCommand() *cobra.Command {
	cmd := newDeinitCommand()
	cmd.Deprecated = "Use 'swarmcracker cluster deinit' instead"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("cluster deinit")
		setupLogging(logLevel)
	}
	return cmd
}

// newDeprecatedResetCommand creates a deprecated reset command wrapper
func newDeprecatedResetCommand() *cobra.Command {
	cmd := newResetCommand()
	cmd.Deprecated = "Use 'swarmcracker cluster reset' instead"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("cluster reset")
		setupLogging(logLevel)
	}
	return cmd
}

// newDeprecatedRunCommand creates a deprecated run command that redirects to vm create
func newDeprecatedRunCommand() *cobra.Command {
	// Reuse vm create command but with deprecation warning
	cmd := newVMCreateCommand()
	cmd.Use = "run <image>"
	cmd.Deprecated = "Use 'swarmcracker vm create' instead"
	cmd.Short = "Run a container as a microVM (DEPRECATED)"
	cmd.Long = "Run a container as a microVM.\n\n" + deprecationWarning + "Use: swarmcracker vm create"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("vm create " + args[0])
		setupLogging(logLevel)
	}
	return cmd
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
	cmd := newListCommand()
	cmd.Deprecated = "Use 'swarmcracker vm ls' instead"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("vm ls")
		setupLogging(logLevel)
	}
	return cmd
}

// newDeprecatedStatusCommand creates a deprecated status command wrapper
func newDeprecatedStatusCommand() *cobra.Command {
	// Reuse existing status command
	cmd := newStatusCommand()
	cmd.Deprecated = "Use 'swarmcracker cluster status' instead"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("cluster status")
		setupLogging(logLevel)
	}
	return cmd
}

// newDeprecatedLogsCommand creates a deprecated logs command wrapper
func newDeprecatedLogsCommand() *cobra.Command {
	// Reuse existing logs command
	cmd := newLogsCommand()
	cmd.Deprecated = "Use 'swarmcracker vm logs' instead"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("vm logs")
		setupLogging(logLevel)
	}
	return cmd
}

// newDeprecatedStopCommand creates a deprecated stop command wrapper
func newDeprecatedStopCommand() *cobra.Command {
	// Reuse existing stop command
	cmd := newStopCommand()
	cmd.Deprecated = "Use 'swarmcracker vm stop' instead"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("vm stop")
		setupLogging(logLevel)
	}
	return cmd
}

// newDeprecatedMetricsCommand creates a deprecated metrics command wrapper
func newDeprecatedMetricsCommand() *cobra.Command {
	// Reuse existing metrics command
	cmd := newMetricsCommand()
	cmd.Deprecated = "Use 'swarmcracker cluster status --metrics' instead"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("cluster status --metrics")
		setupLogging(logLevel)
	}
	return cmd
}

// newDeprecatedSnapshotCommand creates a deprecated snapshot command wrapper
func newDeprecatedSnapshotCommand() *cobra.Command {
	// Reuse existing snapshot command
	cmd := newSnapshotCommand()
	cmd.Deprecated = "Use 'swarmcracker vm snapshot' instead"
	cmd.PreRun = func(cmd *cobra.Command, args []string) {
		showDeprecationWarning("vm snapshot")
		setupLogging(logLevel)
	}
	return cmd
}
