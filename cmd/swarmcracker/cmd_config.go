package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/spf13/cobra"
)

// newConfigCommand creates the config command group (SwarmKit configs).
func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configs",
		Long: `Manage SwarmKit configs.

A config is a blob of data (up to 500KB) that is injected into a microVM's
rootfs at the path a service requests with 'service create --config'. Config
values are never shown by 'inspect'.

To manage the SwarmCracker daemon configuration file, use 'config file'.`,
	}

	cmd.AddCommand(newConfigCreateCommand())
	cmd.AddCommand(newConfigListCommand())
	cmd.AddCommand(newConfigInspectCommand())
	cmd.AddCommand(newConfigRemoveCommand())
	cmd.AddCommand(newConfigFileCommand())

	return cmd
}

func newConfigCreateCommand() *cobra.Command {
	var labels []string
	cmd := &cobra.Command{
		Use:   "create <name> <file>",
		Short: "Create a config from a file",
		Long:  `Create a config from a file. Use '-' to read the value from stdin.`,
		Example: `  swarmcracker config create app-yaml ./app.yaml
  cat nginx.conf | swarmcracker config create nginx -`,
		Args: cobra.ExactArgs(2),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return createConfig(args[0], args[1], labels)
		},
	}
	cmd.Flags().StringArrayVarP(&labels, "label", "l", nil, "Config labels (key=value)")
	return cmd
}

func createConfig(name, path string, labels []string) error {
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

	resp, err := client.CreateConfig(ctx, &api.CreateConfigRequest{
		Spec: &api.ConfigSpec{
			Annotations: api.Annotations{Name: name, Labels: parseLabels(labels)},
			Data:        data,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create config: %w", err)
	}
	fmt.Printf("Config %s created with ID: %s\n", name, resp.Config.ID)
	return nil
}

func newConfigListCommand() *cobra.Command {
	var (
		format string
		quiet  bool
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List configs",
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listConfigs(format, quiet)
		},
	}
	cmd.Flags().StringVar(&format, "format", "table", "Output format (table, json)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only display IDs")
	return cmd
}

func listConfigs(format string, quiet bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.ListConfigs(ctx, &api.ListConfigsRequest{})
	if err != nil {
		return fmt.Errorf("failed to list configs: %w", err)
	}

	if quiet {
		for _, c := range resp.Configs {
			fmt.Println(c.ID)
		}
		return nil
	}

	if format == "json" {
		views := make([]configView, 0, len(resp.Configs))
		for _, c := range resp.Configs {
			views = append(views, configView{
				ID:     c.ID,
				Name:   c.Spec.Annotations.Name,
				Labels: c.Spec.Annotations.Labels,
				Size:   len(c.Spec.Data),
			})
		}
		return printJSON(views)
	}

	if len(resp.Configs) == 0 {
		fmt.Println("No configs found")
		return nil
	}
	fmt.Printf("%-20s %-24s %s\n", "ID", "NAME", "SIZE")
	fmt.Println(strings.Repeat("-", 60))
	for _, c := range resp.Configs {
		id := c.ID
		if len(id) > 12 {
			id = id[:12]
		}
		fmt.Printf("%-20s %-24s %dB\n", id, c.Spec.Annotations.Name, len(c.Spec.Data))
	}
	fmt.Printf("\nTotal: %d config(s)\n", len(resp.Configs))
	return nil
}

func newConfigInspectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect <name|id>",
		Short: "Inspect a config (value is never shown)",
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return inspectConfig(args[0])
		},
	}
	return cmd
}

func inspectConfig(ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	id, err := resolveConfigRef(ctx, client, ref)
	if err != nil {
		return err
	}
	resp, err := client.GetConfig(ctx, &api.GetConfigRequest{ConfigID: id})
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}
	view := configView{
		ID:     resp.Config.ID,
		Name:   resp.Config.Spec.Annotations.Name,
		Labels: resp.Config.Spec.Annotations.Labels,
		Size:   len(resp.Config.Spec.Data),
	}
	return printJSON(view)
}

func newConfigRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rm <name|id>",
		Aliases: []string{"remove"},
		Short:   "Remove a config",
		Args:    cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return removeConfig(args[0])
		},
	}
	return cmd
}

func removeConfig(ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, conn, err := getSwarmClientForService()
	if err != nil {
		return err
	}
	defer conn.Close()

	id, err := resolveConfigRef(ctx, client, ref)
	if err != nil {
		return err
	}

	if names, err := servicesUsing(ctx, client, func(c *api.ContainerSpec) []string {
		var ids []string
		for _, cf := range c.Configs {
			ids = append(ids, cf.ConfigID)
		}
		return ids
	}, id); err != nil {
		return err
	} else if len(names) > 0 {
		return fmt.Errorf("config is in use by service(s) %s; remove or update them first", strings.Join(names, ", "))
	}

	if _, err := client.RemoveConfig(ctx, &api.RemoveConfigRequest{ConfigID: id}); err != nil {
		return fmt.Errorf("failed to remove config: %w", err)
	}
	fmt.Printf("Config %s removed\n", ref)
	return nil
}

// configView is the redacted representation used by list/inspect (no payload).
type configView struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Size   int               `json:"size_bytes"`
}

// resolveConfigRef maps a full ID, unique ID prefix, or name to a config ID.
func resolveConfigRef(ctx context.Context, client api.ControlClient, ref string) (string, error) {
	resp, err := client.ListConfigs(ctx, &api.ListConfigsRequest{})
	if err != nil {
		return "", fmt.Errorf("failed to list configs: %w", err)
	}
	ref = strings.TrimSpace(ref)
	var matches []string
	for _, c := range resp.Configs {
		if c.ID == ref || c.Spec.Annotations.Name == ref || strings.HasPrefix(c.ID, ref) {
			matches = append(matches, c.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("config %q not found", ref)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("config reference %q is ambiguous", ref)
	}
}
