// Package image provides OCI image validation for Firecracker VMs.
package image

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/rs/zerolog/log"
)

// Architecture aliases for normalization.
// Different registries/manifests may use different names for the same architecture.
var archAliases = map[string]string{
	"x86_64":  "amd64",
	"amd64":   "amd64",
	"aarch64": "arm64",
	"arm64":   "arm64",
	"armhf":   "arm",
	"armv7l":  "arm",
	"i386":    "386",
	"i686":    "386",
	"x86":     "386",
}

// normalizeArch normalizes architecture names to Go's runtime.GOARCH format.
func normalizeArch(arch string) string {
	arch = strings.ToLower(strings.TrimSpace(arch))
	if normalized, ok := archAliases[arch]; ok {
		return normalized
	}
	return arch
}

// validateImageManifest validates that an image is compatible with Firecracker.
// It checks:
// 1. OS is "linux" (rejects Windows containers)
// 2. Architecture matches the host (with alias normalization)
//
// Returns nil if validation passes, or an error describing the incompatibility.
// Returns nil with a warning log if manifest cannot be read (graceful degradation).
func validateImageManifest(ctx context.Context, imageRef string, opts ...remote.Option) error {
	if imageRef == "" {
		return fmt.Errorf("image reference must not be empty")
	}

	// Ensure image ref has docker.io prefix for standard images
	fullRef := imageRef
	if !strings.Contains(fullRef, "/") {
		fullRef = "docker.io/library/" + fullRef
	} else if !strings.Contains(fullRef, ".") {
		fullRef = "docker.io/" + fullRef
	}

	// Parse the image reference
	ref, err := name.ParseReference(fullRef)
	if err != nil {
		return fmt.Errorf("failed to parse image reference %q: %w", fullRef, err)
	}

	// Get the manifest
	img, err := remote.Image(ref, opts...)
	if err != nil {
		// Graceful degradation: log warning and return nil
		// This allows the system to work with images that may not support
		// manifest inspection (e.g., legacy registries, air-gapped environments)
		log.Warn().
			Err(err).
			Str("image", fullRef).
			Msg("Unable to read image manifest, skipping validation (graceful degradation)")
		return nil
	}

	// Get manifest config
	cfg, err := img.ConfigFile()
	if err != nil {
		// Graceful degradation
		log.Warn().
			Err(err).
			Str("image", fullRef).
			Msg("Unable to read image config, skipping validation (graceful degradation)")
		return nil
	}

	if cfg == nil || cfg.Architecture == "" {
		// Graceful degradation
		log.Warn().
			Str("image", fullRef).
			Msg("Image config missing architecture info, skipping validation")
		return nil
	}

	// Validate OS
	imageOS := strings.ToLower(strings.TrimSpace(cfg.OS))
	if imageOS != "linux" {
		return fmt.Errorf("incompatible image OS: image is %q, but Firecracker requires Linux containers", imageOS)
	}

	// Validate Architecture with normalization
	imageArch := normalizeArch(cfg.Architecture)
	hostArch := normalizeArch(runtime.GOARCH)

	log.Debug().
		Str("image", fullRef).
		Str("image_arch_raw", cfg.Architecture).
		Str("image_arch_normalized", imageArch).
		Str("host_arch", hostArch).
		Msg("Validating image architecture")

	if imageArch != hostArch {
		return fmt.Errorf("incompatible image architecture: image is %q (normalized: %q), but host is %q",
			cfg.Architecture, imageArch, hostArch)
	}

	log.Info().
		Str("image", fullRef).
		Str("os", imageOS).
		Str("arch", imageArch).
		Msg("Image validation passed")

	return nil
}
