package main

// sandbox status and sandbox logs: what a sandbox is, from the guest's own answer rather
// than from a command whose output someone parses.
//
// `status` asks the sandbox's guest agent for its `info` and `net` reports — the guest's
// identity, kernel, resources and toolchain, and its own view of its network — and prints
// them beside the node's record. That is why it needs no shell in the template: the answer
// is a document, and it does not change when a command's formatting does.
//
// `logs` prints the sandbox's serial console, which is the VMM's record of the guest's
// serial port. It is a different thing from a task's logs (the workload's own output, over
// the node's log stream): a console exists from the first instruction the guest executes,
// which is what makes it the place a boot that failed explains itself.

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

func newSandboxStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <id>",
		Short: "Show a sandbox and what its guest says about itself",
		Long: `Show a sandbox and what its guest says about itself.

The first half is the node's record, which is what "sandbox inspect" prints. The second
half is the guest's own answer, read inside the guest by its agent: its hostname and
machine id, the kernel it is running, the vCPUs and memory it believes it has, its uptime,
the tools its template ships, and its network — interfaces, addresses, routes and the
resolver it consults.

None of that is parsed out of a command's output, so it needs no shell in the template and
does not change when a command's formatting does. A sandbox that is not ready cannot answer
and the refusal says which state it is in; a sandbox that failed to boot is exactly the one
whose console is worth reading instead (see "sandbox logs").`,
		Example: `  swarmcracker sandbox status 5f3a1b2c9d
  swarmcracker sandbox status 5f3a1b2c9d --format json`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			status, err := c.SandboxStatus(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			switch output.Format(outputFormat) {
			case output.FormatJSON:
				return printSandboxJSON(cmd.OutOrStdout(), status)
			case "", output.FormatTable:
			default:
				return output.NewErrorf(output.CodeUsage,
					"unsupported output format %q for sandbox status", outputFormat)
			}
			printSandboxStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
	return cmd
}

// printSandboxStatus renders one status: the record, then the guest's own report. Everything
// the guest said is printed as the guest said it — an empty field is left out rather than
// filled with a guess.
func printSandboxStatus(out io.Writer, status v1.SandboxStatus) {
	fmt.Fprintf(out, "%s\n", status.ID)
	fmt.Fprintf(out, "  state:      %s\n", status.State)
	if status.Template != "" {
		fmt.Fprintf(out, "  template:   %s\n", status.Template)
	}
	if status.Owner != "" {
		fmt.Fprintf(out, "  owner:      %s\n", status.Owner)
	}
	if status.GuestIP != "" || status.MAC != "" {
		fmt.Fprintf(out, "  guest:      %s %s\n", status.GuestIP, status.MAC)
	}
	if status.State == "failed" && status.Reason != "" {
		fmt.Fprintf(out, "  reason:     %s\n", status.Reason)
	}
	fmt.Fprintf(out, "  ready:      %s\n", sandboxReadyWindow(status.Sandbox))

	info := status.Info
	if info.Hostname != "" {
		fmt.Fprintf(out, "  hostname:   %s\n", info.Hostname)
	}
	if info.MachineID != "" {
		fmt.Fprintf(out, "  machine id: %s\n", info.MachineID)
	}
	if info.KernelRelease != "" {
		fmt.Fprintf(out, "  kernel:     %s %s", info.KernelRelease, strings.TrimSpace(info.KernelVersion))
		fmt.Fprintln(out)
	}
	if info.Machine != "" {
		fmt.Fprintf(out, "  arch:       %s\n", info.Machine)
	}
	if info.OSName != "" || info.OSID != "" {
		fmt.Fprintf(out, "  os:         %s %s\n", info.OSName, info.OSID)
	}
	fmt.Fprintf(out, "  resources:  %d vCPU, %s\n", info.CPUs, humanMiB(info.MemoryTotalBytes))
	fmt.Fprintf(out, "  uptime:     %s\n", (time.Duration(info.UptimeSeconds * float64(time.Second))).Round(time.Second))
	if info.AgentVersion != "" {
		fmt.Fprintf(out, "  agent:      %s (protocol %s, boot %s)\n",
			info.AgentVersion, info.Protocol, shortBoot(info.BootID))
	}
	if len(info.Tools) > 0 {
		fmt.Fprintf(out, "  tools:      %s\n", strings.Join(info.Tools, ", "))
	}

	fmt.Fprintln(out, "  network:")
	for _, iface := range status.Net.Interfaces {
		state := "down"
		if iface.Up {
			state = "up"
		}
		fmt.Fprintf(out, "    %-8s %-4s mtu %-5d %-18s %s\n",
			iface.Name, state, iface.MTU, iface.MAC, strings.Join(iface.Addresses, " "))
	}
	for _, route := range status.Net.Routes {
		via := ""
		if route.Gateway != "" {
			via = " via " + route.Gateway
		}
		fmt.Fprintf(out, "    route    %s %s%s dev %s\n", route.Family, route.Destination, via, route.Device)
	}
	if len(status.Net.Resolvers) > 0 {
		fmt.Fprintf(out, "    resolver %s\n", strings.Join(status.Net.Resolvers, " "))
	}
}

// humanMiB renders a byte count as the MiB a memory size is usually stated in.
func humanMiB(bytes uint64) string {
	if bytes == 0 {
		return "unknown"
	}
	return fmt.Sprintf("%d MiB", bytes>>20)
}

func newSandboxLogsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <id>",
		Short: "Print a sandbox's serial console",
		Long: `Print a sandbox's serial console, as the node recorded it.

A console is not a task's logs: it is the VMM's record of the guest's serial port, from the
first instruction the guest executes. It therefore exists for a sandbox that never became
ready, which is where a boot that failed explains itself — and it is written whether or not
the guest agent ever answered.

The console is addressed by the sandbox, so a sandbox this node no longer has is reported as
missing even when its console file is still on disk. A node configured with no log directory
has none for any sandbox and says so.`,
		Example: `  swarmcracker sandbox logs 5f3a1b2c9d
  swarmcracker sandbox logs 5f3a1b2c9d | tail -50`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			return c.SandboxLogs(cmd.Context(), args[0], cmd.OutOrStdout())
		},
	}
	return cmd
}
