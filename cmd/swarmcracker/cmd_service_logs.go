package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/spf13/cobra"
)

var (
	serviceLogsFollow bool
	serviceLogsTail   int
	serviceLogsSince  string
)

// newServiceLogsCommand creates `service logs`, the aggregated view across every
// task of a service.
//
// It always goes through the manager API: a service's replicas live on different
// nodes, and only the manager can reach each node's console file. The local path
// (`vm logs <task-id>`) stays the single-node answer and needs no API.
func newServiceLogsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <service>",
		Short: "View logs from every task of a service",
		Long: `Display the console logs of all tasks of a service in one stream.

Every line is prefixed with a short task ID, because merged output without
attribution is unreadable when a service has several replicas. Streams are
interleaved as they arrive rather than sorted by time: the console log carries no
per-line timestamp.

This command needs --host: replicas live on different nodes, and only the manager
can reach each node's console file. For one task on this node, use
'swarmcracker vm logs <task-id>'. The API serves at most 10000 lines per task per
request, which is what --tail -1 (the default) asks for.

Example:
  swarmcracker -H https://manager:4244 --ca ca.crt --token-file token service logs web
  swarmcracker -H https://manager:4244 --ca ca.crt --token-file token service logs --tail 50 web
  swarmcracker -H https://manager:4244 --ca ca.crt --token-file token service logs -f web`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiHost == "" {
				return output.NewError(output.CodeUsage,
					"service logs require --host <manager>: replicas run on different nodes and only the manager can read them all; "+
						"use 'swarmcracker vm logs <task-id>' for a task on this node")
			}

			opts, err := remoteLogsOptions(serviceLogsTail, serviceLogsSince, serviceLogsFollow)
			if err != nil {
				return err
			}

			c, err := newContractClient()
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			ctx := cmd.Context()
			if serviceLogsFollow {
				var stop context.CancelFunc
				ctx, stop = signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
				defer stop()
			}

			return c.ServiceLogs(ctx, args[0], opts, os.Stdout)
		},
	}

	cmd.Flags().BoolVarP(&serviceLogsFollow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVar(&serviceLogsTail, "tail", -1, "Show last N lines per task (default: all, capped at 10000 per task)")
	cmd.Flags().StringVar(&serviceLogsSince, "since", "", "Show logs since a duration (1h, 30m) or RFC3339 timestamp")

	return cmd
}
