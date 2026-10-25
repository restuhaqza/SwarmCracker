package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/apihttp"
	"github.com/restuhaqza/swarmcracker/pkg/client"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/spf13/cobra"
)

// Global remote-API flags. --token-file doubles as the store path for the
// `api token` commands, so an operator who moved the token file on the manager
// uses one flag for both reading the API and minting tokens.
var (
	apiHost          string
	apiCA            string
	apiTokenFile     string
	apiToken         string
	apiTLSServerName string
)

// contractClientConfig builds the client configuration for the v1 contract
// reads. Without --host it stays on the local control socket, so a machine that
// is a cluster member needs no flags.
func contractClientConfig() client.Config {
	cfg := client.DefaultConfig()
	if apiHost == "" {
		return cfg
	}

	cfg.Endpoint = apiHost
	cfg.CAFile = apiCA
	cfg.TokenFile = apiTokenFile
	cfg.Token = apiToken
	cfg.ServerName = apiTLSServerName
	return cfg
}

// newContractClient returns the client every `-o json` / `-o name` read uses.
// A remote configuration mistake is already a typed usage error, so it is kept
// instead of being flattened into `unreachable`.
func newContractClient() (*client.Client, error) {
	c, err := client.New(contractClientConfig())
	if err != nil {
		return nil, contractError(err)
	}
	return c, nil
}

// contractError preserves an error that already carries a stable code. gRPC
// status errors and raw transport failures keep the FromGRPC mapping.
func contractError(err error) error {
	if err == nil {
		return nil
	}
	var typed *output.Error
	if errors.As(err, &typed) {
		return typed
	}
	return output.FromGRPC(err)
}

// remoteUnsupported is the refusal every command reaches when --host is set but
// the command only knows the local control socket. Failing loudly is the point:
// silently reading the local cluster because a remote flag was ignored is worse
// than not supporting the command remotely yet.
func remoteUnsupported() error {
	return output.NewError(output.CodeUsage,
		"--host selects a remote manager: this command only supports the local control socket; "+
			"use a read command with -o json or -o name")
}

// remoteCapablePaths are the commands that understand --host. The list is
// explicit so a new command cannot inherit remote support by accident, and a
// command outside it fails loudly instead of reading the local cluster.
var remoteCapablePaths = map[string]struct{}{
	"swarmcracker service ls":      {},
	"swarmcracker service inspect": {},
	"swarmcracker service ps":      {},
	"swarmcracker task ls":         {},
	"swarmcracker task inspect":    {},
	"swarmcracker node ls":         {},
	"swarmcracker node inspect":    {},
	"swarmcracker config ls":       {},
	"swarmcracker config inspect":  {},
	"swarmcracker secret ls":       {},
	"swarmcracker secret inspect":  {},
	"swarmcracker network ls":      {},
	"swarmcracker network inspect": {},
	// vm exec is the first remote-capable mutation: it streams a command's
	// output from the node that runs the task through the public API.
	"swarmcracker vm exec": {},
	// vm snapshot stop-restored is a manager-relayed mutation: only the node that
	// started the restored VM holds its process, so -H is the only way to reach it
	// from another machine.
	"swarmcracker vm snapshot stop-restored": {},
}

// commandAllowsRemote reports whether cmd may run when --host is set. The
// command path (not the invoked alias) identifies the leaf, so `service list`
// and `service ls` take the same decision.
func commandAllowsRemote(cmd *cobra.Command) bool {
	path := cmd.CommandPath()

	// The root, help and version never read a cluster.
	if path == "swarmcracker" || path == "swarmcracker help" || path == "swarmcracker version" {
		return true
	}

	// `api token` edits the local token store and never contacts a manager.
	if path == "swarmcracker api" || strings.HasPrefix(path, "swarmcracker api ") {
		return true
	}

	_, ok := remoteCapablePaths[path]
	return ok
}

// guardRemoteHost refuses --host on a command that would otherwise read the
// local cluster. It is installed as the root PersistentPreRunE, so it covers
// every command (including the daemon-socket `vm` group) without each one
// remembering the check. A remote flag that silently falls back to the local
// control socket is the failure mode this prevents.
func guardRemoteHost(cmd *cobra.Command, _ []string) error {
	if apiHost == "" || commandAllowsRemote(cmd) {
		return nil
	}

	return output.NewErrorf(output.CodeUsage,
		"--host is not supported by %q: this command only reads the local cluster; "+
			"remote operations are available on service/task/node/config/secret/network ls|inspect|ps, "+
			"vm exec and vm snapshot stop-restored (with -o json or -o name where it applies)", cmd.CommandPath())
}

// newAPICommand groups the programmatic-API operator commands.
func newAPICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Manage the programmatic API",
		Long: `Manage the manager's programmatic API.

Tokens are stored on the manager in <api-dir>/tokens.json. A token's secret is
printed once at creation and never again; the file holds only its hash. Reads
against a manager use the global -H, --ca and --token-file flags.

The daemon takes its state directory from --state-dir and does not read
SWARM_STATE_DIR. The CLI resolves the default token path from SWARM_STATE_DIR
when it is set, otherwise /var/lib/swarmkit; pass --token-file to name the store
explicitly.`,
	}

	cmd.AddCommand(newAPITokenCommand())
	return cmd
}

func newAPITokenCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage API bearer tokens",
		Long: `Manage the bearer tokens the programmatic API accepts.

These commands edit the local token file and never contact the manager. The
global --token-file flag names that file; otherwise the CLI uses
<SWARM_STATE_DIR or /var/lib/swarmkit>/api/tokens.json. The daemon's own
--state-dir flag sets where it looks for the store; SWARM_STATE_DIR is only the
CLI's default.`,
	}

	cmd.AddCommand(newAPITokenCreateCommand())
	cmd.AddCommand(newAPITokenListCommand())
	cmd.AddCommand(newAPITokenRevokeCommand())
	return cmd
}

func newAPITokenCreateCommand() *cobra.Command {
	var (
		name       string
		role       string
		scopes     []string
		serviceIDs []string
		taskIDs    []string
		labels     []string
		expiresAt  string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Mint a token and print its secret once",
		Long: `Mint a token and print its secret to stdout, once.

Store the output somewhere safe: the token file keeps only a hash, so the secret
cannot be recovered later.

--role expands to a scope set (reader, runner, operator or admin). --scope
narrows it: every requested scope must be part of the role, and a token can never
hold more. A runner must carry at least one task selector (--service-id,
--task-id or --selector-label); operator and admin may omit them, which means all
tasks.`,
		Example: `  swarmcracker api token create --name ci --role reader
  swarmcracker api token create --name deploy --role operator --scope services:write --scope services:read
  swarmcracker api token create --name build --role runner --service-id svc-123 --expires-at 2026-12-01T00:00:00Z
  swarmcracker --token-file /etc/swarmcracker/api/tokens.json api token create --name ops --role admin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			selectors, err := tokenSelectors(serviceIDs, taskIDs, labels)
			if err != nil {
				return err
			}

			store, err := openAPITokenStore()
			if err != nil {
				return err
			}

			secret, _, err := store.CreateWithOptions(name, role, apihttp.TokenOptions{
				Scopes:        scopes,
				TaskSelectors: selectors,
				ExpiresAt:     expiresAt,
			})
			if err != nil {
				return err
			}

			// The secret is the only value printed: the hash stays on disk.
			fmt.Fprintln(cmd.OutOrStdout(), secret)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Token name (required)")
	cmd.Flags().StringVar(&role, "role", apihttp.RoleReader,
		"Token role (reader, runner, operator or admin)")
	cmd.Flags().StringArrayVar(&scopes, "scope", nil,
		"Narrow the token to one scope (repeatable); defaults to the whole role")
	cmd.Flags().StringArrayVar(&serviceIDs, "service-id", nil,
		"Task selector: restrict the token to a service ID (repeatable)")
	cmd.Flags().StringArrayVar(&taskIDs, "task-id", nil,
		"Task selector: restrict the token to a task ID (repeatable)")
	cmd.Flags().StringArrayVar(&labels, "selector-label", nil,
		"Task selector: require a service label, as k=v (repeatable)")
	cmd.Flags().StringVar(&expiresAt, "expires-at", "",
		"Expiry as an RFC3339 timestamp, for example 2026-12-01T00:00:00Z (default: never)")
	return cmd
}

// tokenSelectors turns the repeatable selector flags into a selector set. No
// flag at all means nil, which the token store reads as "no restriction".
func tokenSelectors(serviceIDs, taskIDs, labels []string) (*apihttp.TaskSelectors, error) {
	if len(serviceIDs) == 0 && len(taskIDs) == 0 && len(labels) == 0 {
		return nil, nil
	}

	selectors := &apihttp.TaskSelectors{
		ServiceIDs: serviceIDs,
		TaskIDs:    taskIDs,
	}
	if len(labels) == 0 {
		return selectors, nil
	}

	selectors.Labels = make(map[string]string, len(labels))
	for _, raw := range labels {
		key, value, found := strings.Cut(raw, "=")
		if !found || strings.TrimSpace(key) == "" {
			return nil, output.NewErrorf(output.CodeInvalid,
				"selector label %q must be key=value", raw)
		}
		selectors.Labels[strings.TrimSpace(key)] = value
	}
	return selectors, nil
}

func newAPITokenListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List tokens",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		Long: `List the tokens in the local store.

SCOPES prints "all" when a token still holds every scope its role grants, and
the comma-separated scope list when it was narrowed with --scope. EXPIRES prints
the token's expiry, or "-" when it never expires. Hashes are never printed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The table is the only representation; silently ignoring -o json
			// would hand a caller an unparseable document.
			if outputFormat != "" && output.Format(outputFormat) != output.FormatTable {
				return output.NewErrorf(output.CodeUsage,
					"api token list only supports table output; %q is not supported", outputFormat)
			}

			store, err := openAPITokenStore()
			if err != nil {
				return err
			}

			tokens := store.List()
			w := cmd.OutOrStdout()
			if len(tokens) == 0 {
				fmt.Fprintln(w, "No tokens found")
				return nil
			}

			fmt.Fprintf(w, "%-14s %-24s %-9s %-20s %-10s %s\n",
				"ID", "NAME", "ROLE", "CREATED", "SCOPES", "EXPIRES")
			for _, tok := range tokens {
				// Token carries no usable hash after List; printing the struct's
				// fields explicitly keeps it that way.
				fmt.Fprintf(w, "%-14s %-24s %-9s %-20s %-10s %s\n",
					tok.ID, tok.Name, tok.Role, tok.CreatedAt, formatTokenScopes(tok), formatTokenExpiry(tok.ExpiresAt))
			}
			return nil
		},
	}
}

// formatTokenScopes abbreviates the scope column: a token holding exactly its
// role's set prints "all", a narrowed one prints its scopes.
func formatTokenScopes(tok apihttp.Token) string {
	roleScopes, err := apihttp.RoleScopes(tok.Role)
	if err == nil && sameScopeSet(roleScopes, tok.Scopes) {
		return "all"
	}

	return strings.Join(tok.Scopes, ",")
}

// sameScopeSet compares two unique scope sets regardless of order, so
// `--scope` flags given in a different order still read as the full role.
func sameScopeSet(roleScopes []apihttp.Scope, scopes []string) bool {
	if len(roleScopes) != len(scopes) {
		return false
	}

	have := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		have[scope] = true
	}
	for _, scope := range roleScopes {
		if !have[string(scope)] {
			return false
		}
	}
	return true
}

// formatTokenExpiry prints the expiry, or "-" for a token that never expires.
func formatTokenExpiry(expiresAt string) string {
	if expiresAt == "" {
		return "-"
	}
	return expiresAt
}

func newAPITokenRevokeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke a token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openAPITokenStore()
			if err != nil {
				return err
			}

			if err := store.Revoke(args[0]); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Revoked %s\n", args[0])
			return nil
		},
	}
}

// apiTokenStorePath resolves the token file: --token-file wins, otherwise the
// file sits next to the manager's other API state.
func apiTokenStorePath() string {
	if apiTokenFile != "" {
		return apiTokenFile
	}
	return filepath.Join(client.DefaultConfig().StateDir, "api", "tokens.json")
}

func openAPITokenStore() (*apihttp.TokenStore, error) {
	return apihttp.OpenTokenStore(apiTokenStorePath())
}
