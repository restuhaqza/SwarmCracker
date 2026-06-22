package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newVersionCommand creates the version command.
func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Long:  `Display detailed version information about the SwarmCracker CLI.`,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("SwarmCracker %s\n", Version)
			fmt.Printf("  Build Time: %s\n", BuildTime)
			fmt.Printf("  Git Commit: %s\n", GitCommit)
			fmt.Printf("  Go Version: %s\n", goVersion())
		},
	}
}
