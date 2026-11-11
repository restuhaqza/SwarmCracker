package golden

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
)

// Artifact is a built golden image resolved from a store directory.
type Artifact struct {
	// Ref is the canonical "name@version" reference.
	Ref string
	// Name and Version identify the recipe.
	Name    string
	Version string
	// Path is the ext4 artifact on the host.
	Path string
	// Metadata is the artifact sidecar.
	Metadata *ArtifactMetadata
}

// PinSource reports this node's pin for a template that names kernelProfile: the
// Firecracker version this node runs and the SHA-256 of the kernel image it
// would boot that template with.
//
// It is a function of the profile rather than a value because the profile lives
// in the manifest: nothing knows which kernel to hash until the manifest has
// been read, so Resolve reads it and asks afterwards. It is a parameter rather
// than something this package reads on its own because only the caller holds
// the node's configuration, and a pin that did not come from the node that is
// about to boot the template would prove nothing.
type PinSource func(ctx context.Context, kernelProfile string) (checkpoint.Pin, error)

// NodeHostPin builds the PinSource of a node that runs firecrackerBinary and
// boots a template through profiles, falling back to kernelPath for a profile
// it does not map.
//
// That is the rule the node's own boot path applies to a golden task (a mapped
// profile is the kernel; an unmapped one gets the default kernel —
// taskTranslatorImpl.resolveKernelPath), stated once here so the pin check asks
// the question the boot will ask. A template is therefore held to the kernel it
// would really be booted with, not to one this build resolved separately.
//
// An empty firecrackerBinary is DefaultFirecrackerBinary on PATH, and an empty
// kernelPath is reported by NodePin as a node with no kernel to pin: a node
// that cannot name the software it runs cannot be compared with a template's
// pin, and refusing says so rather than guessing.
func NodeHostPin(firecrackerBinary, kernelPath string, profiles map[string]string) PinSource {
	return func(ctx context.Context, kernelProfile string) (checkpoint.Pin, error) {
		kernel := strings.TrimSpace(profiles[strings.TrimSpace(kernelProfile)])
		if kernel == "" {
			kernel = kernelPath
		}
		return NodePin(ctx, firecrackerBinary, kernel)
	}
}

// Resolve finds a built artifact in storeDir by "name" (newest version) or
// "name@version". It verifies both the metadata sidecar and the artifact exist.
//
// It also answers the two questions a node asks before it uses a template, and
// refuses the template rather than returning it when either answer is no:
//
//   - "Can I drive the agent inside it?" — the agent pin, checked with
//     CheckAgentPin.
//   - "Was it built by software I run?" — the pin, checked against the pin
//     pinSource reports for the kernel profile the template names, with
//     ArtifactMetadata.VerifyPin.
//
// This is the only path to a template, so there is no way around either check:
// nothing else in this build turns a template reference into something a VM can
// be started from. A template built on other host software is refused here, with
// *PinMismatchError carrying checkpoint.ErrPinMismatch and the stable code
// template_pin_mismatch, before its rootfs is materialized and before any
// Firecracker process exists — the failure M4 exists for.
//
// pinSource is required, and a nil one is refused rather than treated as "skip the
// check": a resolve that cannot say what software this node runs cannot say
// whether the template may be used, and returning a template nobody checked is
// the mistake this gate exists to remove.
func Resolve(ctx context.Context, storeDir, ref string, pinSource PinSource) (*Artifact, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("golden: empty image reference")
	}

	name, version := ref, ""
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		name, version = ref[:i], ref[i+1:]
	}
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("golden: invalid image name %q", name)
	}
	if version != "" && !versionPattern.MatchString(version) {
		return nil, fmt.Errorf("golden: invalid version %q", version)
	}

	var metadataPath string
	if version != "" {
		metadataPath = filepath.Join(storeDir, ArtifactBaseName(name, version)+".json")
		if _, err := os.Stat(metadataPath); err != nil {
			return nil, fmt.Errorf("golden: image %s@%s not found in %s", name, version, storeDir)
		}
	} else {
		matches, err := filepath.Glob(filepath.Join(storeDir, "golden-"+name+"-*.json"))
		if err != nil {
			return nil, fmt.Errorf("golden: glob %s: %w", storeDir, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("golden: image %s not found in %s", name, storeDir)
		}
		sort.Slice(matches, func(i, j int) bool {
			fi, errI := os.Stat(matches[i])
			fj, errJ := os.Stat(matches[j])
			if errI != nil || errJ != nil {
				return matches[i] < matches[j]
			}
			return fi.ModTime().After(fj.ModTime())
		})
		metadataPath = matches[0]
	}

	md, err := ReadMetadata(metadataPath)
	if err != nil {
		return nil, err
	}

	artifactPath := filepath.Join(storeDir, md.Artifact)
	if info, err := os.Stat(artifactPath); err != nil {
		return nil, fmt.Errorf("golden: artifact %s for %s@%s is missing: %w", artifactPath, md.Name, md.Version, err)
	} else if info.Size() == 0 {
		return nil, fmt.Errorf("golden: artifact %s for %s@%s is empty", artifactPath, md.Name, md.Version)
	}

	// Refuse a template this host cannot drive rather than booting an agent that
	// cannot advertise an operation the host relies on.
	if err := CheckAgentPin(md.Name+"@"+md.Version, md.Agent); err != nil {
		return nil, err
	}
	// And refuse one this host's software did not build, before the artifact is
	// copied anywhere: a template made by another Firecracker version or another
	// kernel is a VM that fails to resume, or resumes as another machine.
	if err := verifyHostPin(ctx, md, pinSource); err != nil {
		return nil, err
	}

	return &Artifact{
		Ref:      md.Name + "@" + md.Version,
		Name:     md.Name,
		Version:  md.Version,
		Path:     artifactPath,
		Metadata: md,
	}, nil
}

// verifyHostPin holds a template to the software this node runs.
//
// The two failures are told apart on purpose. A node that runs other software
// than the template was built on is a *PinMismatchError, the same class a
// checkpoint reports and the same stable code the build path refuses with. A
// node that cannot say what it runs — no kernel mapped for the template's
// profile, or a Firecracker binary that does not answer — is not a mismatch but
// an unanswerable question, and it is reported as itself: an operator who reads
// "cannot be checked" goes looking for the kernel, and one who reads "pinned to
// v1.15.1, this node runs v1.16.0" goes looking for the template.
func verifyHostPin(ctx context.Context, md *ArtifactMetadata, pinSource PinSource) error {
	if pinSource == nil {
		return fmt.Errorf(
			"golden: %s cannot be checked against this node: a resolve must be told how this node reads its own Firecracker version and kernel, so that a template built on other software is refused rather than booted",
			md.Ref())
	}
	pin, err := pinSource(ctx, md.KernelProfile)
	if err != nil {
		return fmt.Errorf(
			"golden: %s cannot be checked against this node, so it is not used here: %w",
			md.Ref(), err)
	}
	return md.VerifyPin(pin)
}
