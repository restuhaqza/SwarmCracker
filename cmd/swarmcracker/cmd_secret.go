package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/spf13/cobra"
)

// newSecretCommand creates the secret command group (SwarmKit secrets).
func newSecretCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage secrets",
		Long: `Manage SwarmKit secrets.

A secret is a blob of data (up to 500KB) that is injected into a microVM's
rootfs at the path a service requests with 'service create --secret'. Secret
values are never shown by 'inspect'.`,
	}

	cmd.AddCommand(newSecretCreateCommand())
	cmd.AddCommand(newSecretListCommand())
	cmd.AddCommand(newSecretInspectCommand())
	cmd.AddCommand(newSecretRemoveCommand())

	return cmd
}

func newSecretCreateCommand() *cobra.Command {
	var labels []string
	cmd := &cobra.Command{
		Use:   "create <name> <file>",
		Short: "Create a secret from a file",
		Long:  `Create a secret from a file. Use '-' to read the value from stdin.`,
		Example: `  swarmcracker secret create db-pass ./password.txt
  cat cert.pem | swarmcracker secret create tls-cert -`,
		Args: cobra.ExactArgs(2),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return createSecret(args[0], args[1], labels)
		},
	}
	cmd.Flags().StringArrayVarP(&labels, "label", "l", nil, "Secret labels (key=value)")
	return cmd
}

func createSecret(name, path string, labels []string) error {
	data, err := readSecretData(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.CreateSecret(ctx, &api.CreateSecretRequest{
		Spec: &api.SecretSpec{
			Annotations: api.Annotations{Name: name, Labels: parseLabels(labels)},
			Data:        data,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create secret: %w", err)
	}
	fmt.Printf("Secret %s created with ID: %s\n", name, resp.Secret.ID)
	return nil
}

// readSecretData reads a secret/config payload from a file, or stdin for "-".
func readSecretData(path string) ([]byte, error) {
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("failed to read stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	return data, nil
}

func newSecretListCommand() *cobra.Command {
	var (
		format string
		quiet  bool
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List secrets",
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listSecrets(format, quiet)
		},
	}
	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only display IDs")
	return cmd
}

func listSecrets(format string, quiet bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.ListSecrets(ctx, &api.ListSecretsRequest{})
	if err != nil {
		return fmt.Errorf("failed to list secrets: %w", err)
	}

	if quiet {
		for _, s := range resp.Secrets {
			fmt.Println(s.ID)
		}
		return nil
	}

	if format == "json" {
		views := make([]secretView, 0, len(resp.Secrets))
		for _, s := range resp.Secrets {
			views = append(views, secretView{
				ID:     s.ID,
				Name:   s.Spec.Annotations.Name,
				Labels: s.Spec.Annotations.Labels,
			})
		}
		return printJSON(views)
	}

	if len(resp.Secrets) == 0 {
		fmt.Println("No secrets found")
		return nil
	}
	fmt.Printf("%-20s %s\n", "ID", "NAME")
	fmt.Println(strings.Repeat("-", 44))
	for _, s := range resp.Secrets {
		id := s.ID
		if len(id) > 12 {
			id = id[:12]
		}
		fmt.Printf("%-20s %s\n", id, s.Spec.Annotations.Name)
	}
	fmt.Printf("\nTotal: %d secret(s)\n", len(resp.Secrets))
	return nil
}

func newSecretInspectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect <name|id>",
		Short: "Inspect a secret (value is never shown)",
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return inspectSecret(args[0])
		},
	}
	return cmd
}

func inspectSecret(ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	id, err := resolveSecretRef(ctx, client, ref)
	if err != nil {
		return err
	}
	resp, err := client.GetSecret(ctx, &api.GetSecretRequest{SecretID: id})
	if err != nil {
		return fmt.Errorf("failed to get secret: %w", err)
	}
	// Never print the payload.
	view := secretView{
		ID:     resp.Secret.ID,
		Name:   resp.Secret.Spec.Annotations.Name,
		Labels: resp.Secret.Spec.Annotations.Labels,
	}
	return printJSON(view)
}

func newSecretRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rm <name|id>",
		Aliases: []string{"remove"},
		Short:   "Remove a secret",
		Args:    cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return removeSecret(args[0])
		},
	}
	return cmd
}

func removeSecret(ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	id, err := resolveSecretRef(ctx, client, ref)
	if err != nil {
		return err
	}

	if names, err := servicesUsing(ctx, client, func(c *api.ContainerSpec) []string {
		var ids []string
		for _, s := range c.Secrets {
			ids = append(ids, s.SecretID)
		}
		return ids
	}, id); err != nil {
		return err
	} else if len(names) > 0 {
		return fmt.Errorf("secret is in use by service(s) %s; remove or update them first", strings.Join(names, ", "))
	}

	if _, err := client.RemoveSecret(ctx, &api.RemoveSecretRequest{SecretID: id}); err != nil {
		return fmt.Errorf("failed to remove secret: %w", err)
	}
	fmt.Printf("Secret %s removed\n", ref)
	return nil
}

// secretView is the redacted representation used by list/inspect (no payload).
type secretView struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

// printJSON renders a value as indented JSON.
func printJSON(v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// servicesUsing returns the names of services whose container spec references
// the given dependency ID. extract pulls the referenced IDs out of a container
// spec (secrets or configs).
func servicesUsing(ctx context.Context, client api.ControlClient, extract func(*api.ContainerSpec) []string, id string) ([]string, error) {
	resp, err := client.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list services: %w", err)
	}
	var names []string
	for _, svc := range resp.Services {
		c := svc.Spec.Task.GetContainer()
		if c == nil {
			continue
		}
		for _, dep := range extract(c) {
			if dep == id {
				names = append(names, svc.Spec.Annotations.Name)
				break
			}
		}
	}
	return names, nil
}

// resolveSecretRef maps a full ID, unique ID prefix, or name to a secret ID.
func resolveSecretRef(ctx context.Context, client api.ControlClient, ref string) (string, error) {
	resp, err := client.ListSecrets(ctx, &api.ListSecretsRequest{})
	if err != nil {
		return "", fmt.Errorf("failed to list secrets: %w", err)
	}
	ref = strings.TrimSpace(ref)
	var matches []string
	for _, s := range resp.Secrets {
		if s.ID == ref || s.Spec.Annotations.Name == ref || strings.HasPrefix(s.ID, ref) {
			matches = append(matches, s.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("secret %q not found", ref)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("secret reference %q is ambiguous", ref)
	}
}
