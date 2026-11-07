package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/golden"
	"github.com/spf13/cobra"
)

// newTemplateCommand creates the "template" command group: the sealed,
// versioned templates a golden VM boots from.
//
// It replaces the older "image" group, which is kept as a deprecated alias for
// the same three commands.
func newTemplateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Build, list and inspect sealed VM templates",
		Long: `Build, list and inspect sealed, versioned VM templates.

A template is built from a recipe (see recipes/*.yaml) and sealed before it is
packed: the machine identity, the SSH host keys, the shell history, the build's
own logs and any runtime state that claims a service is already running are
removed, and the build fails rather than recording a template that cannot be
shown to be clean.

A build also pins the template to the host software that produced it — the
Firecracker version and the kernel image — and refuses to record one whose guest
agent never answered. 'template inspect' prints the manifest, including that pin
and the agent version, and says whether this node is pinned to the same
software.`,
	}
	cmd.AddCommand(newTemplateBuildCommand())
	cmd.AddCommand(newTemplateListCommand())
	cmd.AddCommand(newTemplateInspectCommand())
	return cmd
}

// defaultGoldenDir is where built templates live: beside the rootfs directory
// the node already uses, or the standard location when none was given.
func defaultGoldenDir() string {
	if rootfsDir != "" {
		return filepath.Join(rootfsDir, "golden")
	}
	return "/var/lib/firecracker/golden"
}

func newTemplateBuildCommand() *cobra.Command {
	var outputDir string
	var force bool
	cmd := &cobra.Command{
		Use:   "build <recipe.yaml>",
		Short: "Build and seal a template from a recipe",
		Long: `Build and seal a template from a recipe.

The build extracts the recipe's source, provisions it, installs and enables the
guest agent for the recipe's init system, applies the recipe's own seal steps and
then the standard seal, verifies the result in the image, packs the root
filesystem and boots a copy of it to hold its guest agent to the v2 handshake.

Every one of those checks is a build failure, not a warning: a template whose
agent is missing or not enabled, whose image still carries an identity, or whose
host software does not match the recipe's own pin is not recorded at all.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTemplateBuild(cmd, args[0], outputDir, force)
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", defaultGoldenDir(), "Directory for built templates")
	cmd.Flags().BoolVar(&force, "force", false, "Rebuild even if an up-to-date template exists")
	return cmd
}

func runTemplateBuild(cmd *cobra.Command, recipePath, outputDir string, force bool) error {
	recipe, err := golden.LoadRecipe(recipePath)
	if err != nil {
		return err
	}

	builder, err := golden.NewBuilder(golden.BuilderOptions{
		OutputDir: outputDir,
		Force:     force,
	})
	if err != nil {
		return err
	}

	res, err := builder.Build(cmd.Context(), recipe)
	if err != nil {
		return err
	}
	if res.Skipped {
		fmt.Printf("Reused %s@%s (up to date)\n", res.Name, res.Version)
	} else {
		fmt.Printf("Built %s@%s\n", res.Name, res.Version)
	}
	fmt.Printf("  artifact: %s (%.1f MiB)\n", res.ArtifactPath, float64(res.SizeBytes)/(1024*1024))
	fmt.Printf("  manifest: %s\n", res.MetadataPath)
	fmt.Printf("  digest:   %s\n", res.Digest)
	fmt.Printf("  sha256:   %s\n", res.SHA256)

	// The pin and the agent version are reported from the manifest the build
	// wrote, not from the recipe: a build that recorded nothing, or recorded
	// something other than what its guest answered with, must not be reported
	// as a success.
	md, err := golden.ReadMetadata(res.MetadataPath)
	if err != nil {
		return fmt.Errorf("template %s@%s was built but its manifest cannot be read: %w", res.Name, res.Version, err)
	}
	if err := golden.CheckAgentPin(md.Ref(), md.Agent); err != nil {
		return fmt.Errorf("template %s@%s was built with an agent this host cannot drive: %w", res.Name, res.Version, err)
	}
	fmt.Printf("  pin:      firecracker %s, kernel %s\n", md.Pin.Firecracker, md.Pin.Kernel)
	fmt.Printf("  base:     %s\n", orNone(md.BaseImageDigest))
	fmt.Printf("  agent:    protocol %s, version %s, caps [%s]\n",
		md.Agent.Protocol, md.Agent.Version, strings.Join(md.Agent.Caps, " "))
	return nil
}

func newTemplateListCommand() *cobra.Command {
	var outputDir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List built templates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTemplateList(outputDir)
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", defaultGoldenDir(), "Directory containing built templates")
	return cmd
}

func runTemplateList(outputDir string) error {
	paths, err := filepath.Glob(filepath.Join(outputDir, "golden-*.json"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		fmt.Printf("No templates found in %s\n", outputDir)
		return nil
	}

	type row struct{ name, version, kernel, runtime, agent, size string }
	rows := make([]row, 0, len(paths))
	for _, p := range paths {
		md, err := golden.ReadMetadata(p)
		if err != nil {
			// A manifest that cannot be read is reported rather than skipped:
			// silently leaving a template out of the listing is how an
			// unusable one gets used.
			fmt.Printf("%-32s (unreadable manifest %s: %v)\n", filepath.Base(p), p, err)
			continue
		}
		rows = append(rows, row{
			name:    md.Name,
			version: md.Version,
			kernel:  md.KernelProfile,
			runtime: md.Runtime.Name,
			agent:   fmt.Sprintf("%s@%s", orNone(md.Agent.Protocol), orNone(md.Agent.Version)),
			size:    fmt.Sprintf("%.0f MiB", float64(md.SizeBytes)/(1024*1024)),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].name != rows[j].name {
			return rows[i].name < rows[j].name
		}
		return rows[i].version < rows[j].version
	})

	fmt.Printf("%-32s %-12s %-20s %-10s %-16s %s\n", "NAME", "VERSION", "KERNEL", "RUNTIME", "AGENT", "SIZE")
	for _, r := range rows {
		fmt.Printf("%-32s %-12s %-20s %-10s %-16s %s\n", r.name, r.version, r.kernel, r.runtime, r.agent, r.size)
	}
	return nil
}

func newTemplateInspectCommand() *cobra.Command {
	var outputDir string
	cmd := &cobra.Command{
		Use:   "inspect <name@version | path.json>",
		Short: "Show a template's manifest and whether this node can use it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTemplateInspect(cmd, args[0], outputDir)
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", defaultGoldenDir(), "Directory containing built templates")
	return cmd
}

func runTemplateInspect(cmd *cobra.Command, ref, outputDir string) error {
	path, err := metadataPathFor(ref, outputDir)
	if err != nil {
		return err
	}
	md, err := golden.ReadMetadata(path)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(md, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))

	// The artifact beside the manifest is what a boot would use; a manifest
	// without one is a listing of something that is not there.
	artifactPath := filepath.Join(filepath.Dir(path), md.Artifact)
	if _, err := os.Stat(artifactPath); err != nil {
		fmt.Printf("\nwarning: artifact %s is missing\n", artifactPath)
	}

	printTemplateVerdict(cmd, md)
	return nil
}

// printTemplateVerdict says what this node would decide about the template: the
// two checks the build itself made (agent pin, host pin), re-made here against
// this host, because a manifest copied from another node was never checked
// against this one.
func printTemplateVerdict(cmd *cobra.Command, md *golden.ArtifactMetadata) {
	fmt.Printf("\nagent: ")
	if err := golden.CheckAgentPin(md.Ref(), md.Agent); err != nil {
		fmt.Printf("NOT usable here — %v\n", err)
	} else {
		// The capabilities are reported because they are what this node actually
		// checks: two templates can both say "protocol 2" and only one of them can
		// give a restored clone an identity of its own.
		fmt.Printf("protocol %s, version %s, caps [%s] (this host requires protocol %s or newer and [%s])\n",
			md.Agent.Protocol, md.Agent.Version, strings.Join(md.Agent.Caps, " "),
			golden.MinimumAgentProtocol, strings.Join(golden.RequiredAgentCaps, " "))
	}

	fmt.Printf("pin:   ")
	kernel, err := kernelForProfile(md.KernelProfile)
	if err != nil {
		fmt.Printf("cannot be checked — %v\n", err)
		return
	}
	pin, err := golden.NodePin(cmd.Context(), "", kernel)
	if err != nil {
		fmt.Printf("cannot be checked — %v\n", err)
		return
	}
	if err := md.VerifyPin(pin); err != nil {
		fmt.Printf("NOT usable here — %v\n", err)
		return
	}
	fmt.Printf("usable on this node (firecracker %s, kernel %s)\n", pin.Firecracker, pin.Kernel)
}

// kernelForProfile resolves a template's kernel profile to the kernel image
// this node would boot it with, which is what its pin is checked against.
func kernelForProfile(profile string) (string, error) {
	cfg, err := loadConfigWithOverrides(cfgFile)
	if err != nil {
		return "", err
	}
	if kernel := cfg.Executor.KernelProfiles[profile]; kernel != "" {
		return kernel, nil
	}
	if cfg.Executor.KernelPath != "" {
		return cfg.Executor.KernelPath, nil
	}
	return "", fmt.Errorf("this node has no kernel mapped for profile %q and no --kernel to fall back to", profile)
}

// metadataPathFor resolves an inspect argument to a manifest: either a path to a
// .json manifest, or <name>@<version> under outputDir.
func metadataPathFor(ref, outputDir string) (string, error) {
	if strings.HasSuffix(ref, ".json") {
		return ref, nil
	}
	name, version, ok := strings.Cut(ref, "@")
	if !ok {
		return "", fmt.Errorf("reference %q must be <name>@<version> or a path to a .json manifest", ref)
	}
	return filepath.Join(outputDir, golden.ArtifactBaseName(name, version)+".json"), nil
}

// orNone prints a value or "none", so an empty field in a listing is not a blank
// column a reader has to interpret.
func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
