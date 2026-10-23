package main

import (
	"errors"
	"fmt"
	"path/filepath"

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

// newAPICommand groups the programmatic-API operator commands.
func newAPICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Manage the programmatic API",
		Long: `Manage the manager's programmatic API.

Tokens are stored on the manager in <state-dir>/api/tokens.json. A token's
secret is printed once at creation and never again; the file holds only its
hash. Reads against a manager use the global -H, --ca and --token-file flags.`,
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
global --token-file flag names that file; otherwise it is
<state-dir>/api/tokens.json.`,
	}

	cmd.AddCommand(newAPITokenCreateCommand())
	cmd.AddCommand(newAPITokenListCommand())
	cmd.AddCommand(newAPITokenRevokeCommand())
	return cmd
}

func newAPITokenCreateCommand() *cobra.Command {
	var (
		name string
		role string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Mint a token and print its secret once",
		Long: `Mint a token and print its secret to stdout, once.

Store the output somewhere safe: the token file keeps only a hash, so the secret
cannot be recovered later.`,
		Example: `  swarmcracker api token create --name ci --role reader
  swarmcracker --token-file /etc/swarmcracker/api/tokens.json api token create --name ops --role admin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openAPITokenStore()
			if err != nil {
				return err
			}

			secret, _, err := store.Create(name, role)
			if err != nil {
				return err
			}

			// The secret is the only value printed: the hash stays on disk.
			fmt.Fprintln(cmd.OutOrStdout(), secret)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Token name (required)")
	cmd.Flags().StringVar(&role, "role", apihttp.RoleReader, "Token role (reader or admin)")
	return cmd
}

func newAPITokenListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List tokens",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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

			fmt.Fprintf(w, "%-14s %-24s %-8s %s\n", "ID", "NAME", "ROLE", "CREATED")
			for _, tok := range tokens {
				// Token carries no usable hash after List; printing the struct's
				// fields explicitly keeps it that way.
				fmt.Fprintf(w, "%-14s %-24s %-8s %s\n", tok.ID, tok.Name, tok.Role, tok.CreatedAt)
			}
			return nil
		},
	}
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
