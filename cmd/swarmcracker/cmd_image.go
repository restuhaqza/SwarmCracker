package main

import (
	"github.com/spf13/cobra"
)

// newImageCommand creates the "image" command group, which is now a deprecated
// alias for "template": the artifact a golden VM boots from is a sealed,
// versioned template, and that is the name the rest of the system uses for it.
//
// The commands themselves are the same ones, so there is one implementation of
// each and no behaviour that only the old spelling has.
func newImageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Build and inspect golden VM images (deprecated: use 'template')",
		Long: `Build and inspect golden VM images.

Deprecated: a golden image is a sealed, versioned template, and these commands
are now 'template build', 'template list' and 'template inspect'.`,
	}
	cmd.AddCommand(wrapDeprecated(newTemplateBuildCommand(), "template build"))
	cmd.AddCommand(wrapDeprecated(newTemplateListCommand(), "template list"))
	cmd.AddCommand(wrapDeprecated(newTemplateInspectCommand(), "template inspect"))
	return cmd
}
