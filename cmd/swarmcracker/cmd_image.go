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

// newImageCommand creates the "image" command group for golden images.
func newImageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Build and inspect golden VM images",
		Long: `Build and inspect golden VM images from recipes.

A golden image is a sealed, versioned root filesystem built from a recipe
(see recipes/*.yaml). Runtime recipes boot their own init and run a container
runtime such as Docker inside the microVM.`,
	}
	cmd.AddCommand(newImageBuildCommand())
	cmd.AddCommand(newImageListCommand())
	cmd.AddCommand(newImageInspectCommand())
	return cmd
}

func defaultGoldenDir() string {
	if rootfsDir != "" {
		return filepath.Join(rootfsDir, "golden")
	}
	return "/var/lib/firecracker/golden"
}

func newImageBuildCommand() *cobra.Command {
	var outputDir string
	var force bool
	cmd := &cobra.Command{
		Use:   "build <recipe.yaml>",
		Short: "Build a golden image from a recipe",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImageBuild(cmd, args[0], outputDir, force)
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", defaultGoldenDir(), "Directory for built artifacts")
	cmd.Flags().BoolVar(&force, "force", false, "Rebuild even if an up-to-date artifact exists")
	return cmd
}

func runImageBuild(cmd *cobra.Command, recipePath, outputDir string, force bool) error {
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
	fmt.Printf("  metadata: %s\n", res.MetadataPath)
	fmt.Printf("  sha256:   %s\n", res.SHA256)
	fmt.Printf("  digest:   %s\n", res.Digest)
	return nil
}

func newImageListCommand() *cobra.Command {
	var outputDir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List built golden images",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImageList(outputDir)
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", defaultGoldenDir(), "Directory containing built artifacts")
	return cmd
}

func runImageList(outputDir string) error {
	paths, err := filepath.Glob(filepath.Join(outputDir, "golden-*.json"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		fmt.Printf("No golden images found in %s\n", outputDir)
		return nil
	}

	type row struct{ name, version, kernel, runtime, size string }
	rows := make([]row, 0, len(paths))
	for _, p := range paths {
		md, err := golden.ReadMetadata(p)
		if err != nil {
			continue
		}
		rows = append(rows, row{
			name:    md.Name,
			version: md.Version,
			kernel:  md.KernelProfile,
			runtime: md.Runtime.Name,
			size:    fmt.Sprintf("%.0f MiB", float64(md.SizeBytes)/(1024*1024)),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].name != rows[j].name {
			return rows[i].name < rows[j].name
		}
		return rows[i].version < rows[j].version
	})

	fmt.Printf("%-32s %-12s %-20s %-10s %s\n", "NAME", "VERSION", "KERNEL", "RUNTIME", "SIZE")
	for _, r := range rows {
		fmt.Printf("%-32s %-12s %-20s %-10s %s\n", r.name, r.version, r.kernel, r.runtime, r.size)
	}
	return nil
}

func newImageInspectCommand() *cobra.Command {
	var outputDir string
	cmd := &cobra.Command{
		Use:   "inspect <name@version | path.json>",
		Short: "Show metadata for a built golden image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImageInspect(args[0], outputDir)
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", defaultGoldenDir(), "Directory containing built artifacts")
	return cmd
}

func runImageInspect(ref, outputDir string) error {
	path := ref
	if !strings.HasSuffix(ref, ".json") {
		name, version, ok := strings.Cut(ref, "@")
		if !ok {
			return fmt.Errorf("reference %q must be <name>@<version> or a path to a .json metadata file", ref)
		}
		path = filepath.Join(outputDir, golden.ArtifactBaseName(name, version)+".json")
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

	// Surface a clear signal when the artifact file is missing.
	artifactPath := filepath.Join(filepath.Dir(path), md.Artifact)
	if _, err := os.Stat(artifactPath); err != nil {
		fmt.Fprintf(os.Stderr, "\nwarning: artifact %s is missing\n", artifactPath)
	}
	return nil
}
