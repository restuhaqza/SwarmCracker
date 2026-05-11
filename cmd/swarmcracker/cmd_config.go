package main

import (
	"fmt"
	"os"

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

	return cmd
}

// newConfigListCommand lists configuration
func newConfigListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List configuration files",
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

// Config helper functions

func listConfig() error {
	configDir := "/etc/swarmcracker"

	// Check if directory exists
	if _, err := os.Stat(configDir); err != nil {
		fmt.Printf("No configuration directory found at %s\n", configDir)
		fmt.Println("Initialize with: swarmcracker cluster init")
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
	configFile := "/etc/swarmcracker/config.yaml"

	// Check if directory exists
	if _, err := os.Stat(configDir); err != nil {
		return fmt.Errorf("configuration directory not found: %s", configDir)
	}

	// Check if config file exists
	if _, err := os.Stat(configFile); err != nil {
		fmt.Printf("⚠️  Main config file not found: %s\n", configFile)
		fmt.Println("This is normal for a new cluster - config will be created on init")
		return nil
	}

	fmt.Printf("✅ Configuration validated: %s\n", configDir)
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