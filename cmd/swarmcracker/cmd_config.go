package main

import (
	"fmt"
	"os"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/spf13/cobra"
)

// newConfigCommand creates the config command group
func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage SwarmCracker configuration",
		Long: `Manage SwarmCracker configuration files and settings.

Provides commands for viewing, validating, and managing configuration.`,
	}

	// Add subcommands
	cmd.AddCommand(newConfigListCommand())
	cmd.AddCommand(newConfigValidateCommand())
	cmd.AddCommand(newConfigMigrateCommand())

	return cmd
}

// newConfigListCommand lists configuration
func newConfigListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Short:   "List configuration files",
		Aliases: []string{"list"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listConfig()
		},
	}
}

// newConfigValidateCommand validates configuration
func newConfigValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration files",
		RunE: func(cmd *cobra.Command, args []string) error {
			return validateConfig()
		},
	}
}

// newConfigMigrateCommand migrates configuration between schema versions
func newConfigMigrateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Migrate configuration to latest schema version",
		Long: `Migrate the SwarmCracker configuration file to the latest schema version.

This is needed when upgrading SwarmCracker to a version that introduces
new config fields or changes existing ones. The original config is backed
up before migration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigMigrate()
		},
	}
}

// Config helper functions

func listConfig() error {
	configDir := "/etc/swarmcracker"

	// Check if directory exists
	if _, err := os.Stat(configDir); err != nil {
		fmt.Printf("No configuration directory found at %s\n", configDir)
		fmt.Println("Initialize with: swarmcracker cluster init")
		//nolint:nilerr
		return nil
	}

	// List config files
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return err
	}

	fmt.Printf("Configuration directory: %s\n", configDir)
	fmt.Printf("\nFiles:\n")
	for _, entry := range entries {
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			fmt.Printf("  %s (%.2f KB)\n", entry.Name(), float64(info.Size())/1024)
		}
	}

	return nil
}

func validateConfig() error {
	configDir := "/etc/swarmcracker"
	configFile := config.GetDefaultConfigPath()

	// Check if directory exists
	if _, err := os.Stat(configDir); err != nil {
		return fmt.Errorf("configuration directory not found: %s", configDir)
	}

	// Check if config file exists
	if _, err := os.Stat(configFile); err != nil {
		fmt.Printf("⚠️  Main config file not found: %s\n", configFile)
		fmt.Println("This is normal for a new cluster — config will be created on init")
		return nil
	}

	// Actually load and validate
	cfg, err := config.LoadConfig(configFile)
	if err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}

	fmt.Printf("✅ Configuration valid (version %d): %s\n", cfg.Version, configFile)
	return nil
}

func runConfigMigrate() error {
	cfgPath := config.GetDefaultConfigPath()

	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		fmt.Println("No config file to migrate.")
		fmt.Printf("Run 'swarmcracker setup config' to create one at %s\n", cfgPath)
		return nil
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	fmt.Printf("Current config version: %d\n", cfg.Version)

	if cfg.Version >= 1 {
		fmt.Println("✅ Config is already at the latest version — no migration needed")
		return nil
	}

	// Future: add actual migration logic when schema changes
	// For now, just rewrite with version=1
	cfg.Version = 1

	// Backup original
	backupPath := cfgPath + ".backup"
	data, _ := os.ReadFile(cfgPath)
	if err := os.WriteFile(backupPath, data, 0600); err != nil {
		return fmt.Errorf("failed to backup config: %w", err)
	}
	fmt.Printf("Backup saved to %s\n", backupPath)

	// Save migrated
	if err := cfg.Save(cfgPath); err != nil {
		return fmt.Errorf("failed to save migrated config: %w", err)
	}

	fmt.Printf("✅ Config migrated to version %d\n", cfg.Version)
	return nil
}

// runDoctorNetwork runs network diagnostics
func runDoctorNetwork() error {
	// Delegate to doctor command
	fmt.Println("Running network diagnostics...")

	// Run bridge check
	checkDoctorBridgeModule()

	// Run VXLAN check
	checkDoctorBridgeIface()

	fmt.Println("\nUse 'swarmcracker doctor' for full diagnostics")
	return nil
}
