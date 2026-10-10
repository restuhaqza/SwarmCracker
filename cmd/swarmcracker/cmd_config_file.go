package main

import (
	"fmt"
	"os"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/spf13/cobra"
)

// newConfigFileCommand manages the SwarmCracker daemon configuration file.
// SwarmKit configs live under the parent 'config' command.
func newConfigFileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "file",
		Short: "Manage the daemon configuration file",
		Long:  `View, validate, and migrate the SwarmCracker daemon configuration file.`,
	}

	cmd.AddCommand(newConfigFileListCommand())
	cmd.AddCommand(newConfigFileValidateCommand())
	cmd.AddCommand(newConfigFileMigrateCommand())

	return cmd
}

// newConfigFileListCommand lists configuration files.
func newConfigFileListCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Short:   "List configuration files",
		Aliases: []string{"list"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return listConfig()
		},
	}
}

// newConfigFileValidateCommand validates configuration.
func newConfigFileValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate configuration files",
		Long: `Validate a SwarmCracker configuration file.

With no argument it validates the default config (or the file given with
--config). Passing a path validates that file instead.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			return validateConfig(path)
		},
	}
}

// newConfigFileMigrateCommand migrates configuration between schema versions.
func newConfigFileMigrateCommand() *cobra.Command {
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

func validateConfig(argPath string) error {
	explicit := argPath != ""
	configFile := argPath
	if configFile == "" {
		if cfgFile != "" {
			configFile = cfgFile
			explicit = true
		} else {
			configFile = config.GetDefaultConfigPath()
		}
	}

	// Check if config file exists (a missing default config is OK for a new
	// cluster; an explicitly requested path must exist).
	if _, statErr := os.Stat(configFile); statErr != nil {
		if os.IsNotExist(statErr) {
			if explicit {
				return fmt.Errorf("config file not found: %s", configFile)
			}
			fmt.Printf("⚠️  Main config file not found: %s\n", configFile)
			fmt.Println("This is normal for a new cluster — config will be created on init")
			return nil
		}
		return fmt.Errorf("cannot access config file %s: %w", configFile, statErr)
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
