package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/client"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// The sandbox command group: the node's sandbox control plane (sandboxd) as a caller
// at a shell reaches it.
//
// Every command here is a client of that daemon and none of them touches a VM
// directly, because two of a sandbox's resources outlive the command that asked for
// it: the VM is a process, and an egress allowlist's endpoints are sockets the daemon
// holds inside the sandbox's namespace. A shell that created a sandbox itself would
// hand back a sandbox whose egress died with it.
//
// The commands speak the same v1 API a token-holding caller uses over HTTPS: without
// --host they dial the local control socket (whose file mode is the credential), and
// with --host they present a bearer token, so the same refusal reaches both.
var sandboxSocketPath string

func newSandboxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Run agent sandboxes on this node",
		Long: `Run agent sandboxes: microVMs booted from a sealed template, with their own
disk, their own network namespace and their own per-boot credential.

These commands are clients of the node's sandbox control plane (sandboxd). Start it
first; without it every command here reports the socket it could not reach.

  sandbox create <template>   Boot a new sandbox from a sealed template
  sandbox ls                  List the node's sandboxes
  sandbox inspect <id>        Show one sandbox
  sandbox exec <id> -- <cmd>  Run a command inside a ready sandbox
  sandbox rm <id>             Remove a sandbox and everything it holds
  sandbox token <id>          Mint a token scoped to one sandbox
  sandbox checkpoint <verb>   Take, list, show or forget a sandbox's images
  sandbox fork <id>           Clone a running sandbox, or a stored checkpoint

Without --host the commands use the local control socket ($SWARM_SANDBOX_SOCKET,
default /var/run/swarmcracker/sandboxd.sock), which only a process that can reach
that socket may use. With --host they go through the same API over HTTPS with a
bearer token, which is how a caller that is not on the node reaches a sandbox: a
sandbox-scoped token may exec in its own sandbox and read it, and nothing else.`,
	}
	cmd.PersistentFlags().StringVar(&sandboxSocketPath, "sandbox-socket", "",
		"sandboxd control socket (default $SWARM_SANDBOX_SOCKET or /var/run/swarmcracker/sandboxd.sock)")

	cmd.AddCommand(newSandboxCreateCommand())
	cmd.AddCommand(newSandboxListCommand())
	cmd.AddCommand(newSandboxInspectCommand())
	cmd.AddCommand(newSandboxExecCommand())
	cmd.AddCommand(newSandboxRemoveCommand())
	cmd.AddCommand(newSandboxTokenCommand())
	cmd.AddCommand(newSandboxCheckpointCommand())
	cmd.AddCommand(newSandboxForkCommand())
	return cmd
}

// newSandboxClientForCommand builds the client every sandbox command uses: the local
// control socket, or --host with the global TLS and token flags.
func newSandboxClientForCommand() (*client.SandboxClient, error) {
	cfg := client.DefaultConfig()
	cfg.SandboxSocket = sandboxSocketPath
	if apiHost != "" {
		cfg.Endpoint = apiHost
		cfg.CAFile = apiCA
		cfg.TokenFile = apiTokenFile
		cfg.Token = apiToken
		cfg.ServerName = apiTLSServerName
	}
	return client.NewSandboxClient(cfg)
}

func newSandboxCreateCommand() *cobra.Command {
	var (
		id       string
		owner    string
		allow    []string
		ttl      time.Duration
		vcpus    int
		memoryMB int
	)

	cmd := &cobra.Command{
		Use:   "create <template>",
		Short: "Boot a sandbox from a sealed template",
		Long: `Boot a sandbox from a sealed template.

<template> is a sealed template's name, or "name@version" (see
"swarmcracker template ls"). The node resolves it through its template store, where
the template's pin is checked against the software this node actually runs: a
template built on another Firecracker version or another kernel is refused with
template_pin_mismatch before a disk is copied and before any VMM exists.

A sandbox is ready when its guest agent answers its hello, which is what this
command waits for and reports; "the VMM started" is not ready and is not reported as
success. The reported ready time is the node's own (record creation to the guest's
answer), so it excludes this command's own round trip.

An idle TTL (-ttl) is how long the sandbox may sit unused before the node's reaper
takes it back. Zero means the node's configured default; a negative value means
never.`,
		Example: `  swarmcracker sandbox create sandbox-base
  swarmcracker sandbox create sandbox-python@1.0.0 --ttl 30m
  swarmcracker sandbox create sandbox-node -e registry.npmjs.org:443 -e proxy.golang.org:443`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			// A negative TTL asks for a sandbox the reaper never takes; the API
			// carries that as -1, which is distinct from 0 ("the node's default").
			ttlSeconds := int64(ttl / time.Second)
			if ttl < 0 {
				ttlSeconds = -1
			}

			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			started := time.Now()
			sandbox, err := c.CreateSandbox(cmd.Context(), v1.SandboxCreate{
				Template:       args[0],
				ID:             id,
				Owner:          owner,
				Egress:         allow,
				IdleTTLSeconds: ttlSeconds,
				VCPUs:          vcpus,
				MemoryMB:       memoryMB,
			})
			elapsed := time.Since(started)
			if err != nil {
				return err
			}

			switch output.Format(outputFormat) {
			case output.FormatJSON:
				return printSandboxJSON(cmd.OutOrStdout(), v1.NewSandboxDocument(sandbox))
			case output.FormatName:
				fmt.Fprintln(cmd.OutOrStdout(), sandbox.ID)
				return nil
			case "", output.FormatTable:
			default:
				return output.NewErrorf(output.CodeUsage, "unsupported output format %q for sandbox create", outputFormat)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s\n", sandbox.ID)
			fmt.Fprintf(out, "  state:      %s\n", sandbox.State)
			fmt.Fprintf(out, "  template:   %s\n", sandbox.Template)
			fmt.Fprintf(out, "  guest:      %s %s\n", sandbox.GuestIP, sandbox.MAC)
			if sandbox.DiskCopy != "" {
				fmt.Fprintf(out, "  disk copy:  %s\n", sandbox.DiskCopy)
			}
			if sandbox.AgentVersion != "" {
				fmt.Fprintf(out, "  agent:      %s (boot %s)\n", sandbox.AgentVersion, shortBoot(sandbox.BootID))
			}
			if sandbox.IdleTTLSeconds > 0 {
				fmt.Fprintf(out, "  idle ttl:   %s\n", (time.Duration(sandbox.IdleTTLSeconds) * time.Second).String())
			} else {
				fmt.Fprintf(out, "  idle ttl:   none (the reaper will not take it)\n")
			}
			fmt.Fprintf(out, "  ready in:   %s (node), %s (this command)\n", sandboxReadyWindow(sandbox), elapsed.Round(time.Millisecond))
			return nil
		},
	}

	cmd.Flags().StringVar(&id, "id", "", "Sandbox ID to use (default: a minted one)")
	cmd.Flags().StringVar(&owner, "owner", "", "Who is asking for the sandbox (kept in the record)")
	cmd.Flags().StringArrayVarP(&allow, "egress", "e", nil, "Allow egress to host:port (repeatable; default reaches nothing)")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "Idle TTL after which the reaper takes the sandbox (0 = the node's default, negative = never)")
	cmd.Flags().IntVar(&vcpus, "vcpus", 0, "vCPUs (0 = the node's default)")
	cmd.Flags().IntVar(&memoryMB, "memory", 0, "Memory in MB (0 = the node's default)")
	return cmd
}

func newSandboxListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the node's sandboxes",
		Long: `List the node's sandboxes.

STATE is where each sandbox is: created (being built, or a create that died),
ready (its guest agent answered and it can run commands), failed (it could not be
made usable, or its VMM is gone — REASON says why) or stopped. A sandbox a caller
removed is not listed: its record is deleted once everything it held is freed.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			items, err := c.ListSandboxes(cmd.Context())
			if err != nil {
				return err
			}

			switch output.Format(outputFormat) {
			case output.FormatJSON:
				return printSandboxJSON(cmd.OutOrStdout(), v1.NewSandboxList(items))
			case output.FormatName:
				for _, sb := range items {
					fmt.Fprintln(cmd.OutOrStdout(), sb.ID)
				}
				return nil
			case "", output.FormatTable:
			default:
				return output.NewErrorf(output.CodeUsage, "unsupported output format %q for sandbox ls", outputFormat)
			}

			out := cmd.OutOrStdout()
			if len(items) == 0 {
				fmt.Fprintln(out, "No sandboxes. Create one with `swarmcracker sandbox create <template>`.")
				return nil
			}

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATE\tTEMPLATE\tGUEST IP\tIDLE TTL\tLAST ACTIVE\tREASON")
			now := time.Now()
			for _, sb := range items {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					sb.ID, sb.State, orDash(sb.Template), orDash(sb.GuestIP),
					ttlCell(sb.IdleTTLSeconds), ageCell(sb.LastActiveAt, now), sb.Reason)
			}
			return w.Flush()
		},
	}
	return cmd
}

func newSandboxInspectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "inspect <id>",
		Short:        "Show one sandbox",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			sandbox, err := c.GetSandbox(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printSandboxJSON(cmd.OutOrStdout(), v1.NewSandboxDocument(sandbox))
		},
	}
	return cmd
}

func newSandboxExecCommand() *cobra.Command {
	var (
		env     []string
		workdir string
	)

	cmd := &cobra.Command{
		Use:   "exec <id> -- <command> [args...]",
		Short: "Run a command inside a ready sandbox",
		Long: `Run a command inside a ready sandbox over its guest agent.

Everything after "--" is the command and its arguments, passed to the guest
unchanged. The command's standard output and standard error are streamed to this
command's own, and its exit status becomes the exit status of this command: a
command that fails is reported as a failure by the guest's own code, not as a
successful exec of a failing command.

A sandbox that is not ready cannot run anything, and the refusal says which state it
is in: a sandbox whose create failed names its reason.`,
		Example: `  swarmcracker sandbox exec 5f3a1b2c9d -- uname -a
  swarmcracker sandbox exec 5f3a1b2c9d -- sh -c 'echo out; echo err >&2; exit 3'`,
		Args:          cobra.MinimumNArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRun:        func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			req := v1.ExecRequest{Command: args[1:], WorkDir: workdir}
			if len(env) > 0 {
				req.Env = map[string]string{}
				for _, pair := range env {
					key, value, found := strings.Cut(pair, "=")
					if !found || strings.TrimSpace(key) == "" {
						return output.NewErrorf(output.CodeUsage, "environment variable %q must be K=V", pair)
					}
					req.Env[key] = value
				}
			}

			result, err := c.ExecSandbox(cmd.Context(), args[0], req, cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			// The guest's status is this command's status: a script that checks
			// `sandbox exec ... && ...` must see what the guest returned.
			return execExitError(execOutcome{code: result.ExitCode, signal: result.Signal}, exitCodeGuest)
		},
	}

	cmd.Flags().StringArrayVarP(&env, "env", "e", nil, "Set an environment variable in the guest (repeatable, K=V)")
	cmd.Flags().StringVarP(&workdir, "workdir", "w", "", "Working directory in the guest")
	return cmd
}

func newSandboxRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rm <id>",
		Aliases: []string{"remove"},
		Short:   "Remove a sandbox and everything it holds",
		Long: `Remove a sandbox: stop its VMM, then free its socket, its cgroup, its jail, its
network namespace (with the TAP device and the policy inside it), its disk, the
host's copy of its agent token, the node's instance record and every API token scoped
to it, and finally its record.

A sandbox that is running is removed exactly as an idle one is: the VMM is stopped
first, and if it cannot be stopped nothing else is freed and the refusal says so —
freeing a live VM's namespace and disk would be the opposite of a removal.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			removal, err := c.DeleteSandbox(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			switch output.Format(outputFormat) {
			case output.FormatJSON:
				return printSandboxJSON(cmd.OutOrStdout(), removal)
			case output.FormatName:
				fmt.Fprintln(cmd.OutOrStdout(), removal.Sandbox)
				return nil
			case "", output.FormatTable:
			default:
				return output.NewErrorf(output.CodeUsage, "unsupported output format %q for sandbox rm", outputFormat)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s removed\n", removal.Sandbox)
			fmt.Fprintf(out, "  process stopped:      %t\n", removal.Freed.ProcessStopped)
			fmt.Fprintf(out, "  api socket removed:   %t\n", removal.Freed.SocketRemoved)
			fmt.Fprintf(out, "  cgroup removed:       %t\n", removal.Freed.CgroupRemoved)
			fmt.Fprintf(out, "  jail removed:         %t\n", removal.Freed.JailRemoved)
			fmt.Fprintf(out, "  netns removed:        %t\n", removal.Freed.NetNSRemoved)
			fmt.Fprintf(out, "  tap removed:          %t\n", removal.Freed.TAPRemoved)
			fmt.Fprintf(out, "  disk removed:         %t\n", removal.Freed.DiskRemoved)
			fmt.Fprintf(out, "  agent token removed:  %t\n", removal.Freed.AgentCredentialRemoved)
			fmt.Fprintf(out, "  instance record:      %t\n", removal.Freed.InstanceRecordRemoved)
			fmt.Fprintf(out, "  api tokens revoked:   %d\n", removal.Freed.APITokensRevoked)
			fmt.Fprintf(out, "  sandbox record:       %t\n", removal.Freed.RecordRemoved)
			return nil
		},
	}
	return cmd
}

func newSandboxTokenCommand() *cobra.Command {
	var (
		name      string
		expiresAt string
	)

	cmd := &cobra.Command{
		Use:   "token <id>",
		Short: "Mint a token scoped to one sandbox",
		Long: `Mint an API token scoped to one sandbox.

The token carries sandboxes:read and sandboxes:exec and names exactly this sandbox,
so it can run commands in it and read it, and cannot exec in another sandbox, create
or remove one, or mint another token. The secret is printed once and never again.

Minting a credential is administrative work: this command needs the sandboxes:token
scope, which only an admin token (or the local control socket) holds.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			tok, err := c.CreateSandboxToken(cmd.Context(), args[0], v1.SandboxTokenRequest{
				Name:      name,
				ExpiresAt: expiresAt,
			})
			if err != nil {
				return err
			}

			switch output.Format(outputFormat) {
			case output.FormatJSON:
				return printSandboxJSON(cmd.OutOrStdout(), tok)
			case "", output.FormatTable, output.FormatName:
			default:
				return output.NewErrorf(output.CodeUsage, "unsupported output format %q for sandbox token", outputFormat)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s\n", tok.Secret)
			fmt.Fprintf(out, "  id:       %s\n", tok.ID)
			fmt.Fprintf(out, "  name:     %s\n", tok.Name)
			fmt.Fprintf(out, "  sandbox:  %s\n", tok.Sandbox)
			fmt.Fprintf(out, "  scopes:   %s\n", strings.Join(tok.Scopes, ", "))
			if tok.ExpiresAt != "" {
				fmt.Fprintf(out, "  expires:  %s\n", tok.ExpiresAt)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Token name (default: \"sandbox <id>\")")
	cmd.Flags().StringVar(&expiresAt, "expires-at", "", "RFC3339 expiry instant (default: never)")
	return cmd
}

// printSandboxJSON writes one v1 document.
func printSandboxJSON(out io.Writer, doc any) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return output.NewErrorf(output.CodeInternal, "encode response: %v", err)
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}

// sandboxReadyWindow renders how long the node took to make a sandbox ready, from
// its own two timestamps. A sandbox that never became ready says so rather than
// printing a zero.
func sandboxReadyWindow(sb v1.Sandbox) string {
	created, err := time.Parse(time.RFC3339Nano, sb.CreatedAt)
	if err != nil || sb.ReadyAt == "" {
		return "never"
	}
	ready, err := time.Parse(time.RFC3339Nano, sb.ReadyAt)
	if err != nil {
		return "never"
	}
	return ready.Sub(created).Round(time.Millisecond).String()
}

// shortBoot renders a boot identity in a listing-sized form: its first 12 characters
// are enough to tell two boots apart and short enough to read.
func shortBoot(bootID string) string {
	if len(bootID) <= 12 {
		return bootID
	}
	return bootID[:12]
}

// ttlCell renders a sandbox's idle TTL for a listing.
func ttlCell(seconds int64) string {
	if seconds <= 0 {
		return "none"
	}
	return (time.Duration(seconds) * time.Second).String()
}

// ageCell renders how long ago a timestamp was, in a listing-sized form.
func ageCell(stamp string, now time.Time) string {
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return "-"
	}
	age := now.Sub(at)
	if age < 0 {
		age = 0
	}
	return age.Round(time.Second).String()
}

// orDash renders an empty field as a dash, so a table's columns stay readable when a
// sandbox has no template or no address yet.
func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// The checkpoint command group. A checkpoint is a stored image of a sandbox — its memory
// and its disk — that a fork resumes. It is immutable and it outlives the sandbox it came
// from, which is why every command here is node-scoped: once the sandbox is gone, these
// are the only names left that can find the image and free it.
func newSandboxCheckpointCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "checkpoint",
		Short: "Take, list, show and forget a sandbox's checkpoint images",
		Long: `Work with checkpoints: the images a fork resumes.

A checkpoint is taken with the guest's writers stopped, so what it captures is a
consistent filesystem rather than one mid-write. What is stored on the node — the jail
paths, the disk copy, the guest credential the image carries — is not reported here; a
caller sees that a checkpoint exists and what it can resume.

A checkpoint outlives its sandbox: removing the sandbox does not remove it. Free it with
"sandbox checkpoint rm".`,
	}
	cmd.AddCommand(newSandboxCheckpointCreateCommand())
	cmd.AddCommand(newSandboxCheckpointListCommand())
	cmd.AddCommand(newSandboxCheckpointShowCommand())
	cmd.AddCommand(newSandboxCheckpointRemoveCommand())
	return cmd
}

func newSandboxCheckpointCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <sandbox> <name>",
		Short: "Take a checkpoint of a running sandbox",
		Long: `Take a checkpoint of a running sandbox under a name you choose.

The name is yours to choose because it is how you fork the image later and how you free
it: a node-minted name would only be reachable through a listing. The sandbox must be
ready, and its guest must implement quiesce — the checkpoint is refused per call, naming
the capability the template's agent lacks, rather than taking an image whose filesystems
were never stopped.

The guest's writers are stopped (fsfreeze on each writable, block-device-backed mount) for
as long as the copy takes, and released afterwards. tmpfs, /dev/shm and an overlayfs upper
layer are not frozen by fsfreeze and are therefore not captured consistently; keep state
you care about on a frozen mount.`,
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			checkpoint, err := c.CreateCheckpoint(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}

			switch output.Format(outputFormat) {
			case output.FormatJSON:
				return printSandboxJSON(cmd.OutOrStdout(), v1.NewCheckpointDocument(checkpoint))
			case output.FormatName:
				fmt.Fprintln(cmd.OutOrStdout(), checkpoint.Name)
				return nil
			case "", output.FormatTable:
			default:
				return output.NewErrorf(output.CodeUsage, "unsupported output format %q for sandbox checkpoint create", outputFormat)
			}

			writeCheckpointTable(cmd.OutOrStdout(), []v1.Checkpoint{checkpoint})
			return nil
		},
	}
	return cmd
}

func newSandboxCheckpointListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls [<sandbox>]",
		Aliases: []string{"list"},
		Short:   "List checkpoints, of one sandbox or of the node",
		Long: `List checkpoints.

Without an argument this lists every checkpoint on the node, which is the listing that
still finds the images of a sandbox that no longer exists. With a sandbox it lists only
that sandbox's checkpoints.`,
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			var items []v1.Checkpoint
			if len(args) == 1 {
				items, err = c.ListSandboxCheckpoints(cmd.Context(), args[0])
			} else {
				items, err = c.ListCheckpoints(cmd.Context())
			}
			if err != nil {
				return err
			}

			switch output.Format(outputFormat) {
			case output.FormatJSON:
				return printSandboxJSON(cmd.OutOrStdout(), v1.NewCheckpointList(items))
			case output.FormatName:
				for _, chk := range items {
					fmt.Fprintln(cmd.OutOrStdout(), chk.Name)
				}
				return nil
			case "", output.FormatTable:
			default:
				return output.NewErrorf(output.CodeUsage, "unsupported output format %q for sandbox checkpoint ls", outputFormat)
			}

			if len(items) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No checkpoints. Take one with `swarmcracker sandbox checkpoint create <sandbox> <name>`.")
				return nil
			}
			writeCheckpointTable(cmd.OutOrStdout(), items)
			return nil
		},
	}
	return cmd
}

func newSandboxCheckpointShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "inspect <name>",
		Aliases:      []string{"show"},
		Short:        "Show one checkpoint",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			checkpoint, err := c.GetCheckpoint(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			switch output.Format(outputFormat) {
			case output.FormatJSON:
				return printSandboxJSON(cmd.OutOrStdout(), v1.NewCheckpointDocument(checkpoint))
			case output.FormatName:
				fmt.Fprintln(cmd.OutOrStdout(), checkpoint.Name)
				return nil
			case "", output.FormatTable:
			default:
				return output.NewErrorf(output.CodeUsage, "unsupported output format %q for sandbox checkpoint inspect", outputFormat)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Name:        %s\n", checkpoint.Name)
			fmt.Fprintf(out, "Sandbox:     %s\n", orDash(checkpoint.Sandbox))
			fmt.Fprintf(out, "Template:    %s\n", orDash(checkpoint.Template))
			fmt.Fprintf(out, "Kernel:      %s\n", orDash(checkpoint.Kernel))
			fmt.Fprintf(out, "Size:        %s\n", snapshotFormatBytes(checkpoint.SizeBytes))
			fmt.Fprintf(out, "Quiesce:     %s\n", orDash(checkpoint.QuiesceMode))
			fmt.Fprintf(out, "Mounts:      %s\n", orDash(strings.Join(checkpoint.QuiesceMounts, ", ")))
			fmt.Fprintf(out, "Egress:      %s\n", orDash(strings.Join(checkpoint.Egress, ", ")))
			fmt.Fprintf(out, "Created:     %s\n", orDash(checkpoint.CreatedAt))
			return nil
		},
	}
	return cmd
}

func newSandboxCheckpointRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Forget a checkpoint and free what it held",
		Long: `Forget a checkpoint: its record, its state document, its disk copy, and the memory
image chunks nothing else references.

A checkpoint's memory image is stored as chunks shared with every other checkpoint of the
same sandbox, so removing one of two forks frees only the chunks the other does not use.
The response therefore separates what was freed from what is retained — retainedBytes is
what the remaining checkpoints still reference, counted once per referencing checkpoint
rather than deduplicated — so that "removed" is never read as "the disk came back".

Forgetting a checkpoint leaves the sandboxes forked from it running: they have their own
disk and their own memory.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			removal, err := c.DeleteCheckpoint(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			switch output.Format(outputFormat) {
			case output.FormatJSON:
				// The removal document whole, freed and retained: a caller that asked for
				// JSON gets what the API answered, not a summary of it.
				return printSandboxJSON(cmd.OutOrStdout(), removal)
			case output.FormatName:
				fmt.Fprintln(cmd.OutOrStdout(), removal.Checkpoint)
				return nil
			case "", output.FormatTable:
			default:
				return output.NewErrorf(output.CodeUsage, "unsupported output format %q for sandbox checkpoint rm", outputFormat)
			}

			out := cmd.OutOrStdout()
			freed := removal.Freed
			fmt.Fprintf(out, "%s removed\n", removal.Checkpoint)
			fmt.Fprintf(out, "  freed:  %s (state %s, disk %s, memory chunks %s in %d chunks)\n",
				snapshotFormatBytes(freed.FreedBytes), snapshotFormatBytes(freed.StateBytes), snapshotFormatBytes(freed.DiskBytes),
				snapshotFormatBytes(freed.ChunkBytes), freed.Chunks)
			fmt.Fprintf(out, "  retained: %s referenced by %d checkpoint(s), counted once per reference and not deduplicated\n",
				snapshotFormatBytes(freed.RetainedBytes), freed.Retained)
			return nil
		},
	}
	return cmd
}

// writeCheckpointTable writes the checkpoint listing every checkpoint command shares.
func writeCheckpointTable(out io.Writer, items []v1.Checkpoint) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSANDBOX\tTEMPLATE\tMEMORY\tQUIESCE\tSIZE")
	for _, chk := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			chk.Name, orDash(chk.Sandbox), orDash(chk.Template),
			orDash(fmt.Sprintf("%d MB", chk.MemoryMB)), orDash(chk.QuiesceMode), snapshotFormatBytes(chk.SizeBytes))
	}
	_ = w.Flush()
}

func newSandboxForkCommand() *cobra.Command {
	var (
		checkpoint string
		count      int
		owner      string
		idleTTL    time.Duration
		ids        []string
	)

	cmd := &cobra.Command{
		Use:   "fork <sandbox>",
		Short: "Clone a running sandbox, or a stored checkpoint",
		Long: `Fork: clone a sandbox from an image of one.

"sandbox fork <sandbox>" clones what that sandbox is running right now: the node takes a
fresh checkpoint of it first, and the checkpoint it left behind is part of the answer, so
you can find it later and free it. "sandbox fork --checkpoint <name>" clones a checkpoint
you already have, which does not need its sandbox to exist any more.

Each clone gets its own disk, its own network namespace, its own IP and MAC, and its own
credential — it is a separate sandbox, not a second view of the same one.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		PreRun:       func(cmd *cobra.Command, _ []string) { setupLogging(logLevel) },
		RunE: func(cmd *cobra.Command, args []string) error {
			if count < 0 {
				return output.NewErrorf(output.CodeUsage, "-count cannot be negative")
			}
			if idleTTL < 0 && idleTTL != -1 {
				return output.NewErrorf(output.CodeUsage, "-idle-ttl must be positive, or -1 for never")
			}
			c, err := newSandboxClientForCommand()
			if err != nil {
				return err
			}
			defer c.Close()

			req := v1.SandboxFork{
				Count:          count,
				Owner:          owner,
				IdleTTLSeconds: int64(idleTTL / time.Second),
				IDs:            ids,
			}
			var doc v1.SandboxList
			if checkpoint != "" {
				doc, err = c.ForkCheckpoint(cmd.Context(), checkpoint, req)
			} else {
				doc, err = c.ForkSandbox(cmd.Context(), args[0], req)
			}
			if err != nil {
				return err
			}

			switch output.Format(outputFormat) {
			case output.FormatJSON:
				return printSandboxJSON(cmd.OutOrStdout(), doc)
			case output.FormatName:
				for _, sb := range doc.Items {
					fmt.Fprintln(cmd.OutOrStdout(), sb.ID)
				}
				return nil
			case "", output.FormatTable:
			default:
				return output.NewErrorf(output.CodeUsage, "unsupported output format %q for sandbox fork", outputFormat)
			}

			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATE\tGUEST IP\tTEMPLATE")
			for _, sb := range doc.Items {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", sb.ID, sb.State, orDash(sb.GuestIP), orDash(sb.Template))
			}
			_ = w.Flush()
			// The checkpoint is named because the clones were resumed from it and it stays
			// on the node: a fork whose image the caller cannot name is a fork the caller
			// cannot free.
			if doc.Checkpoint != "" {
				fmt.Fprintf(out, "\ncheckpoint %s holds the image these were forked from.\n", doc.Checkpoint)
				fmt.Fprintf(out, "Free it with `swarmcracker sandbox checkpoint rm %s` once no more clones are needed.\n", doc.Checkpoint)
			}
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&checkpoint, "checkpoint", "",
		"fork a stored checkpoint by name instead of taking a fresh one from <sandbox>")
	flags.IntVar(&count, "count", 0, "how many clones to create (default 1)")
	flags.StringVar(&owner, "owner", "", "override the clones' owner")
	flags.DurationVar(&idleTTL, "idle-ttl", 0,
		"how long a clone may sit idle before the reaper takes it (0 = the node's default, -1 = never)")
	flags.StringSliceVar(&ids, "id", nil, "name the clones, in order (repeatable)")
	return cmd
}
